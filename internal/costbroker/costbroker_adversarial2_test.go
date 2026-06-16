package costbroker

import (
	"math"
	"sync"
	"testing"
)

// ----------------------------------------------------------------------------
// (1) NaN-SCORE CAP/SELECTION BYPASS VIA UNVALIDATED LatencyMs  (REAL BUG, fixed)
//
// qualifies() validated PricePerHr (NaN/Inf rejected) but NOT LatencyMs, which
// also feeds effectiveCost via `score += LatencyWeight * LatencyMs`. A valid,
// finite, in-budget price combined with LatencyMs = NaN produces a NaN score
// while STILL passing qualifies().
//
// The min-search uses `bestIdx == -1 || score < bestScore`. If the NaN-score
// offer is the FIRST qualifying one it becomes the incumbent with bestScore=NaN;
// thereafter every `realScore < NaN` is false, so a genuinely cheaper offer can
// NEVER displace it. The broker then SELECTS the wrong offer and CHARGES its
// price — wrong selection + overcharge driven entirely by a hostile/malformed
// input field the gate never checked. This is the same defect class as the
// already-fixed NaN price, routed through the score instead of the cap.
//
// Mutation proof: remove the `IsNaN(o.LatencyMs)...` guard in qualifies and this
// test FAILS (poisonlat is selected over cheapgood, $9 charged, score NaN).
// With the guard the poison offer is excluded and cheapgood ($1) wins.
// ----------------------------------------------------------------------------
func TestAdv2_NaNLatencyScore_CannotPinSelectionOrOvercharge(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 10.0, ReliabilityWeight: 1.0, LatencyWeight: 0.01}
	// poisonlat is FIRST so it would set bestIdx with a NaN score; cheapgood is the
	// true winner ($1 vs $9). All prices are finite/in-budget so only the latency
	// field is hostile.
	offers := []Offer{
		{Provider: "poisonlat", PricePerHr: 9.0, Reliability: 1.0, LatencyMs: math.NaN(), Capacity: 8},
		{Provider: "cheapgood", PricePerHr: 1.0, Reliability: 1.0, LatencyMs: 1, Capacity: 8},
	}

	d := b.Select(req, offers)

	// SINK-SIDE 1: a NaN-latency offer must never be the selection.
	if d.Selected && d.Offer.Provider == "poisonlat" {
		t.Fatalf("NaN-latency offer pinned the min-search and was selected: %s (charged %v)", d, d.Charged)
	}
	// SINK-SIDE 2: the genuinely cheapest qualifying offer must win.
	if !d.Selected || d.Offer.Provider != "cheapgood" {
		t.Fatalf("expected cheapgood to win, got selected=%v provider=%q", d.Selected, d.Offer.Provider)
	}
	// SINK-SIDE 3: neither the score nor the running spend may be NaN.
	if math.IsNaN(d.Score) {
		t.Fatalf("decision score poisoned to NaN by unvalidated latency: %s", d)
	}
	if math.IsNaN(b.SpendSoFar()) {
		t.Fatalf("cumulative spend poisoned to NaN: %v", b.SpendSoFar())
	}
	// SINK-SIDE 4: exact charge is the honest $1, not the $9 of the poison offer.
	if math.Abs(b.SpendSoFar()-1.0) > 1e-9 {
		t.Fatalf("overcharge: spend %.6f, want 1.000000 (poison offer would charge 9.0)", b.SpendSoFar())
	}
}

// (1b) NaN-latency as the ONLY offer must be a clean rejection (not a NaN-score
// selection charging a finite price). Pins that the poison offer is excluded
// outright, not merely out-competed.
func TestAdv2_NaNLatency_OnlyOffer_Rejected(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 10.0, ReliabilityWeight: 1.0, LatencyWeight: 0.01}
	offers := []Offer{
		{Provider: "poisonlat", PricePerHr: 5.0, Reliability: 1.0, LatencyMs: math.NaN(), Capacity: 8},
	}
	d := b.Select(req, offers)
	if d.Selected {
		t.Fatalf("NaN-latency sole offer must be rejected, got selection %s (charged %v)", d, d.Charged)
	}
	if d.Reason != ReasonNoneQualify {
		t.Fatalf("expected reason %q, got %q", ReasonNoneQualify, d.Reason)
	}
	if got := b.SpendSoFar(); got != 0 {
		t.Fatalf("rejected poison offer accrued spend %v", got)
	}
}

