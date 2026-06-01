package discovery

import (
	"context"
	"errors"
	"testing"
	"time"
)

// idsOf returns the set of instance IDs for exact-set assertions.
func idsOf(insts []*Instance) map[string]bool {
	out := make(map[string]bool, len(insts))
	for _, i := range insts {
		out[i.ID] = true
	}
	return out
}

// seedTrustFleet registers a fixed fleet spanning several trust tiers and
// returns the registry. Each instance is on the same service "compute".
func seedTrustFleet(t *testing.T) *TrustRegistry {
	t.Helper()
	clk := &fakeClock{now: time.Unix(1000, 0)}
	tr := NewStaticTrustRegistry(WithClock(clk))
	ctx := context.Background()

	fleet := []struct {
		id   string
		tier TrustTier
	}{
		{"root-1", TrustRoot},              // 15
		{"dc-1", TrustDatacenter},          // 11 (== TrustHigh)
		{"dc-2", TrustDatacenter},          // 11
		{"internal-1", TrustInternal},      // 8  (== TrustMedium)
		{"volunteer-1", TrustVolunteer},    // 2
		{"public-1", TrustPublicAnonymous}, // 1
	}
	for _, f := range fleet {
		if err := tr.RegisterWithTrust(ctx, &Instance{
			ID: f.id, Service: "compute", TTL: time.Hour,
		}, f.tier); err != nil {
			t.Fatalf("register %s: %v", f.id, err)
		}
	}
	return tr
}

// --- TrustTier ordering / parsing ---

func TestTrustTier_OrderingAndRoundTrip(t *testing.T) {
	// The ladder MUST be strictly increasing root > dc > internal > volunteer.
	if !(TrustRoot > TrustDatacenter && TrustDatacenter > TrustInternal &&
		TrustInternal > TrustVolunteer && TrustVolunteer > TrustUnknown) {
		t.Fatalf("trust ladder not monotonic: root=%d dc=%d internal=%d vol=%d unknown=%d",
			TrustRoot, TrustDatacenter, TrustInternal, TrustVolunteer, TrustUnknown)
	}
	// Metadata round-trip must preserve the exact tier.
	inst := &Instance{ID: "x", Metadata: trustToMeta(nil, TrustAttested)}
	if got := TrustOf(inst); got != TrustAttested {
		t.Fatalf("trust round-trip: got %s, want %s", got, TrustAttested)
	}
}

func TestTrustOf_UntaggedIsUnknown(t *testing.T) {
	// An instance with no trust tag is the LEAST trusted tier so it can never
	// satisfy a bar above unknown.
	// Mutation: returning a high default (e.g. TrustRoot) from TrustOf would make
	// this AtLeast(TrustHigh) true and fail the assertion.
	if got := TrustOf(&Instance{ID: "x"}); got != TrustUnknown {
		t.Fatalf("untagged instance must be TrustUnknown, got %s", got)
	}
	if TrustOf(&Instance{ID: "x"}).AtLeast(TrustHigh) {
		t.Fatal("untagged instance must NOT satisfy a high-trust bar")
	}
}

// --- trust>=HIGH returns EXACTLY the high-trust instances ---

func TestLookupMinTrust_HighReturnsExactlyHighTrust(t *testing.T) {
	tr := seedTrustFleet(t)
	got, err := tr.LookupMinTrust(context.Background(), "compute", TrustHigh)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	want := map[string]bool{"root-1": true, "dc-1": true, "dc-2": true}
	gotSet := idsOf(got)
	if len(gotSet) != len(want) {
		t.Fatalf("trust>=HIGH set size: got %v, want %v", gotSet, want)
	}
	for id := range want {
		if !gotSet[id] {
			t.Fatalf("trust>=HIGH missing %s; got %v", id, gotSet)
		}
	}
	// Explicitly assert the LOW-trust nodes are EXCLUDED (sink-side).
	for _, bad := range []string{"internal-1", "volunteer-1", "public-1"} {
		if gotSet[bad] {
			t.Fatalf("trust>=HIGH leaked low-trust node %s: got %v", bad, gotSet)
		}
	}
}

