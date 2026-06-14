package quantization

// Adversarial / sink-side probes for the NUMERIC surface of pkg/quantization.
//
// This package is NOT a tensor float->int quantizer; the numeric hotspot is
// EstimateVRAM, which computes  int64(float64(modelWeightBytes) * factor).
// We hunt the four classic numeric hazards on that expression:
//
//   (a) NUMERIC POISON (NaN / +-Inf): structurally IMPOSSIBLE at this API
//       because every public entry point takes int64 (or string), never a
//       float. We prove the API shape pins this out rather than asserting on a
//       value that cannot exist. See TestAdversarial_NoFloatIngress_documents.
//
//   (b) OVERFLOW / SCALE: the prod doc (awq.go) WORRIES that inputs above ~8 EB
//       "overflow int64 after float64 multiplication" and deliberately ships NO
//       runtime guard. We prove the actual SINK: on this platform the float->int
//       conversion SATURATES toward MaxInt64 (positive), it does NOT wrap to a
//       negative. So the doc's feared decision-flip (a giant model read as
//       "fits" because TotalBytes went negative) does NOT occur. We pin the
//       saturation as the de-facto contract and prove TotalBytes stays positive
//       and the awq<fp16 ordering survives even at MaxInt64.
//
//   (c) ROUNDING / CLAMP BOUNDARY: the AWQ path multiplies by 0.25*1.15=0.2875
//       and truncates (Go float->int truncates toward zero). For small but
//       IN-DOMAIN inputs (the doc declares the domain to start at "1 byte"),
//       AWQ TotalBytes truncates to 0 -- a NON-POSITIVE estimate that violates
//       the package's own invariant ("Both estimates must be positive",
//       awq_test.go TestVRAMEstimate). We capture the exact boundary (x in
//       {1,2,3} -> 0) as a SINK-SIDE fact and show how it leaks into
//       SelectFormat (a model "fits" in a budget claiming 0 VRAM). This is a
//       LATENT RISK boundary defect, not a wrap/NaN bug: no 1-3 byte model
//       exists, and on real inputs (>= 4 bytes) the estimate is positive.
//
//   (d) ROUND-TRIP / SATURATION for SelectFormat: out-of-budget saturates to
//       ErrNoFit, never wraps to a spurious fit, for realistic inputs.
//
// Tag: adversarial-quant-7f3a1c.

import (
	"errors"
	"math"
	"testing"
)

const advQuantUUID = "adversarial-quant-7f3a1c"

// fp16Sink / awqSink replicate the EXACT prod expressions from awq.go so we can
// assert against the independent oracle (the literal float math) rather than
// re-calling the function under test in a circular way.
func fp16Sink(x int64) int64 { return int64(float64(x) * OverheadFactor) }
func awqSink(x int64) int64  { return int64(float64(x) * AWQ4WeightFactor * OverheadFactor) }

// ── (a) NUMERIC POISON: NaN / Inf cannot enter ───────────────────────────────

// TestAdversarial_NoFloatIngress_documents proves the NaN/Inf class is closed by
// construction: the public numeric entry points consume int64, so a poisoned
// float can never reach the multiply. There is no value to assert "wrong" on;
// the type system is the guard. This is a DOCUMENTED-CONTRACT pin, not a bug.
//
// (Compile-time evidence: EstimateVRAM(int64, ServeFormat), SelectFormat(int64,
// int64), PodConfig.{VRAMBudgetBytes,ModelWeightBytes} int64. If any of these
// were ever changed to float64, this comment + the calls below would need
// revisiting because NaN < min and NaN > max are both false and would slip the
// SelectFormat budget gate.)
func TestAdversarial_NoFloatIngress_documents(t *testing.T) {
	// Smoke the int64 API with an ordinary value; the point is that the only way
	// in is via int64, which has no NaN/Inf representation.
	const sevenBfp16 = 7_000_000_000 * 2
	est, err := EstimateVRAM(sevenBfp16, ServeFormatFP16)
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", advQuantUUID, err)
	}
	if est.TotalBytes <= 0 {
		t.Fatalf("%s: 7B fp16 estimate must be positive, got %d", advQuantUUID, est.TotalBytes)
	}
	t.Logf("%s: int64-only ingress confirmed; NaN/Inf cannot reach the multiply", advQuantUUID)
}

