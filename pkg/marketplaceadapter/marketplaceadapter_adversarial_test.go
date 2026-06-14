package marketplaceadapter

// marketplaceadapter_adversarial_test.go — adversarial, mutation-verified
// contract pins for the marketplace offer/order translation layer.
//
// Centerpieces:
//   - FIELD FIDELITY: a fully-populated external offer with DISTINCT values in
//     every field is translated and every field is asserted against a hand-built
//     expected, so a field swap / drop / mis-map is detectable.
//   - PRICE / UNIT: the AKT->USD scale conversion and the JSON price round-trip
//     are pinned at boundary and fractional values; a dropped multiplier or a
//     cents/dollars mis-scale would bite.
//
// These tests are additive and touch only this file.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ----------------------------------------------------------------------------
// CHUTES: GetCurrentPricing field-fidelity centerpiece.
//
// The fixture returns an Offer JSON whose gpu_model is DELIBERATELY DIFFERENT
// from the requested gpu and whose marketplace is a DISTINCT poison value. This
// pins the documented contract precisely:
//   - PriceUSD comes from the decoded body (price_usd).
//   - GPUModel comes from the decoded BODY (gpu_model), NOT re-derived from the
//     request argument.
//   - Marketplace is always STAMPED "chutes", clobbering any server value.
//
// Distinct values mean a price<->anything swap, a gpu_model drop, or a failure
// to stamp marketplace are all individually observable.
// ----------------------------------------------------------------------------
func TestChutes_GetCurrentPricing_FieldFidelity(t *testing.T) {
	const runUUID = "adv-chutes-fidelity-0001"
	const (
		wantPrice    = 123.456                    // distinct, fractional
		serverGPU    = "SERVER-RETURNED-B200"     // distinct from request arg
		requestGPU   = "REQUESTED-A100"           // what we ask for
		poisonMarket = "POISON-MARKET-NOT-CHUTES" // must be clobbered to "chutes"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo a FULLY populated Offer with a poison marketplace and a gpu_model
		// distinct from the request, so each field's provenance is provable.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"marketplace": poisonMarket,
			"price_usd":   wantPrice,
			"gpu_model":   serverGPU,
		})
	}))
	t.Cleanup(srv.Close)

	c := NewChutesAdapter(srv.URL)
	got, err := c.GetCurrentPricing(context.Background(), requestGPU)
	if err != nil {
		t.Fatalf("[%s] GetCurrentPricing error: %v", runUUID, err)
	}

	want := Offer{
		Marketplace: "chutes",  // always stamped, poison clobbered
		PriceUSD:    wantPrice, // from body
		GPUModel:    serverGPU, // from body, NOT the request arg
	}
	if got != want {
		t.Fatalf("[%s] field fidelity broken:\n got = %+v\nwant = %+v\n"+
			"(if GPUModel==%q the body field was dropped/overwritten by the request arg; "+
			"if Marketplace==%q the stamp is dead)",
			runUUID, got, want, requestGPU, poisonMarket)
	}
	t.Logf("[%s] before: body{marketplace:%q price_usd:%v gpu_model:%q}, req gpu=%q -> after: %+v",
		runUUID, poisonMarket, wantPrice, serverGPU, requestGPU, got)
}

// TestChutes_GetCurrentPricing_PriceFractionalFidelity pins that the price float
// survives the JSON round-trip with no precision loss / no scale change at a
// value where a cents<->dollars (x100) mis-scale would be glaring.
func TestChutes_GetCurrentPricing_PriceFractionalFidelity(t *testing.T) {
	const runUUID = "adv-chutes-price-0002"
	cases := []float64{0, 0.01, 0.07, 1.005, 9999999.99, math.MaxFloat64}

	for _, want := range cases {
		want := want
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"price_usd": want,
				"gpu_model": "A100",
			})
		}))
		c := NewChutesAdapter(srv.URL)
		got, err := c.GetCurrentPricing(context.Background(), "A100")
		srv.Close()
		if err != nil {
			t.Fatalf("[%s] price=%v: error %v", runUUID, want, err)
		}
		if got.PriceUSD != want {
			t.Fatalf("[%s] price mis-scaled/lost: got %v want %v (x100 would be %v)",
				runUUID, got.PriceUSD, want, want*100)
		}
	}
	t.Logf("[%s] all %d price boundary/fractional values round-tripped exactly", runUUID, len(cases))
}

