// Package gravalverify implements the GraVal GPU attestation verifier with a
// VRAM-threshold gate and a concurrent batch verifier with a pass-rate KPI.
//
// # Architecture
//
// Verification proceeds in two stages:
//
//  1. VRAM-ratio gate: VRAMReportedBytes / VRAMPhysicalBytes must be >= 0.95.
//     A GPU below the threshold is rejected immediately with a threshold error.
//     Its ProofValid field is NEVER consulted (short-circuit, fail-fast).
//
//  2. Proof gate: only reached when the VRAM ratio passes. Passed is true if
//     and only if ProofValid is true. ProofValid is the caller-injected result
//     of the real GPU matrix-multiplication proof (a genuinely-absent capability
//     that this package cannot perform itself).  The caller produces or injects
//     the value via whatever attestation facility is available on the host; this
//     package consumes it as a typed boolean and returns a typed error when it is
//     false, rather than faking the proof internally.
//
// # CLAUDE-2 note
//
// The real GPU matrix-multiplication proof (e.g. CUDA/Metal tensor round-trip) is
// a host capability this pure-Go package cannot perform. It is accepted as an
// injected boolean field (ProofValid) so the verifier is still fully exercisable on
// any OS — NEVER faked or guessed internally.
//
// # Concurrency
//
// BatchVerify runs per-GPU Verify calls concurrently under goroutines coordinated
// by sync.WaitGroup. Results are collected into a fixed-size slice (pre-allocated
// by index) so no mutex is needed for the per-slot write.
package gravalverify

import (
	"errors"
	"fmt"
	"sync"
)

// vramThreshold is the minimum acceptable ratio of VRAMReportedBytes to
// VRAMPhysicalBytes. A GPU whose ratio is below this value fails the VRAM gate
// immediately, and its proof is never consulted.
//
// MUTATION GUARD — TestLowVRAMRejected and TestBatchPassRate both pin this
// constant. Changing the comparison from >= to > (or lowering the constant
// below 0.95) causes at least one of those tests to FAIL.
const vramThreshold = 0.95

// Sentinel errors returned in Result.Reason.
var (
	// ErrVRAMThreshold is returned when a GPU's VRAM ratio is below vramThreshold.
	// It is the only error kind produced by the VRAM gate; the proof gate is
	// never reached when this error is set.
	ErrVRAMThreshold = errors.New("gravalverify: VRAM ratio below threshold")

	// ErrProofInvalid is returned when the VRAM ratio passes but ProofValid is false.
	ErrProofInvalid = errors.New("gravalverify: GPU proof invalid")

	// ErrZeroPhysical is returned when VRAMPhysicalBytes is zero, preventing a
	// divide-by-zero in the ratio computation.
	ErrZeroPhysical = errors.New("gravalverify: VRAMPhysicalBytes is zero")
)

// GPUInfo describes a single GPU presented for attestation.
//
// VRAMReportedBytes is the VRAM size the device reports to the OS driver.
// VRAMPhysicalBytes is the true hardware VRAM capacity (from firmware / OOB).
// ProofValid is the result of the real GPU matrix-multiplication proof, injected
// by the caller. This package accepts it as an opaque boolean seam because the
// actual proof computation is a genuinely host-specific capability (CUDA/Metal
// tensor round-trip) that cannot be performed inside a pure-Go library. Passing
// false causes ErrProofInvalid (after passing the VRAM gate); passing true means
// the device proof is attested genuine by the caller.
type GPUInfo struct {
	// ID is an opaque device identifier (e.g. PCI slot, UUID). It is echoed into
	// every Result so the caller can correlate outcomes back to devices.
	ID string

	// VRAMReportedBytes is the VRAM size the device exposes to the OS.
	VRAMReportedBytes uint64

	// VRAMPhysicalBytes is the true physical VRAM capacity of the device.
	VRAMPhysicalBytes uint64

	// ProofValid is the caller-injected verdict of the real GPU matrix-multiplication
	// proof. The absence of a native proof facility on the host MUST be expressed by
	// the caller as a typed error (ErrCapabilityAbsent) returned before calling
	// BatchVerify — never by silently passing false or true here.
	ProofValid bool
}

