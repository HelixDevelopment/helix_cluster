package benchmark_test

// Adversarial / sink-side probes for pkg/benchmark's NUMERIC surface.
//
// The package has no percentile functions; its statistical surface is
// CoefficientOfVariation (the repeatability metric) and the score derivation in
// RunBenchmarks. The existing suite pins the happy path, the Bessel oracle, the
// <2-sample degenerate case, and a symmetric non-positive mean. These probes
// attack the GAPS: NaN/Inf poison, an overflow-to-Inf sum, the exact-zero-mean
// boundary, and concurrent RunBenchmarks racing on the package-global sink.
//
// Every assertion is SINK-SIDE: it asserts the actual returned float, never
// merely "did not panic".

import (
	"context"
	"math"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/benchmark"
)

// TestCV_NaNSamplePoison documents the SINK behaviour when a NaN sample is fed
// to CoefficientOfVariation. The doc contract says only "<2 samples or a
// non-positive mean yields 0"; NaN is neither, so the guard `mean <= 0` (which
// is FALSE for NaN) does not fire and the function returns NaN.
//
// This is the dangerous part: the live repeatability gate in
// TestRunBenchmarksRepeatableWithinTolerance asserts `cv >= tolerance` to FAIL a
// flaky host. Because `NaN >= tolerance` is false in Go, a NaN-poisoned CV
// SILENTLY PASSES that gate — a poisoned metric reads as "perfectly stable".
//
// We pin the real sink so the behaviour is explicit and any future change is
// caught. This is a contract-gap/LATENT-RISK pin, not a "no panic" check.
func TestCV_NaNSamplePoison(t *testing.T) {
	t.Parallel()
	got := benchmark.CoefficientOfVariation([]float64{1.0, math.NaN()})
	if !math.IsNaN(got) {
		// If a future hardening makes this return 0 (treat poison as degenerate),
		// update this pin deliberately — do not let it silently regress.
		t.Fatalf("NaN-poisoned CV = %g, want NaN on current prod (documents the contract gap)", got)
	}

	// SINK CONSEQUENCE: prove the poisoned value defeats the repeatability gate.
	const tolerance = 0.5
	if got >= tolerance {
		t.Fatalf("expected NaN>=tolerance to be false (NaN silently passes the gate); got %g", got)
	}
}

// TestCV_PosInfSamplePoison feeds +Inf. sum=+Inf, mean=+Inf, the `mean<=0`
// guard is false, and the deviation (Inf-Inf) is NaN, so CV is NaN. Same silent
// poison as the NaN case, reached via overflow rather than an explicit NaN.
func TestCV_PosInfSamplePoison(t *testing.T) {
	t.Parallel()
	got := benchmark.CoefficientOfVariation([]float64{1.0, math.Inf(1)})
	if !math.IsNaN(got) {
		t.Fatalf("+Inf-poisoned CV = %g, want NaN on current prod", got)
	}
}

// TestCV_OverflowSumToInf proves an OVERFLOW in the sum of large-but-finite
// samples produces the same poison: two values near math.MaxFloat64 sum to +Inf
// in float64, so mean is +Inf and CV is NaN. No explicit Inf in the input.
func TestCV_OverflowSumToInf(t *testing.T) {
	t.Parallel()
	big := math.MaxFloat64
	samples := []float64{big, big} // big+big overflows to +Inf.
	if !math.IsInf(big+big, 1) {
		t.Fatalf("precondition: MaxFloat64+MaxFloat64 should overflow to +Inf")
	}
	got := benchmark.CoefficientOfVariation(samples)
	if !math.IsNaN(got) {
		t.Fatalf("overflow-sum CV = %g, want NaN on current prod (sum overflows to +Inf)", got)
	}
}

// TestCV_ExactZeroMeanGuarded pins the boundary the existing suite hints at: a
// symmetric set with an EXACT zero mean. Here `mean <= 0` IS true (0<=0), so the
// guard fires and returns 0 — NOT a NaN from a 0/0 division. This proves the
// guard correctly intercepts the divide-by-zero on a zero mean.
func TestCV_ExactZeroMeanGuarded(t *testing.T) {
	t.Parallel()
	got := benchmark.CoefficientOfVariation([]float64{-10, 10}) // mean exactly 0.
	if got != 0 {
		t.Fatalf("zero-mean CV = %g, want 0 (guard must intercept 0/0)", got)
	}
}

// TestCV_NegativeMeanGuarded pins that an all-negative set (finite, well-formed,
// just negative) yields 0 by the `mean <= 0` guard. For a dispersion metric over
// signed inputs this SUPPRESSES a real, finite CV — documented as a deliberate
// contract ("non-positive mean yields 0"), pinned here so it is explicit.
func TestCV_NegativeMeanGuarded(t *testing.T) {
	t.Parallel()
	got := benchmark.CoefficientOfVariation([]float64{-10, -20, -30})
	if got != 0 {
		t.Fatalf("negative-mean CV = %g, want 0 (documented non-positive-mean contract)", got)
	}
}

// TestCV_TwoSampleKnownOracle pins the smallest non-degenerate case with an
// independent hand oracle, complementing the 5-sample oracle in the base suite.
// Samples {2,4}: mean=3, deviations {-1,1}, sum-sq=2, sample variance=2/(2-1)=2,
// stddev=sqrt(2), CV=sqrt(2)/3. Catches an off-by-one in the Bessel divisor
// (n vs n-1) at the n=2 boundary, where the error is largest.
func TestCV_TwoSampleKnownOracle(t *testing.T) {
	t.Parallel()
	got := benchmark.CoefficientOfVariation([]float64{2, 4})
	want := math.Sqrt(2) / 3
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("CV{2,4} = %.15g, want %.15g (Bessel n-1 at n=2)", got, want)
	}
	// A population-variance bug (divide by n=2) would give sqrt(1)/3 = 0.3333...,
	// which differs from sqrt(2)/3 = 0.4714... by well over the band.
	if math.Abs(got-(1.0/3.0)) < 1e-3 {
		t.Fatalf("CV looks population-variance (≈%.4f); want Bessel sqrt(2)/3=%.4f", 1.0/3.0, want)
	}
}

// TestRunBenchmarks_ConcurrentSinkNoRaceConservation attacks the documented
// "safe for use from multiple goroutines" claim. Many goroutines call
// RunBenchmarks concurrently; they all Store into the package-global atomic
// `sink`. Under -race a non-atomic global would trip; we ALSO assert sink-side
// conservation: every concurrent run returns a real, finite, positive score
// (none lost, none poisoned by interleaving).
func TestRunBenchmarks_ConcurrentSinkNoRaceConservation(t *testing.T) {
	t.Parallel()
	const goroutines = 16
	b := benchmark.New(200_000, nil) // small but real kernel; keep the test fast.

	var wg sync.WaitGroup
	scores := make([]float64, goroutines)
	errs := make([]error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			s, err := b.RunBenchmarks(context.Background())
			scores[idx] = s.CPUScore
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	// Conservation: every run produced a real, usable score.
	for i := 0; i < goroutines; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: unexpected error %v", i, errs[i])
		}
		if !(scores[i] > 0) || math.IsNaN(scores[i]) || math.IsInf(scores[i], 0) {
			t.Fatalf("goroutine %d: CPUScore = %g, want finite > 0 (no lost/poisoned sample)", i, scores[i])
		}
	}
}
