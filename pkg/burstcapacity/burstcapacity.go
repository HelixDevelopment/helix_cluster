// Package burstcapacity provides deterministic, pure-Go burst capacity
// estimation and activation for the Helix Cluster scheduler.
//
// Model: a local cluster has a fixed local capacity and an incoming demand.
// When local utilization exceeds a high threshold (e.g. 0.90), a burst is
// ACTIVATED. On activation the estimator computes the spillover capacity —
// how much demand exceeds local capacity (the EXCESS, demand-localCapacity) —
// and creates a burst Allocation of exactly that size on the cheapest remote
// provider whose own capacity is sufficient to absorb the excess.
//
// This package is self-contained and does not depend on pkg/burst (or any
// other in-repo package). It owns its own types.
package burstcapacity

// Provider describes a remote burst-capacity provider.
type Provider struct {
	// ID identifies the provider.
	ID string
	// CostPerUnit is the price per unit of burst capacity. Lower is cheaper.
	CostPerUnit float64
	// Capacity is the maximum number of units this provider can absorb.
	Capacity float64
}

// Allocation is the result of a burst activation: the chosen remote provider
// and the estimated number of spillover units placed on it.
type Allocation struct {
	// ProviderID is the ID of the chosen remote provider (empty if no burst).
	ProviderID string
	// EstimatedUnits is the spillover capacity placed on the provider. It is
	// derived as demand-localCapacity (the EXCESS), never the full demand.
	EstimatedUnits float64
	// CostPerUnit is the per-unit cost of the chosen provider.
	CostPerUnit float64
}

// Estimator decides whether to activate a burst and sizes the spillover.
type Estimator struct {
	// Threshold is the utilization level (in [0,1]) above which a burst is
	// activated. A typical value is 0.90.
	Threshold float64
}

// ActivateBurst evaluates the local utilization and demand and, if a burst is
// warranted, returns an Allocation sized to the EXCESS demand on the cheapest
// sufficient remote provider.
//
// A burst is activated only when BOTH hold:
//   - localUtil is strictly greater than e.Threshold, AND
//   - demand strictly exceeds localCapacity (there is real spillover).
//
// The returned bool reports whether a burst was activated. When false, the
// Allocation is the zero value.
//
// Provider selection: among providers whose Capacity is at least the excess,
// the one with the lowest CostPerUnit is chosen (ties broken by lower
// Capacity, then by ID, for determinism). If no provider is sufficient, no
// burst is activated.
func (e Estimator) ActivateBurst(localUtil, localCapacity, demand float64, providers []Provider) (Allocation, bool) {
	// Below threshold -> never burst.
	if localUtil <= e.Threshold {
		return Allocation{}, false
	}

	// Excess is the spillover: how much demand exceeds local capacity.
	excess := demand - localCapacity
	if excess <= 0 {
		// Demand fits within local capacity; nothing to spill.
		return Allocation{}, false
	}

	chosen, ok := cheapestSufficient(providers, excess)
	if !ok {
		return Allocation{}, false
	}

	return Allocation{
		ProviderID:     chosen.ID,
		EstimatedUnits: excess,
		CostPerUnit:    chosen.CostPerUnit,
	}, true
}

// cheapestSufficient returns the provider with the lowest CostPerUnit whose
// Capacity is at least need. Determinism: ties on cost break to lower
// Capacity, then to lexicographically smaller ID.
func cheapestSufficient(providers []Provider, need float64) (Provider, bool) {
	var best Provider
	found := false
	for _, p := range providers {
		if p.Capacity < need {
			continue
		}
		if !found || betterChoice(p, best) {
			best = p
			found = true
		}
	}
	return best, found
}

// betterChoice reports whether candidate c should be preferred over the current
// best b. Ordering: lower cost, then lower capacity, then smaller ID.
func betterChoice(c, b Provider) bool {
	if c.CostPerUnit != b.CostPerUnit {
		return c.CostPerUnit < b.CostPerUnit
	}
	if c.Capacity != b.Capacity {
		return c.Capacity < b.Capacity
	}
	return c.ID < b.ID
}