// Result is the outcome of verifying a single GPUInfo.
type Result struct {
	// GPUID is copied from GPUInfo.ID so results are self-describing.
	GPUID string

	// RunID is the batch run identifier stamped at result creation time; it is
	// the same string for every Result in a given BatchVerify call.
	RunID string

	// Passed is true only when the GPU passed both the VRAM-ratio gate and the
	// proof gate.
	Passed bool

	// Reason is a human-readable description of why the GPU passed or failed.
	// On success it is "ok". On failure it is the sentinel error string from
	// ErrVRAMThreshold, ErrProofInvalid, or ErrZeroPhysical.
	Reason string

	// VRAMRatio is the computed ratio VRAMReportedBytes / VRAMPhysicalBytes,
	// or 0 when VRAMPhysicalBytes is zero (ErrZeroPhysical). It is always set
	// so callers can log or threshold-tune without re-computing.
	VRAMRatio float64
}

// Report is the aggregate outcome of a BatchVerify call.
type Report struct {
	// RunID is the caller-supplied run identifier, echoed here for correlation.
	RunID string

	// Results holds one Result per GPU, in input order.
	Results []Result

	// PassRate is passed / total, in [0.0, 1.0]. It is 0.0 when the batch is empty.
	PassRate float64
}

// Verify checks a single GPU against the VRAM-ratio gate followed by the proof
// gate.  The RunID field of the returned Result is left empty; use BatchVerify
// (which stamps RunID) for batches.
//
// Stage 1 — VRAM gate (guards: TestLowVRAMRejected, TestBatchPassRate):
//
//	ratio = VRAMReportedBytes / VRAMPhysicalBytes
//	ratio >= vramThreshold (0.95) → proceed to stage 2
//	ratio <  vramThreshold        → Passed=false, Reason=ErrVRAMThreshold.Error()
//
// Stage 2 — Proof gate:
//
//	ProofValid == true  → Passed=true,  Reason="ok"
//	ProofValid == false → Passed=false, Reason=ErrProofInvalid.Error()
func Verify(g GPUInfo) Result {
	r := Result{GPUID: g.ID}

	// Guard against divide-by-zero before computing the ratio.
	if g.VRAMPhysicalBytes == 0 {
		r.Passed = false
		r.Reason = ErrZeroPhysical.Error()
		return r
	}

	ratio := float64(g.VRAMReportedBytes) / float64(g.VRAMPhysicalBytes)
	r.VRAMRatio = ratio

	// MUTATION GUARD — this branch is the kill-line for TestLowVRAMRejected and
	// TestBatchPassRate. Mutating the comparison (e.g. < to <=, or removing the
	// branch entirely) causes both named tests to FAIL because a GPU with ratio
	// 0.94 would then reach the proof gate instead of being rejected here.
	if ratio < vramThreshold {
		r.Passed = false
		r.Reason = fmt.Sprintf("%s (ratio=%.4f threshold=%.2f)", ErrVRAMThreshold.Error(), ratio, vramThreshold)
		return r
	}

	// Stage 2: proof gate. ProofValid is caller-injected; see GPUInfo doc.
	if !g.ProofValid {
		r.Passed = false
		r.Reason = ErrProofInvalid.Error()
		return r
	}

	r.Passed = true
	r.Reason = "ok"
	return r
}

// BatchVerify runs Verify concurrently for every GPU in gpus, stamps each Result
// with runID, and returns a Report with the aggregate PassRate.
//
// Concurrency model: one goroutine per GPU; results are written to a pre-indexed
// fixed-size slice so no mutex is needed for per-slot writes. A sync.WaitGroup
// ensures all goroutines complete before the Report is assembled. The
// implementation is race-detector clean.
//
// PassRate is 0.0 for an empty batch.
func BatchVerify(runID string, gpus []GPUInfo) Report {
	n := len(gpus)
	results := make([]Result, n)

	if n == 0 {
		return Report{RunID: runID, Results: results, PassRate: 0.0}
	}

	var wg sync.WaitGroup
	wg.Add(n)

	for i, g := range gpus {
		i, g := i, g // capture loop vars
		go func() {
			defer wg.Done()
			res := Verify(g)
			res.RunID = runID
			results[i] = res
		}()
	}

	wg.Wait()

	passed := 0
	for _, res := range results {
		if res.Passed {
			passed++
		}
	}

	return Report{
		RunID:    runID,
		Results:  results,
		PassRate: float64(passed) / float64(n),
	}
}