// (1c) NaN Reliability also feeds effectiveCost (`relWeight*(1-rel)`); guard it
// the same way. A NaN reliability would otherwise sail past qualifies (the
// rel<=0 / rel>1 clamps in effectiveCost are false for NaN, leaving rel=NaN and
// score=NaN). Pins the reliability half of the same guard.
func TestAdv2_NaNReliability_Rejected(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 10.0, ReliabilityWeight: 1.0}
	offers := []Offer{
		{Provider: "poisonrel", PricePerHr: 9.0, Reliability: math.NaN(), LatencyMs: 1, Capacity: 8},
		{Provider: "good", PricePerHr: 1.0, Reliability: 1.0, LatencyMs: 1, Capacity: 8},
	}
	d := b.Select(req, offers)
	if d.Selected && d.Offer.Provider == "poisonrel" {
		t.Fatalf("NaN-reliability offer pinned selection: %s", d)
	}
	if !d.Selected || d.Offer.Provider != "good" {
		t.Fatalf("expected good to win, got selected=%v provider=%q", d.Selected, d.Offer.Provider)
	}
	if math.IsNaN(d.Score) || math.IsNaN(b.SpendSoFar()) {
		t.Fatalf("NaN reliability poisoned score/spend: score=%v spend=%v", d.Score, b.SpendSoFar())
	}
}

// (1d) +Inf LatencyMs: with the guard it is rejected (Inf is non-finite). Without
// the guard it produced score=+Inf, which compares correctly so it would merely
// lose — but it is still out-of-contract (must be >= 0 and finite). Pin rejection
// so the guard covers Inf too, matching the price guard's symmetry.
func TestAdv2_InfLatency_Rejected(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 10.0, ReliabilityWeight: 1.0, LatencyWeight: 0.01}
	d := b.Select(req, []Offer{
		{Provider: "infl", PricePerHr: 2.0, Reliability: 1.0, LatencyMs: math.Inf(1), Capacity: 8},
	})
	if d.Selected {
		t.Fatalf("+Inf latency sole offer must be rejected, got %s", d)
	}
	if got := b.SpendSoFar(); got != 0 {
		t.Fatalf("rejected +Inf-latency offer accrued spend %v", got)
	}
}

// (1e) Negative LatencyMs: documented "must be >= 0". A negative latency with a
// positive LatencyWeight produces a NEGATIVE surcharge, artificially lowering the
// score so a bad offer can undercut a good one. Pin that it is rejected.
func TestAdv2_NegativeLatency_Rejected(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 10.0, ReliabilityWeight: 1.0, LatencyWeight: 0.5}
	offers := []Offer{
		// Without the guard: score = 5.0 + 0.5*(-100) = -45, far below good's 1.5,
		// so the expensive offer wins via a fabricated negative surcharge.
		{Provider: "neglat", PricePerHr: 5.0, Reliability: 1.0, LatencyMs: -100, Capacity: 8},
		{Provider: "good", PricePerHr: 1.0, Reliability: 1.0, LatencyMs: 1, Capacity: 8},
	}
	d := b.Select(req, offers)
	if d.Selected && d.Offer.Provider == "neglat" {
		t.Fatalf("negative-latency offer won via fabricated negative surcharge: %s", d)
	}
	if !d.Selected || d.Offer.Provider != "good" {
		t.Fatalf("expected good to win, got selected=%v provider=%q", d.Selected, d.Offer.Provider)
	}
	if math.Abs(b.SpendSoFar()-1.0) > 1e-9 {
		t.Fatalf("wrong charge: spend %.6f, want 1.0", b.SpendSoFar())
	}
}

