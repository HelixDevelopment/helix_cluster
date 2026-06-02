package gravalverify

import (
	"strings"
	"testing"
)

// runID is the fixed run identifier stamped on all Results in this test suite.
// Every assertion that touches Result.RunID checks against this constant so that
// a code change stripping RunID propagation is immediately caught.
const runID = "gravalverify-HXC-TEST-7f3a9d12-4c5e-4b7a-b821-0e9f3c2d8a44"

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeGPU returns a GPUInfo where the VRAM ratio equals reportedFraction of
// physicalBytes. ProofValid is caller-controlled.
func makeGPU(id string, physicalBytes uint64, reportedFraction float64, proofValid bool) GPUInfo {
	reported := uint64(float64(physicalBytes) * reportedFraction)
	return GPUInfo{
		ID:                id,
		VRAMPhysicalBytes: physicalBytes,
		VRAMReportedBytes: reported,
		ProofValid:        proofValid,
	}
}

// ---------------------------------------------------------------------------
// TestLowVRAMRejected
//
// MUTATION GUARD: the >= 0.95 threshold branch in Verify is the kill-line.
// Removing or mutating it (e.g. ratio < 0.0, or always-pass) causes this test
// to FAIL because the Result would have Passed=true instead of Passed=false,
// and Reason would not contain ErrVRAMThreshold.
// ---------------------------------------------------------------------------

// TestLowVRAMRejected verifies that a GPU whose VRAM ratio is below the 0.95
// threshold is rejected with Passed=false and a reason containing the threshold
// sentinel, WITHOUT consulting ProofValid.
//
// The GPU is constructed with ratio ≈ 0.94 (below 0.95). Even though ProofValid
// is set to true (which would normally pass the proof gate), the VRAM gate must
// short-circuit and produce Passed=false.
//
// Observable sink: Result.Passed == false AND Result.Reason contains
// ErrVRAMThreshold.Error(). The proof gate is NOT consulted (Reason must NOT
// contain ErrProofInvalid).
func TestLowVRAMRejected(t *testing.T) {
	const physBytes = uint64(80 << 30) // 80 GiB — typical H100

	// ratio ≈ 0.94 — just below the 0.95 gate.
	// ProofValid=true: if the gate were absent, this would pass through and
	// yield Passed=true, causing this test to FAIL.
	g := makeGPU("gpu-low-vram", physBytes, 0.94, true)

	res := Verify(g)

	t.Logf("TestLowVRAMRejected runID=%s GPUID=%s Passed=%v Ratio=%.4f Reason=%q",
		runID, res.GPUID, res.Passed, res.VRAMRatio, res.Reason)

	// --- sink-side assertions ---

	// 1. Passed must be false: the VRAM gate must reject this device.
	if res.Passed {
		t.Fatalf("TestLowVRAMRejected [MUTATION GUARD]: expected Passed=false for ratio %.4f < %.2f, got Passed=true (guard: if ratio < vramThreshold in Verify)",
			res.VRAMRatio, vramThreshold)
	}

	// 2. Reason must contain the threshold sentinel.
	if !strings.Contains(res.Reason, ErrVRAMThreshold.Error()) {
		t.Fatalf("TestLowVRAMRejected: expected Reason to contain %q, got %q",
			ErrVRAMThreshold.Error(), res.Reason)
	}

	// 3. Reason must NOT contain the proof error (proof gate must be short-circuited).
	if strings.Contains(res.Reason, ErrProofInvalid.Error()) {
		t.Fatalf("TestLowVRAMRejected: proof gate must not be consulted when VRAM gate fails; got Reason=%q", res.Reason)
	}

	// 4. GPUID must be echoed.
	if res.GPUID != g.ID {
		t.Fatalf("TestLowVRAMRejected: GPUID mismatch: want %q got %q", g.ID, res.GPUID)
	}

	// 5. Ratio must be reported accurately.
	want := float64(g.VRAMReportedBytes) / float64(g.VRAMPhysicalBytes)
	if res.VRAMRatio != want {
		t.Fatalf("TestLowVRAMRejected: VRAMRatio want %.6f got %.6f", want, res.VRAMRatio)
	}
}

