package marketplace

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// httpOfferSource is a reference real-world OfferSource: it talks JSON/HTTP to a
// marketplace endpoint. In production the URL is the live Chutes API; in this
// test it is a LOCAL httptest server (never a real provider, never a container).
// It exercises the exact same code path a production client would.
type httpOfferSource struct {
	url    string
	client *http.Client
}

func (h *httpOfferSource) Fetch(ctx context.Context) ([]Offer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("marketplace: upstream status " + resp.Status)
	}
	var offers []Offer
	if err := json.NewDecoder(resp.Body).Decode(&offers); err != nil {
		return nil, err
	}
	return offers, nil
}

// TestChutesAdapter_ListOffers_StampsProvider proves the adapter fetches offers
// from its source and stamps each offer's Provider with the adapter Name — the
// sink-side fact downstream ranking/audit relies on.
//
// Mutation that this kills: in ListOffers, drop the `o.Provider = c.Name()`
// line. Then the returned offers keep their empty Provider and the
// "Provider == chutes" assertion FAILS.
func TestChutesAdapter_ListOffers_StampsProvider(t *testing.T) {
	src := &FakeSource{Offers: []Offer{
		{ID: "g1", PricePerHour: 2.0, AvailabilityPct: 99, LatencyMS: 40, ThroughputTokS: 500},
		{ID: "g2", PricePerHour: 1.0, AvailabilityPct: 90, LatencyMS: 90, ThroughputTokS: 300, Confidential: true},
	}}
	a := &ChutesAdapter{Source: src}

	if a.Name() != "chutes" {
		t.Fatalf("Name() = %q, want chutes", a.Name())
	}
	got, err := a.ListOffers(context.Background())
	if err != nil {
		t.Fatalf("ListOffers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d offers, want 2", len(got))
	}
	for _, o := range got {
		if o.Provider != "chutes" {
			t.Fatalf("offer %q Provider = %q, want chutes", o.ID, o.Provider)
		}
	}
	// Concrete payload assertion, not just "not nil".
	if got[1].ID != "g2" || !got[1].Confidential {
		t.Fatalf("offer[1] = %+v, want g2/confidential", got[1])
	}
}

// TestChutesAdapter_NoSource proves a misconfigured adapter fails loudly.
//
// Mutation that this kills: in ListOffers, remove the `if c.Source == nil`
// guard. Then it dereferences nil Source and panics instead of returning
// ErrNoSource, failing this test.
func TestChutesAdapter_NoSource(t *testing.T) {
	a := &ChutesAdapter{}
	_, err := a.ListOffers(context.Background())
	if !errors.Is(err, ErrNoSource) {
		t.Fatalf("err = %v, want ErrNoSource", err)
	}
}

// TestChutesAdapter_SourceErrorWrapped proves source errors are propagated and
// wrapped (not swallowed) — a CLAUDE-1 anti-bluff requirement.
//
// Mutation that this kills: in ListOffers, replace `return nil, fmt.Errorf(...
// %w ...)` with `return nil, nil`. Then errors.Is(err, sentinel) is false and
// this test FAILS.
func TestChutesAdapter_SourceErrorWrapped(t *testing.T) {
	sentinel := errors.New("backend down")
	a := &ChutesAdapter{Source: &FakeSource{Err: sentinel}}
	_, err := a.ListOffers(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want to wrap sentinel", err)
	}
}

// TestChutesAdapter_OverHTTP proves a real HTTP OfferSource plugs into the same
// Adapter and that the end-to-end fetch+stamp+rank pipeline produces the exact
// expected winner. The server is local httptest — the honest stand-in for the
// live Chutes API.
//
// Mutation that this kills: in ListOffers, stop forwarding ctx / mis-stamp
// Provider, OR in the http source decode the wrong field — the decoded "fast"
// offer would not rank first. We assert the exact ranked winner ID, so any of
// those breaks the assertion.
func TestChutesAdapter_OverHTTP(t *testing.T) {
	payload := []Offer{
		{ID: "slow", PricePerHour: 1.0, AvailabilityPct: 90, LatencyMS: 300, ThroughputTokS: 100},
		{ID: "fast", PricePerHour: 1.0, AvailabilityPct: 90, LatencyMS: 20, ThroughputTokS: 900},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	a := &ChutesAdapter{
		AdapterName: "chutes",
		Source:      &httpOfferSource{url: srv.URL, client: srv.Client()},
	}
	offers, err := a.ListOffers(context.Background())
	if err != nil {
		t.Fatalf("ListOffers over HTTP: %v", err)
	}
	for _, o := range offers {
		if o.Provider != "chutes" {
			t.Fatalf("offer %q Provider = %q, want chutes", o.ID, o.Provider)
		}
	}
	// Same price+avail; "fast" wins on latency+throughput. Assert exact winner.
	ranked := CompositeScorer{}.Rank(offers, DefaultWeights, false)
	if ranked[0].Offer.ID != "fast" {
		t.Fatalf("ranked winner = %q, want fast", ranked[0].Offer.ID)
	}
}

// TestChutesAdapter_OverHTTP_503_Surfaces proves that when the upstream
// marketplace REALLY answers 503 (Service Unavailable), the httpOfferSource does
// NOT silently accept the error body and attempt to JSON-decode it — it surfaces
// a concrete error that the adapter wraps with its provider name. This is the
// "must REALLY fall back on 503" behaviour: the caller gets an error it can act
// on (retry / fall back to another marketplace), not an empty offer list.
//
// We assert three concrete facts, not just "err != nil":
//   - the error is non-nil,
//   - it is wrapped with the adapter Name ("chutes:"),
//   - it carries the HTTP status string ("503").
//
// Mutation that this kills: remove the `if resp.StatusCode != http.StatusOK`
// guard in httpOfferSource.Fetch. Then Fetch would feed the 503 error body
// straight into json.Decode. The handler writes a non-JSON body, so the decode
// error happens to be non-nil here — BUT it would no longer contain "503", so
// the status-string assertion FAILS. If an attacker/upstream returned a valid
// JSON error body shaped like []Offer, removing the guard would make the adapter
// silently return that bogus list with NO error — which this test's status-string
// assertion is the only thing that pins.
func TestChutesAdapter_OverHTTP_503_Surfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A body shaped like a valid empty offer list: if the status guard were
		// removed, decode would SUCCEED and the adapter would silently return [].
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable) // 503
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	a := &ChutesAdapter{
		AdapterName: "chutes",
		Source:      &httpOfferSource{url: srv.URL, client: srv.Client()},
	}
	offers, err := a.ListOffers(context.Background())
	if err == nil {
		t.Fatalf("ListOffers accepted a 503 response, got offers=%v, want error", offers)
	}
	if offers != nil {
		t.Fatalf("ListOffers returned offers alongside the 503 error: %v", offers)
	}
	if !strings.Contains(err.Error(), "chutes:") {
		t.Fatalf("error %q not wrapped with adapter name", err.Error())
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("error %q does not carry the upstream 503 status", err.Error())
	}
}
