package mvcc

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// runUUIDAdv is a distinct forensic anchor for this adversarial suite so its
// evidence lines can be grepped independently from the other test files.
const runUUIDAdv = "mvcc-adv-3e7b2d18-4411-49aa-bc02-helix-adversarial-mvcc"

// ---------------------------------------------------------------------------
// CENTERPIECE 1: TIME-TRAVEL CORRECTNESS
//
// Contract (from mvcc.go): Get(key, atRev) returns the value of the version
// with the greatest ModRev <= atRev. atRev<=0 means latest. A read before a
// key's first version is not-found. A read AT a revision returns that
// revision's exact value; a read BETWEEN two writes returns the latest <=
// that revision (never the newer one — no fall-forward; never an older one
// when a qualifying newer-but-<= version exists).
//
// This probe interleaves writes to MULTIPLE keys so that a single key's
// version revisions are NON-contiguous global revisions (e.g. key "x" gets
// ModRevs 1,3,6 while "y" gets 2,4,5). A correct as-of read must select by
// ModRev<=atRev within the key's own history, NOT by global position. A buggy
// implementation that returned the latest, or that used global revision as a
// dense array index, would be caught here.
// ---------------------------------------------------------------------------

func TestTimeTravelAsOfAcrossInterleavedKeys(t *testing.T) {
	s := New()

	// Interleave writes to x and y. Record (rev -> value) per key.
	type stamp struct {
		rev int64
		val string
	}
	xs := []stamp{}
	ys := []stamp{}

	put := func(k, v string, into *[]stamp) {
		r := s.Put(k, []byte(v))
		*into = append(*into, stamp{r, v})
	}

	put("x", "x@1", &xs) // rev1
	put("y", "y@2", &ys) // rev2
	put("x", "x@3", &xs) // rev3
	put("y", "y@4", &ys) // rev4
	put("y", "y@5", &ys) // rev5
	put("x", "x@6", &xs) // rev6

	t.Logf("run=%s x history=%v y history=%v head=%d", runUUIDAdv, xs, ys, s.Rev())

	// Helper: expected as-of value for a key given a target revision.
	asOf := func(hist []stamp, atRev int64) (string, bool) {
		var bestVal string
		var found bool
		for _, st := range hist {
			if st.rev <= atRev {
				bestVal = st.val
				found = true
			}
		}
		return bestVal, found
	}

	// Probe every global revision 0..7 (0 = latest sentinel; 7 is beyond head)
	// for BOTH keys, comparing the store against the independent oracle.
	for atRev := int64(0); atRev <= 7; atRev++ {
		for _, tc := range []struct {
			key  string
			hist []stamp
		}{{"x", xs}, {"y", ys}} {
			gotBytes, gotFound := s.Get(tc.key, atRev)
			got := string(gotBytes)

			var wantVal string
			var wantFound bool
			if atRev <= 0 {
				// latest sentinel: last stamp.
				wantVal = tc.hist[len(tc.hist)-1].val
				wantFound = true
			} else {
				wantVal, wantFound = asOf(tc.hist, atRev)
			}

			t.Logf("run=%s Get(%q, atRev=%d): got=%q found=%v | oracle want=%q found=%v",
				runUUIDAdv, tc.key, atRev, got, gotFound, wantVal, wantFound)

			if gotFound != wantFound {
				t.Fatalf("run=%s Get(%q,%d) found=%v, oracle found=%v",
					runUUIDAdv, tc.key, atRev, gotFound, wantFound)
			}
			if wantFound && got != wantVal {
				t.Fatalf("run=%s TIME-TRAVEL WRONG VERSION: Get(%q,%d)=%q, want %q "+
					"(as-of read selected the wrong version)",
					runUUIDAdv, tc.key, atRev, got, wantVal)
			}
		}
	}

	// Explicit no-fall-forward: at rev 2, key x has only x@1 (ModRev1<=2);
	// x@3 (ModRev3>2) must be invisible.
	if v, ok := s.Get("x", 2); !ok || string(v) != "x@1" {
		t.Fatalf("run=%s Get(x,2)=%q ok=%v, want x@1 (x@3 must not fall forward)",
			runUUIDAdv, string(v), ok)
	}
	// At rev 5, key x's as-of value is still x@3 (x@6 not yet); not x@6.
	if v, ok := s.Get("x", 5); !ok || string(v) != "x@3" {
		t.Fatalf("run=%s Get(x,5)=%q ok=%v, want x@3 (latest x<=5, not future x@6)",
			runUUIDAdv, string(v), ok)
	}
}

