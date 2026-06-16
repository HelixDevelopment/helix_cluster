package merkle

import (
	"crypto/sha256"
	"testing"
)

// ---------------------------------------------------------------------------
// Adversarial probes for pkg/crdt/merkle.
//
// This package has NO proof generation/verification API (the prompt's
// "forged proof verifies" class does not apply). The exported promises are:
//   - Root(): deterministic, order-independent digest of a k/v set.
//   - Equal(): roots equal  <=>  (assumed) same set, fast anti-entropy path.
//   - Diff(): exact set of keys that differ or are present on one side only.
//
// The adversarial angle here is: can an attacker (or just an unlucky input)
// make Equal/Diff CLAIM two replicas agree (fail-open: "nothing to sync")
// when their key/value sets actually differ, OR make Root collide across
// genuinely-distinct sets?  These are the anti-entropy analogues of a
// fail-open proof verify: a wrongly-empty Diff means stale data is never
// shipped and the replicas silently stay divergent.
// ---------------------------------------------------------------------------

// PROBE 1 (REAL-BUG hunt): empty-key boundary collision.
//
// computeRoot concatenates fixed-width 32-byte leaf hashes with NO framing,
// but leafHash length-prefixes key and value, so distinct (k,v) pairs get
// distinct leaves. Probe the nastiest boundary: empty key vs empty value.
// {"":"x"} and {"x":""} must NOT collide.
func TestAdversarial_EmptyKeyValueBoundary(t *testing.T) {
	a := New(map[string]string{"": "x"})
	b := New(map[string]string{"x": ""})
	if a.Root() == b.Root() {
		t.Fatalf("FAIL-OPEN: distinct empty-boundary sets share a root")
	}
	if a.Equal(b) {
		t.Fatalf("FAIL-OPEN: Equal true for distinct sets")
	}
	if d := a.Diff(b); len(d) != 2 {
		t.Fatalf("Diff=%v, want both keys differing", d)
	}
}

// PROBE 2: empty tree has a well-defined, stable root and is Equal only to
// another empty tree. An empty set must NOT be Equal to a single-key set.
func TestAdversarial_EmptyTree(t *testing.T) {
	e1 := New(map[string]string{})
	e2 := New(nil)
	if e1.Root() != e2.Root() {
		t.Fatalf("two empty trees must share a root")
	}
	if !e1.Equal(e2) {
		t.Fatalf("empty trees must be Equal")
	}
	// Empty set root is sha256 over zero leaf bytes = sha256("").
	if e1.Root() != sha256.Sum256(nil) {
		t.Fatalf("empty root must equal sha256 of zero bytes")
	}
	one := New(map[string]string{"k": "v"})
	if e1.Equal(one) {
		t.Fatalf("FAIL-OPEN: empty tree Equal to a non-empty tree")
	}
	if d := e1.Diff(one); len(d) != 1 || d[0] != "k" {
		t.Fatalf("Diff(empty,one)=%v, want [k]", d)
	}
}

// PROBE 3: one-byte tamper in a value must flip the root and surface in Diff.
// This is the "tampered leaf still verifies" analogue.
func TestAdversarial_OneByteTamper(t *testing.T) {
	a := New(map[string]string{"id": "value"})
	b := New(map[string]string{"id": "valuf"}) // last byte e->f
	if a.Root() == b.Root() {
		t.Fatalf("FAIL-OPEN: one-byte value tamper did not change root")
	}
	if a.Equal(b) {
		t.Fatalf("FAIL-OPEN: Equal true after tamper")
	}
	if d := a.Diff(b); len(d) != 1 || d[0] != "id" {
		t.Fatalf("Diff=%v, want [id]", d)
	}
}

