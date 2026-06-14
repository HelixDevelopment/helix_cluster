package configsync_test

// Adversarial / concurrency-focused tests for pkg/configsync.
//
// These complement configsync_test.go (which pins the CRDT laws) by attacking
// the *contract under concurrency* and the *defensive-copy / aliasing* surface:
//
//   - VERSIONING: Set advances the LWW winner monotonically by timestamp; a
//     stale (lower or equal-from-same-replica) write is superseded, never
//     overwrites a newer value; Get returns the latest value.
//   - CONCURRENCY: many goroutines hammer Get/Set/Delete/Keys/Snapshot/Merge
//     under `go test -race` — the centerpiece. Asserts no race, no panic, and
//     a consistent final state.
//   - DEFENSIVE COPY: the map returned by Snapshot must not alias internal
//     storage — mutating it must not corrupt the store.
//   - EDGES: get-missing, delete-missing, double-delete, empty store, zero ts.
//
// Anti-bluff: the load-bearing mutation that makes each test fail is named in
// the doc comment, and the mutation must compile.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/configsync"
	"github.com/HelixDevelopment/helix_cluster/pkg/crdt"
)

// ---------------------------------------------------------------------------
// A. VERSIONING — monotonic last-writer-wins by timestamp
// ---------------------------------------------------------------------------

// TestVersioning_StaleWriteDoesNotOverwriteNewer pins the core overwrite
// policy: once a value is written at timestamp T, a subsequent Set at a
// timestamp < T (a stale / out-of-order write) MUST be superseded — the newer
// value remains visible. This is the "stale write overwriting a newer value"
// bug class, pinned to the documented LWW contract.
//
// ANTI-BLUFF mutation: in crdt.LWWRegister.dominates, change
//
//	return r.timestamp > timestamp   →   return r.timestamp < timestamp
//
// → the stale ts=50 write would then win over ts=100 → Get returns "stale" →
// this test fails. (Equivalent observable mutation; we don't own crdt but the
// configsync contract is what we assert here.)
func TestVersioning_StaleWriteDoesNotOverwriteNewer(t *testing.T) {
	t.Parallel()
	s := newStore("A")

	s.Set("k", "newer", 100)
	if v, ok := s.Get("k"); !ok || v != "newer" {
		t.Fatalf("after first Set: got (%q,%v), want (newer,true)", v, ok)
	}

	// Out-of-order / stale write with a strictly lower timestamp.
	s.Set("k", "stale", 50)

	v, ok := s.Get("k")
	if !ok {
		t.Fatal("key k must remain live")
	}
	if v != "newer" {
		t.Errorf("stale write (ts=50) overwrote newer value (ts=100): got %q, want %q", v, "newer")
	}
}

// TestVersioning_EqualTimestampSameReplicaIgnored pins idempotence under
// replay: a Set at the SAME timestamp from the SAME replica is ignored (time
// never moves backward, ties from the same author do not flip the value).
//
// ANTI-BLUFF mutation: in crdt.LWWRegister.dominates, change the tiebreak
//
//	return r.replica >= id   →   return r.replica > id
//
// → an equal-ts re-Set from the same replica would no longer be dominated, so
// "second" would overwrite "first" → this test fails.
func TestVersioning_EqualTimestampSameReplicaIgnored(t *testing.T) {
	t.Parallel()
	s := newStore("A")

	s.Set("k", "first", 42)
	s.Set("k", "second", 42) // same ts, same replica → must be ignored

	v, ok := s.Get("k")
	if !ok || v != "first" {
		t.Errorf("equal-ts same-replica re-Set must be ignored: got (%q,%v), want (first,true)", v, ok)
	}
}