// ----------------------------------------------------------------------------
// CHUTES: SubmitWork order/round-trip fidelity.
//
// The fixture echoes back the EXACT WorkSpec it received (decoded) as the job
// id, so we prove the spec was serialized correctly on the wire (ID + GPUModel
// both present and not swapped) and the returned WorkResult is built from the
// server's job_id, with Marketplace stamped.
// ----------------------------------------------------------------------------
func TestChutes_SubmitWork_SpecRoundTripFidelity(t *testing.T) {
	const runUUID = "adv-chutes-submit-0003"

	var seen WorkSpec
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// job_id encodes BOTH fields so a swap on the wire is detectable.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"job_id": "id=" + seen.ID + ";gpu=" + seen.GPUModel,
		})
	}))
	t.Cleanup(srv.Close)

	c := NewChutesAdapter(srv.URL)
	spec := WorkSpec{ID: "DISTINCT-ID-42", GPUModel: "DISTINCT-GPU-H200"}
	got, err := c.SubmitWork(context.Background(), spec)
	if err != nil {
		t.Fatalf("[%s] SubmitWork error: %v", runUUID, err)
	}

	// Wire fidelity: the server saw exactly the spec we sent, no swap.
	if seen != spec {
		t.Fatalf("[%s] wire spec corrupted: server saw %+v want %+v", runUUID, seen, spec)
	}
	want := WorkResult{
		Marketplace: "chutes",
		JobID:       "id=DISTINCT-ID-42;gpu=DISTINCT-GPU-H200",
		Accepted:    true,
	}
	if got != want {
		t.Fatalf("[%s] WorkResult fidelity broken:\n got = %+v\nwant = %+v", runUUID, got, want)
	}
	t.Logf("[%s] before: spec=%+v -> wire saw=%+v -> after: %+v", runUUID, spec, seen, got)
}

// ----------------------------------------------------------------------------
// CHUTES: malformed / hostile body robustness.
//
// A 2xx response whose body is NOT valid JSON for the target type MUST surface
// an error and a ZERO result — never panic, never a silently-wrong/zero-value
// "success".
// ----------------------------------------------------------------------------
func TestChutes_MalformedBodyRejected(t *testing.T) {
	const runUUID = "adv-chutes-malformed-0004"

	t.Run("pricing_non_json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("this is not json <<<"))
		}))
		t.Cleanup(srv.Close)
		c := NewChutesAdapter(srv.URL)
		got, err := c.GetCurrentPricing(context.Background(), "A100")
		if err == nil {
			t.Fatalf("[%s] malformed pricing body: err=nil want non-nil (got %+v)", runUUID, got)
		}
		if got != (Offer{}) {
			t.Fatalf("[%s] malformed pricing body: offer=%+v want zero", runUUID, got)
		}
	})

	t.Run("pricing_wrong_type", func(t *testing.T) {
		// price_usd as a string must fail decode, not coerce to 0 silently.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"price_usd":"not-a-number","gpu_model":"A100"}`))
		}))
		t.Cleanup(srv.Close)
		c := NewChutesAdapter(srv.URL)
		got, err := c.GetCurrentPricing(context.Background(), "A100")
		if err == nil {
			t.Fatalf("[%s] wrong-typed price: err=nil want non-nil (got %+v)", runUUID, got)
		}
		if got != (Offer{}) {
			t.Fatalf("[%s] wrong-typed price: offer=%+v want zero", runUUID, got)
		}
	})

	t.Run("submit_non_json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("}{ broken"))
		}))
		t.Cleanup(srv.Close)
		c := NewChutesAdapter(srv.URL)
		got, err := c.SubmitWork(context.Background(), WorkSpec{ID: "x", GPUModel: "A100"})
		if err == nil {
			t.Fatalf("[%s] malformed submit body: err=nil want non-nil (got %+v)", runUUID, got)
		}
		if got != (WorkResult{}) {
			t.Fatalf("[%s] malformed submit body: res=%+v want zero", runUUID, got)
		}
	})

	t.Run("submit_empty_jobid_still_accepted", func(t *testing.T) {
		// Document the ACTUAL contract: an empty job_id is decodable JSON, so
		// SubmitWork returns Accepted:true with an empty JobID. This pins the
		// (arguably lax) behavior so a future change is a conscious one.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		}))
		t.Cleanup(srv.Close)
		c := NewChutesAdapter(srv.URL)
		got, err := c.SubmitWork(context.Background(), WorkSpec{ID: "x", GPUModel: "A100"})
		if err != nil {
			t.Fatalf("[%s] empty job_id: unexpected err %v", runUUID, err)
		}
		want := WorkResult{Marketplace: "chutes", JobID: "", Accepted: true}
		if got != want {
			t.Fatalf("[%s] empty job_id contract drift: got %+v want %+v", runUUID, got, want)
		}
	})
}

