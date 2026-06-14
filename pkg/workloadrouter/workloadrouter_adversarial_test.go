package workloadrouter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// noLivePrice forces the static-price fallback path (probe always errors).
func noLivePrice(_ context.Context, _ string) (float64, error) {
	return 0, errors.New("no live price")
}

// ---------------------------------------------------------------------------
// (a) COMPOSITE CORRECTNESS — winner decided by the weighted BLEND, not a
//     single dominant factor. Hand-computed expected winners.
// ---------------------------------------------------------------------------

// TestAdv_CompositeBlendWinner_NoSingleDominantFactor pins that the winner is
// the best weighted blend, where NO single offer dominates every factor.
//
// Weights Price=2, Latency=1, Health=3 (no TEE). invert(x)=1/(1+x):
//
//	"price-king"  p=0 lat=9  h=0.10 -> 2*1     + 1*0.1   + 3*0.10 = 2 + 0.1 + 0.3   = 2.40
//	"lat-king"    p=4 lat=0  h=0.20 -> 2*0.2   + 1*1     + 3*0.20 = 0.4 + 1 + 0.6   = 2.00
//	"blend"       p=1 lat=1  h=0.80 -> 2*0.5   + 1*0.5   + 3*0.80 = 1 + 0.5 + 2.4   = 3.90  <-- winner
//	"health-king" p=9 lat=9  h=1.00 -> 2*0.1   + 1*0.1   + 3*1.00 = 0.2 + 0.1 + 3   = 3.30
//
// "blend" wins though it is best at NOTHING individually: price-king has the
// lowest price, lat-king the lowest latency, health-king the highest health.
// This bites if any weight is dropped/mis-applied (the comparison flip mutation
// also fails here).
func TestAdv_CompositeBlendWinner_NoSingleDominantFactor(t *testing.T) {
	const runUUID = "adv-blend-9c1f"
	m := NewUnifiedManager(Weights{Price: 2, Latency: 1, Health: 3}, 2.0)
	offers := []Offer{
		{Marketplace: "price-king", PriceUSD: 0, LatencyMS: 9, HealthScore: 0.10},
		{Marketplace: "lat-king", PriceUSD: 4, LatencyMS: 0, HealthScore: 0.20},
		{Marketplace: "blend", PriceUSD: 1, LatencyMS: 1, HealthScore: 0.80},
		{Marketplace: "health-king", PriceUSD: 9, LatencyMS: 9, HealthScore: 1.00},
	}
	got, err := m.RouteWorkload(context.Background(), Workload{ID: "w"}, offers, noLivePrice)
	if err != nil {
		t.Fatalf("[%s] err: %v", runUUID, err)
	}
	if got.Winner != "blend" {
		t.Fatalf("[%s] winner = %q, want blend (composite 3.90 highest; no factor-king wins)", runUUID, got.Winner)
	}
	const wantScore = 3.90
	if diff := got.Score - wantScore; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("[%s] score = %v, want %v", runUUID, got.Score, wantScore)
	}
	t.Logf("[%s] composites price-king=2.40 lat-king=2.00 blend=3.90 health-king=3.30 -> winner=%s score=%.4f (best blend, dominant at nothing)",
		runUUID, got.Winner, got.Score)
}

// TestAdv_TEEMultiplier_MustNotFlip pins the NEGATIVE direction: when a non-TEE
// offer is already strictly ahead of the boosted TEE offer, the multiplier must
// NOT flip the winner. This catches a multiplier applied to the wrong side
// (boosting non-TEE) or an over-eager boost.
//
// Weights Price=1 only, TEEMult=2.0:
//
//	"cheap-plain" non-TEE p=0   -> 1/(1+0)   = 1.0000          (winner, no boost)
//	"tee-mid"     TEE     p=1   -> 1/(1+1)*2 = 0.5*2 = 1.0 ... TIE risk; use p=1.2:
//	  p=1.2 -> 1/(2.2)=0.45454; *2 = 0.90909 < 1.0
//
// Even with the *2 boost, tee-mid (0.909) < cheap-plain (1.0): plain must win.
func TestAdv_TEEMultiplier_MustNotFlip(t *testing.T) {
	const runUUID = "adv-tee-noflip-4b77"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 0, Health: 0}, 2.0)
	offers := []Offer{
		{Marketplace: "cheap-plain", PriceUSD: 0, TEECapable: false},
		{Marketplace: "tee-mid", PriceUSD: 1.2, TEECapable: true},
	}
	got, err := m.RouteWorkload(context.Background(), Workload{ID: "w", TEERequired: true}, offers, noLivePrice)
	if err != nil {
		t.Fatalf("[%s] err: %v", runUUID, err)
	}
	if got.Winner != "cheap-plain" {
		t.Fatalf("[%s] winner = %q, want cheap-plain (1.0 > boosted tee-mid 0.909); boost must not over-flip", runUUID, got.Winner)
	}
	const wantScore = 1.0
	if diff := got.Score - wantScore; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("[%s] score = %v, want %v", runUUID, got.Score, wantScore)
	}
	t.Logf("[%s] cheap-plain=1.0 vs boosted tee-mid=0.9091 -> winner=%s (multiplier correctly does NOT flip)", runUUID, got.Winner)
}