// TestVersioning_MonotonicAdvance pins that strictly increasing timestamps
// each take effect, so the latest write always wins.
//
// ANTI-BLUFF mutation: in ConfigStore.Set, drop the reg.Set call
// (comment out `reg.Set(value, timestamp, s.replica)`) — the value never
// advances past the first → got != "v9" → fails. (Mutation compiles: reg is
// still referenced by the liveKeys.Add path is independent; to keep it
// referenced, the equivalent compiling mutation is `_ = reg`.)
func TestVersioning_MonotonicAdvance(t *testing.T) {
	t.Parallel()
	s := newStore("A")
	for i := 0; i <= 9; i++ {
		s.Set("k", fmt.Sprintf("v%d", i), uint64(i+1))
	}
	if v, ok := s.Get("k"); !ok || v != "v9" {
		t.Errorf("monotonic advance: got (%q,%v), want (v9,true)", v, ok)
	}
}

// ---------------------------------------------------------------------------
// B. CONCURRENCY — the centerpiece. Run with: go test -race -count=1
// ---------------------------------------------------------------------------

// TestConcurrent_GetSetDelete_NoRace hammers a single shared store from many
// goroutines doing Set / Get / Delete / Keys / Snapshot simultaneously. Under
// `-race` this proves every map access is guarded by s.mu. It also asserts no
// panic and that the store remains internally consistent (Get agrees with
// Snapshot for every key Snapshot reports).
//
// ANTI-BLUFF mutation: in ConfigStore.Get (or Set), delete the
//
//	s.mu.RLock(); defer s.mu.RUnlock()   (resp. s.mu.Lock())
//
// → concurrent map read + map write on s.regs / liveKeys → the race detector
// reports "DATA RACE" and the test fails / the binary aborts. (Proven below in
// the report via a temporary edit.)
func TestConcurrent_GetSetDelete_NoRace(t *testing.T) {
	t.Parallel()
	s := newStore("R")

	const (
		workers = 24
		iters   = 1500
		nKeys   = 16
	)
	keyOf := func(i int) string { return fmt.Sprintf("key-%d", i%nKeys) }

	var ts uint64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				k := keyOf(w*iters + i)
				switch (w + i) % 5 {
				case 0, 1:
					s.Set(k, fmt.Sprintf("v-%d-%d", w, i), atomic.AddUint64(&ts, 1))
				case 2:
					_, _ = s.Get(k)
				case 3:
					s.Delete(k)
				case 4:
					_ = s.Keys()
					_ = s.Snapshot()
				}
			}
		}(w)
	}
	wg.Wait()

	// Consistency invariant: whatever Snapshot reports, Get must agree on.
	snap := s.Snapshot()
	for k, want := range snap {
		got, ok := s.Get(k)
		if !ok {
			t.Errorf("Snapshot reports %q live but Get says absent", k)
			continue
		}
		if got != want {
			t.Errorf("Snapshot/Get disagree for %q: snap=%q get=%q", k, want, got)
		}
	}
	// Keys() must list exactly the snapshot's key set.
	if len(s.Keys()) != len(snap) {
		t.Errorf("Keys() count %d != Snapshot() count %d", len(s.Keys()), len(snap))
	}
}

