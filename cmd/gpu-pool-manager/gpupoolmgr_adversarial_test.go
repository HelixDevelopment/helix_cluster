package main

import (
	"sort"
	"testing"
)

// providersSorted replicates the router's /providers ordering (Tier asc, then
// Name asc) so adversarial tests can assert the cross-consistency invariant
// between Allocate() and the published ordering without an HTTP roundtrip.
func providersSorted(ps []Provider) []Provider {
	out := make([]Provider, len(ps))
	copy(out, ps)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Tier != out[j].Tier {
			return out[i].Tier < out[j].Tier
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// firstHealthy returns the first healthy provider in the supplied (already
// sorted) slice. This is the INDEPENDENT ORACLE for what Allocate() must pick:
// the most-preferred healthy provider in published order.
func firstHealthy(sorted []Provider) (Provider, bool) {
	for _, p := range sorted {
		if p.Healthy {
			return p, true
		}
	}
	return Provider{}, false
}

// TestAllocate_TieBrokenByNameAscending_OrderIndependent probes the documented
// tiebreak promise: "Ties on Tier are broken deterministically by Name
// (ascending)." Two healthy providers share TierLocal; the winner MUST be the
// lexically-smaller name regardless of registration order.
//
// Mutation proof (tiebreak inverted to p.Name > best.Name): registration order
// alone would decide the winner and one of the two sub-cases below FAILS.
func TestAllocate_TieBrokenByNameAscending_OrderIndependent(t *testing.T) {
	const runUUID = "a4e1c0d7-2b88-4f33-9a61-7c5e0d2b9f14"

	orders := [][2]string{
		{"alpha", "bravo"}, // smaller registered first
		{"bravo", "alpha"}, // smaller registered last
	}
	for _, ord := range orders {
		m := NewManager()
		m.Register(Provider{Name: ord[0], Tier: TierLocal, Healthy: true})
		m.Register(Provider{Name: ord[1], Tier: TierLocal, Healthy: true})

		got, ok := m.Allocate()
		if !ok {
			t.Fatalf("order %v: Allocate ok=false, want a winner", ord)
		}
		if got.Name != "alpha" {
			t.Fatalf("order %v: tiebreak winner got %q, want %q (Name ascending)", ord, got.Name, "alpha")
		}
	}
	t.Logf("run %s: equal-tier pair selected lexically-smallest name in BOTH registration orders (delta: tiebreak is order-independent)", runUUID)
}

// TestAllocate_SkipsUnhealthyLowestTier proves a healthy higher-tier provider
// wins over an UNHEALTHY lower-tier provider — i.e. health gates tier
// preference. Mutation (drop the !p.Healthy skip): the unhealthy TierLocal
// provider would wrongly win.
func TestAllocate_SkipsUnhealthyLowestTier(t *testing.T) {
	const runUUID = "0d9b3f2a-6c41-4e58-8b07-2a1f5e9c4d63"

	m := NewManager()
	m.Register(Provider{Name: "local-down", Tier: TierLocal, Healthy: false})
	m.Register(Provider{Name: "cloud-up", Tier: TierCloud, Healthy: true})

	got, ok := m.Allocate()
	if !ok {
		t.Fatalf("Allocate ok=false, want healthy cloud provider")
	}
	if got.Name != "cloud-up" || got.Tier != TierCloud {
		t.Fatalf("winner got %q@Tier%d, want cloud-up@TierCloud (unhealthy TierLocal must be skipped)", got.Name, got.Tier)
	}
	if !got.Healthy {
		t.Fatalf("winner healthy=false; Allocate must never return an unhealthy provider")
	}
	t.Logf("run %s: unhealthy TierLocal skipped, healthy TierCloud selected (delta: health gates tier preference)", runUUID)
}

// TestAllocate_MatchesPublishedOrderFirstHealthy is the CROSS-CONSISTENCY
// invariant pin: Allocate() must return exactly the first HEALTHY provider in
// the same order /providers publishes (Tier asc, Name asc). It exercises a
// crafted mixed pool with unhealthy entries interleaved across tiers and
// duplicate tiers, and compares Allocate against an independent oracle derived
// from the sort. A divergence here would mean a user sees /providers[0]-class
// expectation but Allocate hands back a different node.
func TestAllocate_MatchesPublishedOrderFirstHealthy(t *testing.T) {
	const runUUID = "5f2c8a14-9d03-4b67-a1e9-3c6b0f8d2e47"

	pool := []Provider{
		{Name: "local-b", Tier: TierLocal, Healthy: false},        // lowest tier but DOWN
		{Name: "local-a", Tier: TierLocal, Healthy: false},        // lowest tier but DOWN (smaller name)
		{Name: "cloud-z", Tier: TierCloud, Healthy: true},         // healthy, larger name
		{Name: "cloud-m", Tier: TierCloud, Healthy: true},         // healthy, smaller name -> should win
		{Name: "dec-a", Tier: TierDecentralized, Healthy: true},   // healthy but worst tier
	}
	m := NewManager()
	for _, p := range pool {
		m.Register(p)
	}

	want, wantOK := firstHealthy(providersSorted(m.Providers()))
	got, gotOK := m.Allocate()

	if gotOK != wantOK {
		t.Fatalf("ok mismatch: Allocate ok=%v, oracle ok=%v", gotOK, wantOK)
	}
	if got.Name != want.Name || got.Tier != want.Tier {
		t.Fatalf("Allocate diverged from published-order first-healthy: got %q@Tier%d, want %q@Tier%d",
			got.Name, got.Tier, want.Name, want.Tier)
	}
	if got.Name != "cloud-m" {
		t.Fatalf("hand-derived winner: got %q, want %q", got.Name, "cloud-m")
	}
	t.Logf("run %s: Allocate winner %q equals /providers-order first-healthy oracle (delta: selection and published ordering agree across an interleaved unhealthy pool)", runUUID, got.Name)
}

// TestAllocate_EmptyAndAllUnhealthy_NoZeroPick proves the empty-candidate and
// all-unhealthy paths return ok=false rather than a zero-value Provider pick.
// Mutation (return best,true unconditionally): an empty pool would surface a
// bogus zero Provider{} as a real allocation.
func TestAllocate_EmptyAndAllUnhealthy_NoZeroPick(t *testing.T) {
	const runUUID = "c7140e6b-8a52-4f19-9d03-6b2e1a4c8f50"

	// Empty pool.
	if got, ok := NewManager().Allocate(); ok {
		t.Fatalf("empty pool: Allocate ok=true with %+v, want ok=false", got)
	}

	// All unhealthy.
	m := NewManager()
	m.Register(Provider{Name: "a", Tier: TierLocal, Healthy: false})
	m.Register(Provider{Name: "b", Tier: TierCloud, Healthy: false})
	if got, ok := m.Allocate(); ok {
		t.Fatalf("all-unhealthy pool: Allocate ok=true with %+v, want ok=false", got)
	}
	t.Logf("run %s: empty and all-unhealthy pools both returned ok=false (delta: no zero-value Provider leaked as an allocation)", runUUID)
}

// TestAllocate_ExoticTierValues_NumericOrdering probes that ordering is a pure
// numeric Tier comparison with no special-casing of the named constants:
// a provider with a Tier numerically LOWER than TierLocal (including zero and
// negative) must out-prefer TierLocal, and an out-of-range high Tier must lose.
// This guards against a mutation that hardcodes the three known tiers.
func TestAllocate_ExoticTierValues_NumericOrdering(t *testing.T) {
	const runUUID = "9a3d51c8-4e70-4b22-8f16-0c5e2d9b7a31"

	m := NewManager()
	m.Register(Provider{Name: "normal", Tier: TierLocal, Healthy: true})
	m.Register(Provider{Name: "super", Tier: TierLocal - 5, Healthy: true})    // numerically lower
	m.Register(Provider{Name: "exotic", Tier: TierDecentralized + 99, Healthy: true}) // worst

	got, ok := m.Allocate()
	if !ok {
		t.Fatalf("Allocate ok=false, want a winner")
	}
	if got.Name != "super" {
		t.Fatalf("winner got %q@Tier%d, want %q (numerically-lowest Tier)", got.Name, got.Tier, "super")
	}
	t.Logf("run %s: numerically-lowest Tier (%d) won over TierLocal and an out-of-range high tier (delta: ordering is pure numeric, not hardcoded to named constants)", runUUID, got.Tier)
}