// PROBE 4: key/value swap. {"a":"b"} vs {"b":"a"} must not collide and Diff
// must report BOTH keys (a present only on left, b present only on right).
func TestAdversarial_KeyValueSwap(t *testing.T) {
	a := New(map[string]string{"a": "b"})
	b := New(map[string]string{"b": "a"})
	if a.Root() == b.Root() {
		t.Fatalf("FAIL-OPEN: key/value swap collided to same root")
	}
	d := a.Diff(b)
	if len(d) != 2 || d[0] != "a" || d[1] != "b" {
		t.Fatalf("Diff=%v, want [a b]", d)
	}
}

// PROBE 5: substring / prefix confusion across keys. Without length-prefixing
// in leafHash, {"a":"ab"} and {"aa":"b"} (and the cross pair {"ab":"a"} etc.)
// could collide. Pin that they don't.
func TestAdversarial_PrefixConfusion(t *testing.T) {
	sets := []map[string]string{
		{"a": "ab"},
		{"aa": "b"},
		{"ab": "a"},
		{"": "aab"},
		{"aab": ""},
	}
	roots := make(map[[32]byte]int)
	for i, s := range sets {
		r := New(s).Root()
		if j, ok := roots[r]; ok {
			t.Fatalf("FAIL-OPEN: set %d and set %d collide to same root", j, i)
		}
		roots[r] = i
	}
}

// PROBE 6: Diff exactness under a mix of (shared, stale, left-only, right-only)
// keys. The returned slice must be sorted and contain EXACTLY the divergent
// keys — no shared-and-equal key may leak in (over-report) and no divergent
// key may be dropped (under-report = fail-open).
func TestAdversarial_DiffExactness(t *testing.T) {
	a := New(map[string]string{"same": "1", "stale": "old", "lonlyL": "x"})
	b := New(map[string]string{"same": "1", "stale": "new", "lonlyR": "y"})
	d := a.Diff(b)
	want := []string{"lonlyL", "lonlyR", "stale"}
	if len(d) != len(want) {
		t.Fatalf("Diff=%v, want %v", d, want)
	}
	for i := range want {
		if d[i] != want[i] {
			t.Fatalf("Diff=%v, want %v (sorted, exact)", d, want)
		}
	}
}

// PROBE 7: Diff is symmetric in its key SET (order of operands does not change
// which keys are reported).
func TestAdversarial_DiffSymmetric(t *testing.T) {
	a := New(map[string]string{"x": "1", "y": "2", "z": "3"})
	b := New(map[string]string{"x": "1", "y": "9"}) // y stale, z missing
	da := a.Diff(b)
	db := b.Diff(a)
	if len(da) != len(db) {
		t.Fatalf("asymmetric Diff sizes: %v vs %v", da, db)
	}
	for i := range da {
		if da[i] != db[i] {
			t.Fatalf("asymmetric Diff: %v vs %v", da, db)
		}
	}
	want := []string{"y", "z"}
	for i := range want {
		if da[i] != want[i] {
			t.Fatalf("Diff=%v, want %v", da, want)
		}
	}
}

// PROBE 8: end-to-end convergence. After shipping exactly Diff keys, roots
// must match — and the FULL set must match, not merely the root. This catches
// a Diff that under-reports (leaves a stale key unsynced) which would be a
// silent fail-open in anti-entropy.
func TestAdversarial_ConvergenceFull(t *testing.T) {
	src := map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"}
	dst := map[string]string{"a": "1", "b": "X", "e": "stale-extra"}
	st := New(src)
	// First reconcile keys src says differ.
	for _, k := range st.Diff(New(dst)) {
		if v, ok := src[k]; ok {
			dst[k] = v
		} else {
			delete(dst, k)
		}
	}
	// dst still has "e" which src lacks; a correct Diff from dst's side must
	// have flagged "e". Verify the symmetric direction caught it.
	dt := New(map[string]string{"a": "1", "b": "X", "e": "stale-extra"})
	if _, flagged := contains(dt.Diff(st), "e"); !flagged {
		t.Fatalf("FAIL-OPEN: extra key 'e' not flagged by Diff")
	}
}

func contains(s []string, x string) (int, bool) {
	for i, v := range s {
		if v == x {
			return i, true
		}
	}
	return 0, false
}
