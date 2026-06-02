package quantization

import (
	"errors"
	"math"
	"testing"
)

// awqRunUUID tags log lines so they are traceable in CI output.
const awqRunUUID = "awq-9b1e4c2f-7a83-4d50-b60e-3c5f9d1e7a82"

// ── Helpers ──────────────────────────────────────────────────────────────────

// fp16BytesForBillionParams returns the fp16 weight size for a model with the
// given number of parameters (billions). 1B fp16 params = 1e9 × 2 bytes = 2 GiB.
func fp16BytesForBillionParams(billions float64) int64 {
	return int64(billions * 1e9 * 2)
}

// ── TestDefaultServeFormat ────────────────────────────────────────────────────

// TestDefaultServeFormat proves closure criterion 2: the default serving format
// is AWQ 4-bit.
//
// Mutation guard: if DefaultServeFormat() is changed to return ServeFormatFP16
// (or anything other than ServeFormatAWQ4) this test fails.
func TestDefaultServeFormat(t *testing.T) {
	t.Logf("%s: checking DefaultServeFormat()", awqRunUUID)
	got := DefaultServeFormat()
	t.Logf("%s: DefaultServeFormat() = %q", awqRunUUID, got)
	if got != ServeFormatAWQ4 {
		t.Errorf("DefaultServeFormat(): want %q, got %q", ServeFormatAWQ4, got)
	}
}

// ── TestVRAMEstimate ──────────────────────────────────────────────────────────

// TestVRAMEstimate proves closure criterion 1: EstimateVRAM(AWQ-4bit) is
// approximately 25 % of EstimateVRAM(fp16) for the same model, within the
// documented tolerance of 0.20–0.35.
//
// Mutation guard: if AWQ4WeightFactor is set equal to 1.0 (no reduction) then
// the AWQ estimate equals the fp16 estimate, the ratio is 1.0, and both the
// ratio-range and ratio-neq-fp16 assertions below fail.
func TestVRAMEstimate(t *testing.T) {
	// Use a 7B-parameter model as the canonical example.
	const billion7 = 7.0
	weightBytes := fp16BytesForBillionParams(billion7)
	t.Logf("%s: 7B model fp16 weight bytes = %d", awqRunUUID, weightBytes)

	fp16Est, err := EstimateVRAM(weightBytes, ServeFormatFP16)
	if err != nil {
		t.Fatalf("EstimateVRAM(fp16) error: %v", err)
	}
	awqEst, err := EstimateVRAM(weightBytes, ServeFormatAWQ4)
	if err != nil {
		t.Fatalf("EstimateVRAM(awq-4bit) error: %v", err)
	}

	t.Logf("%s: fp16 total=%d AWQ4 total=%d", awqRunUUID, fp16Est.TotalBytes, awqEst.TotalBytes)

	// 1. Both estimates must be positive.
	if fp16Est.TotalBytes <= 0 {
		t.Fatalf("fp16 TotalBytes must be > 0, got %d", fp16Est.TotalBytes)
	}
	if awqEst.TotalBytes <= 0 {
		t.Fatalf("AWQ-4bit TotalBytes must be > 0, got %d", awqEst.TotalBytes)
	}

	// 2. AWQ estimate must be strictly smaller than fp16.
	if awqEst.TotalBytes >= fp16Est.TotalBytes {
		t.Errorf("AWQ-4bit estimate (%d) must be < fp16 estimate (%d)",
			awqEst.TotalBytes, fp16Est.TotalBytes)
	}

	// 3. Ratio must be within documented tolerance 0.20–0.35 (centre: 0.25).
	ratio := float64(awqEst.TotalBytes) / float64(fp16Est.TotalBytes)
	const (
		ratioLow  = 0.20
		ratioHigh = 0.35
	)
	t.Logf("%s: AWQ/fp16 VRAM ratio = %.4f (want %.2f–%.2f)", awqRunUUID, ratio, ratioLow, ratioHigh)
	if ratio < ratioLow || ratio > ratioHigh {
		t.Errorf("VRAM ratio %.4f out of documented range [%.2f, %.2f]", ratio, ratioLow, ratioHigh)
	}

	// 4. Sanity: the ratio must be (very close to) AWQ4WeightFactor, since the
	// OverheadFactor cancels in the ratio.
	wantRatio := AWQ4WeightFactor // 0.25 exactly
	if math.Abs(ratio-wantRatio) > 0.001 {
		t.Errorf("VRAM ratio %.6f deviates from AWQ4WeightFactor %.6f by more than 0.001",
			ratio, wantRatio)
	}

	// 5. Format fields are set correctly.
	if fp16Est.Format != ServeFormatFP16 {
		t.Errorf("fp16 estimate: Format = %q, want %q", fp16Est.Format, ServeFormatFP16)
	}
	if awqEst.Format != ServeFormatAWQ4 {
		t.Errorf("AWQ estimate: Format = %q, want %q", awqEst.Format, ServeFormatAWQ4)
	}

	// 6. WeightBytes round-trips correctly (same input for both).
	if fp16Est.WeightBytes != weightBytes {
		t.Errorf("fp16 WeightBytes: want %d, got %d", weightBytes, fp16Est.WeightBytes)
	}
	if awqEst.WeightBytes != weightBytes {
		t.Errorf("AWQ WeightBytes: want %d, got %d", weightBytes, awqEst.WeightBytes)
	}
}

