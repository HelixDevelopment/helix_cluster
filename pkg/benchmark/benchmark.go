// Package benchmark implements HXC-1308: an on-host micro-benchmark harness
// that produces NORMALIZED per-compute-class scores suitable for
// tier-assignment and scheduling hints.
//
// The CPU score is a REAL, repeatable measurement: RunBenchmarks times a fixed
// integer+float arithmetic workload on the host CPU and divides the achieved
// op-rate by a fixed reference rate, yielding a dimensionless normalized score
// (1.0 == a host that matches the reference rate exactly). The workload is a
// genuine computation whose result is consumed (and asserted in tests) so the
// Go compiler cannot elide it — the score therefore reflects actual CPU
// throughput on the running host.
//
// GPU TFLOPS-effective and NPU TOPS-effective are typed fields on the output
// manifest. They are 0 when no such device is probed/declared on this host, but
// the type and manifest shape are real so a node that DOES carry an accelerator
// (declared via the operator override seam) reports a non-zero figure that
// AssignTier-style logic can consume — without this package importing or
// modifying pkg/capability, pkg/discovery, or pkg/resources.
//
// Cross-node relative comparison (e.g. EPYC vs RK3588) is intentionally out of
// host scope: this harness measures the host it runs on and emits a normalized
// score; a second node's score is gathered by running this same harness there.
//
// Pure Go standard library only.
package benchmark

import (
	"context"
	"math"
	"sync/atomic"
	"time"
)

// referenceOpsPerSecond is the fixed normalization reference: the op-rate of a
// notional baseline core. The CPU score is achievedOpsPerSecond divided by this
// constant, so the score is dimensionless and comparable across hosts that all
// use the SAME reference. It is deliberately a round, documented constant rather
// than a measured-on-first-run value so the normalization is stable and
// reproducible across processes. One "op" is one iteration of the inner kernel
// (see cpuKernel), which performs a fixed mix of integer and float arithmetic.
const referenceOpsPerSecond = 50_000_000.0

// defaultIterations is the kernel iteration count used by a zero-value
// Benchmarker. It is large enough that wall-clock timing dominates scheduler
// jitter on the hosts we target, keeping run-to-run variation low, yet small
// enough to complete in well under a second.
const defaultIterations = 8_000_000

// CapabilityScore is the normalized output manifest of a benchmark run. It is
// the documented seam that AssignTier-style logic consumes: a higher CPUScore
// means a faster host CPU; GPUTFLOPSEffective / NPUTOPSEffective are the
// per-class accelerator figures (0 when absent).
//
// CapabilityScore is a plain value type with no dependency on pkg/capability so
// it can be embedded or mapped by any scheduler package without coupling.
type CapabilityScore struct {
	// CPUScore is the normalized CPU throughput: achieved kernel op-rate divided
	// by referenceOpsPerSecond. It is > 0 on any host where the benchmark ran,
	// and ~1.0 on a host matching the reference rate. It varies slightly
	// run-to-run; callers must treat it as repeatable-within-tolerance, not
	// bit-exact.
	CPUScore float64

	// GPUTFLOPSEffective is the effective single-precision GPU throughput in
	// TFLOPS. 0 when no GPU is probed/declared on this host.
	GPUTFLOPSEffective float64

	// NPUTOPSEffective is the effective NPU throughput in TOPS. 0 when no NPU is
	// probed/declared on this host.
	NPUTOPSEffective float64

	// CPUOpsPerSecond is the raw (un-normalized) achieved kernel op-rate that the
	// CPUScore was derived from, exposed for diagnostics and so a caller can
	// re-normalize against a different reference if desired. Always > 0.
	CPUOpsPerSecond float64

	// Iterations is the kernel iteration count that produced this measurement.
	Iterations int

	// Elapsed is the wall-clock time the CPU kernel took. Always > 0.
	Elapsed time.Duration
}

// AcceleratorProbe supplies per-class accelerator capacity that the host cannot
// self-time from pure-Go arithmetic (GPU/NPU peak throughput). It is the seam
// through which a real probe (operator override labels, a sysfs reader, etc.)
// is injected WITHOUT this package importing those packages. A nil probe means
// "no accelerators" and yields zeroed accelerator fields.
type AcceleratorProbe interface {
	// GPUTFLOPS returns the host's effective GPU FP32 throughput in TFLOPS, or 0
	// when none is present.
	GPUTFLOPS() float64
	// NPUTOPS returns the host's effective NPU throughput in TOPS, or 0 when none
	// is present.
	NPUTOPS() float64
}

// StaticAccelerators is an AcceleratorProbe backed by fixed values, for callers
// that already hold probed/declared figures (and for tests). The zero value
// reports no accelerators.
type StaticAccelerators struct {
	GPU float64
	NPU float64
}

// GPUTFLOPS implements AcceleratorProbe.
func (s StaticAccelerators) GPUTFLOPS() float64 { return s.GPU }

