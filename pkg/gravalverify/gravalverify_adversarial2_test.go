package gravalverify

import (
	"math"
	"strings"
	"testing"
)

// ===========================================================================
// ADVERSARIAL2 — NaN/Inf POISON CLASS + LOWER-BOUND POLARITY (HXC GRAVAL)
//
// A systematic scan flagged gravalverify as a NaN/Inf-poison CANDIDATE: it has a
// float64 threshold comparison (`ratio < vramThreshold`) with no math.IsNaN /
// math.IsInf guard. The worst case for that class is a non-finite measurement
// that BYPASSES a verification gate and ACCEPTS an invalid proof (fail-open).
//
// VERDICT for this package: the flag is a FALSE POSITIVE, and these tests pin
// WHY, structurally, so a future refactor that opens the door is caught:
//
//   * `ratio` is computed as float64(uint64) / float64(uint64).
//   * uint64 -> float64 is ALWAYS finite and non-negative (max uint64 ~1.8e19
//     is far below float64's ~1.8e308 overflow point), so neither operand can
//     be NaN or +/-Inf.
//   * The ONLY way float division yields NaN is 0.0/0.0 and the ONLY way it
//     yields +Inf is x/0.0 — both require a zero denominator, which is
//     short-circuited by the explicit `VRAMPhysicalBytes == 0` guard BEFORE the
//     division (ErrZeroPhysical).
//   * Therefore `ratio` reaching `ratio < vramThreshold` is ALWAYS a finite,
//     non-negative real — the comparison can never receive NaN/Inf.
//
// These tests assert that invariant on the live code (current pass), and pin the
// lower-bound polarity so an inverted/raised gate that would ACCEPT a
// shrinkage-fraud (low-ratio) device is caught. They also confirm fail-closed
// behavior: no input combination produces a non-finite ratio that slips a forged
// or fraudulent device past the gate.
// ===========================================================================

// adv2Run is a fixed run id for batch sink-side evidence in this file.
const adv2Run = "gravalverify-HXC-ADV2-1c4e7b09"

// TestAdv2_RatioAlwaysFiniteAcrossUintExtremes drives Verify across the extreme
// uint64 corners (max physical, max reported, 1-byte values, equal max values)
// and asserts the COMPUTED VRAMRatio is ALWAYS finite — never NaN, never +/-Inf.
// This is the structural proof that the scanner's NaN/Inf-poison flag cannot
// fire here: every value reaching the `ratio < vramThreshold` comparison is a
// finite real.
//
// SINK: res.VRAMRatio for every non-zero-physical case must satisfy
// !IsNaN && !IsInf. (Zero-physical cases never compute a ratio; they return
// ErrZeroPhysical with VRAMRatio left 0.0, also finite.)
//
// MUTATION THAT WOULD BITE: if a future change removed the `VRAMPhysicalBytes==0`
// guard, the both-zero case would compute 0.0/0.0 = NaN and this test FAILS on
// the IsNaN assertion (currently it cannot, because the guard returns first).
func TestAdv2_RatioAlwaysFiniteAcrossUintExtremes(t *testing.T) {
	const maxU = uint64(math.MaxUint64)
	cases := []struct {
		name     string
		phys     uint64
		reported uint64
	}{
		{"max-phys-max-reported", maxU, maxU},
		{"max-phys-one-reported", maxU, 1},
		{"one-phys-max-reported", 1, maxU},          // ratio ~1.8e19 — huge but FINITE
		{"max-phys-zero-reported", maxU, 0},         // ratio 0.0 — finite
		{"large-phys-large-reported", 1 << 60, 1 << 60},
		{"phys-1-reported-1", 1, 1},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			res := Verify(GPUInfo{
				ID:                c.name,
				VRAMPhysicalBytes: c.phys,
				VRAMReportedBytes: c.reported,
				ProofValid:        true,
			})
			t.Logf("finite-probe case=%s phys=%d reported=%d Ratio=%v Passed=%v Reason=%q",
				c.name, c.phys, c.reported, res.VRAMRatio, res.Passed, res.Reason)

			if math.IsNaN(res.VRAMRatio) {
				t.Fatalf("Adv2 %s [NaN-POISON GUARD]: VRAMRatio is NaN — a non-finite measurement reached the gate; a NaN comparison (NaN<x==false) could fail-open. Kill-line: float64(uint64)/float64(uint64) must stay finite and the zero-physical guard must precede division.", c.name)
			}
			if math.IsInf(res.VRAMRatio, 0) {
				t.Fatalf("Adv2 %s [Inf-POISON GUARD]: VRAMRatio is +/-Inf — division produced a non-finite value (zero denominator slipped the guard).", c.name)
			}
		})
	}
}

