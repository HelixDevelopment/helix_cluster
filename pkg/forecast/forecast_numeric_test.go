package forecast

import (
	"math"
	"testing"
)

// This file is an adversarial NUMERIC-CORRECTNESS suite for the Forecaster.
//
// The existing forecast_test.go proves *behavioural* properties (pre-emptive
// fire timing, no-false-positive on flat/declining traces, determinism) but it
// never independently re-derives the documented forecast VALUE, never pins the
// exact window membership, and only ever uses a single straight-line rising
// trace. The tests below close that gap:
//
//   1. Formula correctness: an independent, hand-written ordinary-least-squares
//      oracle (oracleSlope/oracleProj) recomputes projected = current+slope*Lead
//      over the documented trailing window and the package output is asserted to
//      match it bit-tight (<=1e-12) across rising, falling, flat, oscillating,
//      noisy, step-change, single-sample, and short-window traces.
//   2. Directional property: strictly rising -> projected >= current; strictly
//      falling -> projected <= current; flat -> projected == current within
//      float noise (constant-trace OLS slope is a rounding residual, not bit 0).
//   3. Exact window: a step-change trace where last-N, last-(N-1) and last-(N+1)
//      windows give materially different slopes pins the window to exactly N.
//   4. Edge cases: empty (never observed), single sample, and fewer-than-window
//      samples follow the documented "no slope -> projected == current, no fire".
//
// ANTI-BLUFF: every assertion is against an oracle re-derived from the package
// doc-comment ("projected = current + slope*Lead", least-squares over the
// trailing Window), NOT against the package's own output. A wrong alpha-style
// weight, a wrong slope sign, or an off-by-one in the window would make the
// package diverge from the oracle. TestNumeric_MutationBites at the bottom
// re-implements three documented-but-WRONG variants (reactive, last-2-only,
// flipped-sign) and proves each produces a value the correct package rejects,
// so the oracle has real teeth.

// oracleTrim returns exactly the trailing `window` samples — the independent
// re-implementation of the documented "trailing window of the most recent
// Window samples". An off-by-one here vs. the package would surface as a slope
// mismatch.
func oracleTrim(samples [][2]float64, window int) [][2]float64 {
	if window > 0 && len(samples) > window {
		return samples[len(samples)-window:]
	}
	return samples
}

// oracleSlope independently computes the ordinary-least-squares slope of util
// vs tick over the given samples, mirroring the documented formula
// slope = (n*ΣXY - ΣX*ΣY) / (n*ΣXX - (ΣX)^2). Returns ok=false for <2 samples
// or a degenerate (zero-variance-in-x) spread, exactly as documented.
func oracleSlope(samples [][2]float64) (float64, bool) {
	n := len(samples)
	if n < 2 {
		return 0, false
	}
	var sx, sy, sxy, sxx float64
	for _, s := range samples {
		x, y := s[0], s[1]
		sx += x
		sy += y
		sxy += x * y
		sxx += x * x
	}
	fn := float64(n)
	den := fn*sxx - sx*sx
	if den == 0 {
		return 0, false
	}
	return (fn*sxy - sx*sy) / den, true
}

// oracleProj is the independent forecast value: documented projected =
// current + slope*Lead; when slope is undefined the projection is current.
func oracleProj(history [][2]float64, window, lead int) (proj float64, slopeDefined bool) {
	w := oracleTrim(history, window)
	cur := w[len(w)-1][1]
	slope, ok := oracleSlope(w)
	if !ok {
		return cur, false
	}
	return cur + slope*float64(lead), true
}

const numTol = 1e-12