// TestConcurrent_MergeUnderLoad runs concurrent Merge of two stores while both
// are being mutated, exercising the cross-store lock path (other.snapshot()
// under other's RLock, then s.mu.Lock()). Under `-race` this proves Merge does
// not touch other's maps without other's lock, and never deadlocks.
//
// ANTI-BLUFF mutation: in ConfigStore.Merge, replace the
//
//	otherRegs, otherLive := other.snapshot()
//
// snapshot call with direct field access (e.g. ranging other.regs without
// other.mu) — this is impossible from the external package, but the equivalent
// internal mutation makes the race detector fire on other.regs. We assert the
// observable effect: after the storm, a final deterministic cross-merge makes
// both stores converge.
func TestConcurrent_MergeUnderLoad(t *testing.T) {
	t.Parallel()
	a := newStore("A")
	b := newStore("B")

	var ts uint64
	var wg sync.WaitGroup

	// Writers on A and B.
	for _, store := range []*configsync.ConfigStore{a, b} {
		st := store
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				st.Set(fmt.Sprintf("k-%d", i%8), fmt.Sprintf("x%d", i), atomic.AddUint64(&ts, 1))
			}
		}()
	}
	// Concurrent mergers in both directions.
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			a.Merge(b)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			b.Merge(a)
		}
	}()
	wg.Wait()

	// Final quiescent cross-merge: stores must converge to identical snapshots.
	a.Merge(b)
	b.Merge(a)
	a.Merge(b) // one extra each way to flush both directions to a fixpoint
	b.Merge(a)

	sa := a.Snapshot()
	sb := b.Snapshot()
	if len(sa) != len(sb) {
		t.Fatalf("after convergence snapshots differ in size: A=%d B=%d", len(sa), len(sb))
	}
	for k, va := range sa {
		if vb, ok := sb[k]; !ok || vb != va {
			t.Errorf("post-convergence divergence at %q: A=%q B=(%q,%v)", k, va, vb, ok)
		}
	}
}

// ---------------------------------------------------------------------------
// C. DEFENSIVE COPY — Snapshot must not alias internal state
// ---------------------------------------------------------------------------

// TestSnapshot_DefensiveCopy verifies that mutating the map returned by
// Snapshot cannot corrupt the store, and that a later Set is not reflected in
// a previously returned snapshot (it is a point-in-time copy).
//
// ANTI-BLUFF mutation: change Snapshot to return s's internal map directly,
// e.g. cache `s.snap = result` and `return s.snap` reusing the same map across
// calls — then the caller's deletion below would corrupt the store and the
// second Snapshot would observe the caller's mutation. With the real (fresh
// map each call) implementation the store is intact.
func TestSnapshot_DefensiveCopy(t *testing.T) {
	t.Parallel()
	s := newStore("A")
	s.Set("a", "1", 10)
	s.Set("b", "2", 20)

	snap := s.Snapshot()
	// Adversarially mutate the returned map.
	delete(snap, "a")
	snap["b"] = "TAMPERED"
	snap["c"] = "injected"

	// Store must be wholly intact.
	if v, ok := s.Get("a"); !ok || v != "1" {
		t.Errorf("store corrupted via snapshot mutation: a=(%q,%v), want (1,true)", v, ok)
	}
	if v, ok := s.Get("b"); !ok || v != "2" {
		t.Errorf("store corrupted via snapshot mutation: b=(%q,%v), want (2,true)", v, ok)
	}
	if _, ok := s.Get("c"); ok {
		t.Error("injected key c leaked into store via snapshot mutation")
	}

	// A fresh snapshot reflects the true state, not the tampered one.
	snap2 := s.Snapshot()
	want := map[string]string{"a": "1", "b": "2"}
	if len(snap2) != len(want) {
		t.Fatalf("fresh snapshot size %d, want %d", len(snap2), len(want))
	}
	for k, v := range want {
		if snap2[k] != v {
			t.Errorf("fresh snapshot[%q]=%q, want %q", k, snap2[k], v)
		}
	}
}

// TestKeys_ReturnedSliceIndependent verifies the slice returned by Keys() is
// not aliased to internal storage in a way that a later mutation observes, and
// that mutating the returned slice does not affect a subsequent Keys() call.
// (Keys builds out via raw[:0:len(raw)] over a fresh Elements() slice — this
// pins that the reuse stays internal and safe.)
//
// ANTI-BLUFF mutation: have Keys() return a cached package-level slice shared
// across calls — then clobbering keys1 below would corrupt keys2.
func TestKeys_ReturnedSliceIndependent(t *testing.T) {
	t.Parallel()
	s := newStore("A")
	s.Set("alpha", "1", 1)
	s.Set("beta", "2", 2)
	s.Set("gamma", "3", 3)

	keys1 := s.Keys()
	for i := range keys1 {
		keys1[i] = "CLOBBERED"
	}

	keys2 := s.Keys()
	want := []string{"alpha", "beta", "gamma"}
	if len(keys2) != len(want) {
		t.Fatalf("second Keys() len=%d, want %d", len(keys2), len(want))
	}
	for i, w := range want {
		if keys2[i] != w {
			t.Errorf("Keys() returned clobbered/aliased data: keys2[%d]=%q, want %q", i, keys2[i], w)
		}
	}
}