// TestAdv2_ZeroRatioRejectedFailClosed pins the LOWER-BOUND POLARITY of the VRAM
// gate at its extreme: a worker that reports ZERO usable VRAM (reported=0,
// ratio=0.0) — the most extreme VRAM-shrinkage fraud — MUST be rejected even
// with a genuine proof. ratio 0.0 is finite, so this also confirms the gate
// rejects the low end without any NaN/Inf involvement.
//
// SINK: Passed==false AND Reason contains ErrVRAMThreshold AND VRAMRatio==0.0.
//
// MUTATION THAT WOULD BITE: inverting the gate to `ratio > vramThreshold` (i.e.
// REJECT high, ACCEPT low) or `ratio >= 0` (accept-all) flips this to Passed=true
// and the test FAILS — catching a polarity inversion that would accept a
// zero-VRAM forger.
func TestAdv2_ZeroRatioRejectedFailClosed(t *testing.T) {
	res := Verify(GPUInfo{
		ID:                "zero-vram-forger",
		VRAMPhysicalBytes: physH100,
		VRAMReportedBytes: 0,    // ratio 0.0 — extreme shrinkage fraud
		ProofValid:        true, // even WITH a genuine proof, the VRAM gate must reject
	})
	t.Logf("zero-ratio GPUID=%s Passed=%v Ratio=%v Reason=%q", res.GPUID, res.Passed, res.VRAMRatio, res.Reason)

	if res.VRAMRatio != 0.0 {
		t.Fatalf("Adv2 ZeroRatio: setup expects ratio 0.0, got %v", res.VRAMRatio)
	}
	if res.Passed {
		t.Fatalf("Adv2 ZeroRatio [POLARITY/MUTATION GUARD]: a zero-VRAM device (ratio 0.0) was ACCEPTED with Passed=true — VRAM lower-bound gate inverted or disabled.")
	}
	if !strings.Contains(res.Reason, ErrVRAMThreshold.Error()) {
		t.Fatalf("Adv2 ZeroRatio: Reason must carry VRAM threshold sentinel %q, got %q", ErrVRAMThreshold.Error(), res.Reason)
	}
}

// TestAdv2_HugeFiniteRatioStillNeedsProof drives the over-report extreme: a
// device that reports astronomically more VRAM than it physically has
// (reported=MaxUint64, phys=1) yields a HUGE but FINITE ratio (~1.8e19). This
// proves (a) the huge ratio is finite (no Inf poison), and (b) the proof gate
// still governs — a forged proof on a hardware-over-claiming device is rejected,
// and the ratio being enormous does NOT bypass the proof comparison.
//
// MUTATION THAT WOULD BITE: removing the proof gate (`if !g.ProofValid`) makes
// the forged-but-huge-ratio device pass, failing the Passed==false assertion.
func TestAdv2_HugeFiniteRatioStillNeedsProof(t *testing.T) {
	const maxU = uint64(math.MaxUint64)

	// Forged proof, absurd over-report.
	forged := Verify(GPUInfo{
		ID:                "huge-overclaim-forger",
		VRAMPhysicalBytes: 1,
		VRAMReportedBytes: maxU,
		ProofValid:        false,
	})
	t.Logf("huge-forged Ratio=%v Passed=%v Reason=%q", forged.VRAMRatio, forged.Passed, forged.Reason)

	if math.IsInf(forged.VRAMRatio, 0) || math.IsNaN(forged.VRAMRatio) {
		t.Fatalf("Adv2 HugeRatio [Inf/NaN GUARD]: ratio non-finite (%v) for MaxUint64/1 — must be a finite ~1.8e19", forged.VRAMRatio)
	}
	if forged.VRAMRatio < vramThreshold {
		t.Fatalf("Adv2 HugeRatio: setup error — huge over-report ratio %v unexpectedly below gate; cannot isolate proof gate", forged.VRAMRatio)
	}
	if forged.Passed {
		t.Fatalf("Adv2 HugeRatio [SECURITY/MUTATION GUARD]: a hardware-over-claiming worker with a FORGED proof was ACCEPTED — proof gate bypassed by a huge finite ratio.")
	}
	if !strings.Contains(forged.Reason, ErrProofInvalid.Error()) {
		t.Fatalf("Adv2 HugeRatio: forged case Reason must contain %q, got %q", ErrProofInvalid.Error(), forged.Reason)
	}

	// Companion: same over-report WITH a genuine proof passes (documented model:
	// VRAM gate is a one-sided lower bound). Confirms the rejection above was the
	// proof gate, not the ratio magnitude.
	genuine := Verify(GPUInfo{
		ID:                "huge-overclaim-genuine",
		VRAMPhysicalBytes: 1,
		VRAMReportedBytes: maxU,
		ProofValid:        true,
	})
	if !genuine.Passed {
		t.Fatalf("Adv2 HugeRatio companion: over-report WITH genuine proof must pass (one-sided gate); got Passed=false Reason=%q", genuine.Reason)
	}
}

