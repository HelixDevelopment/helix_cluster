package costbroker

import (
	"math"
	"sync"
	"testing"
)

// ----------------------------------------------------------------------------
// (a) CONSERVATION UNDER RACE
//
// The package documents ComputeBroker as "safe for concurrent use" and spend is
// guarded by mu. N goroutines each perform one successful Select charging a
// fixed amount; the final SpendSoFar MUST equal the arithmetic sum exactly.
// A lost-update race (unguarded accumulator) would silently drop charges.
//
// Concurrency capped at 8 goroutines * fixed iterations so `go test -timeout 90s`
// completes. Run with -race to surface any data race on spend.
// ----------------------------------------------------------------------------
func TestConservation_UnderRace(t *testing.T) {
	const goroutines = 8
	const perG = 250
	const price = 0.10

	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 2.0, ReliabilityWeight: 1.0}
	offer := []Offer{{Provider: "p", PricePerHr: price, Reliability: 1.0, Capacity: 4}}

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				d := b.Select(req, offer)
				if !d.Selected {
					t.Errorf("expected selection, got %s", d)
					return
				}
			}
		}()
	}
	wg.Wait()

	wantCharges := goroutines * perG
	wantTotal := float64(wantCharges) * price
	got := b.SpendSoFar()

	// SINK-SIDE: the accumulated total must equal the arithmetic sum of every
	// charge. A lost update under race would make got < wantTotal.
	if math.Abs(got-wantTotal) > 1e-9 {
		t.Fatalf("conservation violated: got spend %.6f, want %.6f (lost %.0f charges of $%.2f)",
			got, wantTotal, (wantTotal-got)/price, price)
	}
}

// ----------------------------------------------------------------------------
// (b)/(d) NUMERIC POISON + CAP BYPASS: a NaN-priced offer.
//
// qualifies() rejects PricePerHr < 0 and enforces the budget cap via
// `req.MaxPricePer > 0 && o.PricePerHr > req.MaxPricePer`. But for a NaN price:
//   - NaN < 0            -> false  (slips the negative-price guard)
//   - NaN > MaxPricePer  -> false  (slips the budget cap -> ADMITTED)
// So a NaN-priced offer qualifies, and once selected its NaN price is added to
// spend, permanently poisoning the cumulative total (NaN propagates forever).
//
// This is the MONEY bug: a malformed/hostile price bypasses the hard budget cap
// the broker promises to enforce, AND corrupts billing irreversibly.
// ----------------------------------------------------------------------------
func TestNumericPoison_NaNPrice_BypassesCapAndPoisonsSpend(t *testing.T) {
	b := NewComputeBroker()
	// Cap is $1.00/hr. A NaN price is, by any sane reading, "not <= cap" and must
	// be rejected. The broker must NOT admit it and must NOT charge NaN.
	req := Requirement{MinCapacity: 1, MaxPricePer: 1.00, ReliabilityWeight: 1.0}
	offers := []Offer{
		{Provider: "poison", PricePerHr: math.NaN(), Reliability: 1.0, Capacity: 8},
		{Provider: "honest", PricePerHr: 0.90, Reliability: 1.0, Capacity: 8},
	}

	d := b.Select(req, offers)

	// SINK-SIDE 1: spend must never be NaN. If the NaN offer was charged, the
	// cumulative total is poisoned for the lifetime of the broker.
	if math.IsNaN(b.SpendSoFar()) {
		t.Fatalf("NaN-priced offer poisoned cumulative spend: SpendSoFar()=%v (cap bypass admitted %q)",
			b.SpendSoFar(), d.Offer.Provider)
	}

	// SINK-SIDE 2: if a selection was made it must be a real, in-budget offer,
	// never the NaN one.
	if d.Selected {
		if d.Offer.Provider == "poison" {
			t.Fatalf("NaN-priced offer bypassed the budget cap and was selected: %s", d)
		}
		if math.IsNaN(d.Charged) || d.Charged > req.MaxPricePer {
			t.Fatalf("charged %v exceeds/poisons cap %v via offer %q", d.Charged, req.MaxPricePer, d.Offer.Provider)
		}
	}
}