// TestVRAMExactThreshold verifies the boundary: a GPU whose ratio is exactly
// 0.95 (the threshold) passes the VRAM gate and reaches the proof gate.
// ProofValid=true → Passed=true.
func TestVRAMExactThreshold(t *testing.T) {
	const physBytes = uint64(80 << 30)
	// Construct exact 0.95 ratio with integer arithmetic.
	reported := uint64(float64(physBytes) * 0.95)
	g := GPUInfo{
		ID:                "gpu-exact-threshold",
		VRAMPhysicalBytes: physBytes,
		VRAMReportedBytes: reported,
		ProofValid:        true,
	}

	res := Verify(g)

	t.Logf("TestVRAMExactThreshold GPUID=%s Passed=%v Ratio=%.6f Reason=%q",
		res.GPUID, res.Passed, res.VRAMRatio, res.Reason)

	// The ratio may be AT or very slightly below 0.95 due to integer truncation
	// in makeGPU arithmetic. Test the real computed ratio vs the gate.
	actualRatio := float64(g.VRAMReportedBytes) / float64(g.VRAMPhysicalBytes)
	if actualRatio >= vramThreshold {
		if !res.Passed {
			t.Fatalf("TestVRAMExactThreshold: ratio=%.6f >= %.2f should pass, got Passed=false Reason=%q",
				actualRatio, vramThreshold, res.Reason)
		}
		if res.Reason != "ok" {
			t.Fatalf("TestVRAMExactThreshold: Reason want %q got %q", "ok", res.Reason)
		}
	} else {
		// Integer truncation pushed it below; must be rejected with threshold error.
		if res.Passed {
			t.Fatalf("TestVRAMExactThreshold: truncated ratio=%.6f < %.2f should fail, got Passed=true",
				actualRatio, vramThreshold)
		}
	}
}

// TestProofGateConsulted verifies that when the VRAM ratio passes (>= 0.95)
// but ProofValid=false, the result is Passed=false with ErrProofInvalid.
func TestProofGateConsulted(t *testing.T) {
	const physBytes = uint64(80 << 30)
	// ratio = 0.99 (above gate), proof invalid.
	g := makeGPU("gpu-bad-proof", physBytes, 0.99, false)

	res := Verify(g)

	t.Logf("TestProofGateConsulted GPUID=%s Passed=%v Ratio=%.4f Reason=%q",
		res.GPUID, res.Passed, res.VRAMRatio, res.Reason)

	if res.Passed {
		t.Fatalf("TestProofGateConsulted: ProofValid=false must yield Passed=false, got Passed=true")
	}
	if !strings.Contains(res.Reason, ErrProofInvalid.Error()) {
		t.Fatalf("TestProofGateConsulted: Reason must contain %q, got %q", ErrProofInvalid.Error(), res.Reason)
	}
}

// TestZeroPhysicalBytes verifies that a GPU with VRAMPhysicalBytes==0 returns
// ErrZeroPhysical and Passed=false (divide-by-zero guard).
func TestZeroPhysicalBytes(t *testing.T) {
	g := GPUInfo{ID: "gpu-zero-physical", VRAMPhysicalBytes: 0, VRAMReportedBytes: 1 << 30, ProofValid: true}

	res := Verify(g)

	t.Logf("TestZeroPhysicalBytes GPUID=%s Passed=%v Reason=%q", res.GPUID, res.Passed, res.Reason)

	if res.Passed {
		t.Fatalf("TestZeroPhysicalBytes: zero physical bytes must yield Passed=false")
	}
	if !strings.Contains(res.Reason, ErrZeroPhysical.Error()) {
		t.Fatalf("TestZeroPhysicalBytes: Reason must contain %q, got %q", ErrZeroPhysical.Error(), res.Reason)
	}
}

// ---------------------------------------------------------------------------
// TestBatchPassRate
//
// MUTATION GUARD (dual):
//  1. If the >= 0.95 threshold branch is removed/changed so the low-VRAM GPU
//     passes the gate, the bad GPU would show Passed=true and PassRate would
//     reach 1.0, causing this test to FAIL.
//  2. If Passed is set to true for a GPU that should fail (threshold or proof),
//     the PassRate and per-GPU Passed assertions both FAIL.
// ---------------------------------------------------------------------------

