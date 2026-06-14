package gravalverify

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// ===========================================================================
// ADVERSARIAL ANTI-CHEAT SUITE (HXC GRAVAL hardening)
//
// GraVal is a GPU result/work-validation gate. A "forged result" is a worker
// claiming a genuine GPU computation it did not actually perform; in this model
// the real CUDA/Metal matrix-multiplication proof verdict is injected as
// GPUInfo.ProofValid (true = attested genuine, false = forged/failed proof).
// A second axis catches a worker LYING ABOUT HARDWARE: the VRAM-ratio gate
// rejects a device whose reported-to-physical VRAM ratio is below 0.95
// (VRAM-shrinkage fraud), short-circuiting before the proof gate.
//
// These tests prove the two anti-cheat properties hold ADVERSARIALLY:
//   - a forged proof is REJECTED (the core anti-cheat),
//   - a VRAM-fraud device is REJECTED,
//   - and the accept path requires BOTH gates to genuinely pass.
//
// ANTI-BLUFF: every rejection test is written so that DELETING the relevant
// check in Verify makes the test FAIL (the cheat would be accepted). The
// mutation that bites each test is named in its doc comment.
// ===========================================================================

// physH100 is a typical 80 GiB datacenter GPU physical VRAM size, used as the
// honest hardware baseline across the adversarial suite.
const physH100 = uint64(80 << 30)

// ---------------------------------------------------------------------------
// 1. GENUINE ACCEPT (baseline) — a correct device with a valid proof verifies.
// ---------------------------------------------------------------------------

// TestAdversarial_GenuineAccept is the positive baseline: an honest GPU that
// reports its true VRAM (ratio 1.0) AND carries a genuine proof (ProofValid
// =true) MUST be accepted (Passed=true, Reason="ok"). If this fails, the gate
// is rejecting genuine work — a false-reject denial-of-service for honest
// workers, the inverse anti-cheat failure.
func TestAdversarial_GenuineAccept(t *testing.T) {
	g := GPUInfo{
		ID:                "honest-gpu",
		VRAMPhysicalBytes: physH100,
		VRAMReportedBytes: physH100, // ratio exactly 1.0 — honest reporting
		ProofValid:        true,     // genuine compute proof
	}

	res := Verify(g)
	t.Logf("GenuineAccept GPUID=%s Passed=%v Ratio=%.6f Reason=%q",
		res.GPUID, res.Passed, res.VRAMRatio, res.Reason)

	if !res.Passed {
		t.Fatalf("GenuineAccept: honest GPU (ratio=1.0, ProofValid=true) MUST be accepted; got Passed=false Reason=%q", res.Reason)
	}
	if res.Reason != "ok" {
		t.Fatalf("GenuineAccept: Reason want \"ok\" got %q", res.Reason)
	}
	if res.VRAMRatio != 1.0 {
		t.Fatalf("GenuineAccept: VRAMRatio want 1.0 got %.6f", res.VRAMRatio)
	}
}

// ---------------------------------------------------------------------------
// 2. FORGED RESULT REJECTION (THE CORE ANTI-CHEAT)
// ---------------------------------------------------------------------------