// TestAdv_TEEMultiplier_NotAppliedWhenNotRequired pins that the boost is gated
// on Workload.TEERequired: a TEE-capable offer is NOT boosted when the workload
// does not require a TEE.
func TestAdv_TEEMultiplier_NotAppliedWhenNotRequired(t *testing.T) {
	const runUUID = "adv-tee-gate-1d20"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 0, Health: 0}, 10.0)
	offers := []Offer{
		{Marketplace: "plain", PriceUSD: 1, TEECapable: false}, // 0.5
		{Marketplace: "tee", PriceUSD: 3, TEECapable: true},    // 0.25, would be 2.5 if wrongly boosted
	}
	got, err := m.RouteWorkload(context.Background(), Workload{ID: "w" /* TEERequired: false */}, offers, noLivePrice)
	if err != nil {
		t.Fatalf("[%s] err: %v", runUUID, err)
	}
	if got.Winner != "plain" {
		t.Fatalf("[%s] winner = %q, want plain (0.5); TEE offer must NOT be boosted when TEERequired=false", runUUID, got.Winner)
	}
	t.Logf("[%s] TEERequired=false -> tee NOT boosted; winner=%s score=%.4f", runUUID, got.Winner, got.Score)
}

// ---------------------------------------------------------------------------
// (b) DETERMINISTIC TIE-BREAK — equal composite scores resolve to the same
//     marketplace every call and regardless of input ordering.
// ---------------------------------------------------------------------------

// TestAdv_TieBreak_DeterministicByName builds an EXACT score tie (identical
// factors, different names) and asserts the lexicographically-smallest name
// wins, stably across input permutations and repeated runs. A map-iteration /
// unstable tie-break would fail this.
func TestAdv_TieBreak_DeterministicByName(t *testing.T) {
	const runUUID = "adv-tie-7e55"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 1, Health: 1}, 2.0)
	mk := func(name string) Offer {
		return Offer{Marketplace: name, PriceUSD: 2, LatencyMS: 3, HealthScore: 0.5}
	}
	// All identical scores: 1/3 + 1/4 + 0.5. Smallest name "aaa" must always win.
	perms := [][]Offer{
		{mk("ccc"), mk("aaa"), mk("bbb")},
		{mk("aaa"), mk("bbb"), mk("ccc")},
		{mk("bbb"), mk("ccc"), mk("aaa")},
		{mk("ccc"), mk("bbb"), mk("aaa")},
	}
	for pi, offers := range perms {
		for rep := 0; rep < 200; rep++ {
			got, err := m.RouteWorkload(context.Background(), Workload{ID: "w"}, offers, noLivePrice)
			if err != nil {
				t.Fatalf("[%s] perm=%d rep=%d err: %v", runUUID, pi, rep, err)
			}
			if got.Winner != "aaa" {
				t.Fatalf("[%s] perm=%d rep=%d winner = %q, want aaa (deterministic tie-break by name)", runUUID, pi, rep, got.Winner)
			}
		}
	}
	t.Logf("[%s] 4 input permutations x 200 reps: winner ALWAYS aaa (stable tie-break, not map-order)", runUUID)
}

// ---------------------------------------------------------------------------
// (c) CONCURRENCY CENTERPIECE — many goroutines call RouteWorkload while the
//     PriceFunc itself is hammered concurrently. Run under `go test -race`.
//     Asserts: no race, no panic, and the deterministic winner under a known
//     live-price mapping. The internal one-goroutine-per-offer fan-out writes
//     to distinct livePrices[i] slots; this drives that path hard from many
//     outer goroutines simultaneously.
// ---------------------------------------------------------------------------

