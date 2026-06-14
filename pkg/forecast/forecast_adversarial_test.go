package forecast

// Adversarial SINK-SIDE suite for pkg/forecast.
//
// forecast does float prediction (OLS slope projection), the exact surface that
// poisoned ewmarank: a NaN sample corrupts accumulated numeric state so that
// subsequent predictions are garbage, and a `< 0`-style guard would miss NaN.
//
// What the documented contract promises (forecast.go doc comments):
//   - projected = current + slope*Lead, OLS slope over the trailing Window.
//   - deterministic, pure-Go, no I/O / time / host access.
//   - PreWarm iff slope > slopeEpsilon AND projected >= Threshold.
//   - "fewer than 2 samples => projection equals current, PreWarm false".
//   - non-finite (NaN/±Inf) utilization samples are REJECTED by Observe (the
//     HXC-1798 hardening): they are not added to the window, never fire, and
//     report the last in-window utilization (finite).
//
// HXC-1798 (FIXED): originally Observe had NO input guard, so a single NaN/Inf
// util poisoned the OLS sums and made EVERY projection NaN for as long as the
// poison sat in the trailing window — and since the fire gate requires
// `slope > slopeEpsilon` (which NaN never satisfies), it silently SUPPRESSED
// legitimate PreWarm signals for in-range follow-up samples (a missed
// pre-emptive scale-up — a real availability defect under CLAUDE-1). The fix
// drops non-finite samples at the top of Observe. The tests below are the
// regression guards: they PASS with the guard in place and FAIL if it is removed
// (mutation-proven).
//
// Blast radius: ZERO non-test importers today (package unwired), so the fix is a
// latent-safe foundation any future caller inherits.