// TestAdversarial_ForgedProofRejected is the CORE anti-cheat test. The device
// passes the VRAM gate cleanly (honest hardware, ratio 1.0) so the ONLY thing
// standing between it and acceptance is the proof gate. The worker submits a
// FORGED result — it did not actually run the GPU computation — modeled as
// ProofValid=false. Verify MUST reject it (Passed=false, ErrProofInvalid).
//
// ANTI-BLUFF / MUTATION THAT BITES: if the proof gate `if !g.ProofValid` in
// Verify were removed (so Verify always proceeds to Passed=true once the VRAM
// gate passes — "skip the proof comparison"), this test FAILS because the
// forged device would be accepted. That is the exact "forged result accepted"
// security bypass this suite exists to catch.
func TestAdversarial_ForgedProofRejected(t *testing.T) {
	forged := GPUInfo{
		ID:                "forger",
		VRAMPhysicalBytes: physH100,
		VRAMReportedBytes: physH100, // honest VRAM — passes stage 1 cleanly
		ProofValid:        false,    // FORGED compute result
	}

	res := Verify(forged)
	t.Logf("ForgedProofRejected GPUID=%s Passed=%v Ratio=%.6f Reason=%q",
		res.GPUID, res.Passed, res.VRAMRatio, res.Reason)

	// The VRAM gate must have passed (so we know it's the proof gate doing the work).
	if res.VRAMRatio < vramThreshold {
		t.Fatalf("ForgedProofRejected: setup error — VRAM ratio %.4f below gate; cannot isolate proof gate", res.VRAMRatio)
	}
	if res.Passed {
		t.Fatalf("ForgedProofRejected [SECURITY/MUTATION GUARD]: a FORGED result (ProofValid=false) past the VRAM gate was ACCEPTED (Passed=true). This is a forged-result anti-cheat BYPASS. Kill-line: `if !g.ProofValid` in Verify.")
	}
	if !strings.Contains(res.Reason, ErrProofInvalid.Error()) {
		t.Fatalf("ForgedProofRejected: Reason must contain %q (proof gate must name the failure), got %q", ErrProofInvalid.Error(), res.Reason)
	}
}

// TestAdversarial_OverReportVRAMStillNeedsProof pins the documented contract
// that the VRAM gate is a LOWER bound (it catches VRAM shrinkage / under-
// reporting), and that a worker who LIES UPWARD about hardware (reports 2x its
// physical VRAM, ratio 2.0) is NOT auto-accepted: the proof gate still governs.
//
// Two devices, both over-reporting VRAM 2x:
//   - with a forged proof  -> REJECTED (proof gate catches the fraud),
//   - with a genuine proof -> accepted (over-claiming hardware is, per the
//     documented model, only meaningful if the worker can also pass the real
//     compute proof; the VRAM gate is not the anti-cheat for over-reporting).
//
// ANTI-BLUFF / MUTATION THAT BITES: the over-report+forged case FAILS if the
// proof gate is removed — proving the proof gate, not the VRAM gate, is what
// stops a hardware-over-claiming forger. This documents that the VRAM gate's
// one-sided `>=` is intentional and is backstopped by the proof gate.
func TestAdversarial_OverReportVRAMStillNeedsProof(t *testing.T) {
	overReportForged := GPUInfo{
		ID:                "overclaim-forger",
		VRAMPhysicalBytes: 40 << 30, // physically 40 GiB
		VRAMReportedBytes: 80 << 30, // claims 80 GiB -> ratio 2.0
		ProofValid:        false,    // forged result
	}
	res := Verify(overReportForged)
	t.Logf("OverReport+Forged GPUID=%s Passed=%v Ratio=%.4f Reason=%q",
		res.GPUID, res.Passed, res.VRAMRatio, res.Reason)

	if res.VRAMRatio < vramThreshold {
		t.Fatalf("OverReport+Forged: setup error — ratio %.4f unexpectedly below gate", res.VRAMRatio)
	}
	if res.Passed {
		t.Fatalf("OverReport+Forged [SECURITY/MUTATION GUARD]: a hardware-over-claiming worker with a FORGED proof was ACCEPTED. Proof gate (`if !g.ProofValid`) must reject it regardless of VRAM over-reporting.")
	}
	if !strings.Contains(res.Reason, ErrProofInvalid.Error()) {
		t.Fatalf("OverReport+Forged: Reason must contain %q, got %q", ErrProofInvalid.Error(), res.Reason)
	}

	// Companion: same over-report but with a genuine proof is accepted, proving
	// the over-report case is governed solely by the proof gate (documented).
	overReportGenuine := overReportForged
	overReportGenuine.ID = "overclaim-genuine"
	overReportGenuine.ProofValid = true
	res2 := Verify(overReportGenuine)
	t.Logf("OverReport+Genuine GPUID=%s Passed=%v Ratio=%.4f Reason=%q",
		res2.GPUID, res2.Passed, res2.VRAMRatio, res2.Reason)
	if !res2.Passed {
		t.Fatalf("OverReport+Genuine: per documented model (VRAM gate is a lower bound), an over-reporting device WITH a genuine proof passes; got Passed=false Reason=%q", res2.Reason)
	}
}

