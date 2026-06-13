package swim

// Tests for HierarchicalTopology (HXC-1344).
//
// ANTI-BLUFF discipline: every test names the EXACT single-line mutation of
// production code that would cause it to fail, and asserts CONCRETE ID sets,
// never just len>0 or err==nil.

import (
	"fmt"
	"sort"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeAlive returns a *Member with StateAlive and the given zone metadata.
func makeAlive(id, zone string) *Member {
	return &Member{
		ID:      id,
		Address: "127.0.0.1:0",
		State:   StateAlive,
		Metadata: map[string]string{
			"zone": zone,
		},
	}
}

// makeDead returns a *Member with StateDead (should be excluded from gossip).
func makeDead(id, zone string) *Member {
	return &Member{
		ID:      id,
		Address: "127.0.0.1:0",
		State:   StateDead,
		Metadata: map[string]string{
			"zone": zone,
		},
	}
}

// memberIDs extracts and sorts IDs from a []*Member slice.
func memberIDs(ms []*Member) []string {
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	sort.Strings(ids)
	return ids
}

// strSet converts a []string to a map[string]bool for set operations.
func strSet(ss []string) map[string]bool {
	out := make(map[string]bool, len(ss))
	for _, s := range ss {
		out[s] = true
	}
	return out
}

// ---------------------------------------------------------------------------
// 1. Determinism and exact delegate count
// ---------------------------------------------------------------------------

// TestDelegatesDeterminismAndExactCount verifies:
//
//	(a) Delegates(g) returns EXACTLY K members for each group across 10
//	    independent reconstructions of the topology.
//	(b) The exact same IDs are returned on every reconstruction (determinism).
//	(c) The elected IDs are independently confirmed to be the K members with
//	    the lowest stableHash(ID) in their group (correctness, not just
//	    stability).
//	(d) Non-gossip-eligible members (StateDead) are never elected.
//
// MUTATION that breaks this test:
//
//	Change the delegate election in NewHierarchicalTopology to use
//	rand.Shuffle instead of sort.Slice(stableHash) — the chosen IDs will
//	differ from both the golden set and across reconstructions.
//	Alternatively, replacing stableHash with a constant (always 0) will still
//	produce a stable result but the elected IDs will differ from the
//	hash-lowest golden set, failing assertion (c).
func TestDelegatesDeterminismAndExactCount(t *testing.T) {
	t.Parallel()

	const K = 2
	// 3 zones × 5 members each; 1 dead member per zone that must never appear.
	zones := []string{"us-east", "eu-west", "ap-south"}
	var members []*Member
	for _, z := range zones {
		for i := 0; i < 5; i++ {
			members = append(members, makeAlive(fmt.Sprintf("%s-node%d", z, i), z))
		}
		// dead member in each zone — must never appear in delegates.
		members = append(members, makeDead(fmt.Sprintf("%s-dead", z), z))
	}

	// Independently derive the expected delegate IDs per zone by replicating
	// the stableHash election algorithm in the test body. This proves
	// correctness (not just stability): a wrong-but-stable election algorithm
	// (e.g. constant hash → alphabetic fallback) will produce different IDs and
	// fail here.
	type hashPair struct {
		id string
		h  uint64
	}
	expected := make(map[string][]string) // group -> sorted delegate IDs
	for _, z := range zones {
		var pairs []hashPair
		for _, m := range members {
			if m.Metadata["zone"] != z {
				continue
			}
			// Only gossip-eligible states (StateAlive, StateSuspect).
			if m.State == StateDead || m.State == StateLeft {
				continue
			}
			pairs = append(pairs, hashPair{id: m.ID, h: stableHash(m.ID)})
		}
		sort.Slice(pairs, func(i, j int) bool {
			if pairs[i].h != pairs[j].h {
				return pairs[i].h < pairs[j].h
			}
			return pairs[i].id < pairs[j].id
		})
		k := K
		if k > len(pairs) {
			k = len(pairs)
		}
		ids := make([]string, k)
		for i := range ids {
			ids[i] = pairs[i].id
		}
		sort.Strings(ids)
		expected[z] = ids
	}

	// Reconstruct 10 times; every run must produce the same IDs as the golden set.
	for run := 0; run < 10; run++ {
		ht := NewHierarchicalTopology(members, nil, K)
		for _, z := range zones {
			got := memberIDs(ht.Delegates(z))
			if len(got) != K {
				t.Fatalf("run %d group %q: want %d delegates, got %d", run, z, K, len(got))
			}
			for i := range got {
				if got[i] != expected[z][i] {
					t.Fatalf(
						"run %d group %q: delegate[%d] = %q, want %q (hash-based election broken or non-deterministic)",
						run, z, i, got[i], expected[z][i],
					)
				}
			}
			// Assert no dead member snuck in.
			for _, id := range got {
				m := memberByIDInSlice(members, id)
				if m.State == StateDead || m.State == StateLeft {
					t.Fatalf("run %d group %q: delegate %q has non-gossip-eligible state %v", run, z, id, m.State)
				}
			}
		}
	}
}

// memberByIDInSlice is a test-only linear scan.
func memberByIDInSlice(ms []*Member, id string) *Member {
	for _, m := range ms {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 2. Delegate election picks lowest-hash IDs
// ---------------------------------------------------------------------------

// TestDelegateElectionPicksLowestHashIDs verifies that the elected delegates
// are exactly the members with the two numerically smallest stableHash(ID)
// values in their group.
//
// MUTATION that breaks this test:
//
//	Change election sort to sort.Slice by ID string instead of stableHash —
//	the two selected IDs will differ from the hash-based expectation whenever
//	the alphabetically-first IDs are not also the hash-lowest IDs.
func TestDelegateElectionPicksLowestHashIDs(t *testing.T) {
	t.Parallel()

	zone := "test-zone"
	ids := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	var members []*Member
	for _, id := range ids {
		members = append(members, makeAlive(id, zone))
	}

	// Compute expected: the two IDs with the lowest stableHash.
	type pair struct {
		id string
		h  uint64
	}
	pairs := make([]pair, len(ids))
	for i, id := range ids {
		pairs[i] = pair{id: id, h: stableHash(id)}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].h != pairs[j].h {
			return pairs[i].h < pairs[j].h
		}
		return pairs[i].id < pairs[j].id
	})
	expectedIDs := []string{pairs[0].id, pairs[1].id}
	sort.Strings(expectedIDs)

	ht := NewHierarchicalTopology(members, nil, 2)
	got := memberIDs(ht.Delegates(zone))

	if len(got) != 2 {
		t.Fatalf("want 2 delegates, got %d: %v", len(got), got)
	}
	for i, id := range expectedIDs {
		if got[i] != id {
			t.Fatalf("delegate[%d]: got %q, want %q (hash-based election broken)", i, got[i], id)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. Non-delegate SelectGossipTargets: only same-group members
// ---------------------------------------------------------------------------

// TestNonDelegateTargetsOnlySameGroup verifies that a node that is NOT a
// delegate receives gossip targets drawn exclusively from its own group.
//
// MUTATION that breaks this test:
//
//	Remove the `!ht.delegateSet[selfID]` early-return in SelectGossipTargets
//	so all nodes always get cross-group delegates — the test's check that no
//	remote member IDs appear will then fail.
func TestNonDelegateTargetsOnlySameGroup(t *testing.T) {
	t.Parallel()

	// 3 groups × 4 members each; K=1 delegate per group so only 1 member per
	// group is a delegate. We pick a known non-delegate.
	const K = 1
	zones := []string{"z1", "z2", "z3"}
	var members []*Member
	for _, z := range zones {
		for i := 0; i < 4; i++ {
			members = append(members, makeAlive(fmt.Sprintf("%s-m%d", z, i), z))
		}
	}

	ht := NewHierarchicalTopology(members, nil, K)

	// Find a member in z1 that is NOT the delegate.
	z1Delegate := memberIDs(ht.Delegates("z1"))[0]
	var nonDelegate string
	for _, id := range ht.groups["z1"] {
		if id != z1Delegate {
			nonDelegate = id
			break
		}
	}
	if nonDelegate == "" {
		t.Fatal("could not find a non-delegate in z1")
	}

	targets := ht.SelectGossipTargets(nonDelegate, 10)
	if len(targets) == 0 {
		t.Fatalf("expected at least one intra-group target, got none")
	}

	// All returned targets must belong to z1.
	z1Set := strSet(ht.groups["z1"])
	for _, m := range targets {
		if !z1Set[m.ID] {
			t.Fatalf(
				"non-delegate %q got cross-group target %q (group %q) — must stay within z1",
				nonDelegate, m.ID, ht.GroupFor(m.ID),
			)
		}
		if m.ID == nonDelegate {
			t.Fatalf("self %q appeared in its own target list", nonDelegate)
		}
	}

	// Concrete count: intraFanout=10 with 3 other alive peers in z1.
	if len(targets) != 3 {
		t.Fatalf("want 3 intra targets, got %d: %v", len(targets), memberIDs(targets))
	}
}

// ---------------------------------------------------------------------------
// 4. Delegate SelectGossipTargets: cross-group targets are ONLY other delegates
// ---------------------------------------------------------------------------

// TestDelegateTargetsIncludesOnlyOtherDelegates verifies that a delegate's
// target list includes:
//
//	(a) its same-group intra peers (excluding self), and
//	(b) EXACTLY the elected delegates of every other group — NOT all remote members.
//
// This is the core O(groups) WAN fanout proof.
//
// MUTATION that breaks this test:
//
//	Change SelectGossipTargets to append all remote members (memberByID values)
//	instead of only ht.delegates[gk] — the returned set will include
//	non-delegate remote IDs and the "no unexpected remote ID" assertion fails.
func TestDelegateTargetsIncludesOnlyOtherDelegates(t *testing.T) {
	t.Parallel()

	// 3 zones × 5 members; K=2 delegates per zone.
	const K = 2
	zones := []string{"alpha", "beta", "gamma"}
	var members []*Member
	for _, z := range zones {
		for i := 0; i < 5; i++ {
			members = append(members, makeAlive(fmt.Sprintf("%s-%d", z, i), z))
		}
	}

	ht := NewHierarchicalTopology(members, nil, K)

	// Pick the first delegate in "alpha".
	alphaDelegate := ht.Delegates("alpha")[0]
	selfID := alphaDelegate.ID

	// Compute expected remote delegate IDs (from beta + gamma).
	expectedRemote := make(map[string]bool)
	for _, z := range []string{"beta", "gamma"} {
		for _, d := range ht.Delegates(z) {
			expectedRemote[d.ID] = true
		}
	}
	// There should be exactly K*2 remote delegates.
	if len(expectedRemote) != K*2 {
		t.Fatalf("setup: want %d remote delegates, got %d", K*2, len(expectedRemote))
	}

	targets := ht.SelectGossipTargets(selfID, 10)

	// Self must never appear.
	for _, m := range targets {
		if m.ID == selfID {
			t.Fatalf("self %q appeared in target list", selfID)
		}
	}

	// Every remote target must be a known delegate.
	alphaSet := strSet(ht.groups["alpha"])
	for _, m := range targets {
		if alphaSet[m.ID] {
			continue // intra-group — allowed
		}
		// Cross-group — must be a delegate.
		if !expectedRemote[m.ID] {
			t.Fatalf(
				"delegate %q got non-delegate remote target %q (O(groups) WAN violated)",
				selfID, m.ID,
			)
		}
	}

	// Every expected remote delegate must appear exactly once.
	gotRemote := make(map[string]bool)
	for _, m := range targets {
		if !alphaSet[m.ID] {
			gotRemote[m.ID] = true
		}
	}
	for wantID := range expectedRemote {
		if !gotRemote[wantID] {
			t.Fatalf("delegate %q: expected remote delegate %q in targets, but it's absent", selfID, wantID)
		}
	}

	// Intra-group targets: 4 alive peers in alpha minus self = 4 peers; intraFanout=10 so all 4.
	intraCount := 0
	for _, m := range targets {
		if alphaSet[m.ID] {
			intraCount++
		}
	}
	if intraCount != 4 {
		t.Fatalf("want 4 intra targets for delegate, got %d", intraCount)
	}

	// Total: 4 intra + 4 remote = 8.
	if len(targets) != 8 {
		t.Fatalf("want 8 total targets (4 intra + 4 remote delegates), got %d: %v", len(targets), memberIDs(targets))
	}
}

// ---------------------------------------------------------------------------
// 5. IsDelegate and GroupFor correctness
// ---------------------------------------------------------------------------

// TestIsDelegateAndGroupFor verifies that IsDelegate returns true for elected
// delegates and false for non-delegates, and that GroupFor maps every member
// correctly.
//
// MUTATION that breaks this test:
//
//	Clear ht.delegateSet after construction (set it to empty map) — IsDelegate
//	would always return false and the delegate-positive assertions fail.
func TestIsDelegateAndGroupFor(t *testing.T) {
	t.Parallel()

	zones := []string{"x", "y"}
	var members []*Member
	for _, z := range zones {
		for i := 0; i < 3; i++ {
			members = append(members, makeAlive(fmt.Sprintf("%s%d", z, i), z))
		}
	}

	ht := NewHierarchicalTopology(members, nil, 1)

	for _, z := range zones {
		delegates := ht.Delegates(z)
		if len(delegates) != 1 {
			t.Fatalf("zone %q: want 1 delegate, got %d", z, len(delegates))
		}
		dID := delegates[0].ID

		// Delegate is recognised.
		if !ht.IsDelegate(dID) {
			t.Fatalf("IsDelegate(%q) = false, want true", dID)
		}
		// All members in the zone map correctly.
		for _, m := range members {
			if m.Metadata["zone"] != z {
				continue
			}
			if got := ht.GroupFor(m.ID); got != z {
				t.Fatalf("GroupFor(%q) = %q, want %q", m.ID, got, z)
			}
		}
		// Non-delegates return false.
		for _, id := range ht.groups[z] {
			if id == dID {
				continue
			}
			if ht.IsDelegate(id) {
				t.Fatalf("IsDelegate(%q) = true for non-delegate", id)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 6. Dead/non-alive members are excluded everywhere
// ---------------------------------------------------------------------------

// TestDeadMembersExcluded asserts that dead members appear in neither
// Delegates(), SelectGossipTargets(), nor IsDelegate().
//
// MUTATION that breaks this test:
//
//	Remove the `m.State == StateDead || m.State == StateLeft` guard in
//	NewHierarchicalTopology — dead members would enter groupMembers and could
//	be elected as delegates, causing the assertion to fail.
func TestDeadMembersExcluded(t *testing.T) {
	t.Parallel()

	zone := "sole-zone"
	alive1 := makeAlive("a1", zone)
	alive2 := makeAlive("a2", zone)
	dead := makeDead("d1", zone)
	members := []*Member{alive1, alive2, dead}

	ht := NewHierarchicalTopology(members, nil, 3) // request more than available

	// No dead member in delegates.
	for _, d := range ht.Delegates(zone) {
		if d.ID == dead.ID {
			t.Fatalf("dead member %q appeared in Delegates", dead.ID)
		}
	}
	// Dead member not a delegate.
	if ht.IsDelegate(dead.ID) {
		t.Fatalf("IsDelegate(%q) = true for dead member", dead.ID)
	}
	// Dead member has no group mapping.
	if g := ht.GroupFor(dead.ID); g != "" {
		t.Fatalf("GroupFor(dead) = %q, want empty", g)
	}
	// Dead member does not appear in gossip targets.
	for _, m := range ht.SelectGossipTargets(alive1.ID, 10) {
		if m.ID == dead.ID {
			t.Fatalf("dead member %q appeared in SelectGossipTargets", dead.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// 7. Groups() returns sorted group keys
// ---------------------------------------------------------------------------

// TestGroupsSorted ensures Groups() is lexicographically sorted, as
// documented.
//
// MUTATION that breaks this test:
//
//	Remove the sort.Strings(sortedGroupKeys) call in NewHierarchicalTopology —
//	map iteration order is random so the returned slice will not be sorted.
func TestGroupsSorted(t *testing.T) {
	t.Parallel()

	zones := []string{"z3", "z1", "z2"} // intentionally unordered
	var members []*Member
	for _, z := range zones {
		members = append(members, makeAlive("node-"+z, z))
	}

	ht := NewHierarchicalTopology(members, nil, 1)
	groups := ht.Groups()

	if len(groups) != 3 {
		t.Fatalf("want 3 groups, got %d", len(groups))
	}
	expected := []string{"z1", "z2", "z3"}
	for i, g := range groups {
		if g != expected[i] {
			t.Fatalf("groups[%d] = %q, want %q (not sorted)", i, g, expected[i])
		}
	}
}

// ---------------------------------------------------------------------------
// 8. Custom groupKeyFn is respected
// ---------------------------------------------------------------------------

// TestCustomGroupKeyFn verifies that a caller-supplied groupKeyFn overrides
// the default Metadata["zone"] lookup, so topology is governed by the provided
// key.
//
// MUTATION that breaks this test:
//
//	Ignore groupKeyFn and always call DefaultGroupKeyFn — members with no
//	"zone" key all land in the same empty-string group, collapsing the 2-group
//	structure and breaking the group count assertion.
func TestCustomGroupKeyFn(t *testing.T) {
	t.Parallel()

	// Members have no "zone" metadata; keyed by the first character of ID.
	members := []*Member{
		{ID: "a1", Address: "127.0.0.1:0", State: StateAlive},
		{ID: "a2", Address: "127.0.0.1:0", State: StateAlive},
		{ID: "b1", Address: "127.0.0.1:0", State: StateAlive},
		{ID: "b2", Address: "127.0.0.1:0", State: StateAlive},
	}

	keyFn := func(m *Member) string {
		if len(m.ID) == 0 {
			return ""
		}
		return string(m.ID[0])
	}

	ht := NewHierarchicalTopology(members, keyFn, 1)
	groups := ht.Groups()
	if len(groups) != 2 {
		t.Fatalf("want 2 groups (a, b), got %d: %v", len(groups), groups)
	}
	if groups[0] != "a" || groups[1] != "b" {
		t.Fatalf("want groups [a b], got %v", groups)
	}
	for _, m := range members {
		wantGroup := string(m.ID[0])
		if g := ht.GroupFor(m.ID); g != wantGroup {
			t.Fatalf("GroupFor(%q) = %q, want %q", m.ID, g, wantGroup)
		}
	}
}

// ---------------------------------------------------------------------------
// 9. SelectGossipTargets excludes self unconditionally
// ---------------------------------------------------------------------------

// TestSelectGossipTargetsExcludesSelf verifies that selfID never appears in
// the returned target list, whether self is a delegate or not.
//
// MUTATION that breaks this test:
//
//	Remove the `id == selfID` guard in SelectGossipTargets — self is appended
//	to the intra list.
func TestSelectGossipTargetsExcludesSelf(t *testing.T) {
	t.Parallel()

	zone := "only-zone"
	var members []*Member
	for i := 0; i < 5; i++ {
		members = append(members, makeAlive(fmt.Sprintf("n%d", i), zone))
	}

	ht := NewHierarchicalTopology(members, nil, 2)
	for _, m := range members {
		targets := ht.SelectGossipTargets(m.ID, 10)
		for _, t2 := range targets {
			if t2.ID == m.ID {
				t.Errorf("self %q appeared in own target list", m.ID)
			}
		}
	}
}

// TestSelectGossipTargetsExcludesSelf_table is a table-driven companion that
// explicitly checks the assertion with t.Fatal.
func TestSelectGossipTargetsExcludesSelf_table(t *testing.T) {
	t.Parallel()

	zone := "z"
	members := []*Member{
		makeAlive("s1", zone),
		makeAlive("s2", zone),
		makeAlive("s3", zone),
	}

	ht := NewHierarchicalTopology(members, nil, 1)
	cases := []struct{ selfID string }{
		{"s1"}, {"s2"}, {"s3"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.selfID, func(t *testing.T) {
			t.Parallel()
			for _, m := range ht.SelectGossipTargets(tc.selfID, 10) {
				if m.ID == tc.selfID {
					t.Fatalf("self %q appeared in its own target list", tc.selfID)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 10. delegatesPerGroup clamped to available alive members
// ---------------------------------------------------------------------------

// TestDelegatesPerGroupClamped verifies that when delegatesPerGroup > alive
// count in a group, all alive members are returned (not a panic or nil).
//
// MUTATION that breaks this test:
//
//	Remove the `if k > len(alive) { k = len(alive) }` clamp in
//	NewHierarchicalTopology — alive[:k] would panic with index out of range.
func TestDelegatesPerGroupClamped(t *testing.T) {
	t.Parallel()

	zone := "small"
	members := []*Member{
		makeAlive("x1", zone),
		makeAlive("x2", zone),
	}

	ht := NewHierarchicalTopology(members, nil, 100) // request far more than available
	ds := ht.Delegates(zone)
	if len(ds) != 2 {
		t.Fatalf("want 2 delegates (clamped), got %d", len(ds))
	}
	got := strSet(memberIDs(ds))
	for _, id := range []string{"x1", "x2"} {
		if !got[id] {
			t.Fatalf("expected %q in clamped delegates, got %v", id, memberIDs(ds))
		}
	}
}

// ---------------------------------------------------------------------------
// 11. Suspect members are gossip-eligible (CLAUDE-1 correctness)
// ---------------------------------------------------------------------------

// makeSuspect returns a *Member with StateSuspect and the given zone metadata.
func makeSuspect(id, zone string) *Member {
	return &Member{
		ID:      id,
		Address: "127.0.0.1:0",
		State:   StateSuspect,
		Metadata: map[string]string{
			"zone": zone,
		},
	}
}

// TestSuspectMembersIncluded verifies that StateSuspect members are treated as
// gossip-eligible (matching the live protocol's handling), and therefore appear
// in Delegates(), SelectGossipTargets(), IsDelegate(), and GroupFor() — exactly
// the same as StateAlive members.
//
// Background: protocol.go treats StateSuspect nodes as active participants that
// continue to send and receive gossip messages (gossip-eligible). Excluding
// them from HierarchicalTopology would produce zero targets for a suspected
// node calling SelectGossipTargets, silently breaking gossip for that node.
//
// MUTATION that breaks this test:
//
//	Change the guard in NewHierarchicalTopology back to
//	`if m.State != StateAlive { continue }` — the suspect member is excluded,
//	GroupFor returns "", Delegates returns only the alive member, and
//	SelectGossipTargets for the suspect returns an empty slice, causing the
//	concrete assertions below to fail.
func TestSuspectMembersIncluded(t *testing.T) {
	t.Parallel()

	zone := "mixed-zone"
	alive := makeAlive("alive-node", zone)
	suspect := makeSuspect("suspect-node", zone)
	members := []*Member{alive, suspect}

	// K=2: both members should be elected as delegates since there are only two.
	ht := NewHierarchicalTopology(members, nil, 2)

	// --- Delegates includes the suspect ---
	delegates := ht.Delegates(zone)
	if len(delegates) != 2 {
		t.Fatalf("Delegates(%q): want 2 (alive+suspect), got %d: %v", zone, len(delegates), memberIDs(delegates))
	}
	delegateIDSet := strSet(memberIDs(delegates))
	if !delegateIDSet[alive.ID] {
		t.Fatalf("Delegates(%q): alive member %q missing", zone, alive.ID)
	}
	if !delegateIDSet[suspect.ID] {
		t.Fatalf("Delegates(%q): suspect member %q missing — StateSuspect must be gossip-eligible", zone, suspect.ID)
	}

	// --- IsDelegate returns true for both ---
	if !ht.IsDelegate(alive.ID) {
		t.Fatalf("IsDelegate(%q) = false for alive member, want true", alive.ID)
	}
	if !ht.IsDelegate(suspect.ID) {
		t.Fatalf("IsDelegate(%q) = false for suspect member, want true — StateSuspect must be gossip-eligible", suspect.ID)
	}

	// --- GroupFor maps both correctly ---
	if g := ht.GroupFor(alive.ID); g != zone {
		t.Fatalf("GroupFor(%q) = %q, want %q", alive.ID, g, zone)
	}
	if g := ht.GroupFor(suspect.ID); g != zone {
		t.Fatalf("GroupFor(%q) = %q, want %q — suspect must have a group mapping", suspect.ID, g, zone)
	}

	// --- SelectGossipTargets for the suspect node returns the alive peer ---
	// (suspect calls SGT with intraFanout=10; the only other member is alive)
	targetsForSuspect := ht.SelectGossipTargets(suspect.ID, 10)
	if len(targetsForSuspect) != 1 {
		t.Fatalf("SelectGossipTargets(%q): want 1 target (the alive peer), got %d: %v",
			suspect.ID, len(targetsForSuspect), memberIDs(targetsForSuspect))
	}
	if targetsForSuspect[0].ID != alive.ID {
		t.Fatalf("SelectGossipTargets(%q): expected alive peer %q, got %q",
			suspect.ID, alive.ID, targetsForSuspect[0].ID)
	}

	// --- SelectGossipTargets for the alive node returns the suspect peer ---
	targetsForAlive := ht.SelectGossipTargets(alive.ID, 10)
	if len(targetsForAlive) != 1 {
		t.Fatalf("SelectGossipTargets(%q): want 1 target (the suspect peer), got %d: %v",
			alive.ID, len(targetsForAlive), memberIDs(targetsForAlive))
	}
	if targetsForAlive[0].ID != suspect.ID {
		t.Fatalf("SelectGossipTargets(%q): expected suspect peer %q, got %q",
			alive.ID, suspect.ID, targetsForAlive[0].ID)
	}
}