// ---------------------------------------------------------------------------
// D. EDGES — no panic on degenerate inputs
// ---------------------------------------------------------------------------

// TestEdge_DeleteMissingAndDoubleDelete verifies Delete on an absent key and a
// double Delete are no-ops that never panic.
//
// ANTI-BLUFF mutation: make ORSet.Remove index s.adds[element] without a nil
// guard and then range it — a missing key would panic. We assert no panic and
// correct absence.
func TestEdge_DeleteMissingAndDoubleDelete(t *testing.T) {
	t.Parallel()
	s := newStore("A")

	s.Delete("never-existed") // must not panic
	if _, ok := s.Get("never-existed"); ok {
		t.Error("deleting a missing key made it appear")
	}

	s.Set("k", "v", 1)
	s.Delete("k")
	s.Delete("k") // double delete must not panic
	if _, ok := s.Get("k"); ok {
		t.Error("key k should remain absent after double delete")
	}
}

// TestEdge_EmptyStore verifies all read paths behave on an empty store.
func TestEdge_EmptyStore(t *testing.T) {
	t.Parallel()
	s := newStore("A")

	if v, ok := s.Get("anything"); ok || v != "" {
		t.Errorf("empty Get = (%q,%v), want (\"\",false)", v, ok)
	}
	if k := s.Keys(); len(k) != 0 {
		t.Errorf("empty Keys() = %v, want []", k)
	}
	if snap := s.Snapshot(); len(snap) != 0 {
		t.Errorf("empty Snapshot() = %v, want {}", snap)
	}
}

// TestEdge_ZeroTimestampAndEmptyValue verifies a ts=0 write and empty-string
// value are valid and visible (zero is a legitimate timestamp; empty value is
// distinct from "absent").
//
// ANTI-BLUFF mutation: in Get, return ("",false) whenever the value == ""
// (conflating empty value with unset) → this test fails because the key is
// genuinely set to "".
func TestEdge_ZeroTimestampAndEmptyValue(t *testing.T) {
	t.Parallel()
	s := newStore("A")
	s.Set("k", "", 0)

	v, ok := s.Get("k")
	if !ok {
		t.Fatal("key set with empty value at ts=0 must be live")
	}
	if v != "" {
		t.Errorf("got %q, want empty string", v)
	}
	if snap := s.Snapshot(); snap["k"] != "" {
		t.Errorf("Snapshot[k]=%q, want empty string; map=%v", snap["k"], snap)
	}
}

// TestEdge_ResurrectAfterDelete pins the documented resurrection contract: a
// direct Set after a Delete re-adds the key (ORSet.Add mints a fresh tag).
//
// ANTI-BLUFF mutation: in Set, drop the s.liveKeys.Add(key, s.replica) call →
// after Delete the key stays tombstoned → Get returns false → fails.
func TestEdge_ResurrectAfterDelete(t *testing.T) {
	t.Parallel()
	s := newStore("A")
	s.Set("k", "v1", 10)
	s.Delete("k")
	if _, ok := s.Get("k"); ok {
		t.Fatal("key should be absent right after delete")
	}
	s.Set("k", "v2", 20) // resurrect
	v, ok := s.Get("k")
	if !ok || v != "v2" {
		t.Errorf("resurrected key = (%q,%v), want (v2,true)", v, ok)
	}
}

// guard against an unused import if the file is edited down later.
var _ = crdt.ReplicaID("")