func TestLookupMinTrust_IgnoringTier_Mutation(t *testing.T) {
	// Mutation: replace `TrustOf(inst).AtLeast(min)` in filterByMinTrust with a
	// constant `true` (filter ignores the tier). Then the trust>=HIGH query would
	// also return internal-1/volunteer-1/public-1 and this exact-set check fails.
	tr := seedTrustFleet(t)
	got, _ := tr.LookupMinTrust(context.Background(), "compute", TrustHigh)
	gotSet := idsOf(got)
	if len(gotSet) != 3 {
		t.Fatalf("expected EXACTLY 3 high-trust instances, got %d: %v", len(gotSet), gotSet)
	}
	if gotSet["volunteer-1"] || gotSet["public-1"] || gotSet["internal-1"] {
		t.Fatalf("low-trust node returned by a high-trust filter: %v", gotSet)
	}
}

func TestLookupMinTrust_OrderingHighestFirst(t *testing.T) {
	// Deterministic ordering: descending trust then ID. root(15) before dc(11),
	// and the two dc nodes ordered by ID.
	// Mutation: removing sortByTrustDescThenID (or sorting ascending) breaks this.
	tr := seedTrustFleet(t)
	got, _ := tr.LookupMinTrust(context.Background(), "compute", TrustMedium)
	wantOrder := []string{"root-1", "dc-1", "dc-2", "internal-1"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d instances, want %d: %v", len(got), len(wantOrder), idsOf(got))
	}
	gotOrder := make([]string, len(got))
	for i, g := range got {
		gotOrder[i] = g.ID
	}
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Fatalf("order[%d]: got %s, want %s (full: %v)", i, got[i].ID, id, gotOrder)
		}
	}
}

// --- lowering trust removes instance from a high-trust query (sink-side) ---

func TestSetTrust_LoweringRemovesFromHighQuery(t *testing.T) {
	tr := seedTrustFleet(t)
	ctx := context.Background()

	// dc-1 starts as high-trust and is in the >=HIGH set.
	before, _ := tr.LookupMinTrust(ctx, "compute", TrustHigh)
	if !idsOf(before)["dc-1"] {
		t.Fatalf("precondition: dc-1 should be high-trust, got %v", idsOf(before))
	}

	// Lower dc-1 to a volunteer tier.
	if err := tr.SetTrust(ctx, "compute", "dc-1", TrustVolunteer); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}

	// Sink-side: re-query the backend; dc-1 must be gone from >=HIGH.
	after, _ := tr.LookupMinTrust(ctx, "compute", TrustHigh)
	afterSet := idsOf(after)
	if afterSet["dc-1"] {
		t.Fatalf("dc-1 still in high-trust query after lowering: %v", afterSet)
	}
	// But it must still exist at its new (low) tier.
	low, _ := tr.LookupMinTrust(ctx, "compute", TrustVolunteer)
	if !idsOf(low)["dc-1"] {
		t.Fatalf("dc-1 vanished entirely after lowering trust: %v", idsOf(low))
	}
}

func TestSetTrust_Mutation(t *testing.T) {
	// Mutation: make SetTrust update only the local cache (skip backend.Put).
	// Then the sink-side re-query (which reads the backend via Lookup) would
	// still see dc-1 as high-trust and this assertion fails.
	tr := seedTrustFleet(t)
	ctx := context.Background()
	_ = tr.SetTrust(ctx, "compute", "dc-1", TrustVolunteer)
	after, _ := tr.LookupMinTrust(ctx, "compute", TrustHigh)
	if idsOf(after)["dc-1"] {
		t.Fatal("SetTrust did not propagate to backend: dc-1 still high-trust")
	}
}

func TestSetTrust_UnknownInstance(t *testing.T) {
	tr := seedTrustFleet(t)
	err := tr.SetTrust(context.Background(), "compute", "ghost", TrustRoot)
	if !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("expected ErrInstanceNotFound, got %v", err)
	}
}

