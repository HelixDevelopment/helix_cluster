package bursthysteresis

import (
	"math"
	"testing"
)

// ============================================================================
// HOSTILE-NUMERIC adversarial probes for the two-threshold hysteresis burst
// controller. This file is a sibling to adversarial_test.go and focuses
// exclusively on the class-of-bug that the existing suite does NOT cover:
// NaN / +Inf / -Inf samples, inverted/degenerate threshold configs, and the
// exact `>=` vs `>` / `<=` vs `<` boundary mutations.
//
// Documented contract (bursthysteresis.go):
//   - Samples are float64 "in [0,1]" (doc) but Feed performs NO validation, so
//     out-of-contract values (NaN/Inf/>1/<0) flow straight into the numeric
//     comparisons. These probes CHARACTERIZE and PIN the resulting behavior.
//   - MONITOR: sample >= SpillHigh  -> SPILL
//   - SPILL  : sample <= RecoverLow -> RECOVER ; else dead-band, stays SPILL
//   - RECOVER: next Feed auto-advances to MONITOR then re-evaluates.
//   - New: SpillHigh MUST be strictly > RecoverLow, else ErrInvalidThresholds.
// ============================================================================

func mustCtrl(t *testing.T, runID string) (*BurstController, *[]Event) {
	t.Helper()
	ctrl, err := New(Config{SpillHigh: 0.90, RecoverLow: 0.70, RunID: runID})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	evs := &[]Event{}
	ctrl.Subscribe(func(ev Event) { *evs = append(*evs, ev) })
	return ctrl, evs
}

// ----------------------------------------------------------------------------
// NaN-1. NaN in MONITOR must NOT trigger SPILL.
//
// `NaN >= SpillHigh` is false by IEEE-754, so the controller stays MONITOR and
// emits nothing. This is the SAFE direction of the classic "NaN poisons a
// `<`/`>=` guard" bug: here the guard fails CLOSED (no spurious spill).
//
// ANTI-BLUFF: a mutant that flipped the MONITOR guard to `sample < SpillHigh`
// (or to `!(sample >= SpillHigh)` style) would make NaN take the SPILL branch
// (since `NaN < x` is also false... but `!(false)`==true), so this pins the
// fail-closed property explicitly rather than by luck.
// ----------------------------------------------------------------------------
func TestNaNInMonitorDoesNotSpill(t *testing.T) {
	ctrl, evs := mustCtrl(t, "nan-monitor")
	ctrl.Feed(math.NaN())
	if len(*evs) != 0 {
		t.Fatalf("NaN in MONITOR emitted %d event(s), want 0: %+v", len(*evs), *evs)
	}
	if ctrl.State() != StateMonitor {
		t.Fatalf("NaN in MONITOR: state=%v want MONITOR", ctrl.State())
	}
	// Controller must remain usable: a real spike still spills.
	ctrl.Feed(0.95)
	if ctrl.State() != StateSpill || len(*evs) != 1 || (*evs)[0].Kind != EventSpill {
		t.Fatalf("after NaN, real spike failed to spill: state=%v evs=%+v", ctrl.State(), *evs)
	}
}

// ----------------------------------------------------------------------------
// NaN-2. NaN while in SPILL must NOT trigger RECOVER and must NOT exit the
// dead-band. `NaN <= RecoverLow` is false, so the controller stays SPILL.
//
// This is the load-bearing hysteresis-safety property: a poisoned/garbage
// sample must NOT silently drop the controller out of SPILL (which would let a
// real overload go unmitigated). We feed a burst of NaN and assert it is
// absorbed with zero events and SPILL retained, then prove a real low sample
// still recovers.
//
// ANTI-BLUFF: if the SPILL guard were `!(sample > RecoverLow)` instead of
// `sample <= RecoverLow`, NaN would satisfy it (`NaN > x` is false → negation
// true) and spuriously RECOVER. The mutation proof below flips exactly this.
// ----------------------------------------------------------------------------
func TestNaNInSpillStaysSpill(t *testing.T) {
	ctrl, evs := mustCtrl(t, "nan-spill")
	ctrl.Feed(0.95) // enter SPILL
	base := len(*evs)
	for i := 0; i < 8; i++ {
		ctrl.Feed(math.NaN())
		if ctrl.State() != StateSpill {
			t.Fatalf("NaN #%d in SPILL left SPILL: state=%v", i, ctrl.State())
		}
		if len(*evs) != base {
			t.Fatalf("NaN #%d in SPILL emitted spurious event: total=%d want %d (%+v)",
				i, len(*evs), base, *evs)
		}
	}
	// A genuine low sample still recovers — NaN did not wedge the machine.
	ctrl.Feed(0.50)
	if ctrl.State() != StateRecover || len(*evs) != base+1 ||
		(*evs)[len(*evs)-1].Kind != EventRecover {
		t.Fatalf("after NaN burst, real low failed to recover: state=%v evs=%+v",
			ctrl.State(), *evs)
	}
}

// ----------------------------------------------------------------------------
// INF-1. +Inf in MONITOR spills; -Inf in SPILL recovers. These are the
// well-defined IEEE comparisons (+Inf >= x true ; -Inf <= x true) and the
// controller must honor them as "extreme overload" / "extreme idle".
// ----------------------------------------------------------------------------
func TestInfinitiesDriveTransitions(t *testing.T) {
	ctrl, evs := mustCtrl(t, "inf")
	ctrl.Feed(math.Inf(+1)) // +Inf >= 0.90 -> SPILL
	if ctrl.State() != StateSpill || len(*evs) != 1 || (*evs)[0].Kind != EventSpill {
		t.Fatalf("+Inf failed to SPILL: state=%v evs=%+v", ctrl.State(), *evs)
	}
	ctrl.Feed(math.Inf(-1)) // -Inf <= 0.70 -> RECOVER
	if ctrl.State() != StateRecover || len(*evs) != 2 || (*evs)[1].Kind != EventRecover {
		t.Fatalf("-Inf failed to RECOVER: state=%v evs=%+v", ctrl.State(), *evs)
	}
}

