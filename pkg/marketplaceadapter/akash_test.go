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
	// Sink-side: the SDL manifest was submitted and embeds the work id + GPU model
	// as double-quoted YAML scalars (the injection-safe encoding).
	if !strings.Contains(cl.recordedSDL, `image: "helix/work:w-777"`) {
		t.Fatalf("[%s] recorded SDL missing work id; got:\n%s", runUUID, cl.recordedSDL)
	}
	if !strings.Contains(cl.recordedSDL, `model: "A100"`) {
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
// branch `if rep < a.effectiveMinReputation() { ... }` is removed (or inverted),
// this below-threshold provider would slip through: CreateLease would be called
// and the test fails on both leaseCalls!=0 and err==nil.
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

// TestAkash_SubmitWork_ZeroValueAdapterGate is the LOAD-BEARING guard for the
// HXC-918 reputation-gate-bypass finding: a directly-constructed AkashAdapter{}
// (NOT via NewAkashAdapter) has MinReputation==0. Without the safe-default at the
// admission check, a zero-reputation provider would be admitted because
// `rep(0) < min(0)` is false — a silent bypass of the 0.5 floor. This test pins
// that a zero-value adapter rejects a below-safe-default provider and creates NO
// lease. Removing the `effectiveMinReputation` clamp makes this fail.
func TestAkash_SubmitWork_ZeroValueAdapterGate(t *testing.T) {
	const runUUID = "1458a000-0000-4000-8000-0000000000a8"
	cl := &fakeAkashClient{
		bid:        AkashBid{Provider: "akash1zero", PriceAKT: 3.0, OrderID: "ord-zero"},
		reputation: map[string]float64{"akash1zero": 0.0}, // zero reputation
	}
	// Directly constructed — MinReputation is the zero value, bypassing the
	// constructor's 0.5 default.
	a := &AkashAdapter{Client: cl}

	got, err := a.SubmitWork(context.Background(), WorkSpec{ID: "w-zero", GPUModel: "A100"})
	if !errors.Is(err, ErrProviderBelowThreshold) {
		t.Fatalf("[%s] err=%v want ErrProviderBelowThreshold (zero-value adapter must not admit rep=0)", runUUID, err)
	}
	if got != (WorkResult{}) {
		t.Fatalf("[%s] got=%+v want zero WorkResult on rejection", runUUID, got)
	}
	if cl.leaseCalls != 0 {
		t.Fatalf("[%s] CreateLease called %d times via zero-value adapter; gate bypassed", runUUID, cl.leaseCalls)
	}
	t.Logf("[%s] before: AkashAdapter{}.SubmitWork(rep=0.0) -> after: ErrProviderBelowThreshold, leaseCalls=0", runUUID)
}

// TestAkash_SubmitWork_ZeroValueAdapterAdmitsAboveDefault proves the zero-value
// adapter still ADMITS a provider at/above the safe default (0.5), so the clamp
// uses the 0.5 floor — not an unconditional reject.
func TestAkash_SubmitWork_ZeroValueAdapterAdmitsAboveDefault(t *testing.T) {
	const runUUID = "1458a000-0000-4000-8000-0000000000a9"
	cl := &fakeAkashClient{
		bid:        AkashBid{Provider: "akash1ok", PriceAKT: 3.0},
		reputation: map[string]float64{"akash1ok": 0.5}, // exactly the safe default
	}
	a := &AkashAdapter{Client: cl}

	if _, err := a.SubmitWork(context.Background(), WorkSpec{ID: "w-ok", GPUModel: "A100"}); err != nil {
		t.Fatalf("[%s] provider at safe-default threshold rejected by zero-value adapter: %v", runUUID, err)
	}
	if cl.leaseCalls != 1 {
		t.Fatalf("[%s] CreateLease called %d times want 1", runUUID, cl.leaseCalls)
	}
}

// TestAkash_BuildSDL_InjectionContained is the LOAD-BEARING guard for the
// HXC-918 SDL-manifest-injection finding. A caller-controlled WorkSpec.ID
// containing YAML/manifest metacharacters (newlines, ':', indentation, a '---'
// document separator) MUST NOT inject manifest structure: the value must stay a
// single contained YAML scalar. We assert the produced manifest has exactly the
// expected number of services/top-level keys and that no injected key/document
// leaked into structural position. Removing the sanitization (reverting to a raw
// fmt.Sprintf splice) makes this fail.
func TestAkash_BuildSDL_InjectionContained(t *testing.T) {
	const runUUID = "1458a000-0000-4000-8000-0000000000aa"

	// A hostile ID attempting to break out of the `image:` scalar and inject a
	// second service + a new top-level mapping + a YAML document separator.
	malicious := "evil\"\n    image: \"pwned\"\n  attacker:\n    image: \"x\"\n---\ninjected: true\n: weird"
	spec := WorkSpec{ID: malicious, GPUModel: "A100"}

	sdl := buildSDL(spec)

	// 1) No new YAML document may be injected.
	if strings.Contains(sdl, "\n---") || strings.HasPrefix(sdl, "---\n") {
		t.Fatalf("[%s] injected YAML document separator leaked into manifest:\n%s", runUUID, sdl)
	}
	// 2) No injected top-level key may appear at column 0 (structural position).
	for _, bad := range []string{"\ninjected:", "\nattacker:"} {
		if strings.Contains(sdl, bad) {
			t.Fatalf("[%s] injected top-level key %q leaked into manifest:\n%s", runUUID, bad, sdl)
		}
	}
	// 3) The manifest must still parse to the SAME structure as a benign spec:
	// exactly one service named "work" and exactly the known top-level keys. We
	// verify structurally by counting lines at each indentation that begin a key.
	if got := countTopLevelKeys(sdl); got != 2 { // "version" and "services"
		t.Fatalf("[%s] manifest gained injected top-level keys: have %d top-level keys want 2:\n%s", runUUID, got, sdl)
	}
	if got := countServices(sdl); got != 1 { // only "work"
		t.Fatalf("[%s] manifest gained injected services: have %d want 1:\n%s", runUUID, got, sdl)
	}
	// 4) The dangerous newline must NOT survive as a structural line break in the
	// image value: the encoded scalar must be a single physical line containing
	// the escaped content (the raw newline turned into the two-char sequence \n).
	for _, line := range strings.Split(sdl, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "image:") {
			// The whole image value lives on this one line; the raw injected
			// newline must have been escaped, so "pwned" cannot appear as its own
			// structural token on a separate line.
			if strings.Contains(sdl, "\n    image: \"pwned\"") {
				t.Fatalf("[%s] injected second image scalar leaked structurally:\n%s", runUUID, sdl)
			}
		}
	}
	t.Logf("[%s] before: buildSDL(malicious ID) -> after: 2 top-level keys, 1 service, no injected document/keys", runUUID)
}

// countTopLevelKeys counts lines that start a key at column 0 (no leading space),
// i.e. "key:" — the structural top level of the SDL mapping.
func countTopLevelKeys(sdl string) int {
	n := 0
	for _, line := range strings.Split(sdl, "\n") {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue // indented => not top level
		}
		// A top-level key line looks like `name:` or `name: value`.
		if i := strings.IndexByte(line, ':'); i > 0 {
			n++
		}
	}
	return n
}

// countServices counts service entries: keys indented exactly under "services:".
// Service entries are at 2-space indentation directly below the services mapping.
func countServices(sdl string) int {
	lines := strings.Split(sdl, "\n")
	inServices := false
	n := 0
	for _, line := range lines {
		if line == "services:" {
			inServices = true
			continue
		}
		if !inServices {
			continue
		}
		if line == "" {
			continue
		}
		// Leaving the services block: a column-0 line ends it.
		if line[0] != ' ' && line[0] != '\t' {
			inServices = false
			continue
		}
		// A service entry is at exactly 2 spaces of indent and ends with ':'.
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
			trimmed := strings.TrimSpace(line)
			if strings.HasSuffix(trimmed, ":") {
				n++
			}
		}
	}
	return n
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