// --- policy hook rejects a sensitive lookup when nothing meets the bar ---

func TestRequireTrust_RejectsWhenBarUnmet(t *testing.T) {
	// Fleet tops out at TrustRoot, but ask for... nothing tops out below.
	// Build a fleet with NO instance at the required bar.
	clk := &fakeClock{now: time.Unix(1000, 0)}
	tr := NewStaticTrustRegistry(WithClock(clk))
	ctx := context.Background()
	_ = tr.RegisterWithTrust(ctx, &Instance{ID: "v1", Service: "secure", TTL: time.Hour}, TrustVolunteer)
	_ = tr.RegisterWithTrust(ctx, &Instance{ID: "p1", Service: "secure", TTL: time.Hour}, TrustPublicAnonymous)

	// Sensitive workload requires datacenter-grade trust: must be DENIED.
	got, err := tr.RequireTrust(ctx, "secure", TrustHigh)
	if !errors.Is(err, ErrTrustPolicyDenied) {
		t.Fatalf("expected ErrTrustPolicyDenied, got err=%v (instances=%v)", err, idsOf(got))
	}
	if got != nil {
		t.Fatalf("denied lookup must return nil instances, got %v", idsOf(got))
	}
}

func TestRequireTrust_AdmitsWhenBarMet(t *testing.T) {
	tr := seedTrustFleet(t)
	got, err := tr.RequireTrust(context.Background(), "compute", TrustHigh)
	if err != nil {
		t.Fatalf("expected admit, got %v", err)
	}
	want := map[string]bool{"root-1": true, "dc-1": true, "dc-2": true}
	if gs := idsOf(got); len(gs) != 3 || !gs["root-1"] || !gs["dc-1"] || !gs["dc-2"] {
		t.Fatalf("admitted set wrong: got %v want %v", gs, want)
	}
}

func TestRequireTrust_PolicyHook_Mutation(t *testing.T) {
	// Mutation: have RequireTrust ignore the policy result (drop the Authorize
	// error check and always return candidates). Then a bar that nothing meets
	// would return an empty-but-non-error result and this assertion fails.
	clk := &fakeClock{now: time.Unix(1000, 0)}
	tr := NewStaticTrustRegistry(WithClock(clk))
	ctx := context.Background()
	_ = tr.RegisterWithTrust(ctx, &Instance{ID: "v1", Service: "secure", TTL: time.Hour}, TrustVolunteer)

	_, err := tr.RequireTrust(ctx, "secure", TrustSecureEnclave)
	if err == nil {
		t.Fatal("policy hook must reject sensitive lookup when no instance meets the bar")
	}
}

func TestRequireTrust_CustomPolicy(t *testing.T) {
	// A custom policy that refuses ANY volunteer/public node even if the min bar
	// is low proves the hook is actually consulted (not hard-wired to min-only).
	clk := &fakeClock{now: time.Unix(1000, 0)}
	reg := NewStaticRegistry(WithClock(clk))
	tr := NewTrustRegistry(reg, refuseUntrustedPolicy{})
	ctx := context.Background()
	_ = tr.RegisterWithTrust(ctx, &Instance{ID: "v1", Service: "secure", TTL: time.Hour}, TrustVolunteer)

	// min is TrustUnknown (lowest), so the default policy would ADMIT; the custom
	// one must DENY because the only candidate is a volunteer node.
	_, err := tr.RequireTrust(ctx, "secure", TrustUnknown)
	if !errors.Is(err, errCustomRefused) {
		t.Fatalf("custom policy not consulted: got %v", err)
	}
}

var errCustomRefused = errors.New("custom: untrusted node refused")

type refuseUntrustedPolicy struct{}

func (refuseUntrustedPolicy) Authorize(service string, min TrustTier, candidates []*Instance) error {
	for _, c := range candidates {
		if TrustOf(c) <= TrustVolunteer {
			return errCustomRefused
		}
	}
	if len(candidates) == 0 {
		return errCustomRefused
	}
	return nil
}

