package crdt

import (
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func vcEqual(t *testing.T, name string, got, want VectorClock) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: len got=%d want=%d; got=%v want=%v", name, len(got), len(want), got, want)
		return
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: replica %q got=%d want=%d", name, id, got[id], w)
		}
	}
}

func assertRelation(t *testing.T, name string, a, b VectorClock, want CausalityRelation) {
	t.Helper()
	got := a.Compare(b)
	if got != want {
		t.Errorf("%s: Compare() got=%s want=%s (a=%v b=%v)", name, got, want, a, b)
	}
}

// ---------------------------------------------------------------------------
// TestCausalityBeforeAfter
// ---------------------------------------------------------------------------

// TestCausalityBeforeAfter verifies the canonical sequential-observation scenario:
// replica "a" sends a message; replica "b" observes it and then records its own
// local event.  The resulting clocks must satisfy A Before B and B After A.
//
// Anti-bluff: if Compare never returns After (always maps to Before/Equal/Concurrent),
// the After assertion fails.
func TestCausalityBeforeAfter(t *testing.T) {
	// a sends event {a:1}.
	a := VectorClock{"a": 1}

	// b receives a's message (merges {a:1}) then records its own event.
	b := a.Merge(VectorClock{})
	b.Increment("b")
	// b is now {a:1, b:1}.

	assertRelation(t, "a Before b", a, b, Before)
	assertRelation(t, "b After a", b, a, After)

	if !a.HappensBefore(b) {
		t.Errorf("HappensBefore: expected a HappensBefore b")
	}
	if b.HappensBefore(a) {
		t.Errorf("HappensBefore: b HappensBefore a must be false")
	}
}

// ---------------------------------------------------------------------------
// TestCausalityConcurrent — THE key anti-bluff bite
// ---------------------------------------------------------------------------

// TestCausalityConcurrent verifies that two clocks produced by independent
// replicas that have never observed each other are Concurrent.
//
// A naive Compare that only emits Before/After/Equal will return Before for
// {a:1}.Compare({b:1}) because it sees a["b"]==0 <= b["b"]==1 and treats the
// absence of "a" in b as 0 <= 1, concluding "before" without noticing that a
// also dominates on its own key (1 > 0). The exclusive "either direction"
// check exposes this: both vcLess and vcGreater must be detected and Concurrent
// returned.
func TestCausalityConcurrent(t *testing.T) {
	a := VectorClock{"a": 1}
	b := VectorClock{"b": 1}

	assertRelation(t, "a Concurrent b", a, b, Concurrent)
	assertRelation(t, "b Concurrent a", b, a, Concurrent)

	// Neither happens-before the other.
	if a.HappensBefore(b) {
		t.Errorf("HappensBefore: a HappensBefore b must be false for concurrent clocks")
	}
	if b.HappensBefore(a) {
		t.Errorf("HappensBefore: b HappensBefore a must be false for concurrent clocks")
	}
}

// ---------------------------------------------------------------------------
// TestCausalityEqual
// ---------------------------------------------------------------------------

// TestCausalityEqual verifies that identical clocks compare as Equal.
func TestCausalityEqual(t *testing.T) {
	a := VectorClock{"a": 2, "b": 3}
	b := VectorClock{"a": 2, "b": 3}

	assertRelation(t, "equal clocks", a, b, Equal)
	assertRelation(t, "equal clocks symmetric", b, a, Equal)
}

// TestEqualEmptyClocks verifies that two empty clocks are Equal.
func TestEqualEmptyClocks(t *testing.T) {
	a := VectorClock{}
	b := VectorClock{}
	assertRelation(t, "empty clocks", a, b, Equal)
}

// TestEqualOneEmptyOneImplicit verifies that an empty clock and a clock with all
// zero-valued slots compare as Equal (missing == 0).
func TestEqualOneEmptyOneImplicit(t *testing.T) {
	a := VectorClock{}
	// b has a slot for "a" but with value 0 — same as absent.
	b := VectorClock{"a": 0}
	// b[a]=0 equals a[a]=0 (implicit), so they should be Equal.
	assertRelation(t, "zero-slot equal", a, b, Equal)
}

// ---------------------------------------------------------------------------
// TestMerge
// ---------------------------------------------------------------------------