// TestBatchPassRate runs BatchVerify over a heterogeneous set of GPUs and
// verifies:
//
//  1. All-pass case: 3 GPUs, all with ratio >= 0.95 and ProofValid=true →
//     PassRate == 1.0, all Results Passed=true.
//
//  2. Tampered case: cloning that batch but dropping one GPU's ratio to 0.80
//     → PassRate == 2/3 (≈ 0.667), tampered device is Passed=false with
//     ErrVRAMThreshold in Reason.
//
// Every Result is asserted to carry the correct runID (sink-side evidence that
// BatchVerify stamps the RunID field).
func TestBatchPassRate(t *testing.T) {
	const physBytes = uint64(80 << 30)

	// --- sub-test 1: all-pass batch ---
	t.Run("AllPass", func(t *testing.T) {
		gpus := []GPUInfo{
			makeGPU("gpu-a", physBytes, 0.97, true),
			makeGPU("gpu-b", physBytes, 0.99, true),
			makeGPU("gpu-c", physBytes, 0.96, true),
		}

		report := BatchVerify(runID, gpus)

		t.Logf("TestBatchPassRate/AllPass runID=%s PassRate=%.4f Results=%d",
			report.RunID, report.PassRate, len(report.Results))

		// Sink: RunID echoed on the Report.
		if report.RunID != runID {
			t.Fatalf("AllPass: report.RunID want %q got %q", runID, report.RunID)
		}
		// Sink: PassRate must be exactly 1.0.
		if report.PassRate != 1.0 {
			t.Fatalf("AllPass: PassRate want 1.0 got %.6f", report.PassRate)
		}
		// Sink: every Result must be Passed=true and carry runID.
		for _, res := range report.Results {
			t.Logf("  result GPUID=%s RunID=%s Passed=%v Reason=%q",
				res.GPUID, res.RunID, res.Passed, res.Reason)
			if res.RunID != runID {
				t.Errorf("AllPass: result GPUID=%s: RunID want %q got %q", res.GPUID, runID, res.RunID)
			}
			if !res.Passed {
				t.Errorf("AllPass [MUTATION GUARD]: result GPUID=%s should Passed=true, got false Reason=%q", res.GPUID, res.Reason)
			}
		}
	})

	// --- sub-test 2: one tampered GPU (VRAM ratio dropped to 0.80) ---
	t.Run("TamperedVRAM", func(t *testing.T) {
		gpus := []GPUInfo{
			makeGPU("gpu-a", physBytes, 0.97, true), // passes
			makeGPU("gpu-b", physBytes, 0.80, true), // tampered: ratio < 0.95 → must fail
			makeGPU("gpu-c", physBytes, 0.96, true), // passes
		}

		report := BatchVerify(runID, gpus)

		t.Logf("TestBatchPassRate/TamperedVRAM runID=%s PassRate=%.4f Results=%d",
			report.RunID, report.PassRate, len(report.Results))

		// Sink: RunID echoed on the Report.
		if report.RunID != runID {
			t.Fatalf("TamperedVRAM: report.RunID want %q got %q", runID, report.RunID)
		}

		// Sink: PassRate must be 2/3.
		wantPassRate := 2.0 / 3.0
		if report.PassRate != wantPassRate {
			t.Fatalf("TamperedVRAM [MUTATION GUARD]: PassRate want %.6f got %.6f (tampered GPU must reduce rate)",
				wantPassRate, report.PassRate)
		}

		// Sink: exactly 2 results Passed=true, 1 Passed=false.
		passedCount := 0
		for _, res := range report.Results {
			t.Logf("  result GPUID=%s RunID=%s Passed=%v Ratio=%.4f Reason=%q",
				res.GPUID, res.RunID, res.Passed, res.VRAMRatio, res.Reason)
			if res.RunID != runID {
				t.Errorf("TamperedVRAM: result GPUID=%s: RunID want %q got %q", res.GPUID, runID, res.RunID)
			}
			if res.Passed {
				passedCount++
			}
		}
		if passedCount != 2 {
			t.Fatalf("TamperedVRAM: expected 2 passed GPUs, got %d", passedCount)
		}

		// Sink: identify the tampered device (gpu-b) and verify it is Passed=false
		// with the threshold error — prove it was the VRAM gate that caught it.
		var tamperedResult *Result
		for i := range report.Results {
			if report.Results[i].GPUID == "gpu-b" {
				tamperedResult = &report.Results[i]
				break
			}
		}
		if tamperedResult == nil {
			t.Fatalf("TamperedVRAM: could not find result for gpu-b")
		}
		if tamperedResult.Passed {
			t.Fatalf("TamperedVRAM [MUTATION GUARD]: gpu-b (ratio=%.4f) must be Passed=false; got true — threshold gate not applied",
				tamperedResult.VRAMRatio)
		}
		if !strings.Contains(tamperedResult.Reason, ErrVRAMThreshold.Error()) {
			t.Fatalf("TamperedVRAM: gpu-b Reason must contain %q, got %q",
				ErrVRAMThreshold.Error(), tamperedResult.Reason)
		}
	})
}

