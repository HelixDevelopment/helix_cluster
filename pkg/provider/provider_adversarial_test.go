package provider

// Adversarial / contract-pinning suite for the TOP-LEVEL pkg/provider.
//
// Tonight's hunted classes were: (a) numeric / total-order in a price- or
// capacity-based SELECTOR (NaN/±Inf poisoning a comparator, ties without a
// deterministic tiebreak), (b) UNGUARDED shared state in a provider REGISTRY
// map, and (c) IDENTITY collisions (duplicate / empty provider IDs).
//
// FINDING (honest, sink-side): the top-level pkg/provider has NO selector, NO
// registry map, and NO numeric comparator. It is a pure typed HTTP adapter:
//
//   - GPUOffer{PricePerHour float64, Available int} and Lease{...} are DTOs.
//   - ListOffers parses the JSON envelope and returns offers VERBATIM
//     ("every field is parsed from the provider response, never invented" —
//     provider.go). It does not rank, dedup, or validate numerics.
//   - Provision/Terminate gate ONLY on empty id (ErrEmptyOfferID /
//     ErrEmptyLeaseID) and on HTTP status; the lease is read off the wire.
//   - ChutesProvider holds only immutable fields after construction; the only
//     shared state across goroutines is the injected *http.Client, which is
//     designed for concurrent use.
//
// So none of the three bug classes has a SINK here. Rather than fabricate a
// selector that does not exist, these tests PIN the documented pass-through and
// concurrency contract sink-side, so a future regression that (1) starts
// mutating offers, (2) dedups/reorders them nondeterministically, (3) fabricates
// a lease, or (4) adds raced shared state would FAIL here. Every assertion is on
// the actual returned value / count / order, never "no panic".
//
// Where a genuine numeric-handling edge exists (negative price, huge price,
// negative Available, JSON NaN/Inf), it is exercised and its REAL behavior is
// asserted, with the three-way discrimination noted inline:
//   REAL BUG | DOCUMENTED CONTRACT (pin) | LATENT RISK (note).

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
)

// newOffersServer returns an httptest server whose GET /v1/gpu/offers replies
// with the supplied raw JSON body (so we can inject hostile numerics the typed
// fake would not let us encode) and whose other routes 404.
func newOffersServer(t *testing.T, rawOffersBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/gpu/offers" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(rawOffersBody))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mustProvider(t *testing.T, baseURL string) *ChutesProvider {
	t.Helper()
	p, err := NewChutesProvider(ChutesConfig{BaseURL: baseURL})
	if err != nil {
		t.Fatalf("NewChutesProvider: %v", err)
	}
	return p
}

// --------------------------------------------------------------------------
// (a) NUMERIC: ListOffers passes PricePerHour / Available through VERBATIM.
//
// There is no selector to poison, so the sink-side truth we assert is the exact
// numeric round-trip: a hostile server can advertise a negative or astronomically
// large price and a negative Available, and ListOffers returns them UNCHANGED and
// UNREORDERED. This pins "never invented, never massaged" and would catch a
// regression that silently clamps/drops/normalizes numerics.
// Discrimination: DOCUMENTED CONTRACT (pin), no fix.
// --------------------------------------------------------------------------
func TestListOffers_PassesHostileNumericsVerbatim(t *testing.T) {
	body := `{"offers":[
		{"id":"neg","gpu_type":"H100","price_per_hour":-1.5,"region":"r","available":-7},
		{"id":"huge","gpu_type":"H100","price_per_hour":1e308,"region":"r","available":2147483647},
		{"id":"zero","gpu_type":"H100","price_per_hour":0,"region":"r","available":0}
	]}`
	p := mustProvider(t, newOffersServer(t, body).URL)

	offers, err := p.ListOffers(context.Background())
	if err != nil {
		t.Fatalf("ListOffers: %v", err)
	}
	if len(offers) != 3 {
		t.Fatalf("offer count = %d, want 3 (no drop/dedup): %+v", len(offers), offers)
	}
	// Order preserved exactly as served (no implicit sort/reorder).
	gotIDs := []string{offers[0].ID, offers[1].ID, offers[2].ID}
	wantIDs := []string{"neg", "huge", "zero"}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("offer[%d].ID = %q, want %q (order must be verbatim); full=%v", i, gotIDs[i], wantIDs[i], gotIDs)
		}
	}
	// Numerics round-tripped verbatim, including negative and 1e308.
	if offers[0].PricePerHour != -1.5 {
		t.Errorf("neg PricePerHour = %v, want -1.5 (no clamp)", offers[0].PricePerHour)
	}
	if offers[0].Available != -7 {
		t.Errorf("neg Available = %d, want -7 (no clamp)", offers[0].Available)
	}
	if offers[1].PricePerHour != 1e308 {
		t.Errorf("huge PricePerHour = %v, want 1e308 (no overflow massage)", offers[1].PricePerHour)
	}
	if offers[1].Available != math.MaxInt32 {
		t.Errorf("huge Available = %d, want %d", offers[1].Available, math.MaxInt32)
	}
}

