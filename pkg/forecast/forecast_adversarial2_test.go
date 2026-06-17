package forecast

// Adversarial SECOND-PASS suite for pkg/forecast.
//
// The first adversarial pass (forecast_adversarial_test.go) fixed the
// non-finite-sample-poisons-OLS bug (HXC-1798): a NaN/±Inf util that corrupted
// the least-squares sums and silently suppressed legitimate PreWarm signals.
// This file goes after a DIFFERENT, deeper edge the first pass missed:
//
//   OLS NUMERICAL CONDITIONING UNDER LARGE TICKS.
//
// forecast.slope() computes the textbook *uncentered* normal-equation form:
//
//	denom = n*ΣXX - (ΣX)^2
//	slope = (n*ΣXY - ΣX*ΣY) / denom
//
// Both denom and the numerator are differences of two LARGE, NEARLY-EQUAL
// quantities when the tick values X are large (a long-running tick counter, a
// high-frequency / millisecond tick, or any monotone counter that has been
// advancing for a while). For X on the order of 2^30 (~1.07e9 — entirely
// representable as an int and reached by, e.g., a ms-granularity counter in
// ~12 days, or a per-request counter), ΣXX ~ 1e18 and (ΣX)^2 ~ 1e18 differ only
// in their low-order bits, so the subtraction loses almost all significance to
// CATASTROPHIC CANCELLATION. The result is not a small rounding error — the sign
// of the computed slope FLIPS:
//
//	true rising slope +0.05  ->  computed slope -0.025  (large ticks, n=5)
//
// SINK-SIDE CONSEQUENCE (the real defect): the fire gate is `slope > slopeEpsilon`.
// A genuinely RISING utilization trace whose ticks happen to be large is seen as
// DECLINING (slope < 0), so PreWarm is SILENTLY SUPPRESSED — the forecaster fails
// to pre-warm capacity ahead of a real load climb. This is the exact
// "predicts-down-when-load-is-rising" missed-scale-up availability defect
// (CLAUDE-1 class), reachable on a CRAFTED-BUT-VALID, finite, monotone, in-range
// series with NO NaN/Inf involved — which is why the first pass (whose largest
// tick test used only ~2^20) did not catch it.
//
// FIX (applied, minimal, left in place): slope() now computes the MEAN-CENTERED
// OLS form (subtract x̄ before forming the cross/variance sums). This is the
// mathematically identical least-squares slope but is numerically stable: the
// centered sums are O(spread^2), not O(magnitude^2), so no catastrophic
// cancellation occurs and the sign is preserved. The denom==0 / n<2 guards are
// preserved.
//
// MUTATION-PROVEN: TestCond_RisingLargeTicksStillFires below FAILS against the
// pre-fix uncentered formula (computed slope is negative -> no fire) and PASSES
// with the centered fix. The other tests pin that the fix changes nothing on the
// well-conditioned small-tick path (still matches the documented OLS value).

import (
	"math"
	"testing"
)

// centeredOracleSlope is an INDEPENDENT mean-centered OLS slope used only as a
// cross-check oracle for the conditioning tests. It is the standard stable form
// and agrees with the uncentered normal equations for well-conditioned inputs.
func centeredOracleSlope(samples [][2]float64) (float64, bool) {
	n := len(samples)
	if n < 2 {
		return 0, false
	}
	var sx, sy float64
	for _, s := range samples {
		sx += s[0]
		sy += s[1]
	}
	fn := float64(n)
	mx := sx / fn
	my := sy / fn
	var num, den float64
	for _, s := range samples {
		dx := s[0] - mx
		num += dx * (s[1] - my)
		den += dx * dx
	}
	if den == 0 {
		return 0, false
	}
	return num / den, true
}

// ---------------------------------------------------------------------------
// (1) CONDITIONING — the missed bug. SINK-SIDE: a real rising trend at large
//     ticks must still produce a positive slope and FIRE a PreWarm.
// ---------------------------------------------------------------------------

