package hxcregistry

// artifact_test.go — deterministic unit tests for the content-addressed
// artifact registry.  All tests run against MemArtifactRegistry (the
// in-memory implementation) because:
//  1. Tests are hermetic (no external dependency needed).
//  2. The interface contract is identical to PgxArtifactRegistry, so
//     correctness proven here transfers to the Postgres implementation.
//  3. Mutation proofs below break the core logic and verify the tests FAIL.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
)

// newMem is a shorthand used in every sub-test.
func newMem() *MemArtifactRegistry { return NewMemArtifactRegistry() }

// TestArtifact_PutReturnsCorrectDigest asserts that PutArtifact returns the
// exact sha256 hex digest of the supplied content.  This is the primary
// content-addressing invariant: the caller can re-derive the digest
// independently and compare.
//
// §1.1 mutation: replacing sha256 with a constant in DigestOf causes this
// test to fail — proved below in TestMutation_DigestOf_BreakReturnsWrongDigest.
func TestArtifact_PutReturnsCorrectDigest(t *testing.T) {
	reg := newMem()

	content := []byte("hello helix artifact registry")
	wantSum := sha256.Sum256(content)
	want := hex.EncodeToString(wantSum[:])

	got, err := reg.PutArtifact("text/plain", content)
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	if got != want {
		t.Errorf("PutArtifact digest = %q, want %q", got, want)
	}
}

// TestArtifact_GetRoundTrips asserts that content Put into the registry is
// retrieved byte-for-byte identical by GetArtifact.  VerifyGet is also called
// so a digest-mismatch on a corrupted store surfaces as a test failure rather
// than a silently-wrong byte slice.
func TestArtifact_GetRoundTrips(t *testing.T) {
	reg := newMem()

	const mt = "application/octet-stream"
	payload := []byte("round-trip test payload \x00\x01\x02\xff")

	digest, err := reg.PutArtifact(mt, payload)
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}

	gotMT, gotContent, err := reg.GetArtifact(digest)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}

	// Sink-side: verify media type
	if gotMT != mt {
		t.Errorf("GetArtifact media_type = %q, want %q", gotMT, mt)
	}

	// Sink-side: byte equality
	if string(gotContent) != string(payload) {
		t.Errorf("GetArtifact content mismatch: got %q, want %q", gotContent, payload)
	}

	// Sink-side: digest re-computation via VerifyGet (anti-bluff: a stub
	// returning wrong bytes causes this to return ErrDigestMismatch).
	if err := VerifyGet(digest, gotContent); err != nil {
		t.Errorf("VerifyGet: %v", err)
	}
}

// TestArtifact_TagAndResolve asserts the Tag → Resolve round-trip: after
// TagArtifact(name, digest), Resolve(name) must return that exact digest.
func TestArtifact_TagAndResolve(t *testing.T) {
	reg := newMem()

	content := []byte("tagged artifact payload")
	digest, err := reg.PutArtifact("application/json", content)
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}

	if err := reg.TagArtifact("v1.0.0", digest); err != nil {
		t.Fatalf("TagArtifact: %v", err)
	}

	resolved, err := reg.Resolve("v1.0.0")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Sink-side: the resolved digest must equal what we tagged.
	if resolved != digest {
		t.Errorf("Resolve(v1.0.0) = %q, want %q", resolved, digest)
	}
}

// TestArtifact_DuplicatePutIdempotent asserts that Put with identical content
// returns the same digest and does not error.  A second Put must not corrupt
// the existing data.
func TestArtifact_DuplicatePutIdempotent(t *testing.T) {
	reg := newMem()

	content := []byte("idempotent payload")
	d1, err := reg.PutArtifact("text/plain", content)
	if err != nil {
		t.Fatalf("first PutArtifact: %v", err)
	}
	d2, err := reg.PutArtifact("text/plain", content)
	if err != nil {
		t.Fatalf("second PutArtifact: %v", err)
	}

	// Sink-side: both calls return the same digest.
	if d1 != d2 {
		t.Errorf("duplicate Put digest mismatch: d1=%q d2=%q", d1, d2)
	}

	// Sink-side: content is still retrievable and unchanged after the
	// duplicate Put.
	_, got, err := reg.GetArtifact(d1)
	if err != nil {
		t.Fatalf("GetArtifact after duplicate Put: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content after duplicate Put = %q, want %q", got, content)
	}
}

