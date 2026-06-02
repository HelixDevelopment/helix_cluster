package marketplaceadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeAdapter is a second, distinct adapter used to prove that Dispatch routes
// by Name() and not by registration order or "first match". Its SubmitWork
// returns a DISTINCT Marketplace string, so wrong-routing is detectable.
type fakeAdapter struct {
	name   string
	market string
}

func (f *fakeAdapter) Name() string { return f.name }

func (f *fakeAdapter) GetCurrentPricing(ctx context.Context, gpuModel string) (Offer, error) {
	return Offer{Marketplace: f.market, GPUModel: gpuModel}, nil
}

func (f *fakeAdapter) SubmitWork(ctx context.Context, spec WorkSpec) (WorkResult, error) {
	return WorkResult{Marketplace: f.market, JobID: "fake-" + spec.ID, Accepted: true}, nil
}

// chutesFixture stands up an httptest server emulating the Chutes API:
//
//	GET  /v1/pricing?gpu=...  -> {"price_usd":..., "gpu_model":...}
//	POST /v1/work             -> {"job_id":"job-<spec.ID>"}
func chutesFixture(t *testing.T, price float64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pricing", func(w http.ResponseWriter, r *http.Request) {
		gpu := r.URL.Query().Get("gpu")
		// Note: deliberately do NOT set marketplace here; the adapter MUST stamp it.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"price_usd": price,
			"gpu_model": gpu,
		})
	})
	mux.HandleFunc("/v1/work", func(w http.ResponseWriter, r *http.Request) {
		var spec WorkSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"job_id": "job-" + spec.ID})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestRegistry_DispatchByName is the LOAD-BEARING GUARD: Name()-based dispatch.
// A SECOND adapter ("decoy") returning a DISTINCT marketplace string is
// registered so that wrong-routing (mutation of Dispatch to ignore the name or
// pick the wrong adapter) is detectable: the result's Marketplace would become
// "DECOY-MARKET" instead of "chutes".
func TestRegistry_DispatchByName(t *testing.T) {
	const runUUID = "8f1c0a42-7c1e-4b9a-9d3e-1454deadbeef"
	srv := chutesFixture(t, 1.23)

	reg := NewRegistry()
	reg.Register(NewChutesAdapter(srv.URL))
	// Decoy with a different Name() and a DISTINCT marketplace string.
	reg.Register(&fakeAdapter{name: "decoy", market: "DECOY-MARKET"})

	spec := WorkSpec{ID: "w-001", GPUModel: "H100"}
	got, err := reg.Dispatch(context.Background(), "chutes", spec)
	if err != nil {
		t.Fatalf("[%s] Dispatch error: %v", runUUID, err)
	}

	// Sink-side assertions with hand-derived expected values.
	if got.Marketplace != "chutes" {
		t.Fatalf("[%s] routed to wrong adapter: Marketplace=%q want %q (decoy would yield %q)",
			runUUID, got.Marketplace, "chutes", "DECOY-MARKET")
	}
	if !got.Accepted {
		t.Fatalf("[%s] Accepted=false want true", runUUID)
	}
	if got.JobID != "job-w-001" {
		t.Fatalf("[%s] JobID=%q want %q", runUUID, got.JobID, "job-w-001")
	}
	t.Logf("[%s] before: Dispatch(\"chutes\", w-001) -> after: {Marketplace:%q Accepted:%v JobID:%q} (decoy=%q proves name-routing)",
		runUUID, got.Marketplace, got.Accepted, got.JobID, "DECOY-MARKET")
}

// TestChutes_GetCurrentPricing proves GetCurrentPricing performs a real HTTP
// roundtrip through the fixture handler and decodes exact Offer fields.
func TestChutes_GetCurrentPricing(t *testing.T) {
	const runUUID = "b27e5d10-3a4f-4c8b-8e2a-1454cafef00d"
	const wantPrice = 2.75
	srv := chutesFixture(t, wantPrice)

	c := NewChutesAdapter(srv.URL)
	got, err := c.GetCurrentPricing(context.Background(), "A100")
	if err != nil {
		t.Fatalf("[%s] GetCurrentPricing error: %v", runUUID, err)
	}

	if got.Marketplace != "chutes" {
		t.Fatalf("[%s] Marketplace=%q want %q (adapter must stamp it; fixture omits it)",
			runUUID, got.Marketplace, "chutes")
	}
	if got.PriceUSD != wantPrice {
		t.Fatalf("[%s] PriceUSD=%v want %v", runUUID, got.PriceUSD, wantPrice)
	}
	if got.GPUModel != "A100" {
		t.Fatalf("[%s] GPUModel=%q want %q", runUUID, got.GPUModel, "A100")
	}
	t.Logf("[%s] before: GET /v1/pricing?gpu=A100 (price=%v, no marketplace) -> after: Offer{Marketplace:%q PriceUSD:%v GPUModel:%q}",
		runUUID, wantPrice, got.Marketplace, got.PriceUSD, got.GPUModel)
}

