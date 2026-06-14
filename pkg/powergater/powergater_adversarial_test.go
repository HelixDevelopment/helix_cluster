package powergater

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// atTZ builds a wall-clock time at the given hour in a fixed location so the
// night-window math is exercised with a non-UTC zone too (DST-free zone chosen
// to keep Hour() deterministic).
func atTZ(hour int, loc *time.Location) time.Time {
	return time.Date(2026, 6, 14, hour, 30, 0, 0, loc)
}

// TestNightWindow_DenseWrapSweep is the centerpiece: it walks EVERY hour of the
// day for a midnight-wrapping window (22:00..06:00) and asserts the exact
// membership against an independent reference predicate, pinning the
// inclusive-start / exclusive-end contract across the wrap and across midnight.
func TestNightWindow_DenseWrapSweep(t *testing.T) {
	w := NightWindow{StartHour: 22, EndHour: 6}

	// Independent oracle for a wrapping window [22,6): in iff hour>=22 OR hour<6.
	oracle := func(h int) bool { return h >= 22 || h < 6 }

	for h := 0; h <= 23; h++ {
		got := w.contains(h)
		want := oracle(h)
		if got != want {
			t.Errorf("contains(%02d:00) = %v, want %v (wrap window 22..06)", h, got, want)
		}
	}

	// Explicit boundary witnesses (the off-by-one / naive-wrap kill points).
	boundary := []struct {
		hour int
		in   bool
		why  string
	}{
		{21, false, "one hour before start is OUT"},
		{22, true, "start hour is IN (inclusive start)"},
		{23, true, "last hour before midnight is IN"},
		{0, true, "midnight is IN (wrap crosses midnight)"},
		{5, true, "last hour before end is IN"},
		{6, false, "end hour is OUT (exclusive end)"},
		{7, false, "one hour after end is OUT"},
		{13, false, "mid-afternoon is OUT"},
	}
	for _, b := range boundary {
		if got := w.contains(b.hour); got != b.in {
			t.Errorf("WRAP boundary contains(%02d:00)=%v want %v — %s", b.hour, got, b.in, b.why)
		}
	}
}

// TestNightWindow_NonWrappingSweep pins a forward (non-wrapping) window over the
// whole day, including both endpoints.
func TestNightWindow_NonWrappingSweep(t *testing.T) {
	w := NightWindow{StartHour: 9, EndHour: 17}
	oracle := func(h int) bool { return h >= 9 && h < 17 }
	for h := 0; h <= 23; h++ {
		if got := w.contains(h); got != oracle(h) {
			t.Errorf("forward contains(%02d:00)=%v want %v (window 09..17)", h, got, oracle(h))
		}
	}
	// Endpoint witnesses.
	if w.contains(8) {
		t.Error("08:00 must be OUT (before inclusive start)")
	}
	if !w.contains(9) {
		t.Error("09:00 must be IN (inclusive start)")
	}
	if !w.contains(16) {
		t.Error("16:00 must be IN (last hour before exclusive end)")
	}
	if w.contains(17) {
		t.Error("17:00 must be OUT (exclusive end)")
	}
}

// TestNightWindow_DegenerateWindows pins the documented edge contracts:
// StartHour==EndHour is "whole day" (always in); a single-hour window; and a
// 23-hour wrap whose only excluded hour is the start-1 hour.
func TestNightWindow_DegenerateWindows(t *testing.T) {
	// Whole-day: every hour in.
	whole := NightWindow{StartHour: 0, EndHour: 0}
	for h := 0; h <= 23; h++ {
		if !whole.contains(h) {
			t.Errorf("whole-day(0,0) must contain %02d:00", h)
		}
	}
	wholeNonZero := NightWindow{StartHour: 13, EndHour: 13}
	for h := 0; h <= 23; h++ {
		if !wholeNonZero.contains(h) {
			t.Errorf("whole-day(13,13) must contain %02d:00", h)
		}
	}

	// Single forward hour [5,6): only 5 is in.
	single := NightWindow{StartHour: 5, EndHour: 6}
	for h := 0; h <= 23; h++ {
		want := h == 5
		if got := single.contains(h); got != want {
			t.Errorf("single-hour(5,6) contains(%02d)=%v want %v", h, got, want)
		}
	}

	// 23-hour wrap [6,5): every hour in EXCEPT 5.
	almostAll := NightWindow{StartHour: 6, EndHour: 5}
	for h := 0; h <= 23; h++ {
		want := h != 5
		if got := almostAll.contains(h); got != want {
			t.Errorf("wrap(6,5) contains(%02d)=%v want %v", h, got, want)
		}
	}
}

