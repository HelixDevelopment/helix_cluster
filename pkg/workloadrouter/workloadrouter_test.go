package workloadrouter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// staticPrice returns offer.PriceUSD unchanged (no live override).
func staticPrice(_ context.Context, _ string) (float64, error) {
	return 0, errors.New("no live price")
}

// TestRoute_NoOffers proves the empty-offers contract.
func TestRoute_NoOffers(t *testing.T) {
	const runUUID = "run-7d2a1c44-no-offers"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 1, Health: 1}, 2.0)
	_, err := m.RouteWorkload(context.Background(), Workload{ID: "w"}, nil, staticPrice)
	if !errors.Is(err, ErrNoOffers) {
		t.Fatalf("[%s] expected ErrNoOffers, got %v", runUUID, err)
	}
	t.Logf("[%s] before=0 offers -> after=ErrNoOffers (delta: empty slice => sentinel error)", runUUID)
}

// TestRoute_TEEMultiplierFlipsWinner is the LOAD-BEARING GUARD for the TEE
// multiplier. Weights isolate price only (Price=1, others 0), TEEMult=2.0.
//
//	mkt "alpha" non-TEE, price=1  -> base = 1/(1+1)       = 0.5
//	mkt "zulu"  TEE,     price=2  -> base = 1/(1+2)       = 0.3333...
//	mkt "mid"   non-TEE, price=3  -> base = 1/(1+3)       = 0.25
//
// Without TEE boost: alpha (0.5) wins.
// With TEERequired + *2.0: zulu = 0.3333*2 = 0.6667 > alpha 0.5 -> zulu wins.
// Dropping the `s *= m.TEEMultiplier` line makes zulu stay at 0.3333 and alpha
// wins, FAILING this test.
func TestRoute_TEEMultiplierFlipsWinner(t *testing.T) {
	const runUUID = "run-3f88b001-tee-flip"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 0, Health: 0}, 2.0)
	offers := []Offer{
		{Marketplace: "alpha", PriceUSD: 1, TEECapable: false},
		{Marketplace: "zulu", PriceUSD: 2, TEECapable: true},
		{Marketplace: "mid", PriceUSD: 3, TEECapable: false},
	}

	// Baseline: no TEE required -> cheapest non-TEE (alpha) wins.
	baseline, err := m.RouteWorkload(context.Background(), Workload{ID: "w-base"}, offers, staticPrice)
	if err != nil {
		t.Fatalf("[%s] baseline err: %v", runUUID, err)
	}
	if baseline.Winner != "alpha" {
		t.Fatalf("[%s] baseline winner = %q, want alpha (0.5 highest)", runUUID, baseline.Winner)
	}

	// TEE required -> zulu boosted to 0.6667 and wins.
	got, err := m.RouteWorkload(context.Background(), Workload{ID: "w-tee", TEERequired: true}, offers, staticPrice)
	if err != nil {
		t.Fatalf("[%s] tee err: %v", runUUID, err)
	}
	if got.Winner != "zulu" {
		t.Fatalf("[%s] tee winner = %q, want zulu (boosted 0.6667 > alpha 0.5)", runUUID, got.Winner)
	}
	const wantScore = 2.0 / 3.0 // 0.3333... * 2.0
	if diff := got.Score - wantScore; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("[%s] tee score = %v, want %v", runUUID, got.Score, wantScore)
	}
	t.Logf("[%s] before(no-TEE winner=%s score~0.5) -> after(TEE winner=%s score=%.4f); delta proves TEEMultiplier flips winner alpha->zulu",
		runUUID, baseline.Winner, got.Winner, got.Score)
}

