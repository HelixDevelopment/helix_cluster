package costbroker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSelect_CheapestAdequateChosen asserts the EXACT provider selected among
// three budget-feasible, capacity-adequate offers: the one with the lowest
// effective cost.
//
// Mutation that kills this: in effectiveCost, drop the reliability penalty
// (return o.PricePerHr only). Then "cheapraw" (price 1.00, reliability 0.50)
// would win over "balanced" (price 1.10, reliability 0.99) because raw price
// ignores that cheapraw's unreliability makes it effectively more expensive
// (1.00*(1+0.5)=1.50 vs 1.10*(1+0.01)=1.111). The assertion on "balanced" fails.
func TestSelect_CheapestAdequateChosen(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 4, MaxPricePer: 2.00, ReliabilityWeight: 1.0}
	offers := []Offer{
		{Provider: "cheapraw", PricePerHr: 1.00, Reliability: 0.50, LatencyMs: 10, Capacity: 8},
		{Provider: "balanced", PricePerHr: 1.10, Reliability: 0.99, LatencyMs: 10, Capacity: 8},
		{Provider: "premium", PricePerHr: 1.80, Reliability: 1.00, LatencyMs: 5, Capacity: 16},
	}

	d := b.Select(req, offers)

	require.True(t, d.Selected, "expected a selection, got %s", d)
	assert.Equal(t, "balanced", d.Offer.Provider)
	// effective cost of balanced = 1.10 * (1 + 1.0*(1-0.99)) = 1.10*1.01 = 1.111
	assert.InDelta(t, 1.111, d.Score, 1e-9)
}

// TestSelect_OverBudgetExcluded is the primary anti-bluff test. The cheapest and
// most reliable offer is OVER the $/hr budget and MUST be excluded even though it
// would otherwise score best.
//
// Mutation that kills this: remove the budget check in qualifies (delete the
// `if req.MaxPricePer > 0 && o.PricePerHr > req.MaxPricePer { return false }`
// block). A broker that ignores the budget cap selects "overbudget" (price 0.50,
// the best score) and the assertion on "withinbudget" fails.
func TestSelect_OverBudgetExcluded(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 2, MaxPricePer: 1.00, ReliabilityWeight: 1.0}
	offers := []Offer{
		{Provider: "overbudget", PricePerHr: 0.50, Reliability: 1.00, LatencyMs: 1, Capacity: 8}, // best score but > budget? no — under price
		{Provider: "withinbudget", PricePerHr: 0.90, Reliability: 1.00, LatencyMs: 1, Capacity: 8},
		{Provider: "waytoorich", PricePerHr: 5.00, Reliability: 1.00, LatencyMs: 1, Capacity: 8},
	}
	// Make "overbudget" genuinely over budget by raising its price above the cap
	// while keeping it otherwise dominant (perfect reliability, low latency).
	offers[0].PricePerHr = 1.50

	d := b.Select(req, offers)

	require.True(t, d.Selected, "expected a selection, got %s", d)
	assert.Equal(t, "withinbudget", d.Offer.Provider,
		"over-budget offer must be excluded; got %s", d.Offer.Provider)
	assert.LessOrEqual(t, d.Offer.PricePerHr, req.MaxPricePer)
}

// TestSelect_UnderCapacityExcluded asserts an offer below the capacity floor is
// excluded even when it is the cheapest by effective cost.
//
// Mutation that kills this: remove the capacity check in qualifies (delete
// `if o.Capacity < req.MinCapacity { return false }`). Then "tiny" (capacity 1,
// cheapest) is selected and the assertion on "bigenough" fails.
func TestSelect_UnderCapacityExcluded(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 8, MaxPricePer: 2.00, ReliabilityWeight: 1.0}
	offers := []Offer{
		{Provider: "tiny", PricePerHr: 0.20, Reliability: 1.00, LatencyMs: 1, Capacity: 1},
		{Provider: "bigenough", PricePerHr: 1.00, Reliability: 1.00, LatencyMs: 1, Capacity: 8},
	}

	d := b.Select(req, offers)

	require.True(t, d.Selected, "expected a selection, got %s", d)
	assert.Equal(t, "bigenough", d.Offer.Provider)
	assert.GreaterOrEqual(t, d.Offer.Capacity, req.MinCapacity)
}

// TestSelect_NoneQualifyRejected asserts that when every offer is either over
// budget or under capacity, the decision is a rejection with the correct reason
// and spend is untouched.
//
// Mutation that kills this: in Select, change `if bestIdx == -1` to `if false`
// (or fabricate a selection). Then a Decision with Selected=true is returned for
// an empty qualifying set and require.False fails. Also the spend assertion
// catches a broker that charges on rejection.
func TestSelect_NoneQualifyRejected(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 100, MaxPricePer: 0.10, ReliabilityWeight: 1.0}
	offers := []Offer{
		{Provider: "tooexpensive", PricePerHr: 5.00, Reliability: 1.00, Capacity: 200},
		{Provider: "toosmall", PricePerHr: 0.05, Reliability: 1.00, Capacity: 2},
	}

	d := b.Select(req, offers)

	require.False(t, d.Selected, "expected rejection, got %s", d)
	assert.Equal(t, ReasonNoneQualify, d.Reason)
	assert.Equal(t, 0.0, b.SpendSoFar(), "rejection must not accrue spend")
}