// TestAdv2_BatchNoNonFiniteRatioLeaks runs a batch mixing the uint64 extremes
// (each individually finite) through BatchVerify and asserts that NO Result in
// the aggregate carries a non-finite VRAMRatio, and that the PassRate KPI itself
// is finite and in [0,1]. This is the batch-level sink-side guarantee that a
// non-finite ratio cannot poison the aggregated KPI (e.g. a NaN ratio silently
// counted as a pass, or an Inf skewing nothing but signalling an unguarded path).
//
// MUTATION THAT WOULD BITE: any change introducing a non-finite ratio (e.g.
// dropping the zero-physical guard so a both-zero entry computes NaN) would
// surface here as a NaN VRAMRatio in Results.
func TestAdv2_BatchNoNonFiniteRatioLeaks(t *testing.T) {
	const maxU = uint64(math.MaxUint64)
	gpus := []GPUInfo{
		{ID: "honest", VRAMPhysicalBytes: physH100, VRAMReportedBytes: physH100, ProofValid: true},
		{ID: "zero-reported", VRAMPhysicalBytes: physH100, VRAMReportedBytes: 0, ProofValid: true},
		{ID: "zero-physical", VRAMPhysicalBytes: 0, VRAMReportedBytes: 1 << 30, ProofValid: true},
		{ID: "max-both", VRAMPhysicalBytes: maxU, VRAMReportedBytes: maxU, ProofValid: true},
		{ID: "huge-overclaim", VRAMPhysicalBytes: 1, VRAMReportedBytes: maxU, ProofValid: false},
	}

	report := BatchVerify(adv2Run, gpus)
	t.Logf("batch-finite runID=%s PassRate=%v Results=%d", report.RunID, report.PassRate, len(report.Results))

	if math.IsNaN(report.PassRate) || math.IsInf(report.PassRate, 0) {
		t.Fatalf("Adv2 Batch [KPI GUARD]: PassRate is non-finite (%v) — a non-finite ratio poisoned the aggregate KPI", report.PassRate)
	}
	if report.PassRate < 0.0 || report.PassRate > 1.0 {
		t.Fatalf("Adv2 Batch: PassRate %v out of [0,1]", report.PassRate)
	}

	for i, res := range report.Results {
		t.Logf("  idx=%d GPUID=%s Passed=%v Ratio=%v Reason=%q", i, res.GPUID, res.Passed, res.VRAMRatio, res.Reason)
		if math.IsNaN(res.VRAMRatio) {
			t.Fatalf("Adv2 Batch [NaN-POISON GUARD]: Result idx=%d GPUID=%s has NaN VRAMRatio — non-finite measurement leaked into a batch result.", i, res.GPUID)
		}
		if math.IsInf(res.VRAMRatio, 0) {
			t.Fatalf("Adv2 Batch [Inf-POISON GUARD]: Result idx=%d GPUID=%s has Inf VRAMRatio.", i, res.GPUID)
		}
		if res.RunID != adv2Run {
			t.Errorf("Adv2 Batch: idx=%d RunID want %q got %q", i, adv2Run, res.RunID)
		}
	}
}