// TestNumeric_FormulaAcrossTraces feeds many trace SHAPES and asserts, at every
// single Observe call, that the package's projected value equals the
// independently re-derived OLS oracle within a tight tolerance. This is the
// core anti-bluff: the package never gets to "grade its own homework".
//
// WHY IT BITES: the oracle is derived solely from the doc-comment formula. If
// the package used a wrong slope (e.g. a different weighting, a dropped sample,
// or a sign flip) the running per-tick value would diverge from the oracle and
// fail. Covered shapes: rising, falling, flat, oscillating, noisy-but-trending,
// step-change, and degenerate (single / sub-window) prefixes — each exercised
// implicitly because the assertion runs from the very first sample.
func TestNumeric_FormulaAcrossTraces(t *testing.T) {
	id := runUUID(t)
	const window, lead = 5, 7
	const thr = 0.80

	traces := map[string][][2]float64{
		// strictly rising straight line
		"rising": linTrace(20, 0.05, 0.04),
		// strictly falling straight line
		"falling": linTrace(20, 0.95, -0.03),
		// perfectly flat
		"flat": linTrace(20, 0.42, 0.0),
		// oscillating square-ish wave around 0.5 (sign of slope flips per window)
		"oscill": oscTrace(24, 0.5, 0.3),
		// rising line with deterministic +/- jitter (noise) on top of a real trend
		"noisy": noisyTrace(24, 0.10, 0.03),
		// flat then a sharp upward step partway through (window must "forget" old flat)
		"step": stepTrace(0.20, 0.85, 12, 24),
		// non-unit, non-monotone tick spacing to stress the OLS denom term
		"gappy": gappyTrace(),
	}

	for name, trace := range traces {
		t.Run(name, func(t *testing.T) {
			f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
			hist := make([][2]float64, 0, len(trace))
			maxAbs := 0.0
			for i, p := range trace {
				_, proj := f.Observe(int(p[0]), p[1])
				hist = append(hist, p)
				want, _ := oracleProj(hist, window, lead)
				d := math.Abs(proj - want)
				if d > maxAbs {
					maxAbs = d
				}
				if d > numTol {
					t.Fatalf("run=%s trace=%s i=%d tick=%v: proj=%.17g != oracle=%.17g (diff=%g > tol=%g)",
						id, name, i, p[0], proj, want, d, numTol)
				}
			}
			t.Logf("run=%s trace=%s: %d samples, projected matched OLS oracle (max|diff|=%g <= %g)",
				id, name, len(trace), maxAbs, numTol)
		})
	}
}

// TestNumeric_DirectionalProperty asserts the documented sign relationship
// between the projection and the current value once a slope is defined:
//   - strictly rising window  => projected >= current (and strictly > for lead>0)
//   - strictly falling window => projected <= current
//   - flat window             => projected == current within float noise
//
// WHY IT BITES: a sign-flipped slope (projected = current - slope*Lead) would
// make a rising trace project DOWN and a falling trace project UP — caught here
// directly. The flat case pins projected to current within numTol: any spurious
// drift term (e.g. an added intercept/EWMA bias) would break it.
func TestNumeric_DirectionalProperty(t *testing.T) {
	id := runUUID(t)
	const window, lead = 4, 6
	const thr = 0.80

	t.Run("rising_up", func(t *testing.T) {
		f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
		trace := linTrace(15, 0.10, 0.05)
		checks := 0
		for _, p := range trace {
			_, proj := f.Observe(int(p[0]), p[1])
			cur := p[1]
			if _, ok := oracleProj(append([][2]float64(nil), pairsUpTo(trace, p)...), window, lead); ok {
				if !(proj >= cur) {
					t.Fatalf("run=%s rising: proj=%.6f must be >= current=%.6f", id, proj, cur)
				}
				checks++
			}
		}
		if checks == 0 {
			t.Fatalf("run=%s rising: no slope-defined ticks — vacuous", id)
		}
		t.Logf("run=%s rising: %d slope-defined ticks all projected >= current", id, checks)
	})

	t.Run("falling_down", func(t *testing.T) {
		f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
		trace := linTrace(15, 0.95, -0.05)
		checks := 0
		for _, p := range trace {
			_, proj := f.Observe(int(p[0]), p[1])
			cur := p[1]
			if _, ok := oracleProj(pairsUpTo(trace, p), window, lead); ok {
				if !(proj <= cur) {
					t.Fatalf("run=%s falling: proj=%.6f must be <= current=%.6f", id, proj, cur)
				}
				checks++
			}
		}
		if checks == 0 {
			t.Fatalf("run=%s falling: no slope-defined ticks — vacuous", id)
		}
		t.Logf("run=%s falling: %d slope-defined ticks all projected <= current", id, checks)
	})

	t.Run("flat_equal", func(t *testing.T) {
		f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
		const c = 0.37
		trace := linTrace(15, c, 0.0)
		checks := 0
		for _, p := range trace {
			_, proj := f.Observe(int(p[0]), p[1])
			if _, ok := oracleProj(pairsUpTo(trace, p), window, lead); ok {
				// On a constant trace the documented OLS slope is ~0 — but least-
				// squares over identical y still yields a rounding-residual slope
				// (~1e-16), so projected = current + slope*Lead lands within float
				// noise of current, NOT necessarily bit-identical. The documented
				// directional property for a flat trend is "projects the flat
				// value": assert |projected - current| is at float-noise scale.
				// (A real drift term — e.g. an EWMA bias or a non-zero intercept
				// added to the projection — would push this well past numTol.)
				if math.Abs(proj-c) > numTol {
					t.Fatalf("run=%s flat: proj=%.17g must equal current=%.17g within %g (got diff=%g)",
						id, proj, c, numTol, math.Abs(proj-c))
				}
				checks++
			}
		}
		if checks == 0 {
			t.Fatalf("run=%s flat: no slope-defined ticks — vacuous", id)
		}
		t.Logf("run=%s flat: %d slope-defined ticks all projected == current within %g", id, checks, numTol)
	})
}