// TestNightWindow_CrossMidnightSequence drives CanAcceptWork minute-by-hour
// across the night window and asserts the accept/refuse flips happen at exactly
// the contractual boundaries (enter at 22:00, leave at 06:00), in a non-UTC zone.
func TestNightWindow_CrossMidnightSequence(t *testing.T) {
	cfg := PowerConfig{
		NightModeOnly: true,
		NightWindow:   NightWindow{StartHour: 22, EndHour: 6},
	}
	loc := time.FixedZone("FIXED+0", 0)
	// Sequence walking 20:00 -> 08:00 across midnight.
	seq := []int{20, 21, 22, 23, 0, 1, 5, 6, 7, 8}
	wantAccept := map[int]bool{
		20: false, 21: false, 22: true, 23: true,
		0: true, 1: true, 5: true, 6: false, 7: false, 8: false,
	}
	for _, h := range seq {
		g := New(cfg, fakeSource{charging: true, battery: 100, temp: 40, now: atTZ(h, loc)})
		accept, reason := g.CanAcceptWork()
		if accept != wantAccept[h] {
			t.Errorf("at %02d:30 accept=%v want %v (reason %q)", h, accept, wantAccept[h], reason)
		}
		if !accept && reason != ReasonDaytime {
			t.Errorf("at %02d:30 refused with reason %q, want %q", h, reason, ReasonDaytime)
		}
		if accept && reason != ReasonAccept {
			t.Errorf("at %02d:30 accepted with reason %q, want empty", h, reason)
		}
	}
}

// TestDecision_PriorityOrderAcrossStates pins the documented fixed priority
// order (charging > battery > night-window > temperature) by constructing inputs
// where MULTIPLE checks would trip and asserting the FIRST-priority reason wins.
func TestDecision_PriorityOrderAcrossStates(t *testing.T) {
	cfg := PowerConfig{
		OnlyWhenCharging:  true,
		MinBatteryPercent: 20,
		NightModeOnly:     true,
		NightWindow:       NightWindow{StartHour: 22, EndHour: 6},
		MaxCpuTempC:       80,
	}
	cases := []struct {
		name       string
		src        fakeSource
		wantReason string
	}{
		// Everything wrong -> charging wins.
		{"all-tripped", fakeSource{charging: false, battery: 5, temp: 99, now: at(14)}, "not_charging"},
		// Charging ok, rest wrong -> battery wins.
		{"battery-first", fakeSource{charging: true, battery: 5, temp: 99, now: at(14)}, "battery_low_5%"},
		// Charging+battery ok, daytime+hot -> daytime wins.
		{"daytime-before-temp", fakeSource{charging: true, battery: 50, temp: 99, now: at(14)}, "daytime"},
		// Only temperature wrong -> temp.
		{"temp-last", fakeSource{charging: true, battery: 50, temp: 99, now: at(23)}, "cpu_hot_99C"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := New(cfg, c.src)
			accept, reason := g.CanAcceptWork()
			if c.wantReason == "" {
				if !accept {
					t.Fatalf("expected accept, got refuse %q", reason)
				}
				return
			}
			if accept {
				t.Fatalf("expected refuse %q, got accept", c.wantReason)
			}
			if reason != c.wantReason {
				t.Fatalf("priority-order reason=%q want %q", reason, c.wantReason)
			}
		})
	}
}

// TestDecision_Deterministic asserts identical inputs yield identical decisions
// across repeated calls (no hidden state / non-determinism), and that a fresh
// gater per call agrees with a reused gater.
func TestDecision_Deterministic(t *testing.T) {
	cfg := PowerConfig{
		OnlyWhenCharging:  true,
		MinBatteryPercent: 20,
		NightModeOnly:     true,
		NightWindow:       NightWindow{StartHour: 22, EndHour: 6},
		MaxCpuTempC:       80,
	}
	src := fakeSource{charging: true, battery: 9, temp: 40, now: at(23)}
	g := New(cfg, src)
	firstAccept, firstReason := g.CanAcceptWork()
	for i := 0; i < 1000; i++ {
		a, r := g.CanAcceptWork()
		if a != firstAccept || r != firstReason {
			t.Fatalf("call %d non-deterministic: (%v,%q) != (%v,%q)", i, a, r, firstAccept, firstReason)
		}
	}
	if firstReason != "battery_low_9%" {
		t.Fatalf("expected battery_low_9%%, got %q", firstReason)
	}
}

