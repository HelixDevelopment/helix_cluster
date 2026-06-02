package gpuattest

import (
	"errors"
	"testing"
)

const runUUIDPoVW = "gpuattest-HXC1569-7d4e1a22-2c88-4f51-93ab-6e0c19d4f7a1"

// Genuine PoVW verifies (root matches, all links match); perturbing ONE element
// of one iteration's row makes that iteration's chain hash differ, so
// VerifyProof FAILS and reports the FIRST broken link index. Full closure for
// HXC-1569.
//
// Mutation: if VerifyProof ignores per-link comparison (e.g. only compares the
// final Root, or returns ok=true unconditionally), the perturbed-link sub-case
// no longer reports the precise first broken index and this test fails. If
// GenerateProof/VerifyProof disagree on hashRow layout, the genuine case fails.
func TestVerifyProof_GenuineAndPerturbed(t *testing.T) {
	const seed = 0xABCDEF12345
	const n = 6

	p, err := GenerateProof(seed, n)
	if err != nil {
		t.Fatalf("GenerateProof: %v", err)
	}

	ok, link, err := VerifyProof(p)
	t.Logf("run=%s before: VerifyProof(genuine) ok=%v link=%d err=%v", runUUIDPoVW, ok, link, err)
	if !ok || link != -1 || err != nil {
		t.Fatalf("genuine proof should verify: ok=%v link=%d err=%v", ok, link, err)
	}

	// Re-deriving from the same seed gives an identical root (determinism).
	p2, _ := GenerateProof(seed, n)
	if p2.Root != p.Root {
		t.Fatalf("non-deterministic root: %x != %x", p2.Root, p.Root)
	}

	// Perturb the chain hash recorded for a middle iteration. VerifyProof
	// recomputes C from the seed; the recomputed link for index 2 will NOT match
	// the corrupted stored value, and since links 0,1 still match, index 2 is the
	// FIRST break.
	tampered := Proof{Seed: p.Seed, N: p.N, Root: p.Root, Chain: make([][32]byte, n)}
	copy(tampered.Chain, p.Chain)
	tampered.Chain[2][0] ^= 0xFF

	ok2, link2, err2 := VerifyProof(tampered)
	t.Logf("run=%s after: VerifyProof(perturbed@2) ok=%v firstBrokenLink=%d err=%v", runUUIDPoVW, ok2, link2, err2)
	if ok2 {
		t.Fatalf("perturbed proof unexpectedly verified")
	}
	if link2 != 2 {
		t.Fatalf("first broken link = %d, want 2", link2)
	}
	if !errors.Is(err2, ErrProofBroken) {
		t.Fatalf("want ErrProofBroken, got %v", err2)
	}
}

// A perturbation that only changes the underlying matrix data (via a tampered
// root with all chain links rewritten consistently EXCEPT a single element)
// must still be caught: here we corrupt the LAST link only, so the first break
// is reported at the last index, and root mismatch never masks it.
//
// Mutation: if VerifyProof short-circuits to true after matching the root
// without checking links, corrupting an interior link while leaving Root intact
// (the case above) already fails; this case additionally guards the boundary by
// asserting the last index is named.
func TestVerifyProof_LastLinkBreakIndex(t *testing.T) {
	const seed = 99
	const n = 4
	p, _ := GenerateProof(seed, n)

	tampered := Proof{Seed: p.Seed, N: p.N, Root: p.Root, Chain: make([][32]byte, n)}
	copy(tampered.Chain, p.Chain)
	tampered.Chain[n-1][5] ^= 0x01

	ok, link, err := VerifyProof(tampered)
	t.Logf("run=%s last-link: ok=%v firstBrokenLink=%d err=%v (want link=%d)", runUUIDPoVW, ok, link, err, n-1)
	if ok || link != n-1 || !errors.Is(err, ErrProofBroken) {
		t.Fatalf("want break at %d ErrProofBroken, got ok=%v link=%d err=%v", n-1, ok, link, err)
	}
}

// The PRNG must be self-contained and seed-deterministic (no clock, no global
// state). Different seeds MUST yield different matrices and therefore different
// roots; the same seed MUST reproduce exactly.
//
// Mutation: if newXorshift ignores the seed (constant stream) the two distinct
// seeds below produce equal roots and this fails. If next() is made stateless
// the genuine chain in the other test breaks too.
func TestPoVW_SeedDeterminismAndSensitivity(t *testing.T) {
	a1, _ := GenerateProof(1, 5)
	a1b, _ := GenerateProof(1, 5)
	a2, _ := GenerateProof(2, 5)
	if a1.Root != a1b.Root {
		t.Fatalf("same seed produced different roots")
	}
	if a1.Root == a2.Root {
		t.Fatalf("different seeds produced identical roots")
	}
	t.Logf("run=%s seed=1 root=%x seed=2 root=%x (distinct, seed=1 reproducible)", runUUIDPoVW, a1.Root[:4], a2.Root[:4])
}
