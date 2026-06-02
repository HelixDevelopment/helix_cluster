package antientropy

import (
	"fmt"
	"sort"
	"testing"
)

// runUUID is a fixed per-suite identifier logged with before/after observables so
// closure evidence is traceable in test output.
const runUUID = "antientropy-HXC-1410-7c1f"

// TestHintedHandoffCatchUp proves CLOSURE clause 1: a replica that is DOWN during
// several writes accumulates hints; once it comes UP and hints are replayed it
// holds ALL the previously-missed writes.
//
// Mutation: if replayHints stops delivering buffered hints (e.g. the apply loop
// is removed), R stays missing the keys and the "after" assertions fail.
func TestHintedHandoffCatchUp(t *testing.T) {
	r0 := NewReplica("r0")
	r1 := NewReplica("r1")
	rDown := NewReplica("r2")
	c := NewCoordinator(r0, r1, rDown)

	// Take rDown offline, then perform several writes it will miss.
	rDown.SetUp(false)
	writes := []struct {
		key, val string
		ver      uint64
	}{
		{"alpha", "a1", 1},
		{"beta", "b1", 1},
		{"gamma", "g1", 1},
		{"alpha", "a2", 2}, // newer version of alpha; replay must converge to this
	}
	for _, w := range writes {
		c.Write(w.key, w.val, w.ver)
	}

	// BEFORE: rDown has none of the missed writes; hints are buffered for it.
	missingBefore := 0
	for _, k := range []string{"alpha", "beta", "gamma"} {
		if _, ok := rDown.Get(k); !ok {
			missingBefore++
		}
	}
	pending := c.PendingHints("r2")
	t.Logf("run=%s phase=before replica=r2 missingKeys=%d pendingHints=%d", runUUID, missingBefore, pending)
	if missingBefore != 3 {
		t.Fatalf("expected r2 to be missing all 3 keys while down, missing=%d", missingBefore)
	}
	if pending != 4 {
		t.Fatalf("expected 4 buffered hints (one per write), got %d", pending)
	}

	// Recover the replica and replay its hints.
	rDown.SetUp(true)
	delivered := c.replayHints(rDown)
	if delivered != 4 {
		t.Fatalf("expected 4 hints delivered, got %d", delivered)
	}

	// AFTER: rDown must now hold every missed write, at the newest version.
	want := map[string]VersionedValue{
		"alpha": {Value: "a2", Version: 2},
		"beta":  {Value: "b1", Version: 1},
		"gamma": {Value: "g1", Version: 1},
	}
	convergedKeys := 0
	for k, exp := range want {
		got, ok := rDown.Get(k)
		if !ok {
			t.Fatalf("after replay r2 still missing key %q", k)
		}
		if got != exp {
			t.Fatalf("after replay r2[%q]=%+v want %+v", k, got, exp)
		}
		convergedKeys++
	}
	if pendingAfter := c.PendingHints("r2"); pendingAfter != 0 {
		t.Fatalf("expected hint buffer cleared after replay, got %d", pendingAfter)
	}
	t.Logf("run=%s phase=after replica=r2 convergedKeys=%d alpha=%s@%d pendingHints=%d",
		runUUID, convergedKeys, want["alpha"].Value, want["alpha"].Version, c.PendingHints("r2"))
}

// TestHintsNotBufferedForUpReplica guards the DOWN-only branch: an up replica
// never accumulates hints (writes go straight through).
//
// Mutation: if Write buffered hints for up replicas too, this count would be
// non-zero and the test fails.
func TestHintsNotBufferedForUpReplica(t *testing.T) {
	r0 := NewReplica("r0")
	c := NewCoordinator(r0)
	c.Write("k", "v", 1)
	if n := c.PendingHints("r0"); n != 0 {
		t.Fatalf("up replica must not accumulate hints, got %d", n)
	}
	if vv, ok := r0.Get("k"); !ok || vv.Value != "v" {
		t.Fatalf("up replica must take the write directly, got %+v ok=%v", vv, ok)
	}
	t.Logf("run=%s upReplicaPendingHints=0 directValue=%s", runUUID, "v")
}

// TestReadRepair proves CLOSURE clause 2: with a STALE older value on one replica
// and a newer value on the others, a Read returns the NEWEST value AND repairs
// the stale replica in place (write-back side effect).
//
// Mutation: if Read skips the second pass (no apply write-back), the stale
// replica keeps version 1 and the "after" assertion that it equals the newest
// fails. Also the Repaired set would be empty.
func TestReadRepair(t *testing.T) {
	stale := NewReplica("stale")
	fresh1 := NewReplica("fresh1")
	fresh2 := NewReplica("fresh2")
	c := NewCoordinator(stale, fresh1, fresh2)

	// Seed divergence directly so we control versions precisely.
	stale.apply("key", VersionedValue{Value: "old", Version: 1})
	fresh1.apply("key", VersionedValue{Value: "new", Version: 5})
	fresh2.apply("key", VersionedValue{Value: "new", Version: 5})

	// BEFORE: stale replica holds the old value at version 1.
	beforeVV, _ := stale.Get("key")
	t.Logf("run=%s phase=before staleReplica=%s value=%s version=%d", runUUID, stale.ID, beforeVV.Value, beforeVV.Version)
	if beforeVV.Value != "old" || beforeVV.Version != 1 {
		t.Fatalf("precondition: stale must hold old@1, got %+v", beforeVV)
	}

	res := c.Read("key")

	// Read returns the newest value.
	if !res.Found || res.Value != "new" || res.Version != 5 {
		t.Fatalf("read must return newest new@5, got %+v", res)
	}
	// The stale replica must be in the repaired set; the already-fresh ones must not.
	if got := fmt.Sprint(res.Repaired); got != "[stale]" {
		t.Fatalf("expected exactly [stale] repaired, got %v", res.Repaired)
	}

	// AFTER: stale replica has been corrected to the newest value/version.
	afterVV, ok := stale.Get("key")
	if !ok || afterVV.Value != "new" || afterVV.Version != 5 {
		t.Fatalf("read-repair side effect missing: stale=%+v ok=%v", afterVV, ok)
	}
	t.Logf("run=%s phase=after staleReplica=%s value=%s version=%d repaired=%v",
		runUUID, stale.ID, afterVV.Value, afterVV.Version, res.Repaired)
}