// TestMergePerReplicaMax verifies that Merge yields the component-wise maximum.
func TestMergePerReplicaMax(t *testing.T) {
	a := VectorClock{"x": 3, "y": 1}
	b := VectorClock{"x": 1, "y": 5, "z": 2}

	merged := a.Merge(b)

	want := VectorClock{"x": 3, "y": 5, "z": 2}
	vcEqual(t, "merge max", merged, want)
}

// TestMergeCommutative verifies Merge(a,b) == Merge(b,a).
func TestMergeCommutative(t *testing.T) {
	a := VectorClock{"x": 3, "y": 1}
	b := VectorClock{"x": 1, "y": 5, "z": 2}

	ab := a.Merge(b)
	ba := b.Merge(a)

	vcEqual(t, "merge commutative", ab, ba)
}

// TestMergeIdempotent verifies Merge(a,a) == a.
func TestMergeIdempotent(t *testing.T) {
	a := VectorClock{"x": 3, "y": 7}
	aa := a.Merge(a)
	vcEqual(t, "merge idempotent", aa, a)
}

// TestMergeDoesNotMutateInputs verifies that Merge returns a NEW clock and does
// not modify either operand.
func TestMergeDoesNotMutateInputs(t *testing.T) {
	a := VectorClock{"x": 1}
	b := VectorClock{"y": 2}

	aSnap := a.Clone()
	bSnap := b.Clone()

	_ = a.Merge(b)

	vcEqual(t, "a unchanged after merge", a, aSnap)
	vcEqual(t, "b unchanged after merge", b, bSnap)
}

// ---------------------------------------------------------------------------
// TestIncrement
// ---------------------------------------------------------------------------

// TestIncrement verifies that Increment advances only the target slot.
func TestIncrement(t *testing.T) {
	vc := VectorClock{"a": 5, "b": 2}
	vc.Increment("a")
	if vc["a"] != 6 {
		t.Errorf("Increment a: got=%d want=6", vc["a"])
	}
	if vc["b"] != 2 {
		t.Errorf("Increment a should not affect b: got=%d want=2", vc["b"])
	}

	// Increment a new replica.
	vc.Increment("c")
	if vc["c"] != 1 {
		t.Errorf("Increment new replica: got=%d want=1", vc["c"])
	}
}

// ---------------------------------------------------------------------------
// TestClone
// ---------------------------------------------------------------------------

// TestClone verifies that Clone produces an independent deep copy.
func TestClone(t *testing.T) {
	orig := VectorClock{"a": 1, "b": 2}
	cp := orig.Clone()

	// Mutate the copy; original must be unaffected.
	cp.Increment("a")
	cp["c"] = 99

	if orig["a"] != 1 {
		t.Errorf("Clone: mutating copy changed original a: got=%d want=1", orig["a"])
	}
	if _, ok := orig["c"]; ok {
		t.Errorf("Clone: mutating copy added key c to original")
	}
}

// ---------------------------------------------------------------------------
// TestCausalityRelationString
// ---------------------------------------------------------------------------

func TestCausalityRelationString(t *testing.T) {
	cases := []struct {
		r    CausalityRelation
		want string
	}{
		{Before, "Before"},
		{After, "After"},
		{Equal, "Equal"},
		{Concurrent, "Concurrent"},
		{CausalityRelation(99), "Unknown"},
	}
	for _, tc := range cases {
		if got := tc.r.String(); got != tc.want {
			t.Errorf("String(%d): got=%q want=%q", tc.r, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestMultiReplicaOrdering — wider scenario with three replicas
// ---------------------------------------------------------------------------

// TestMultiReplicaOrdering runs a three-replica scenario:
// r1 sends to r2; r2 merges and advances; r3 is independent.
// Verifies that r1 < r2 (Before), r3 concurrent with both r1 and r2.
func TestMultiReplicaOrdering(t *testing.T) {
	r1 := VectorClock{}
	r1.Increment("r1") // {r1:1}

	r2 := r1.Merge(VectorClock{})
	r2.Increment("r2") // {r1:1, r2:1}

	r3 := VectorClock{}
	r3.Increment("r3") // {r3:1}  — independent

	assertRelation(t, "r1 Before r2", r1, r2, Before)
	assertRelation(t, "r2 After r1", r2, r1, After)
	assertRelation(t, "r1 Concurrent r3", r1, r3, Concurrent)
	assertRelation(t, "r2 Concurrent r3", r2, r3, Concurrent)
}
