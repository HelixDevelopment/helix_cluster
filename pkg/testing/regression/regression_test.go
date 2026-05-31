package regression

import (
	"errors"
	"math"
	"math/rand"
	"testing"
)

// round4 rounds to 4 decimal places for pinning exact t-statistic values.
func round4(f float64) float64 { return math.Round(f*1e4) / 1e4 }

// clearRegressionFixture: baseline ~100, candidate ~120, both low variance.
// Hand-computed (see PY derivation): baseMean=100, candMean=120,
// baseVar=candVar=0.444444, t=+67.0820, df=18.
func clearRegressionFixture() (base, cand []float64) {
	base = []float64{100, 101, 99, 100, 100, 101, 99, 100, 100, 100}
	cand = []float64{120, 121, 119, 120, 120, 121, 119, 120, 120, 120}
	return base, cand
}

func TestDetect_ClearRegression(t *testing.T) {
	base, cand := clearRegressionFixture()
	res, err := Detect(base, cand, DefaultOptions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verdict must be REGRESSION (candidate slower for a HigherIsWorse metric).
	// MUTATION: forcing Detect to `res.Verdict = NoChange` always (e.g. removing
	// the Regression branch) makes this fail.
	if res.Verdict != Regression {
		t.Fatalf("expected REGRESSION, got %s", res.Verdict)
	}
	// t-statistic sign MUST be positive (candidate mean above baseline) and its
	// magnitude pinned to the hand-computed value.
	// MUTATION: computing t as (mb-mc)/se instead of (mc-mb)/se flips the sign
	// and fails this assertion.
	if res.TStatistic <= 0 {
		t.Fatalf("expected positive t-statistic, got %v", res.TStatistic)
	}
	if got := round4(res.TStatistic); got != 67.0820 {
		t.Fatalf("expected t=67.0820, got %v", got)
	}
	// Welch–Satterthwaite df pinned: equal n and equal variance => df = 18.
	// MUTATION: replacing the WS df formula with nb+nc-2 still gives 18 here, so
	// the asymmetric-variance test below guards the formula instead.
	if got := round4(res.DegreesFreedom); got != 18.0 {
		t.Fatalf("expected df=18, got %v", got)
	}
	// Means pinned.
	if res.BaselineMean != 100 || res.CandidateMean != 120 {
		t.Fatalf("means wrong: base=%v cand=%v", res.BaselineMean, res.CandidateMean)
	}
	// Variance is the unbiased (n-1) estimator.
	// MUTATION: dividing ss by n instead of n-1 yields 0.4 and fails.
	if got := round4(res.BaselineVar); got != 0.4444 {
		t.Fatalf("expected baseVar=0.4444, got %v", got)
	}
	// A t of 67 on df 18 is overwhelmingly significant.
	if !res.Significant {
		t.Fatalf("expected significant result, p=%v", res.PValue)
	}
	if res.PValue >= 1e-9 {
		t.Fatalf("expected vanishingly small p-value, got %v", res.PValue)
	}
}

func TestDetect_IdenticalNoChange(t *testing.T) {
	s := []float64{50, 51, 49, 50, 52, 48, 50, 50}
	// Copy so candidate is a distinct but value-equal slice.
	cand := append([]float64(nil), s...)
	res, err := Detect(s, cand, DefaultOptions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// MUTATION: returning Regression unconditionally fails here.
	if res.Verdict != NoChange {
		t.Fatalf("expected NO-CHANGE for identical samples, got %s", res.Verdict)
	}
	if res.TStatistic != 0 {
		t.Fatalf("expected t=0 for identical samples, got %v", res.TStatistic)
	}
	if res.EffectSize != 0 {
		t.Fatalf("expected zero effect size, got %v", res.EffectSize)
	}
}

func TestDetect_ClearImprovement(t *testing.T) {
	base, _ := clearRegressionFixture()
	cand := []float64{80, 81, 79, 80, 80, 81, 79, 80, 80, 80}
	res, err := Detect(base, cand, DefaultOptions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Candidate is faster => IMPROVEMENT for a HigherIsWorse (latency) metric.
	// MUTATION: swapping the Regression/Improvement branches in the HigherIsWorse
	// case fails this (it would report REGRESSION).
	if res.Verdict != Improvement {
		t.Fatalf("expected IMPROVEMENT, got %s", res.Verdict)
	}
	// t-statistic must be NEGATIVE (candidate below baseline) and pinned.
	if res.TStatistic >= 0 {
		t.Fatalf("expected negative t-statistic, got %v", res.TStatistic)
	}
	if got := round4(res.TStatistic); got != -67.0820 {
		t.Fatalf("expected t=-67.0820, got %v", got)
	}
}

func TestDetect_TinyShiftBelowEffectGuard(t *testing.T) {
	// 200 samples each; candidate mean is 0.5% higher with tiny noise. This is
	// hugely statistically significant (t~49.87) yet only a 0.5% effect.
	const n = 200
	base := make([]float64, n)
	cand := make([]float64, n)
	for i := 0; i < n; i++ {
		noise := 0.1
		if i%2 == 1 {
			noise = -0.1
		}
		base[i] = 100.0 + noise
		cand[i] = 100.5 + noise
	}
	opts := DefaultOptions() // MinEffectSize = 0.02 (2%)
	res, err := Detect(base, cand, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// It IS statistically significant...
	if !res.Significant {
		t.Fatalf("expected significant t-test, p=%v", res.PValue)
	}
	if res.TStatistic < 40 {
		t.Fatalf("expected large t-statistic, got %v", res.TStatistic)
	}
	// ...but the 0.5% effect is below the 2% guard => NO-CHANGE.
	// MUTATION: dropping the `res.EffectSize < opts.MinEffectSize` clause from the
	// guard makes this report REGRESSION and fails.
	if res.Verdict != NoChange {
		t.Fatalf("expected NO-CHANGE (below effect guard), got %s (effect=%v)", res.Verdict, res.EffectSize)
	}
	if round4(res.EffectSize) != 0.0050 {
		t.Fatalf("expected effect size 0.0050, got %v", res.EffectSize)
	}
	// Lowering the guard below the effect must flip it to REGRESSION, proving the
	// guard is the ONLY thing suppressing the flag (not lack of significance).
	opts.MinEffectSize = 0.001
	res2, err := Detect(base, cand, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.Verdict != Regression {
		t.Fatalf("expected REGRESSION once guard lowered, got %s", res2.Verdict)
	}
}

func TestDetect_HigherIsBetterInvertsVerdict(t *testing.T) {
	// Throughput metric: candidate has a higher mean => IMPROVEMENT, not regression.
	base, cand := clearRegressionFixture() // cand mean 120 > base mean 100
	opts := DefaultOptions()
	opts.Direction = HigherIsBetter
	res, err := Detect(base, cand, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// MUTATION: ignoring opts.Direction (always treating higher as worse) reports
	// REGRESSION and fails.
	if res.Verdict != Improvement {
		t.Fatalf("expected IMPROVEMENT for HigherIsBetter with higher candidate, got %s", res.Verdict)
	}

	// And a lower-throughput candidate is a REGRESSION.
	lower := []float64{80, 81, 79, 80, 80, 81, 79, 80, 80, 80}
	res2, err := Detect(base, lower, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.Verdict != Regression {
		t.Fatalf("expected REGRESSION for HigherIsBetter with lower candidate, got %s", res2.Verdict)
	}
}

func TestDetect_WelchDFWithUnequalVariance(t *testing.T) {
	// Asymmetric variances/sizes so Welch–Satterthwaite df differs from the
	// pooled nb+nc-2. Hand-checked below.
	base := []float64{10, 12, 8, 11, 9}             // n=5
	cand := []float64{20, 40, 5, 35, 10, 25, 30, 8} // n=8, high variance
	res, err := Detect(base, cand, DefaultOptions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mb, vb := meanVar(base)
	mc, vc := meanVar(cand)
	nb, nc := 5.0, 8.0
	seSq := vb/nb + vc/nc
	wantDF := (seSq * seSq) / ((vb/nb)*(vb/nb)/(nb-1) + (vc/nc)*(vc/nc)/(nc-1))
	pooled := nb + nc - 2
	// Guard: the pooled df (11) must NOT equal the Welch df here, otherwise this
	// test would not distinguish the formulas.
	if math.Abs(wantDF-pooled) < 0.5 {
		t.Fatalf("fixture too symmetric: welchDF=%v pooled=%v", wantDF, pooled)
	}
	// MUTATION: replacing the WS df with nb+nc-2 changes df from ~7.7 to 11 and
	// fails this assertion.
	if round4(res.DegreesFreedom) != round4(wantDF) {
		t.Fatalf("expected Welch df=%v, got %v", round4(wantDF), round4(res.DegreesFreedom))
	}
	_ = mb
	_ = mc
}

func TestDetect_Errors(t *testing.T) {
	if _, err := Detect([]float64{1}, []float64{1, 2}, DefaultOptions()); !errors.Is(err, ErrInsufficientData) {
		t.Fatalf("expected ErrInsufficientData for n<2 baseline, got %v", err)
	}
	if _, err := Detect([]float64{1, 2}, []float64{1}, DefaultOptions()); !errors.Is(err, ErrInsufficientData) {
		t.Fatalf("expected ErrInsufficientData for n<2 candidate, got %v", err)
	}
	// Zero-variance, non-equal means => degenerate (SE=0, t undefined).
	if _, err := Detect([]float64{5, 5, 5}, []float64{7, 7, 7}, DefaultOptions()); !errors.Is(err, ErrDegenerate) {
		t.Fatalf("expected ErrDegenerate for zero-variance shift, got %v", err)
	}
}

func TestPValue_TwoSidedKnownValue(t *testing.T) {
	// For t=2.101, df=18 the two-sided p-value is ~0.05 (the classic 95% critical
	// value of the t-distribution at 18 df is 2.101). Assert the approximation
	// lands near 0.05.
	p := twoSidedPValue(2.101, 18)
	if math.Abs(p-0.05) > 1e-3 {
		t.Fatalf("expected two-sided p ~0.05 at t=2.101,df=18, got %v", p)
	}
	// Symmetry: p(t) == p(-t).
	// MUTATION: using a one-sided tail (e.g. returning regIncBeta only for t>0)
	// breaks this symmetry.
	if math.Abs(twoSidedPValue(2.101, 18)-twoSidedPValue(-2.101, 18)) > 1e-12 {
		t.Fatalf("p-value not symmetric in t sign")
	}
	// t=0 => p=1 (no evidence against the null).
	if got := twoSidedPValue(0, 18); math.Abs(got-1) > 1e-9 {
		t.Fatalf("expected p=1 at t=0, got %v", got)
	}
}

func TestVerdict_String(t *testing.T) {
	cases := map[Verdict]string{
		Regression:  "REGRESSION",
		Improvement: "IMPROVEMENT",
		NoChange:    "NO-CHANGE",
		Verdict(99): "UNKNOWN",
	}
	for v, want := range cases {
		if got := v.String(); got != want {
			t.Fatalf("Verdict(%d).String()=%q want %q", v, got, want)
		}
	}
}

// TestDetect_DeterministicWithSeed proves a seeded sample draw reproduces the
// exact same Result twice (DST-style reproducibility), and that an injected
// upward shift on the candidate is flagged REGRESSION.
func TestDetect_DeterministicWithSeed(t *testing.T) {
	const seed = int64(0xC0FFEE)

	gen := func() Result {
		rng := rand.New(rand.NewSource(seed))
		base := make([]float64, 64)
		cand := make([]float64, 64)
		for i := range base {
			base[i] = 100 + rng.NormFloat64()*2
		}
		// Re-seed identically for the candidate noise, then inject a +15 shift so
		// the regression is real and observable sink-side (verdict + sign).
		rng = rand.New(rand.NewSource(seed))
		for i := range cand {
			cand[i] = 115 + rng.NormFloat64()*2
		}
		res, err := Detect(base, cand, DefaultOptions())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return res
	}

	r1 := gen()
	r2 := gen()
	// MUTATION: seeding the PRNG from time.Now() (non-deterministic) makes the two
	// runs diverge and fails this equality.
	if r1.TStatistic != r2.TStatistic || r1.DegreesFreedom != r2.DegreesFreedom || r1.PValue != r2.PValue {
		t.Fatalf("non-deterministic result: %+v vs %+v", r1, r2)
	}
	if r1.Verdict != Regression {
		t.Fatalf("expected REGRESSION on injected +15 shift, got %s", r1.Verdict)
	}
	if r1.TStatistic <= 0 {
		t.Fatalf("expected positive t-statistic for upward shift, got %v", r1.TStatistic)
	}
}