// ---------------------------------------------------------------------------
// 3. PRECEDENCE / SHORT-CIRCUIT — VRAM fraud is caught BEFORE the proof gate,
//    and a forged proof CANNOT be masked by it.
// ---------------------------------------------------------------------------

// TestAdversarial_VRAMFraudShortCircuitsProof proves the documented fail-fast
// ordering: when a device BOTH lies about VRAM (ratio below 0.95) AND submits
// a forged proof, the VRAM gate must fire FIRST — Reason carries ErrVRAMThreshold,
// NOT ErrProofInvalid, and ProofValid is never consulted. This matters because
// a cheater must not be able to probe which gate caught them, and the cheaper
// VRAM check must short-circuit the expensive proof.
//
// ANTI-BLUFF / MUTATION THAT BITES: if the VRAM gate's early `return` were
// removed (falling through to the proof gate), Reason would become
// ErrProofInvalid and this test FAILS on the "must NOT contain ErrProofInvalid"
// assertion. If the VRAM comparison were inverted/removed, Passed would flip and
// the first assertion FAILS.
func TestAdversarial_VRAMFraudShortCircuitsProof(t *testing.T) {
	doubleCheat := GPUInfo{
		ID:                "double-cheat",
		VRAMPhysicalBytes: physH100,
		VRAMReportedBytes: uint64(float64(physH100) * 0.50), // ratio 0.50 — gross VRAM fraud
		ProofValid:        false,                            // also a forged proof
	}

	res := Verify(doubleCheat)
	t.Logf("VRAMFraudShortCircuit GPUID=%s Passed=%v Ratio=%.4f Reason=%q",
		res.GPUID, res.Passed, res.VRAMRatio, res.Reason)

	if res.Passed {
		t.Fatalf("VRAMFraudShortCircuit [MUTATION GUARD]: device with VRAM fraud (ratio 0.5) must be Passed=false")
	}
	if !strings.Contains(res.Reason, ErrVRAMThreshold.Error()) {
		t.Fatalf("VRAMFraudShortCircuit: Reason must contain VRAM threshold sentinel %q, got %q", ErrVRAMThreshold.Error(), res.Reason)
	}
	// The proof gate MUST NOT have been reached (short-circuit precedence).
	if strings.Contains(res.Reason, ErrProofInvalid.Error()) {
		t.Fatalf("VRAMFraudShortCircuit [MUTATION GUARD]: proof gate must be short-circuited by the VRAM gate; Reason leaked %q", res.Reason)
	}
}

// ---------------------------------------------------------------------------
// 4. THRESHOLD BOUNDARY — exact off-by-one around 0.95.
// ---------------------------------------------------------------------------

