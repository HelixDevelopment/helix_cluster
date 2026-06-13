// Package stats provides statistical utilities for HelixCluster OS.
// Currently contains a Welch's t-test regression detector used by HelixQA.
package stats

import (
	"errors"
	"math"
)

// ErrInsufficientData is returned when a slice has fewer than 2 elements.
var ErrInsufficientData = errors.New("stats: each sample must have at least 2 elements")

// Result holds the output of a Welch's t-test.
type Result struct {
	// TStat is the Welch t-statistic: (mean_baseline - mean_candidate) / sqrt(s1²/n1 + s2²/n2)
	TStat float64
	// DF is the Welch-Satterthwaite degrees of freedom.
	DF float64
	// PValue is the two-tailed p-value from the t-distribution.
	PValue float64
	// Significant is true when PValue < alpha.  For WelchTTest the alpha used is 0.05.
	// For DetectRegression the caller-supplied alpha is used.
	Significant bool
}

// WelchTTest performs a two-sample Welch's t-test (unequal variances) comparing
// baseline against candidate.  It returns an error if either slice has fewer
// than 2 elements or if the denominator of the t-statistic is exactly zero
// (both samples are constant and equal).
//
// When only one slice has zero variance the SE is still well-defined; the
// result is returned without NaN/Inf.
func WelchTTest(baseline, candidate []float64) (Result, error) {
	if len(baseline) < 2 {
		return Result{}, ErrInsufficientData
	}
	if len(candidate) < 2 {
		return Result{}, ErrInsufficientData
	}

	n1 := float64(len(baseline))
	n2 := float64(len(candidate))

	m1 := mean(baseline)
	m2 := mean(candidate)

	v1 := variance(baseline, m1)  // sample variance s1²
	v2 := variance(candidate, m2) // sample variance s2²

	se2 := v1/n1 + v2/n2 // pooled standard error squared

	// Guard: if SE is exactly zero both samples are constant and equal.
	// Return Significant=false without NaN.
	if se2 == 0 {
		return Result{TStat: 0, DF: 0, PValue: 1.0, Significant: false}, nil
	}

	se := math.Sqrt(se2)
	t := (m1 - m2) / se

	// Welch-Satterthwaite degrees of freedom.
	//   df = (s1²/n1 + s2²/n2)²  /  ( (s1²/n1)²/(n1-1) + (s2²/n2)²/(n2-1) )
	dfNum := se2 * se2
	dfDen := (v1/n1)*(v1/n1)/(n1-1) + (v2/n2)*(v2/n2)/(n2-1)

	var df float64
	if dfDen == 0 {
		// Degenerate: one or both groups have zero variance contribution.
		// Fall back to min(n1, n2) - 1 to avoid Inf/NaN.
		df = math.Min(n1, n2) - 1
	} else {
		df = dfNum / dfDen
	}

	pv := twoTailedP(t, df)
	return Result{
		TStat:       t,
		DF:          df,
		PValue:      pv,
		Significant: pv < 0.05,
	}, nil
}

// DetectRegression runs WelchTTest and sets Significant = (PValue < alpha).
// It is the primary entry point for the HelixQA regression detector.
func DetectRegression(baseline, candidate []float64, alpha float64) (Result, error) {
	r, err := WelchTTest(baseline, candidate)
	if err != nil {
		return r, err
	}
	r.Significant = r.PValue < alpha
	return r, nil
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

// mean returns the arithmetic mean of xs.
func mean(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// variance returns the sample variance (divisor n-1) of xs given its mean.
func variance(xs []float64, m float64) float64 {
	s := 0.0
	for _, x := range xs {
		d := x - m
		s += d * d
	}
	return s / float64(len(xs)-1)
}

// twoTailedP returns the two-tailed p-value for the t-distribution with the
// given t-statistic and degrees of freedom.
//
// Two-tailed p-value = I_x(df/2, 1/2) where x = df / (df + t²).
// I_x is the regularized incomplete beta function, computed here using
// the continued-fraction / Lentz algorithm (pure math, no external deps).
func twoTailedP(t, df float64) float64 {
	if math.IsNaN(t) || math.IsInf(t, 0) || math.IsNaN(df) || df <= 0 {
		return 1.0
	}
	x := df / (df + t*t)
	return regularizedIncompleteBeta(x, df/2, 0.5)
}

// regularizedIncompleteBeta computes I_x(a, b) — the regularized incomplete
// beta function — using the Lentz continued-fraction method.
// For numerical stability, when x > (a+1)/(a+b+2) we use the symmetry:
//
//	I_x(a, b) = 1 - I_{1-x}(b, a)
//
// This is the standard algorithm used in textbooks (e.g. Numerical Recipes).
func regularizedIncompleteBeta(x, a, b float64) float64 {
	if x < 0 || x > 1 {
		return math.NaN()
	}
	if x == 0 {
		return 0
	}
	if x == 1 {
		return 1
	}

	// Symmetry relation for numerical stability.
	if x > (a+1)/(a+b+2) {
		return 1 - regularizedIncompleteBeta(1-x, b, a)
	}

	// Front factor: x^a * (1-x)^b / (a * B(a,b))
	// log of front factor = a*log(x) + b*log(1-x) - log(a) - logB(a,b)
	// logB(a,b) = lgamma(a) + lgamma(b) - lgamma(a+b)
	lga, _ := math.Lgamma(a)
	lgb, _ := math.Lgamma(b)
	lgab, _ := math.Lgamma(a + b)
	logFront := a*math.Log(x) + b*math.Log(1-x) - math.Log(a) - (lga + lgb - lgab)
	front := math.Exp(logFront)

	return front * betaCF(x, a, b)
}

// betaCF evaluates the continued fraction for the incomplete beta function
// using the Lentz method (modified Lentz algorithm for improved convergence).
// Reference: Numerical Recipes §6.4.
func betaCF(x, a, b float64) float64 {
	const (
		maxIter = 200
		eps     = 3e-15
		fpMin   = 1e-300
	)

	qab := a + b
	qap := a + 1
	qam := a - 1

	c := 1.0
	d := 1.0 - qab*x/qap
	if math.Abs(d) < fpMin {
		d = fpMin
	}
	d = 1.0 / d
	h := d

	for m := 1; m <= maxIter; m++ {
		m2 := 2 * m
		mf := float64(m)

		// Even step
		aa := mf * (b - mf) * x / ((qam + float64(m2)) * (a + float64(m2)))
		d = 1.0 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1.0 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1.0 / d
		h *= d * c

		// Odd step
		aa = -(a + mf) * (qab + mf) * x / ((a + float64(m2)) * (qap + float64(m2)))
		d = 1.0 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1.0 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1.0 / d
		delta := d * c
		h *= delta

		if math.Abs(delta-1.0) < eps {
			break
		}
	}
	return h
}
