package workloadrouter

import (
	"context"
	"math"
	"testing"
)

// ===========================================================================
// NaN/Inf SCORE-POISON probes (HXC adversarial wave).
//
// RouteWorkload ranks offers with sort.Slice using the comparator
//
//	if results[a].score != results[b].score { return results[a].score > results[b].score }
//
// A NaN score breaks strict-weak-ordering: `NaN != x` is always true and every
// `NaN > x` / `x > NaN` is false. A NaN-scored offer that starts in slot 0 is
// never displaced (nothing is "greater" than it), so it SEIZES the winner slot
// and the workload is mis-routed to a marketplace whose price/latency/health was
// garbage. A finite, genuinely-best offer must always win regardless of where a
// poisoned offer sits in the input.
//
// Realistic source of a NaN score: a hostile/buggy live PriceFunc returning NaN
// or +Inf flows straight into composite()/invert(); also a NaN/Inf field on the
// Offer itself (LatencyMS/HealthScore) or a non-finite Weight.
// ===========================================================================

// nanPriceFor returns a PriceFunc that yields NaN for one named marketplace and
// errors (static fallback) for all others.
func nanPriceFor(target string) PriceFunc {
	return func(_ context.Context, mk string) (float64, error) {
		if mk == target {
			return math.NaN(), nil
		}
		return 0, errStaticFallback
	}
}

var errStaticFallback = context.DeadlineExceeded // any non-nil error triggers fallback

// TestAdv2_NaNProbe_CannotSeizeWinner_AnyPosition is the load-bearing NaN-poison
// guard via the LIVE PRICE path. One offer's probe returns NaN; a finite offer
// is the genuine best. The poisoned offer is placed at every slot. The finite
// best ("good") MUST win every time, and the winning score MUST be finite.
//
// On UNGUARDED code the NaN score (from invert(NaN)=NaN) wins whenever the
// poisoned offer is first in the input slice -> FAILS.
func TestAdv2_NaNProbe_CannotSeizeWinner_AnyPosition(t *testing.T) {
	const runUUID = "adv2-nanprobe-6b1c"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 0, Health: 0}, 2.0)

	// "good" static price 0 -> finite score 1.0 (the true best).
	// "filler" static price 3 -> 0.25. "poison" gets NaN from the live probe.
	good := Offer{Marketplace: "good", PriceUSD: 0}
	filler := Offer{Marketplace: "filler", PriceUSD: 3}
	poison := Offer{Marketplace: "poison", PriceUSD: 1}

	layouts := [][]Offer{
		{poison, good, filler}, // poison first — the dangerous arrangement
		{good, poison, filler},
		{good, filler, poison},
		{poison, filler, good},
	}
	for li, offers := range layouts {
		got, err := m.RouteWorkload(context.Background(), Workload{ID: "w"}, offers, nanPriceFor("poison"))
		if err != nil {
			t.Fatalf("[%s] layout=%d err: %v", runUUID, li, err)
		}
		if got.Winner != "good" {
			t.Fatalf("[%s] layout=%d winner=%q score=%v, want good (finite best). NaN-poison seized the slot.",
				runUUID, li, got.Winner, got.Score)
		}
		if math.IsNaN(got.Score) || math.IsInf(got.Score, 0) {
			t.Fatalf("[%s] layout=%d winning score=%v is non-finite", runUUID, li, got.Score)
		}
	}
	t.Logf("[%s] NaN live-price probe cannot win from ANY input position; finite 'good' always wins", runUUID)
}

// TestAdv2_NaNOfferField_CannotSeizeWinner pins the same poison via a NaN field
// directly on the Offer (HealthScore=NaN), the static-price path (no live probe).
// The finite best must win from any position.
func TestAdv2_NaNOfferField_CannotSeizeWinner(t *testing.T) {
	const runUUID = "adv2-nanfield-9d04"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 1, Health: 1}, 2.0)

	good := Offer{Marketplace: "good", PriceUSD: 0, LatencyMS: 0, HealthScore: 1} // 1+1+1 = 3
	poison := Offer{Marketplace: "poison", PriceUSD: 0, LatencyMS: 0, HealthScore: math.NaN()}

	for li, offers := range [][]Offer{{poison, good}, {good, poison}} {
		got, err := m.RouteWorkload(context.Background(), Workload{ID: "w"}, offers, noLivePrice)
		if err != nil {
			t.Fatalf("[%s] layout=%d err: %v", runUUID, li, err)
		}
		if got.Winner != "good" {
			t.Fatalf("[%s] layout=%d winner=%q score=%v, want good. NaN HealthScore seized the slot.",
				runUUID, li, got.Winner, got.Score)
		}
		if math.IsNaN(got.Score) || math.IsInf(got.Score, 0) {
			t.Fatalf("[%s] layout=%d winning score=%v non-finite", runUUID, li, got.Score)
		}
	}
	t.Logf("[%s] NaN HealthScore offer cannot seize the winner slot from any position", runUUID)
}

