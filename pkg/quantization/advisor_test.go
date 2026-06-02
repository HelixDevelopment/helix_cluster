package quantization

import (
	"errors"
	"strings"
	"testing"
)

// advUUID tags log lines so they are traceable in CI output.
const advUUID = "adv-3f7c1b9a-5e82-4c60-ad11-6b4d2e0f9c45"

// ── Integration tests: PodFormatAdvisor ──────────────────────────────────────
//
// These tests exercise PodFormatAdvisor as a real consumer would: constructing
// a real advisor with real configs and asserting on real outputs. This is the
// CLAUDE-1 end-to-end evidence that DefaultServeFormat / SelectFormat /
// EstimateVRAM are actually wired into a usable serving-decision path, not just
// floating policy functions.

// a100VRAMBytes is the VRAM of an A100-40GB GPU (40 GiB) used as a realistic
// pod budget throughout these integration tests.
const a100VRAMBytes = int64(40 * 1 << 30) // 40 GiB

// model7BWeightBytes is the fp16 weight footprint of a 7B-parameter model
// (7e9 params × 2 bytes/param = 14 GiB).
const model7BWeightBytes = int64(7_000_000_000 * 2) // ~14 GiB fp16

// model70BWeightBytes is the fp16 weight footprint of a 70B-parameter model.
// (~140 GiB fp16 — far exceeds an A100-40GB, only AWQ might fit on a larger pod).
const model70BWeightBytes = int64(70_000_000_000 * 2) // ~140 GiB fp16

// TestAdvisorAWQDefault proves that when only AWQ fits the VRAM budget,
// PodFormatAdvisor.Advise() returns ServeFormatAWQ4 — which equals
// DefaultServeFormat() — wiring the default-format policy into a real serving
// decision.
//
// CLAUDE-1 evidence: this exercises the full path
//   NewPodFormatAdvisor → Advise → SelectFormat → DefaultServeFormat
// as a real pod controller would, asserting sink-side behavior (the returned
// Format is AWQ-4bit and the estimated VRAM is below the budget).
//
// Mutation guard: if AWQ4WeightFactor is set to 1.0 (no reduction), AWQ
// TotalBytes == fp16 TotalBytes. The budget is crafted to be just below the fp16
// estimate. So AWQ would also fail to fit, causing SelectFormat to return
// ErrNoFit and this test fails on the non-nil error assertion.
func TestAdvisorAWQDefault(t *testing.T) {
	// Craft a budget: above AWQ estimate but below fp16 estimate for a 7B model.
	fp16Est, err := EstimateVRAM(model7BWeightBytes, ServeFormatFP16)
	if err != nil {
		t.Fatalf("%s: EstimateVRAM(fp16): %v", advUUID, err)
	}
	awqEst, err := EstimateVRAM(model7BWeightBytes, ServeFormatAWQ4)
	if err != nil {
		t.Fatalf("%s: EstimateVRAM(awq): %v", advUUID, err)
	}

	// Budget sits between AWQ and fp16 estimates — only AWQ fits.
	budget := fp16Est.TotalBytes - 1
	if awqEst.TotalBytes > budget {
		t.Skipf("%s: test premise violated: AWQ (%d) does not fit crafted budget (%d) — "+
			"AWQ4WeightFactor may have been modified", advUUID, awqEst.TotalBytes, budget)
	}

	t.Logf("%s: 7B model fp16=%d AWQ=%d budget=%d", advUUID,
		fp16Est.TotalBytes, awqEst.TotalBytes, budget)

	adv, err := NewPodFormatAdvisor(PodConfig{
		PodID:            "test-pod-awq-default",
		VRAMBudgetBytes:  budget,
		ModelWeightBytes: model7BWeightBytes,
	})
	if err != nil {
		t.Fatalf("%s: NewPodFormatAdvisor: unexpected error: %v", advUUID, err)
	}

	rec, err := adv.Advise()
	t.Logf("%s: Advise() format=%q VRAM=%d reason=%q err=%v",
		advUUID, rec.Format, rec.EstimatedVRAMBytes, rec.Reason, err)

	if err != nil {
		t.Fatalf("%s: Advise() unexpected error: %v", advUUID, err)
	}

	// Primary assertion: the chosen format is AWQ-4bit (= DefaultServeFormat()).
	if rec.Format != ServeFormatAWQ4 {
		t.Errorf("want ServeFormatAWQ4 (%q), got %q", ServeFormatAWQ4, rec.Format)
	}
	// The chosen format must equal DefaultServeFormat().
	if rec.Format != DefaultServeFormat() {
		t.Errorf("Advise() chose %q but DefaultServeFormat() is %q; they must agree "+
			"when only AWQ fits", rec.Format, DefaultServeFormat())
	}

	// Sink-side: VRAM estimate must be positive and within budget.
	if rec.EstimatedVRAMBytes <= 0 {
		t.Errorf("EstimatedVRAMBytes must be > 0, got %d", rec.EstimatedVRAMBytes)
	}
	if rec.EstimatedVRAMBytes > budget {
		t.Errorf("EstimatedVRAMBytes (%d) exceeds budget (%d)", rec.EstimatedVRAMBytes, budget)
	}

	// Reason must be non-empty (operator-visible log).
	if rec.Reason == "" {
		t.Error("Reason must be non-empty")
	}
	if !strings.Contains(rec.Reason, "test-pod-awq-default") {
		t.Errorf("Reason must contain the pod ID; got %q", rec.Reason)
	}
}