// TestSelect_NoOffersRejected asserts the empty-input rejection reason.
//
// Mutation that kills this: in Select, change the `len(offers) == 0` guard to
// `len(offers) < 0`. The empty slice then falls through to ReasonNoneQualify and
// this exact-reason assertion fails.
func TestSelect_NoOffersRejected(t *testing.T) {
	b := NewComputeBroker()
	d := b.Select(Requirement{MinCapacity: 1}, nil)
	require.False(t, d.Selected)
	assert.Equal(t, ReasonNoOffers, d.Reason)
}

// TestSpendSoFar_Accumulates asserts the EXACT cumulative total across multiple
// Select calls: 1.10 + 0.90 = 2.00.
//
// Mutation that kills this: in Select, replace `b.spend += chosen.PricePerHr`
// with `b.spend = chosen.PricePerHr`. The total then becomes 0.90 (last only)
// instead of 2.00 and the exact-total assertion fails.
func TestSpendSoFar_Accumulates(t *testing.T) {
	b := NewComputeBroker()

	d1 := b.Select(
		Requirement{MinCapacity: 4, MaxPricePer: 2.0, ReliabilityWeight: 1.0},
		[]Offer{{Provider: "a", PricePerHr: 1.10, Reliability: 0.99, Capacity: 8}},
	)
	require.True(t, d1.Selected)
	assert.InDelta(t, 1.10, b.SpendSoFar(), 1e-9)

	d2 := b.Select(
		Requirement{MinCapacity: 4, MaxPricePer: 2.0, ReliabilityWeight: 1.0},
		[]Offer{{Provider: "b", PricePerHr: 0.90, Reliability: 0.99, Capacity: 8}},
	)
	require.True(t, d2.Selected)

	// Exact cumulative total across both selections.
	assert.InDelta(t, 2.00, b.SpendSoFar(), 1e-9)
}

// TestSelect_LatencyWeightAffectsChoice asserts latency surcharge can flip the
// selection between two equal-price, equal-reliability offers.
//
// Mutation that kills this: in effectiveCost, drop the latency term (remove
// `score += req.LatencyWeight * o.LatencyMs`). Then both offers score equally and
// the first ("slow") is kept, so the assertion on "fast" fails.
func TestSelect_LatencyWeightAffectsChoice(t *testing.T) {
	b := NewComputeBroker()
	req := Requirement{MinCapacity: 1, MaxPricePer: 2.0, LatencyWeight: 0.01}
	offers := []Offer{
		{Provider: "slow", PricePerHr: 1.00, Reliability: 1.00, LatencyMs: 100, Capacity: 4},
		{Provider: "fast", PricePerHr: 1.00, Reliability: 1.00, LatencyMs: 5, Capacity: 4},
	}

	d := b.Select(req, offers)

	require.True(t, d.Selected)
	assert.Equal(t, "fast", d.Offer.Provider)
	// fast score = 1.00 + 0.01*5 = 1.05
	assert.InDelta(t, 1.05, d.Score, 1e-9)
}

// TestFetchOffers_LocalHTTPThenSelect exercises the real HTTP provider seam
// against a LOCAL httptest server, then runs a real Select on the fetched
// offers and asserts the exact chosen provider.
//
// Mutation that kills this: in FetchOffers, ignore the status code (remove the
// `resp.StatusCode != http.StatusOK` check) — covered by the error subtest; or
// in Select, ignore budget — the chosen-provider assertion below ("mid") fails
// because the over-budget "cheap-but-rich" offer would win.
func TestFetchOffers_LocalHTTPThenSelect(t *testing.T) {
	served := []Offer{
		{Provider: "rich", PricePerHr: 3.00, Reliability: 1.00, LatencyMs: 1, Capacity: 16},
		{Provider: "mid", PricePerHr: 0.80, Reliability: 0.99, LatencyMs: 2, Capacity: 16},
		{Provider: "tiny", PricePerHr: 0.10, Reliability: 1.00, LatencyMs: 1, Capacity: 1},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(served)
	}))
	defer srv.Close()

	offers, err := FetchOffers(context.Background(), srv.Client(), srv.URL)
	require.NoError(t, err)
	require.Len(t, offers, 3)
	assert.Equal(t, "rich", offers[0].Provider) // proves real decode, not a stub

	b := NewComputeBroker()
	d := b.Select(Requirement{MinCapacity: 8, MaxPricePer: 1.00, ReliabilityWeight: 1.0}, offers)
	require.True(t, d.Selected, "expected a selection, got %s", d)
	// "rich" is over budget, "tiny" is under capacity; only "mid" qualifies.
	assert.Equal(t, "mid", d.Offer.Provider)
	assert.InDelta(t, 0.80, b.SpendSoFar(), 1e-9)
}

// TestFetchOffers_NonOKStatusErrors asserts the seam surfaces a non-200 as an
// error rather than returning an empty offer set.
//
// Mutation that kills this: remove the status-code check in FetchOffers. A 500
// body that fails to decode would still error here, but a 500 with an empty JSON
// array `[]` would return (nil-error, empty) — so we serve `[]` with status 500
// to specifically catch the missing status guard.
func TestFetchOffers_NonOKStatusErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	_, err := FetchOffers(context.Background(), srv.Client(), srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 500")
}
