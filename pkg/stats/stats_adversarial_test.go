// Package stats adversarial test suite.
//
// This file pins the *statistical contract* of WelchTTest and DetectRegression
// against independently-computed oracle values (scipy.stats.ttest_ind with
// equal_var=False) and probes the degenerate / boundary / direction behavior.
//
// The oracle values embedded here were produced by an external tool (scipy
// 1.13.1) and are therefore independent of this codebase's implementation —
// satisfying the "independent OS oracle" requirement: a wrong t-statistic,
// wrong Welch-Satterthwaite df, inverted significance test, or flipped
// regression direction will make the relevant assertion below fail.
package stats_test

import (
	"math"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/stats"
)

// closeEnough reports whether got is within tol of want (absolute tolerance).
func closeEnough(got, want, tol float64) bool {
	return math.Abs(got-want) <= tol
}

// ---------------------------------------------------------------------------
// CENTERPIECE 1: Welch known-values — unequal variances AND unequal sample
// sizes. This is the whole reason Welch exists (vs Student's pooled-variance
// t-test). A pooled-variance formula, or dropping the per-group n/variance
// normalization, would produce a different t AND a different (integer-ish) df.
// Oracle (scipy, equal_var=False):
//
//	baseline={10,12,11,13,9,10,14}  candidate={20,30,25,15,40}
//	t = -3.3790172902093336   df = 4.200786719356406   p = 0.02578537883611878
//
// ---------------------------------------------------------------------------
func TestWelch_UnequalVarUnequalN_KnownValues(t *testing.T) {
	baseline := []float64{10, 12, 11, 13, 9, 10, 14}
	candidate := []float64{20, 30, 25, 15, 40}

	const (
		wantT  = -3.3790172902093336
		wantDF = 4.200786719356406
		wantP  = 0.02578537883611878
	)

	r, err := stats.WelchTTest(baseline, candidate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// t-statistic: tight tolerance. A pooled-variance (Student) t for this data
	// is ~ -4.6 with df=10 — far outside this band — so this assertion is what
	// distinguishes Welch from Student.
	if !closeEnough(r.TStat, wantT, 1e-9) {
		t.Errorf("TStat: got %.15g, want %.15g (tol 1e-9)", r.TStat, wantT)
	}
	// Welch-Satterthwaite df is fractional (~4.20). Student's df would be 10.
	// A wrong df formula changes the p-value and the significance decision.
	if !closeEnough(r.DF, wantDF, 1e-9) {
		t.Errorf("DF: got %.15g, want %.15g (tol 1e-9) — fractional Welch df, NOT n1+n2-2=10", r.DF, wantDF)
	}
	if !closeEnough(r.PValue, wantP, 1e-12) {
		t.Errorf("PValue: got %.15g, want %.15g (tol 1e-12)", r.PValue, wantP)
	}
	// At alpha=0.05 this pair IS significant (p=0.0258 < 0.05).
	if !r.Significant {
		t.Errorf("expected Significant=true at alpha=0.05 (p=%.6g)", r.PValue)
	}
}

// CENTERPIECE 1b: a second known-values pair (moderate separation, equal n,
// unequal variance) to pin the contract from a different point in the space.
// Oracle: baseline={10,11,12,10,11,9,10,12} candidate={11,13,12,14,11,13,12,14}
//
//	t = -3.3187325915153236  df = 13.804899218825346  p = 0.0051553421009225605
func TestWelch_Moderate_KnownValues(t *testing.T) {
	baseline := []float64{10, 11, 12, 10, 11, 9, 10, 12}
	candidate := []float64{11, 13, 12, 14, 11, 13, 12, 14}

	r, err := stats.WelchTTest(baseline, candidate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !closeEnough(r.TStat, -3.3187325915153236, 1e-9) {
		t.Errorf("TStat: got %.15g, want -3.3187325915153236", r.TStat)
	}
	if !closeEnough(r.DF, 13.804899218825346, 1e-9) {
		t.Errorf("DF: got %.15g, want 13.804899218825346", r.DF)
	}
	if !closeEnough(r.PValue, 0.0051553421009225605, 1e-12) {
		t.Errorf("PValue: got %.15g, want 0.0051553421009225605", r.PValue)
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE 2: Regression-direction semantics.
//
// The metric here is latency, so HIGHER candidate == WORSE (a regression).
//
// The package's DetectRegression is a TWO-SIDED test: Significant = (p < alpha)
// with a two-tailed p-value. We pin exactly that contract and make its
// direction behavior explicit:
//   - A clearly-worse candidate IS flagged.                          (true positive)
//   - A statistically-insignificant difference is NOT flagged.        (true negative)
//   - A clearly-BETTER candidate is ALSO flagged by this two-sided   (documented:
//     test — improvements are NOT distinguished from regressions.     direction-blind)
//
// The t-statistic sign IS the carrier of direction:
//   TStat = (mean_baseline - mean_candidate)/se.
//   worse candidate (higher) -> negative t; better candidate (lower) -> positive t.
// A consumer that wants a one-sided "worse-only" decision must inspect the sign
// of TStat; DetectRegression alone does not. These tests lock that down so a
// future change that silently makes the test one-sided (or flips the sign) bites.
// ---------------------------------------------------------------------------

func TestDetectRegression_WorseCandidate_IsFlagged(t *testing.T) {
	// latency: baseline ~100, candidate ~130 => candidate is WORSE.
	baseline := []float64{100, 101, 99, 100, 102}
	candidate := []float64{130, 131, 129, 132, 130}

	r, err := stats.DetectRegression(baseline, candidate, 0.05)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.Significant {
		t.Fatalf("a clearly-worse candidate MUST be flagged (p=%.6g)", r.PValue)
	}
	// Direction carrier: baseline mean < candidate mean => TStat negative.
	if r.TStat >= 0 {
		t.Errorf("worse candidate must yield negative TStat (baseline-candidate<0), got %.6g", r.TStat)
	}
}

func TestDetectRegression_BetterCandidate_DirectionContract(t *testing.T) {
	// latency: baseline ~130, candidate ~100 => candidate is BETTER (improvement).
	baseline := []float64{130, 131, 129, 132, 130}
	candidate := []float64{100, 101, 99, 100, 102}

	r, err := stats.DetectRegression(baseline, candidate, 0.05)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// CONTRACT (current, two-sided): an improvement of this magnitude is ALSO
	// "significant". This is the documented direction-blindness of the
	// two-sided detector. If this ever flips to false, the test became
	// one-sided — a contract change that must be reviewed, not silent.
	if !r.Significant {
		t.Errorf("two-sided DetectRegression flags large improvements too; "+
			"got Significant=false (p=%.6g) — became one-sided?", r.PValue)
	}

	// The SIGN is what distinguishes an improvement from a regression:
	// baseline mean > candidate mean => TStat positive.
	if r.TStat <= 0 {
		t.Errorf("better candidate must yield POSITIVE TStat (improvement marker), got %.6g", r.TStat)
	}

	// Symmetry: |t| for the improvement equals |t| for the mirror-image
	// regression — proving the test cannot tell them apart by magnitude alone.
	worse, _ := stats.DetectRegression(
		[]float64{100, 101, 99, 100, 102},
		[]float64{130, 131, 129, 132, 130}, 0.05)
	if !closeEnough(math.Abs(r.TStat), math.Abs(worse.TStat), 1e-9) {
		t.Errorf("|t| improvement (%.6g) must equal |t| regression (%.6g) — two-sided symmetry",
			math.Abs(r.TStat), math.Abs(worse.TStat))
	}
	if !closeEnough(r.PValue, worse.PValue, 1e-12) {
		t.Errorf("p improvement (%.6g) must equal p regression (%.6g)", r.PValue, worse.PValue)
	}
}

func TestDetectRegression_InsignificantDifference_NotFlagged(t *testing.T) {
	// Near-identical samples: a real-but-tiny mean difference that is NOT
	// statistically significant at alpha=0.05.
	baseline := []float64{10.0, 11.0, 9.0, 10.0, 10.5}
	candidate := []float64{10.2, 10.8, 9.5, 10.3, 10.1}

	r, err := stats.DetectRegression(baseline, candidate, 0.05)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Significant {
		t.Errorf("insignificant difference must NOT be flagged, got Significant=true (p=%.6g)", r.PValue)
	}
	if r.PValue <= 0.05 {
		t.Errorf("expected p > 0.05 for near-identical pair, got %.6g", r.PValue)
	}
}

// ---------------------------------------------------------------------------
// SIGNIFICANCE BOUNDARY: the decision is strictly p < alpha (open interval).
// We bracket the same data with an alpha just above and just below its p-value
// and assert the flip happens on the correct side.
//
//	Data: t=-3.3790…, p = 0.02578537883611878 (the unequal-var/n pair).
//
// ---------------------------------------------------------------------------
func TestDetectRegression_SignificanceBoundary(t *testing.T) {
	baseline := []float64{10, 12, 11, 13, 9, 10, 14}
	candidate := []float64{20, 30, 25, 15, 40}
	const p = 0.02578537883611878

	// alpha strictly greater than p => significant.
	if r, err := stats.DetectRegression(baseline, candidate, p+1e-6); err != nil {
		t.Fatalf("err: %v", err)
	} else if !r.Significant {
		t.Errorf("alpha=%.10g > p=%.10g must be Significant=true", p+1e-6, p)
	}

	// alpha strictly less than p => NOT significant.
	if r, err := stats.DetectRegression(baseline, candidate, p-1e-6); err != nil {
		t.Fatalf("err: %v", err)
	} else if r.Significant {
		t.Errorf("alpha=%.10g < p=%.10g must be Significant=false", p-1e-6, p)
	}
}

// WelchTTest's own Significant field uses a HARD-CODED alpha=0.05 (per its
// doc). Pin that so a change to the embedded constant is caught.
func TestWelchTTest_HardcodedAlpha05(t *testing.T) {
	// p = 0.02578… < 0.05 => Significant true under WelchTTest's fixed alpha.
	r, err := stats.WelchTTest([]float64{10, 12, 11, 13, 9, 10, 14}, []float64{20, 30, 25, 15, 40})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !r.Significant {
		t.Errorf("WelchTTest uses alpha=0.05; p=%.6g<0.05 must be Significant=true", r.PValue)
	}

	// p = 0.0258… would NOT be significant at a stricter 0.01 — but WelchTTest
	// ignores any caller alpha and uses 0.05, so it stays significant. This pins
	// that WelchTTest does NOT take an alpha argument and is locked at 0.05.
	if r.PValue >= 0.05 {
		t.Errorf("sanity: expected p<0.05, got %.6g", r.PValue)
	}
}

// ---------------------------------------------------------------------------
// DEGENERATE INPUT: zero variance, single-element, empty. No NaN / Inf / panic.
// ---------------------------------------------------------------------------

func TestDegenerate_IdenticalConstants_NoNaN(t *testing.T) {
	// Both samples identical constants => se2==0 => guarded path.
	base := []float64{5, 5, 5, 5, 5}
	cand := []float64{5, 5, 5, 5, 5}

	r, err := stats.WelchTTest(base, cand)
	if err != nil {
		// An explicit error is an acceptable documented behavior.
		return
	}
	if math.IsNaN(r.TStat) || math.IsInf(r.TStat, 0) {
		t.Errorf("TStat must be finite for identical constants, got %v", r.TStat)
	}
	if math.IsNaN(r.PValue) || math.IsInf(r.PValue, 0) {
		t.Errorf("PValue must be finite, got %v", r.PValue)
	}
	if r.Significant {
		t.Errorf("identical constants must NOT be flagged significant")
	}
	// Documented guarded contract: TStat=0, DF=0, PValue=1.
	if r.TStat != 0 || r.DF != 0 || r.PValue != 1.0 {
		t.Errorf("zero-variance guard contract: want {t=0,df=0,p=1}, got {t=%v,df=%v,p=%v}", r.TStat, r.DF, r.PValue)
	}
}

func TestDegenerate_OneZeroVarianceGroup_NoNaN(t *testing.T) {
	// Only baseline has zero variance; SE is still well-defined.
	// Oracle (scipy): base={5,5,5,5,5} cand={10,11,9,10.5,10.2}
	//   t=-15.52593779858555  df=4.0  p=0.00010046236773047068
	base := []float64{5, 5, 5, 5, 5}
	cand := []float64{10, 11, 9, 10.5, 10.2}

	r, err := stats.WelchTTest(base, cand)
	if err != nil {
		t.Fatalf("unexpected error for one-zero-variance group: %v", err)
	}
	if math.IsNaN(r.TStat) || math.IsInf(r.TStat, 0) {
		t.Fatalf("TStat must be finite, got %v", r.TStat)
	}
	if !closeEnough(r.TStat, -15.52593779858555, 1e-9) {
		t.Errorf("TStat: got %.15g, want -15.52593779858555", r.TStat)
	}
	// df falls back to min(n1,n2)-1 = 4 here (one group contributes zero to
	// the Welch df denominator), which matches scipy's df=4.0 for this case.
	if !closeEnough(r.DF, 4.0, 1e-9) {
		t.Errorf("DF: got %.15g, want 4.0", r.DF)
	}
	if !closeEnough(r.PValue, 0.00010046236773047068, 1e-10) {
		t.Errorf("PValue: got %.15g, want 0.00010046236773047068", r.PValue)
	}
}

func TestDegenerate_SingleElementAndEmpty_Errors(t *testing.T) {
	cases := []struct {
		name       string
		base, cand []float64
	}{
		{"single-element baseline", []float64{1}, []float64{1, 2, 3}},
		{"single-element candidate", []float64{1, 2, 3}, []float64{9}},
		{"empty baseline", nil, []float64{1, 2, 3}},
		{"empty candidate", []float64{1, 2, 3}, nil},
		{"both empty", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := stats.WelchTTest(c.base, c.cand)
			if err == nil {
				t.Errorf("expected ErrInsufficientData for %s, got nil", c.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DETERMINISM: identical inputs => bit-identical outputs across repeated runs.
// ---------------------------------------------------------------------------
func TestDeterminism(t *testing.T) {
	base := []float64{10, 12, 11, 13, 9, 10, 14}
	cand := []float64{20, 30, 25, 15, 40}

	first, err := stats.WelchTTest(base, cand)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for i := 0; i < 50; i++ {
		r, err := stats.WelchTTest(base, cand)
		if err != nil {
			t.Fatalf("err on iter %d: %v", i, err)
		}
		if r.TStat != first.TStat || r.DF != first.DF || r.PValue != first.PValue || r.Significant != first.Significant {
			t.Fatalf("non-deterministic at iter %d: %+v != %+v", i, r, first)
		}
	}
}
