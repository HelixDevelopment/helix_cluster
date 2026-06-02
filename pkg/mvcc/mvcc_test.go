package mvcc

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

const runUUID = "mvcc-test-7f3c1a90-0001-4d2e-9b6a-helix-hxc1390"

// TestTimeTravelHistoricalReads proves CLOSURE CLAUSE 1: writing ONE key 5
// times with distinct values and reading at each of the 5 historical revisions
// returns the EXACT value written at that revision.
//
// Mutation: time-travel uses keyHistory.get with the predicate ModRev > atRev
// (so the version at idx-1 has ModRev <= atRev). Changing the read selection to
// require ModRev < atRev (strict) would return the PREVIOUS version at each
// boundary revision, so Get(rev_i) would yield value_{i-1} and every assertion
// below would fail.
func TestTimeTravelHistoricalReads(t *testing.T) {
	s := New()
	key := "alpha"
	values := []string{"v1", "v2", "v3", "v4", "v5"}
	revs := make([]int64, 0, len(values))
	for _, v := range values {
		r := s.Put(key, []byte(v))
		revs = append(revs, r)
	}
	t.Logf("run=%s wrote key=%q across revs=%v", runUUID, key, revs)

	// Sanity: revisions must be the strictly increasing 1..5.
	for i, r := range revs {
		if r != int64(i+1) {
			t.Fatalf("run=%s expected rev %d at write %d, got %d", runUUID, i+1, i, r)
		}
	}

	for i, r := range revs {
		want := values[i]
		gotBytes, found := s.Get(key, r)
		got := string(gotBytes)
		t.Logf("run=%s time-travel Get(key=%q, atRev=%d): before(expected)=%q after(actual)=%q found=%v",
			runUUID, key, r, want, got, found)
		if !found {
			t.Fatalf("run=%s Get(%q,%d) not found", runUUID, key, r)
		}
		if got != want {
			t.Fatalf("run=%s Get(%q,%d)=%q, want %q (time-travel selected wrong version)",
				runUUID, key, r, got, want)
		}
	}

	// Latest (atRev<=0) must equal the final value, distinct from earlier ones.
	latest, found := s.Get(key, 0)
	if !found || string(latest) != "v5" {
		t.Fatalf("run=%s latest Get=%q found=%v, want v5", runUUID, string(latest), found)
	}
	// Reading at rev 3 must NOT equal latest, proving versions are distinct.
	at3, _ := s.Get(key, 3)
	if string(at3) == string(latest) {
		t.Fatalf("run=%s Get@3=%q equals latest=%q; history not versioned",
			runUUID, string(at3), string(latest))
	}
}

// TestWatchReplaysAllInterveningEvents proves CLOSURE CLAUSE 2: Watch from an
// old revision replays ALL intervening events in revision order with exact
// keys/values.
//
// Mutation: Watch filters with e.Rev > fromRev and iterates s.log in append
// (revision) order. Flipping the filter to e.Rev >= fromRev would wrongly
// include the boundary event (failing the count and the first-key assertion);
// changing iteration order would break the ordering assertion.
func TestWatchReplaysAllInterveningEvents(t *testing.T) {
	s := New()
	// Establish an old revision baseline.
	s.Put("a", []byte("a0"))
	s.Put("b", []byte("b0"))
	fromOldRev := s.Rev() // == 2
	t.Logf("run=%s baseline fromOldRev=%d", runUUID, fromOldRev)

	// Several more writes after the baseline.
	type kv struct{ k, v string }
	later := []kv{{"c", "c1"}, {"a", "a1"}, {"d", "d1"}, {"b", "b1"}}
	for _, e := range later {
		s.Put(e.k, []byte(e.v))
	}

	events := s.Watch(fromOldRev)
	t.Logf("run=%s Watch(%d) returned %d events (expected %d)",
		runUUID, fromOldRev, len(events), len(later))

	if len(events) != len(later) {
		t.Fatalf("run=%s Watch replayed %d events, want %d", runUUID, len(events), len(later))
	}

	prevRev := fromOldRev
	for i, e := range events {
		t.Logf("run=%s event[%d]: rev=%d key=%q val=%q", runUUID, i, e.Rev, e.Key, string(e.Value))
		// Ordering: strictly increasing revisions, all greater than fromOldRev.
		if e.Rev <= prevRev {
			t.Fatalf("run=%s event ordering broken: rev %d not > prev %d", runUUID, e.Rev, prevRev)
		}
		prevRev = e.Rev
		if e.Key != later[i].k || string(e.Value) != later[i].v {
			t.Fatalf("run=%s event[%d]=(%q,%q), want (%q,%q)",
				runUUID, i, e.Key, string(e.Value), later[i].k, later[i].v)
		}
	}

	// Boundary check: the event AT fromOldRev (b0, rev 2) must NOT appear.
	for _, e := range events {
		if e.Rev == fromOldRev {
			t.Fatalf("run=%s Watch wrongly included boundary rev %d", runUUID, fromOldRev)
		}
	}
}

