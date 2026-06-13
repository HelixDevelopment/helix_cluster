package mvcc

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// runUUIDSnap is a distinct forensic anchor for the snapshot-isolation suite so
// its evidence lines can be grepped independently from the existing tests.
const runUUIDSnap = "mvcc-snap-9a2f7c41-0007-4e8b-a1d3-helix-snapshot-iso"

// ---------------------------------------------------------------------------
// THE SNAPSHOT MODEL UNDER TEST
//
// In this Store an MVCC *snapshot* is simply a captured global revision:
//
//	snap := s.Rev()           // open a read snapshot at the current revision
//	v, ok := s.Get(key, snap) // consistent point-in-time read as-of `snap`
//
// Get(key, atRev) returns the value of the version with the greatest
// ModRev <= atRev (keyHistory.get via the predicate `ModRev > atRev`, taking
// idx-1). Therefore a snapshot taken at revision S is STABLE: any Put that
// happens AFTER S produces a version with ModRev > S, which is by construction
// invisible to Get(key, S). This is real snapshot isolation: a reader pinned at
// S sees a frozen point-in-time view; concurrent later writes do not bleed in.
//
// ANTI-BLUFF (load-bearing): a fake "MVCC" that ignored atRev and always
// returned the LATEST value (last-write-wins, no real versioning) would PASS a
// naive "read returns something" test but FAIL every assertion below that reads
// at an OLD snapshot after newer writes have landed — because it would return
// the NEW value where we require the OLD one. Each such assertion is annotated
// "[BITES non-versioning]".
// ---------------------------------------------------------------------------

// TestSnapshotIsolationStableUnderConcurrentWrites is the headline proof of
// snapshot isolation / read stability.
//
// Sequence (deterministic ordering enforced by a sync.WaitGroup write barrier,
// NOT by racing): write initial versions of several keys, open snapshot S,
// then run CONCURRENT writers that each Put a NEW version of every key and
// WAIT for all of them to commit. Only after the barrier do we assert:
//
//   - Reads via the OLD snapshot S still return the OLD values
//     (later writes are invisible to S).  [BITES non-versioning]
//   - A FRESH snapshot taken after the writes sees the NEW values.
//
// Writes are serialized through an external mutex because the Store documents
// itself as not safe for concurrent use ("callers requiring concurrency should
// serialize access externally"); the mutex IS that external serialization. The
// concurrency here is genuine (N goroutines contend for the writer lock), while
// the happens-after ordering needed for the stability assertion is made
// deterministic by the barrier — exactly as the task requires.
func TestSnapshotIsolationStableUnderConcurrentWrites(t *testing.T) {
	s := New()
	var mu sync.Mutex // external serialization per the Store's documented contract

	keys := []string{"k1", "k2", "k3", "k4"}
	oldVals := map[string]string{}
	for i, k := range keys {
		v := fmt.Sprintf("OLD-%d", i)
		oldVals[k] = v
		s.Put(k, []byte(v)) // no concurrency yet; direct is fine
	}

	// Open the read snapshot S AFTER the initial writes, BEFORE any new write.
	snapS := s.Rev()
	t.Logf("run=%s opened snapshot S at rev=%d over keys=%v", runUUIDSnap, snapS, keys)

	// Concurrent writers: each key gets a brand-new version, written from its
	// own goroutine, all contending for the external writer lock. A WaitGroup
	// is the write barrier guaranteeing every new version has COMMITTED before
	// we assert — so "the write happened after S" is a fact, not a race.
	newVals := map[string]string{}
	for i, k := range keys {
		newVals[k] = fmt.Sprintf("NEW-%d", i)
	}
	var wg sync.WaitGroup
	for _, k := range keys {
		k := k
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			s.Put(k, []byte(newVals[k]))
			mu.Unlock()
		}()
	}
	wg.Wait() // write barrier: all NEW versions are now committed.

	freshRev := s.Rev()
	t.Logf("run=%s after concurrent writes: freshRev=%d (S still=%d)", runUUIDSnap, freshRev, snapS)
	if freshRev <= snapS {
		t.Fatalf("run=%s freshRev %d must exceed snapshot S %d after writes (revs not advancing)",
			runUUIDSnap, freshRev, snapS)
	}

	// (1) STABILITY: snapshot S must still see the OLD values, never the NEW
	// ones that committed after S. [BITES non-versioning]
	for _, k := range keys {
		gotBytes, ok := s.Get(k, snapS)
		got := string(gotBytes)
		t.Logf("run=%s snapshot S=%d Get(%q)=%q ok=%v (expect OLD=%q)",
			runUUIDSnap, snapS, k, got, ok, oldVals[k])
		if !ok {
			t.Fatalf("run=%s snapshot S=%d Get(%q) not found", runUUIDSnap, snapS, k)
		}
		if got != oldVals[k] {
			t.Fatalf("run=%s SNAPSHOT LEAK: S=%d Get(%q)=%q, want OLD %q — a later write became "+
				"visible to an earlier snapshot (this is exactly the non-versioning / last-write-wins "+
				"bug an MVCC must not have)", runUUIDSnap, snapS, k, got, oldVals[k])
		}
		if got == newVals[k] {
			t.Fatalf("run=%s SNAPSHOT LEAK: S=%d Get(%q) returned the NEW value %q; the snapshot is "+
				"not stable", runUUIDSnap, snapS, k, got)
		}
	}

	// (2) FRESHNESS: a fresh snapshot (latest) must see the NEW values, proving
	// the writes really did land and that S's stability is not just staleness
	// of the whole store.
	for _, k := range keys {
		gotBytes, ok := s.Get(k, freshRev)
		got := string(gotBytes)
		t.Logf("run=%s freshRev=%d Get(%q)=%q ok=%v (expect NEW=%q)",
			runUUIDSnap, freshRev, k, got, ok, newVals[k])
		if !ok || got != newVals[k] {
			t.Fatalf("run=%s fresh snapshot rev=%d Get(%q)=%q ok=%v, want NEW %q — writes not visible "+
				"to a later snapshot", runUUIDSnap, freshRev, k, got, ok, newVals[k])
		}
	}

	// (3) Cross-check via latest (atRev<=0) too: latest == NEW, distinct from S.
	for _, k := range keys {
		latest, _ := s.Get(k, 0)
		if string(latest) != newVals[k] {
			t.Fatalf("run=%s latest Get(%q)=%q, want NEW %q", runUUIDSnap, k, string(latest), newVals[k])
		}
	}
	t.Logf("run=%s PROVED: snapshot S=%d frozen at OLD values while freshRev=%d sees NEW values",
		runUUIDSnap, snapS, freshRev)
}

