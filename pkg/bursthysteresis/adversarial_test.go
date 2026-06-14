package bursthysteresis

import (
	"math"
	"testing"
)

// ============================================================================
// Adversarial exhaustive state-machine tests for the two-threshold hysteresis
// burst controller.
//
// Documented semantics being pinned (from bursthysteresis.go doc comment):
//
//	MONITOR: sample >= SpillHigh  -> emit SPILL,   move to SPILL   (inclusive lower bound)
//	SPILL  : sample <= RecoverLow -> emit RECOVER, move to RECOVER (inclusive upper bound)
//	SPILL  : RecoverLow < sample < SpillHigh -> dead-band, NO event, stays SPILL
//	SPILL  : sample >= SpillHigh -> no-op (already spilling, edge-triggered)
//	RECOVER: next Feed auto-advances to MONITOR, then re-evaluates the sample.
//
// Default thresholds: SpillHigh = 0.90, RecoverLow = 0.70.
//
// These tests deliberately probe just-below / exactly-at / just-above EACH
// threshold using math.Nextafter so they bite an off-by-one (>= vs >, <= vs <)
// or a wrong-threshold-direction mutation. They also drive a long no-flap
// sweep inside the dead-band so collapsing the two thresholds into one is
// caught.
// ============================================================================

const (
	spillHigh  = 0.90
	recoverLow = 0.70
)

// newCtrl builds a default-threshold controller plus an event-capturing
// subscriber. Returns the controller and a pointer to the captured slice.
func newCtrl(t *testing.T, runID string) (*BurstController, *[]Event) {
	t.Helper()
	ctrl, err := New(Config{SpillHigh: spillHigh, RecoverLow: recoverLow, RunID: runID})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := &[]Event{}
	ctrl.Subscribe(func(ev Event) { *events = append(*events, ev) })
	return ctrl, events
}

// just returns the next representable float64 above x (dir>0) or below x (dir<0).
func just(x float64, dir int) float64 {
	if dir >= 0 {
		return math.Nextafter(x, math.Inf(1))
	}
	return math.Nextafter(x, math.Inf(-1))
}