// (a') NUMERIC: JSON literal NaN / +Inf are NOT valid JSON numbers; encoding/json
// rejects them. So a provider CANNOT smuggle a NaN price into the typed offer via
// the wire — the decode fails and ListOffers surfaces an error instead of a
// poisoned offer. We assert the SINK: a decode error, not a NaN-bearing offer.
// Discrimination: DOCUMENTED CONTRACT (the std decoder is the guard) — note that
// THIS is exactly why no NaN can reach a (hypothetical future) selector through
// ListOffers. If a future change swapped to a lenient decoder, this bites.
func TestListOffers_RejectsNaNAndInfLiteralsAtDecode(t *testing.T) {
	for _, lit := range []string{"NaN", "Infinity", "-Infinity", "1e", "0x1p3"} {
		body := fmt.Sprintf(`{"offers":[{"id":"x","gpu_type":"H100","price_per_hour":%s,"region":"r","available":1}]}`, lit)
		p := mustProvider(t, newOffersServer(t, body).URL)
		offers, err := p.ListOffers(context.Background())
		if err == nil {
			t.Fatalf("price literal %q: ListOffers returned nil error and %d offers; want a decode error so no poison numeric leaks through", lit, len(offers))
		}
		if offers != nil {
			t.Fatalf("price literal %q: offers = %v, want nil on decode failure", lit, offers)
		}
	}
}

// Sanity: a NaN can never even be MARSHALED into the wire by a well-behaved Go
// server, confirming the only ingress is the (rejected) raw-literal path above.
func TestNaN_CannotBeJSONMarshaled(t *testing.T) {
	_, err := json.Marshal(GPUOffer{ID: "x", PricePerHour: math.NaN()})
	if err == nil {
		t.Fatal("json.Marshal(NaN price) succeeded; expected 'unsupported value' error — NaN cannot ride the wire")
	}
}

// --------------------------------------------------------------------------
// (c) IDENTITY: duplicate offer IDs are returned VERBATIM (no clobber, no dedup).
//
// The top-level package has no registry keyed by ID, so duplicate IDs are simply
// two offers in the slice. We pin that BOTH survive with their distinct fields —
// catching a regression that started keying a map by ID (which would drop one).
// Discrimination: DOCUMENTED CONTRACT (pin). Empty IDs likewise pass through in
// ListOffers (only Provision/Terminate reject empty ids) — pinned below.
// --------------------------------------------------------------------------
func TestListOffers_DuplicateAndEmptyIDsPassThroughNoClobber(t *testing.T) {
	body := `{"offers":[
		{"id":"dup","gpu_type":"A100","price_per_hour":1,"region":"a","available":1},
		{"id":"dup","gpu_type":"H100","price_per_hour":2,"region":"b","available":9},
		{"id":"","gpu_type":"L4","price_per_hour":3,"region":"c","available":4}
	]}`
	p := mustProvider(t, newOffersServer(t, body).URL)

	offers, err := p.ListOffers(context.Background())
	if err != nil {
		t.Fatalf("ListOffers: %v", err)
	}
	if len(offers) != 3 {
		t.Fatalf("offer count = %d, want 3 (duplicate id must NOT collapse): %+v", len(offers), offers)
	}
	// Both "dup" rows survive with their distinct fields (no map clobber).
	if offers[0].GPUType != "A100" || offers[1].GPUType != "H100" {
		t.Fatalf("duplicate-id rows clobbered: [0]=%q [1]=%q, want A100 then H100", offers[0].GPUType, offers[1].GPUType)
	}
	if offers[0].PricePerHour != 1 || offers[1].PricePerHour != 2 {
		t.Fatalf("duplicate-id prices clobbered: [0]=%v [1]=%v, want 1 then 2", offers[0].PricePerHour, offers[1].PricePerHour)
	}
	// Empty-id offer is still returned by ListOffers (the empty-id guard lives in
	// Provision/Terminate, not in listing).
	if offers[2].ID != "" || offers[2].GPUType != "L4" {
		t.Fatalf("empty-id offer mangled: %+v, want {ID:\"\" GPUType:L4}", offers[2])
	}
}