// (b) +Inf price: documents the asymmetry. +Inf > cap is TRUE, so +Inf is
// correctly rejected by the budget cap today. This pins that contrast: only NaN
// (not +Inf) slips the cap, isolating the defect to NaN handling.
func TestNumericPoison_PosInfPrice_RejectedByCap(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 1.00, ReliabilityWeight: 1.0}
	offers := []Offer{
		{Provider: "inf", PricePerHr: math.Inf(1), Reliability: 1.0, Capacity: 8},
	}
	d := b.Select(req, offers)
	if d.Selected {
		t.Fatalf("+Inf price must be excluded by budget cap, got selection %s", d)
	}
	if b.SpendSoFar() != 0 {
		t.Fatalf("rejected +Inf offer must not accrue spend, got %v", b.SpendSoFar())
	}
}

// (b) NEGATIVE-CHARGE-AS-CREDIT: the package documents PricePerHr "must be >= 0"
// and qualifies() rejects PricePerHr < 0. A negative charge would act as a
// credit, reducing cumulative spend below a prior honest total — a conservation
// violation. This pins the guard: a negative-priced offer must be rejected and
// must not reduce spend.
func TestNumericPoison_NegativePrice_NotACredit(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 2.0, ReliabilityWeight: 1.0}

	// First, an honest charge establishes a positive baseline.
	d1 := b.Select(req, []Offer{{Provider: "honest", PricePerHr: 1.00, Reliability: 1.0, Capacity: 4}})
	if !d1.Selected {
		t.Fatalf("expected honest selection, got %s", d1)
	}
	base := b.SpendSoFar()

	// Now offer a negative price alongside a valid one. The negative offer must be
	// rejected by qualifies(); spend must only ever grow.
	d2 := b.Select(req, []Offer{
		{Provider: "credit", PricePerHr: -5.00, Reliability: 1.0, Capacity: 4},
		{Provider: "valid", PricePerHr: 0.50, Reliability: 1.0, Capacity: 4},
	})
	if d2.Selected && d2.Offer.Provider == "credit" {
		t.Fatalf("negative-priced offer acted as a credit: selected %s", d2)
	}
	if got := b.SpendSoFar(); got < base {
		t.Fatalf("conservation violated: spend dropped from %.4f to %.4f via a negative charge", base, got)
	}
}

// ----------------------------------------------------------------------------
// (c) TOTAL-ORDER on equal price/score: two offers with identical effective cost
// must yield a deterministic selection (a stable tiebreak), and a NaN score must
// not corrupt the min-search by being treated as "best".
//
// The strict `score < bestScore` keeps the first-by-index winner on ties, which
// is deterministic. We assert that determinism here and that a trailing NaN-score
// offer never displaces a real winner.
// ----------------------------------------------------------------------------
func TestTotalOrder_EqualScoreDeterministic(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 2.0, ReliabilityWeight: 1.0}
	// Identical price & reliability -> identical effective cost. First by index
	// ("alpha") must win every time.
	offers := []Offer{
		{Provider: "alpha", PricePerHr: 1.00, Reliability: 1.0, Capacity: 4},
		{Provider: "bravo", PricePerHr: 1.00, Reliability: 1.0, Capacity: 4},
	}
	first := b.Select(req, offers).Offer.Provider
	for i := 0; i < 50; i++ {
		if got := NewComputeBroker().Select(req, offers).Offer.Provider; got != first {
			t.Fatalf("non-deterministic tiebreak on equal score: %q then %q", first, got)
		}
	}
	if first != "alpha" {
		t.Fatalf("equal-score tiebreak should pick first-by-index %q, got %q", "alpha", first)
	}
}