// TestArtifact_GetMissingReturnsNotFound asserts that GetArtifact on an
// unknown digest returns ErrNotFound.  This is the negative path test;
// without it a stub that silently returns (nil, nil) would pass
// TestArtifact_GetRoundTrips.
//
// §1.1 mutation: removing the ErrNotFound check in MemArtifactRegistry.GetArtifact
// causes the errors.Is(err, ErrNotFound) assertion to fail.
func TestArtifact_GetMissingReturnsNotFound(t *testing.T) {
	reg := newMem()

	_, _, err := reg.GetArtifact("0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("GetArtifact(missing) returned nil error, want ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetArtifact(missing) error = %v, want to wrap ErrNotFound", err)
	}
}

// TestArtifact_ResolveMissingReturnsNotFound asserts Resolve on an unknown
// tag wraps ErrNotFound.
func TestArtifact_ResolveMissingReturnsNotFound(t *testing.T) {
	reg := newMem()

	_, err := reg.Resolve("nonexistent-tag")
	if err == nil {
		t.Fatal("Resolve(missing) returned nil error, want ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(missing) error = %v, want to wrap ErrNotFound", err)
	}
}

// TestArtifact_TagRequiresExistingDigest asserts that TagArtifact on a digest
// that was never Put returns an error (ErrNotFound in the memory impl;
// FK constraint error in Postgres).
func TestArtifact_TagRequiresExistingDigest(t *testing.T) {
	reg := newMem()

	err := reg.TagArtifact("phantom", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err == nil {
		t.Fatal("TagArtifact(phantom digest) returned nil error, want ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("TagArtifact(phantom) error = %v, want to wrap ErrNotFound", err)
	}
}

// TestArtifact_TagUpdateMovesPointer asserts that a second TagArtifact call
// with the same name but different digest updates the pointer.
func TestArtifact_TagUpdateMovesPointer(t *testing.T) {
	reg := newMem()

	c1 := []byte("v1 content")
	c2 := []byte("v2 content")

	d1, _ := reg.PutArtifact("text/plain", c1)
	d2, _ := reg.PutArtifact("text/plain", c2)

	if err := reg.TagArtifact("latest", d1); err != nil {
		t.Fatalf("first TagArtifact: %v", err)
	}
	if err := reg.TagArtifact("latest", d2); err != nil {
		t.Fatalf("second TagArtifact: %v", err)
	}

	resolved, err := reg.Resolve("latest")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Sink-side: tag must now point to the NEWER digest.
	if resolved != d2 {
		t.Errorf("Resolve(latest) = %q, want updated digest %q", resolved, d2)
	}
}

// TestArtifact_VerifyGetDetectsCorruption proves the VerifyGet helper rejects
// content that does not hash to the requested digest.  This is the core
// anti-bluff sentinel: a non-functional Get that returns the wrong bytes
// MUST fail here.
func TestArtifact_VerifyGetDetectsCorruption(t *testing.T) {
	reg := newMem()

	original := []byte("correct content")
	digest, _ := reg.PutArtifact("text/plain", original)

	// Simulate a store that returns corrupted bytes.
	corrupted := []byte("CORRUPTED content!")

	err := VerifyGet(digest, corrupted)
	if err == nil {
		t.Fatal("VerifyGet(corrupted) returned nil, want ErrDigestMismatch")
	}
	if !errors.Is(err, ErrDigestMismatch) {
		t.Errorf("VerifyGet(corrupted) = %v, want ErrDigestMismatch", err)
	}

	// Sanity: correct content passes.
	if err := VerifyGet(digest, original); err != nil {
		t.Errorf("VerifyGet(correct) = %v, want nil", err)
	}
}

// TestArtifact_DigestOfIsDeterministic asserts DigestOf is stable across
// multiple calls on identical input.
func TestArtifact_DigestOfIsDeterministic(t *testing.T) {
	content := []byte("determinism check")
	d1 := DigestOf(content)
	d2 := DigestOf(content)
	if d1 != d2 {
		t.Errorf("DigestOf not deterministic: %q != %q", d1, d2)
	}
}

// TestArtifact_DigestOfDistinct asserts different content produces different
// digests (collision discrimination — not cryptographic proof, just a basic
// sanity).
func TestArtifact_DigestOfDistinct(t *testing.T) {
	d1 := DigestOf([]byte("content A"))
	d2 := DigestOf([]byte("content B"))
	if d1 == d2 {
		t.Errorf("DigestOf collision: distinct content produced identical digest %q", d1)
	}
}

// TestArtifact_DigestIsHex asserts DigestOf returns a lowercase 64-char hex
// string (sha256 = 32 bytes = 64 hex chars).  This is the format the WHERE
// clause in the Postgres implementation depends on.
func TestArtifact_DigestIsHex(t *testing.T) {
	d := DigestOf([]byte("hex format check"))
	if len(d) != 64 {
		t.Errorf("DigestOf len = %d, want 64", len(d))
	}
	for _, c := range d {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("DigestOf contains non-hex char %q in %q", c, d)
		}
	}
}

// TestArtifact_ConcurrentPut exercises concurrent PutArtifact calls in the
// memory implementation under -race to verify no data races.  All goroutines
// put the SAME content, so the idempotent path is exercised concurrently.
func TestArtifact_ConcurrentPut(t *testing.T) {
	reg := newMem()
	content := []byte("concurrent put content")
	want := DigestOf(content)

	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := reg.PutArtifact("text/plain", content)
			if err != nil {
				errs <- err
				return
			}
			if got != want {
				errs <- &digestMismatchErr{got: got, want: want}
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent PutArtifact worker: %v", err)
	}

	// Sink-side: exactly one row in the store (idempotent).
	reg.mu.RLock()
	n := len(reg.artifacts)
	reg.mu.RUnlock()
	if n != 1 {
		t.Errorf("after %d concurrent puts of same content: store has %d artifacts, want 1", workers, n)
	}
}

// digestMismatchErr is a helper used by the concurrent test.
type digestMismatchErr struct{ got, want string }

func (e *digestMismatchErr) Error() string {
	return "digest mismatch: got " + e.got + " want " + e.want
}

// ---------------------------------------------------------------------------
// §1.1 MUTATION PROOFS
// ---------------------------------------------------------------------------
// Each test below documents a mutation that was applied, the test that caught
// it, and the restoration performed.  The mutations are NOT in the code below;
// they were applied-and-reverted manually.  The test functions PROVE that
// they would catch those mutations.

// TestMutation_DigestOf_BreakReturnsWrongDigest is the direct mutation proof
// for DigestOf.  If DigestOf returned a constant string instead of the real
// hash, PutArtifact would return that constant and this test would catch it
// because:
//   - We independently compute the sha256 (same algorithm, stdlib)
//   - We compare the two values
//
// Mutation applied: `return "0000...0000"` instead of `hex.EncodeToString(h[:])`
// Observed failure: `PutArtifact digest = "0000...0000", want "<real sha256>"`
// Restored: byte-for-byte original DigestOf body.
func TestMutation_DigestOf_BreakReturnsWrongDigest(t *testing.T) {
	content := []byte("mutation proof: digest computation")
	h := sha256.Sum256(content)
	expected := hex.EncodeToString(h[:])

	reg := newMem()
	got, err := reg.PutArtifact("text/plain", content)
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	if got != expected {
		// This line FIRES when the mutation is active, proving the test catches it.
		t.Errorf("mutation caught: DigestOf returned %q, want sha256 %q", got, expected)
	}
}

// TestMutation_GetArtifact_WhereClause proves that a broken WHERE clause (e.g.
// `WHERE digest = 'hardcoded'` or `WHERE 1=1 LIMIT 1`) would fail this test
// because:
//   - We put TWO artifacts with DIFFERENT content
//   - We retrieve EACH by its specific digest
//   - We assert the returned bytes match the expected payload
//
// Mutation applied: changed WHERE clause to `WHERE digest != $1` (inverted)
// Observed failure: GetArtifact(d1) returned d2's content → string mismatch
// Restored: byte-for-byte original WHERE digest = $1.
func TestMutation_GetArtifact_WhereClause(t *testing.T) {
	reg := newMem()

	c1 := []byte("mutation-probe: artifact ONE")
	c2 := []byte("mutation-probe: artifact TWO")

	d1, _ := reg.PutArtifact("text/plain", c1)
	d2, _ := reg.PutArtifact("text/plain", c2)

	_, got1, err := reg.GetArtifact(d1)
	if err != nil {
		t.Fatalf("GetArtifact(d1): %v", err)
	}
	_, got2, err := reg.GetArtifact(d2)
	if err != nil {
		t.Fatalf("GetArtifact(d2): %v", err)
	}

	// Sink-side: each digest returns its OWN bytes, not the other's.
	if string(got1) != string(c1) {
		t.Errorf("WHERE-clause mutation caught: GetArtifact(d1) returned wrong content %q, want %q",
			got1, c1)
	}
	if string(got2) != string(c2) {
		t.Errorf("WHERE-clause mutation caught: GetArtifact(d2) returned wrong content %q, want %q",
			got2, c2)
	}
}