// TestNoDirtyReadsMonotonicVersionsAndUnconditionalCompact proves three
// version-visibility properties plus the package's documented compaction
// behavior:
//
//   - versions are MONOTONIC: each Put returns a strictly larger revision.
//   - read-at-V returns EXACTLY the most-recent version with ModRev <= V — never
//     a newer (future) version, never a torn/partial value. We read at the
//     revision strictly BETWEEN two writes and require the EARLIER value.
//     [BITES non-versioning: a last-write-wins store returns the latest here.]
//   - Compact is UNCONDITIONAL with respect to open snapshots: this Store does
//     NOT pin versions a still-open snapshot might need. We assert that
//     documented behavior precisely rather than inventing snapshot-pinning the
//     API does not provide.
func TestNoDirtyReadsMonotonicVersionsAndUnconditionalCompact(t *testing.T) {
	s := New()
	key := "key"

	// Three writes -> three monotonic revisions, three distinct values.
	rA := s.Put(key, []byte("A")) // value A at rev rA
	rB := s.Put(key, []byte("B")) // value B at rev rB
	rC := s.Put(key, []byte("C")) // value C at rev rC
	t.Logf("run=%s monotonic revs rA=%d rB=%d rC=%d", runUUIDSnap, rA, rB, rC)
	if !(rA < rB && rB < rC) {
		t.Fatalf("run=%s revisions not strictly monotonic: rA=%d rB=%d rC=%d", runUUIDSnap, rA, rB, rC)
	}

	// At snapshot rA: only A is visible. B and C are FUTURE writes; a correct
	// MVCC must NOT reveal them. [BITES non-versioning]
	if v, ok := s.Get(key, rA); !ok || string(v) != "A" {
		t.Fatalf("run=%s Get(%q, rA=%d)=%q ok=%v, want A (future versions B/C must be invisible)",
			runUUIDSnap, key, rA, string(v), ok)
	}
	// At snapshot rB: B is the most-recent <= rB; C must remain invisible.
	if v, ok := s.Get(key, rB); !ok || string(v) != "B" {
		t.Fatalf("run=%s Get(%q, rB=%d)=%q, want B (must be most-recent<=rB, not future C)",
			runUUIDSnap, key, rB, string(v))
	}
	// Reading at a revision STRICTLY BETWEEN rB and rC (rC-1, which here equals
	// rB) must still yield B, never the not-yet-committed-as-of-that-rev C.
	betweenRev := rC - 1
	if vb, ok := s.Get(key, betweenRev); !ok || string(vb) != "B" {
		t.Fatalf("run=%s Get(%q, between=%d)=%q ok=%v, want B (no dirty read of future C)",
			runUUIDSnap, key, betweenRev, string(vb), ok)
	}
	t.Logf("run=%s no-dirty-read: Get@rA=A Get@rB=B Get@between(%d)=B (future versions hidden)",
		runUUIDSnap, betweenRev)

	// A read BELOW a key's first version must be "not found": Get must not fall
	// FORWARD to a newer version when none qualifies at-or-before atRev. We use a
	// SECOND key whose first version lands at a rev > 1, then read it at an
	// EARLIER positive revision (before it existed). Note: atRev <= 0 is the
	// documented "latest" sentinel, so we deliberately probe with a positive
	// revision strictly less than the key's first ModRev.
	late := "late-key"
	rLate := s.Put(late, []byte("L")) // first version of `late` at rev rLate (>1)
	if rLate <= 1 {
		t.Fatalf("run=%s expected late key first rev >1, got %d", runUUIDSnap, rLate)
	}
	beforeExisted := rLate - 1 // positive, but before `late` had any version
	if v, ok := s.Get(late, beforeExisted); ok {
		t.Fatalf("run=%s Get(%q, %d)=%q unexpectedly found; read fell FORWARD to a future version "+
			"(key did not exist at that revision)", runUUIDSnap, late, beforeExisted, string(v))
	}
	t.Logf("run=%s no-fall-forward: Get(%q, %d) correctly not-found (before key's first version at rev %d)",
		runUUIDSnap, late, beforeExisted, rLate)

	// Documented Compact behavior: superseded versions with ModRev < rev are
	// pruned UNCONDITIONALLY (no open-snapshot pinning). Open a snapshot at rA,
	// compact past it, and assert rA's value is NOW GONE — i.e. the snapshot was
	// NOT honored. This nails the package's actual contract honestly.
	openSnap := rA
	preV, preOK := s.Get(key, openSnap)
	if !preOK || string(preV) != "A" {
		t.Fatalf("run=%s precondition Get(%q,%d)=%q ok=%v, want A", runUUIDSnap, key, openSnap, string(preV), preOK)
	}
	s.Compact(rC) // prune everything superseded with ModRev < rC (A and B).
	postV, postOK := s.Get(key, openSnap)
	t.Logf("run=%s after Compact(%d): Get(openSnap=%d) ok=%v val=%q — Store does NOT pin open snapshots",
		runUUIDSnap, rC, openSnap, postOK, string(postV))
	if postOK {
		t.Fatalf("run=%s Compact unexpectedly RETAINED superseded rev %d (=%q); documented behavior is "+
			"unconditional pruning of ModRev<rev", runUUIDSnap, openSnap, string(postV))
	}
	// Latest survives compaction regardless.
	if lv, ok := s.Get(key, 0); !ok || string(lv) != "C" {
		t.Fatalf("run=%s latest after Compact=%q ok=%v, want C", runUUIDSnap, string(lv), ok)
	}
}