// ----------------------------------------------------------------------------
// (2) CONSERVATION under concurrent MIXED selections (charge vs reject).
//
// Half the goroutines submit a qualifying offer (charges price), half submit an
// over-budget offer (must reject, charge nothing). The final spend MUST equal
// exactly the count of qualifying calls * price — no lost updates, no phantom
// charges from the rejecting goroutines. One -race run guards the accumulator.
// ----------------------------------------------------------------------------
func TestAdv2_Conservation_MixedChargeReject_Race(t *testing.T) {
	const goroutines = 8 // even split: 4 charge, 4 reject
	const perG = 200
	const price = 0.25

	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 1.0, ReliabilityWeight: 1.0}
	good := []Offer{{Provider: "good", PricePerHr: price, Reliability: 1.0, Capacity: 4}}
	overBudget := []Offer{{Provider: "rich", PricePerHr: 5.0, Reliability: 1.0, Capacity: 4}}

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		charging := g%2 == 0
		go func(charging bool) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				if charging {
					if d := b.Select(req, good); !d.Selected {
						t.Errorf("expected charge, got %s", d)
						return
					}
				} else {
					if d := b.Select(req, overBudget); d.Selected {
						t.Errorf("over-budget offer must reject, got %s", d)
						return
					}
				}
			}
		}(charging)
	}
	wg.Wait()

	chargingGoroutines := (goroutines + 1) / 2 // g=0,2,4,6 -> 4
	wantTotal := float64(chargingGoroutines*perG) * price
	got := b.SpendSoFar()
	if math.Abs(got-wantTotal) > 1e-9 {
		t.Fatalf("conservation violated under mixed race: got %.6f, want %.6f (delta %.6f)",
			got, wantTotal, got-wantTotal)
	}
}

// ----------------------------------------------------------------------------
// (3) NON-CONSERVATION SWEEP: across a batch of independent single-offer Selects,
// the cumulative SpendSoFar must equal the exact arithmetic sum of every charged
// price (total charged == sum of charges). Catches any rounding/accumulation
// defect that loses or invents money over many small charges.
// ----------------------------------------------------------------------------
func TestAdv2_Conservation_SumOfCharges(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 100.0, ReliabilityWeight: 1.0}
	prices := []float64{0.01, 0.07, 1.23, 9.99, 0.005, 42.0, 0.333, 7.77}

	var want float64
	for i, p := range prices {
		d := b.Select(req, []Offer{{Provider: "p", PricePerHr: p, Reliability: 1.0, Capacity: 4}})
		if !d.Selected {
			t.Fatalf("offer %d (price %v) should qualify, got %s", i, p, d)
		}
		want += p
		// per-step charge must equal exactly this offer's price.
		if math.Abs(d.Charged-p) != 0 {
			t.Fatalf("step %d charged %v, want exactly %v", i, d.Charged, p)
		}
	}
	if got := b.SpendSoFar(); math.Abs(got-want) > 1e-9 {
		t.Fatalf("non-conservation: spend %.10f != sum of charges %.10f", got, want)
	}
}

// ----------------------------------------------------------------------------
// (4) CAP ENFORCED EXACTLY at the boundary: an offer priced exactly AT MaxPricePer
// is admitted (cap is documented inclusive), one a cent above is rejected. Pins
// the off-by-one direction of the `o.PricePerHr > req.MaxPricePer` comparison.
// ----------------------------------------------------------------------------
func TestAdv2_CapBoundaryInclusive(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 1.00, ReliabilityWeight: 1.0}

	// Exactly at cap -> admitted.
	atCap := b.Select(req, []Offer{{Provider: "atcap", PricePerHr: 1.00, Reliability: 1.0, Capacity: 4}})
	if !atCap.Selected || atCap.Offer.Provider != "atcap" {
		t.Fatalf("price exactly at inclusive cap must be admitted, got %s", atCap)
	}

	// Just above cap (only offer) -> rejected.
	over := b.Select(req, []Offer{{Provider: "over", PricePerHr: 1.0000001, Reliability: 1.0, Capacity: 4}})
	if over.Selected {
		t.Fatalf("price above cap must be rejected, got %s", over)
	}
}