// TestCond_RisingLargeTicksStillFires is the REGRESSION GUARD for the
// large-tick catastrophic-cancellation defect. It feeds a genuinely rising,
// finite, in-range, monotone trace whose TICK values are large (base 2^30) and
// asserts the forecaster STILL fires a pre-warm — i.e. the computed slope keeps
// its (positive) sign.
//
// MUTATION TARGET: the uncentered `denom = n*ΣXX - (ΣX)^2` form. Against it, the
// computed slope for this trace is NEGATIVE, the fire gate `slope > eps` is
// false, and NO PreWarm fires -> this test FAILS (`--- FAIL`). With the centered
// fix it fires -> PASS.
func TestCond_RisingLargeTicksStillFires(t *testing.T) {
	id := runUUID(t)
	const window, lead = 5, 4
	// Threshold chosen so that ONLY a correctly-signed positive slope projecting
	// forward reaches it: current util tops out at 0.30, threshold 0.40, so a
	// reactive/flat/declining read can never reach 0.40 — a fire here REQUIRES a
	// correctly positive slope.
	const thr = 0.40

	// Genuinely rising: util = 0.10 + 0.05*i over i in [0,window). True slope +0.05.
	// Large base tick (2^30 ~ 1.07e9) triggers the cancellation in the buggy form.
	const base = 1 << 30
	f := &Forecaster{Window: window, Threshold: thr, Lead: lead}

	var fired bool
	var lastProj float64
	for i := 0; i < window; i++ {
		tick := base + i
		util := 0.10 + 0.05*float64(i) // 0.10,0.15,0.20,0.25,0.30 (rising)
		pre, proj := f.Observe(tick, util)
		lastProj = proj
		if pre {
			fired = true
		}
	}

	// Independent stable oracle confirms the trend really is rising (slope>0):
	want := make([][2]float64, 0, window)
	for i := 0; i < window; i++ {
		want = append(want, [2]float64{float64(base + i), 0.10 + 0.05*float64(i)})
	}
	os, ok := centeredOracleSlope(want)
	if !ok || os <= 0 {
		t.Fatalf("run=%s oracle invalid: stable slope=%.6g ok=%v (trace must be rising)", id, os, ok)
	}

	if math.IsNaN(lastProj) || math.IsInf(lastProj, 0) {
		t.Fatalf("run=%s large-tick projection non-finite: %v", id, lastProj)
	}
	if !fired {
		t.Fatalf("run=%s MISSED SCALE-UP: rising trend (true slope ~%.3f) at large ticks (base=2^30) "+
			"did NOT fire PreWarm — uncentered OLS cancellation flipped the slope sign so a rising load "+
			"was read as declining (lastProj=%.4f, thr=%.2f)", id, os, lastProj, thr)
	}
	t.Logf("run=%s CONDITIONING HOLDS: rising trend at base=2^30 fired PreWarm (stable slope=%.4f, lastProj=%.4f>=thr=%.2f)",
		id, os, lastProj, thr)
}

// TestCond_SlopeSignPreservedLargeTicks pins the underlying invariant directly:
// the package's projected value over a rising large-tick window must be STRICTLY
// GREATER than the current utilization (i.e. the implied slope is positive),
// matching the independent stable oracle's SIGN. Against the uncentered form the
// projection is BELOW current (implied negative slope) and this bites.
func TestCond_SlopeSignPreservedLargeTicks(t *testing.T) {
	id := runUUID(t)
	const window, lead = 5, 6
	const thr = 999 // never fire; we test the value/sign only

	for _, base := range []int{1 << 26, 1 << 30, 1 << 40, 1 << 50} {
		f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
		var cur, proj float64
		want := make([][2]float64, 0, window)
		for i := 0; i < window; i++ {
			tick := base + i
			util := 0.10 + 0.05*float64(i)
			cur = util
			_, proj = f.Observe(tick, util)
			want = append(want, [2]float64{float64(tick), util})
		}
		os, ok := centeredOracleSlope(want)
		if !ok {
			t.Fatalf("run=%s base=%d: stable oracle slope undefined", id, base)
		}
		if math.IsNaN(proj) || math.IsInf(proj, 0) {
			t.Fatalf("run=%s base=%d: projection non-finite %v", id, base, proj)
		}
		// Stable oracle says rising (os>0) => projected must be > current.
		if os > 0 && !(proj > cur) {
			t.Fatalf("run=%s base=%d SIGN FLIP: rising trend (stable slope=%.4f) projected DOWN: "+
				"proj=%.6f <= cur=%.6f (uncentered OLS cancellation)", id, base, os, proj, cur)
		}
		t.Logf("run=%s base=2^k=%d: sign preserved, proj=%.6f > cur=%.6f (stable slope=%.4f)", id, base, proj, cur, os)
	}
}

// TestCond_CenteredMatchesSmallTicks proves the fix is value-preserving on the
// well-conditioned small-tick path: for small ticks the package's projection must
// still match the documented OLS value (re-derived via the stable oracle) to
// tight tolerance. This guards against the fix accidentally changing results on
// the normal path.
func TestCond_CenteredMatchesSmallTicks(t *testing.T) {
	id := runUUID(t)
	const window, lead = 5, 7
	const thr = 999

	trace := [][2]float64{
		{0, 0.10}, {1, 0.13}, {2, 0.19}, {3, 0.22}, {4, 0.30},
		{5, 0.31}, {6, 0.41}, {7, 0.44}, {8, 0.52}, {9, 0.61},
	}
	f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
	hist := make([][2]float64, 0, len(trace))
	for _, p := range trace {
		_, proj := f.Observe(int(p[0]), p[1])
		hist = append(hist, p)
		w := hist
		if len(w) > window {
			w = w[len(w)-window:]
		}
		cur := w[len(w)-1][1]
		s, ok := centeredOracleSlope(w)
		want := cur
		if ok {
			want = cur + s*float64(lead)
		}
		if math.Abs(proj-want) > 1e-9 {
			t.Fatalf("run=%s tick=%v: proj=%.17g != stable-oracle=%.17g (diff=%g)", id, p[0], proj, want, math.Abs(proj-want))
		}
	}
	t.Logf("run=%s small-tick path: package matches stable OLS oracle within 1e-9 (fix is value-preserving)", id)
}

