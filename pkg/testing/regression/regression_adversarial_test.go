package regression

import (
	"errors"
	"math"
	"testing"
)

// adversarialBaseline / adversarialCandidate model a HigherIsWorse (latency)
// benchmark where the candidate is a clear, real regression (~20% slower) with
// modest noise. We mutate ONE candidate observation into NaN/Inf to prove the
// detector cannot be made to bluff an all-clear verdict on corrupt data.
func adversarialFixture() (base, cand []float64) {
	base = []float64{100, 101, 99, 100, 100, 101, 99, 100, 100, 100}
	cand = []float64{120, 121, 119, 120, 120, 121, 119, 120, 120, 120}
	return base, cand
}

// TestAdversarial_NaNInCandidateMustNotBluffNoChange is the highest-value probe:
// a single NaN in the candidate sample (a failed/corrupt benchmark measurement)
// MUST NOT be silently reported as NO-CHANGE with a nil error. Without the
// allFinite guard, mc/vc/seSq/t/p all become NaN, `NaN < Alpha` is false, the
// significance guard short-circuits, and Detect returns (NoChange, nil) — a
// false-negative that hides every real regression in that run.
//
// MUTATION PROOF: deleting the `!allFinite(...)` guard in Detect makes this test
// FAIL (Detect returns NoChange/nil instead of ErrNonFinite).
func TestAdversarial_NaNInCandidateMustNotBluffNoChange(t *testing.T) {
	base, cand := adversarialFixture()
	cand[2] = math.NaN()

	res, err := Detect(base, cand, DefaultOptions())

	if !errors.Is(err, ErrNonFinite) {
		t.Fatalf("NaN candidate must be rejected with ErrNonFinite; got err=%v verdict=%s p=%v",
			err, res.Verdict, res.PValue)
	}
	// And it must NOT have produced a usable, falsely-clean verdict.
	if err == nil && res.Verdict == NoChange {
		t.Fatalf("BLUFF: NaN-poisoned candidate reported NO-CHANGE with nil error")
	}
}

// TestAdversarial_NaNInBaselineMustNotBluff mirrors the above for a corrupt
// baseline sample.
func TestAdversarial_NaNInBaselineMustNotBluff(t *testing.T) {
	base, cand := adversarialFixture()
	base[0] = math.NaN()

	_, err := Detect(base, cand, DefaultOptions())
	if !errors.Is(err, ErrNonFinite) {
		t.Fatalf("NaN baseline must be rejected with ErrNonFinite, got %v", err)
	}
}

// TestAdversarial_InfInCandidateMustNotBluffNoChange: +Inf is the other
// non-finite poison. With Inf in the candidate, seSq=+Inf, se=+Inf, t=NaN,
// p=NaN, and (like NaN) `NaN < Alpha` is false => NO-CHANGE without the guard.
//
// MUTATION PROOF: without the allFinite guard, Detect returns (NoChange, nil)
// and this fails.
func TestAdversarial_InfInCandidateMustNotBluffNoChange(t *testing.T) {
	base, cand := adversarialFixture()
	for _, inf := range []float64{math.Inf(1), math.Inf(-1)} {
		c := append([]float64(nil), cand...)
		c[3] = inf
		res, err := Detect(base, c, DefaultOptions())
		if !errors.Is(err, ErrNonFinite) {
			t.Fatalf("Inf=%v candidate must be rejected with ErrNonFinite; got err=%v verdict=%s",
				inf, err, res.Verdict)
		}
	}
}

// TestAdversarial_CleanRegressionJustPastThresholdIsDetected confirms the guard
// did not break the happy path: a finite, clear regression is still flagged. A
// NaN reject that also suppressed real detections would be useless.
func TestAdversarial_CleanRegressionStillDetected(t *testing.T) {
	base, cand := adversarialFixture()
	res, err := Detect(base, cand, DefaultOptions())
	if err != nil {
		t.Fatalf("unexpected error on clean data: %v", err)
	}
	if res.Verdict != Regression {
		t.Fatalf("expected REGRESSION on clean 20%% slowdown, got %s", res.Verdict)
	}
}

