package crdt

import (
	"sort"
	"testing"
)

// ---------------------------------------------------------------------------
// GCounter
// ---------------------------------------------------------------------------

// Concrete multi-replica convergence: two replicas each increment their own
// slot, then exchange and merge. Converged value MUST be the sum 7+5=12.
//
// Mutation that kills this: change GCounter.Merge to add slots instead of
// taking the per-replica max (out.counts[id] += v). Replica A already holds its
// own slot=7; adding B's view back in (or re-merging) double-counts and the
// asserted 12 becomes 19/24. See TestGCounter_Merge_Idempotent for the direct
// idempotence kill.
func TestGCounter_ConcurrentConverges(t *testing.T) {
	a := NewGCounter()
	b := NewGCounter()
	a.Inc("A", 7)
	b.Inc("B", 5)

	ab := a.Merge(b)
	ba := b.Merge(a)

	if got := ab.Value(); got != 12 {
		t.Fatalf("merge(a,b).Value()=%d, want 12", got)
	}
	if got := ba.Value(); got != 12 {
		t.Fatalf("merge(b,a).Value()=%d, want 12", got)
	}
}

// Idempotence on a NON-trivial state: merging a counter with a stale copy of
// itself must not double-count. With per-replica-max this stays 7; a summing
// Merge yields 14 and fails here.
func TestGCounter_Merge_Idempotent(t *testing.T) {
	a := NewGCounter()
	a.Inc("A", 7)
	snapshot := a.Clone()
	merged := a.Merge(snapshot)
	if got := merged.Value(); got != 7 {
		t.Fatalf("idempotence broken: merge(a,a).Value()=%d, want 7", got)
	}
	if !merged.Equal(a) {
		t.Fatalf("merge(a,a) must equal a")
	}
}

// Commutativity + associativity on three non-trivial replicas.
// Mutation: if Merge used per-replica MIN instead of MAX, the AB-vs-BA values
// would still match but TestGCounter_ConcurrentConverges would already fail;
// here a dropped slot (e.g. forgetting to copy out.counts in Clone) breaks the
// associativity equality.
func TestGCounter_Laws(t *testing.T) {
	a, b, c := NewGCounter(), NewGCounter(), NewGCounter()
	a.Inc("A", 3)
	a.Inc("B", 1) // A has also observed one of B's increments
	b.Inc("B", 4)
	c.Inc("C", 9)
	c.Inc("A", 2)

	if !a.Merge(b).Equal(b.Merge(a)) {
		t.Fatalf("commutativity: merge(a,b) != merge(b,a)")
	}
	left := a.Merge(b).Merge(c)
	right := a.Merge(b.Merge(c))
	if !left.Equal(right) {
		t.Fatalf("associativity: merge(merge(a,b),c) != merge(a,merge(b,c))")
	}
	// Concrete value check: A.B=max(1,4)=4, A.A=max(3,2)=3, C=9 -> 16.
	if got := left.Value(); got != 16 {
		t.Fatalf("converged value=%d, want 16", got)
	}
}

// ---------------------------------------------------------------------------
// PNCounter
// ---------------------------------------------------------------------------

// Concrete scenario: A +10, B +3, A -4, B -1 across two replicas. Converged
// value MUST be (10+3) - (4+1) = 8.
//
// Mutation that kills this: make PNCounter.Value add the n half instead of
// subtracting (p.Value + n.Value) -> 18, or merge only the p half -> wrong.
func TestPNCounter_ConcurrentConverges(t *testing.T) {
	a := NewPNCounter()
	b := NewPNCounter()
	a.Inc("A", 10)
	a.Dec("A", 4)
	b.Inc("B", 3)
	b.Dec("B", 1)

	ab := a.Merge(b)
	ba := b.Merge(a)
	if got := ab.Value(); got != 8 {
		t.Fatalf("merge(a,b).Value()=%d, want 8", got)
	}
	if got := ba.Value(); got != 8 {
		t.Fatalf("merge(b,a).Value()=%d, want 8", got)
	}
}

// Laws on non-trivial PN state.
func TestPNCounter_Laws(t *testing.T) {
	a, b, c := NewPNCounter(), NewPNCounter(), NewPNCounter()
	a.Inc("A", 5)
	a.Dec("A", 1)
	b.Inc("B", 2)
	c.Inc("C", 4)
	c.Dec("C", 6)

	if !a.Merge(b).Equal(b.Merge(a)) {
		t.Fatalf("commutativity broken")
	}
	left := a.Merge(b).Merge(c)
	right := a.Merge(b.Merge(c))
	if !left.Equal(right) {
		t.Fatalf("associativity broken")
	}
	if !a.Merge(a).Equal(a) {
		t.Fatalf("idempotence broken")
	}
	// (5+2+4) - (1+6) = 11 - 7 = 4.
	if got := left.Value(); got != 4 {
		t.Fatalf("converged value=%d, want 4", got)
	}
}

// ---------------------------------------------------------------------------
// LWWRegister
// ---------------------------------------------------------------------------

// Concurrent writes: A writes "x"@5, B writes "y"@9. Higher timestamp wins, so
// converged value MUST be "y" on both replicas.
//
// Mutation that kills this: flip dominates to keep the LOWER timestamp
// (r.timestamp < timestamp) -> converges to "x".
func TestLWWRegister_HigherTimestampWins(t *testing.T) {
	a := NewLWWRegister()
	b := NewLWWRegister()
	a.Set("x", 5, "A")
	b.Set("y", 9, "B")

	ab, _ := a.Merge(b).Value()
	ba, _ := b.Merge(a).Value()
	if ab != "y" || ba != "y" {
		t.Fatalf("LWW converge: merge(a,b)=%q merge(b,a)=%q, want both \"y\"", ab, ba)
	}
}

