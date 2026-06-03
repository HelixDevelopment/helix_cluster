package powergater

import (
	"math"
	"strings"
	"testing"
	"time"
)

// fakeSource is a fully controllable PowerSource for exhaustive unit testing of
// CanAcceptWork across every state. It is the ONLY mock in this package and is
// used solely to drive the pure decision logic — the real per-OS readers
// (reader_darwin.go / reader_linux.go) return measured values and are exercised
// by the integration test.
type fakeSource struct {
	charging bool
	battery  int
	temp     float64
	now      time.Time
}

func (f fakeSource) Charging() bool      { return f.charging }
func (f fakeSource) BatteryPercent() int { return f.battery }
func (f fakeSource) CPUTempC() float64   { return f.temp }
func (f fakeSource) Now() time.Time      { return f.now }

func at(hour int) time.Time {
	return time.Date(2026, 6, 3, hour, 0, 0, 0, time.UTC)
}

func TestCanAcceptWork_States(t *testing.T) {
	tests := []struct {
		name       string
		cfg        PowerConfig
		src        fakeSource
		wantAccept bool
		wantReason string
	}{
		{
			name:       "all clear charging",
			cfg:        PowerConfig{OnlyWhenCharging: true, MinBatteryPercent: 20, MaxCpuTempC: 80},
			src:        fakeSource{charging: true, battery: 90, temp: 45, now: at(12)},
			wantAccept: true,
			wantReason: "",
		},
		{
			name:       "not charging refused",
			cfg:        PowerConfig{OnlyWhenCharging: true},
			src:        fakeSource{charging: false, battery: 90, temp: 45, now: at(12)},
			wantAccept: false,
			wantReason: "not_charging",
		},
		{
			name:       "battery low refused with live pct",
			cfg:        PowerConfig{MinBatteryPercent: 20},
			src:        fakeSource{charging: false, battery: 7, temp: 45, now: at(12)},
			wantAccept: false,
			wantReason: "battery_low_7%",
		},
		{
			name:       "battery exactly at min accepted",
			cfg:        PowerConfig{MinBatteryPercent: 20},
			src:        fakeSource{charging: false, battery: 20, temp: 45, now: at(12)},
			wantAccept: true,
			wantReason: "",
		},
		{
			name:       "daytime refused in night-only window",
			cfg:        PowerConfig{NightModeOnly: true, NightWindow: NightWindow{StartHour: 22, EndHour: 6}},
			src:        fakeSource{charging: true, battery: 90, temp: 45, now: at(13)},
			wantAccept: false,
			wantReason: "daytime",
		},
		{
			name:       "nighttime accepted in wrapping window",
			cfg:        PowerConfig{NightModeOnly: true, NightWindow: NightWindow{StartHour: 22, EndHour: 6}},
			src:        fakeSource{charging: true, battery: 90, temp: 45, now: at(23)},
			wantAccept: true,
			wantReason: "",
		},
		{
			name:       "early morning accepted in wrapping window",
			cfg:        PowerConfig{NightModeOnly: true, NightWindow: NightWindow{StartHour: 22, EndHour: 6}},
			src:        fakeSource{charging: true, battery: 90, temp: 45, now: at(3)},
			wantAccept: true,
			wantReason: "",
		},
		{
			name:       "cpu hot refused with live temp",
			cfg:        PowerConfig{MaxCpuTempC: 80},
			src:        fakeSource{charging: true, battery: 90, temp: 95, now: at(12)},
			wantAccept: false,
			wantReason: "cpu_hot_95C",
		},
		{
			name:       "cpu exactly at max accepted",
			cfg:        PowerConfig{MaxCpuTempC: 80},
			src:        fakeSource{charging: true, battery: 90, temp: 80, now: at(12)},
			wantAccept: true,
			wantReason: "",
		},
		{
			name:       "unknown temp treated as not hot",
			cfg:        PowerConfig{MaxCpuTempC: 80},
			src:        fakeSource{charging: true, battery: 90, temp: math.NaN(), now: at(12)},
			wantAccept: true,
			wantReason: "",
		},
		{
			name:       "priority: not_charging beats battery_low",
			cfg:        PowerConfig{OnlyWhenCharging: true, MinBatteryPercent: 20},
			src:        fakeSource{charging: false, battery: 5, temp: 45, now: at(12)},
			wantAccept: false,
			wantReason: "not_charging",
		},
		{
			name:       "no policy accepts everything",
			cfg:        PowerConfig{},
			src:        fakeSource{charging: false, battery: 1, temp: 200, now: at(12)},
			wantAccept: true,
			wantReason: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := New(tt.cfg, tt.src)
			accept, reason := g.CanAcceptWork()
			if accept != tt.wantAccept {
				t.Fatalf("accept = %v, want %v (reason %q)", accept, tt.wantAccept, reason)
			}
			if reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

// TestNightWindowContains pins the window semantics, including the wrap and the
// whole-day cases, so a mutation to the comparison logic is caught.
func TestNightWindowContains(t *testing.T) {
	w := NightWindow{StartHour: 22, EndHour: 6}
	cases := map[int]bool{
		21: false, 22: true, 23: true, 0: true, 5: true, 6: false, 13: false,
	}
	for hour, want := range cases {
		if got := w.contains(hour); got != want {
			t.Errorf("wrap window contains(%d) = %v, want %v", hour, got, want)
		}
	}

	day := NightWindow{StartHour: 9, EndHour: 17}
	if !day.contains(12) || day.contains(8) || day.contains(17) {
		t.Errorf("forward window boundaries wrong")
	}

	whole := NightWindow{StartHour: 0, EndHour: 0}
	if !whole.contains(0) || !whole.contains(23) {
		t.Errorf("whole-day window should contain every hour")
	}
}

// TestDecisionLogAcrossStates captures a decision log proving the guard yields
// the correct refuse reason as the charger, battery, temperature, and time are
// toggled — the HXC-1175 closure evidence (sink-side, not just a return value).
func TestDecisionLogAcrossStates(t *testing.T) {
	cfg := PowerConfig{
		OnlyWhenCharging:  true,
		MinBatteryPercent: 20,
		NightModeOnly:     true,
		NightWindow:       NightWindow{StartHour: 22, EndHour: 6},
		MaxCpuTempC:       80,
	}
	steps := []struct {
		label string
		src   fakeSource
	}{
		{"charger-unplugged", fakeSource{charging: false, battery: 90, temp: 40, now: at(23)}},
		{"battery-drained", fakeSource{charging: true, battery: 9, temp: 40, now: at(23)}},
		{"daytime", fakeSource{charging: true, battery: 90, temp: 40, now: at(14)}},
		{"cpu-overheating", fakeSource{charging: true, battery: 90, temp: 91, now: at(23)}},
		{"all-clear", fakeSource{charging: true, battery: 90, temp: 40, now: at(23)}},
	}
	var log strings.Builder
	want := []string{"not_charging", "battery_low_9%", "daytime", "cpu_hot_91C", ""}
	for i, s := range steps {
		g := New(cfg, s.src)
		accept, reason := g.CanAcceptWork()
		log.WriteString(s.label + " -> accept=" +
			boolStr(accept) + " reason=" + quote(reason) + "\n")
		if reason != want[i] {
			t.Fatalf("step %q: reason=%q want %q", s.label, reason, want[i])
		}
	}
	t.Logf("PowerGater decision log:\n%s", log.String())
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
func quote(s string) string { return "\"" + s + "\"" }
