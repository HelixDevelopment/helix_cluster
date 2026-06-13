// Package smartrouter implements an intelligent ModelRouter that selects the
// best-fit model for a workload based on one of five routing strategies:
// Latency, Throughput, Cost, TEE, or Balanced.
//
// Routing rules:
//   - UNHEALTHY models (Healthy == false) are ALWAYS excluded from every strategy.
//     Removing this guard makes TestUnhealthyModelExcluded fail.  (guard A)
//   - Latency picks the model with the LOWEST TTFTMillis (time-to-first-token).
//     Inverting the comparator makes TestLatencyPicksLowestTTFT fail.       (guard B)
//   - Throughput picks the model with the HIGHEST ThroughputTokS.
//   - Cost picks the model with the LOWEST CostPerMTokens.
//     This guard makes TestCostPicksCheapest fail when inverted.            (guard C)
//   - TEE restricts to TEE-capable models then picks the cheapest among them.
//   - Balanced computes a weighted score:
//     score = w_ttft*(1/TTFTMillis) + w_tput*ThroughputTokS + w_cost*(1/CostPerMTokens)
//     with weights 0.4 / 0.3 / 0.3 respectively; higher is better.
//
// Every routing decision is appended to an internal Decision log accessible
// via Decisions(). Each Decision records the RunID, Strategy, and chosen
// model ID so callers can assert on real sink-side behaviour.
package smartrouter

import (
	"errors"
	"fmt"
	"sync"
)

// Strategy names the algorithm used to select a model.
type Strategy string

const (
	StrategyLatency    Strategy = "Latency"
	StrategyThroughput Strategy = "Throughput"
	StrategyCost       Strategy = "Cost"
	StrategyTEE        Strategy = "TEE"
	StrategyBalanced   Strategy = "Balanced"
)

// Balanced score weights (must sum to 1.0 for interpretability, but the
// router only requires consistent relative weighting).
const (
	wTTFT = 0.4
	wTput = 0.3
	wCost = 0.3
)

// ErrNoEligibleModel is returned when every model is either unhealthy or
// does not satisfy the strategy's hard constraints (e.g. TEE required but
// no TEE-capable healthy model exists).
var ErrNoEligibleModel = errors.New("smartrouter: no eligible model for the requested strategy")

// Model describes a single AI model available for routing.
type Model struct {
	// ID is the unique, stable identifier for the model.
	ID string

	// TTFTMillis is the mean time-to-first-token in milliseconds.
	// Lower is better for latency-sensitive workloads.
	TTFTMillis float64

	// ThroughputTokS is the sustained token generation rate (tokens/second).
	// Higher is better for bulk-generation workloads.
	ThroughputTokS float64

	// CostPerMTokens is the cost in USD per one million tokens generated.
	// Lower is better for cost-sensitive workloads.
	CostPerMTokens float64

	// TEE indicates the model runs inside a Trusted Execution Environment.
	TEE bool

	// Healthy gates the model's eligibility: false → always excluded.
	Healthy bool
}

// Decision records one routing event.
type Decision struct {
	RunID    string
	Strategy Strategy
	ModelID  string
}

// Router selects models according to the configured pool.
type Router struct {
	mu        sync.Mutex
	models    []Model
	decisions []Decision
}

// New constructs a Router with the supplied model pool.  The slice is copied
// so later mutations by the caller do not affect the Router.
func New(models []Model) *Router {
	cp := make([]Model, len(models))
	copy(cp, models)
	return &Router{models: cp}
}

// Decisions returns a snapshot of every routing decision recorded so far.
// The slice is a copy; mutations do not affect the internal log.
func (r *Router) Decisions() []Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Decision, len(r.decisions))
	copy(out, r.decisions)
	return out
}