// --- SelectTrusted never falls back below the bar ---

func TestSelectTrusted_NeverPicksBelowBar(t *testing.T) {
	tr := seedTrustFleet(t)
	ctx := context.Background()
	// Run several picks; every pick MUST be at >= HIGH.
	for i := 0; i < 10; i++ {
		pick, err := tr.SelectTrusted(ctx, "compute", TrustHigh)
		if err != nil {
			t.Fatalf("select trusted: %v", err)
		}
		if !TrustOf(pick).AtLeast(TrustHigh) {
			t.Fatalf("SelectTrusted returned below-bar node %s at tier %s", pick.ID, TrustOf(pick))
		}
	}
}

func TestSelectTrusted_NoEligible_Mutation(t *testing.T) {
	// Mutation: have SelectTrusted draw from the unfiltered Lookup instead of the
	// trust-filtered set. Then asking for TrustRoot when only volunteer nodes
	// exist would wrongly return a volunteer node instead of ErrNoInstances.
	clk := &fakeClock{now: time.Unix(1000, 0)}
	tr := NewStaticTrustRegistry(WithClock(clk))
	ctx := context.Background()
	_ = tr.RegisterWithTrust(ctx, &Instance{ID: "v1", Service: "secure", TTL: time.Hour}, TrustVolunteer)

	pick, err := tr.SelectTrusted(ctx, "secure", TrustRoot)
	if !errors.Is(err, ErrNoInstances) {
		t.Fatalf("expected ErrNoInstances, got pick=%v err=%v", pick, err)
	}
	if pick != nil {
		t.Fatalf("must not fall back to below-bar node, got %s", pick.ID)
	}
}

// --- RegisterWithTrust does not mutate caller's metadata; invalid tier rejected ---

func TestRegisterWithTrust_DoesNotMutateCallerMeta(t *testing.T) {
	tr := NewStaticTrustRegistry()
	meta := map[string]string{"zone": "eu-1"}
	inst := &Instance{ID: "i1", Service: "svc", TTL: time.Hour, Metadata: meta}
	if err := tr.RegisterWithTrust(context.Background(), inst, TrustDatacenter); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Caller's map must be untouched (no trust key leaked in).
	if _, leaked := meta["helix.trust.tier"]; leaked {
		t.Fatal("RegisterWithTrust mutated the caller's metadata map")
	}
	// And the stored instance must carry the tier.
	got, _ := tr.LookupMinTrust(context.Background(), "svc", TrustHigh)
	if len(got) != 1 || TrustOf(got[0]) != TrustDatacenter {
		t.Fatalf("stored trust wrong: %v", got)
	}
}

func TestRegisterWithTrust_InvalidTier(t *testing.T) {
	tr := NewStaticTrustRegistry()
	err := tr.RegisterWithTrust(context.Background(),
		&Instance{ID: "i1", Service: "svc"}, TrustTier(999))
	if !errors.Is(err, ErrInvalidTrustTier) {
		t.Fatalf("expected ErrInvalidTrustTier, got %v", err)
	}
}

// --- Backward-compat: embedded ServiceRegistry methods still work unchanged ---

func TestTrustRegistry_PlainRegisterStillWorks(t *testing.T) {
	tr := NewStaticTrustRegistry()
	// Use the embedded plain Register (no trust): it must behave exactly as before
	// and the instance reads back as TrustUnknown.
	if err := tr.Register(context.Background(), &Instance{ID: "i1", Service: "svc", TTL: time.Hour}); err != nil {
		t.Fatalf("plain register: %v", err)
	}
	list, _ := tr.Lookup(context.Background(), "svc")
	if len(list) != 1 || list[0].ID != "i1" {
		t.Fatalf("embedded Lookup broken: %v", list)
	}
	if TrustOf(list[0]) != TrustUnknown {
		t.Fatalf("plain-registered instance should be TrustUnknown, got %s", TrustOf(list[0]))
	}
}