// ----------------------------------------------------------------------------
// AKASH: price/unit (AKT -> USD) translation centerpiece.
//
// Pins that PriceUSD == PriceAKT * AKTToUSD exactly, at fractional and large
// values, AND that the conversion uses the configured rate (not a dropped
// multiplier i.e. PriceUSD==PriceAKT, and not the default constant when a rate
// is set). A swap of operands wouldn't be caught by equality alone, so we also
// use a non-commutative-detectable distinct pair where AKT != rate.
// ----------------------------------------------------------------------------
func TestAkash_GetCurrentPricing_PriceScaleFidelity(t *testing.T) {
	const runUUID = "adv-akash-scale-0005"

	cases := []struct {
		aktBid float64
		rate   float64
	}{
		{aktBid: 4.0, rate: 2.5},   // 10.0
		{aktBid: 0.001, rate: 3.0}, // 0.003 fractional
		{aktBid: 1234.5, rate: 0.123456},
		{aktBid: 7.0, rate: 1.0}, // rate 1 must NOT be confused with "no conversion"
	}
	for _, tc := range cases {
		cl := &fakeAkashClient{bid: AkashBid{Provider: "akash1p", PriceAKT: tc.aktBid}}
		a := NewAkashAdapter(cl)
		a.AKTToUSD = tc.rate

		got, err := a.GetCurrentPricing(context.Background(), "H100")
		if err != nil {
			t.Fatalf("[%s] akt=%v rate=%v: err %v", runUUID, tc.aktBid, tc.rate, err)
		}
		want := tc.aktBid * tc.rate
		if got.PriceUSD != want {
			t.Fatalf("[%s] price scale wrong: akt=%v rate=%v -> got %v want %v "+
				"(dropped multiplier would give %v)",
				runUUID, tc.aktBid, tc.rate, got.PriceUSD, want, tc.aktBid)
		}
	}
	t.Logf("[%s] all %d AKT->USD conversions exact", runUUID, len(cases))
}