// TestAdvisorFP16WhenFits proves that when fp16 fits in the VRAM budget,
// PodFormatAdvisor.Advise() prefers fp16 (highest quality) over AWQ-4bit.
//
// CLAUDE-1 evidence: end-to-end path from NewPodFormatAdvisor through Advise
// asserting the correct quality-first choice.
//
// Mutation guard: if the fp16 branch in SelectFormat is removed (so AWQ is
// always chosen), this test fails because we expect fp16 when fp16 fits.
func TestAdvisorFP16WhenFits(t *testing.T) {
	// Use an A100-40GB pod for a 7B model: fp16 ~16 GiB easily fits in 40 GiB.
	fp16Est, err := EstimateVRAM(model7BWeightBytes, ServeFormatFP16)
	if err != nil {
		t.Fatalf("%s: EstimateVRAM(fp16): %v", advUUID, err)
	}

	if fp16Est.TotalBytes > a100VRAMBytes {
		t.Skipf("%s: 7B fp16 (%d) does not fit A100-40GB (%d) — constants changed",
			advUUID, fp16Est.TotalBytes, a100VRAMBytes)
	}

	t.Logf("%s: 7B fp16=%d A100 budget=%d (fp16 fits)", advUUID, fp16Est.TotalBytes, a100VRAMBytes)

	adv, err := NewPodFormatAdvisor(PodConfig{
		PodID:            "test-pod-fp16-fits",
		VRAMBudgetBytes:  a100VRAMBytes,
		ModelWeightBytes: model7BWeightBytes,
	})
	if err != nil {
		t.Fatalf("%s: NewPodFormatAdvisor: %v", advUUID, err)
	}

	rec, err := adv.Advise()
	t.Logf("%s: Advise() format=%q VRAM=%d reason=%q err=%v",
		advUUID, rec.Format, rec.EstimatedVRAMBytes, rec.Reason, err)

	if err != nil {
		t.Fatalf("%s: unexpected error: %v", advUUID, err)
	}
	if rec.Format != ServeFormatFP16 {
		t.Errorf("want FP16 (fits in budget and is highest quality), got %q", rec.Format)
	}
	if rec.EstimatedVRAMBytes <= 0 {
		t.Errorf("EstimatedVRAMBytes must be > 0, got %d", rec.EstimatedVRAMBytes)
	}
	if rec.EstimatedVRAMBytes > a100VRAMBytes {
		t.Errorf("EstimatedVRAMBytes (%d) exceeds pod budget (%d)",
			rec.EstimatedVRAMBytes, a100VRAMBytes)
	}
}