// ----------------------------------------------------------------------------
// 1. BOUNDARY CORRECTNESS — the enter (SpillHigh) threshold from MONITOR.
//
// Documented: MONITOR fires SPILL iff sample >= SpillHigh.
//
//	just-below SpillHigh -> NO fire
//	exactly  SpillHigh   -> fire (inclusive)
//	just-above SpillHigh  -> fire
//
// ANTI-BLUFF: the "exactly SpillHigh fires" case is what a `>= -> >` mutant
// breaks (0.90 > 0.90 is false), and the "just-below does NOT fire" case is
// what a `>= -> <=`/wrong-direction or a lowered-threshold mutant breaks.
// We test all three with the SMALLEST representable gap (Nextafter), so a
// mutant cannot survive by exploiting a coarse epsilon.
// ----------------------------------------------------------------------------
func TestEnterThresholdBoundary(t *testing.T) {
	cases := []struct {
		name      string
		sample    float64
		wantFire  bool
		wantState State
	}{
		{"just below SpillHigh: no fire", just(spillHigh, -1), false, StateMonitor},
		{"exactly SpillHigh: fire (inclusive)", spillHigh, true, StateSpill},
		{"just above SpillHigh: fire", just(spillHigh, +1), true, StateSpill},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl, events := newCtrl(t, "enter-boundary")
			ctrl.Feed(tc.sample)

			gotFire := len(*events) > 0
			if gotFire != tc.wantFire {
				t.Fatalf("sample %.17g: fire=%v want %v (events=%+v)",
					tc.sample, gotFire, tc.wantFire, *events)
			}
			if tc.wantFire {
				if (*events)[0].Kind != EventSpill {
					t.Errorf("sample %.17g: kind=%v want SPILL", tc.sample, (*events)[0].Kind)
				}
				if (*events)[0].Sample != tc.sample {
					t.Errorf("event.Sample=%.17g want %.17g", (*events)[0].Sample, tc.sample)
				}
			}
			if got := ctrl.State(); got != tc.wantState {
				t.Errorf("state=%v want %v", got, tc.wantState)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// 2. BOUNDARY CORRECTNESS — the exit (RecoverLow) threshold from SPILL.
//
// Documented: SPILL fires RECOVER iff sample <= RecoverLow.
//
//	just-above RecoverLow -> NO fire (still in dead-band, stays SPILL)
//	exactly  RecoverLow   -> fire (inclusive)
//	just-below RecoverLow  -> fire
//
// ANTI-BLUFF: the "exactly RecoverLow fires" case is what a `<= -> <` mutant
// breaks (0.70 < 0.70 is false). The "just-above RecoverLow does NOT fire"
// case is the hysteresis dead-band lower fence — it bites a `<= -> <=` raised
// threshold or a wrong-direction mutant. Each controller is driven into SPILL
// first (via 0.95) so we are genuinely testing the SPILL->RECOVER edge.
// ----------------------------------------------------------------------------
func TestExitThresholdBoundary(t *testing.T) {
	cases := []struct {
		name      string
		sample    float64
		wantFire  bool
		wantState State
	}{
		{"just above RecoverLow: no fire (dead-band)", just(recoverLow, +1), false, StateSpill},
		{"exactly RecoverLow: fire (inclusive)", recoverLow, true, StateRecover},
		{"just below RecoverLow: fire", just(recoverLow, -1), true, StateRecover},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl, events := newCtrl(t, "exit-boundary")

			// Drive into SPILL.
			ctrl.Feed(0.95)
			if ctrl.State() != StateSpill {
				t.Fatalf("setup: expected SPILL, got %v", ctrl.State())
			}
			baseline := len(*events) // 1 (the SPILL)

			ctrl.Feed(tc.sample)
			gotFire := len(*events) > baseline
			if gotFire != tc.wantFire {
				t.Fatalf("sample %.17g: fire=%v want %v (events=%+v)",
					tc.sample, gotFire, tc.wantFire, *events)
			}
			if tc.wantFire {
				last := (*events)[len(*events)-1]
				if last.Kind != EventRecover {
					t.Errorf("sample %.17g: kind=%v want RECOVER", tc.sample, last.Kind)
				}
				if last.Sample != tc.sample {
					t.Errorf("event.Sample=%.17g want %.17g", last.Sample, tc.sample)
				}
			}
			if got := ctrl.State(); got != tc.wantState {
				t.Errorf("state=%v want %v", got, tc.wantState)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// 3. HYSTERESIS — NO FLAP in the dead-band.
//
// Once in SPILL, an arbitrarily long oscillation of samples strictly inside
// (RecoverLow, SpillHigh) — including values touching both fences from the
// inside via Nextafter — MUST NOT emit any event and MUST stay SPILL. Only a
// sample <= RecoverLow exits.
//
// ANTI-BLUFF: if the two thresholds were collapsed to a single value (no
// hysteresis gap), then samples in this band would straddle that single
// threshold and flap SPILL<->RECOVER, producing many spurious events. This
// test asserts ZERO events across the whole in-band sweep, so a
// collapsed-threshold mutant produces a non-zero count and fails loudly.
// It also asserts the state never leaves SPILL mid-sweep.
// ----------------------------------------------------------------------------
func TestNoFlapInDeadBand(t *testing.T) {
	ctrl, events := newCtrl(t, "no-flap")

	ctrl.Feed(0.95) // enter SPILL
	if ctrl.State() != StateSpill {
		t.Fatalf("setup: expected SPILL, got %v", ctrl.State())
	}
	eventsAfterSpill := len(*events) // exactly 1

	// A long adversarial oscillation strictly inside the dead-band. Includes
	// the innermost points next to each fence (just inside RecoverLow and
	// just inside SpillHigh) and a saw-tooth between them.
	band := []float64{
		just(recoverLow, +1), // just above the exit fence (must NOT exit)
		0.71, 0.89, 0.72, 0.88, 0.75, 0.85, 0.80, 0.78, 0.82,
		just(spillHigh, -1), // just below the enter fence
		0.71, 0.89, 0.705, 0.895, 0.80,
		just(recoverLow, +1),
		just(spillHigh, -1),
	}
	for i, s := range band {
		ctrl.Feed(s)
		if got := len(*events); got != eventsAfterSpill {
			t.Fatalf("dead-band sample #%d %.17g produced a spurious event (count %d, want %d): %+v",
				i, s, got, eventsAfterSpill, *events)
		}
		if st := ctrl.State(); st != StateSpill {
			t.Fatalf("dead-band sample #%d %.17g left SPILL: state=%v", i, s, st)
		}
	}

	// Only a sample at/below RecoverLow finally exits.
	ctrl.Feed(recoverLow)
	if ctrl.State() != StateRecover {
		t.Fatalf("after exit sample %.2f: state=%v want RECOVER", recoverLow, ctrl.State())
	}
	if len(*events) != eventsAfterSpill+1 {
		t.Fatalf("exit: want exactly one more event, got total %d: %+v", len(*events), *events)
	}
	if (*events)[len(*events)-1].Kind != EventRecover {
		t.Errorf("exit event kind=%v want RECOVER", (*events)[len(*events)-1].Kind)
	}
}

// ----------------------------------------------------------------------------
// 4. IDEMPOTENT / EDGE-TRIGGERED within SPILL.
//
// Documented: while in SPILL, a sample >= SpillHigh is a no-op (already
// spilling). Feeding many at-or-above-SpillHigh samples in a row must produce
// exactly ONE SPILL event total (on the first crossing), not one per sample.
//
// ANTI-BLUFF: a level-triggered (rather than edge-triggered) implementation
// would re-emit SPILL on every high sample; this asserts exactly one event
// across a run of high samples. Symmetrically, repeated samples at/below
// RecoverLow after RECOVER must not re-emit RECOVER while dwelling.
// ----------------------------------------------------------------------------
func TestEdgeTriggeredNoRepeatEvents(t *testing.T) {
	ctrl, events := newCtrl(t, "edge-triggered")

	// Run of high samples: only the FIRST should emit SPILL.
	for _, s := range []float64{0.90, 0.95, 0.99, 1.0, 0.91, 0.90} {
		ctrl.Feed(s)
	}
	if len(*events) != 1 {
		t.Fatalf("run of high samples: want exactly 1 SPILL event, got %d: %+v", len(*events), *events)
	}
	if (*events)[0].Kind != EventSpill {
		t.Errorf("first event kind=%v want SPILL", (*events)[0].Kind)
	}
	if ctrl.State() != StateSpill {
		t.Errorf("state=%v want SPILL", ctrl.State())
	}

	// Now exit once.
	ctrl.Feed(0.60) // RECOVER
	if len(*events) != 2 || (*events)[1].Kind != EventRecover {
		t.Fatalf("expected RECOVER as 2nd event, got %+v", *events)
	}
	if ctrl.State() != StateRecover {
		t.Errorf("state=%v want RECOVER", ctrl.State())
	}

	// Dwelling at/below RecoverLow after RECOVER: the documented behavior is
	// that the NEXT Feed auto-advances RECOVER->MONITOR and re-evaluates. A low
	// sample in MONITOR does not fire (only >= SpillHigh fires in MONITOR), so
	// no new RECOVER events should appear and the state lands back in MONITOR.
	ctrl.Feed(0.50) // RECOVER->MONITOR (re-eval 0.50: below SpillHigh, no event)
	if len(*events) != 2 {
		t.Fatalf("dwell-low after RECOVER: spurious event, total=%d: %+v", len(*events), *events)
	}
	if ctrl.State() != StateMonitor {
		t.Errorf("after low post-RECOVER: state=%v want MONITOR", ctrl.State())
	}
}

// ----------------------------------------------------------------------------
// 5. REALISTIC TRACE — exact event SEQUENCE assertion.
//
// rise above high -> enter; oscillate within the band -> stay; drop below low
// -> exit; rise again -> re-enter. Asserts the precise ordered list of events
// and the per-step state, end to end.
//
// ANTI-BLUFF: an off-by-one at either fence, a missing dead-band, or a
// collapsed threshold changes the event COUNT or ORDER and fails the exact
// sequence comparison below.
// ----------------------------------------------------------------------------
func TestRealisticTraceExactSequence(t *testing.T) {
	ctrl, events := newCtrl(t, "trace")

	type step struct {
		sample    float64
		wantState State
		wantKind  *EventKind // nil => no new event this step
	}
	spill := EventSpill
	recover := EventRecover
	steps := []step{
		{0.10, StateMonitor, nil},      // idle
		{0.70, StateMonitor, nil},      // == RecoverLow but in MONITOR: no effect
		{0.89, StateMonitor, nil},      // just under SpillHigh: no enter
		{0.90, StateSpill, &spill},     // exactly SpillHigh: ENTER
		{0.95, StateSpill, nil},        // higher while spilling: no repeat
		{0.88, StateSpill, nil},        // dead-band
		{0.71, StateSpill, nil},        // dead-band near low fence
		{0.85, StateSpill, nil},        // dead-band again (oscillation, no flap)
		{0.70, StateRecover, &recover}, // exactly RecoverLow: EXIT
		{0.95, StateSpill, &spill},     // RECOVER->MONITOR auto-advance, re-eval -> ENTER
		{0.60, StateRecover, &recover}, // EXIT
	}

	prev := 0
	for i, st := range steps {
		ctrl.Feed(st.sample)
		curr := len(*events)
		gotNew := curr - prev
		wantNew := 0
		if st.wantKind != nil {
			wantNew = 1
		}
		if gotNew != wantNew {
			t.Fatalf("step %d sample %.2f: new events=%d want %d (all=%+v)",
				i, st.sample, gotNew, wantNew, *events)
		}
		if st.wantKind != nil {
			got := (*events)[curr-1]
			if got.Kind != *st.wantKind {
				t.Errorf("step %d sample %.2f: kind=%v want %v", i, st.sample, got.Kind, *st.wantKind)
			}
			if got.Sample != st.sample {
				t.Errorf("step %d: event.Sample=%.17g want %.17g", i, got.Sample, st.sample)
			}
		}
		if got := ctrl.State(); got != st.wantState {
			t.Errorf("step %d sample %.2f: state=%v want %v", i, st.sample, got, st.wantState)
		}
		prev = curr
	}

	// Exact ordered kind sequence over the whole trace.
	wantSeq := []EventKind{EventSpill, EventRecover, EventSpill, EventRecover}
	if len(*events) != len(wantSeq) {
		t.Fatalf("trace produced %d events, want %d: %+v", len(*events), len(wantSeq), *events)
	}
	for i, want := range wantSeq {
		if (*events)[i].Kind != want {
			t.Errorf("event[%d].Kind=%v want %v", i, (*events)[i].Kind, want)
		}
	}
}

// ----------------------------------------------------------------------------
// 6. EDGE CASES — monotonic rise, monotonic fall, flat dwell.
//
// Each must cross its boundary EXACTLY once (one event), never flap.
// ----------------------------------------------------------------------------
func TestMonotonicRiseSingleEnter(t *testing.T) {
	ctrl, events := newCtrl(t, "mono-rise")
	// Strictly increasing ramp that crosses SpillHigh exactly once.
	ramp := []float64{0.00, 0.20, 0.40, 0.60, 0.70, 0.80, 0.89, 0.90, 0.95, 1.00}
	enterIdx := -1
	for i, s := range ramp {
		before := len(*events)
		ctrl.Feed(s)
		if len(*events) > before {
			if enterIdx != -1 {
				t.Fatalf("rise flapped: second event at index %d sample %.2f", i, s)
			}
			enterIdx = i
			if (*events)[0].Kind != EventSpill {
				t.Errorf("rise event kind=%v want SPILL", (*events)[0].Kind)
			}
		}
	}
	if enterIdx == -1 {
		t.Fatalf("monotonic rise never entered SPILL: %+v", *events)
	}
	// The crossing must be exactly at the first sample >= 0.90, which is ramp[7]=0.90.
	if ramp[enterIdx] != 0.90 {
		t.Errorf("entered at sample %.2f, want exactly 0.90 (first >= SpillHigh)", ramp[enterIdx])
	}
	if len(*events) != 1 {
		t.Errorf("monotonic rise: want exactly 1 event, got %d", len(*events))
	}
}

func TestMonotonicFallSingleExit(t *testing.T) {
	ctrl, events := newCtrl(t, "mono-fall")
	ctrl.Feed(0.95) // enter SPILL
	if ctrl.State() != StateSpill {
		t.Fatalf("setup: want SPILL got %v", ctrl.State())
	}
	// Strictly decreasing ramp that crosses RecoverLow exactly once.
	ramp := []float64{0.89, 0.85, 0.80, 0.75, 0.71, 0.70, 0.65, 0.50, 0.00}
	exitIdx := -1
	for i, s := range ramp {
		before := len(*events)
		ctrl.Feed(s)
		if len(*events) > before {
			if exitIdx != -1 {
				// After RECOVER, the next Feed auto-advances to MONITOR; a low
				// sample there does NOT fire, so no further events are allowed.
				t.Fatalf("fall flapped: extra event at index %d sample %.2f", i, s)
			}
			exitIdx = i
			last := (*events)[len(*events)-1]
			if last.Kind != EventRecover {
				t.Errorf("fall event kind=%v want RECOVER", last.Kind)
			}
		}
	}
	if exitIdx == -1 {
		t.Fatalf("monotonic fall never exited: %+v", *events)
	}
	// First sample <= 0.70 is ramp[5]=0.70.
	if ramp[exitIdx] != 0.70 {
		t.Errorf("exited at sample %.2f, want exactly 0.70 (first <= RecoverLow)", ramp[exitIdx])
	}
	// 1 SPILL (setup) + 1 RECOVER = 2 total.
	if len(*events) != 2 {
		t.Errorf("monotonic fall: want 2 events total, got %d: %+v", len(*events), *events)
	}
}

func TestFlatDwellNoSpuriousEvents(t *testing.T) {
	// Flat dwell exactly AT each threshold value and in the band — assert the
	// documented single-edge behavior with no repeats.
	t.Run("dwell at SpillHigh", func(t *testing.T) {
		ctrl, events := newCtrl(t, "flat-high")
		for i := 0; i < 20; i++ {
			ctrl.Feed(spillHigh)
		}
		if len(*events) != 1 {
			t.Errorf("dwell at SpillHigh: want 1 SPILL, got %d", len(*events))
		}
		if ctrl.State() != StateSpill {
			t.Errorf("state=%v want SPILL", ctrl.State())
		}
	})
	t.Run("dwell in dead-band stays SPILL silently", func(t *testing.T) {
		ctrl, events := newCtrl(t, "flat-band")
		ctrl.Feed(0.95) // enter
		base := len(*events)
		for i := 0; i < 20; i++ {
			ctrl.Feed(0.80) // mid-band
		}
		if len(*events) != base {
			t.Errorf("dwell in band: spurious events, total=%d want %d", len(*events), base)
		}
		if ctrl.State() != StateSpill {
			t.Errorf("state=%v want SPILL", ctrl.State())
		}
	})
}
