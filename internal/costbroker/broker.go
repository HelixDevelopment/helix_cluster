// Package costbroker provides a ComputeBroker that selects the best compute
// offer under a budget while meeting a job's capacity requirement, and tracks
// cumulative spend across selections.
//
// The broker is stdlib-only. Offers are scored by an effective-cost metric that
// blends raw price with reliability and latency penalties; among all offers that
// satisfy the requirement (within budget and at-or-above the required capacity)
// the broker selects the one with the lowest effective cost.
package costbroker

import (
	"errors"
	"fmt"
	"math"
	"sync"
)

// Reasons a Select call may reject all candidate offers.
const (
	// ReasonNoOffers indicates no candidate offers were supplied.
	ReasonNoOffers = "no offers supplied"
	// ReasonNoneQualify indicates every offer was over budget, under capacity,
	// or otherwise invalid.
	ReasonNoneQualify = "no offer satisfies the requirement"
)

// Offer is a single candidate compute offer from a provider.
type Offer struct {
	Provider    string  // provider/offer identity, e.g. "aws-spot"
	PricePerHr  float64 // raw price in $/hr (must be >= 0)
	Reliability float64 // reliability score in [0,1]; higher is better
	LatencyMs   float64 // expected latency in milliseconds (must be >= 0)
	Capacity    int     // units of compute capacity offered (must be >= 0)
}

// Requirement describes what a job needs from an offer.
type Requirement struct {
	MinCapacity int     // minimum acceptable capacity (inclusive)
	MaxPricePer float64 // hard budget cap in $/hr (inclusive); offers above are rejected
	// Weights tuning the effective-cost score. Zero values fall back to
	// sensible defaults so a zero Requirement still produces a usable score.
	ReliabilityWeight float64 // penalty multiplier for unreliability; default 1.0
	LatencyWeight     float64 // $/hr added per millisecond of latency; default 0
}

// Decision is the outcome of a Select call.
type Decision struct {
	Selected bool    // true if an offer was chosen
	Offer    Offer   // the chosen offer (zero value when Selected is false)
	Score    float64 // effective cost of the chosen offer (lower is better)
	Reason   string  // rejection reason when Selected is false
	Charged  float64 // $/hr added to cumulative spend by this decision
}

// ErrNilRequirement is returned when Select is given no requirement context it
// can act on. It is currently unused by Select (which takes a value) but is
// exported so callers wrapping the broker can reuse a stable sentinel.
var ErrNilRequirement = errors.New("costbroker: nil requirement")

// ComputeBroker selects offers under a budget and tracks cumulative spend. It is
// safe for concurrent use.
type ComputeBroker struct {
	mu    sync.Mutex
	spend float64
}

// NewComputeBroker creates a broker with zero accumulated spend.
func NewComputeBroker() *ComputeBroker {
	return &ComputeBroker{}
}

// effectiveCost computes the score used to rank offers. Lower is better.
//
// The score starts from the raw price, divides by reliability so that a less
// reliable offer at the same price costs effectively more, and adds a latency
// surcharge. Defaults are applied for zero weights so a zero Requirement still
// yields a meaningful, finite score.
func effectiveCost(o Offer, req Requirement) float64 {
	relWeight := req.ReliabilityWeight
	if relWeight <= 0 {
		relWeight = 1.0
	}
	// Reliability factor in (0,1]; guard against a zero/invalid reliability so we
	// never divide by zero. A perfectly reliable offer (1.0) incurs no penalty.
	rel := o.Reliability
	if rel <= 0 {
		rel = 1e-6
	}
	if rel > 1 {
		rel = 1
	}
	// penalty grows as reliability drops below 1: at rel=1 it is exactly price.
	score := o.PricePerHr * (1 + relWeight*(1-rel))
	score += req.LatencyWeight * o.LatencyMs
	return score
}

// qualifies reports whether an offer is eligible under the requirement: it must
// have non-negative price, meet the capacity floor, and not exceed the budget.
func qualifies(o Offer, req Requirement) bool {
	// Reject a non-finite price FIRST: a NaN slips both the `< 0` guard and the
	// `> MaxPricePer` budget cap (every NaN comparison is false), so it would be
	// admitted, selected, and poison the cumulative spend to NaN permanently — a
	// cap bypass + billing corruption. (+Inf is caught by the cap, but guard both
	// here for clarity.)
	if math.IsNaN(o.PricePerHr) || math.IsInf(o.PricePerHr, 0) {
		return false
	}
	if o.PricePerHr < 0 || o.Capacity < 0 {
		return false
	}
	if o.Capacity < req.MinCapacity {
		return false
	}
	if req.MaxPricePer > 0 && o.PricePerHr > req.MaxPricePer {
		return false
	}
	return true
}

// Select chooses the lowest effective-cost offer that satisfies req from the
// given candidates. Offers over budget or under the capacity floor are excluded.
// On success the chosen offer's raw price is added to cumulative spend.
//
// When no offer qualifies, the returned Decision has Selected=false and a Reason
// explaining why, and cumulative spend is left unchanged.
func (b *ComputeBroker) Select(req Requirement, offers []Offer) Decision {
	if len(offers) == 0 {
		return Decision{Selected: false, Reason: ReasonNoOffers}
	}

	bestIdx := -1
	var bestScore float64
	for i, o := range offers {
		if !qualifies(o, req) {
			continue
		}
		score := effectiveCost(o, req)
		if bestIdx == -1 || score < bestScore {
			bestIdx = i
			bestScore = score
		}
	}

	if bestIdx == -1 {
		return Decision{Selected: false, Reason: ReasonNoneQualify}
	}

	chosen := offers[bestIdx]
	b.mu.Lock()
	b.spend += chosen.PricePerHr
	b.mu.Unlock()

	return Decision{
		Selected: true,
		Offer:    chosen,
		Score:    bestScore,
		Charged:  chosen.PricePerHr,
	}
}

// SpendSoFar returns the cumulative $/hr charged across all successful Select
// calls on this broker.
func (b *ComputeBroker) SpendSoFar() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spend
}

// String renders a Decision for logs and test diagnostics.
func (d Decision) String() string {
	if d.Selected {
		return fmt.Sprintf("selected %s @ $%.4f/hr (score %.4f)", d.Offer.Provider, d.Offer.PricePerHr, d.Score)
	}
	return fmt.Sprintf("rejected: %s", d.Reason)
}