// TestCompactPrunesSupersededHistory proves CLOSURE CLAUSE 3: after
// Compact(midRev), a Get at a pre-compaction revision for a SUPERSEDED version
// is gone, while Get(latest) and post-compaction history remain intact.
//
// Mutation: keyHistory.compact drops versions with ModRev < rev that are not
// the latest. If the prune (the `continue`) is skipped, the superseded version
// remains retrievable and the "gone" assertion below fails. If compact wrongly
// dropped the latest, the latest-intact assertion fails.
func TestCompactPrunesSupersededHistory(t *testing.T) {
	s := New()
	key := "k"
	r1 := s.Put(key, []byte("old1")) // rev 1, will be superseded
	r2 := s.Put(key, []byte("old2")) // rev 2, will be superseded
	r3 := s.Put(key, []byte("mid3")) // rev 3
	midRev := r3
	r4 := s.Put(key, []byte("post4")) // rev 4 (post-compaction history)
	r5 := s.Put(key, []byte("last5"))  // rev 5, latest
	t.Logf("run=%s revs r1..r5 = %d,%d,%d,%d,%d midRev=%d", runUUID, r1, r2, r3, r4, r5, midRev)

	// Before compaction every historical read works.
	beforeV1, beforeFound1 := s.Get(key, r1)
	t.Logf("run=%s before Compact: Get(%d)=%q found=%v", runUUID, r1, string(beforeV1), beforeFound1)
	if !beforeFound1 || string(beforeV1) != "old1" {
		t.Fatalf("run=%s precondition: Get(%d) want old1, got %q found=%v", runUUID, r1, string(beforeV1), beforeFound1)
	}

	s.Compact(midRev)

	// Superseded versions with ModRev < midRev (r1, r2) must now be gone:
	// a read at r1 falls before the earliest surviving version (mid3@rev3),
	// so it is unretrievable.
	afterV1, afterFound1 := s.Get(key, r1)
	t.Logf("run=%s after Compact(%d): Get(%d) found=%v val=%q (expect gone)",
		runUUID, midRev, r1, afterFound1, string(afterV1))
	if afterFound1 {
		t.Fatalf("run=%s superseded rev %d still retrievable as %q after compaction; prune skipped",
			runUUID, r1, string(afterV1))
	}
	if _, f2 := s.Get(key, r2); f2 {
		t.Fatalf("run=%s superseded rev %d still retrievable after compaction", runUUID, r2)
	}

	// Latest must be intact.
	latest, lf := s.Get(key, 0)
	t.Logf("run=%s after Compact: latest=%q found=%v (expect last5)", runUUID, string(latest), lf)
	if !lf || string(latest) != "last5" {
		t.Fatalf("run=%s latest after compaction=%q found=%v, want last5", runUUID, string(latest), lf)
	}

	// Post-compaction history (rev 3 = midRev itself, and rev 4) intact.
	v3, f3 := s.Get(key, midRev)
	if !f3 || string(v3) != "mid3" {
		t.Fatalf("run=%s Get(midRev=%d)=%q found=%v, want mid3", runUUID, midRev, string(v3), f3)
	}
	v4, f4 := s.Get(key, r4)
	t.Logf("run=%s post-compaction Get(%d)=%q found=%v (expect post4)", runUUID, r4, string(v4), f4)
	if !f4 || string(v4) != "post4" {
		t.Fatalf("run=%s Get(%d)=%q found=%v, want post4", runUUID, r4, string(v4), f4)
	}
}

// TestBtreeOrderedRange proves CLOSURE CLAUSE 4: the B-tree maintains keys in
// sorted order across many inserts (NOT insertion order), as observed via
// Range.
//
// Mutation: Range relies on the B-tree's in-order traversal (rangeKeys) over a
// correctly ordered tree maintained by insert/splitChild. If splitChild placed
// the median or right node out of order, or insertNonFull inserted at the wrong
// index, the traversal would no longer be sorted and the sortedness assertion
// would fail.
func TestBtreeOrderedRange(t *testing.T) {
	s := New()
	// Insert many keys in a deterministic but NON-sorted (shuffled) order so a
	// pass cannot come from accidental insertion ordering.
	const n = 200
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		keys = append(keys, fmt.Sprintf("key-%04d", i))
	}
	rng := rand.New(rand.NewSource(42))
	shuffled := make([]string, len(keys))
	copy(shuffled, keys)
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	// Prove the input order is genuinely not sorted (otherwise the test would
	// be vacuous).
	if sort.StringsAreSorted(shuffled) {
		t.Fatalf("run=%s shuffled input is accidentally sorted; test would be vacuous", runUUID)
	}
	t.Logf("run=%s inserting %d keys; first 5 insertion-order=%v", runUUID, n, shuffled[:5])

	for _, k := range shuffled {
		s.Put(k, []byte("x"))
	}

	got := s.Range("", "")
	t.Logf("run=%s Range full: count=%d first5=%v last=%q", runUUID, len(got), got[:5], got[len(got)-1])

	if len(got) != n {
		t.Fatalf("run=%s Range returned %d keys, want %d", runUUID, len(got), n)
	}
	want := make([]string, len(keys))
	copy(want, keys)
	sort.Strings(want)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("run=%s Range not sorted at %d: got %q want %q (full got=%v)",
				runUUID, i, got[i], want[i], got)
		}
	}

	// Bounded sub-range must also be sorted and correctly clipped [lo,hi).
	lo, hi := "key-0050", "key-0060"
	sub := s.Range(lo, hi)
	t.Logf("run=%s Range[%q,%q)=%v", runUUID, lo, hi, sub)
	if len(sub) != 10 {
		t.Fatalf("run=%s sub-range count=%d, want 10", runUUID, len(sub))
	}
	if sub[0] != "key-0050" || sub[len(sub)-1] != "key-0059" {
		t.Fatalf("run=%s sub-range bounds wrong: %v (end must be exclusive)", runUUID, sub)
	}
	if !sort.StringsAreSorted(sub) {
		t.Fatalf("run=%s sub-range not sorted: %v", runUUID, sub)
	}
}