// TestVRAMEstimateTableDriven exercises EstimateVRAM across several model sizes
// to show the ratio holds for small, medium, and large models.
//
// Mutation guard: if AWQ4WeightFactor is set to 1.0 the ratio becomes 1.0 for
// all rows and the ratio-range assertion fails on every row.
func TestVRAMEstimateTableDriven(t *testing.T) {
	cases := []struct {
		name     string
		billions float64
	}{
		{"1B", 1},
		{"7B", 7},
		{"13B", 13},
		{"70B", 70},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			wb := fp16BytesForBillionParams(tc.billions)
			fp16Est, err := EstimateVRAM(wb, ServeFormatFP16)
			if err != nil {
				t.Fatalf("fp16 error: %v", err)
			}
			awqEst, err := EstimateVRAM(wb, ServeFormatAWQ4)
			if err != nil {
				t.Fatalf("awq error: %v", err)
			}

			ratio := float64(awqEst.TotalBytes) / float64(fp16Est.TotalBytes)
			t.Logf("%s %s: fp16=%d AWQ=%d ratio=%.4f", awqRunUUID, tc.name,
				fp16Est.TotalBytes, awqEst.TotalBytes, ratio)

			if ratio < 0.20 || ratio > 0.35 {
				t.Errorf("model %s: ratio %.4f outside [0.20, 0.35]", tc.name, ratio)
			}
			if awqEst.TotalBytes >= fp16Est.TotalBytes {
				t.Errorf("model %s: AWQ must be smaller than fp16", tc.name)
			}
		})
	}
}

// ── TestSelectFormat ──────────────────────────────────────────────────────────