func TestAdv_ConcurrentRoute_NoRaceNoPanic(t *testing.T) {
	const runUUID = "adv-conc-2a90"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 0, Health: 0}, 2.0)
	offers := []Offer{
		{Marketplace: "m0", PriceUSD: 100},
		{Marketplace: "m1", PriceUSD: 100},
		{Marketplace: "m2", PriceUSD: 100},
		{Marketplace: "m3", PriceUSD: 100},
	}
	// Live probe overrides: m2 is cheapest (price 0) -> must always win.
	var probeCalls int64
	live := func(_ context.Context, mk string) (float64, error) {
		atomic.AddInt64(&probeCalls, 1)
		switch mk {
		case "m2":
			return 0, nil // cheapest -> score 1.0
		case "m0":
			return 7, nil
		case "m1":
			return 5, nil
		case "m3":
			return 9, nil
		}
		return 0, errors.New("unknown")
	}

	const goroutines = 64
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errCh := make(chan string, goroutines*iters)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				d, err := m.RouteWorkload(context.Background(), Workload{ID: "w"}, offers, live)
				if err != nil {
					errCh <- "unexpected err: " + err.Error()
					return
				}
				if d.Winner != "m2" {
					errCh <- "wrong winner: " + d.Winner
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatalf("[%s] %s", runUUID, msg)
	}
	wantCalls := int64(goroutines * iters * len(offers))
	if n := atomic.LoadInt64(&probeCalls); n != wantCalls {
		t.Fatalf("[%s] probe calls = %d, want %d (once per offer per call)", runUUID, n, wantCalls)
	}
	t.Logf("[%s] %d goroutines x %d iters under -race: no race, no panic, winner always m2, probes=%d (exact)",
		runUUID, goroutines, iters, atomic.LoadInt64(&probeCalls))
}

// TestAdv_ConcurrentRoute_SharedOffersSliceReadOnly stresses that many
// concurrent RouteWorkload calls SHARE the same offers slice (read-only) and
// the same manager without corrupting each other's results. Each call uses a
// distinct workload requiring TEE; the TEE offer must win every time.
func TestAdv_ConcurrentRoute_SharedOffersSliceReadOnly(t *testing.T) {
	const runUUID = "adv-conc-shared-5f31"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 0, Health: 0}, 4.0)
	// Shared, never mutated by Route.
	offers := []Offer{
		{Marketplace: "plain", PriceUSD: 0.5, TEECapable: false}, // base 0.6667
		{Marketplace: "tee", PriceUSD: 1, TEECapable: true},      // base 0.5 -> *4 = 2.0 wins
	}
	const goroutines = 48
	const iters = 150
	var wg sync.WaitGroup
	wg.Add(goroutines)
	var bad int64
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				d, err := m.RouteWorkload(context.Background(), Workload{ID: "w", TEERequired: true}, offers, noLivePrice)
				if err != nil || d.Winner != "tee" {
					atomic.AddInt64(&bad, 1)
					return
				}
			}
		}()
	}
	wg.Wait()
	if n := atomic.LoadInt64(&bad); n != 0 {
		t.Fatalf("[%s] %d goroutines reported wrong/err result on shared offers slice", runUUID, n)
	}
	t.Logf("[%s] %d goroutines x %d iters share one offers slice + manager: tee always wins, no corruption", runUUID, goroutines, iters)
}

// ---------------------------------------------------------------------------
// (d) EDGES — single offer; empty offers reaffirmed; nil PriceFunc fallback.
// ---------------------------------------------------------------------------

// TestAdv_SingleOffer pins that a single offer is returned as the winner with
// its base composite score.
func TestAdv_SingleOffer(t *testing.T) {
	const runUUID = "adv-single-8c44"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 1, Health: 1}, 2.0)
	offers := []Offer{{Marketplace: "only", PriceUSD: 1, LatencyMS: 1, HealthScore: 0.5}}
	got, err := m.RouteWorkload(context.Background(), Workload{ID: "w"}, offers, noLivePrice)
	if err != nil {
		t.Fatalf("[%s] err: %v", runUUID, err)
	}
	if got.Winner != "only" {
		t.Fatalf("[%s] winner = %q, want only", runUUID, got.Winner)
	}
	const wantScore = 0.5 + 0.5 + 0.5 // 1/2 + 1/2 + 0.5
	if diff := got.Score - wantScore; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("[%s] score = %v, want %v", runUUID, got.Score, wantScore)
	}
	t.Logf("[%s] single offer -> winner=%s score=%.4f", runUUID, got.Winner, got.Score)
}

// TestAdv_NilPriceFunc pins that a nil PriceFunc falls back to static prices
// (no panic) and the static-cheapest wins.
func TestAdv_NilPriceFunc(t *testing.T) {
	const runUUID = "adv-nilfunc-3e09"
	m := NewUnifiedManager(Weights{Price: 1, Latency: 0, Health: 0}, 2.0)
	offers := []Offer{
		{Marketplace: "dear", PriceUSD: 9},
		{Marketplace: "cheap", PriceUSD: 1},
	}
	got, err := m.RouteWorkload(context.Background(), Workload{ID: "w"}, offers, nil)
	if err != nil {
		t.Fatalf("[%s] err: %v", runUUID, err)
	}
	if got.Winner != "cheap" {
		t.Fatalf("[%s] winner = %q, want cheap (nil PriceFunc -> static fallback)", runUUID, got.Winner)
	}
	t.Logf("[%s] nil PriceFunc -> static fallback, winner=%s", runUUID, got.Winner)
}