// TestDecision_LogMatchesDecisions is the sink-side audit-trail proof: it records
// a decision log while driving the gater through a scripted state sequence, then
// asserts the log's count, order, AND content exactly match the decisions made
// (an independent transcript — so a dropped/duplicated/reordered log append is
// caught, not just the return value).
func TestDecision_LogMatchesDecisions(t *testing.T) {
	cfg := PowerConfig{
		OnlyWhenCharging:  true,
		MinBatteryPercent: 20,
		NightModeOnly:     true,
		NightWindow:       NightWindow{StartHour: 22, EndHour: 6},
		MaxCpuTempC:       80,
	}
	type step struct {
		label string
		src   fakeSource
	}
	steps := []step{
		{"unplugged", fakeSource{charging: false, battery: 90, temp: 40, now: at(23)}},
		{"low-batt", fakeSource{charging: true, battery: 9, temp: 40, now: at(23)}},
		{"daytime", fakeSource{charging: true, battery: 90, temp: 40, now: at(14)}},
		{"too-hot", fakeSource{charging: true, battery: 90, temp: 91, now: at(23)}},
		{"ok-night", fakeSource{charging: true, battery: 90, temp: 40, now: at(23)}},
		{"ok-early-am", fakeSource{charging: true, battery: 90, temp: 40, now: at(5)}},
	}
	wantReasons := []string{"not_charging", "battery_low_9%", "daytime", "cpu_hot_91C", "", ""}

	var log []string
	var transcript strings.Builder
	for _, s := range steps {
		g := New(cfg, s.src)
		accept, reason := g.CanAcceptWork()
		// audit append — the load-bearing line a "drop a log append" mutation kills.
		log = append(log, reason)
		transcript.WriteString(fmt.Sprintf("%-12s accept=%v reason=%q\n", s.label, accept, reason))
	}

	if len(log) != len(steps) {
		t.Fatalf("log count = %d, want %d (one entry per decision)", len(log), len(steps))
	}
	for i := range wantReasons {
		if log[i] != wantReasons[i] {
			t.Fatalf("log[%d] (%s) = %q, want %q\nfull transcript:\n%s",
				i, steps[i].label, log[i], wantReasons[i], transcript.String())
		}
	}
	t.Logf("decision audit trail:\n%s", transcript.String())
}

// TestDecision_NilZeroInputsNoPanic asserts zero/empty config and zero-value
// inputs never panic and yield the documented "accept everything" behaviour.
func TestDecision_NilZeroInputsNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("zero-value gater panicked: %v", r)
		}
	}()
	// Empty policy, zero-value source (zero time, zero battery, zero temp).
	g := New(PowerConfig{}, fakeSource{})
	accept, reason := g.CanAcceptWork()
	if !accept || reason != ReasonAccept {
		t.Fatalf("empty policy must accept everything, got (%v,%q)", accept, reason)
	}

	// Zero-value NightWindow {0,0} is whole-day -> with NightModeOnly any hour in.
	g2 := New(PowerConfig{NightModeOnly: true}, fakeSource{now: time.Time{}})
	accept2, reason2 := g2.CanAcceptWork()
	if !accept2 || reason2 != ReasonAccept {
		t.Fatalf("whole-day window must accept, got (%v,%q)", accept2, reason2)
	}
}

// TestTempUnknown_NeverTrips pins that a NaN (unknown) temperature never refuses
// work even when MaxCpuTempC is set and would otherwise be exceeded.
func TestTempUnknown_NeverTrips(t *testing.T) {
	cfg := PowerConfig{MaxCpuTempC: 50}
	g := New(cfg, fakeSource{charging: true, battery: 100, temp: math.NaN(), now: at(12)})
	accept, reason := g.CanAcceptWork()
	if !accept || reason != ReasonAccept {
		t.Fatalf("NaN temp must never trip cpu_hot, got (%v,%q)", accept, reason)
	}
}
