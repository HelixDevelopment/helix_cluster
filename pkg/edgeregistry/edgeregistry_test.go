package edgeregistry

import (
	"errors"
	"reflect"
	"testing"
)

const runUUID = "edgeregistry-run-3f1c2a9e-7b4d-4c61-9a2e-8d5f0c1b6e44"

// allTiers is the closed admissible range T3..T8.
var allTiers = []Tier{T3, T4, T5, T6, T7, T8}

// makeReg builds a registration whose attributes are uniquely derived from the
// tier, so a wrong tier/trust/cap assignment in the dump is detectable.
func makeReg(t Tier) Registration {
	return Registration{
		NodeID: "node-" + t.String(),
		Tier:   t,
		Trust:  TrustLevel(int(t) % 4), // varies across tiers: 3->3,4->0,5->1,6->2,7->3,8->0
		Caps: Capability{
			ComputeClass: "class-" + t.String(),
			MemoryMB:     1000 * int(t),
			Backends:     []string{"backend-" + t.String()},
		},
	}
}

// TestClause1_DumpEnumeratesEachTierWithAttributes covers CLOSURE clause 1:
// register one device of EACH tier T3..T8 and assert Dump enumerates each node
// exactly once with the CORRECT tier label + trust level + capability advert.
//
// Mutation: if Register drops the tier-label assignment (e.g. stores Tier 0),
// the per-tier attribute checks below fail (wrong tier label, empty/zero caps).
func TestClause1_DumpEnumeratesEachTierWithAttributes(t *testing.T) {
	r := New()

	// BEFORE: registry empty / tiers unrecognized.
	t.Logf("run=%s BEFORE len=%d dump=%v", runUUID, r.Len(), r.Dump())
	if r.Len() != 0 {
		t.Fatalf("expected empty registry before, got len=%d", r.Len())
	}
	if got := r.Dump(); len(got) != 0 {
		t.Fatalf("expected empty dump before, got %v", got)
	}

	want := make(map[string]Registration)
	for _, tier := range allTiers {
		reg := makeReg(tier)
		want[reg.NodeID] = reg
		if err := r.Register(reg); err != nil {
			t.Fatalf("Register(%s) unexpected error: %v", tier, err)
		}
	}

	dump := r.Dump()
	t.Logf("run=%s AFTER len=%d dump=%v", runUUID, r.Len(), dump)

	if len(dump) != len(allTiers) {
		t.Fatalf("expected %d dumped nodes, got %d", len(allTiers), len(dump))
	}

	// Dump must be sorted by node id deterministically.
	for i := 1; i < len(dump); i++ {
		if dump[i-1].NodeID >= dump[i].NodeID {
			t.Fatalf("dump not sorted ascending: %q >= %q", dump[i-1].NodeID, dump[i].NodeID)
		}
	}

	// Each tier appears exactly once with its expected attributes.
	seenTier := make(map[Tier]int)
	for _, got := range dump {
		exp, ok := want[got.NodeID]
		if !ok {
			t.Fatalf("dump contained unexpected node %q", got.NodeID)
		}
		seenTier[got.Tier]++
		if got.Tier != exp.Tier {
			t.Errorf("node %q tier = %v, want %v", got.NodeID, got.Tier, exp.Tier)
		}
		// The string label must be the real Tn label, proving the tier was stored.
		if got.Tier.String() != exp.Tier.String() || got.Tier.String() == "" {
			t.Errorf("node %q tier label = %q, want %q", got.NodeID, got.Tier.String(), exp.Tier.String())
		}
		if got.Trust != exp.Trust {
			t.Errorf("node %q trust = %v, want %v", got.NodeID, got.Trust, exp.Trust)
		}
		if !reflect.DeepEqual(got.Caps, exp.Caps) {
			t.Errorf("node %q caps = %+v, want %+v", got.NodeID, got.Caps, exp.Caps)
		}
	}
	for _, tier := range allTiers {
		if seenTier[tier] != 1 {
			t.Errorf("tier %v appeared %d times in dump, want exactly 1", tier, seenTier[tier])
		}
	}

	// Get must return the same attributes as the dump (sink-side cross-check).
	got, err := r.Get("node-T6")
	if err != nil {
		t.Fatalf("Get(node-T6) error: %v", err)
	}
	if got.Tier != T6 || got.Caps.MemoryMB != 6000 {
		t.Errorf("Get(node-T6) = tier %v mem %d, want T6 mem 6000", got.Tier, got.Caps.MemoryMB)
	}
}

