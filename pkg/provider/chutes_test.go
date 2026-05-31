package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeChutes is a LOCAL httptest emulator of the Chutes GPU-rental control API.
// It holds offers and an in-memory lease table so terminate observably releases
// a lease and a missing lease 404s. It also records the last seen Authorization
// header and request method/path for wire-shape assertions.
type fakeChutes struct {
	mu       sync.Mutex
	offers   []GPUOffer
	leases   map[string]Lease // leaseID -> lease
	nextID   int
	lastAuth string
	lastMeth string
	lastPath string
	provBody provisionRequest
}

func newFakeChutes(offers []GPUOffer) *fakeChutes {
	return &fakeChutes{offers: offers, leases: map[string]Lease{}}
}

func (f *fakeChutes) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/gpu/offers", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(offersEnvelope{Offers: f.offers})
	})
	mux.HandleFunc("/v1/gpu/leases", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var req provisionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.provBody = req
		f.nextID++
		id := "lease-" + itoa(f.nextID)
		lease := Lease{ID: id, OfferID: req.OfferID, Endpoint: "gpu-" + id + ".chutes.example:443"}
		f.leases[id] = lease
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(lease)
	})
	mux.HandleFunc("/v1/gpu/leases/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if r.Method != http.MethodDelete {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/gpu/leases/")
		f.mu.Lock()
		_, ok := f.leases[id]
		if ok {
			delete(f.leases, id)
		}
		f.mu.Unlock()
		if !ok {
			http.Error(w, "no such lease", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (f *fakeChutes) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastAuth = r.Header.Get("Authorization")
	f.lastMeth = r.Method
	f.lastPath = r.URL.Path
}

func (f *fakeChutes) leaseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.leases)
}

// itoa avoids pulling strconv into a hot helper; small positive ints only.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func newProvider(t *testing.T, fake *fakeChutes, token string) (*ChutesProvider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	p, err := NewChutesProvider(ChutesConfig{BaseURL: srv.URL, Token: token})
	if err != nil {
		t.Fatalf("NewChutesProvider: %v", err)
	}
	return p, srv
}

// TestNewChutesProviderRejectsEmptyBaseURL proves a misconfigured adapter fails
// loud instead of silently no-opping.
//
// MUTATION: drop the `if strings.TrimSpace(cfg.BaseURL) == ""` guard in
// NewChutesProvider — err becomes nil and this fails.
func TestNewChutesProviderRejectsEmptyBaseURL(t *testing.T) {
	_, err := NewChutesProvider(ChutesConfig{BaseURL: ""})
	if !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("want ErrNoEndpoint, got %v", err)
	}
}

// TestListOffersParsesExactTypedOffers proves ListOffers parses the fake
// server's JSON into exact typed GPUOffers, asserting concrete fields (not just
// "non-empty").
//
// MUTATION: in ListOffers, replace `json.Unmarshal(raw, &env)` with a no-op so
// env.Offers stays nil — len==0 and the field assertions below fail.
func TestListOffersParsesExactTypedOffers(t *testing.T) {
	fake := newFakeChutes([]GPUOffer{
		{ID: "off-h100", GPUType: "H100", PricePerHour: 2.49, Region: "us-east-1", Available: 4},
		{ID: "off-a100", GPUType: "A100-80G", PricePerHour: 1.10, Region: "eu-west-1", Available: 0},
	})
	p, _ := newProvider(t, fake, "")

	offers, err := p.ListOffers(context.Background())
	if err != nil {
		t.Fatalf("ListOffers: %v", err)
	}
	if len(offers) != 2 {
		t.Fatalf("want 2 offers, got %d", len(offers))
	}
	got := offers[0]
	if got.ID != "off-h100" || got.GPUType != "H100" {
		t.Fatalf("offer[0] id/type = %q/%q, want off-h100/H100", got.ID, got.GPUType)
	}
	if got.PricePerHour != 2.49 {
		t.Fatalf("offer[0] $/hr = %v, want 2.49", got.PricePerHour)
	}
	if got.Region != "us-east-1" || got.Available != 4 {
		t.Fatalf("offer[0] region/avail = %q/%d, want us-east-1/4", got.Region, got.Available)
	}
	if offers[1].Available != 0 || offers[1].PricePerHour != 1.10 {
		t.Fatalf("offer[1] avail/$ = %d/%v, want 0/1.10", offers[1].Available, offers[1].PricePerHour)
	}
}

