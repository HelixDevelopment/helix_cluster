// Package regression provides a statistical performance-regression detector for
// HelixQA benchmark results.
//
// Given a baseline sample and a candidate sample of a single metric (latency,
// throughput, etc.), it computes the sample mean and (unbiased) variance of each
// group, runs Welch's unequal-variance two-sample t-test, derives the
// Welch–Satterthwaite degrees of freedom, and classifies the change as
// REGRESSION, IMPROVEMENT, or NO-CHANGE against a configurable significance
// threshold AND a minimum-effect-size guard so that statistically-significant but
// practically-trivial noise is not flagged.
//
// # Metric direction
//
// For a HigherIsWorse metric (e.g. latency), a candidate mean that is
// significantly HIGHER than the baseline is a REGRESSION and a significantly
// LOWER mean is an IMPROVEMENT. For a HigherIsBetter metric (e.g. throughput)
// the interpretation is inverted. The raw Welch t-statistic is always computed as
// (candidateMean - baselineMean) / standardError, so its sign is independent of
// the metric direction; only the REGRESSION/IMPROVEMENT labelling flips.
//
// # p-value approximation (honest disclosure)
//
// The two-sided p-value is derived from the Student's t cumulative distribution
// function evaluated via the regularized incomplete beta function I_x(a,b), using
// the Lentz continued-fraction expansion (Numerical Recipes, betacf). This is an
// approximation: the continued fraction is iterated to a relative tolerance of
// 1e-12 with a 200-iteration cap, and lgamma is the stdlib math.Lgamma. Accuracy
// is excellent for the moderate degrees of freedom typical of benchmark samples
// (well below 1e-9 absolute error for df>=2), but the p-value is intended for
// ranking/threshold decisions, NOT for publication-grade exact tail
// probabilities. The classification decision uses the t-statistic and degrees of
// freedom directly against a critical value, so it does not depend on the
// absolute precision of the reported p-value.
package regression

import (
	"errors"
	"math"
)

// Direction describes whether a higher metric value is better or worse.
type Direction int

const (
	// HigherIsWorse marks metrics like latency where larger means slower/worse.
	HigherIsWorse Direction = iota
	// HigherIsBetter marks metrics like throughput where larger means better.
	HigherIsBetter
)

// Verdict is the classification outcome of a detection.
type Verdict int

const (
	// NoChange means no statistically- and practically-significant difference.
	NoChange Verdict = iota
	// Regression means the candidate is significantly worse than the baseline.
	Regression
	// Improvement means the candidate is significantly better than the baseline.
	Improvement
)

// String renders a Verdict as a stable, human-readable token.
func (v Verdict) String() string {
	switch v {
	case Regression:
		return "REGRESSION"
	case Improvement:
		return "IMPROVEMENT"
	case NoChange:
		return "NO-CHANGE"
	default:
		return "UNKNOWN"
	}
}

// Options configures a detection.
type Options struct {
	// Direction selects how the sign of the mean shift maps to verdicts.
	Direction Direction
	// Alpha is the two-sided significance threshold (e.g. 0.05). A change is
	// statistically significant when the p-value is strictly below Alpha.
	Alpha float64
	// MinEffectSize is the minimum absolute relative change in the mean
	// (|candidateMean-baselineMean| / |baselineMean|) required before a
	// statistically-significant result is allowed to be flagged. This guards
	// against flagging trivial noise. Set to 0 to disable the guard.
	MinEffectSize float64
}

// DefaultOptions returns sensible defaults: latency-style metric, 5% alpha, and
// a 2% minimum effect size.
func DefaultOptions() Options {
	return Options{
		Direction:     HigherIsWorse,
		Alpha:         0.05,
		MinEffectSize: 0.02,
	}
}

// Result holds the full statistics of a detection.
type Result struct {
	Verdict        Verdict
	BaselineMean   float64
	CandidateMean  float64
	BaselineVar    float64 // unbiased (n-1) sample variance
	CandidateVar   float64
	BaselineN      int
	CandidateN     int
	TStatistic     float64 // (candidateMean - baselineMean) / standardError
	DegreesFreedom float64 // Welch–Satterthwaite
	PValue         float64 // two-sided, approximated (see package doc)
	EffectSize     float64 // |relative mean change|
	Significant    bool    // PValue < Alpha
}

// ErrInsufficientData is returned when a sample has fewer than two observations
// (variance is undefined for n<2).
var ErrInsufficientData = errors.New("regression: each sample needs at least 2 observations")

// ErrDegenerate is returned when both samples have zero variance, making the
// standard error zero and the t-statistic undefined.
var ErrDegenerate = errors.New("regression: zero combined variance; t-statistic undefined")

// ErrNonFinite is returned when a sample contains a non-finite observation
// (NaN or ±Inf). Such a value poisons the mean/variance and would otherwise
// produce a NaN t-statistic and NaN p-value; because `NaN < Alpha` is false the
// detector would silently classify a corrupt benchmark as NO-CHANGE — a
// false-negative. We reject the input instead of bluffing an all-clear verdict.
var ErrNonFinite = errors.New("regression: sample contains a non-finite value (NaN or Inf)")