// Route selects the best model for the given strategy and records a Decision
// tagged with runID.  It returns the chosen model ID, or ErrNoEligibleModel
// when no healthy model satisfies the strategy's constraints.
func (r *Router) Route(runID string, s Strategy) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// guard A — Healthy filter: removing this makes TestUnhealthyModelExcluded fail.
	healthy := make([]Model, 0, len(r.models))
	for _, m := range r.models {
		if m.Healthy { // MUTATION TARGET: invert (m.Healthy → !m.Healthy) kills TestUnhealthyModelExcluded
			healthy = append(healthy, m)
		}
	}

	if len(healthy) == 0 {
		return "", ErrNoEligibleModel
	}

	var chosen string
	var err error

	switch s {
	case StrategyLatency:
		chosen, err = routeLatency(healthy)
	case StrategyThroughput:
		chosen, err = routeThroughput(healthy)
	case StrategyCost:
		chosen, err = routeCost(healthy)
	case StrategyTEE:
		chosen, err = routeTEE(healthy)
	case StrategyBalanced:
		chosen, err = routeBalanced(healthy)
	default:
		return "", fmt.Errorf("smartrouter: unknown strategy %q", s)
	}

	if err != nil {
		return "", err
	}

	r.decisions = append(r.decisions, Decision{
		RunID:    runID,
		Strategy: s,
		ModelID:  chosen,
	})
	return chosen, nil
}

// routeLatency picks the model with the lowest TTFTMillis.
// guard B — comparator: inverting (best.TTFTMillis > m.TTFTMillis) kills TestLatencyPicksLowestTTFT.
func routeLatency(pool []Model) (string, error) {
	best := pool[0]
	for _, m := range pool[1:] {
		if m.TTFTMillis < best.TTFTMillis { // MUTATION TARGET: invert (<→>) kills TestLatencyPicksLowestTTFT
			best = m
		}
	}
	return best.ID, nil
}

// routeThroughput picks the model with the highest ThroughputTokS.
func routeThroughput(pool []Model) (string, error) {
	best := pool[0]
	for _, m := range pool[1:] {
		if m.ThroughputTokS > best.ThroughputTokS {
			best = m
		}
	}
	return best.ID, nil
}

// routeCost picks the model with the lowest CostPerMTokens.
// guard C — comparator: inverting (<→>) kills TestCostPicksCheapest.
func routeCost(pool []Model) (string, error) {
	best := pool[0]
	for _, m := range pool[1:] {
		if m.CostPerMTokens < best.CostPerMTokens { // MUTATION TARGET: invert (<→>) kills TestCostPicksCheapest
			best = m
		}
	}
	return best.ID, nil
}

// routeTEE restricts the pool to TEE-capable models, then picks the cheapest.
func routeTEE(pool []Model) (string, error) {
	var teePool []Model
	for _, m := range pool {
		if m.TEE {
			teePool = append(teePool, m)
		}
	}
	if len(teePool) == 0 {
		return "", ErrNoEligibleModel
	}
	return routeCost(teePool)
}

// routeBalanced scores each model with a weighted combination of normalised
// inverse-latency, throughput, and inverse-cost.  Higher score wins.
//
//	score = 0.4*(1/TTFTMillis) + 0.3*ThroughputTokS + 0.3*(1/CostPerMTokens)
//
// Models with zero TTFTMillis or zero CostPerMTokens are silently skipped to
// avoid division-by-zero; if all are skipped ErrNoEligibleModel is returned.
func routeBalanced(pool []Model) (string, error) {
	bestScore := -1.0
	bestID := ""
	for _, m := range pool {
		if m.TTFTMillis == 0 || m.CostPerMTokens == 0 {
			continue
		}
		score := wTTFT*(1/m.TTFTMillis) + wTput*m.ThroughputTokS + wCost*(1/m.CostPerMTokens)
		if score > bestScore {
			bestScore = score
			bestID = m.ID
		}
	}
	if bestID == "" {
		return "", ErrNoEligibleModel
	}
	return bestID, nil
}