// TestAdversarial_BarelyPastEffectGuardIsDetected probes the boundary of the
// MinEffectSize guard from the FALSE-NEGATIVE side: an effect just ABOVE the
// guard, with overwhelming significance, MUST be flagged. The guard is `<`
// (EffectSize < MinEffectSize => suppressed), so an effect strictly greater than
// the guard must pass.
//
// MUTATION PROOF: changing the guard to `<=` (or `EffectSize <= MinEffectSize`)
// would still pass here because our effect is strictly greater; the companion
// exactly-at-threshold test below pins the boundary direction.
func TestAdversarial_EffectJustAboveGuardDetected(t *testing.T) {
	// candidate mean is 3% above baseline (> 2% default guard), low noise.
	const n = 200
	base := make([]float64, n)
	cand := make([]float64, n)
	for i := 0; i < n; i++ {
		noise := 0.05
		if i%2 == 1 {
			noise = -0.05
		}
		base[i] = 100.0 + noise
		cand[i] = 103.0 + noise // 3% higher
	}
	res, err := Detect(base, cand, DefaultOptions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Significant {
		t.Fatalf("expected statistically significant result, p=%v", res.PValue)
	}
	if res.Verdict != Regression {
		t.Fatalf("3%% slowdown (> 2%% guard) must be REGRESSION, got %s (effect=%v)",
			res.Verdict, res.EffectSize)
	}
}

// TestAdversarial_EffectExactlyAtGuardBoundary pins the `<` boundary semantics:
// an effect size EXACTLY equal to MinEffectSize is NOT suppressed (the guard is
// strict `<`, so effect == guard passes the guard). This is a documented-contract
// pin guarding against an accidental flip to `<=` which would turn an
// at-threshold real regression into a false-negative NO-CHANGE.
func TestAdversarial_EffectExactlyAtGuardNotSuppressed(t *testing.T) {
	// Construct an exact 5% effect and set MinEffectSize to exactly 0.05.
	// baseline mean = 100 exactly, candidate mean = 105 exactly.
	base := []float64{100, 101, 99, 100, 101, 99, 100, 100}
	cand := []float64{105, 106, 104, 105, 106, 104, 105, 105}
	mb, _ := meanVar(base)
	mc, _ := meanVar(cand)
	wantEffect := math.Abs(mc-mb) / math.Abs(mb)

	opts := DefaultOptions()
	opts.MinEffectSize = wantEffect // exactly at the boundary

	res, err := Detect(base, cand, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(res.EffectSize-wantEffect) > 1e-12 {
		t.Fatalf("effect size precondition failed: got %v want %v", res.EffectSize, wantEffect)
	}
	// effect == guard, guard is strict `<`, so it is NOT suppressed => REGRESSION.
	if res.Verdict != Regression {
		t.Fatalf("effect exactly at guard must NOT be suppressed (strict <); got %s", res.Verdict)
	}
}

// TestAdversarial_ZeroBaselineMeanNoDivByZeroFalsePass guards the baseline==0
// branch of the effect-size computation. When baseline mean is 0, the relative
// change would be a div-by-zero; the code falls back to the absolute difference.
// A real shift away from zero must still be detectable, not silently NaN/clean.
func TestAdversarial_ZeroBaselineMeanStillDetects(t *testing.T) {
	base := []float64{-1, 1, -1, 1, -1, 1} // mean 0
	cand := []float64{5, 6, 5, 6, 5, 6}    // mean 5.5
	mb, _ := meanVar(base)
	if mb != 0 {
		t.Fatalf("fixture precondition: baseline mean must be 0, got %v", mb)
	}
	res, err := Detect(base, cand, DefaultOptions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.IsNaN(res.EffectSize) {
		t.Fatalf("effect size must not be NaN when baseline mean is 0")
	}
	if res.Verdict != Regression {
		t.Fatalf("clear shift from zero baseline must be REGRESSION, got %s", res.Verdict)
	}
}

// TestAdversarial_ImprovementNotFlaggedAsRegression: a candidate clearly BETTER
// (lower latency) must never be mislabelled a regression — the inverse
// false-positive direction.
func TestAdversarial_ImprovementNotRegression(t *testing.T) {
	base, _ := adversarialFixture()
	cand := []float64{80, 81, 79, 80, 80, 81, 79, 80, 80, 80}
	res, err := Detect(base, cand, DefaultOptions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Verdict == Regression {
		t.Fatalf("a clear speedup must not be flagged REGRESSION; got %s", res.Verdict)
	}
	if res.Verdict != Improvement {
		t.Fatalf("expected IMPROVEMENT, got %s", res.Verdict)
	}
}

// TestAdversarial_EmptyAndShortSeries: nil and single-element series are rejected
// (variance undefined), never silently treated as NO-CHANGE.
func TestAdversarial_EmptyAndShortSeries(t *testing.T) {
	cases := [][2][]float64{
		{nil, {1, 2, 3}},
		{{1, 2, 3}, nil},
		{{}, {}},
		{{1}, {1, 2}},
		{{1, 2}, {1}},
	}
	for i, c := range cases {
		if _, err := Detect(c[0], c[1], DefaultOptions()); !errors.Is(err, ErrInsufficientData) {
			t.Fatalf("case %d: expected ErrInsufficientData, got %v", i, err)
		}
	}
}