// errorFixture stands up an httptest server that returns HTTP 500 for every
// route, BUT writes a body that is VALID, fully-decodable JSON for the
// respective endpoint (a populated Offer / a job_id echo). This is the crux of
// the guard: if the body were non-JSON (e.g. http.Error's "down\n"), the
// json.Decode step would fail and mask a dead status guard — the test would
// pass even with the status check removed. By returning decodable JSON the
// ONLY thing that can reject the response is the `resp.StatusCode` guard, so a
// mutation to `if false` is forced to be observable: GetCurrentPricing would
// return a non-zero Offer with nil error, SubmitWork an Accepted result with
// nil error.
func errorFixture(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// Body is valid JSON that would populate both an Offer and the job_id
		// decode struct, so only the status guard can reject this response.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"price_usd": 7.5,
			"gpu_model": "A100",
			"job_id":    "job-w-err",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestChutes_Non2xxRejected is the guard for the status-rejection branch
// (marketplaceadapter.go:123 and :153). The fixture returns HTTP 500 for both
// /v1/pricing and /v1/work. Both adapter methods MUST surface a non-nil error
// and a ZERO result. If the status guard is mutated to `if false` (accept any
// status as success), GetCurrentPricing would return Offer{Marketplace:"chutes"}
// with err==nil and SubmitWork would return Accepted:true with err==nil — both
// caught here.
func TestChutes_Non2xxRejected(t *testing.T) {
	const runUUID = "f0e1d2c3-b4a5-4968-8776-1454500err500"
	srv := errorFixture(t)
	c := NewChutesAdapter(srv.URL)

	// GetCurrentPricing must reject 500 with a zero Offer.
	offer, err := c.GetCurrentPricing(context.Background(), "A100")
	if err == nil {
		t.Fatalf("[%s] GetCurrentPricing on HTTP 500: err=nil want non-nil (status guard dead?)", runUUID)
	}
	if offer != (Offer{}) {
		t.Fatalf("[%s] GetCurrentPricing on HTTP 500: offer=%+v want zero Offer{}", runUUID, offer)
	}

	// SubmitWork must reject 500 with a zero WorkResult.
	res, err := c.SubmitWork(context.Background(), WorkSpec{ID: "w-err", GPUModel: "A100"})
	if err == nil {
		t.Fatalf("[%s] SubmitWork on HTTP 500: err=nil want non-nil (status guard dead?)", runUUID)
	}
	if res != (WorkResult{}) {
		t.Fatalf("[%s] SubmitWork on HTTP 500: res=%+v want zero WorkResult{}", runUUID, res)
	}

	t.Logf("[%s] before: server returns HTTP 500 -> after: GetCurrentPricing & SubmitWork both return (zeroValue, non-nil error) instead of a fake-accepted success", runUUID)
}

// TestRegistry_UnknownMarketplace proves Dispatch surfaces ErrUnknownMarketplace
// for an unregistered name.
func TestRegistry_UnknownMarketplace(t *testing.T) {
	const runUUID = "d4a90b88-1f2c-4e77-9aa1-1454badc0de5"
	srv := chutesFixture(t, 9.99)

	reg := NewRegistry()
	reg.Register(NewChutesAdapter(srv.URL)) // only "chutes" exists

	_, err := reg.Dispatch(context.Background(), "nonexistent", WorkSpec{ID: "x"})
	if err == nil {
		t.Fatalf("[%s] expected error for unknown marketplace, got nil", runUUID)
	}
	if !isUnknown(err) {
		t.Fatalf("[%s] err=%v does not wrap ErrUnknownMarketplace", runUUID, err)
	}
	t.Logf("[%s] before: Dispatch(\"nonexistent\") -> after: error wraps ErrUnknownMarketplace (%v)", runUUID, err)
}

func isUnknown(err error) bool {
	return err != nil && errors.Is(err, ErrUnknownMarketplace)
}