// TestAdv2_InfProbe_CannotProduceNaNScore pins the +Inf vector. A +Inf live
// price makes invert(+Inf)=0 (finite, fine) — but a +Inf LATENCY combined with a
// 0 weight on a DIFFERENT term must not create NaN, and an offer whose latency is
// +Inf must score as the WORST (its 1/(1+Inf)=0 contribution), never seize the
// slot. The finite, lowest-latency offer must win and the score stays finite.
func TestAdv2_InfLatency_ScoresWorstNotBest(t *testing.T) {
	const runUUID = "adv2-inflat-2f7a"
	m := NewUnifiedManager(Weights{Price: 0, Latency: 1, Health: 0}, 2.0)
	good := Offer{Marketplace: "good", LatencyMS: 0}                  // 1/(1+0) = 1.0
	poison := Offer{Marketplace: "poison", LatencyMS: math.Inf(1)}    // 1/(1+Inf) = 0 (worst)
	for li, offers := range [][]Offer{{poison, good}, {good, poison}} {
		got, err := m.RouteWorkload(context.Background(), Workload{ID: "w"}, offers, noLivePrice)
		if err != nil {
			t.Fatalf("[%s] layout=%d err: %v", runUUID, li, err)
		}
		if got.Winner != "good" {
			t.Fatalf("[%s] layout=%d winner=%q, want good (lowest latency)", runUUID, li, got.Winner)
		}
		if math.IsNaN(got.Score) || math.IsInf(got.Score, 0) {
			t.Fatalf("[%s] layout=%d score=%v non-finite", runUUID, li, got.Score)
		}
	}
	t.Logf("[%s] +Inf latency scores worst, finite 'good' wins, score finite", runUUID)
}

// TestAdv2_AllScoresFinite_Invariant is a broad invariant: across a spread of
// hostile inputs (NaN/Inf on every numeric field + via the probe), RouteWorkload
// must always return a finite winning score and never a poisoned marketplace
// when a clean finite offer is present.
func TestAdv2_CleanOfferAlwaysBeatsPoison_Invariant(t *testing.T) {
	const runUUID = "adv2-invariant-4e88"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 1, Health: 1}, 2.0)
	clean := Offer{Marketplace: "clean", PriceUSD: 0, LatencyMS: 0, HealthScore: 1} // 3.0

	poisons := []Offer{
		{Marketplace: "p-price-nan", PriceUSD: math.NaN()},
		{Marketplace: "p-lat-nan", LatencyMS: math.NaN()},
		{Marketplace: "p-health-nan", HealthScore: math.NaN()},
		{Marketplace: "p-price-inf", PriceUSD: math.Inf(1), HealthScore: math.Inf(1)},
		{Marketplace: "p-health-inf", HealthScore: math.Inf(1)},
		{Marketplace: "p-health-neginf", HealthScore: math.Inf(-1)},
	}
	for _, p := range poisons {
		// Put poison first (the seize-prone slot).
		offers := []Offer{p, clean}
		got, err := m.RouteWorkload(context.Background(), Workload{ID: "w"}, offers, noLivePrice)
		if err != nil {
			t.Fatalf("[%s] poison=%s err: %v", runUUID, p.Marketplace, err)
		}
		if got.Winner != "clean" {
			t.Fatalf("[%s] poison=%s winner=%q score=%v, want clean (finite best)",
				runUUID, p.Marketplace, got.Winner, got.Score)
		}
		if math.IsNaN(got.Score) || math.IsInf(got.Score, 0) {
			t.Fatalf("[%s] poison=%s winning score=%v non-finite", runUUID, p.Marketplace, got.Score)
		}
	}
	t.Logf("[%s] clean finite offer beats every NaN/Inf-poisoned offer; winning score always finite", runUUID)
}
