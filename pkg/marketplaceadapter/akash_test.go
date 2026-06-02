package marketplaceadapter

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeAkashClient is an in-process implementation of AkashClient with REAL
// recorded state. It is NOT a mock-only stub: SubmitWork's effects are observed
// sink-side (the SDL it received and the lease it minted are recorded here and
// asserted by tests). This satisfies CLAUDE-1: the adapter's decision logic
// (reverse-auction bid, reputation gate, SDL->lease flow) runs for real against
// this seam.
type fakeAkashClient struct {
	// inputs
	bid        AkashBid
	bidErr     error
	reputation map[string]float64
	repErr     error

	// recorded sink-side effects
	recordedSDL      string
	recordedProvider string
	leaseCalls       int
	mintedLeaseID    string
}

func (f *fakeAkashClient) WinningBid(_ context.Context, _ string) (AkashBid, error) {
	if f.bidErr != nil {
		return AkashBid{}, f.bidErr
	}
	return f.bid, nil
}

func (f *fakeAkashClient) ProviderReputation(_ context.Context, provider string) (float64, error) {
	if f.repErr != nil {
		return 0, f.repErr
	}
	return f.reputation[provider], nil
}

func (f *fakeAkashClient) CreateLease(_ context.Context, sdl string, provider string) (AkashLease, error) {
	// Record the SDL + provider verbatim so tests prove sink-side what was sent.
	f.recordedSDL = sdl
	f.recordedProvider = provider
	f.leaseCalls++
	id := "lease-akash-001"
	f.mintedLeaseID = id
	return AkashLease{LeaseID: id, Provider: provider}, nil
}

// TestAkash_GetCurrentPricing proves GetCurrentPricing returns a reverse-auction
// AKT bid from the injected client, converted to USD and stamped "akash".
// Closure criterion #1.
func TestAkash_GetCurrentPricing(t *testing.T) {
	const runUUID = "1458a000-0000-4000-8000-0000000000a1"
	const priceAKT = 4.0
	cl := &fakeAkashClient{
		bid: AkashBid{Provider: "akash1prov", PriceAKT: priceAKT, OrderID: "ord-1"},
	}
	a := NewAkashAdapter(cl)
	a.AKTToUSD = 2.0 // deterministic conversion

	got, err := a.GetCurrentPricing(context.Background(), "H100")
	if err != nil {
		t.Fatalf("[%s] GetCurrentPricing error: %v", runUUID, err)
	}
	if got.Marketplace != "akash" {
		t.Fatalf("[%s] Marketplace=%q want %q", runUUID, got.Marketplace, "akash")
	}
	wantUSD := priceAKT * 2.0 // 8.0
	if got.PriceUSD != wantUSD {
		t.Fatalf("[%s] PriceUSD=%v want %v (AKT bid %v * %v)", runUUID, got.PriceUSD, wantUSD, priceAKT, 2.0)
	}
	if got.GPUModel != "H100" {
		t.Fatalf("[%s] GPUModel=%q want %q", runUUID, got.GPUModel, "H100")
	}
	t.Logf("[%s] before: WinningBid -> %v AKT -> after: Offer{Marketplace:%q PriceUSD:%v GPUModel:%q}",
		runUUID, priceAKT, got.Marketplace, got.PriceUSD, got.GPUModel)
}

// TestAkash_GetCurrentPricing_NoBid proves the empty-order-book path surfaces
// ErrNoBid rather than a fake zero-price Offer.
func TestAkash_GetCurrentPricing_NoBid(t *testing.T) {
	const runUUID = "1458a000-0000-4000-8000-0000000000a2"
	cl := &fakeAkashClient{bid: AkashBid{Provider: "akash1prov", PriceAKT: 0}}
	a := NewAkashAdapter(cl)

	_, err := a.GetCurrentPricing(context.Background(), "H100")
	if !errors.Is(err, ErrNoBid) {
		t.Fatalf("[%s] err=%v want ErrNoBid", runUUID, err)
	}
}

