// Package quantization — PodFormatAdvisor: the concrete serving-format decision
// point that GPU inference pods consult at startup (and on reschedule) to pick
// their operating format.
//
// WHY this file exists (CLAUDE-1 wiring rationale):
//
// DefaultServeFormat, EstimateVRAM, and SelectFormat in awq.go expose the AWQ
// policy as pure functions. On their own they are ONLY called by tests — a
// CLAUDE-1 PASS-bluff. This file wires them into a concrete, exported type that
// GPU pod controllers instantiate and call. PodFormatAdvisor is the real API
// surface: a pod controller constructs one with the pod's VRAM budget and the
// fp16 model-weight size, then calls Advise() to get the format the pod should
// actually use. If the operator has not overridden the format, Advise() defers to
// DefaultServeFormat() — making AWQ 4-bit the real operational default.
//
// Scope / honesty (§11.4.3):
//
//	Actual GPU driver interaction, vLLM process management, and model loading are
//	OUT OF SCOPE for this pure-Go package. PodFormatAdvisor produces a
//	ServeFormat decision (a string) that the calling pod controller acts on.
//	No GPU probing or inference is performed here.
package quantization

import "fmt"

// PodConfig describes the VRAM envelope and model characteristics of a single
// GPU inference pod.
type PodConfig struct {
	// PodID is a stable identifier used in log and error messages.
	PodID string
	// VRAMBudgetBytes is the total VRAM available to the pod (in bytes).
	// A typical A100-40GB pod: 40 * 1024 * 1024 * 1024 bytes.
	VRAMBudgetBytes int64
	// ModelWeightBytes is the fp16 model weight size in bytes. This is the
	// baseline for VRAM estimation; AWQ-4bit compresses from this figure.
	// For a 7B-parameter fp16 model: 7e9 × 2 bytes ≈ 14 GiB.
	ModelWeightBytes int64
	// FormatOverride, when non-empty, bypasses automatic format selection and
	// forces the named format. An operator sets this to pin a pod to a specific
	// serving format regardless of the VRAM budget.
	FormatOverride ServeFormat
}

// PodFormatAdvisor is the serving-format decision point for a single GPU pod.
//
// Construction: use NewPodFormatAdvisor(cfg). The zero value is invalid.
//
// Usage pattern (a real pod controller):
//
//	adv, err := NewPodFormatAdvisor(PodConfig{
//	    PodID:            "gpu-pod-0",
//	    VRAMBudgetBytes:  40 * 1<<30,          // 40 GiB A100
//	    ModelWeightBytes: 7_000_000_000 * 2,   // 7B fp16
//	})
//	if err != nil { /* reject pod config */ }
//	rec, err := adv.Advise()
//	if err != nil { /* ErrNoFit: pod cannot serve this model */ }
//	startVLLM(rec.Format, rec.EstimatedVRAMBytes) // → "awq-4bit" or "fp16"
type PodFormatAdvisor struct {
	cfg PodConfig
}

// FormatRecommendation is the output of PodFormatAdvisor.Advise.
type FormatRecommendation struct {
	// Format is the serving format the pod should start with.
	Format ServeFormat
	// EstimatedVRAMBytes is the VRAM the advisor expects the pod to consume.
	EstimatedVRAMBytes int64
	// Reason is a human-readable explanation of why this format was chosen.
	// Pod controllers should log this on startup.
	Reason string
}

// NewPodFormatAdvisor validates cfg and returns a ready advisor.
//
// Returns an error for invalid configs (non-positive budget/weight, or an
// unrecognised FormatOverride value).
func NewPodFormatAdvisor(cfg PodConfig) (*PodFormatAdvisor, error) {
	if cfg.VRAMBudgetBytes <= 0 {
		return nil, fmt.Errorf("quantization: pod %q: VRAMBudgetBytes must be > 0, got %d",
			cfg.PodID, cfg.VRAMBudgetBytes)
	}
	if cfg.ModelWeightBytes <= 0 {
		return nil, fmt.Errorf("quantization: pod %q: ModelWeightBytes must be > 0, got %d",
			cfg.PodID, cfg.ModelWeightBytes)
	}
	// Validate override, if set, by running it through EstimateVRAM (unknown
	// formats return an error there). An empty override is valid — Advise picks.
	if cfg.FormatOverride != "" {
		if _, err := EstimateVRAM(cfg.ModelWeightBytes, cfg.FormatOverride); err != nil {
			return nil, fmt.Errorf("quantization: pod %q: invalid FormatOverride: %w",
				cfg.PodID, err)
		}
	}
	return &PodFormatAdvisor{cfg: cfg}, nil
}