// Equal-timestamp tiebreak by ReplicaID: A="x"@7 vs B="y"@7. ReplicaID "B" > "A"
// so "y" wins deterministically on BOTH orders (this is what makes Merge
// commutative under ties).
//
// Mutation that kills this: drop the tiebreak (return r.timestamp > timestamp
// only) -> merge(a,b) keeps "x" while merge(b,a) keeps "y": non-commutative.
func TestLWWRegister_TiebreakByReplica(t *testing.T) {
	a := NewLWWRegister()
	b := NewLWWRegister()
	a.Set("x", 7, "A")
	b.Set("y", 7, "B")

	ab, _ := a.Merge(b).Value()
	ba, _ := b.Merge(a).Value()
	if ab != "y" {
		t.Fatalf("merge(a,b)=%q, want \"y\" (higher replica id wins tie)", ab)
	}
	if ab != ba {
		t.Fatalf("tiebreak not commutative: merge(a,b)=%q merge(b,a)=%q", ab, ba)
	}
}

// Idempotence: merging a register with itself is a no-op.
func TestLWWRegister_Idempotent(t *testing.T) {
	a := NewLWWRegister()
	a.Set("v", 3, "A")
	if !a.Merge(a).Equal(a) {
		t.Fatalf("idempotence broken")
	}
}

// ---------------------------------------------------------------------------
// ORSet
// ---------------------------------------------------------------------------

// Classic OR-Set semantics: add e, remove e (so it is gone locally), then a
// CONCURRENT add of e on another replica that did not observe the remove. After
// bidirectional merge the element MUST be PRESENT (add-wins), because the
// concurrent add minted a fresh tag the remove never tombstoned.
//
// Mutation that kills this: make Remove tombstone ALL tags for the element
// across both replicas (e.g. Remove ignores observed tags and Merge drops tag
// identity) — then the concurrent add is also removed and Contains returns
// false.
func TestORSet_AddRemoveConcurrentAdd_Present(t *testing.T) {
	a := NewORSet()
	a.Add("e", "A") // A observes one tag for e
	a.Remove("e")   // A tombstones the tag it observed; e gone on A
	if a.Contains("e") {
		t.Fatalf("precondition: e should be absent on A after remove")
	}

	b := NewORSet()
	b.Add("e", "B") // concurrent add, fresh tag {B,1}, unobserved by A's remove

	merged := a.Merge(b)
	if !merged.Contains("e") {
		t.Fatalf("OR-Set add-wins violated: e must be PRESENT after concurrent add")
	}
	// Commutativity of presence.
	if !b.Merge(a).Contains("e") {
		t.Fatalf("merge(b,a) must also contain e")
	}
}

// Sequential add-then-remove on the SAME replica leaves the element absent
// (sanity for the remove path so the test above isn't vacuously add-wins).
func TestORSet_AddThenRemove_Absent(t *testing.T) {
	s := NewORSet()
	s.Add("e", "A")
	s.Remove("e")
	if s.Contains("e") {
		t.Fatalf("e must be absent after add+remove on same replica")
	}
	merged := s.Merge(NewORSet())
	if merged.Contains("e") {
		t.Fatalf("merging with empty set must not resurrect e")
	}
}

// Convergence to an exact element set across two replicas with mixed ops.
func TestORSet_ConcurrentConverges(t *testing.T) {
	a := NewORSet()
	b := NewORSet()
	a.Add("x", "A")
	a.Add("y", "A")
	a.Remove("y") // y removed on A only
	b.Add("y", "B")
	b.Add("z", "B")

	ab := a.Merge(b)
	ba := b.Merge(a)

	want := []string{"x", "y", "z"} // y survives via B's concurrent add
	if got := sortedElems(ab); !equalStrs(got, want) {
		t.Fatalf("merge(a,b) elements=%v, want %v", got, want)
	}
	if got := sortedElems(ba); !equalStrs(got, want) {
		t.Fatalf("merge(b,a) elements=%v, want %v", got, want)
	}
}

// ORSet merge laws on non-trivial state.
func TestORSet_Laws(t *testing.T) {
	a, b, c := NewORSet(), NewORSet(), NewORSet()
	a.Add("p", "A")
	a.Remove("p")
	a.Add("q", "A")
	b.Add("p", "B")
	c.Add("r", "C")
	c.Add("q", "C")

	if !a.Merge(b).Equal(b.Merge(a)) {
		t.Fatalf("commutativity broken")
	}
	left := a.Merge(b).Merge(c)
	right := a.Merge(b.Merge(c))
	if !left.Equal(right) {
		t.Fatalf("associativity broken")
	}
	if !a.Merge(a).Equal(a) {
		t.Fatalf("idempotence broken")
	}
	// p present (B's add wins over A's remove), q present, r present.
	want := []string{"p", "q", "r"}
	if got := sortedElems(left); !equalStrs(got, want) {
		t.Fatalf("converged elements=%v, want %v", got, want)
	}
}

func sortedElems(s *ORSet) []string {
	out := s.Elements()
	sort.Strings(out)
	return out
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