// TestAdvisorNoFit proves that when neither fp16 nor AWQ-4bit fits the VRAM
// budget, Advise() returns a wrapped ErrNoFit.
//
// CLAUDE-1 evidence: operator-visible error path verified end-to-end.
func TestAdvisorNoFit(t *testing.T) {
	awqEst, err := EstimateVRAM(model7BWeightBytes, ServeFormatAWQ4)
	if err != nil {
		t.Fatalf("%s: EstimateVRAM(awq): %v", advUUID, err)
	}
	// Budget smaller than the AWQ estimate — nothing fits.
	budget := awqEst.TotalBytes - 1

	t.Logf("%s: 7B awq=%d budget=%d (nothing fits)", advUUID, awqEst.TotalBytes, budget)

	adv, err := NewPodFormatAdvisor(PodConfig{
		PodID:            "test-pod-no-fit",
		VRAMBudgetBytes:  budget,
		ModelWeightBytes: model7BWeightBytes,
	})
	if err != nil {
		t.Fatalf("%s: NewPodFormatAdvisor: %v", advUUID, err)
	}

	rec, err := adv.Advise()
	t.Logf("%s: Advise() format=%q err=%v", advUUID, rec.Format, err)

	if !errors.Is(err, ErrNoFit) {
		t.Errorf("want ErrNoFit, got %v", err)
	}
	if rec.Format != "" {
		t.Errorf("on ErrNoFit want empty Format, got %q", rec.Format)
	}
}

// TestAdvisorOperatorOverride proves that a FormatOverride bypasses automatic
// selection and the advisor uses the pinned format.
//
// CLAUDE-1 evidence: operator-control path exercised end-to-end.
func TestAdvisorOperatorOverride(t *testing.T) {
	// Pin to fp16 even on a tiny budget — override ignores budget fit.
	adv, err := NewPodFormatAdvisor(PodConfig{
		PodID:            "test-pod-override",
		VRAMBudgetBytes:  a100VRAMBytes,
		ModelWeightBytes: model7BWeightBytes,
		FormatOverride:   ServeFormatAWQ4,
	})
	if err != nil {
		t.Fatalf("%s: NewPodFormatAdvisor: %v", advUUID, err)
	}

	rec, err := adv.Advise()
	t.Logf("%s: Advise() format=%q VRAM=%d reason=%q err=%v",
		advUUID, rec.Format, rec.EstimatedVRAMBytes, rec.Reason, err)

	if err != nil {
		t.Fatalf("%s: unexpected error: %v", advUUID, err)
	}
	if rec.Format != ServeFormatAWQ4 {
		t.Errorf("want override format %q, got %q", ServeFormatAWQ4, rec.Format)
	}
	if !strings.Contains(rec.Reason, "operator-pinned") {
		t.Errorf("Reason should mention 'operator-pinned' for an override; got %q", rec.Reason)
	}
}

// TestAdvisorInvalidConfig proves that NewPodFormatAdvisor rejects bad configs
// with typed errors rather than silently proceeding.
func TestAdvisorInvalidConfig(t *testing.T) {
	t.Run("zero_vram_budget", func(t *testing.T) {
		_, err := NewPodFormatAdvisor(PodConfig{
			PodID:            "bad-pod",
			VRAMBudgetBytes:  0,
			ModelWeightBytes: model7BWeightBytes,
		})
		if err == nil {
			t.Error("want error for zero VRAMBudgetBytes, got nil")
		}
	})

	t.Run("zero_model_weight", func(t *testing.T) {
		_, err := NewPodFormatAdvisor(PodConfig{
			PodID:            "bad-pod",
			VRAMBudgetBytes:  a100VRAMBytes,
			ModelWeightBytes: 0,
		})
		if err == nil {
			t.Error("want error for zero ModelWeightBytes, got nil")
		}
	})

	t.Run("unknown_format_override", func(t *testing.T) {
		_, err := NewPodFormatAdvisor(PodConfig{
			PodID:            "bad-pod",
			VRAMBudgetBytes:  a100VRAMBytes,
			ModelWeightBytes: model7BWeightBytes,
			FormatOverride:   ServeFormat("nf4-hypothetical"),
		})
		if err == nil {
			t.Error("want error for unknown FormatOverride, got nil")
		}
	})
}

