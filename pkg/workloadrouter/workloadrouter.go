// Package workloadrouter implements HXC-1455: UnifiedManager.RouteWorkload.
//
// RouteWorkload routes a workload to the best marketplace offer by:
//  1. probing each offer's LIVE price CONCURRENTLY (one goroutine per offer,
//     via the PriceFunc seam), falling back to the static Offer.PriceUSD on
//     probe error; and
//  2. ranking offers by a WEIGHTED COMPOSITE of multiple factors (price,
//     latency, health) — NOT a simple revenue-greedy pick.
//
// This is deliberately distinct from a single-factor optimiser: the winner is
// the offer with the highest composite score, where lower price and lower
// latency are better (they contribute inversely) and higher health is better.
//
// When the workload requires a TEE, the composite score of every TEE-capable
// offer is multiplied by TEEMultiplier (>1), boosting confidential offers; the
// boost is the mechanism that can flip the winner. Non-TEE-capable offers are
// NOT excluded — they are simply not boosted, so a sufficiently cheap/fast/
// healthy non-TEE offer can still win unless the multiplier tips the balance.
//
// Pure standard library only.
package workloadrouter

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
)

// Offer is a single marketplace bid for hosting a workload.
// HealthScore is in [0,1] (1 = perfectly healthy).
type Offer struct {
	Marketplace string
	PriceUSD    float64
	LatencyMS   float64
	HealthScore float64
	TEECapable  bool
}

// Weights are the composite-score weights for each factor.
type Weights struct {
	Price   float64
	Latency float64
	Health  float64
}

// Workload is the thing being routed.
type Workload struct {
	ID          string
	TEERequired bool
}

// PriceFunc is a per-marketplace live price probe — the "concurrent pricing"
// seam. It returns the current USD price for the given marketplace.
type PriceFunc func(ctx context.Context, marketplace string) (float64, error)

// UnifiedManager routes workloads using a weighted composite score with an
// optional TEE multiplier.
type UnifiedManager struct {
	Weights       Weights
	TEEMultiplier float64
}

// Decision is the routing outcome.
type Decision struct {
	WorkloadID string
	Winner     string
	Score      float64
}

// ErrNoOffers is returned when RouteWorkload is given zero offers.
var ErrNoOffers = errors.New("workloadrouter: no offers")

// NewUnifiedManager constructs a UnifiedManager.
func NewUnifiedManager(w Weights, teeMult float64) *UnifiedManager {
	return &UnifiedManager{Weights: w, TEEMultiplier: teeMult}
}

// scored is an offer paired with its computed composite score.
type scored struct {
	marketplace string
	score       float64
}

// composite computes the base (pre-TEE-boost) composite score for an offer
// given a resolved live price. Higher is better: lower price and lower latency
// contribute inversely (1/(1+x)), higher health contributes directly.
//
// NaN/Inf-POISON GUARD: a non-finite input on ANY factor (a hostile/buggy live
// PriceFunc returning NaN/±Inf, or a NaN/Inf Offer field, or a non-finite
// Weight) must not produce a NaN composite. A NaN score breaks the sort.Slice
// strict-weak-ordering ("NaN != x" is always true, every comparison with NaN is
// false), letting a poisoned offer SEIZE the winner slot and mis-route the
// workload. invert() already clamps non-finite price/latency to the worst-case 0
// contribution; here we also clamp a non-finite health term and, as a final
// backstop, fold any non-finite composite to the worst-possible score so a
// poisoned offer always LOSES to any finite offer rather than seizing first
// place.
func (m *UnifiedManager) composite(price, latency, health float64) float64 {
	if math.IsNaN(health) || math.IsInf(health, 0) {
		health = 0
	}
	s := m.Weights.Price*invert(price) +
		m.Weights.Latency*invert(latency) +
		m.Weights.Health*health
	if math.IsNaN(s) || math.IsInf(s, 0) {
		// Non-finite composite (e.g. Inf*0 from a non-finite Weight) -> worst.
		return 0
	}
	return s
}

// invert maps "lower is better" magnitudes into a bounded "higher is better"
// contribution in [0,1]. Non-negative inputs only (price/latency are >= 0). A
// non-finite input (NaN/±Inf) is clamped to the worst-case 0 contribution so it
// can never poison the composite with a NaN.
func invert(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	if x < 0 {
		x = 0
	}
	return 1.0 / (1.0 + x)
}

// RouteWorkload concurrently probes each offer's live price, computes a
// weighted composite score per offer (boosting TEE-capable offers by
// TEEMultiplier when the workload requires a TEE), and returns the highest
// scoring offer. Ties break deterministically by Marketplace name (ascending).
func (m *UnifiedManager) RouteWorkload(ctx context.Context, w Workload, offers []Offer, price PriceFunc) (Decision, error) {
	if len(offers) == 0 {
		return Decision{}, ErrNoOffers
	}

	// Concurrent pricing: one goroutine per offer resolves the live price,
	// falling back to the static offer price on probe error.
	livePrices := make([]float64, len(offers))
	var wg sync.WaitGroup
	wg.Add(len(offers))
	for i := range offers {
		go func(i int) {
			defer wg.Done()
			p := offers[i].PriceUSD
			if price != nil {
				if lp, err := price(ctx, offers[i].Marketplace); err == nil {
					p = lp
				}
			}
			livePrices[i] = p
		}(i)
	}
	wg.Wait()

	results := make([]scored, 0, len(offers))
	for i, o := range offers {
		s := m.composite(livePrices[i], o.LatencyMS, o.HealthScore)
		// LOAD-BEARING GUARD (1): the TEE multiplier. When the workload
		// requires a TEE, TEE-capable offers get their composite score
		// multiplied by TEEMultiplier. Removing this line drops the boost and
		// flips TestRoute_TEEMultiplierFlipsWinner.
		if w.TEERequired && o.TEECapable {
			s *= m.TEEMultiplier
		}
		results = append(results, scored{marketplace: o.Marketplace, score: s})
	}

	// Deterministic ordering: highest score first, ties by marketplace name.
	sort.Slice(results, func(a, b int) bool {
		if results[a].score != results[b].score {
			return results[a].score > results[b].score
		}
		return results[a].marketplace < results[b].marketplace
	})

	best := results[0]
	return Decision{WorkloadID: w.ID, Winner: best.marketplace, Score: best.score}, nil
}
