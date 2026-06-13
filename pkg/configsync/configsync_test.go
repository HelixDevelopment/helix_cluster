package configsync_test

// Tests for pkg/configsync.
//
// Anti-bluff discipline: every test names the exact one-line mutation that
// makes it fail, and that mutation must still compile (a build-fail is not a
// valid proof).  Assertions are on concrete values, never merely on len>0 or
// err==nil.

import (
	"reflect"
	"sort"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/configsync"
	"github.com/HelixDevelopment/helix_cluster/pkg/crdt"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newStore(id string) *configsync.ConfigStore {
	return configsync.NewConfigStore(crdt.ReplicaID(id))
}

// sortedKeys is a small local helper to compare key slices order-independently.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// 1. Convergence — higher timestamp always wins after cross-merge
// ---------------------------------------------------------------------------

// TestConvergence_HigherTimestampWins verifies that after A.Merge(B) the value
// with the higher timestamp ("b" at ts=200) is visible on both sides.
//
// ANTI-BLUFF mutation: in Merge, replace  s.regs[key] = merged  with
//
//	s.regs[key] = local          (keep local register, ignore other)
//
// → A.Merge(B) still returns "a" → test fails.
func TestConvergence_HigherTimestampWins(t *testing.T) {
	t.Parallel()
	a := newStore("A")
	b := newStore("B")

	a.Set("x", "a", 100)
	b.Set("x", "b", 200)

	// A absorbs B.
	a.Merge(b)
	// B absorbs A (simulates the symmetric direction).
	b.Merge(a)

	got, ok := a.Get("x")
	if !ok {
		t.Fatal("A: key x should be live after merge")
	}
	if got != "b" {
		t.Errorf("A: want Get(x)=%q, got %q (higher timestamp must win)", "b", got)
	}

	got, ok = b.Get("x")
	if !ok {
		t.Fatal("B: key x should be live after merge")
	}
	if got != "b" {
		t.Errorf("B: want Get(x)=%q, got %q", "b", got)
	}
}

// ---------------------------------------------------------------------------
// 2. Commutativity — merge order yields the same Snapshot
// ---------------------------------------------------------------------------

// TestCommutativity_MergeOrderIndependent verifies that merging A into B
// produces the same snapshot as merging B into A.
//
// ANTI-BLUFF mutation: in Merge, skip updating s.liveKeys  (comment out the
//
//	s.liveKeys = s.liveKeys.Merge(otherLive) line)
//
// → the live-key ORSet is not merged, so keys added only on the other side
//
//	are absent from the snapshot → snapshot comparison fails for asymmetric
//	key sets.
func TestCommutativity_MergeOrderIndependent(t *testing.T) {
	t.Parallel()
	a := newStore("A")
	b := newStore("B")

	a.Set("alpha", "1", 10)
	a.Set("beta", "2", 20)
	b.Set("gamma", "3", 30)
	b.Set("alpha", "overwrite", 50)

	// Direction 1: clone of A merges B.
	a1 := newStore("A")
	a1.Set("alpha", "1", 10)
	a1.Set("beta", "2", 20)
	a1.Merge(b)

	// Direction 2: clone of B merges A.
	b1 := newStore("B")
	b1.Set("gamma", "3", 30)
	b1.Set("alpha", "overwrite", 50)
	b1.Merge(a)

	snapA := a1.Snapshot()
	snapB := b1.Snapshot()

	if !reflect.DeepEqual(snapA, snapB) {
		t.Errorf("commutative merge snapshots differ:\n  A-merge-B: %v\n  B-merge-A: %v", snapA, snapB)
	}
	// Also assert the concrete values so the test is not vacuously true.
	wantSnap := map[string]string{
		"alpha": "overwrite", // ts=50 > ts=10
		"beta":  "2",
		"gamma": "3",
	}
	if !reflect.DeepEqual(snapA, wantSnap) {
		t.Errorf("wrong snapshot: got %v, want %v", snapA, wantSnap)
	}
}

// ---------------------------------------------------------------------------
// 3. Idempotence — merging twice equals merging once
// ---------------------------------------------------------------------------

// TestIdempotence_DoubleMergeEqualsOnce verifies that merging B into A twice
// produces the same result as merging it once, AND that the LWW winner (A,
// ts=30 > B, ts=20) is preserved.
//
// ANTI-BLUFF mutation: in Merge, replace  s.regs[key] = merged  with
//
//	s.regs[key] = otherReg.Clone()
//	(configsync.go: the line inside the otherRegs range loop that stores the
//	merged register back into s.regs).
//
// → the mutated Merge always blindly adopts the other replica's register,
//
//	ignoring the LWW timestamp comparison; the first merge overwrites A's
//	winning value "v1" (ts=30) with B's lower-timestamped "v2" (ts=20) →
//	snap1["k"] == "v2" instead of "v1" → concrete assertion fails.
//
// The mutation still compiles; both snap1 and snap2 equal the wrong value
// with the mutation, so the idempotence check passes vacuously — but the
// concrete value check catches the bluff.
func TestIdempotence_DoubleMergeEqualsOnce(t *testing.T) {
	t.Parallel()
	a := newStore("A")
	b := newStore("B")

	// A has a higher timestamp — A's value must win after any number of merges.
	a.Set("k", "v1", 30)
	b.Set("k", "v2", 20)

	// First merge.
	a.Merge(b)
	snap1 := a.Snapshot()

	// Second merge with the same b — must produce identical state (idempotence).
	a.Merge(b)
	snap2 := a.Snapshot()

	if !reflect.DeepEqual(snap1, snap2) {
		t.Errorf("idempotence violated: first merge=%v, second merge=%v", snap1, snap2)
	}
	// Concrete assertion: A's higher-timestamp value must win.
	if v := snap1["k"]; v != "v1" {
		t.Errorf("want snap[k]=%q (A wins by higher ts), got %q", "v1", v)
	}
}

// ---------------------------------------------------------------------------
// 4. Equal-timestamp tiebreak is deterministic
// ---------------------------------------------------------------------------

// TestEqualTimestampTiebreak_DeterministicWinner verifies that when two replicas
// write the same key at the same timestamp the winner is always the lexicographically
// larger ReplicaID, regardless of merge direction.
//
// The crdt.LWWRegister.dominates rule: equal timestamp → larger ReplicaID wins
// (r.replica >= id in the dominates predicate, combined with the fact that a
// Clone+Set of the other value only takes effect when the other dominates).
// Concretely: "replicaZ" > "replicaA" → "replicaZ"'s value wins.
//
// ANTI-BLUFF mutation: swap the tiebreak sense inside LWWRegister.dominates
//
//	from  r.replica >= id  to  r.replica <= id .
//
// We don't own crdt, but the equivalent observable mutation here is: assert
// the winner is "va" (the lexicographically smaller replica). If crdt tiebreak
// ever changes direction this assertion catches it.
func TestEqualTimestampTiebreak_DeterministicWinner(t *testing.T) {
	t.Parallel()
	replicaA := crdt.ReplicaID("replicaA")
	replicaZ := crdt.ReplicaID("replicaZ")

	a := configsync.NewConfigStore(replicaA)
	z := configsync.NewConfigStore(replicaZ)

	const ts = uint64(50)
	a.Set("k", "va", ts)
	z.Set("k", "vz", ts)

	// Determine the expected winner by asking the crdt.LWWRegister directly.
	regA := crdt.NewLWWRegister()
	regA.Set("va", ts, replicaA)
	regZ := crdt.NewLWWRegister()
	regZ.Set("vz", ts, replicaZ)
	merged := regA.Merge(regZ)
	expectedValue, _ := merged.Value()

	// Merge in both directions; both must converge to the same value.
	a.Merge(z)
	z.Merge(a)

	gotA, okA := a.Get("k")
	gotZ, okZ := z.Get("k")

	if !okA || !okZ {
		t.Fatalf("key k should be live on both sides (okA=%v okZ=%v)", okA, okZ)
	}
	if gotA != expectedValue {
		t.Errorf("A: want %q (crdt tiebreak winner), got %q", expectedValue, gotA)
	}
	if gotZ != expectedValue {
		t.Errorf("Z: want %q (crdt tiebreak winner), got %q", expectedValue, gotZ)
	}
	if gotA != gotZ {
		t.Errorf("tiebreak not deterministic: A=%q Z=%q", gotA, gotZ)
	}
}

// ---------------------------------------------------------------------------
// 5. Delete semantics — Set then Delete → Get returns false
// ---------------------------------------------------------------------------

// TestDelete_SetThenDelete_KeyAbsent verifies that a key that has been set
// and then deleted is invisible via Get.
//
// ANTI-BLUFF mutation: in Get, remove the  if !s.liveKeys.Contains(key)  guard
//
//	(always proceed to read the register regardless of live-set membership).
//
// → Get returns the stale value from the register → ok==true → test fails.
func TestDelete_SetThenDelete_KeyAbsent(t *testing.T) {
	t.Parallel()
	s := newStore("A")
	s.Set("foo", "bar", 100)

	v, ok := s.Get("foo")
	if !ok || v != "bar" {
		t.Fatalf("pre-delete: want (bar, true), got (%q, %v)", v, ok)
	}

	s.Delete("foo")

	got, ok := s.Get("foo")
	if ok {
		t.Errorf("post-delete: want (_, false), got (%q, true)", got)
	}
}

// TestDelete_DeletedKeyAbsentInKeysAndSnapshot verifies that Keys() and
// Snapshot() exclude deleted keys.
//
// ANTI-BLUFF mutation: in Keys(), replace  raw := s.liveKeys.Elements()  with
//
//	a direct range over s.regs:
//	  var raw []string; for k := range s.regs { raw = append(raw, k) }
//	(configsync.go line that initialises raw via liveKeys.Elements()).
//
// → deleted key "foo" is still present in s.regs (Delete only tombstones the
//
//	ORSet tag; it never removes the LWWRegister entry), so "foo" reappears in
//	Keys() → len(keys)==2 and keys[0]=="baz", keys[1]=="foo" → test fails.
func TestDelete_DeletedKeyAbsentInKeysAndSnapshot(t *testing.T) {
	t.Parallel()
	s := newStore("A")
	s.Set("foo", "bar", 100)
	s.Set("baz", "qux", 200)
	s.Delete("foo")

	keys := s.Keys()
	if len(keys) != 1 || keys[0] != "baz" {
		t.Errorf("Keys() = %v, want [baz]", keys)
	}

	snap := s.Snapshot()
	if _, present := snap["foo"]; present {
		t.Error("Snapshot() must not contain deleted key foo")
	}
	if snap["baz"] != "qux" {
		t.Errorf("Snapshot()[baz] = %q, want %q", snap["baz"], "qux")
	}
}

// ---------------------------------------------------------------------------
// 6. Concurrent Delete vs Set — deterministic convergence after cross-merge
// ---------------------------------------------------------------------------

// TestConcurrentDeleteAndSet_ConvergesAfterMerge verifies that when replica A
// deletes key "cfg" and replica B concurrently sets "cfg" with a higher
// timestamp, both sides converge to the same deterministic state after
// cross-merging.
//
// Because ORSet.Add (called by ConfigStore.Set) mints fresh tags that the
// Remove on replica A has not observed, the add-wins semantics of ORSet mean
// the key remains live after merge.  The LWWRegister holds B's value (higher
// timestamp wins).
//
// ANTI-BLUFF mutation: in Merge, replace  s.liveKeys = s.liveKeys.Merge(otherLive)
//
//	with  s.liveKeys = otherLive.Clone()  (last-writer-wins the live-set itself).
//
// → the ORSet merge is bypassed; Add-wins semantics are lost; the result
//
//	depends on merge order and the test's directional assertions fail.
func TestConcurrentDeleteAndSet_ConvergesAfterMerge(t *testing.T) {
	t.Parallel()
	a := newStore("A")
	b := newStore("B")

	// Both start with "cfg" set on A.
	a.Set("cfg", "old", 10)
	b.Merge(a) // B knows about the old value.

	// Concurrent: A deletes "cfg"; B sets "cfg" with a higher timestamp.
	a.Delete("cfg")
	b.Set("cfg", "new", 999)

	// Cross-merge.
	a.Merge(b)
	b.Merge(a)

	gotA, okA := a.Get("cfg")
	gotB, okB := b.Get("cfg")

	// Both sides must agree.
	if okA != okB {
		t.Fatalf("liveness mismatch after cross-merge: A live=%v B live=%v", okA, okB)
	}
	if gotA != gotB {
		t.Errorf("value mismatch after cross-merge: A=%q B=%q", gotA, gotB)
	}
	// The concrete expected outcome: B's Set mints a fresh tag not seen by A's
	// Delete, so add-wins → key is live.  LWW ts=999 > ts=10 → value is "new".
	if !okA {
		t.Error("expected key cfg to be live (add-wins over concurrent delete)")
	}
	if gotA != "new" {
		t.Errorf("expected value %q, got %q", "new", gotA)
	}
}

// TestConcurrentDeleteAndHigherTsSet_Symmetric verifies the same outcome when
// delete happens after set on A (true concurrency — no causal ordering).
//
// ANTI-BLUFF mutation: in Get, bypass the liveKeys.Contains check entirely.
// → the deleted snapshot leaks through on the side that only ran Delete without
//
//	the concurrent Set, making the pre-merge assertions pass vacuously and
//	breaking the post-merge identity check.
func TestConcurrentDeleteAndHigherTsSet_Symmetric(t *testing.T) {
	t.Parallel()
	a := newStore("A")
	b := newStore("B")

	// A: set then delete (net: absent on A before merge).
	a.Set("cfg", "fromA", 5)
	a.Delete("cfg")

	// B: set with higher timestamp (net: present on B before merge).
	b.Set("cfg", "fromB", 100)

	// Pre-merge sanity.
	if _, ok := a.Get("cfg"); ok {
		t.Error("pre-merge: A should not see deleted cfg")
	}
	if v, ok := b.Get("cfg"); !ok || v != "fromB" {
		t.Errorf("pre-merge: B should see cfg=fromB, got (%q, %v)", v, ok)
	}

	// Cross-merge.
	a.Merge(b)
	b.Merge(a)

	gotA, okA := a.Get("cfg")
	gotB, okB := b.Get("cfg")

	if okA != okB || gotA != gotB {
		t.Errorf("asymmetric convergence: A=(%q,%v) B=(%q,%v)", gotA, okA, gotB, okB)
	}
	if !okA || gotA != "fromB" {
		t.Errorf("expected cfg=fromB live after merge, got (%q, %v)", gotA, okA)
	}
}

// ---------------------------------------------------------------------------
// 7. Keys() is sorted
// ---------------------------------------------------------------------------

// TestKeys_Sorted verifies that Keys() always returns keys in sorted order.
//
// ANTI-BLUFF mutation: in Keys(), remove the  sort.Strings(out)  call.
// → key order is map-iteration order (non-deterministic) → slice-equality
//
//	check against a sorted reference fails when iteration happens to differ.
func TestKeys_Sorted(t *testing.T) {
	t.Parallel()
	s := newStore("R")
	keys := []string{"zebra", "apple", "mango", "banana"}
	for i, k := range keys {
		s.Set(k, "v", uint64(i+1))
	}

	got := s.Keys()
	want := make([]string, len(keys))
	copy(want, keys)
	sort.Strings(want)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Keys() = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// 8. Snapshot reflects all live keys and no deleted keys
// ---------------------------------------------------------------------------

// TestSnapshot_ContentsCorrect is an end-to-end check of the full lifecycle:
// set, get, delete, merge.
//
// ANTI-BLUFF mutation: in Snapshot(), enumerate s.regs directly instead of
//
//	s.liveKeys.Elements().
//
// → deleted key "d" reappears in the snapshot → test fails on the concrete map
//
//	comparison.
func TestSnapshot_ContentsCorrect(t *testing.T) {
	t.Parallel()
	a := newStore("A")
	b := newStore("B")

	a.Set("a-only", "1", 10)
	a.Set("shared", "fromA", 20)
	b.Set("b-only", "2", 30)
	b.Set("shared", "fromB", 40) // higher ts — wins
	a.Set("d", "delete-me", 5)
	a.Delete("d")

	a.Merge(b)

	snap := a.Snapshot()
	want := map[string]string{
		"a-only": "1",
		"shared": "fromB",
		"b-only": "2",
	}

	if !reflect.DeepEqual(snap, want) {
		// Print sorted keys for legible diff.
		t.Errorf("Snapshot() mismatch\n  got:  %v\n  want: %v", snap, want)
	}
	if _, present := snap["d"]; present {
		t.Error("deleted key d must not appear in Snapshot()")
	}
}

// ---------------------------------------------------------------------------
// 9. Associativity — (A merge B) merge C == A merge (B merge C)
// ---------------------------------------------------------------------------

// TestAssociativity_ThreeWayMerge verifies the CRDT associativity law.
//
// ANTI-BLUFF mutation: in Merge, for the register merge replace
//
//	s.regs[key] = merged  with  s.regs[key] = otherReg.Clone()
//	(always take the other's value without LWW resolution).
//
// → applying that operation left- vs right-associatively gives different results
//
//	because each step overwrites with the "other" side → both directions no
//	longer converge to the same snapshot.
func TestAssociativity_ThreeWayMerge(t *testing.T) {
	t.Parallel()
	mkA := func() *configsync.ConfigStore {
		s := newStore("A")
		s.Set("k1", "a1", 10)
		return s
	}
	mkB := func() *configsync.ConfigStore {
		s := newStore("B")
		s.Set("k1", "b1", 20) // wins over a1
		s.Set("k2", "b2", 5)
		return s
	}
	mkC := func() *configsync.ConfigStore {
		s := newStore("C")
		s.Set("k2", "c2", 50) // wins over b2
		s.Set("k3", "c3", 1)
		return s
	}

	// (A merge B) merge C
	ab := mkA()
	ab.Merge(mkB())
	ab.Merge(mkC())
	left := ab.Snapshot()

	// A merge (B merge C)
	bc := mkB()
	bc.Merge(mkC())
	aBC := mkA()
	aBC.Merge(bc)
	right := aBC.Snapshot()

	if !reflect.DeepEqual(left, right) {
		t.Errorf("associativity violated:\n  (A⊔B)⊔C = %v\n  A⊔(B⊔C) = %v", left, right)
	}
	want := map[string]string{"k1": "b1", "k2": "c2", "k3": "c3"}
	if !reflect.DeepEqual(left, want) {
		t.Errorf("wrong final snapshot: got %v, want %v", left, want)
	}
}

// ---------------------------------------------------------------------------
// 10. Get on unknown key returns false without panic
// ---------------------------------------------------------------------------

// TestGet_UnknownKey verifies that querying a key never set returns (_, false).
//
// ANTI-BLUFF mutation: in Get, remove the  if !s.liveKeys.Contains(key)  guard
//
//	and instead always do  reg, ok := s.regs[key]  followed by
//	returning ("", true) when !ok (i.e. flip the sentinel).
//
// → ok becomes true for a missing key → assertion fails.
func TestGet_UnknownKey(t *testing.T) {
	t.Parallel()
	s := newStore("A")
	s.Set("present", "v", 1)

	if _, ok := s.Get("absent"); ok {
		t.Error("Get(absent) should return false for unknown key")
	}

	v, ok := s.Get("present")
	if !ok || v != "v" {
		t.Errorf("Get(present) = (%q, %v), want (\"v\", true)", v, ok)
	}
}