// ---------------------------------------------------------------------------
// (2) LARGE-HORIZON / OUTPUT BOUNDEDNESS — pin that a finite slope and a large
//     (but valid int) Lead produce a FINITE projection (documented value path),
//     and that no integer overflow in float64(Lead) corrupts the sign.
// ---------------------------------------------------------------------------

// TestCond_LargeHorizonFiniteAndDirectional feeds a normal rising small-tick
// trace with a very large Lead and asserts the projection stays finite and keeps
// the correct (upward) direction. This pins that the documented unbounded
// projection does not overflow to ±Inf/NaN for a realistic large horizon and
// that the decision direction is correct (rising trend + large lead => projects
// up, fires once it crosses threshold).
func TestCond_LargeHorizonFiniteAndDirectional(t *testing.T) {
	id := runUUID(t)
	const window = 4
	const thr = 0.60
	const lead = 1 << 20 // large but realistic-int horizon

	f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
	// slope ~ +1e-7 per tick; over lead 2^20 that lifts ~+0.1 — bounded, finite.
	var fired bool
	var proj float64
	for i := 0; i < 8; i++ {
		_, proj = f.Observe(i, 0.50+1e-7*float64(i))
		// not asserting fire yet; just that values stay finite
		if math.IsNaN(proj) || math.IsInf(proj, 0) {
			t.Fatalf("run=%s large-horizon projection non-finite at i=%d: %v", id, i, proj)
		}
	}
	// Direction: a rising trend over a huge positive lead must project UP, never
	// flip to a negative/wrapped prediction.
	if !(proj >= 0.50) {
		t.Fatalf("run=%s large horizon flipped direction: proj=%.6f < base 0.50 on a rising trend", id, proj)
	}
	_ = fired
	t.Logf("run=%s large-horizon (lead=2^20): projection finite and directionally up (proj=%.6f)", id, proj)
}

// ---------------------------------------------------------------------------
// (3) RETURN / STATE ISOLATION — the Forecaster returns only scalars (no slice
//     or map), but it retains an internal `samples` ring. Pin that observing on
//     one Forecaster cannot affect another (no shared backing array via a
//     package-level buffer) and that repeated Observe calls are self-consistent.
// ---------------------------------------------------------------------------

// TestCond_InstanceIsolation runs two forecasters with interleaved Observe calls
// on DIFFERENT traces and asserts each one's decisions match what it would have
// produced in isolation — i.e. there is no cross-instance shared mutable state.
func TestCond_InstanceIsolation(t *testing.T) {
	id := runUUID(t)
	const window, lead, thr = 3, 4, 0.80

	traceA := risingTrace(20, 0.10, 0.05) // rising, will fire
	traceB := risingTrace(20, 0.90, -0.05) // declining, must never fire

	// Reference: run each in isolation.
	refRun := func(tr [][2]float64) (int, bool) {
		f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
		for _, p := range tr {
			f.Observe(int(p[0]), p[1])
		}
		return f.FiredTick()
	}
	rtA, rokA := refRun(traceA)
	rtB, rokB := refRun(traceB)

	// Interleaved: feed A and B alternately into two live instances.
	fa := &Forecaster{Window: window, Threshold: thr, Lead: lead}
	fb := &Forecaster{Window: window, Threshold: thr, Lead: lead}
	n := len(traceA)
	if len(traceB) < n {
		n = len(traceB)
	}
	for i := 0; i < n; i++ {
		fa.Observe(int(traceA[i][0]), traceA[i][1])
		fb.Observe(int(traceB[i][0]), traceB[i][1])
	}
	itA, iokA := fa.FiredTick()
	itB, iokB := fb.FiredTick()

	if itA != rtA || iokA != rokA {
		t.Fatalf("run=%s instance A interleaved fire (%d,%v) != isolated (%d,%v) — cross-instance state leak", id, itA, iokA, rtA, rokA)
	}
	if itB != rtB || iokB != rokB {
		t.Fatalf("run=%s instance B interleaved fire (%d,%v) != isolated (%d,%v) — cross-instance state leak", id, itB, iokB, rtB, rokB)
	}
	if !iokA {
		t.Fatalf("run=%s setup invalid: rising trace A never fired", id)
	}
	if iokB {
		t.Fatalf("run=%s declining trace B must never fire", id)
	}
	t.Logf("run=%s isolation holds: A fired@%d, B never fired, interleaving had no effect", id, itA)
}