// TestBatchEmptyInput verifies that an empty GPU slice returns a Report with
// PassRate==0.0, zero Results, and the runID echoed.
func TestBatchEmptyInput(t *testing.T) {
	report := BatchVerify(runID, nil)

	t.Logf("TestBatchEmptyInput runID=%s PassRate=%.4f Results=%d",
		report.RunID, report.PassRate, len(report.Results))

	if report.RunID != runID {
		t.Fatalf("TestBatchEmptyInput: RunID want %q got %q", runID, report.RunID)
	}
	if report.PassRate != 0.0 {
		t.Fatalf("TestBatchEmptyInput: PassRate want 0.0 got %.6f", report.PassRate)
	}
	if len(report.Results) != 0 {
		t.Fatalf("TestBatchEmptyInput: Results want 0 got %d", len(report.Results))
	}
}

// TestBatchRunIDStamped verifies that every Result in a multi-GPU batch carries
// the supplied runID, independent of pass/fail outcome.
func TestBatchRunIDStamped(t *testing.T) {
	const physBytes = uint64(40 << 30)
	gpus := []GPUInfo{
		makeGPU("gpu-1", physBytes, 0.98, true),  // pass
		makeGPU("gpu-2", physBytes, 0.90, false), // fail VRAM
		makeGPU("gpu-3", physBytes, 0.96, false), // fail proof
	}

	report := BatchVerify(runID, gpus)

	t.Logf("TestBatchRunIDStamped PassRate=%.4f", report.PassRate)

	for _, res := range report.Results {
		t.Logf("  GPUID=%s RunID=%s Passed=%v Reason=%q", res.GPUID, res.RunID, res.Passed, res.Reason)
		if res.RunID != runID {
			t.Errorf("GPUID=%s: RunID want %q got %q", res.GPUID, runID, res.RunID)
		}
	}

	// Exactly 1 should pass (gpu-1 only).
	if report.PassRate != 1.0/3.0 {
		t.Fatalf("TestBatchRunIDStamped: PassRate want %.6f got %.6f", 1.0/3.0, report.PassRate)
	}
}

// TestBatchOrderPreserved verifies that Results are returned in the same order
// as the input GPUs slice, important for callers correlating by index.
func TestBatchOrderPreserved(t *testing.T) {
	const physBytes = uint64(80 << 30)
	ids := []string{"first", "second", "third", "fourth", "fifth"}
	gpus := make([]GPUInfo, len(ids))
	for i, id := range ids {
		gpus[i] = makeGPU(id, physBytes, 0.97, true)
	}

	report := BatchVerify(runID, gpus)

	for i, res := range report.Results {
		if res.GPUID != ids[i] {
			t.Errorf("TestBatchOrderPreserved: index %d want GPUID=%q got %q", i, ids[i], res.GPUID)
		}
	}
}