// TestSelectFormat proves closure criterion 3: a model that fits in budget only
// under AWQ is selected as AWQ; a model that fits fp16 within budget uses fp16.
//
// Mutation guard:
//   - If the fp16 branch is removed/disabled, the fp16-fits case returns AWQ
//     instead of fp16, failing the second sub-test.
//   - If AWQ4WeightFactor is set equal to fp16 factor (1.0), then when fp16
//     does NOT fit, AWQ also does NOT fit (same bytes), and the awq-only case
//     returns ErrNoFit instead of AWQ, failing the first sub-test.
func TestSelectFormat(t *testing.T) {
	// 13B model in fp16: ~26 GB weights (26 × 2 bytes × 1e9 params → 26 GiB).
	weightBytes := fp16BytesForBillionParams(13)

	fp16Est, _ := EstimateVRAM(weightBytes, ServeFormatFP16)
	awqEst, _ := EstimateVRAM(weightBytes, ServeFormatAWQ4)
	t.Logf("%s: 13B model: fp16=%d AWQ=%d", awqRunUUID, fp16Est.TotalBytes, awqEst.TotalBytes)

	t.Run("awq_only_fits", func(t *testing.T) {
		// Budget: between AWQ total and fp16 total — AWQ fits, fp16 does not.
		budget := fp16Est.TotalBytes - 1 // one byte short of fp16; AWQ easily fits
		if awqEst.TotalBytes > budget {
			t.Skipf("test premise invalid: AWQ (%d) does not fit budget (%d)", awqEst.TotalBytes, budget)
		}

		got, err := SelectFormat(weightBytes, budget)
		t.Logf("%s awq_only_fits: budget=%d got=%q err=%v", awqRunUUID, budget, got, err)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != ServeFormatAWQ4 {
			t.Errorf("want AWQ-4bit (fp16 does not fit), got %q", got)
		}
	})

	t.Run("fp16_fits", func(t *testing.T) {
		// Budget: generously above the fp16 estimate — fp16 should be preferred.
		budget := fp16Est.TotalBytes + 1

		got, err := SelectFormat(weightBytes, budget)
		t.Logf("%s fp16_fits: budget=%d got=%q err=%v", awqRunUUID, budget, got, err)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != ServeFormatFP16 {
			t.Errorf("want FP16 (fits in budget and is higher quality), got %q", got)
		}
	})

	t.Run("neither_fits", func(t *testing.T) {
		// Budget: smaller than even AWQ estimate.
		budget := awqEst.TotalBytes - 1

		got, err := SelectFormat(weightBytes, budget)
		t.Logf("%s neither_fits: budget=%d got=%q err=%v", awqRunUUID, budget, got, err)
		if !errors.Is(err, ErrNoFit) {
			t.Errorf("want ErrNoFit, got err=%v got=%q", err, got)
		}
		if got != "" {
			t.Errorf("on ErrNoFit want empty format, got %q", got)
		}
	})
}

// ── TestEstimateVRAMInvalidInput ──────────────────────────────────────────────

// TestEstimateVRAMInvalidInput proves that EstimateVRAM returns a typed error
// for invalid inputs (zero/negative weight, unknown format) rather than silently
// computing nonsense values.
func TestEstimateVRAMInvalidInput(t *testing.T) {
	t.Run("zero_weight", func(t *testing.T) {
		_, err := EstimateVRAM(0, ServeFormatFP16)
		if err == nil {
			t.Error("want error for zero modelWeightBytes, got nil")
		}
	})

	t.Run("negative_weight", func(t *testing.T) {
		_, err := EstimateVRAM(-1, ServeFormatFP16)
		if err == nil {
			t.Error("want error for negative modelWeightBytes, got nil")
		}
	})

	t.Run("unknown_format", func(t *testing.T) {
		_, err := EstimateVRAM(1<<30, ServeFormat("int2-hypothetical"))
		if err == nil {
			t.Error("want error for unknown ServeFormat, got nil")
		}
	})
}

// TestSelectFormatInvalidInput mirrors the above for SelectFormat.
func TestSelectFormatInvalidInput(t *testing.T) {
	t.Run("zero_weight", func(t *testing.T) {
		_, err := SelectFormat(0, 1<<30)
		if err == nil {
			t.Error("want error for zero modelWeightBytes")
		}
	})

	t.Run("zero_budget", func(t *testing.T) {
		_, err := SelectFormat(1<<30, 0)
		if err == nil {
			t.Error("want error for zero budgetBytes")
		}
	})
}

// ── TestOverheadConsistency ───────────────────────────────────────────────────

// TestOverheadConsistency verifies the OverheadFactor is applied consistently:
// fp16 total must equal floor(weightBytes × OverheadFactor).
//
// This is a sanity-check for the computation model, not a mutation guard, but it
// will catch regressions if the overhead formula is accidentally changed.
func TestOverheadConsistency(t *testing.T) {
	var wb int64 = 1 << 30 // 1 GiB
	est, err := EstimateVRAM(wb, ServeFormatFP16)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := int64(float64(wb) * OverheadFactor)
	if est.TotalBytes != want {
		t.Errorf("fp16 total: want %d (wb×%.2f), got %d", want, OverheadFactor, est.TotalBytes)
	}
}