// NPUTOPS implements AcceleratorProbe.
func (s StaticAccelerators) NPUTOPS() float64 { return s.NPU }

// Benchmarker runs the on-host micro-benchmarks. The zero value is usable and
// runs defaultIterations with no accelerator probe (all-CPU score). Use New to
// override the iteration count or attach an AcceleratorProbe.
type Benchmarker struct {
	// iterations is the CPU kernel iteration count; <=0 means defaultIterations.
	iterations int
	// accel supplies GPU/NPU figures; nil means none.
	accel AcceleratorProbe
}

// New constructs a Benchmarker. iterations <= 0 selects defaultIterations. A
// nil probe yields zeroed accelerator fields.
func New(iterations int, accel AcceleratorProbe) *Benchmarker {
	return &Benchmarker{iterations: iterations, accel: accel}
}

// effectiveIterations resolves the iteration count, applying the default.
func (b *Benchmarker) effectiveIterations() int {
	if b == nil || b.iterations <= 0 {
		return defaultIterations
	}
	return b.iterations
}

// cpuKernel runs n iterations of a fixed integer+float arithmetic workload and
// returns an accumulator derived from every iteration. The returned value is
// consumed by RunBenchmarks (folded into the score path and surfaced to tests)
// so the compiler cannot dead-code-eliminate the loop: the measurement reflects
// real CPU work.
//
// The kernel mixes a 64-bit integer LCG step (integer mul/add) with a
// floating-point fused multiply-add and a sqrt, so it exercises both the
// integer and FP units the way a representative compute workload would.
func cpuKernel(n int) float64 {
	var iacc uint64 = 0x9e3779b97f4a7c15
	facc := 1.0
	for i := 0; i < n; i++ {
		// Integer LCG step (Knuth MMIX constants): real integer ALU work.
		iacc = iacc*6364136223846793005 + 1442695040888963407
		// Derive a bounded float from the integer state and do FP work on it.
		x := float64(iacc>>40) * (1.0 / 16777216.0) // in [0, ~1)
		facc = math.Sqrt(facc*0.5 + x*1.5 + 1.0)
	}
	// Fold the integer accumulator in so it is also observed.
	return facc + float64(iacc&0xffff)*1e-9
}

// sink prevents the compiler from proving cpuKernel's result is unused across
// the whole program. RunBenchmarks stores into it atomically so concurrent
// benchmark runs (the harness is safe for use from multiple goroutines) do not
// race on the global.
var sink atomic.Uint64

// RunBenchmarks executes the host micro-benchmarks and returns a
// CapabilityScore. The CPU score is always a real measurement of the running
// host; accelerator fields come from the configured AcceleratorProbe (0 when
// none).
//
// The context is honored cooperatively: if ctx is already cancelled when
// RunBenchmarks is called, it returns ctx.Err() without running the kernel. The
// kernel itself is a single tight timed loop (cancellation mid-kernel would
// corrupt the timing), so cancellation is checked at the boundary only.
func (b *Benchmarker) RunBenchmarks(ctx context.Context) (CapabilityScore, error) {
	if err := ctx.Err(); err != nil {
		return CapabilityScore{}, err
	}

	iters := b.effectiveIterations()

	start := time.Now()
	result := cpuKernel(iters)
	elapsed := time.Since(start)
	sink.Store(math.Float64bits(result)) // observe the kernel output process-wide.

	// Guard against a zero/absurdly-small elapsed (e.g. a clock with coarse
	// granularity returning 0): floor it at 1ns so the rate is finite and >0.
	if elapsed <= 0 {
		elapsed = time.Nanosecond
	}
	opsPerSecond := float64(iters) / elapsed.Seconds()
	score := opsPerSecond / referenceOpsPerSecond

	out := CapabilityScore{
		CPUScore:        score,
		CPUOpsPerSecond: opsPerSecond,
		Iterations:      iters,
		Elapsed:         elapsed,
	}
	if b != nil && b.accel != nil {
		out.GPUTFLOPSEffective = b.accel.GPUTFLOPS()
		out.NPUTOPSEffective = b.accel.NPUTOPS()
	}
	return out, nil
}

// CoefficientOfVariation returns the coefficient of variation (stddev / mean)
// of a set of samples, a unitless measure of dispersion. It is the documented
// repeatability metric for the CPU score: callers (and tests) assert the CV of
// several CPUScore samples is under a tolerance band rather than asserting
// bit-exactness. Fewer than two samples, or a non-positive mean, yields 0.
func CoefficientOfVariation(samples []float64) float64 {
	if len(samples) < 2 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += s
	}
	mean := sum / float64(len(samples))
	if mean <= 0 {
		return 0
	}
	var sq float64
	for _, s := range samples {
		d := s - mean
		sq += d * d
	}
	variance := sq / float64(len(samples)-1) // sample variance (Bessel).
	return math.Sqrt(variance) / mean
}
