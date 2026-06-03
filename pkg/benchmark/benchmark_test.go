package benchmark_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/benchmark"
)

// TestRunBenchmarksRealCPUScorePositive proves the harness yields a REAL,
// positive normalized CPU score on THIS host: the kernel ran, took measurable
// wall-clock time, and produced a > 0 op-rate and > 0 normalized score.
//
// Load-bearing: if RunBenchmarks returned a zeroed/elided score (e.g. the
// kernel were dead-code-eliminated so opsPerSecond computed from a zero elapsed
// guard, or the score divided wrong), CPUScore or CPUOpsPerSecond would not be
// > 0 and this fails.
func TestRunBenchmarksRealCPUScorePositive(t *testing.T) {
	t.Parallel()
	b := benchmark.New(0, nil) // default iterations, no accelerators.
	score, err := b.RunBenchmarks(context.Background())
	if err != nil {
		t.Fatalf("RunBenchmarks: unexpected error: %v", err)
	}
	if score.CPUScore <= 0 {
		t.Fatalf("CPUScore = %g, want > 0", score.CPUScore)
	}
	if score.CPUOpsPerSecond <= 0 {
		t.Fatalf("CPUOpsPerSecond = %g, want > 0", score.CPUOpsPerSecond)
	}
	if score.Elapsed <= 0 {
		t.Fatalf("Elapsed = %v, want > 0", score.Elapsed)
	}
	if score.Iterations <= 0 {
		t.Fatalf("Iterations = %d, want > 0", score.Iterations)
	}
	// The op-rate and normalized score must be self-consistent: score ==
	// opsPerSecond / reference. Recompute from the exposed raw rate and assert
	// they agree, so a normalization bug cannot hide.
	wantScore := score.CPUOpsPerSecond / 50_000_000.0
	if rel := math.Abs(score.CPUScore-wantScore) / wantScore; rel > 1e-9 {
		t.Fatalf("CPUScore %g inconsistent with CPUOpsPerSecond %g (want %g, rel=%g)",
			score.CPUScore, score.CPUOpsPerSecond, wantScore, rel)
	}
}

// TestRunBenchmarksRepeatableWithinTolerance proves REPEATABILITY (not
// bit-exactness): across N runs on this host the CPU score's coefficient of
// variation stays under a tolerance band. This is the sink-side closure
// assertion — a usable scheduler hint must be stable run-to-run.
//
// Load-bearing: if the kernel were elided (giving wildly varying near-zero
// elapsed times) or the score were derived from something non-deterministic
// like rand, the CV would blow past the band and this fails.
func TestRunBenchmarksRepeatableWithinTolerance(t *testing.T) {
	t.Parallel()
	const runs = 7
	// Use a modest iteration count: large enough to dominate jitter, small
	// enough to keep the test fast. This exercises the New(iterations,...) seam.
	b := benchmark.New(4_000_000, nil)

	samples := make([]float64, 0, runs)
	for i := 0; i < runs; i++ {
		score, err := b.RunBenchmarks(context.Background())
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if score.CPUScore <= 0 {
			t.Fatalf("run %d: CPUScore = %g, want > 0", i, score.CPUScore)
		}
		samples = append(samples, score.CPUScore)
	}

	cv := benchmark.CoefficientOfVariation(samples)
	// 0.5 is a deliberately generous band so CI scheduler noise / shared runners
	// do not flake; a healthy host is typically well under 0.15. We assert
	// stability, not a specific speed.
	const tolerance = 0.5
	if cv >= tolerance {
		t.Fatalf("CPU score not repeatable: coefficient of variation %g >= %g across %d runs (samples=%v)",
			cv, tolerance, runs, samples)
	}
	if cv < 0 {
		t.Fatalf("coefficient of variation %g must not be negative", cv)
	}
}