// TestListOffersServerErrorSurfaces proves a 5xx is an error, never silently a
// "no GPUs available" empty slice.
//
// MUTATION: in ListOffers, delete the `if status != http.StatusOK` check — it
// would return a nil error with empty offers and this fails.
func TestListOffersServerErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	p, err := NewChutesProvider(ChutesConfig{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewChutesProvider: %v", err)
	}
	offers, err := p.ListOffers(context.Background())
	if err == nil {
		t.Fatalf("want error on 500, got offers=%v", offers)
	}
	if offers != nil {
		t.Fatalf("want nil offers on error, got %v", offers)
	}
}

// TestProvisionReturnsExactServerLeaseID is the anti-bluff anchor: the lease id
// and endpoint MUST be the ones the server minted, read off the response body.
//
// MUTATION: in Provision, replace `json.Unmarshal(raw, &lease)` with a
// fabricated `lease = Lease{ID: "lease-local", ...}`. The server mints
// "lease-1"; the exact-id assertion below fails. (This is the exact bluff the
// task calls out.)
func TestProvisionReturnsExactServerLeaseID(t *testing.T) {
	fake := newFakeChutes([]GPUOffer{{ID: "off-h100", GPUType: "H100", PricePerHour: 2.49, Region: "us-east-1", Available: 4}})
	p, _ := newProvider(t, fake, "secret-token")

	lease, err := p.Provision(context.Background(), "off-h100")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if lease.ID != "lease-1" {
		t.Fatalf("lease id = %q, want lease-1 (must come from server body)", lease.ID)
	}
	if lease.OfferID != "off-h100" {
		t.Fatalf("lease offer = %q, want off-h100", lease.OfferID)
	}
	if lease.Endpoint != "gpu-lease-1.chutes.example:443" {
		t.Fatalf("lease endpoint = %q, want gpu-lease-1.chutes.example:443", lease.Endpoint)
	}
	// Bearer token actually went over the wire on the POST.
	if fake.lastAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q, want Bearer secret-token", fake.lastAuth)
	}
	if fake.lastMeth != http.MethodPost || fake.lastPath != "/v1/gpu/leases" {
		t.Fatalf("provision wire = %s %s, want POST /v1/gpu/leases", fake.lastMeth, fake.lastPath)
	}
	if fake.provBody.OfferID != "off-h100" {
		t.Fatalf("server saw offer_id %q, want off-h100", fake.provBody.OfferID)
	}
}

// TestProvisionEmptyOfferIDRejected proves an empty offer id is rejected before
// any request is sent.
//
// MUTATION: remove the `strings.TrimSpace(offerID) == ""` guard — Provision
// would POST an empty offer_id and this errors.Is check fails.
func TestProvisionEmptyOfferIDRejected(t *testing.T) {
	fake := newFakeChutes(nil)
	p, _ := newProvider(t, fake, "")
	_, err := p.Provision(context.Background(), "")
	if !errors.Is(err, ErrEmptyOfferID) {
		t.Fatalf("want ErrEmptyOfferID, got %v", err)
	}
}

// TestProvision4xxSurfacesErrorNotSilentLease proves a 4xx/5xx yields an error
// AND a zero Lease — never a silent empty lease that callers might "use".
//
// MUTATION: in Provision, change the status guard to accept all codes (e.g.
// `if false`) — a zero Lease with nil error leaks out and this fails.
func TestProvision4xxSurfacesErrorNotSilentLease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "quota exceeded", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	p, _ := NewChutesProvider(ChutesConfig{BaseURL: srv.URL})

	lease, err := p.Provision(context.Background(), "off-x")
	if !errors.Is(err, ErrProvisionFailed) {
		t.Fatalf("want ErrProvisionFailed, got %v", err)
	}
	if lease != (Lease{}) {
		t.Fatalf("want zero Lease on failure, got %+v", lease)
	}
}

