package merkle

import (
	"testing"
)

// Two replicas holding identical key/value sets MUST have equal roots, even
// when the maps were populated in different orders (root is order-independent).
//
// Mutation that kills this: in computeRoot, drop sort.Strings(keys) and fold in
// map iteration order -> two equal sets can yield different roots and Equal
// returns false here.
func TestEqualSetsEqualRoots(t *testing.T) {
	a := New(map[string]string{"k1": "v1", "k2": "v2", "k3": "v3"})
	b := New(map[string]string{"k3": "v3", "k1": "v1", "k2": "v2"})

	if a.Root() != b.Root() {
		t.Fatalf("equal sets produced different roots")
	}
	if !a.Equal(b) {
		t.Fatalf("Equal must report true for identical sets")
	}
	if d := a.Diff(b); len(d) != 0 {
		t.Fatalf("Diff of equal sets=%v, want empty", d)
	}
}

// One differing value MUST change the root and Diff MUST return exactly that
// one key.
//
// Mutation that kills this: make Diff return all keys (skip the per-key hash
// comparison and append every key) -> returns [k1 k2] and the exact-match
// assertion fails; or make leafHash ignore the value -> roots stay equal and
// the root-difference assertion fails.
func TestOneDifferingKey(t *testing.T) {
	a := New(map[string]string{"k1": "v1", "k2": "v2"})
	b := New(map[string]string{"k1": "v1", "k2": "CHANGED"})

	if a.Root() == b.Root() {
		t.Fatalf("differing value must change the root")
	}
	d := a.Diff(b)
	if len(d) != 1 || d[0] != "k2" {
		t.Fatalf("Diff=%v, want exactly [k2]", d)
	}
}

// A key present on only one side is reported by Diff (descend finds missing
// keys, not just value mismatches).
func TestMissingKeyInDiff(t *testing.T) {
	a := New(map[string]string{"k1": "v1", "k2": "v2"})
	b := New(map[string]string{"k1": "v1"})

	d := a.Diff(b)
	if len(d) != 1 || d[0] != "k2" {
		t.Fatalf("Diff=%v, want exactly [k2]", d)
	}
	// Symmetric: b.Diff(a) must report the same key.
	if d2 := b.Diff(a); len(d2) != 1 || d2[0] != "k2" {
		t.Fatalf("symmetric Diff=%v, want exactly [k2]", d2)
	}
}

// Length-prefixing prevents key/value boundary collisions: ("ab","c") and
// ("a","bc") must hash to different leaves and thus different roots.
//
// Mutation that kills this: remove the length prefixes in leafHash (write only
// key bytes then value bytes) -> both sets concatenate to "abc" and collide.
func TestNoBoundaryCollision(t *testing.T) {
	a := New(map[string]string{"ab": "c"})
	b := New(map[string]string{"a": "bc"})
	if a.Root() == b.Root() {
		t.Fatalf("boundary collision: distinct k/v split produced equal roots")
	}
}

// Anti-entropy end-to-end: after exchanging exactly the Diff keys, the receiver
// converges to the sender's set and roots match.
func TestAntiEntropyReconciles(t *testing.T) {
	src := map[string]string{"a": "1", "b": "2", "c": "3"}
	dst := map[string]string{"a": "1", "b": "X"} // b stale, c missing

	st := New(src)
	dt := New(dst)
	for _, k := range st.Diff(dt) {
		dst[k] = src[k] // ship only the differing keys
	}
	if New(dst).Root() != st.Root() {
		t.Fatalf("after reconciling Diff keys, roots must match")
	}
}