// TestTimeTravelMissingKeyAndEmptyStore covers the not-found edges.
func TestTimeTravelMissingKeyAndEmptyStore(t *testing.T) {
	s := New()

	// Empty store: any Get is not-found and must not panic.
	if v, ok := s.Get("nope", 0); ok {
		t.Fatalf("run=%s empty store Get(nope,0)=%q unexpectedly found", runUUIDAdv, string(v))
	}
	if v, ok := s.Get("nope", 99); ok {
		t.Fatalf("run=%s empty store Get(nope,99)=%q unexpectedly found", runUUIDAdv, string(v))
	}

	s.Put("present", []byte("p1"))
	// A different, never-written key is not-found at any revision.
	if _, ok := s.Get("absent", 1); ok {
		t.Fatalf("run=%s Get(absent,1) unexpectedly found", runUUIDAdv)
	}
	if _, ok := s.Get("absent", 0); ok {
		t.Fatalf("run=%s Get(absent,0) unexpectedly found", runUUIDAdv)
	}
	t.Logf("run=%s missing-key and empty-store reads correctly not-found", runUUIDAdv)
}

// ---------------------------------------------------------------------------
// CENTERPIECE 2: COMPACTION SAFETY
//
// Contract: Compact(rev) drops superseded (non-latest) versions whose ModRev
// < rev. After compaction:
//   - reads at revisions >= rev still return the correct as-of value;
//   - the version that is the as-of answer for `rev` itself (and for any
//     retained later revision) is NOT pruned (no over-pruning / data loss in
//     the retained window);
//   - the latest value always survives, even if its ModRev < rev;
//   - superseded pre-rev versions become not-found (documented pruning).
// ---------------------------------------------------------------------------

func TestCompactionRetainsAsOfWindowAndLatest(t *testing.T) {
	s := New()
	key := "k"
	// Build a 6-version history.
	r := make([]int64, 0, 6)
	for i := 1; i <= 6; i++ {
		r = append(r, s.Put(key, []byte(fmt.Sprintf("v%d", i))))
	}
	// r = [1,2,3,4,5,6]; values v1..v6.
	compactRev := int64(4)
	t.Logf("run=%s pre-compact revs=%v; compacting at rev=%d", runUUIDAdv, r, compactRev)

	s.Compact(compactRev)

	// CENTERPIECE assertion: the as-of answer for compactRev itself must survive.
	// As-of rev 4 is v4 (ModRev4<=4). v4 has ModRev 4, NOT < 4, so it must be
	// retained. If compact pruned one revision too many (e.g. used ModRev <= rev
	// or rev+1), this read would lose v4 -> data loss in the retained window.
	if v, ok := s.Get(key, compactRev); !ok || string(v) != "v4" {
		t.Fatalf("run=%s COMPACTION DATA LOSS: Get(%q, rev=%d)=%q ok=%v, want v4 "+
			"(the as-of answer at the compaction boundary must be retained)",
			runUUIDAdv, key, compactRev, string(v), ok)
	}

	// Reads at revisions > compactRev still correct.
	if v, ok := s.Get(key, 5); !ok || string(v) != "v5" {
		t.Fatalf("run=%s Get(%q,5)=%q ok=%v, want v5 after Compact(%d)",
			runUUIDAdv, key, string(v), ok, compactRev)
	}
	if v, ok := s.Get(key, 6); !ok || string(v) != "v6" {
		t.Fatalf("run=%s Get(%q,6)=%q ok=%v, want v6 after Compact(%d)",
			runUUIDAdv, key, string(v), ok, compactRev)
	}
	// Latest survives.
	if v, ok := s.Get(key, 0); !ok || string(v) != "v6" {
		t.Fatalf("run=%s latest after compaction=%q ok=%v, want v6", runUUIDAdv, string(v), ok)
	}

	// Superseded pre-rev versions (v1,v2,v3 with ModRev 1,2,3 < 4) are pruned:
	// reads at revs 1..3 fall before the earliest retained version (v4@4) and
	// are now not-found.
	for _, oldRev := range []int64{1, 2, 3} {
		if v, ok := s.Get(key, oldRev); ok {
			t.Fatalf("run=%s superseded rev %d still retrievable as %q after Compact(%d)",
				runUUIDAdv, oldRev, string(v), compactRev)
		}
	}
	t.Logf("run=%s compaction retained as-of window [v4@4..v6@6] and latest; pruned v1..v3", runUUIDAdv)
}