// TestProvisionRejectsEmptyServerLeaseID proves a 2xx that omits the lease id is
// treated as a broken contract, not a usable lease.
//
// MUTATION: delete the `if lease.ID == ""` block in Provision — an empty-id
// lease with nil error leaks out and this fails.
func TestProvisionRejectsEmptyServerLeaseID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"endpoint":"x:443"}`)) // no "id"
	}))
	t.Cleanup(srv.Close)
	p, _ := NewChutesProvider(ChutesConfig{BaseURL: srv.URL})

	lease, err := p.Provision(context.Background(), "off-x")
	if !errors.Is(err, ErrProvisionFailed) {
		t.Fatalf("want ErrProvisionFailed for empty id, got %v (lease=%+v)", err, lease)
	}
}

// TestTerminateReleasesLease proves a DELETE on a live lease succeeds and
// observably removes it from the server's lease table.
//
// MUTATION: in Terminate, change http.MethodDelete to http.MethodGet — the fake
// returns 405, the status guard errors, and leaseCount stays 1; this fails.
func TestTerminateReleasesLease(t *testing.T) {
	fake := newFakeChutes([]GPUOffer{{ID: "off-h100", GPUType: "H100", PricePerHour: 2.49, Region: "us-east-1", Available: 4}})
	p, _ := newProvider(t, fake, "")

	lease, err := p.Provision(context.Background(), "off-h100")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if fake.leaseCount() != 1 {
		t.Fatalf("after provision want 1 lease, got %d", fake.leaseCount())
	}
	if err := p.Terminate(context.Background(), lease.ID); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if fake.leaseCount() != 0 {
		t.Fatalf("after terminate want 0 leases, got %d", fake.leaseCount())
	}
	if fake.lastMeth != http.MethodDelete {
		t.Fatalf("terminate method = %s, want DELETE", fake.lastMeth)
	}
	if fake.lastPath != "/v1/gpu/leases/"+lease.ID {
		t.Fatalf("terminate path = %s, want /v1/gpu/leases/%s", fake.lastPath, lease.ID)
	}
}

// TestTerminateMissingLeaseErrors proves a 404 maps to ErrLeaseNotFound rather
// than being swallowed as success.
//
// MUTATION: in Terminate, drop the `if status == http.StatusNotFound` branch (so
// 404 falls into the generic path or is ignored) — errors.Is(ErrLeaseNotFound)
// fails.
func TestTerminateMissingLeaseErrors(t *testing.T) {
	fake := newFakeChutes(nil)
	p, _ := newProvider(t, fake, "")
	err := p.Terminate(context.Background(), "lease-does-not-exist")
	if !errors.Is(err, ErrLeaseNotFound) {
		t.Fatalf("want ErrLeaseNotFound, got %v", err)
	}
}

// TestTerminateEmptyLeaseIDRejected proves an empty lease id is rejected before
// any request.
//
// MUTATION: remove the empty-leaseID guard in Terminate — it would DELETE
// /v1/gpu/leases/ and not return ErrEmptyLeaseID; this fails.
func TestTerminateEmptyLeaseIDRejected(t *testing.T) {
	fake := newFakeChutes(nil)
	p, _ := newProvider(t, fake, "")
	if err := p.Terminate(context.Background(), ""); !errors.Is(err, ErrEmptyLeaseID) {
		t.Fatalf("want ErrEmptyLeaseID, got %v", err)
	}
}

// TestName pins the provider label used in selection/logs.
//
// MUTATION: change the Name() return to "" — this fails.
func TestName(t *testing.T) {
	p, _ := NewChutesProvider(ChutesConfig{BaseURL: "http://x"})
	if p.Name() != "chutes" {
		t.Fatalf("Name = %q, want chutes", p.Name())
	}
}

// TestProviderInterfaceSatisfied is a compile+runtime check that ChutesProvider
// is usable through the Provider interface (the abstraction's whole point).
//
// MUTATION: remove a method from ChutesProvider (e.g. Terminate) — assignment to
// Provider fails to compile, killing the package build.
func TestProviderInterfaceSatisfied(t *testing.T) {
	var _ Provider = (*ChutesProvider)(nil)
	fake := newFakeChutes([]GPUOffer{{ID: "off-1", GPUType: "L4", PricePerHour: 0.5, Region: "us-west-2", Available: 1}})
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	var prov Provider
	p, _ := NewChutesProvider(ChutesConfig{BaseURL: srv.URL})
	prov = p
	offers, err := prov.ListOffers(context.Background())
	if err != nil || len(offers) != 1 || offers[0].GPUType != "L4" {
		t.Fatalf("interface ListOffers = %v, %v", offers, err)
	}
}