// TestAkash_GetCurrentPricing_UsesDefaultRateWhenUnset pins that a zero/negative
// AKTToUSD falls back to the package default constant (2.50), so a misconfigured
// adapter does not silently price everything at zero.
func TestAkash_GetCurrentPricing_DefaultRate(t *testing.T) {
	const runUUID = "adv-akash-defrate-0006"
	cl := &fakeAkashClient{bid: AkashBid{Provider: "akash1p", PriceAKT: 10.0}}

	// Zero-value adapter: AKTToUSD is 0 -> must use defaultAKTPriceUSD (2.50).
	a := &AkashAdapter{Client: cl}
	got, err := a.GetCurrentPricing(context.Background(), "H100")
	if err != nil {
		t.Fatalf("[%s] err %v", runUUID, err)
	}
	want := 10.0 * defaultAKTPriceUSD // 25.0
	if got.PriceUSD != want {
		t.Fatalf("[%s] default-rate conversion wrong: got %v want %v (rate must fall back to %v, not 0)",
			runUUID, got.PriceUSD, want, defaultAKTPriceUSD)
	}
	t.Logf("[%s] before: AKTToUSD unset, 10 AKT -> after: PriceUSD=%v (default rate %v)", runUUID, got.PriceUSD, defaultAKTPriceUSD)
}

// TestAkash_GetCurrentPricing_FieldFidelity pins the full Offer build for Akash:
// Marketplace stamped "akash", GPUModel echoed from the REQUEST arg (Akash has
// no gpu_model in the bid), PriceUSD converted. Distinct gpu arg proves the
// echo path.
func TestAkash_GetCurrentPricing_FieldFidelity(t *testing.T) {
	const runUUID = "adv-akash-fidelity-0007"
	cl := &fakeAkashClient{bid: AkashBid{Provider: "akash1p", PriceAKT: 2.0, OrderID: "ord-x"}}
	a := NewAkashAdapter(cl)
	a.AKTToUSD = 3.0

	got, err := a.GetCurrentPricing(context.Background(), "DISTINCT-AKASH-GPU")
	if err != nil {
		t.Fatalf("[%s] err %v", runUUID, err)
	}
	want := Offer{Marketplace: "akash", PriceUSD: 6.0, GPUModel: "DISTINCT-AKASH-GPU"}
	if got != want {
		t.Fatalf("[%s] Offer fidelity broken: got %+v want %+v", runUUID, got, want)
	}
	t.Logf("[%s] before: bid{2 AKT}, rate 3, gpu arg -> after: %+v", runUUID, got)
}

// TestAkash_NegativePriceRejected pins that a negative AKT bid is treated as
// "no usable bid" (ErrNoBid), not converted into a negative USD price.
func TestAkash_NegativePriceRejected(t *testing.T) {
	const runUUID = "adv-akash-neg-0008"
	cl := &fakeAkashClient{bid: AkashBid{Provider: "akash1p", PriceAKT: -5.0}}
	a := NewAkashAdapter(cl)
	if _, err := a.GetCurrentPricing(context.Background(), "H100"); err == nil {
		t.Fatalf("[%s] negative AKT bid accepted; want ErrNoBid", runUUID)
	}
}

// ----------------------------------------------------------------------------
// AKASH: empty/missing-provider robustness.
//
// A bid with a positive price but EMPTY provider must be rejected by SubmitWork
// (ErrNoBid) and create NO lease — a provider="" must never reach the
// reputation map lookup as a "valid" zero-rep provider that then mints a lease.
// ----------------------------------------------------------------------------
func TestAkash_SubmitWork_EmptyProviderRejected(t *testing.T) {
	const runUUID = "adv-akash-emptyprov-0009"
	cl := &fakeAkashClient{
		bid:        AkashBid{Provider: "", PriceAKT: 3.0, OrderID: "ord-e"},
		reputation: map[string]float64{"": 0.99}, // even if "" had high rep, no provider => reject
	}
	a := NewAkashAdapter(cl)

	got, err := a.SubmitWork(context.Background(), WorkSpec{ID: "w-e", GPUModel: "A100"})
	if err == nil {
		t.Fatalf("[%s] empty-provider bid accepted; want ErrNoBid (got %+v)", runUUID, got)
	}
	if got != (WorkResult{}) {
		t.Fatalf("[%s] got=%+v want zero on empty-provider rejection", runUUID, got)
	}
	if cl.leaseCalls != 0 {
		t.Fatalf("[%s] lease created for empty provider; leaseCalls=%d", runUUID, cl.leaseCalls)
	}
}