// TestClause2_ListByTier covers CLOSURE clause 2: ListByTier returns only nodes
// of that tier; a tier with no nodes returns empty.
//
// Mutation: if ListByTier dropped its `reg.Tier == tier` predicate it would
// return nodes of other tiers, failing the membership and empty-tier checks.
func TestClause2_ListByTier(t *testing.T) {
	r := New()

	// Two nodes on T4, one on T7, none on T3/T5/T6/T8.
	regs := []Registration{
		{NodeID: "a", Tier: T4, Trust: TrustBasic, Caps: Capability{ComputeClass: "cpu", MemoryMB: 512}},
		{NodeID: "b", Tier: T4, Trust: TrustAttested, Caps: Capability{ComputeClass: "gpu", MemoryMB: 2048}},
		{NodeID: "z", Tier: T7, Trust: TrustVerified, Caps: Capability{ComputeClass: "npu", MemoryMB: 256}},
	}
	t.Logf("run=%s BEFORE T4=%v T7=%v", runUUID, r.ListByTier(T4), r.ListByTier(T7))
	for _, reg := range regs {
		if err := r.Register(reg); err != nil {
			t.Fatalf("Register(%s) error: %v", reg.NodeID, err)
		}
	}

	t4 := r.ListByTier(T4)
	t7 := r.ListByTier(T7)
	t.Logf("run=%s AFTER T4=%v T7=%v", runUUID, t4, t7)

	if len(t4) != 2 {
		t.Fatalf("ListByTier(T4) len = %d, want 2", len(t4))
	}
	// Sorted by id => a then b.
	if t4[0].NodeID != "a" || t4[1].NodeID != "b" {
		t.Errorf("ListByTier(T4) ids = %q,%q want a,b", t4[0].NodeID, t4[1].NodeID)
	}
	for _, reg := range t4 {
		if reg.Tier != T4 {
			t.Errorf("ListByTier(T4) returned node %q of tier %v", reg.NodeID, reg.Tier)
		}
	}

	if len(t7) != 1 || t7[0].NodeID != "z" || t7[0].Tier != T7 {
		t.Errorf("ListByTier(T7) = %v, want single node z@T7", t7)
	}

	// Tiers with no nodes return empty (non-nil) slices.
	for _, empty := range []Tier{T3, T5, T6, T8} {
		got := r.ListByTier(empty)
		if got == nil {
			t.Errorf("ListByTier(%v) returned nil, want empty non-nil slice", empty)
		}
		if len(got) != 0 {
			t.Errorf("ListByTier(%v) len = %d, want 0 (nodes: %v)", empty, len(got), got)
		}
	}
}

// TestClause3_ValidationAndReRegistration covers CLOSURE clause 3: invalid tier
// or empty node id rejected with typed error; re-registration updates in place.
//
// Mutation: removing the tier-range validation in Register makes the
// out-of-range cases below succeed, failing this test; dropping the in-place
// update (e.g. appending) would change Len and fail the re-registration check.
func TestClause3_ValidationAndReRegistration(t *testing.T) {
	r := New()
	t.Logf("run=%s BEFORE len=%d", runUUID, r.Len())

	// Empty node id rejected.
	err := r.Register(Registration{NodeID: "", Tier: T5})
	if !errors.Is(err, ErrEmptyNodeID) {
		t.Fatalf("empty id: err = %v, want ErrEmptyNodeID", err)
	}

	// Out-of-range tiers rejected (just below T3 and just above T8, plus zero).
	for _, bad := range []Tier{0, 1, 2, 9, 100, -1} {
		err := r.Register(Registration{NodeID: "x", Tier: bad})
		if !errors.Is(err, ErrTierOutOfRange) {
			t.Errorf("tier %d: err = %v, want ErrTierOutOfRange", int(bad), err)
		}
	}
	if r.Len() != 0 {
		t.Fatalf("rejected registrations leaked into registry, len=%d", r.Len())
	}

	// Boundary tiers T3 and T8 are accepted.
	for _, ok := range []Tier{T3, T8} {
		if err := r.Register(Registration{NodeID: "boundary-" + ok.String(), Tier: ok, Trust: TrustBasic}); err != nil {
			t.Fatalf("boundary tier %v rejected: %v", ok, err)
		}
	}

	// Get on unknown id reports not-found.
	if _, err := r.Get("does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(unknown): err = %v, want ErrNotFound", err)
	}

	// Re-registration updates in place — no duplicate.
	first := Registration{NodeID: "edge-1", Tier: T4, Trust: TrustBasic, Caps: Capability{ComputeClass: "cpu", MemoryMB: 512}}
	if err := r.Register(first); err != nil {
		t.Fatalf("Register first: %v", err)
	}
	lenAfterFirst := r.Len()

	updated := Registration{NodeID: "edge-1", Tier: T6, Trust: TrustVerified, Caps: Capability{ComputeClass: "gpu", MemoryMB: 4096}}
	if err := r.Register(updated); err != nil {
		t.Fatalf("Register update: %v", err)
	}
	if r.Len() != lenAfterFirst {
		t.Errorf("re-registration changed Len from %d to %d (duplicate created)", lenAfterFirst, r.Len())
	}

	got, err := r.Get("edge-1")
	if err != nil {
		t.Fatalf("Get(edge-1): %v", err)
	}
	t.Logf("run=%s AFTER edge-1=%+v len=%d", runUUID, got, r.Len())
	if got.Tier != T6 || got.Trust != TrustVerified || got.Caps.ComputeClass != "gpu" || got.Caps.MemoryMB != 4096 {
		t.Errorf("re-registration did not update in place: got %+v", got)
	}
}

// TestStoredCopyIsolation proves the registry deep-copies registrations so a
// caller mutating its input/output slices cannot corrupt stored state.
//
// Mutation: if Register stored reg without cloning Caps.Backends, mutating the
// caller's backing array would change the stored value and fail this test.
func TestStoredCopyIsolation(t *testing.T) {
	r := New()
	backends := []string{"orig"}
	reg := Registration{NodeID: "n1", Tier: T5, Trust: TrustBasic, Caps: Capability{ComputeClass: "cpu", MemoryMB: 100, Backends: backends}}
	if err := r.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Mutate caller's slice after registering.
	backends[0] = "tampered"

	got, err := r.Get("n1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Logf("run=%s stored backends=%v", runUUID, got.Caps.Backends)
	if got.Caps.Backends[0] != "orig" {
		t.Errorf("stored backend mutated via caller alias: got %q, want orig", got.Caps.Backends[0])
	}
}