// TestCompactionLatestSurvivesEvenWhenBelowRev proves the latest version is
// retained even when its own ModRev < rev (the "compact beyond head" / stale
// key case). A key written once at rev 1, then Compact(100): the only version
// is latest and must survive although 1 < 100.
func TestCompactionLatestSurvivesEvenWhenBelowRev(t *testing.T) {
	s := New()
	s.Put("solo", []byte("only")) // rev 1, the only + latest version
	// Add an unrelated key so the store has multiple histories.
	s.Put("other", []byte("o1"))
	s.Put("other", []byte("o2"))

	s.Compact(100) // far beyond head

	if v, ok := s.Get("solo", 0); !ok || string(v) != "only" {
		t.Fatalf("run=%s latest-only version pruned by Compact(100): Get(solo,0)=%q ok=%v, want only",
			runUUIDAdv, string(v), ok)
	}
	// other's latest survives; its superseded o1 is gone.
	if v, ok := s.Get("other", 0); !ok || string(v) != "o2" {
		t.Fatalf("run=%s other latest=%q ok=%v, want o2", runUUIDAdv, string(v), ok)
	}
	if _, ok := s.Get("other", 1); ok {
		t.Fatalf("run=%s superseded other@1 still retrievable after Compact(100)", runUUIDAdv)
	}
	t.Logf("run=%s latest-below-rev survives compaction; superseded pruned", runUUIDAdv)
}

// TestCompactEdgesZeroAndBeyondHead covers Compact(0) (no-op-ish) and Compact
// beyond head without panicking, and idempotency of repeated compaction.
func TestCompactEdgesZeroAndBeyondHead(t *testing.T) {
	s := New()
	key := "e"
	for i := 1; i <= 3; i++ {
		s.Put(key, []byte(fmt.Sprintf("e%d", i)))
	}

	// Compact(0): nothing has ModRev < 0, so all 3 versions remain retrievable.
	s.Compact(0)
	for i := int64(1); i <= 3; i++ {
		if v, ok := s.Get(key, i); !ok || string(v) != fmt.Sprintf("e%d", i) {
			t.Fatalf("run=%s Compact(0) lost version at rev %d: got=%q ok=%v", runUUIDAdv, i, string(v), ok)
		}
	}

	// Compact beyond head twice: keeps only latest, idempotent, no panic.
	s.Compact(1000)
	s.Compact(1000)
	if v, ok := s.Get(key, 0); !ok || string(v) != "e3" {
		t.Fatalf("run=%s after Compact(1000) latest=%q ok=%v, want e3", runUUIDAdv, string(v), ok)
	}
	if _, ok := s.Get(key, 1); ok {
		t.Fatalf("run=%s superseded e1 retrievable after Compact(1000)", runUUIDAdv)
	}

	// Compact on an empty store must not panic.
	empty := New()
	empty.Compact(5)
	t.Logf("run=%s compact edges (0, beyond-head, idempotent, empty-store) all safe", runUUIDAdv)
}

// ---------------------------------------------------------------------------
// WATCH: EXACTLY-ONCE, IN-ORDER, NO GAPS/DUPES
// ---------------------------------------------------------------------------

func TestWatchExactlyOnceInOrderInterleaved(t *testing.T) {
	s := New()

	// Interleaved multi-key writes. Record the full expected event sequence.
	type ev struct {
		k, v string
	}
	seq := []ev{
		{"a", "a1"}, {"b", "b1"}, {"a", "a2"}, {"c", "c1"},
		{"b", "b2"}, {"a", "a3"}, {"c", "c2"}, {"d", "d1"},
	}
	for _, e := range seq {
		s.Put(e.k, []byte(e.v))
	}
	head := s.Rev()
	t.Logf("run=%s wrote %d events, head=%d", runUUIDAdv, len(seq), head)

	// Watch from every possible fromRev 0..head and assert the suffix matches
	// exactly, in order, once each, with strictly increasing revisions.
	for fromRev := int64(0); fromRev <= head; fromRev++ {
		events := s.Watch(fromRev)

		// Expected: events with Rev > fromRev. Since Put i has Rev i (1-based),
		// the expected suffix is seq[fromRev:].
		start := fromRev
		if start < 0 {
			start = 0
		}
		wantCount := len(seq) - int(start)
		if int(start) > len(seq) {
			wantCount = 0
		}
		if len(events) != wantCount {
			t.Fatalf("run=%s Watch(%d) returned %d events, want %d",
				runUUIDAdv, fromRev, len(events), wantCount)
		}

		prev := fromRev
		for i, e := range events {
			want := seq[int(start)+i]
			if e.Rev <= prev {
				t.Fatalf("run=%s Watch(%d) out-of-order/dup: event[%d].Rev=%d not > prev=%d",
					runUUIDAdv, fromRev, i, e.Rev, prev)
			}
			prev = e.Rev
			if e.Rev != start+int64(i)+1 {
				t.Fatalf("run=%s Watch(%d) gap: event[%d].Rev=%d, want %d",
					runUUIDAdv, fromRev, i, e.Rev, start+int64(i)+1)
			}
			if e.Key != want.k || string(e.Value) != want.v {
				t.Fatalf("run=%s Watch(%d) event[%d]=(%q,%q), want (%q,%q)",
					runUUIDAdv, fromRev, i, e.Key, string(e.Value), want.k, want.v)
			}
		}
	}
	t.Logf("run=%s Watch verified exactly-once/in-order/no-gap for all fromRev in [0,%d]",
		runUUIDAdv, head)

	// Watch from a FUTURE revision (beyond head): empty, no panic.
	future := s.Watch(head + 50)
	if len(future) != 0 {
		t.Fatalf("run=%s Watch(future) returned %d events, want 0", runUUIDAdv, len(future))
	}

	// Watch on an empty store: empty, no panic.
	if got := New().Watch(0); len(got) != 0 {
		t.Fatalf("run=%s Watch on empty store returned %d events, want 0", runUUIDAdv, len(got))
	}
}