// allFinite reports whether every observation in x is finite (no NaN, no ±Inf).
func allFinite(x []float64) bool {
	for _, v := range x {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// meanVar returns the sample mean and unbiased (n-1) variance of x.
func meanVar(x []float64) (mean, variance float64) {
	n := float64(len(x))
	var sum float64
	for _, v := range x {
		sum += v
	}
	mean = sum / n
	var ss float64
	for _, v := range x {
		d := v - mean
		ss += d * d
	}
	variance = ss / (n - 1)
	return mean, variance
}

// Detect runs Welch's t-test on baseline vs candidate and classifies the change.
func Detect(baseline, candidate []float64, opts Options) (Result, error) {
	if len(baseline) < 2 || len(candidate) < 2 {
		return Result{}, ErrInsufficientData
	}
	if !allFinite(baseline) || !allFinite(candidate) {
		return Result{}, ErrNonFinite
	}

	mb, vb := meanVar(baseline)
	mc, vc := meanVar(candidate)
	nb := float64(len(baseline))
	nc := float64(len(candidate))

	res := Result{
		BaselineMean:  mb,
		CandidateMean: mc,
		BaselineVar:   vb,
		CandidateVar:  vc,
		BaselineN:     len(baseline),
		CandidateN:    len(candidate),
	}

	// Effect size: absolute relative change in the mean.
	if mb != 0 {
		res.EffectSize = math.Abs(mc-mb) / math.Abs(mb)
	} else {
		res.EffectSize = math.Abs(mc - mb)
	}

	seSq := vb/nb + vc/nc
	if seSq == 0 {
		// Identical, zero-variance samples => no change; non-identical => degenerate.
		if mb == mc {
			res.Verdict = NoChange
			res.DegreesFreedom = nb + nc - 2
			res.PValue = 1
			return res, nil
		}
		return Result{}, ErrDegenerate
	}
	se := math.Sqrt(seSq)

	res.TStatistic = (mc - mb) / se

	// Welch–Satterthwaite degrees of freedom.
	tb := vb / nb
	tc := vc / nc
	denom := (tb*tb)/(nb-1) + (tc*tc)/(nc-1)
	if denom == 0 {
		res.DegreesFreedom = nb + nc - 2
	} else {
		res.DegreesFreedom = (seSq * seSq) / denom
	}

	res.PValue = twoSidedPValue(res.TStatistic, res.DegreesFreedom)
	res.Significant = res.PValue < opts.Alpha

	// A change is flagged only when it is BOTH statistically significant AND
	// exceeds the minimum effect-size guard.
	if !res.Significant || res.EffectSize < opts.MinEffectSize {
		res.Verdict = NoChange
		return res, nil
	}

	// Map the sign of the mean shift to a verdict given the metric direction.
	candidateHigher := mc > mb
	switch opts.Direction {
	case HigherIsWorse:
		if candidateHigher {
			res.Verdict = Regression
		} else {
			res.Verdict = Improvement
		}
	case HigherIsBetter:
		if candidateHigher {
			res.Verdict = Improvement
		} else {
			res.Verdict = Regression
		}
	}
	return res, nil
}

// twoSidedPValue returns the two-sided Student's t p-value for the given
// t-statistic and degrees of freedom using the regularized incomplete beta
// approximation. See the package doc for the honest accuracy disclosure.
func twoSidedPValue(t, df float64) float64 {
	if df <= 0 {
		return math.NaN()
	}
	// Two-sided tail: P(|T| > |t|) = I_{df/(df+t^2)}(df/2, 1/2).
	x := df / (df + t*t)
	return regIncBeta(df/2.0, 0.5, x)
}

// lgamma returns the natural log of the absolute value of Gamma(x). The beta
// arguments here (a,b > 0) keep Gamma positive, so the sign is discarded.
func lgamma(x float64) float64 {
	l, _ := math.Lgamma(x)
	return l
}

// regIncBeta computes the regularized incomplete beta function I_x(a,b).
func regIncBeta(a, b, x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	lbeta := lgamma(a+b) - lgamma(a) - lgamma(b)
	front := math.Exp(lbeta + a*math.Log(x) + b*math.Log(1-x))
	if x < (a+1)/(a+b+2) {
		return front * betacf(a, b, x) / a
	}
	return 1 - front*betacf(b, a, 1-x)/b
}

// betacf evaluates the continued fraction for the incomplete beta function via
// the modified Lentz algorithm (Numerical Recipes).
func betacf(a, b, x float64) float64 {
	const (
		maxIt = 200
		eps   = 1e-12
		fpMin = 1e-300
	)
	qab := a + b
	qap := a + 1
	qam := a - 1
	c := 1.0
	d := 1 - qab*x/qap
	if math.Abs(d) < fpMin {
		d = fpMin
	}
	d = 1 / d
	h := d
	for m := 1; m <= maxIt; m++ {
		mf := float64(m)
		m2 := 2 * mf
		// even step
		aa := mf * (b - mf) * x / ((qam + m2) * (a + m2))
		d = 1 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1 / d
		h *= d * c
		// odd step
		aa = -(a + mf) * (qab + mf) * x / ((a + m2) * (qap + m2))
		d = 1 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < eps {
			break
		}
	}
	return h
}