import (
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// (a) NUMERIC POISON — SINK-SIDE proof
// ---------------------------------------------------------------------------

// TestNaNSampleRejected_SinkStaysFinite is the REGRESSION GUARD for HXC-1798:
// a NaN util sample must be REJECTED (not added to the window), so it neither
// poisons its own projection nor any SUBSEQUENT known-good, in-range sample.
//
// SINK-SIDE: a clean rising trace establishes that finite follow-up samples must
// project finite values; we then inject NaN mid-trace and assert the NaN sample
// itself returns the last in-window utilization (finite) and that every
// follow-up known-good sample still projects a finite value. If the Observe
// non-finite guard is removed, the projections go NaN and this bites.
func TestNaNSampleRejected_SinkStaysFinite(t *testing.T) {
	id := runUUID(t)
	const window, thr, lead = 3, 0.80, 4

	// Oracle: a clean forecaster's follow-up projections are finite.
	clean := &Forecaster{Window: window, Threshold: thr, Lead: lead}
	clean.Observe(0, 0.10)
	clean.Observe(1, 0.20)
	clean.Observe(2, 0.30)
	_, cleanG4 := clean.Observe(3, 0.40)
	if math.IsNaN(cleanG4) {
		t.Fatalf("run=%s oracle setup invalid: clean projection is NaN (%v)", id, cleanG4)
	}

	// Same trace but a NaN at tick 2: the guard must drop it.
	f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
	f.Observe(0, 0.10)
	_, prevProj := f.Observe(1, 0.20) // last finite in-window util is 0.20
	preP, projP := f.Observe(2, math.NaN())
	if preP {
		t.Fatalf("run=%s NaN sample must not fire a PreWarm", id)
	}
	if math.IsNaN(projP) || math.IsInf(projP, 0) {
		t.Fatalf("run=%s NaN sample must be REJECTED to a finite projection, got %v", id, projP)
	}
	if projP != 0.20 {
		t.Fatalf("run=%s NaN sample must report the last in-window util 0.20, got %.17g (prevProj=%.6f)", id, projP, prevProj)
	}

	// SINK: subsequent KNOWN-GOOD samples (0.30, 0.40) must project FINITE — the
	// dropped NaN never entered the window, so the trend is uncorrupted.
	preG3, projG3 := f.Observe(3, 0.30)
	preG4, projG4 := f.Observe(4, 0.40)
	if preG3 || preG4 {
		t.Fatalf("run=%s sub-threshold rising samples unexpectedly fired", id)
	}
	if math.IsNaN(projG3) || math.IsInf(projG3, 0) {
		t.Fatalf("run=%s SINK POISONED: tick3 good sample projected %v (must be finite after NaN drop)", id, projG3)
	}
	if math.IsNaN(projG4) || math.IsInf(projG4, 0) {
		t.Fatalf("run=%s SINK POISONED: tick4 good sample projected %v (must be finite after NaN drop)", id, projG4)
	}
	t.Logf("run=%s GUARD HOLDS: NaN dropped (proj=%.2f), known-good follow-ups finite t3=%.6f t4=%.6f", id, projP, projG3, projG4)
}

// TestInfSampleRejected_SinkStaysFinite is the REGRESSION GUARD for ±Inf: an
// infinite util sample must be rejected the same way a NaN is, so the sink stays
// finite. Removing the Observe non-finite guard makes the Inf poison the OLS
// sums (Inf*tick / Inf-Inf -> NaN) and this bites.
func TestInfSampleRejected_SinkStaysFinite(t *testing.T) {
	id := runUUID(t)
	const window, thr, lead = 3, 0.80, 4

	for _, inf := range []float64{math.Inf(1), math.Inf(-1)} {
		f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
		f.Observe(0, 0.10)
		f.Observe(1, 0.20)
		pre, proj := f.Observe(2, inf) // must be dropped
		if pre {
			t.Fatalf("run=%s inf=%v must not fire PreWarm", id, inf)
		}
		if math.IsNaN(proj) || math.IsInf(proj, 0) {
			t.Fatalf("run=%s inf=%v: must be REJECTED to a finite projection, got %v", id, inf, proj)
		}
		// Sink: good follow-up stays FINITE because the Inf never entered the window.
		_, projG := f.Observe(3, 0.40)
		if math.IsNaN(projG) || math.IsInf(projG, 0) {
			t.Fatalf("run=%s inf=%v: SINK POISONED, good sample projected %v", id, inf, projG)
		}
		t.Logf("run=%s GUARD HOLDS: inf=%v dropped, good util 0.40 -> finite %.6f", id, inf, projG)
	}
}

// TestNegativeUtilAccepted_NaNRejected pins the precise boundary of the guard:
// the fix rejects ONLY non-finite (NaN/±Inf) samples, NOT merely out-of-[0,1]
// finite values. A negative util is still accepted (it is finite and cannot
// poison the OLS sums), while a NaN — which a naive `x < 0` guard would let
// through (NaN<0 is false) — is now correctly rejected to a finite sink. This
// pins that the guard is NaN-aware, not a value-range clamp.
func TestNegativeUtilAccepted_NaNRejected(t *testing.T) {
	id := runUUID(t)
	const window, thr, lead = 4, 0.50, 3

	// Negative util is finite -> still accepted (the guard rejects non-finite only).
	f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
	f.Observe(0, 0.10)
	_, projNeg := f.Observe(1, -5.0)
	if math.IsNaN(projNeg) || math.IsInf(projNeg, 0) {
		t.Fatalf("run=%s negative (finite) util must stay accepted/finite, got %v", id, projNeg)
	}
	t.Logf("run=%s finite negative util=-5.0 accepted, finite proj=%.4f (guard is non-finite-only)", id, projNeg)

	// A NaN slips past any `x < 0` guard (NaN<0==false) but the non-finite guard
	// rejects it -> finite sink.
	g := &Forecaster{Window: window, Threshold: thr, Lead: lead}
	for i := 0; i < 3; i++ {
		g.Observe(i, 0.10+0.05*float64(i))
	}
	_, projNaN := g.Observe(3, math.NaN())
	if math.IsNaN(projNaN) || math.IsInf(projNaN, 0) {
		t.Fatalf("run=%s NaN must be rejected by the non-finite guard to a finite sink; got %v", id, projNaN)
	}
	t.Logf("run=%s GUARD HOLDS: NaN rejected (NaN<0==false would miss it) -> finite proj=%.4f", id, projNaN)
}

// ---------------------------------------------------------------------------
// (b) TOTAL-ORDER / comparator robustness
//     forecast does NOT rank/sort candidates (no slice sort, no comparator).
//     The only ordered decision is the scalar fire guard `slope > slopeEpsilon`
//     and `projected >= Threshold`. We pin that these comparisons are NaN-SAFE:
//     a NaN slope/projection must NOT satisfy the `>`/`>=` predicates, so poison
//     never manufactures a spurious PreWarm. (This is the strict-weak-ordering
//     analogue for the scalar gate.)
// ---------------------------------------------------------------------------

// TestContract_NaNNeverFires pins that NaN in slope or projection cannot trip
// the fire guard. A naive guard that flipped to `!(slope <= eps)` (to "treat
// undefined as rising") WOULD fire on NaN — this test bites that mutation.
func TestContract_NaNNeverFires(t *testing.T) {
	id := runUUID(t)
	const window, lead = 3, 4
	// Threshold below the poisoned utils so ONLY the NaN-safety of the guard
	// (not a sub-threshold projection) can be what suppresses the fire. The
	// pre-NaN samples are deliberately FLAT (slope 0) so they cannot legitimately
	// fire on their own — the NaN tick is the only candidate for a fire, making a
	// fire here unambiguously a NaN-comparison-safety failure.
	const thr = 0.0
	const flat = 0.90 // well above thr=0.0

	f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
	pre0, _ := f.Observe(0, flat)
	pre1, _ := f.Observe(1, flat) // flat: slope 0, must not fire
	if pre0 || pre1 {
		t.Fatalf("run=%s setup invalid: flat pre-NaN samples fired (slope should be 0)", id)
	}
	if _, ok := f.FiredTick(); ok {
		t.Fatalf("run=%s setup invalid: a fire occurred before the NaN tick", id)
	}
	// Inject NaN with thr=0.0: even a NaN that reached the gate must not fire
	// (NaN >= 0 and NaN > slopeEpsilon are both false), AND the HXC-1798 guard
	// now drops the NaN entirely, so the projection is finite and still no fire.
	// Either way a NaN must NEVER manufacture a PreWarm.
	pre, proj := f.Observe(2, math.NaN())
	if math.IsNaN(proj) || math.IsInf(proj, 0) {
		t.Fatalf("run=%s NaN sample must be dropped to a finite projection, got %v", id, proj)
	}
	if pre {
		t.Fatalf("run=%s NaN must NOT fire even with thr=0.0", id)
	}
	if _, ok := f.FiredTick(); ok {
		t.Fatalf("run=%s FiredTick reports a fire on NaN input", id)
	}
	t.Logf("run=%s NaN never fires: dropped to finite proj=%.2f with thr=0.0, no fire", id, proj)
}

// TestContract_DeterministicNaNPath pins that the poisoned path is itself
// DETERMINISTIC: two independent forecasters fed the identical NaN-bearing
// trace produce the identical fire decision and bit-identical (NaN-pattern)
// projections. Determinism is an explicit documented guarantee and must hold
// even on the degenerate input. (NaN != NaN, so we compare via IsNaN masks.)
func TestContract_DeterministicNaNPath(t *testing.T) {
	id := runUUID(t)
	const window, thr, lead = 3, 0.80, 4
	trace := [][2]float64{
		{0, 0.10}, {1, 0.20}, {2, math.NaN()}, {3, 0.40}, {4, 0.50}, {5, 0.60},
	}
	run := func() (firedTick int, fired bool, nanMask []bool, vals []float64) {
		f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
		for _, p := range trace {
			_, proj := f.Observe(int(p[0]), p[1])
			nanMask = append(nanMask, math.IsNaN(proj))
			vals = append(vals, proj)
		}
		ft, ok := f.FiredTick()
		return ft, ok, nanMask, vals
	}
	t1, ok1, m1, v1 := run()
	t2, ok2, m2, v2 := run()
	if t1 != t2 || ok1 != ok2 {
		t.Fatalf("run=%s nondeterministic fire on NaN path: (%d,%v) vs (%d,%v)", id, t1, ok1, t2, ok2)
	}
	for i := range m1 {
		if m1[i] != m2[i] {
			t.Fatalf("run=%s NaN-mask diverged at i=%d: %v vs %v", id, i, m1[i], m2[i])
		}
		// For finite positions, require bit-identical values.
		if !m1[i] && v1[i] != v2[i] {
			t.Fatalf("run=%s finite projection diverged at i=%d: %.17g vs %.17g", id, i, v1[i], v2[i])
		}
	}
	t.Logf("run=%s determinism holds on NaN-bearing trace (fire=(%d,%v), masks identical)", id, t1, ok1)
}

// ---------------------------------------------------------------------------
// (c) DIVISION / UNDERFLOW / EMPTY-SERIES — these are GENUINELY GUARDED, so
//     these are real CONTRACT-PINNING regression guards (no fix needed).
// ---------------------------------------------------------------------------

// TestContract_DegenerateTickSpreadNoNaN pins the `denom == 0` guard: when all
// ticks in the window are identical, the OLS denominator is zero and the
// documented behaviour is "slope undefined => projected == current", NOT a
// divide-by-zero NaN/Inf. This is a real guarantee the code keeps.
//
// MUTATION TARGET: removing the `if denom == 0 { return 0, false }` line would
// make this divide by zero and project NaN — this test bites that.
func TestContract_DegenerateTickSpreadNoNaN(t *testing.T) {
	id := runUUID(t)
	f := &Forecaster{Window: 3, Threshold: 0.5, Lead: 3}
	f.Observe(7, 0.20)
	pre, proj := f.Observe(7, 0.40) // identical tick => denom 0
	if pre {
		t.Fatalf("run=%s degenerate tick spread must not fire (slope undefined)", id)
	}
	if math.IsNaN(proj) || math.IsInf(proj, 0) {
		t.Fatalf("run=%s denom==0 guard failed: projection non-finite %v", id, proj)
	}
	if proj != 0.40 {
		t.Fatalf("run=%s denom==0: projected must equal current 0.40, got %.17g", id, proj)
	}
	t.Logf("run=%s denom==0 guard: identical ticks -> projected==current==0.40, finite, no fire", id)
}

// TestContract_EmptyAndSingleSeries pins the documented sub-2-sample behaviour:
// never-observed -> no fire; single sample -> projected == that sample's util,
// no fire, finite. This guards the `n < 2 => ok=false` branch.
func TestContract_EmptyAndSingleSeries(t *testing.T) {
	id := runUUID(t)
	f := &Forecaster{Window: 5, Threshold: 0.10, Lead: 4} // low thr: a buggy early fire would show

	if tick, ok := f.FiredTick(); ok {
		t.Fatalf("run=%s never-observed reports fired at %d", id, tick)
	}
	const u = 0.55
	pre, proj := f.Observe(0, u)
	if pre {
		t.Fatalf("run=%s single sample must not fire (slope undefined)", id)
	}
	if proj != u {
		t.Fatalf("run=%s single sample: projected=%.17g must equal current=%.17g", id, proj, u)
	}
	if math.IsNaN(proj) || math.IsInf(proj, 0) {
		t.Fatalf("run=%s single sample projection non-finite: %v", id, proj)
	}
	t.Logf("run=%s empty/single series: no fire, projected==current==%.2f, finite", id, u)
}

// TestContract_HugeTickNoOverflowNaN stresses the OLS denominator with large
// (but int-representable) ticks to confirm the denom term does not overflow to
// Inf/NaN for realistic monotone tick streams. Finite ticks => finite denom =>
// finite slope for finite utils. Pins that the value path stays finite.
func TestContract_HugeTickNoOverflowNaN(t *testing.T) {
	id := runUUID(t)
	f := &Forecaster{Window: 4, Threshold: 1e9, Lead: 2}
	base := 1 << 20
	for i := 0; i < 6; i++ {
		tick := base + i*7
		_, proj := f.Observe(tick, 0.10+0.01*float64(i))
		if math.IsNaN(proj) || math.IsInf(proj, 0) {
			t.Fatalf("run=%s large-tick projection non-finite at i=%d tick=%d: %v", id, i, tick, proj)
		}
	}
	t.Logf("run=%s large monotone ticks: all projections finite (no denom overflow)", id)
}