// ---------------------------------------------------------------------------
// ORDERED RANGE: sorted, [start,end), no missing/dup
// ---------------------------------------------------------------------------

func TestRangeBoundsAndOrderingAdversarial(t *testing.T) {
	s := New()

	const n = 300
	all := make([]string, 0, n)
	for i := 0; i < n; i++ {
		all = append(all, fmt.Sprintf("k%05d", i))
	}
	// Insert in shuffled order to force B-tree balancing, not insertion order.
	shuffled := make([]string, len(all))
	copy(shuffled, all)
	rand.New(rand.NewSource(7)).Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	if sort.StringsAreSorted(shuffled) {
		t.Fatalf("run=%s shuffled input accidentally sorted; test vacuous", runUUIDAdv)
	}
	for _, k := range shuffled {
		s.Put(k, []byte("x"))
	}

	sorted := make([]string, len(all))
	copy(sorted, all)
	sort.Strings(sorted)

	// Independent oracle: keys in [start,end) sorted.
	oracle := func(start, end string) []string {
		out := []string{}
		for _, k := range sorted {
			if k < start {
				continue
			}
			if end != "" && k >= end {
				continue
			}
			out = append(out, k)
		}
		return out
	}

	check := func(start, end string) {
		got := s.Range(start, end)
		want := oracle(start, end)
		if !sort.StringsAreSorted(got) {
			t.Fatalf("run=%s Range(%q,%q) not sorted: %v", runUUIDAdv, start, end, got)
		}
		// No duplicates.
		for i := 1; i < len(got); i++ {
			if got[i] == got[i-1] {
				t.Fatalf("run=%s Range(%q,%q) duplicate key %q", runUUIDAdv, start, end, got[i])
			}
		}
		if len(got) != len(want) {
			t.Fatalf("run=%s Range(%q,%q) count=%d, want %d", runUUIDAdv, start, end, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("run=%s Range(%q,%q) mismatch at %d: got %q want %q",
					runUUIDAdv, start, end, i, got[i], want[i])
			}
		}
	}

	// Full range.
	check("", "")
	// Bounded sub-ranges, including boundaries that land exactly on keys.
	check("k00100", "k00200") // exactly 100 keys, end-exclusive
	check("k00000", "k00001") // single key
	check("k00299", "")       // tail, unbounded above
	check("k99999", "")       // empty (start past all keys)
	check("k00050", "k00050") // empty (start==end, end-exclusive)
	check("a", "z")           // covers all (lexically around the k-prefix)

	// Explicit end-exclusivity: end == an existing key must NOT include it.
	sub := s.Range("k00100", "k00200")
	for _, k := range sub {
		if k == "k00200" {
			t.Fatalf("run=%s Range end-exclusive violated: contains end key %q", runUUIDAdv, k)
		}
	}
	if sub[0] != "k00100" {
		t.Fatalf("run=%s Range start-inclusive violated: first=%q want k00100", runUUIDAdv, sub[0])
	}
	if sub[len(sub)-1] != "k00199" {
		t.Fatalf("run=%s Range last=%q want k00199", runUUIDAdv, sub[len(sub)-1])
	}
	t.Logf("run=%s Range verified sorted/no-dup/[start,end) against oracle over %d keys", runUUIDAdv, n)
}
