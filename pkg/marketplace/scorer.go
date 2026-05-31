package marketplace

import "sort"

// Weights configures the relative importance of each scored dimension. They do
// NOT need to sum to 1; the scorer normalises by the active weight total so a
// zero weight cleanly removes a dimension's influence. All weights must be
// >= 0.
type Weights struct {
	Price        float64 // weight of price (cheaper is better)
	Availability float64 // weight of availability (higher is better)
	Latency      float64 // weight of latency (lower is better)
	Throughput   float64 // weight of throughput (higher is better)
}

// DefaultWeights is the cluster's default preference: price 30%, availability
// 30%, latency 20%, throughput 20%.
var DefaultWeights = Weights{Price: 0.30, Availability: 0.30, Latency: 0.20, Throughput: 0.20}

// TEEMultiplier is the score multiplier applied to a Confidential offer when a
// job requires TEE. 1.5 means a confidential offer's composite score is boosted
// 50% relative to an otherwise-identical non-confidential offer.
const TEEMultiplier = 1.5

// ScoredOffer pairs an Offer with the composite Score that ranked it. Higher
// Score is better. The slice returned by Rank is sorted best-first.
type ScoredOffer struct {
	Offer Offer
	// Score is the final composite score in [0, TEEMultiplier]. It is the
	// weighted sum of normalised dimensions (each in [0,1]) times the TEE
	// multiplier when that applied.
	Score float64
	// TEEBoosted records whether the TEE multiplier was applied to this offer.
	TEEBoosted bool
}

// CompositeScorer ranks offers by a weighted, per-dimension-normalised score.
//
// Normalisation: for each dimension every offer is mapped into [0,1] where 1 is
// best. "Higher is better" dimensions (availability, throughput) map by
//
//	(v - min) / (max - min)
//
// and "lower is better" dimensions (price, latency) map by
//
//	(max - v) / (max - min)
//
// so a low price yields a high normalised value. When max == min for a
// dimension (single offer, or all offers identical on it) every offer scores
// 1.0 on that dimension — it carries no discriminating information, which is
// the correct neutral behaviour.
//
// The composite is the weighted average of the active (non-zero-weight)
// normalised dimensions, so a dimension with weight 0 is dropped entirely.
// Finally, when requireTEE is set, Confidential offers have their composite
// multiplied by TEEMultiplier, lifting confidential capacity above equivalent
// non-confidential capacity.
type CompositeScorer struct{}

// Rank scores every offer and returns them sorted best-first. Ties (equal
// score) are broken deterministically by Offer.ID so the ordering is stable.
//
// requireTEE applies the TEE preference multiplier to Confidential offers. It
// does NOT drop non-confidential offers — it only re-weights — so a job that
// merely prefers TEE still sees every option, just with confidential ones
// promoted.
func (CompositeScorer) Rank(offers []Offer, w Weights, requireTEE bool) []ScoredOffer {
	n := len(offers)
	if n == 0 {
		return nil
	}

	// Per-dimension min/max for normalisation.
	priceMin, priceMax := offers[0].PricePerHour, offers[0].PricePerHour
	availMin, availMax := offers[0].AvailabilityPct, offers[0].AvailabilityPct
	latMin, latMax := offers[0].LatencyMS, offers[0].LatencyMS
	thrMin, thrMax := offers[0].ThroughputTokS, offers[0].ThroughputTokS
	for _, o := range offers[1:] {
		priceMin, priceMax = minF(priceMin, o.PricePerHour), maxF(priceMax, o.PricePerHour)
		availMin, availMax = minF(availMin, o.AvailabilityPct), maxF(availMax, o.AvailabilityPct)
		latMin, latMax = minF(latMin, o.LatencyMS), maxF(latMax, o.LatencyMS)
		thrMin, thrMax = minF(thrMin, o.ThroughputTokS), maxF(thrMax, o.ThroughputTokS)
	}

	weightSum := w.Price + w.Availability + w.Latency + w.Throughput

	out := make([]ScoredOffer, n)
	for i, o := range offers {
		var composite float64
		if weightSum > 0 {
			// normLowerBetter for price/latency, normHigherBetter for avail/throughput.
			nPrice := normLowerBetter(o.PricePerHour, priceMin, priceMax)
			nAvail := normHigherBetter(o.AvailabilityPct, availMin, availMax)
			nLat := normLowerBetter(o.LatencyMS, latMin, latMax)
			nThr := normHigherBetter(o.ThroughputTokS, thrMin, thrMax)
			composite = (w.Price*nPrice +
				w.Availability*nAvail +
				w.Latency*nLat +
				w.Throughput*nThr) / weightSum
		}

		boosted := false
		if requireTEE && o.Confidential {
			composite *= TEEMultiplier
			boosted = true
		}
		out[i] = ScoredOffer{Offer: o, Score: composite, TEEBoosted: boosted}
	}

	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score // best (highest) first
		}
		return out[a].Offer.ID < out[b].Offer.ID // stable tie-break
	})
	return out
}

// normHigherBetter maps v into [0,1] where the maximum observed value scores 1.
// When max == min the dimension is non-discriminating and every offer scores 1.
func normHigherBetter(v, min, max float64) float64 {
	if max == min {
		return 1
	}
	return (v - min) / (max - min)
}

// normLowerBetter maps v into [0,1] where the minimum observed value scores 1.
// When max == min the dimension is non-discriminating and every offer scores 1.
func normLowerBetter(v, min, max float64) float64 {
	if max == min {
		return 1
	}
	return (max - v) / (max - min)
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