// TestReadRepairNoOpWhenConverged proves the repair set is empty when replicas
// already agree — repair is driven by real divergence, not always-run.
//
// Mutation: if apply returned true even when no change occurred, Repaired would
// be non-empty here and the test fails.
func TestReadRepairNoOpWhenConverged(t *testing.T) {
	a := NewReplica("a")
	b := NewReplica("b")
	c := NewCoordinator(a, b)
	c.Write("k", "v", 3) // both up, both converge

	res := c.Read("k")
	if !res.Found || res.Value != "v" {
		t.Fatalf("expected to read v, got %+v", res)
	}
	if len(res.Repaired) != 0 {
		t.Fatalf("no divergence: expected zero repairs, got %v", res.Repaired)
	}
	t.Logf("run=%s convergedRead value=%s repairs=%d", runUUID, res.Value, len(res.Repaired))
}

// TestMerkleDiff proves CLOSURE clause 3: two replicas differing in exactly K
// keys -> MerkleDiff returns exactly those K keys, and identical replicas yield
// an EMPTY diff (proving it does not return matching keys).
//
// Mutation: a diff that returns all keys would fail the identical-replica empty
// sub-assertion and would also over-report on the K-key case. Skipping the
// sub-tree prune (pruned==0) would fail the pruning sub-assertion.
func TestMerkleDiff(t *testing.T) {
	base := map[string]string{}
	for i := 0; i < 16; i++ {
		base[fmt.Sprintf("k%02d", i)] = fmt.Sprintf("v%02d", i)
	}

	// Identical replicas -> empty diff.
	ta := NewMerkleTree(base)
	tbSame := NewMerkleTree(cloneMap(base))
	if d := MerkleDiff(ta, tbSame); len(d) != 0 {
		t.Fatalf("identical replicas must yield empty diff, got %v", d)
	}
	t.Logf("run=%s identicalDiffLen=%d", runUUID, len(MerkleDiff(ta, tbSame)))

	// Diverge exactly K keys (changed values) + one key present on only one side.
	divergent := cloneMap(base)
	changed := []string{"k03", "k07", "k11"}
	for _, k := range changed {
		divergent[k] = divergent[k] + "-MUT"
	}
	divergent["k99-extra"] = "only-on-b" // missing-on-one-side case
	wantKeys := append(append([]string{}, changed...), "k99-extra")
	sort.Strings(wantKeys)

	tb := NewMerkleTree(divergent)
	got := MerkleDiff(ta, tb)
	if fmt.Sprint(got) != fmt.Sprint(wantKeys) {
		t.Fatalf("MerkleDiff mismatch: got %v want %v", got, wantKeys)
	}
	// Prove no matching key leaked into the diff.
	for _, k := range got {
		if k == "k00" || k == "k05" || k == "k15" {
			t.Fatalf("matching key %q wrongly reported as divergent", k)
		}
	}
	t.Logf("run=%s before:K=%d differingKeys=%v emptyOnIdentical=true", runUUID, len(wantKeys), got)
}

// TestMerkleDiffPrunesMatchingSubtrees proves the sub-tree pruning is real:
// changing a single key in a 16-leaf tree visits far fewer node pairs than the
// number of leaves, because matching sub-trees are skipped.
//
// Mutation: if descend stopped short-circuiting on equal hashes (no prune), the
// visited count would equal the full traversal and pruned would be 0, failing
// both assertions below.
func TestMerkleDiffPrunesMatchingSubtrees(t *testing.T) {
	base := map[string]string{}
	for i := 0; i < 16; i++ {
		base[fmt.Sprintf("k%02d", i)] = fmt.Sprintf("v%02d", i)
	}
	ta := NewMerkleTree(base)
	mod := cloneMap(base)
	mod["k08"] = "changed" // single divergent leaf
	tb := NewMerkleTree(mod)

	got, st := MerkleDiffInstrumented(ta, tb)
	if fmt.Sprint(got) != "[k08]" {
		t.Fatalf("expected exactly [k08], got %v", got)
	}
	if st.pruned == 0 {
		t.Fatalf("expected at least one pruned sub-tree, pruned=0 (no skipping happened)")
	}
	// With 16 leaves a full leaf scan is 16; pruning must visit fewer node pairs.
	if st.visited >= 16 {
		t.Fatalf("expected pruning to visit <16 node pairs, visited=%d (no pruning)", st.visited)
	}
	t.Logf("run=%s singleKeyDiff=%v visitedNodePairs=%d prunedSubtrees=%d leafCount=16",
		runUUID, got, st.visited, st.pruned)
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