// TestAkash_SubmitWork_GoodProvider proves the full SDL->lease flow for a
// provider AT/ABOVE the reputation threshold: the fake client RECORDS the SDL it
// received (sink-side) and the adapter returns the lease id as JobID.
// Closure criterion #2.
func TestAkash_SubmitWork_GoodProvider(t *testing.T) {
	const runUUID = "1458a000-0000-4000-8000-0000000000a3"
	cl := &fakeAkashClient{
		bid:        AkashBid{Provider: "akash1good", PriceAKT: 3.0, OrderID: "ord-9"},
		reputation: map[string]float64{"akash1good": 0.91},
	}
	a := NewAkashAdapter(cl) // MinReputation defaults to 0.5

	spec := WorkSpec{ID: "w-777", GPUModel: "A100"}
	got, err := a.SubmitWork(context.Background(), spec)
	if err != nil {
		t.Fatalf("[%s] SubmitWork error: %v", runUUID, err)
	}

	// Sink-side: the lease was actually created exactly once.
	if cl.leaseCalls != 1 {
		t.Fatalf("[%s] CreateLease called %d times want 1", runUUID, cl.leaseCalls)
	}
	// Sink-side: the SDL manifest was submitted and embeds the work id + GPU model.
	if !strings.Contains(cl.recordedSDL, "helix/work:w-777") {
		t.Fatalf("[%s] recorded SDL missing work id; got:\n%s", runUUID, cl.recordedSDL)
	}
	if !strings.Contains(cl.recordedSDL, "model: A100") {
		t.Fatalf("[%s] recorded SDL missing gpu model; got:\n%s", runUUID, cl.recordedSDL)
	}
	if cl.recordedProvider != "akash1good" {
		t.Fatalf("[%s] lease created for provider %q want %q", runUUID, cl.recordedProvider, "akash1good")
	}
	// Sink-side: returned WorkResult carries the lease id as JobID.
	if got.Marketplace != "akash" || !got.Accepted {
		t.Fatalf("[%s] WorkResult=%+v want akash/accepted", runUUID, got)
	}
	if got.JobID != cl.mintedLeaseID || got.JobID == "" {
		t.Fatalf("[%s] JobID=%q want lease id %q", runUUID, got.JobID, cl.mintedLeaseID)
	}
	t.Logf("[%s] before: SubmitWork(w-777,rep=0.91) -> after: SDL recorded + lease %q created (provider %q)",
		runUUID, got.JobID, cl.recordedProvider)
}

// TestAkash_SubmitWork_BadProviderRejected is the LOAD-BEARING MUTATION GUARD
// (closure criterion #3): a provider BELOW the reputation threshold MUST be
// rejected with ErrProviderBelowThreshold and NO lease created. If the gate
// branch `if rep < a.MinReputation { ... }` is removed (or inverted), this
// below-threshold provider would slip through: CreateLease would be called and
// the test fails on both leaseCalls!=0 and err==nil.
func TestAkash_SubmitWork_BadProviderRejected(t *testing.T) {
	const runUUID = "1458a000-0000-4000-8000-0000000000a4"
	cl := &fakeAkashClient{
		bid:        AkashBid{Provider: "akash1bad", PriceAKT: 3.0, OrderID: "ord-bad"},
		reputation: map[string]float64{"akash1bad": 0.20}, // below 0.5 default gate
	}
	a := NewAkashAdapter(cl)

	got, err := a.SubmitWork(context.Background(), WorkSpec{ID: "w-bad", GPUModel: "A100"})
	if !errors.Is(err, ErrProviderBelowThreshold) {
		t.Fatalf("[%s] err=%v want ErrProviderBelowThreshold (reputation gate dead?)", runUUID, err)
	}
	if got != (WorkResult{}) {
		t.Fatalf("[%s] got=%+v want zero WorkResult on rejection", runUUID, got)
	}
	// The crux: NO lease may have been created for a bad provider.
	if cl.leaseCalls != 0 {
		t.Fatalf("[%s] CreateLease called %d times for a below-threshold provider; gate is dead", runUUID, cl.leaseCalls)
	}
	t.Logf("[%s] before: SubmitWork(provider rep=0.20 < 0.50) -> after: ErrProviderBelowThreshold, leaseCalls=0 (no lease)",
		runUUID)
}