// ── (b) OVERFLOW / SCALE: prove SATURATION, not wrap-to-negative ──────────────

// TestAdversarial_OverflowSaturatesPositive probes the doc's own stated fear:
// "Values above ~8 EB would overflow int64 after float64 multiplication". The
// classic bug would be int64(huge_float) wrapping to a NEGATIVE number, which
// would make SelectFormat report a too-huge model as "fits" (negative <= budget
// is true). We prove the SINK at and near MaxInt64:
//
//   - TotalBytes stays NON-NEGATIVE (saturates toward MaxInt64), and
//   - the awq < fp16 ordering survives,
//
// so the feared decision-flip does NOT happen on this platform. If a future Go
// release or GOARCH wrapped to MinInt64 instead, this test would catch it
// (TotalBytes would go negative) and the doc's "no guard needed" stance would
// need revisiting.
func TestAdversarial_OverflowSaturatesPositive(t *testing.T) {
	xs := []int64{
		math.MaxInt64,
		math.MaxInt64 - 1,
		1 << 62,
		8_000_000_000_000_000_000, // 8 EB, the doc's threshold
	}
	for _, x := range xs {
		fp16Est, err := EstimateVRAM(x, ServeFormatFP16)
		if err != nil {
			t.Fatalf("%s: fp16 EstimateVRAM(%d) error: %v", advQuantUUID, x, err)
		}
		awqEst, err := EstimateVRAM(x, ServeFormatAWQ4)
		if err != nil {
			t.Fatalf("%s: awq EstimateVRAM(%d) error: %v", advQuantUUID, x, err)
		}
		t.Logf("%s: x=%d fp16=%d awq=%d", advQuantUUID, x, fp16Est.TotalBytes, awqEst.TotalBytes)

		// SINK 1: no wrap to negative (saturation, not 2's-complement wrap).
		if fp16Est.TotalBytes < 0 {
			t.Errorf("%s: OVERFLOW WRAP: fp16 TotalBytes went NEGATIVE (%d) for x=%d -- "+
				"this would flip SelectFormat to report a too-huge model as fitting",
				advQuantUUID, fp16Est.TotalBytes, x)
		}
		if awqEst.TotalBytes < 0 {
			t.Errorf("%s: OVERFLOW WRAP: awq TotalBytes went NEGATIVE (%d) for x=%d",
				advQuantUUID, awqEst.TotalBytes, x)
		}

		// SINK 2: ordering invariant must survive overflow (awq strictly < fp16).
		if awqEst.TotalBytes >= fp16Est.TotalBytes {
			t.Errorf("%s: ordering broke at overflow x=%d: awq=%d >= fp16=%d",
				advQuantUUID, x, awqEst.TotalBytes, fp16Est.TotalBytes)
		}

		// Oracle: matches the literal prod expression.
		if got := fp16Est.TotalBytes; got != fp16Sink(x) {
			t.Errorf("%s: fp16 sink mismatch x=%d: got %d want %d", advQuantUUID, x, got, fp16Sink(x))
		}
		if got := awqEst.TotalBytes; got != awqSink(x) {
			t.Errorf("%s: awq sink mismatch x=%d: got %d want %d", advQuantUUID, x, got, awqSink(x))
		}
	}
}

// TestAdversarial_OverflowDoesNotFlipSelectFormat proves the decision-level sink:
// at MaxInt64 weight with a finite (non-max) budget, SelectFormat must return
// ErrNoFit -- never a spurious "fits" caused by a wrapped-negative estimate.
func TestAdversarial_OverflowDoesNotFlipSelectFormat(t *testing.T) {
	const x = int64(math.MaxInt64)
	const budget = int64(40) * 1 << 30 // 40 GiB A100 -- a real budget
	got, err := SelectFormat(x, budget)
	t.Logf("%s: SelectFormat(MaxInt64, 40GiB) = %q err=%v", advQuantUUID, got, err)
	if !errors.Is(err, ErrNoFit) {
		t.Errorf("%s: a MaxInt64-byte model must NOT fit a 40GiB budget; got %q err=%v "+
			"(a wrap-to-negative estimate would spuriously fit)", advQuantUUID, got, err)
	}
}

