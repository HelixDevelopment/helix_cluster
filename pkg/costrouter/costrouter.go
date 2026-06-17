// Package costrouter implements per-workload-type provider routing over a
// shared provider table. It is a self-contained, deterministic, pure-Go
// package (registry item HXC-1507).
//
// Model: each provider exposes a CostPerHour and a LatencyMs. A workload has a
// Type — Inference (latency-sensitive) or Training (cost/throughput-sensitive).
// Routing the SAME provider table:
//   - Inference picks the LOWEST-LATENCY provider (latency-weighted objective).
//   - Training  picks the LOWEST-COST    provider (cost-weighted objective).
//
// Ties are broken deterministically by ascending provider ID. Routing over an
// empty provider table returns ErrNoProviders.
package costrouter

import (
	"errors"
	"math"
)

// ErrNoProviders is returned by Route when the provider table is empty.
var ErrNoProviders = errors.New("costrouter: no providers")

// ErrNonFiniteObjective is returned by Route when a candidate's objective value
// (LatencyMs for Inference, CostPerHour for Training) is NaN or ±Inf. Such a
// value cannot participate in an ordering comparison (every comparison against
// NaN is false), so a NaN candidate at the head of the table would silently
// seize the "best" slot and never be displaced — a mis-route. Rather than route
// to a poisoned candidate, Route rejects the table fail-closed.
var ErrNonFiniteObjective = errors.New("costrouter: non-finite objective value")

// WorkloadType selects which objective drives provider selection.
type WorkloadType int

const (
	// Inference is latency-sensitive: route to the lowest-latency provider.
	Inference WorkloadType = iota
	// Training is cost/throughput-sensitive: route to the lowest-cost provider.
	Training
)

// String renders a WorkloadType for logging.
func (w WorkloadType) String() string {
	switch w {
	case Inference:
		return "Inference"
	case Training:
		return "Training"
	default:
		return "Unknown"
	}
}

// Provider is a candidate compute provider in the shared table.
type Provider struct {
	ID          string
	CostPerHour float64
	LatencyMs   float64
}

// Route selects a provider from the shared table according to the workload
// type's objective:
//   - Inference: minimize LatencyMs.
//   - Training:  minimize CostPerHour.
//
// On equal objective values the provider with the smaller ID (lexicographic)
// wins, making the choice fully deterministic. An empty table returns
// ErrNoProviders.
func Route(t WorkloadType, providers []Provider) (Provider, error) {
	if len(providers) == 0 {
		return Provider{}, ErrNoProviders
	}

	best := providers[0]
	bestScore := objective(t, best)
	if !isFinite(bestScore) {
		return Provider{}, ErrNonFiniteObjective
	}
	for _, p := range providers[1:] {
		s := objective(t, p)
		if !isFinite(s) {
			return Provider{}, ErrNonFiniteObjective
		}
		switch {
		case s < bestScore:
			best, bestScore = p, s
		case s == bestScore && p.ID < best.ID:
			// Deterministic tie-break by ascending ID.
			best = p
		}
	}
	return best, nil
}

// objective returns the value to MINIMIZE for the given workload type over a
// provider. Inference weights latency; Training weights cost. These are
// distinct fields, so over a table where the cheapest provider is not the
// fastest the two objectives select different providers.
func objective(t WorkloadType, p Provider) float64 {
	if t == Inference {
		return p.LatencyMs
	}
	return p.CostPerHour
}

// isFinite reports whether x is a usable, orderable objective value — i.e. not
// NaN and not ±Inf. Comparisons against NaN are always false, so a NaN objective
// would corrupt the min-selection; ±Inf is rejected for the same fail-closed
// reason (a malformed/sentinel value must not silently participate in routing).
func isFinite(x float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0)
}