// TestAkash_SubmitWork_BoundaryReputation proves the gate is inclusive at the
// threshold: a provider EXACTLY at MinReputation is accepted. This pins the
// comparison to `<` (not `<=`), so mutating it to `<=` would reject this case
// and fail here.
func TestAkash_SubmitWork_BoundaryReputation(t *testing.T) {
	const runUUID = "1458a000-0000-4000-8000-0000000000a5"
	cl := &fakeAkashClient{
		bid:        AkashBid{Provider: "akash1edge", PriceAKT: 3.0},
		reputation: map[string]float64{"akash1edge": 0.5}, // exactly the default gate
	}
	a := NewAkashAdapter(cl)

	_, err := a.SubmitWork(context.Background(), WorkSpec{ID: "w-edge", GPUModel: "A100"})
	if err != nil {
		t.Fatalf("[%s] provider exactly at threshold rejected: %v (gate must be inclusive)", runUUID, err)
	}
	if cl.leaseCalls != 1 {
		t.Fatalf("[%s] CreateLease called %d times want 1 at threshold", runUUID, cl.leaseCalls)
	}
}

// TestAkash_RegistryDispatch proves the AkashAdapter registers and routes under
// its Name() in the shared Registry, alongside a decoy with a DISTINCT
// marketplace string so wrong-routing is detectable.
func TestAkash_RegistryDispatch(t *testing.T) {
	const runUUID = "1458a000-0000-4000-8000-0000000000a6"
	cl := &fakeAkashClient{
		bid:        AkashBid{Provider: "akash1good", PriceAKT: 3.0},
		reputation: map[string]float64{"akash1good": 0.8},
	}
	reg := NewRegistry()
	reg.Register(NewAkashAdapter(cl))
	reg.Register(&fakeAdapter{name: "decoy", market: "DECOY-MARKET"})

	got, err := reg.Dispatch(context.Background(), "akash", WorkSpec{ID: "w-1", GPUModel: "A100"})
	if err != nil {
		t.Fatalf("[%s] Dispatch error: %v", runUUID, err)
	}
	if got.Marketplace != "akash" {
		t.Fatalf("[%s] routed to wrong adapter: Marketplace=%q want %q (decoy would yield %q)",
			runUUID, got.Marketplace, "akash", "DECOY-MARKET")
	}
	if !got.Accepted || got.JobID == "" {
		t.Fatalf("[%s] WorkResult=%+v want accepted with lease id", runUUID, got)
	}
}

// TestAkash_BidErrorPropagates proves a transport/empty-order-book error from
// WinningBid is surfaced (not swallowed) by both methods.
func TestAkash_BidErrorPropagates(t *testing.T) {
	const runUUID = "1458a000-0000-4000-8000-0000000000a7"
	sentinel := errors.New("rpc dial failed")
	cl := &fakeAkashClient{bidErr: sentinel}
	a := NewAkashAdapter(cl)

	if _, err := a.GetCurrentPricing(context.Background(), "A100"); !errors.Is(err, sentinel) {
		t.Fatalf("[%s] GetCurrentPricing err=%v want sentinel", runUUID, err)
	}
	if _, err := a.SubmitWork(context.Background(), WorkSpec{ID: "w", GPUModel: "A100"}); !errors.Is(err, sentinel) {
		t.Fatalf("[%s] SubmitWork err=%v want sentinel", runUUID, err)
	}
	if cl.leaseCalls != 0 {
		t.Fatalf("[%s] no lease should be created when bid query fails; leaseCalls=%d", runUUID, cl.leaseCalls)
	}
}