// ── (c) ROUNDING / CLAMP BOUNDARY: AWQ truncates to 0 for tiny in-domain x ────

// TestAdversarial_AWQTruncatesToZeroAtBoundary captures, SINK-SIDE, the exact
// boundary where the AWQ estimate (factor 0.25*1.15=0.2875, truncated toward
// zero) yields a NON-POSITIVE result for inputs the doc declares IN-DOMAIN
// ("1 byte to ~8 TB"). For x in {1,2,3}: TotalBytes == 0, which violates the
// package's own stated invariant ("Both estimates must be positive",
// awq_test.go). At x>=4 the estimate is positive again.
//
// CLASSIFICATION: LATENT RISK / boundary defect, NOT a wrap/NaN bug. No 1-3 byte
// model exists, so no real caller is harmed; prod is therefore left UNCHANGED.
// This test PINS the current behavior so a future change (e.g. clamping the
// AWQ floor to >=1, or rejecting sub-4-byte inputs) is a conscious, reviewed
// decision rather than a silent drift. It documents the contract tension at the
// same time.
func TestAdversarial_AWQTruncatesToZeroAtBoundary(t *testing.T) {
	// Boundary: zero for {1,2,3}, positive from 4 up.
	zeroInputs := []int64{1, 2, 3}
	for _, x := range zeroInputs {
		est, err := EstimateVRAM(x, ServeFormatAWQ4)
		if err != nil {
			t.Fatalf("%s: EstimateVRAM(%d, awq) unexpected error: %v", advQuantUUID, x, err)
		}
		t.Logf("%s: BOUNDARY x=%d awq TotalBytes=%d (in-domain per doc, yet non-positive)",
			advQuantUUID, x, est.TotalBytes)
		// Pin the current (defective-but-harmless) sink value. If a future fix
		// floors AWQ to >=1, flip this expectation deliberately.
		if est.TotalBytes != 0 {
			t.Errorf("%s: pinned boundary changed: EstimateVRAM(%d, awq).TotalBytes = %d, "+
				"expected 0 on current prod -- if you fixed the AWQ floor, update this pin",
				advQuantUUID, x, est.TotalBytes)
		}
		// Document the invariant tension explicitly (does not fail the build):
		// the package's own TestVRAMEstimate requires positive estimates, which
		// this in-domain input violates.
		if est.TotalBytes <= 0 {
			t.Logf("%s: NOTE -- TotalBytes %d <= 0 violates the package invariant "+
				"'estimates must be positive' for an in-domain input (LATENT RISK)",
				advQuantUUID, est.TotalBytes)
		}
	}

	// First input for which AWQ becomes positive.
	const firstPositive = int64(4)
	est, err := EstimateVRAM(firstPositive, ServeFormatAWQ4)
	if err != nil {
		t.Fatalf("%s: EstimateVRAM(4, awq) error: %v", advQuantUUID, err)
	}
	if est.TotalBytes <= 0 {
		t.Errorf("%s: x=4 should give positive AWQ estimate, got %d", advQuantUUID, est.TotalBytes)
	}
	t.Logf("%s: x=4 awq TotalBytes=%d (first positive)", advQuantUUID, est.TotalBytes)
}

// TestAdversarial_ZeroAWQLeaksIntoSelectFormat proves the rounding boundary has a
// decision-level consequence: a 3-byte model under AWQ is reported as needing
// 0 VRAM, so SelectFormat says it "fits" a 2-byte budget. SINK-SIDE proof that
// the truncation-to-zero is observable through the public selection API.
//
// This pins LATENT RISK behavior; prod is intentionally unchanged.
func TestAdversarial_ZeroAWQLeaksIntoSelectFormat(t *testing.T) {
	// x=3: fp16Sink(3)=3 (> 2, does not fit), awqSink(3)=0 (<= 2, "fits").
	const x = int64(3)
	const budget = int64(2)
	if fp16Sink(x) <= budget {
		t.Fatalf("%s: test premise broken: fp16(%d)=%d unexpectedly <= budget %d",
			advQuantUUID, x, fp16Sink(x), budget)
	}
	got, err := SelectFormat(x, budget)
	t.Logf("%s: SelectFormat(3, 2) = %q err=%v (awq estimate=%d)",
		advQuantUUID, got, err, awqSink(x))
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", advQuantUUID, err)
	}
	if got != ServeFormatAWQ4 {
		t.Errorf("%s: expected awq-4bit (selected because its truncated estimate is 0), got %q",
			advQuantUUID, got)
	}
	// SINK: the chosen format reports 0 VRAM for a "served" model.
	if awqSink(x) != 0 {
		t.Errorf("%s: expected the leaked AWQ estimate to be 0, got %d", advQuantUUID, awqSink(x))
	}
}