// TestNumeric_ExactWindow pins the window to EXACTLY the last N samples using a
// step trace constructed so that the OLS slope over last-N, last-(N-1) and
// last-(N+1) samples are all materially different. The package's projected
// value must match ONLY the last-N oracle.
//
// WHY IT BITES: an off-by-one in the trim (keeping N+1 or N-1) would shift the
// included samples; on a step trace those alternative windows give a different
// slope and therefore a different projection. We assert the package matches
// last-N and DOES NOT match last-(N±1), so the test fails for either off-by-one.
func TestNumeric_ExactWindow(t *testing.T) {
	id := runUUID(t)
	const window, lead = 4, 10
	const thr = 999 // never fire; we only care about the value

	// Flat 0.0 region then a steepening ramp so neighbouring windows disagree.
	trace := [][2]float64{
		{0, 0.00}, {1, 0.00}, {2, 0.00}, {3, 0.00},
		{4, 0.05}, {5, 0.15}, {6, 0.30}, {7, 0.50}, {8, 0.75},
	}
	f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
	hist := make([][2]float64, 0, len(trace))
	distinctTicks := 0
	for _, p := range trace {
		_, proj := f.Observe(int(p[0]), p[1])
		hist = append(hist, p)

		wantN, _ := oracleProj(hist, window, lead)
		if math.Abs(proj-wantN) > numTol {
			t.Fatalf("run=%s tick=%v: proj=%.17g != last-%d oracle=%.17g", id, p[0], proj, window, wantN)
		}

		// Where enough history exists, the N±1 windows must give DIFFERENT
		// projections than the correct N window — otherwise the off-by-one
		// guard is vacuous at this tick. Assert the package disagrees with them.
		if len(hist) > window {
			wantNm1, _ := oracleProj(hist, window-1, lead)
			wantNp1, _ := oracleProj(hist, window+1, lead)
			if math.Abs(wantNm1-wantN) > 1e-6 && math.Abs(wantNp1-wantN) > 1e-6 {
				distinctTicks++
				if math.Abs(proj-wantNm1) <= numTol {
					t.Fatalf("run=%s tick=%v: package matches WRONG last-%d window (off-by-one)", id, p[0], window-1)
				}
				if math.Abs(proj-wantNp1) <= numTol {
					t.Fatalf("run=%s tick=%v: package matches WRONG last-%d window (off-by-one)", id, p[0], window+1)
				}
			}
		}
	}
	if distinctTicks == 0 {
		t.Fatalf("run=%s exact-window test vacuous: no tick where N±1 windows differed", id)
	}
	t.Logf("run=%s exact-window: matched last-%d at every tick; %d ticks proved distinct from last-%d/last-%d",
		id, window, distinctTicks, window-1, window+1)
}

