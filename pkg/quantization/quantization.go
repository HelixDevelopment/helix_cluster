// Package quantization implements a pure-Go, deterministic per-tier workload
// quantization SELECTION policy.
//
// Scope: this package contains ONLY the selection/policy decision logic. It
// decides which model variant a given device tier should run, based on the
// tier's memory budget and the set of inference backends the tier supports.
// Actual model inference / loading is intentionally OUT OF SCOPE and is not
// faked here.
//
// Model:
//   - A DeviceTier (e.g. T1 datacenter .. T8 microcontroller) has a memory
//     budget (MemoryMiB) and a set of supported inference Backends.
//   - A ModelVariant is one quantized build of a model (e.g. FP16, INT8, INT4),
//     with a memory footprint and the Backend it requires.
//   - Select picks the highest-quality variant whose footprint FITS the tier's
//     memory budget and whose required Backend the tier supports. A tier too
//     small for every variant returns ErrNoFit.
package quantization

import (
	"errors"
	"sort"
)

// ErrNoFit is returned by Select when no candidate variant fits the tier:
// either every variant's footprint exceeds the tier memory budget, or no
// fitting variant uses a backend the tier supports.
var ErrNoFit = errors.New("quantization: no model variant fits the device tier")

// ErrNoCandidates is returned by Select when the candidate slice is empty.
var ErrNoCandidates = errors.New("quantization: no candidate variants supplied")

// Quant identifies a quantization level. Lower bit-width => smaller footprint
// and (generally) lower quality. Quality ordering is given by Quant.Quality.
type Quant string

const (
	// QuantFP16 is half-precision floating point (highest quality, largest).
	QuantFP16 Quant = "FP16"
	// QuantINT8 is 8-bit integer quantization.
	QuantINT8 Quant = "INT8"
	// QuantINT4 is 4-bit integer quantization (lowest quality, smallest).
	QuantINT4 Quant = "INT4"
)

// Quality returns an ordinal quality score for the quantization level. Higher
// is better. Unknown levels score 0 so they never win a tie-break against a
// known level of equal footprint.
func (q Quant) Quality() int {
	switch q {
	case QuantFP16:
		return 30
	case QuantINT8:
		return 20
	case QuantINT4:
		return 10
	default:
		return 0
	}
}

// DeviceTier describes the resource envelope of a class of devices.
type DeviceTier struct {
	// ID is a stable identifier (e.g. "T1", "T8").
	ID string
	// MemoryMiB is the memory budget available to a model on this tier.
	MemoryMiB int
	// Backends is the set of inference backends this tier supports.
	Backends []string
}

// supports reports whether the tier supports the named backend.
func (t DeviceTier) supports(backend string) bool {
	for _, b := range t.Backends {
		if b == backend {
			return true
		}
	}
	return false
}

// ModelVariant is one quantized build of a model.
type ModelVariant struct {
	// Name is a human-readable identifier (e.g. "llama-7b-int4").
	Name string
	// Quant is the quantization level of this build.
	Quant Quant
	// FootprintMiB is the memory required to run this build.
	FootprintMiB int
	// Backend is the inference backend this build requires.
	Backend string
}

// Select chooses the highest-quality candidate variant that BOTH fits the
// tier's memory budget (FootprintMiB <= tier.MemoryMiB) AND requires a backend
// the tier supports.
//
// Selection ranking among eligible candidates:
//  1. higher quantization quality wins (FP16 > INT8 > INT4);
//  2. tie-break: larger footprint wins (closer to fully using the budget);
//  3. tie-break: lexicographically smaller Name wins (fully deterministic).
//
// If the candidate slice is empty, Select returns ErrNoCandidates. If no
// candidate is eligible (none fit memory, or none of the fitting ones use a
// supported backend), Select returns ErrNoFit.
func Select(tier DeviceTier, candidates []ModelVariant) (ModelVariant, error) {
	if len(candidates) == 0 {
		return ModelVariant{}, ErrNoCandidates
	}

	eligible := make([]ModelVariant, 0, len(candidates))
	for _, c := range candidates {
		// Memory-fit gate.
		if c.FootprintMiB > tier.MemoryMiB {
			continue
		}
		// Backend-support gate.
		if !tier.supports(c.Backend) {
			continue
		}
		eligible = append(eligible, c)
	}

	if len(eligible) == 0 {
		return ModelVariant{}, ErrNoFit
	}

	sort.SliceStable(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		// 1. quality (desc)
		if qa, qb := a.Quant.Quality(), b.Quant.Quality(); qa != qb {
			return qa > qb
		}
		// 2. footprint (desc) — prefer the build that uses more of the budget.
		if a.FootprintMiB != b.FootprintMiB {
			return a.FootprintMiB > b.FootprintMiB
		}
		// 3. name (asc) — deterministic final tie-break.
		return a.Name < b.Name
	})

	return eligible[0], nil
}