// TestAdversarial_ThresholdBoundaryExact pins the threshold comparison to the
// exact `>= 0.95` boundary using ratios constructed via float division (not the
// integer-truncating makeGPU helper), so we can assert clean ACCEPT-at and
// ABOVE, REJECT-just-below behavior — the off-by-one a cheater would probe.
//
// Cases (proof always genuine, so the VRAM gate is the sole decider):
//
//	ratio = 0.95 (exact)      -> ACCEPT  (>= boundary is inclusive)
//	ratio = 0.95000001        -> ACCEPT  (just above)
//	ratio = 0.94999999        -> REJECT  (just below — must NOT slip through)
//	ratio = 0.9499            -> REJECT
//
// ANTI-BLUFF / MUTATION THAT BITES:
//   - flipping `>=` to `>` makes the exact-0.95 ACCEPT case FAIL (boundary would reject).
//   - flipping `<` to `<=` (or lowering the constant) makes the just-below
//     REJECT cases FAIL (a sub-threshold VRAM-fraud device would be accepted).
func TestAdversarial_ThresholdBoundaryExact(t *testing.T) {
	// verifyAtRatio builds a GPU whose float ratio is as close to target as
	// float64 allows, with a genuine proof, and returns the Result.
	verifyAtRatio := func(id string, target float64) Result {
		// Choose physical so that reported = round(physical*target) reproduces
		// target with negligible error. A large physical base minimizes the
		// quantization error of the integer reported field.
		const phys = uint64(1) << 52 // large base for fine-grained ratios
		reported := uint64(math.Round(float64(phys) * target))
		return Verify(GPUInfo{
			ID:                id,
			VRAMPhysicalBytes: phys,
			VRAMReportedBytes: reported,
			ProofValid:        true,
		})
	}

	type tc struct {
		name       string
		target     float64
		wantPassed bool
	}
	cases := []tc{
		{"exact-0.95", 0.95, true},
		{"just-above", 0.95 + 1e-7, true},
		{"just-below", 0.95 - 1e-7, false},
		{"clearly-below-0.9499", 0.9499, false},
		{"clearly-above-0.9501", 0.9501, true},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			res := verifyAtRatio(c.name, c.target)
			t.Logf("boundary case=%s target=%.8f actualRatio=%.10f Passed=%v Reason=%q",
				c.name, c.target, res.VRAMRatio, res.Passed, res.Reason)

			// Guard: confirm the constructed ratio lands on the intended side of
			// the gate (defends against quantization flipping the case).
			ratioMeetsGate := res.VRAMRatio >= vramThreshold
			if ratioMeetsGate != c.wantPassed {
				t.Fatalf("boundary %s: constructed ratio %.10f lands on wrong side of gate %.2f (got meets=%v want %v) — test construction invalid",
					c.name, res.VRAMRatio, vramThreshold, ratioMeetsGate, c.wantPassed)
			}

			if res.Passed != c.wantPassed {
				t.Fatalf("boundary %s [MUTATION GUARD]: ratio=%.10f want Passed=%v got Passed=%v Reason=%q",
					c.name, res.VRAMRatio, c.wantPassed, res.Passed, res.Reason)
			}
			if !c.wantPassed && !strings.Contains(res.Reason, ErrVRAMThreshold.Error()) {
				t.Fatalf("boundary %s: rejected case must carry VRAM threshold sentinel, got %q", c.name, res.Reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 5. BATCH-LEVEL ANTI-CHEAT — one forger in a batch lowers the KPI and is the
//    one rejected; a forger cannot hide in an otherwise-honest batch.
// ---------------------------------------------------------------------------

// TestAdversarial_BatchForgerIsolated runs a mixed batch where exactly one GPU
// submits a forged proof (all hardware honest) and proves:
//   - PassRate is exactly (n-1)/n (the forger is excluded from the KPI),
//   - the forger's Result is Passed=false with ErrProofInvalid,
//   - every honest GPU is Passed=true,
//   - results stay index-aligned (a caller cannot be tricked about WHICH
//     device failed).
//
// ANTI-BLUFF / MUTATION THAT BITES: removing the proof gate makes the forger
// pass, PassRate becomes 1.0, and both the PassRate assertion and the
// forger-Passed=false assertion FAIL — the forged result would be silently
// accepted into the KPI.
func TestAdversarial_BatchForgerIsolated(t *testing.T) {
	const forgerIdx = 2
	n := 5
	gpus := make([]GPUInfo, n)
	for i := 0; i < n; i++ {
		gpus[i] = GPUInfo{
			ID:                fmt.Sprintf("gpu-%d", i),
			VRAMPhysicalBytes: physH100,
			VRAMReportedBytes: physH100, // all honest hardware
			ProofValid:        i != forgerIdx,
		}
	}

	report := BatchVerify(runID, gpus)
	t.Logf("BatchForgerIsolated runID=%s PassRate=%.6f Results=%d", report.RunID, report.PassRate, len(report.Results))

	wantRate := float64(n-1) / float64(n) // 4/5 = 0.8
	if report.PassRate != wantRate {
		t.Fatalf("BatchForgerIsolated [MUTATION GUARD]: PassRate want %.6f got %.6f — a forged result leaked into the KPI", wantRate, report.PassRate)
	}

	for i, res := range report.Results {
		t.Logf("  idx=%d GPUID=%s RunID=%s Passed=%v Reason=%q", i, res.GPUID, res.RunID, res.Passed, res.Reason)
		// Index alignment: result i must describe input i.
		if res.GPUID != gpus[i].ID {
			t.Fatalf("BatchForgerIsolated: result index %d misaligned: want %q got %q", i, gpus[i].ID, res.GPUID)
		}
		if res.RunID != runID {
			t.Errorf("BatchForgerIsolated: idx=%d RunID want %q got %q", i, runID, res.RunID)
		}
		if i == forgerIdx {
			if res.Passed {
				t.Fatalf("BatchForgerIsolated [SECURITY/MUTATION GUARD]: forger at idx %d was ACCEPTED — forged-result bypass", i)
			}
			if !strings.Contains(res.Reason, ErrProofInvalid.Error()) {
				t.Fatalf("BatchForgerIsolated: forger idx %d Reason must contain %q, got %q", i, ErrProofInvalid.Error(), res.Reason)
			}
		} else if !res.Passed {
			t.Fatalf("BatchForgerIsolated: honest GPU idx %d falsely rejected: Reason=%q", i, res.Reason)
		}
	}
}

// ---------------------------------------------------------------------------
// 6. EDGE CASES — empty / nil / malformed inputs reject cleanly, no panic.
// ---------------------------------------------------------------------------

// TestAdversarial_MalformedInputsNoBypassNoPanic feeds degenerate GPUInfo
// values (zero-value struct, zero physical bytes with a "valid" proof, zero
// reported bytes) and asserts none are accepted and none panic. A zero-value
// struct is the classic "empty/malformed response" — it must NOT verify OK.
//
// ANTI-BLUFF: the zero-value struct has ProofValid=false AND VRAMPhysicalBytes=0;
// it must be rejected by the zero-physical guard. The zero-reported case has a
// genuine proof but ratio 0.0 -> must be rejected by the VRAM gate. If either
// guard were removed, the corresponding sub-case Passed=false assertion FAILS.
func TestAdversarial_MalformedInputsNoBypassNoPanic(t *testing.T) {
	cases := []struct {
		name string
		g    GPUInfo
	}{
		{"zero-value-struct", GPUInfo{}},
		{"zero-physical-but-proof-true", GPUInfo{ID: "zp", VRAMPhysicalBytes: 0, VRAMReportedBytes: 1 << 30, ProofValid: true}},
		{"zero-reported-genuine-proof", GPUInfo{ID: "zr", VRAMPhysicalBytes: physH100, VRAMReportedBytes: 0, ProofValid: true}},
		{"both-zero", GPUInfo{ID: "bz", VRAMPhysicalBytes: 0, VRAMReportedBytes: 0, ProofValid: true}},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("MalformedInputs %s: Verify PANICKED on malformed input: %v", c.name, p)
				}
			}()

			res := Verify(c.g)
			t.Logf("malformed case=%s Passed=%v Ratio=%.4f Reason=%q", c.name, res.Passed, res.VRAMRatio, res.Reason)

			if res.Passed {
				t.Fatalf("MalformedInputs %s [SECURITY GUARD]: malformed/degenerate input was ACCEPTED (Passed=true) — must be rejected", c.name)
			}
			if res.Reason == "" || res.Reason == "ok" {
				t.Fatalf("MalformedInputs %s: rejected input must carry a non-ok failure reason, got %q", c.name, res.Reason)
			}
		})
	}

	// Empty batch must also not panic and yields PassRate 0.0 (no spurious pass).
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("MalformedInputs: BatchVerify panicked on nil slice: %v", p)
			}
		}()
		rep := BatchVerify(runID, nil)
		if rep.PassRate != 0.0 {
			t.Fatalf("MalformedInputs: empty batch PassRate want 0.0 got %.6f", rep.PassRate)
		}
	}()
}