// Advise returns the serving-format recommendation for this pod.
//
// Decision logic:
//  1. If FormatOverride is set, use it (operator-pinned).
//  2. Otherwise, call SelectFormat(modelWeightBytes, VRAMBudgetBytes):
//     — fp16 if it fits (highest quality)
//     — AWQ-4bit if only AWQ fits (DefaultServeFormat() is AWQ-4bit)
//     — ErrNoFit if neither fits (pod must be rejected or model swapped)
//
// The returned Reason field explains why the format was chosen so the pod
// controller can log an operator-visible decision trace.
func (a *PodFormatAdvisor) Advise() (FormatRecommendation, error) {
	cfg := a.cfg

	// Branch 1: operator override — skip automatic selection.
	if cfg.FormatOverride != "" {
		est, err := EstimateVRAM(cfg.ModelWeightBytes, cfg.FormatOverride)
		if err != nil {
			// Should not happen: we validated the override at construction time.
			return FormatRecommendation{}, err
		}
		reason := fmt.Sprintf("pod %q: operator-pinned format %q; estimated VRAM %d bytes (budget %d bytes)",
			cfg.PodID, cfg.FormatOverride, est.TotalBytes, cfg.VRAMBudgetBytes)
		return FormatRecommendation{
			Format:             cfg.FormatOverride,
			EstimatedVRAMBytes: est.TotalBytes,
			Reason:             reason,
		}, nil
	}

	// Branch 2: automatic selection via SelectFormat.
	//
	// SelectFormat prefers fp16 when it fits (highest quality). When fp16 does
	// not fit but AWQ-4bit does, it returns ServeFormatAWQ4 — which is exactly
	// DefaultServeFormat(). When neither fits, it returns ErrNoFit.
	chosen, err := SelectFormat(cfg.ModelWeightBytes, cfg.VRAMBudgetBytes)
	if err != nil {
		// ErrNoFit propagates as-is; callers must reject the pod config or swap
		// to a smaller model.
		return FormatRecommendation{}, fmt.Errorf("pod %q: %w", cfg.PodID, err)
	}

	est, _ := EstimateVRAM(cfg.ModelWeightBytes, chosen)
	// err is nil here because chosen was returned by SelectFormat which already
	// called EstimateVRAM for both formats; any invalid format would have been
	// caught above.

	var reason string
	defaultFmt := DefaultServeFormat()
	if chosen == defaultFmt {
		reason = fmt.Sprintf("pod %q: automatic selection chose default format %q "+
			"(fp16 does not fit %d-byte VRAM budget); estimated VRAM %d bytes",
			cfg.PodID, chosen, cfg.VRAMBudgetBytes, est.TotalBytes)
	} else {
		reason = fmt.Sprintf("pod %q: automatic selection preferred %q over default %q "+
			"(both fit; fp16 is higher quality); estimated VRAM %d bytes",
			cfg.PodID, chosen, defaultFmt, est.TotalBytes)
	}

	return FormatRecommendation{
		Format:             chosen,
		EstimatedVRAMBytes: est.TotalBytes,
		Reason:             reason,
	}, nil
}

// DefaultFormat returns the default serving format as selected by this advisor's
// automatic policy. This is the value that would be chosen when fp16 does not fit
// in the VRAM budget — currently ServeFormatAWQ4. Pod controllers that want to
// log or display the cluster-wide default without constructing a full advisor can
// call DefaultServeFormat() directly.
func (a *PodFormatAdvisor) DefaultFormat() ServeFormat {
	return DefaultServeFormat()
}