// TestRunBenchmarksAcceleratorSeam proves the GPU/NPU manifest fields are real
// typed outputs wired to the injected probe: a declared accelerator surfaces a
// non-zero figure, and a nil probe yields zero. This is the AssignTier seam.
//
// Load-bearing: if RunBenchmarks ignored the probe (or hard-coded 0/garbage),
// the declared 31.2 TFLOPS / 38.0 TOPS would not appear.
func TestRunBenchmarksAcceleratorSeam(t *testing.T) {
	t.Parallel()

	withAccel := benchmark.New(1_000_000, benchmark.StaticAccelerators{GPU: 31.2, NPU: 38.0})
	score, err := withAccel.RunBenchmarks(context.Background())
	if err != nil {
		t.Fatalf("RunBenchmarks (accel): %v", err)
	}
	if score.GPUTFLOPSEffective != 31.2 {
		t.Fatalf("GPUTFLOPSEffective = %g, want 31.2", score.GPUTFLOPSEffective)
	}
	if score.NPUTOPSEffective != 38.0 {
		t.Fatalf("NPUTOPSEffective = %g, want 38.0", score.NPUTOPSEffective)
	}

	noAccel := benchmark.New(1_000_000, nil)
	z, err := noAccel.RunBenchmarks(context.Background())
	if err != nil {
		t.Fatalf("RunBenchmarks (nil accel): %v", err)
	}
	if z.GPUTFLOPSEffective != 0 || z.NPUTOPSEffective != 0 {
		t.Fatalf("nil probe must yield zero accelerators, got GPU=%g NPU=%g",
			z.GPUTFLOPSEffective, z.NPUTOPSEffective)
	}
}

// TestRunBenchmarksContextCancelled proves cancellation is honored at the
// boundary: a pre-cancelled context returns its error and no measurement.
func TestRunBenchmarksContextCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	score, err := benchmark.New(0, nil).RunBenchmarks(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if score != (benchmark.CapabilityScore{}) {
		t.Fatalf("want zero CapabilityScore on cancellation, got %+v", score)
	}
}

// TestCoefficientOfVariation pins the repeatability metric with a hand-computed
// oracle so a wrong formula (e.g. population vs SAMPLE/Bessel variance, or
// mean/stddev swap) is caught independently of the live benchmark.
//
// Samples {10,20,30,40,50}: mean=30; deviations {-20,-10,0,10,20} => sum of
// squares 1000; SAMPLE variance = 1000/(5-1) = 250; sample stddev = sqrt(250) =
// 15.8113883...; CV = stddev/mean = 0.52704627669... A population-variance
// implementation (divide by 5) would give stddev=sqrt(200) and CV=0.4714, which
// is outside the 1e-12 band — so this test pins the Bessel correction.
func TestCoefficientOfVariation(t *testing.T) {
	t.Parallel()
	samples := []float64{10, 20, 30, 40, 50}
	got := benchmark.CoefficientOfVariation(samples)
	want := math.Sqrt(250) / 30 // independently derived Bessel-corrected oracle.
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("CoefficientOfVariation = %.15g, want %.15g", got, want)
	}

	// Degenerate inputs return 0 (documented contract).
	if v := benchmark.CoefficientOfVariation([]float64{42}); v != 0 {
		t.Fatalf("single sample CV = %g, want 0", v)
	}
	if v := benchmark.CoefficientOfVariation(nil); v != 0 {
		t.Fatalf("nil CV = %g, want 0", v)
	}
}

// TestZeroValueBenchmarker proves the zero-value Benchmarker is usable and uses
// the default iteration count.
func TestZeroValueBenchmarker(t *testing.T) {
	t.Parallel()
	var b benchmark.Benchmarker
	score, err := b.RunBenchmarks(context.Background())
	if err != nil {
		t.Fatalf("zero-value RunBenchmarks: %v", err)
	}
	if score.CPUScore <= 0 {
		t.Fatalf("zero-value CPUScore = %g, want > 0", score.CPUScore)
	}
	if score.Iterations != 8_000_000 {
		t.Fatalf("zero-value Iterations = %d, want default 8000000", score.Iterations)
	}
}