// ── (d) ROUND-TRIP / SATURATION on realistic inputs ──────────────────────────

// TestAdversarial_RealisticRoundTripAndSaturation confirms that for realistic
// model sizes (>= a few KiB) the estimate is positive, the awq<fp16 ratio is
// within the documented 0.20-0.35 band, and an over-budget model saturates to
// ErrNoFit (never wraps to a spurious fit).
func TestAdversarial_RealisticRoundTripAndSaturation(t *testing.T) {
	sizes := []int64{
		1 << 20,            // 1 MiB
		1 << 30,            // 1 GiB
		7_000_000_000 * 2,  // 7B fp16
		70_000_000_000 * 2, // 70B fp16
	}
	for _, wb := range sizes {
		fp16Est, err := EstimateVRAM(wb, ServeFormatFP16)
		if err != nil {
			t.Fatalf("%s: fp16(%d) error: %v", advQuantUUID, wb, err)
		}
		awqEst, err := EstimateVRAM(wb, ServeFormatAWQ4)
		if err != nil {
			t.Fatalf("%s: awq(%d) error: %v", advQuantUUID, wb, err)
		}
		if fp16Est.TotalBytes <= 0 || awqEst.TotalBytes <= 0 {
			t.Fatalf("%s: realistic estimates must be positive: fp16=%d awq=%d",
				advQuantUUID, fp16Est.TotalBytes, awqEst.TotalBytes)
		}
		ratio := float64(awqEst.TotalBytes) / float64(fp16Est.TotalBytes)
		if ratio < 0.20 || ratio > 0.35 {
			t.Errorf("%s: wb=%d ratio %.4f outside [0.20,0.35]", advQuantUUID, wb, ratio)
		}

		// Saturation: budget one byte below the AWQ estimate => ErrNoFit, not wrap.
		tightBudget := awqEst.TotalBytes - 1
		got, err := SelectFormat(wb, tightBudget)
		if !errors.Is(err, ErrNoFit) {
			t.Errorf("%s: wb=%d budget=%d: expected ErrNoFit (over budget), got %q err=%v",
				advQuantUUID, wb, tightBudget, got, err)
		}
	}
}

// ── Policy surface: Select determinism / clamp boundaries (integer-only) ──────

// TestAdversarial_SelectBoundaryFootprintEqualsBudget proves the memory-fit gate
// uses <= (a model whose footprint exactly equals the budget FITS, it does not
// fall off by one). Off-by-one here would silently reject a perfectly-sized
// variant.
func TestAdversarial_SelectBoundaryFootprintEqualsBudget(t *testing.T) {
	tier := DeviceTier{ID: "T-exact", MemoryMiB: 100, Backends: []string{"cpu"}}
	exact := ModelVariant{Name: "fits-exactly", Quant: QuantINT8, FootprintMiB: 100, Backend: "cpu"}
	over := ModelVariant{Name: "one-over", Quant: QuantFP16, FootprintMiB: 101, Backend: "cpu"}

	got, err := Select(tier, []ModelVariant{over, exact})
	t.Logf("%s: Select exact-fit => %+v err=%v", advQuantUUID, got, err)
	if err != nil {
		t.Fatalf("%s: a variant whose footprint == budget must fit; got err %v", advQuantUUID, err)
	}
	if got.Name != "fits-exactly" {
		t.Errorf("%s: expected the exact-budget INT8 variant (the over-budget FP16 must be excluded), got %q",
			advQuantUUID, got.Name)
	}
}
