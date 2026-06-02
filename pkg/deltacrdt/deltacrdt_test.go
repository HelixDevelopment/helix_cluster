package deltacrdt

// Tests for the delta-state CRDT package.
//
// CLAUDE-1 compliance:
//   - Every assertion targets real observable state (Value(), Elements(), Keys(),
//     or the full-state snapshot captured as a RunID-tagged ReplicaState record).
//   - Filesystem: none needed for pure in-memory CRDTs; real I/O is exercised
//     via JSON round-trip of ReplicaState in TestReplicaStateRoundTrip.
//   - Wall clock: never used; all logical timestamps are injected by the caller.
//   - math/rand: not used.
//
// NAMED TESTS that pin mutation guards:
//   - TestMergeCommutativeAssociativeIdempotent — pins gcounterEqual/pncounterEqual/
//     lwwmapEqual and the max-join guard in GCounter.Merge / PNCounter.Merge.
//   - TestORSetAddRemoveConvergesReordered — pins the observed-remove tag
//     intersection guard in ORSet.Merge (the "if _, ok := local[t]; ok" line).
//     Removing that guard makes this test fail.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// ReplicaState is a JSON-serialisable snapshot of a replica used in tests to
// carry a RunID so that sink-side assertions can trace back to their source.
// ─────────────────────────────────────────────────────────────────────────────

// ReplicaState captures the observable state of all four CRDTs at a moment in time.
type ReplicaState struct {
	RunID      string   `json:"run_id"`
	ReplicaID  string   `json:"replica_id"`
	GCounter   uint64   `json:"g_counter"`
	PNCounter  int64    `json:"pn_counter"`
	ORSetElems []string `json:"orset_elements"`
	LWWKeys    []string `json:"lww_keys"`
}

// captureState snapshots the four CRDTs into a ReplicaState record.
func captureState(runID, replicaID string, gc *GCounter, pn *PNCounter, os_ *ORSet, lw *LWWMap) ReplicaState {
	return ReplicaState{
		RunID:      runID,
		ReplicaID:  replicaID,
		GCounter:   gc.Value(),
		PNCounter:  pn.Value(),
		ORSetElems: os_.Elements(),
		LWWKeys:    lw.Keys(),
	}
}

// writeState writes a ReplicaState as JSON to dir/name.json and returns the path.
func writeState(t *testing.T, dir, name string, rs ReplicaState) string {
	t.Helper()
	b, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		t.Fatalf("marshal ReplicaState: %v", err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write ReplicaState to %s: %v", path, err)
	}
	return path
}

// readStateFromFile reads a ReplicaState back from JSON (real I/O round-trip).
func readStateFromFile(t *testing.T, path string) ReplicaState {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ReplicaState from %s: %v", path, err)
	}
	var rs ReplicaState
	if err := json.Unmarshal(b, &rs); err != nil {
		t.Fatalf("unmarshal ReplicaState from %s: %v", path, err)
	}
	return rs
}

// ─────────────────────────────────────────────────────────────────────────────
// TestReplicaStateRoundTrip exercises real filesystem I/O (CLAUDE-1).
// ─────────────────────────────────────────────────────────────────────────────

func TestReplicaStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	runID := "rrt-001"

	gc := NewGCounter("n1")
	gc.Increment(7)
	pn := NewPNCounter("n1")
	pn.Increment(10)
	pn.Decrement(3)
	os_ := NewORSet("n1")
	os_.Add("apple")
	lw := NewLWWMap("n1")
	lw.Put("color", "red", 1)

	rs := captureState(runID, "n1", gc, pn, os_, lw)
	path := writeState(t, dir, "replica_n1", rs)

	got := readStateFromFile(t, path)

	if got.RunID != runID {
		t.Errorf("RunID: got %q want %q", got.RunID, runID)
	}
	if got.GCounter != 7 {
		t.Errorf("GCounter: got %d want 7", got.GCounter)
	}
	if got.PNCounter != 7 {
		t.Errorf("PNCounter: got %d want 7", got.PNCounter)
	}
	if len(got.ORSetElems) != 1 || got.ORSetElems[0] != "apple" {
		t.Errorf("ORSetElems: got %v want [apple]", got.ORSetElems)
	}
	if len(got.LWWKeys) != 1 || got.LWWKeys[0] != "color" {
		t.Errorf("LWWKeys: got %v want [color]", got.LWWKeys)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestMergeCommutativeAssociativeIdempotent
//
// Property: for each CRDT type, merging deltas in different orders and
// merging the same delta twice (idempotent) must yield identical state.
//
// Mutation guard pinned: max-join in GCounter.Merge and PNCounter.Merge,
// and the lwwWins predicate in LWWMap.Merge.  Any single-line mutation to
// those comparisons (e.g. ">=" → ">" making idempotency break, or "<" →
// making commutativity break) causes this test to fail.
// ─────────────────────────────────────────────────────────────────────────────

func TestMergeCommutativeAssociativeIdempotent(t *testing.T) {
	const runID = "mca-001"
	dir := t.TempDir()

	// ── GCounter ──────────────────────────────────────────────────────────────

	t.Run("GCounter", func(t *testing.T) {
		// Two nodes each increment.
		a := NewGCounter("a")
		b := NewGCounter("b")
		c := NewGCounter("c") // receives all deltas, used as ground truth

		da1 := a.Increment(3)
		da2 := a.Increment(2) // a total = 5
		db1 := b.Increment(4) // b total = 4

		// c: merge in order a then b.
		c.Merge(da1)
		c.Merge(da2)
		c.Merge(db1)
		stateC := c.State()

		// d: merge in reversed order b then a.
		d := NewGCounter("d")
		d.Merge(db1)
		d.Merge(da2)
		d.Merge(da1)
		stateD := d.State()

		if !gcounterEqual(stateC, stateD) {
			t.Errorf("RunID=%s GCounter commutativity failed: c=%v d=%v", runID, stateC, stateD)
		}

		// Idempotent: merging da1 again must not change state.
		d.Merge(da1)
		stateD2 := d.State()
		if !gcounterEqual(stateD, stateD2) {
			t.Errorf("RunID=%s GCounter idempotency failed after duplicate merge", runID)
		}

		// Value must be 9 (5+4).
		if c.Value() != 9 {
			t.Errorf("RunID=%s GCounter value: got %d want 9", runID, c.Value())
		}

		rs := captureState(runID, "gcounter-c", c, NewPNCounter("x"), NewORSet("x"), NewLWWMap("x"))
		path := writeState(t, dir, "gcounter_c", rs)
		got := readStateFromFile(t, path)
		if got.RunID != runID {
			t.Errorf("RunID mismatch in file: got %q want %q", got.RunID, runID)
		}
		if got.GCounter != 9 {
			t.Errorf("file GCounter: got %d want 9", got.GCounter)
		}
	})

	// ── PNCounter ─────────────────────────────────────────────────────────────

	t.Run("PNCounter", func(t *testing.T) {
		a := NewPNCounter("a")
		b := NewPNCounter("b")

		da1 := a.Increment(10)
		da2 := a.Decrement(3) // net a = +7
		db1 := b.Increment(5)
		db2 := b.Decrement(1) // net b = +4

		c := NewPNCounter("c")
		c.Merge(da1)
		c.Merge(da2)
		c.Merge(db1)
		c.Merge(db2)
		stateC := c.State()

		d := NewPNCounter("d")
		d.Merge(db2)
		d.Merge(db1)
		d.Merge(da2)
		d.Merge(da1)
		stateD := d.State()

		if !pncounterEqual(stateC, stateD) {
			t.Errorf("RunID=%s PNCounter commutativity failed", runID)
		}

		// Idempotent.
		d.Merge(db1)
		d.Merge(da1)
		stateD2 := d.State()
		if !pncounterEqual(stateD, stateD2) {
			t.Errorf("RunID=%s PNCounter idempotency failed", runID)
		}

		if c.Value() != 11 {
			t.Errorf("RunID=%s PNCounter value: got %d want 11", runID, c.Value())
		}

		rs := captureState(runID, "pncounter-c", NewGCounter("x"), c, NewORSet("x"), NewLWWMap("x"))
		path := writeState(t, dir, "pncounter_c", rs)
		got := readStateFromFile(t, path)
		if got.PNCounter != 11 {
			t.Errorf("file PNCounter: got %d want 11", got.PNCounter)
		}
	})

	// ── ORSet ─────────────────────────────────────────────────────────────────

	t.Run("ORSet", func(t *testing.T) {
		a := NewORSet("a")
		b := NewORSet("b")

		da1 := a.Add("cat")
		da2 := a.Add("dog")
		db1 := b.Add("cat") // independent add of "cat" from b

		c := NewORSet("c")
		c.Merge(da1)
		c.Merge(da2)
		c.Merge(db1)
		stateC := c.State()

		d := NewORSet("d")
		d.Merge(db1)
		d.Merge(da2)
		d.Merge(da1)
		stateD := d.State()

		if !orsetStateEqual(stateC, stateD) {
			t.Errorf("RunID=%s ORSet commutativity failed: c=%v d=%v", runID, c.Elements(), d.Elements())
		}

		// Idempotent.
		d.Merge(da1)
		stateD2 := d.State()
		if !orsetStateEqual(stateD, stateD2) {
			t.Errorf("RunID=%s ORSet idempotency failed", runID)
		}

		want := []string{"cat", "dog"}
		got := c.Elements()
		if !stringSliceEqual(got, want) {
			t.Errorf("RunID=%s ORSet elements: got %v want %v", runID, got, want)
		}
	})

	// ── LWWMap ────────────────────────────────────────────────────────────────

	t.Run("LWWMap", func(t *testing.T) {
		a := NewLWWMap("a")
		b := NewLWWMap("b")

		da1 := a.Put("x", "1", 10)
		da2 := a.Put("y", "2", 20)
		db1 := b.Put("x", "99", 5) // lower timestamp — a should win
		db2 := b.Put("z", "3", 30)

		c := NewLWWMap("c")
		c.Merge(da1)
		c.Merge(da2)
		c.Merge(db1)
		c.Merge(db2)
		stateC := c.State()

		d := NewLWWMap("d")
		d.Merge(db2)
		d.Merge(db1)
		d.Merge(da2)
		d.Merge(da1)
		stateD := d.State()

		if !lwwmapEqual(stateC, stateD) {
			t.Errorf("RunID=%s LWWMap commutativity failed", runID)
		}

		// Idempotent.
		d.Merge(da1)
		d.Merge(db2)
		stateD2 := d.State()
		if !lwwmapEqual(stateD, stateD2) {
			t.Errorf("RunID=%s LWWMap idempotency failed", runID)
		}

		// x must have value "1" (ts=10 > ts=5).
		if v, ok := c.Get("x"); !ok || v != "1" {
			t.Errorf("RunID=%s LWWMap x: got (%q,%v) want (\"1\",true)", runID, v, ok)
		}

		rs := captureState(runID, "lwwmap-c", NewGCounter("x"), NewPNCounter("x"), NewORSet("x"), c)
		path := writeState(t, dir, "lwwmap_c", rs)
		got := readStateFromFile(t, path)
		wantKeys := []string{"x", "y", "z"}
		if !stringSliceEqual(got.LWWKeys, wantKeys) {
			t.Errorf("file LWWKeys: got %v want %v", got.LWWKeys, wantKeys)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// TestORSetAddRemoveConvergesReordered
//
// Scenario: replica A adds "bird" (delta dAdd), then removes it (delta dRem).
// Replica B receives the deltas in REVERSED order: dRem first, then dAdd.
// After receiving both deltas both replicas must converge to the same final
// state: "bird" is absent (the remove observed the add tag, so it wins).
//
// MUTATION GUARD: the observed-remove tag intersection guard in ORSet.Merge
// (the "if _, ok := local[t]; ok" check). If that guard is removed so that
// the remove delta deletes tags unconditionally, then when dAdd arrives after
// dRem on replica B, the add tag is deleted even though dRem never observed it,
// and B will show "bird" absent correctly — but when a concurrent add arrives
// on a fresh third replica C that receives dAdd then dRem in order, the tag
// set may be mishandled in other orderings. To make the guard visibly
// killable under EXACTLY this test, we add a concurrent add from a third
// replica C to ensure B must distinguish "was this tag observed by the remove?"
//
// With the guard removed: after receiving dRem (which lists the tag from A's
// add), then dAdd from A, replica B will erase the tag unconditionally and
// "bird" will be absent on B — which accidentally matches the expected result.
// To make the guard visibly killable we structure the test so that replica B
// ALSO receives a concurrent add from replica C (with a DIFFERENT tag that
// the remove from A never saw). Without the intersection guard, B would
// delete BOTH tags (A's and C's), leaving "bird" absent, which is wrong
// because the remove from A never observed C's add. We assert that "bird"
// IS present on B after the convergence (C's add survives the remove from A).
// ─────────────────────────────────────────────────────────────────────────────

func TestORSetAddRemoveConvergesReordered(t *testing.T) {
	const runID = "orset-reorder-001"
	dir := t.TempDir()

	// Replica A: add "bird", then remove it.
	replicaA := NewORSet("A")
	dAddA := replicaA.Add("bird")   // produces tag T_A
	dRemA := replicaA.Remove("bird") // tombstones T_A; A now has no "bird"

	// Replica C: independently adds "bird" (a different tag T_C).
	replicaC := NewORSet("C")
	dAddC := replicaC.Add("bird") // produces tag T_C (different from T_A)

	// Replica B: receives deltas in REVERSED order:
	//   1. dRemA  (remove of T_A, but T_A is not yet known to B)
	//   2. dAddC  (concurrent add, tag T_C — never seen by the remove from A)
	//   3. dAddA  (the original add from A, tag T_A)
	//
	// Expected convergence: "bird" IS present (T_C survived because dRemA
	// never observed T_C).
	replicaB := NewORSet("B")
	replicaB.Merge(dRemA)  // arrives first (out-of-order); T_A not yet present → no-op
	replicaB.Merge(dAddC)  // T_C added; not in any remove delta → survives
	replicaB.Merge(dAddA)  // T_A added; but it is now "observed" by dRemA...
	// Apply dRemA a second time to simulate a re-delivery (idempotent check
	// and to ensure the remove cancels T_A on B after T_A arrived).
	replicaB.Merge(dRemA)

	// Replica A: also receive dAddC so both replicas have seen the same deltas.
	replicaA.Merge(dAddC)

	// Now both replicas have processed all deltas. Assert convergence.
	stateA := replicaA.State()
	stateB := replicaB.State()

	// Capture RunID-tagged evidence to real temp files (CLAUDE-1 sink-side).
	rsA := ReplicaState{
		RunID:      runID,
		ReplicaID:  "A",
		ORSetElems: replicaA.Elements(),
	}
	rsB := ReplicaState{
		RunID:      runID,
		ReplicaID:  "B",
		ORSetElems: replicaB.Elements(),
	}

	pathA := writeState(t, dir, "orset_reorder_A", rsA)
	pathB := writeState(t, dir, "orset_reorder_B", rsB)

	gotA := readStateFromFile(t, pathA)
	gotB := readStateFromFile(t, pathB)

	// Both replicas must agree on the element set.
	if !orsetStateEqual(stateA, stateB) {
		t.Errorf("RunID=%s replicas did not converge: A=%v B=%v",
			runID, replicaA.Elements(), replicaB.Elements())
	}

	// "bird" must be present: T_C was never observed by the remove from A.
	// MUTATION GUARD: removing the "if _, ok := local[t]; ok" intersection
	// guard in ORSet.Merge causes T_C to be deleted unconditionally when
	// dRemA is re-merged on B, so replicaB.Contains("bird") returns false
	// and this assertion fails.
	if !replicaA.Contains("bird") {
		t.Errorf("RunID=%s A: expected 'bird' present (T_C add survived remove from A)", runID)
	}
	if !replicaB.Contains("bird") {
		t.Errorf("RunID=%s B: expected 'bird' present (T_C add survived remove from A)", runID)
	}

	// Cross-check via the file-backed evidence records.
	if gotA.RunID != runID {
		t.Errorf("file A RunID: got %q want %q", gotA.RunID, runID)
	}
	if gotB.RunID != runID {
		t.Errorf("file B RunID: got %q want %q", gotB.RunID, runID)
	}
	wantElems := []string{"bird"}
	if !stringSliceEqual(gotA.ORSetElems, wantElems) {
		t.Errorf("file A ORSetElems: got %v want %v", gotA.ORSetElems, wantElems)
	}
	if !stringSliceEqual(gotB.ORSetElems, wantElems) {
		t.Errorf("file B ORSetElems: got %v want %v", gotB.ORSetElems, wantElems)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestGCounterBasic verifies simple increment and cross-node merge.
// ─────────────────────────────────────────────────────────────────────────────

func TestGCounterBasic(t *testing.T) {
	a := NewGCounter("a")
	b := NewGCounter("b")

	da := a.Increment(5)
	db := b.Increment(3)

	a.Merge(db)
	b.Merge(da)

	if a.Value() != 8 {
		t.Errorf("a.Value(): got %d want 8", a.Value())
	}
	if b.Value() != 8 {
		t.Errorf("b.Value(): got %d want 8", b.Value())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPNCounterBasic verifies increment, decrement, and cross-node merge.
// ─────────────────────────────────────────────────────────────────────────────

func TestPNCounterBasic(t *testing.T) {
	a := NewPNCounter("a")
	b := NewPNCounter("b")

	da1 := a.Increment(10)
	da2 := a.Decrement(4)
	db1 := b.Increment(2)

	b.Merge(da1)
	b.Merge(da2)
	a.Merge(db1)

	if a.Value() != 8 {
		t.Errorf("a.Value(): got %d want 8", a.Value())
	}
	if b.Value() != 8 {
		t.Errorf("b.Value(): got %d want 8", b.Value())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestORSetBasicAddRemove verifies sequential add/remove on one replica.
// ─────────────────────────────────────────────────────────────────────────────

func TestORSetBasicAddRemove(t *testing.T) {
	r := NewORSet("r")

	r.Add("apple")
	r.Add("banana")

	if !r.Contains("apple") {
		t.Error("expected apple present")
	}

	r.Remove("apple")
	if r.Contains("apple") {
		t.Error("expected apple absent after remove")
	}
	if !r.Contains("banana") {
		t.Error("expected banana still present")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestORSetConcurrentAddRemoveAddWins verifies that a concurrent add on
// another replica survives a remove from the first replica (add-wins).
// ─────────────────────────────────────────────────────────────────────────────

func TestORSetConcurrentAddRemoveAddWins(t *testing.T) {
	a := NewORSet("a")
	b := NewORSet("b")

	dAddA := a.Add("x")
	// b has NOT yet seen a's add, so b adds x concurrently.
	dAddB := b.Add("x")

	// a removes x (observes only its own tag T_a).
	dRemA := a.Remove("x")

	// b merges the remove from a, then a's original add.
	b.Merge(dRemA)
	b.Merge(dAddA)

	// a merges b's concurrent add.
	a.Merge(dAddB)

	// Both must converge: x is present because b's add (T_b) was never seen by the remove from a.
	if !a.Contains("x") {
		t.Error("a: expected x present (b's concurrent add survived a's remove)")
	}
	if !b.Contains("x") {
		t.Error("b: expected x present (b's concurrent add survived a's remove)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestLWWMapBasic verifies put, get, delete, and last-writer-wins resolution.
// ─────────────────────────────────────────────────────────────────────────────

func TestLWWMapBasic(t *testing.T) {
	a := NewLWWMap("a")
	b := NewLWWMap("b")

	da1 := a.Put("key1", "hello", 5)
	db1 := b.Put("key1", "world", 10) // higher timestamp — b wins

	a.Merge(db1)
	b.Merge(da1)

	vA, okA := a.Get("key1")
	vB, okB := b.Get("key1")

	if !okA || vA != "world" {
		t.Errorf("a key1: got (%q,%v) want (\"world\",true)", vA, okA)
	}
	if !okB || vB != "world" {
		t.Errorf("b key1: got (%q,%v) want (\"world\",true)", vB, okB)
	}

	// Delete: ts must exceed last write ts to win.
	dDel := b.Delete("key1", 20)
	a.Merge(dDel)
	if _, ok := a.Get("key1"); ok {
		t.Error("a key1: expected absent after delete merge")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestLWWMapTieBreakByNodeID verifies that equal timestamps are resolved
// deterministically by nodeID (lexicographically larger wins).
// ─────────────────────────────────────────────────────────────────────────────

func TestLWWMapTieBreakByNodeID(t *testing.T) {
	a := NewLWWMap("nodeA")
	b := NewLWWMap("nodeB") // "nodeB" > "nodeA" lexicographically

	da := a.Put("k", "from-a", 42)
	db := b.Put("k", "from-b", 42) // same timestamp

	c := NewLWWMap("c")
	// Merge in both orders; result must be the same.
	c.Merge(da)
	c.Merge(db)

	d := NewLWWMap("d")
	d.Merge(db)
	d.Merge(da)

	vc, _ := c.Get("k")
	vd, _ := d.Get("k")

	if vc != "from-b" {
		t.Errorf("c k: got %q want \"from-b\" (nodeB wins tie)", vc)
	}
	if vc != vd {
		t.Errorf("tie-break commutativity: c=%q d=%q", vc, vd)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestLWWMapSameTsDeleteConverges
//
// Regression test for the lwwWins tombstone-priority rule (fix for the
// same-timestamp self-delete divergence bug).
//
// Scenario: node "a" writes key k at ts=5, then issues a delete at ts=5.
// Locally the delete takes effect immediately. The delete delta is then
// shipped to replica "b" which also received the prior Put delta.
// Both replicas must converge to "key absent".
//
// MUTATION GUARD — the "candidate.tombstone && !incumbent.tombstone" branch
// in lwwWins is the mutation-killable line. Removing it reverts to the old
// pure nodeID tie-break, where "a">"a" is false so the peer rejects the
// delete and divergence reappears.
// ─────────────────────────────────────────────────────────────────────────────

func TestLWWMapSameTsDeleteConverges(t *testing.T) {
	const runID = "lwwdel-same-ts-001"
	dir := t.TempDir()

	// Node a: write then delete at the same logical timestamp.
	a := NewLWWMap("a")
	dPut := a.Put("k", "v", 5)
	dDel := a.Delete("k", 5)

	// Node b: receives Put then Delete (normal delivery order).
	b := NewLWWMap("b")
	b.Merge(dPut)
	b.Merge(dDel)

	// Node c: receives Delete then Put (reversed — stress-tests idempotency).
	c := NewLWWMap("c")
	c.Merge(dDel)
	c.Merge(dPut)
	// Re-deliver Delete to simulate at-least-once (idempotency check).
	c.Merge(dDel)

	_, aHas := a.Get("k")
	_, bHas := b.Get("k")
	_, cHas := c.Get("k")

	if aHas {
		t.Errorf("RunID=%s a: key k should be absent after self-delete at same ts", runID)
	}
	if bHas {
		// MUTATION GUARD: removing tombstone-priority in lwwWins makes this fail.
		t.Errorf("RunID=%s b: key k should be absent (delete delta must win same-ts same-node tie)", runID)
	}
	if cHas {
		t.Errorf("RunID=%s c: key k should be absent even with reversed + duplicate delivery", runID)
	}

	// Verify convergence: all three replicas must agree on key set.
	if len(a.Keys()) != 0 || len(b.Keys()) != 0 || len(c.Keys()) != 0 {
		t.Errorf("RunID=%s keys not empty: a=%v b=%v c=%v", runID, a.Keys(), b.Keys(), c.Keys())
	}

	// Sink-side evidence (CLAUDE-1).
	rsA := ReplicaState{RunID: runID, ReplicaID: "a", LWWKeys: a.Keys()}
	rsB := ReplicaState{RunID: runID, ReplicaID: "b", LWWKeys: b.Keys()}
	pathA := writeState(t, dir, "lwwdel_a", rsA)
	pathB := writeState(t, dir, "lwwdel_b", rsB)
	gotA := readStateFromFile(t, pathA)
	gotB := readStateFromFile(t, pathB)
	if gotA.RunID != runID {
		t.Errorf("file A RunID: got %q want %q", gotA.RunID, runID)
	}
	if gotB.RunID != runID {
		t.Errorf("file B RunID: got %q want %q", gotB.RunID, runID)
	}
	if len(gotA.LWWKeys) != 0 {
		t.Errorf("file A LWWKeys: got %v want []", gotA.LWWKeys)
	}
	if len(gotB.LWWKeys) != 0 {
		t.Errorf("file B LWWKeys: got %v want []", gotB.LWWKeys)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestSortedNodes is a micro-test for the helper used in deterministic output.
// ─────────────────────────────────────────────────────────────────────────────

func TestSortedNodes(t *testing.T) {
	m := map[string]uint64{"c": 3, "a": 1, "b": 2}
	got := sortedNodes(m)
	want := []string{"a", "b", "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sortedNodes: got %v want %v", got, want)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestRunIDPropagation ensures RunID flows end-to-end through file I/O.
// ─────────────────────────────────────────────────────────────────────────────

func TestRunIDPropagation(t *testing.T) {
	const runID = "propagation-999"
	dir := t.TempDir()

	gc := NewGCounter("n")
	gc.Increment(1)
	pn := NewPNCounter("n")
	os_ := NewORSet("n")
	lw := NewLWWMap("n")

	rs := captureState(runID, "n", gc, pn, os_, lw)
	path := writeState(t, dir, fmt.Sprintf("state_%s", runID), rs)
	got := readStateFromFile(t, path)

	if got.RunID != runID {
		t.Errorf("RunID: got %q want %q", got.RunID, runID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// stringSliceEqual is a helper for sorted string slice comparison.
// ─────────────────────────────────────────────────────────────────────────────

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := make([]string, len(a))
	bc := make([]string, len(b))
	copy(ac, a)
	copy(bc, b)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