// TestNumeric_EdgeCases pins the documented behaviour at the boundaries: a
// never-observed forecaster, a single sample, and fewer-than-window samples.
//
// WHY IT BITES: the doc says "fewer than 2 samples => projection equals current
// value and PreWarm is false". A bug that projected a slope off <2 samples
// (e.g. dividing by a zero denom and producing NaN/Inf, or firing) would fail.
func TestNumeric_EdgeCases(t *testing.T) {
	id := runUUID(t)
	const window, lead = 5, 4
	const thr = 0.10 // deliberately LOW so a buggy early fire would be exposed

	t.Run("never_observed", func(t *testing.T) {
		f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
		if tick, ok := f.FiredTick(); ok {
			t.Fatalf("run=%s never-observed forecaster reports fired at %d", id, tick)
		}
		t.Logf("run=%s never-observed: no fire, no panic", id)
	})

	t.Run("single_sample", func(t *testing.T) {
		f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
		const u = 0.55 // well above the 0.10 threshold
		pre, proj := f.Observe(0, u)
		if pre {
			t.Fatalf("run=%s single sample must not fire (slope undefined)", id)
		}
		if proj != u {
			t.Fatalf("run=%s single sample: projected=%.17g must equal current=%.17g", id, proj, u)
		}
		if _, ok := f.FiredTick(); ok {
			t.Fatalf("run=%s single sample: FiredTick reports a fire", id)
		}
		t.Logf("run=%s single sample: projected==current=%.2f, no fire", id, u)
	})

	t.Run("below_window_count", func(t *testing.T) {
		// window=5 but feed only 3 samples; a slope IS defined for >=2 samples,
		// so verify the projection still matches the OLS oracle over whatever
		// (<window) history exists — and never NaN/Inf.
		f := &Forecaster{Window: window, Threshold: 999, Lead: lead}
		trace := [][2]float64{{0, 0.20}, {1, 0.24}, {2, 0.28}}
		hist := make([][2]float64, 0, len(trace))
		for _, p := range trace {
			_, proj := f.Observe(int(p[0]), p[1])
			hist = append(hist, p)
			if math.IsNaN(proj) || math.IsInf(proj, 0) {
				t.Fatalf("run=%s sub-window projection not finite: %v", id, proj)
			}
			want, _ := oracleProj(hist, window, lead)
			if math.Abs(proj-want) > numTol {
				t.Fatalf("run=%s sub-window tick=%v: proj=%.17g != oracle=%.17g", id, p[0], proj, want)
			}
		}
		t.Logf("run=%s sub-window: %d(<window=%d) samples projected per OLS oracle, finite", id, len(trace), window)
	})
}