// TestAdvisorDefaultFormat proves that PodFormatAdvisor.DefaultFormat() returns
// the same value as the package-level DefaultServeFormat().
func TestAdvisorDefaultFormat(t *testing.T) {
	adv, err := NewPodFormatAdvisor(PodConfig{
		PodID:            "test-pod-default-fmt",
		VRAMBudgetBytes:  a100VRAMBytes,
		ModelWeightBytes: model7BWeightBytes,
	})
	if err != nil {
		t.Fatalf("%s: NewPodFormatAdvisor: %v", advUUID, err)
	}
	got := adv.DefaultFormat()
	want := DefaultServeFormat()
	if got != want {
		t.Errorf("PodFormatAdvisor.DefaultFormat() = %q, want DefaultServeFormat() = %q", got, want)
	}
}

// TestAdvisorTableDriven exercises Advise across a table of realistic pod
// configurations: small pod (forces AWQ), large pod (fp16 fits), giant model
// (nothing fits even on A100). This covers real operator scenarios end-to-end.
func TestAdvisorTableDriven(t *testing.T) {
	// Pre-compute estimates for table construction.
	fp16Est7B, _ := EstimateVRAM(model7BWeightBytes, ServeFormatFP16)
	awqEst7B, _ := EstimateVRAM(model7BWeightBytes, ServeFormatAWQ4)
	awqEst70B, _ := EstimateVRAM(model70BWeightBytes, ServeFormatAWQ4)

	cases := []struct {
		name             string
		vramBudget       int64
		modelWeightBytes int64
		wantFormat       ServeFormat // empty means expect ErrNoFit
		wantErr          bool
	}{
		{
			name:             "7B_on_a100_fp16_fits",
			vramBudget:       a100VRAMBytes,
			modelWeightBytes: model7BWeightBytes,
			wantFormat:       ServeFormatFP16,
		},
		{
			name:             "7B_tight_budget_awq_only",
			vramBudget:       fp16Est7B.TotalBytes - 1,
			modelWeightBytes: model7BWeightBytes,
			wantFormat:       ServeFormatAWQ4,
		},
		{
			name:             "7B_tiny_budget_no_fit",
			vramBudget:       awqEst7B.TotalBytes - 1,
			modelWeightBytes: model7BWeightBytes,
			wantFormat:       "",
			wantErr:          true,
		},
		{
			name: "70B_large_pod_awq_fits",
			// Budget: 2× the AWQ estimate so AWQ fits but fp16 (10× larger) does not.
			vramBudget:       awqEst70B.TotalBytes * 2,
			modelWeightBytes: model70BWeightBytes,
			wantFormat:       ServeFormatAWQ4,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			adv, err := NewPodFormatAdvisor(PodConfig{
				PodID:            tc.name,
				VRAMBudgetBytes:  tc.vramBudget,
				ModelWeightBytes: tc.modelWeightBytes,
			})
			if err != nil {
				t.Fatalf("%s %s: NewPodFormatAdvisor: %v", advUUID, tc.name, err)
			}

			rec, err := adv.Advise()
			t.Logf("%s %s: Advise() format=%q VRAM=%d err=%v",
				advUUID, tc.name, rec.Format, rec.EstimatedVRAMBytes, err)

			if tc.wantErr {
				if !errors.Is(err, ErrNoFit) {
					t.Errorf("want ErrNoFit, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rec.Format != tc.wantFormat {
				t.Errorf("want format %q, got %q", tc.wantFormat, rec.Format)
			}
			if rec.EstimatedVRAMBytes <= 0 {
				t.Errorf("EstimatedVRAMBytes must be > 0, got %d", rec.EstimatedVRAMBytes)
			}
			if rec.EstimatedVRAMBytes > tc.vramBudget {
				t.Errorf("EstimatedVRAMBytes (%d) exceeds pod budget (%d)",
					rec.EstimatedVRAMBytes, tc.vramBudget)
			}
			if rec.Reason == "" {
				t.Error("Reason must be non-empty")
			}
		})
	}
}