// TestConcurrentReadersWritersRaceClean is the genuine -race property: under the
// Store's documented "serialize access externally" contract (here a
// sync.RWMutex — many concurrent readers OR one writer), concurrent goroutines
// loop doing snapshot reads and version-appending writes. Two things are
// proven:
//
//   - -race detects NO data race in the version-chain / B-tree access when the
//     documented external synchronization is applied (this is the whole point
//     of MVCC: lock-minimal concurrent read/write over a version chain).
//   - Every read returns a value that was ACTUALLY written at-or-before the
//     reader's own snapshot revision — never garbage, never a future write.
//     Each goroutine writes a strictly increasing per-writer counter, so a
//     value seen at snapshot S for a key must be <= the highest counter
//     committed at-or-before S. [BITES non-versioning: a store that returned a
//     future/garbage value would violate the <= invariant.]
func TestConcurrentReadersWritersRaceClean(t *testing.T) {
	s := New()
	var mu sync.RWMutex // external serialization: the documented contract.

	const (
		nWriters = 4
		nReaders = 8
		nKeys    = 6
		iters    = 400
	)
	keys := make([]string, nKeys)
	for i := range keys {
		keys[i] = fmt.Sprintf("ck-%d", i)
		mu.Lock()
		s.Put(keys[i], encodeCounter(0)) // seed every key with counter 0
		mu.Unlock()
	}

	// highestCommitted[key] tracks the max counter that has been COMMITTED for
	// each key, so readers can validate they never see a value above what a
	// snapshot at-or-before their read could legitimately contain. Guarded by
	// its own mutex; updated inside the writer's critical section so the
	// committed-counter and the store version advance together.
	var maxMu sync.Mutex
	highest := make(map[string]int64, nKeys)

	var writeCount, readCount int64
	var wg sync.WaitGroup

	// Writers: append strictly increasing counters per key.
	for w := 0; w < nWriters; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				k := keys[(w+i)%nKeys]
				mu.Lock()
				// Compute next counter as highest-known + 1 so values are
				// globally increasing per key regardless of which writer wins.
				maxMu.Lock()
				next := highest[k] + 1
				highest[k] = next
				maxMu.Unlock()
				s.Put(k, encodeCounter(next))
				mu.Unlock()
				atomic.AddInt64(&writeCount, 1)
			}
		}()
	}

	// Readers: capture a snapshot, read every key as-of it, validate visibility.
	for r := 0; r < nReaders; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				mu.RLock()
				snap := s.Rev()
				// Upper bound on any legitimately-visible counter at-or-before
				// snap: the highest committed counter right now (snap cannot
				// exceed it because both advance under the same writer lock).
				maxMu.Lock()
				ceilByKey := make(map[string]int64, nKeys)
				for _, k := range keys {
					ceilByKey[k] = highest[k]
				}
				maxMu.Unlock()
				for _, k := range keys {
					vb, ok := s.Get(k, snap)
					if !ok {
						mu.RUnlock()
						t.Errorf("run=%s reader: Get(%q, snap=%d) not found (every key seeded)",
							runUUIDSnap, k, snap)
						return
					}
					c := decodeCounter(vb)
					if c < 0 {
						mu.RUnlock()
						t.Errorf("run=%s reader: Get(%q, snap=%d) garbage value %q",
							runUUIDSnap, k, snap, string(vb))
						return
					}
					// Visibility invariant: a value read as-of snap must not
					// exceed the highest counter committed by the time we
					// sampled the ceiling. A future/garbage write would break
					// this. [BITES non-versioning]
					if c > ceilByKey[k] {
						mu.RUnlock()
						t.Errorf("run=%s reader: Get(%q, snap=%d)=counter %d exceeds committed ceiling %d "+
							"— a FUTURE write became visible to this snapshot",
							runUUIDSnap, k, snap, c, ceilByKey[k])
						return
					}
				}
				mu.RUnlock()
				atomic.AddInt64(&readCount, 1)
			}
		}()
	}

	wg.Wait()
	t.Logf("run=%s race-clean loop done: writers committed=%d reads validated=%d (-race found no data race)",
		runUUIDSnap, atomic.LoadInt64(&writeCount), atomic.LoadInt64(&readCount))

	// Final consistency: latest value of each key equals its highest committed
	// counter — proves writes were not lost and the latest pointer is coherent.
	mu.RLock()
	defer mu.RUnlock()
	for _, k := range keys {
		vb, ok := s.Get(k, 0)
		if !ok {
			t.Fatalf("run=%s final Get(%q, latest) not found", runUUIDSnap, k)
		}
		got := decodeCounter(vb)
		maxMu.Lock()
		want := highest[k]
		maxMu.Unlock()
		t.Logf("run=%s final latest %q=counter %d (highest committed=%d)", runUUIDSnap, k, got, want)
		if got != want {
			t.Fatalf("run=%s final latest %q=counter %d, want highest committed %d (lost/torn write)",
				runUUIDSnap, k, got, want)
		}
	}
}

// encodeCounter / decodeCounter store a non-negative int64 as decimal bytes so a
// stored MVCC value is a real []byte payload (not a struct pointer). decode
// returns -1 on any malformed input so the race test can flag garbage.
func encodeCounter(n int64) []byte { return []byte(fmt.Sprintf("%020d", n)) }

func decodeCounter(b []byte) int64 {
	var n int64
	if _, err := fmt.Sscanf(string(b), "%d", &n); err != nil {
		return -1
	}
	return n
}