// TestNumeric_MutationBites is the explicit anti-bluff demonstration. It
// re-implements three plausible WRONG forecasters and proves that, on a chosen
// trace, each produces a projected value the CORRECT package does not — i.e.
// the oracle/tolerance above is tight enough to separate right from wrong. If a
// future regression turned the package into one of these mutants, the formula
// test would fail. Here we prove the gap is real rather than asserting it.
func TestNumeric_MutationBites(t *testing.T) {
	id := runUUID(t)
	const window, lead = 5, 7
	const thr = 999

	trace := noisyTrace(24, 0.10, 0.03)
	f := &Forecaster{Window: window, Threshold: thr, Lead: lead}

	type mut struct {
		name    string
		project func(hist [][2]float64) float64
		bit     bool // did it ever differ materially from the package?
	}
	muts := []*mut{
		// reactive: ignores slope entirely (projected = current)
		{name: "reactive(no-slope)", project: func(h [][2]float64) float64 {
			w := oracleTrim(h, window)
			return w[len(w)-1][1]
		}},
		// last-2-only: an off-by-window using just the last two samples
		{name: "last2-slope", project: func(h [][2]float64) float64 {
			w := oracleTrim(h, window)
			if len(w) < 2 {
				return w[len(w)-1][1]
			}
			last2 := w[len(w)-2:]
			s, _ := oracleSlope(last2)
			return last2[len(last2)-1][1] + s*float64(lead)
		}},
		// flipped-sign: projects against the trend
		{name: "flipped-sign", project: func(h [][2]float64) float64 {
			w := oracleTrim(h, window)
			cur := w[len(w)-1][1]
			s, ok := oracleSlope(w)
			if !ok {
				return cur
			}
			return cur - s*float64(lead)
		}},
	}

	hist := make([][2]float64, 0, len(trace))
	for _, p := range trace {
		_, proj := f.Observe(int(p[0]), p[1])
		hist = append(hist, p)
		for _, m := range muts {
			if math.Abs(proj-m.project(hist)) > 1e-6 {
				m.bit = true
			}
		}
	}
	for _, m := range muts {
		if !m.bit {
			t.Fatalf("run=%s mutation %q NEVER diverged from package — oracle has no teeth against it", id, m.name)
		}
		t.Logf("run=%s mutation %q diverges from correct package (oracle bites it)", id, m.name)
	}
}

// ---- deterministic trace builders (no randomness; reproducible) ----

// linTrace: util(tick) = base + step*tick for tick in [0,n).
func linTrace(n int, base, step float64) [][2]float64 {
	tr := make([][2]float64, n)
	for i := 0; i < n; i++ {
		tr[i] = [2]float64{float64(i), base + step*float64(i)}
	}
	return tr
}

// oscTrace: square-ish oscillation between center-amp and center+amp.
func oscTrace(n int, center, amp float64) [][2]float64 {
	tr := make([][2]float64, n)
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			tr[i] = [2]float64{float64(i), center + amp}
		} else {
			tr[i] = [2]float64{float64(i), center - amp}
		}
	}
	return tr
}

// noisyTrace: a real rising trend base+step*tick with a deterministic, bounded,
// zero-mean-ish sawtooth perturbation so the slope is non-trivial but the trend
// is real (used to ensure the OLS slope is exercised, not a clean line).
func noisyTrace(n int, base, step float64) [][2]float64 {
	tr := make([][2]float64, n)
	jit := []float64{+0.02, -0.015, +0.01, -0.02, +0.005}
	for i := 0; i < n; i++ {
		v := base + step*float64(i) + jit[i%len(jit)]
		if v < 0 {
			v = 0
		}
		tr[i] = [2]float64{float64(i), v}
	}
	return tr
}

// stepTrace: flat at lo until tick `at`, then flat at hi, over [0,n).
func stepTrace(lo, hi float64, at, n int) [][2]float64 {
	tr := make([][2]float64, n)
	for i := 0; i < n; i++ {
		if i < at {
			tr[i] = [2]float64{float64(i), lo}
		} else {
			tr[i] = [2]float64{float64(i), hi}
		}
	}
	return tr
}

// gappyTrace: non-unit, increasing-but-uneven tick spacing to stress the OLS
// denominator (n*ΣXX - (ΣX)^2) with non-trivial X values.
func gappyTrace() [][2]float64 {
	return [][2]float64{
		{0, 0.10}, {2, 0.18}, {5, 0.33}, {9, 0.40},
		{14, 0.55}, {20, 0.62}, {27, 0.80}, {35, 0.88},
	}
}

// pairsUpTo returns the prefix of trace up to and including the element equal
// to p (by tick). Used so the directional test can build the same trailing
// history the package has seen at that point.
func pairsUpTo(trace [][2]float64, p [2]float64) [][2]float64 {
	out := make([][2]float64, 0, len(trace))
	for _, q := range trace {
		out = append(out, q)
		if q[0] == p[0] {
			break
		}
	}
	return out
}