// TestRoute_WeightedCompositeWinner is the LOAD-BEARING GUARD for a composite
// weight. Weights Price=1, Latency=1, Health=1 (no TEE). Hand-computed:
//
//	"cheap"  price=3 lat=9 health=0.10 -> 1/4 + 1/10 + 0.10 = 0.25+0.10+0.10 = 0.45
//	"fast"   price=9 lat=0 health=0.20 -> 1/10 + 1/1 + 0.20 = 0.10+1.00+0.20 = 1.30
//	"healthy"price=1 lat=4 health=0.90 -> 1/2 + 1/5 + 0.90 = 0.50+0.20+0.90 = 1.60
//
// Winner = "healthy" (1.60). If the Health weight is inverted/zeroed (mutation),
// healthy collapses to 0.70 and "fast" (1.10 without health term) wins instead,
// FAILING this test.
func TestRoute_WeightedCompositeWinner(t *testing.T) {
	const runUUID = "run-aa19e7c2-weighted"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 1, Health: 1}, 2.0)
	offers := []Offer{
		{Marketplace: "cheap", PriceUSD: 3, LatencyMS: 9, HealthScore: 0.10},
		{Marketplace: "fast", PriceUSD: 9, LatencyMS: 0, HealthScore: 0.20},
		{Marketplace: "healthy", PriceUSD: 1, LatencyMS: 4, HealthScore: 0.90},
	}
	got, err := m.RouteWorkload(context.Background(), Workload{ID: "w-comp"}, offers, staticPrice)
	if err != nil {
		t.Fatalf("[%s] err: %v", runUUID, err)
	}
	if got.Winner != "healthy" {
		t.Fatalf("[%s] winner = %q, want healthy (composite 1.60 highest)", runUUID, got.Winner)
	}
	const wantScore = 1.60
	if diff := got.Score - wantScore; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("[%s] score = %v, want %v", runUUID, got.Score, wantScore)
	}
	t.Logf("[%s] before(per-offer composites cheap=0.45 fast=1.30 healthy=1.60) -> after(winner=%s score=%.4f); delta proves Health weight is load-bearing",
		runUUID, got.Winner, got.Score)
}

// TestRoute_ConcurrentPriceProbe proves the PriceFunc is invoked exactly once
// per offer and that its returned live price OVERRIDES the static Offer.PriceUSD.
//
// Static prices would make "static-cheap" win (price 1 vs 5). The live probe
// flips prices so "static-cheap" becomes expensive (10) and "static-dear"
// becomes cheap (1): with Price-only weights the winner becomes "static-dear",
// proving the override took effect. We also count probe invocations.
func TestRoute_ConcurrentPriceProbe(t *testing.T) {
	const runUUID = "run-c4d5e6f7-probe"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 0, Health: 0}, 2.0)
	offers := []Offer{
		{Marketplace: "static-cheap", PriceUSD: 1},
		{Marketplace: "static-dear", PriceUSD: 5},
	}

	var calls int64
	live := func(_ context.Context, mk string) (float64, error) {
		atomic.AddInt64(&calls, 1)
		switch mk {
		case "static-cheap":
			return 10, nil // override: now expensive
		case "static-dear":
			return 1, nil // override: now cheap
		}
		return 0, errors.New("unknown marketplace")
	}

	got, err := m.RouteWorkload(context.Background(), Workload{ID: "w-probe"}, offers, live)
	if err != nil {
		t.Fatalf("[%s] err: %v", runUUID, err)
	}
	if n := atomic.LoadInt64(&calls); n != int64(len(offers)) {
		t.Fatalf("[%s] probe invoked %d times, want once per offer (%d)", runUUID, n, len(offers))
	}
	if got.Winner != "static-dear" {
		t.Fatalf("[%s] winner = %q, want static-dear (live price 1 beats overridden static-cheap@10)", runUUID, got.Winner)
	}
	// static-dear live price 1 -> score 1/(1+1) = 0.5
	const wantScore = 0.5
	if diff := got.Score - wantScore; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("[%s] score = %v, want %v", runUUID, got.Score, wantScore)
	}
	t.Logf("[%s] before(static winner would be static-cheap@1) -> after(probe overrode prices, winner=%s score=%.4f, probes=%d); delta proves PriceFunc override + once-per-offer",
		runUUID, got.Winner, got.Score, atomic.LoadInt64(&calls))
}