// ----------------------------------------------------------------------------
// INF-2. +Inf while ALREADY in SPILL is a no-op (edge-triggered, like any
// >=SpillHigh sample). Pins that Inf does not re-emit.
// ----------------------------------------------------------------------------
func TestPosInfInSpillNoRepeat(t *testing.T) {
	ctrl, evs := mustCtrl(t, "inf-repeat")
	ctrl.Feed(0.95) // SPILL
	base := len(*evs)
	for i := 0; i < 5; i++ {
		ctrl.Feed(math.Inf(+1))
	}
	if len(*evs) != base {
		t.Fatalf("+Inf in SPILL re-emitted: total=%d want %d (%+v)", len(*evs), base, *evs)
	}
	if ctrl.State() != StateSpill {
		t.Fatalf("state=%v want SPILL", ctrl.State())
	}
}

// ----------------------------------------------------------------------------
// CFG-1. Inverted / degenerate threshold configs are rejected by New.
//   - SpillHigh < RecoverLow  -> ErrInvalidThresholds
//   - SpillHigh == RecoverLow -> ErrInvalidThresholds (no dead-band possible)
//   - NaN in either threshold -> NaN comparisons make `<=` false, so New does
//     NOT reject; this is a LATENT gap we pin so a future "validate" change is
//     a conscious decision, not an accident.
//
// ANTI-BLUFF: the `<=` (not `<`) in New is what rejects the equal-threshold
// degenerate case; a `<= -> <` mutant would ACCEPT SpillHigh==RecoverLow and
// build a zero-width dead-band. The mutation proof flips exactly this.
// ----------------------------------------------------------------------------
func TestInvalidThresholdConfigs(t *testing.T) {
	if _, err := New(Config{SpillHigh: 0.50, RecoverLow: 0.70}); err != ErrInvalidThresholds {
		t.Errorf("inverted band: err=%v want ErrInvalidThresholds", err)
	}
	if _, err := New(Config{SpillHigh: 0.80, RecoverLow: 0.80}); err != ErrInvalidThresholds {
		t.Errorf("equal band: err=%v want ErrInvalidThresholds", err)
	}
	// CHARACTERIZATION (LATENT RISK): NaN thresholds are NOT rejected today.
	if _, err := New(Config{SpillHigh: math.NaN(), RecoverLow: 0.70}); err != nil {
		t.Errorf("NaN SpillHigh: documenting current behavior; got err=%v, "+
			"expected nil (no validation). If this fails, validation was added.", err)
	}
}

// ----------------------------------------------------------------------------
// CFG-2. With a NaN SpillHigh (which New accepts), EVERY comparison
// `sample >= NaN` is false, so the controller can NEVER enter SPILL — it is
// silently dead. We PIN this so the degenerate effect is documented and so a
// future fix (rejecting NaN thresholds) deliberately flips this assertion.
// ----------------------------------------------------------------------------
func TestNaNThresholdMakesControllerInert(t *testing.T) {
	ctrl, err := New(Config{SpillHigh: math.NaN(), RecoverLow: 0.70, RunID: "nan-thr"})
	if err != nil {
		t.Skipf("NaN threshold now rejected by New (%v) — inertness no longer reachable", err)
	}
	evs := &[]Event{}
	ctrl.Subscribe(func(ev Event) { *evs = append(*evs, ev) })
	for _, s := range []float64{0.95, 1.0, math.Inf(+1), 100.0} {
		ctrl.Feed(s)
	}
	if len(*evs) != 0 || ctrl.State() != StateMonitor {
		t.Fatalf("NaN SpillHigh: expected inert controller, got state=%v evs=%+v",
			ctrl.State(), *evs)
	}
}

// ----------------------------------------------------------------------------
// BND-1. Exact-boundary mutation tripwires expressed independently of the
// other file: the enter edge fires AT SpillHigh (>=) and the exit edge fires
// AT RecoverLow (<=). These pin the inclusive boundaries directly.
// ----------------------------------------------------------------------------
func TestInclusiveBoundariesExact(t *testing.T) {
	// Enter exactly at SpillHigh.
	ctrl, evs := mustCtrl(t, "enter-exact")
	ctrl.Feed(0.90)
	if ctrl.State() != StateSpill || len(*evs) != 1 {
		t.Fatalf("sample==SpillHigh must enter SPILL: state=%v evs=%+v", ctrl.State(), *evs)
	}
	// Exit exactly at RecoverLow.
	ctrl.Feed(0.70)
	if ctrl.State() != StateRecover || len(*evs) != 2 {
		t.Fatalf("sample==RecoverLow must RECOVER: state=%v evs=%+v", ctrl.State(), *evs)
	}
}

// ----------------------------------------------------------------------------
// EVT-2. Out-of-contract HIGH sample (>1) behaves like any >=SpillHigh sample,
// and the emitted Event carries the exact (unclamped) sample through. Pins
// that Feed does not clamp or sanitize the value it reports downstream.
// ----------------------------------------------------------------------------
func TestOutOfRangeSamplePassedThroughVerbatim(t *testing.T) {
	ctrl, evs := mustCtrl(t, "oor")
	ctrl.Feed(42.5)
	if len(*evs) != 1 || (*evs)[0].Sample != 42.5 {
		t.Fatalf("out-of-range sample not passed through verbatim: evs=%+v", *evs)
	}
}