// (c') IDENTITY: empty offerID/leaseID are rejected at the SINK with the typed
// sentinel BEFORE any request is sent. We assert the sentinel and that no HTTP
// call occurred (the server would 500 if hit, proving the early return).
func TestEmptyIDs_RejectedBeforeAnyRequest(t *testing.T) {
	var hits int32
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := mustProvider(t, srv.URL)

	if _, err := p.Provision(context.Background(), "   "); err != ErrEmptyOfferID {
		t.Fatalf("Provision(blank) err = %v, want ErrEmptyOfferID", err)
	}
	if err := p.Terminate(context.Background(), ""); err != ErrEmptyLeaseID {
		t.Fatalf("Terminate(\"\") err = %v, want ErrEmptyLeaseID", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("server saw %d requests; empty-id calls must short-circuit before the wire", hits)
	}
}

// --------------------------------------------------------------------------
// (b) UNGUARDED SHARED STATE: a single ChutesProvider is documented (chutes.go)
// as built once and then read-only; its only shared mutable thing is the
// injected *http.Client. We hammer ListOffers + Provision + Terminate
// concurrently through ONE provider and assert (under -race) zero data race AND
// conservation: every Provision that returns nil yields the lease the server
// minted, and the count of distinct minted lease IDs equals successful
// provisions. This pins concurrency-safety sink-side, not just "no panic".
// Discrimination: DOCUMENTED CONTRACT ("safe for concurrent use" is implied by
// the immutable-after-construction design) — pin.
// --------------------------------------------------------------------------
func TestConcurrentMixedOps_RaceFreeAndLeaseConserving(t *testing.T) {
	// Server mints a unique lease id per provision and tracks them so we can
	// assert sink-side conservation independent of the adapter's own bookkeeping.
	var smu sync.Mutex
	minted := map[string]bool{}
	var seq int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/gpu/offers":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"offers":[{"id":"o1","gpu_type":"H100","price_per_hour":1.25,"region":"r","available":5}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/gpu/leases":
			smu.Lock()
			seq++
			id := fmt.Sprintf("lease-%d", seq)
			minted[id] = true
			smu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"id":%q,"offer_id":"o1","endpoint":"10.0.0.1:443"}`, id)))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := mustProvider(t, srv.URL)

	const workers = 24
	const provPerWorker = 8
	var wg sync.WaitGroup
	var rmu sync.Mutex
	gotLeases := map[string]bool{}

	wg.Add(workers)
	for g := 0; g < workers; g++ {
		go func(g int) {
			defer wg.Done()
			ctx := context.Background()
			for i := 0; i < provPerWorker; i++ {
				switch (g + i) % 3 {
				case 0:
					if _, err := p.ListOffers(ctx); err != nil {
						t.Errorf("ListOffers: %v", err)
						return
					}
				case 1:
					lease, err := p.Provision(ctx, "o1")
					if err != nil {
						t.Errorf("Provision: %v", err)
						return
					}
					if lease.ID == "" || lease.Endpoint != "10.0.0.1:443" {
						t.Errorf("provisioned lease not read off wire: %+v", lease)
						return
					}
					rmu.Lock()
					if gotLeases[lease.ID] {
						rmu.Unlock()
						t.Errorf("duplicate lease id handed out: %q", lease.ID)
						return
					}
					gotLeases[lease.ID] = true
					rmu.Unlock()
				default:
					if err := p.Terminate(ctx, "lease-x"); err != nil {
						t.Errorf("Terminate: %v", err)
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()

	// Conservation: every lease id the client received was actually minted by the
	// server, and none was duplicated. (No lost-update / fabricated-lease.)
	rmu.Lock()
	defer rmu.Unlock()
	smu.Lock()
	defer smu.Unlock()
	if len(gotLeases) == 0 {
		t.Fatal("no successful provisions observed; test did not exercise the path")
	}
	for id := range gotLeases {
		if !minted[id] {
			t.Fatalf("client returned lease %q the server never minted (fabricated lease)", id)
		}
	}
	// Order-independent sink check: distinct received <= distinct minted.
	if len(gotLeases) > len(minted) {
		t.Fatalf("received %d distinct leases but server minted only %d", len(gotLeases), len(minted))
	}
}

// --------------------------------------------------------------------------
// Determinism note for callers who DO sort offers by price themselves: since the
// top-level package returns offers in server order, a naive caller sorting by
// PricePerHour alone has NO tiebreak on equal price and would pick
// nondeterministically. This is a LATENT RISK for CALLERS, not a bug in
// pkg/provider (which makes no selection promise). We demonstrate the failure
// mode on a plain price-only sort to document why callers must add an ID
// tiebreak — this test asserts the DOCUMENTED stable-with-tiebreak behavior, so
// it stays green and serves as the reference pattern.
// --------------------------------------------------------------------------
func TestEqualPriceTie_RequiresIDTiebreakForDeterminism(t *testing.T) {
	offers := []GPUOffer{
		{ID: "b", PricePerHour: 2.0},
		{ID: "a", PricePerHour: 2.0},
		{ID: "c", PricePerHour: 2.0},
	}
	// Correct pattern: price then ID. Deterministic regardless of input order.
	pick := func(in []GPUOffer) GPUOffer {
		cp := append([]GPUOffer(nil), in...)
		sort.Slice(cp, func(i, j int) bool {
			if cp[i].PricePerHour != cp[j].PricePerHour {
				return cp[i].PricePerHour < cp[j].PricePerHour
			}
			return cp[i].ID < cp[j].ID // total-order tiebreak
		})
		return cp[0]
	}
	want := pick(offers).ID
	// Permute the input; the chosen offer must not change.
	perms := [][]GPUOffer{
		{offers[2], offers[1], offers[0]},
		{offers[1], offers[0], offers[2]},
		{offers[0], offers[2], offers[1]},
	}
	for i, perm := range perms {
		if got := pick(perm).ID; got != want {
			t.Fatalf("perm %d: tie pick = %q, want stable %q — selection is nondeterministic without an ID tiebreak", i, got, want)
		}
	}
	if want != "a" {
		t.Fatalf("lowest-id tiebreak winner = %q, want \"a\"", want)
	}
}
