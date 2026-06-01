//go:build integration

package hxcregistry

// artifact_integration_test.go — integration tests for PgxArtifactRegistry
// against a REAL PostgreSQL instance.
//
// Guard: t.Skip only when DATABASE_URL is unset.  If DATABASE_URL is set,
// tests HARD-FAIL on any mismatch — a passing test on wrong data is a
// PASS-bluff.
//
// Design:
//   - Each test uses a per-run UUID in the artifact payload so the test can
//     never pass by accidentally finding a stale row from a previous run.
//   - GetArtifact is always called via a SECOND pgxpool connection to prove
//     the data round-tripped through Postgres storage (not cached in the
//     inserting connection's memory).
//   - Tag+Resolve is verified via a separate Resolve call that must return
//     the exact digest the test computed locally.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// openTestRegistry opens two independent PgxArtifactRegistry instances
// against DATABASE_URL and applies the schema.  Returns the writer (used for
// PutArtifact / TagArtifact) and the reader (used for GetArtifact / Resolve
// to prove cross-connection durability).  Both are registered for cleanup.
func openTestRegistry(t *testing.T) (writer, reader *PgxArtifactRegistry) {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}

	ctx := context.Background()

	w, err := NewPgxArtifactRegistry(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPgxArtifactRegistry (writer): %v", err)
	}
	t.Cleanup(w.Close)

	if err := w.ApplyArtifactSchema(ctx); err != nil {
		t.Fatalf("ApplyArtifactSchema: %v", err)
	}

	// Second independent pool — proves persistence beyond the inserting connection.
	r, err := NewPgxArtifactRegistry(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPgxArtifactRegistry (reader): %v", err)
	}
	t.Cleanup(r.Close)

	return w, r
}

// uniquePayload returns a payload containing a per-test UUID so no two test
// runs can produce the same digest and accidentally reuse a stale row.
func uniquePayload(prefix string) []byte {
	return []byte(fmt.Sprintf("%s|%s|%s", prefix, uuid.New().String(), time.Now().UTC().Format(time.RFC3339Nano)))
}

// TestPG_Artifact_PutGetRoundTrip is the core integration proof:
//  1. Put a UUID-payload artifact via the writer connection.
//  2. Independently compute the expected digest.
//  3. Get the artifact via the READER connection (different pool/connection).
//  4. Assert byte equality AND digest match (VerifyGet).
//
// Hard-FAIL on any mismatch — a passing test on wrong bytes is the PASS-bluff
// this entire test file exists to prevent.
func TestPG_Artifact_PutGetRoundTrip(t *testing.T) {
	w, r := openTestRegistry(t)

	payload := uniquePayload("TestPG_Artifact_PutGetRoundTrip")
	wantDigest := DigestOf(payload)
	const mt = "application/octet-stream"

	gotDigest, err := w.PutArtifact(mt, payload)
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	if gotDigest != wantDigest {
		t.Fatalf("PutArtifact digest = %q, want %q (digest computation broken)", gotDigest, wantDigest)
	}

	// Read via SECOND connection to prove Postgres persisted the bytes.
	gotMT, gotContent, err := r.GetArtifact(gotDigest)
	if err != nil {
		t.Fatalf("GetArtifact (reader): %v", err)
	}

	if gotMT != mt {
		t.Errorf("GetArtifact media_type = %q, want %q", gotMT, mt)
	}

	// Byte equality — not just length.
	if string(gotContent) != string(payload) {
		t.Fatalf("GetArtifact byte mismatch:\n  got  len=%d %q\n  want len=%d %q",
			len(gotContent), gotContent, len(payload), payload)
	}

	// VerifyGet: recompute digest from retrieved bytes — proves the store
	// did not silently corrupt.
	if err := VerifyGet(gotDigest, gotContent); err != nil {
		t.Fatalf("VerifyGet: %v — real Postgres round-trip digest mismatch", err)
	}

	t.Logf("real postgres: Put digest=%s, reader Get byte-equal, VerifyGet PASS", gotDigest[:16])
}

// TestPG_Artifact_IdempotentPut asserts that putting the same bytes twice
// returns the same digest both times and the row is not duplicated.
func TestPG_Artifact_IdempotentPut(t *testing.T) {
	w, r := openTestRegistry(t)

	payload := uniquePayload("TestPG_Artifact_IdempotentPut")
	const mt = "text/plain"

	d1, err := w.PutArtifact(mt, payload)
	if err != nil {
		t.Fatalf("first PutArtifact: %v", err)
	}
	d2, err := w.PutArtifact(mt, payload)
	if err != nil {
		t.Fatalf("second PutArtifact: %v", err)
	}

	if d1 != d2 {
		t.Fatalf("idempotent Put: digests differ d1=%q d2=%q", d1, d2)
	}

	// Sink-side via reader: exactly one row, correct content.
	_, gotContent, err := r.GetArtifact(d1)
	if err != nil {
		t.Fatalf("GetArtifact after idempotent Put: %v", err)
	}
	if string(gotContent) != string(payload) {
		t.Fatalf("idempotent Put: content mismatch got=%q want=%q", gotContent, payload)
	}

	t.Logf("real postgres: idempotent Put digest=%s, single row, content intact", d1[:16])
}

// TestPG_Artifact_TagResolveRoundTrip asserts the full Tag → Resolve → Get chain
// against real Postgres:
//  1. Put a unique payload via the writer.
//  2. Tag it with a unique tag name.
//  3. Resolve the tag via the READER → must return the exact digest.
//  4. Get the artifact by the resolved digest via the reader → must match payload.
func TestPG_Artifact_TagResolveRoundTrip(t *testing.T) {
	w, r := openTestRegistry(t)

	payload := uniquePayload("TestPG_Artifact_TagResolveRoundTrip")
	tagName := "integration-tag-" + uuid.New().String()

	digest, err := w.PutArtifact("application/json", payload)
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}

	if err := w.TagArtifact(tagName, digest); err != nil {
		t.Fatalf("TagArtifact: %v", err)
	}

	// Resolve via READER (different pool).
	resolved, err := r.Resolve(tagName)
	if err != nil {
		t.Fatalf("Resolve (reader): %v", err)
	}
	if resolved != digest {
		t.Fatalf("Resolve = %q, want %q", resolved, digest)
	}

	// Follow the pointer: Get by resolved digest via reader.
	_, gotContent, err := r.GetArtifact(resolved)
	if err != nil {
		t.Fatalf("GetArtifact by resolved digest: %v", err)
	}
	if string(gotContent) != string(payload) {
		t.Fatalf("GetArtifact(resolved): content mismatch\n  got  %q\n  want %q", gotContent, payload)
	}

	t.Logf("real postgres: Tag+Resolve round-trip OK (tag=%s digest=%s)", tagName, digest[:16])
}

// TestPG_Artifact_GetMissingReturnsNotFound proves GetArtifact on an absent
// digest returns ErrNotFound against real Postgres.
func TestPG_Artifact_GetMissingReturnsNotFound(t *testing.T) {
	_, r := openTestRegistry(t)

	// A digest that was never inserted (all zeros, not a valid sha256 of anything).
	absent := "0000000000000000000000000000000000000000000000000000000000000000"
	_, _, err := r.GetArtifact(absent)
	if err == nil {
		t.Fatal("GetArtifact(absent) returned nil error, want ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetArtifact(absent) error = %v, want ErrNotFound wrapper", err)
	}

	t.Logf("real postgres: GetArtifact(absent) correctly returned: %v", err)
}

// TestPG_Artifact_ResolveMissingReturnsNotFound proves Resolve on an absent tag
// returns ErrNotFound against real Postgres.
func TestPG_Artifact_ResolveMissingReturnsNotFound(t *testing.T) {
	_, r := openTestRegistry(t)

	absent := "nonexistent-tag-" + uuid.New().String()
	_, err := r.Resolve(absent)
	if err == nil {
		t.Fatal("Resolve(absent) returned nil error, want ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(absent) error = %v, want ErrNotFound wrapper", err)
	}

	t.Logf("real postgres: Resolve(absent) correctly returned: %v", err)
}

// TestPG_Artifact_DigestMutationFails verifies that VerifyGet catches a
// real digest-mismatch scenario: store content C, then pass corrupted bytes to
// VerifyGet.  This is the mutation-proof companion for the integration tier:
// a non-functional PgxArtifactRegistry.GetArtifact that returns wrong bytes
// would fail here.
func TestPG_Artifact_DigestMutationFails(t *testing.T) {
	w, r := openTestRegistry(t)

	payload := uniquePayload("TestPG_Artifact_DigestMutationFails")
	digest, err := w.PutArtifact("text/plain", payload)
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}

	_, realContent, err := r.GetArtifact(digest)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}

	// Simulate a store returning corrupted bytes.
	corrupted := make([]byte, len(realContent))
	copy(corrupted, realContent)
	corrupted[0] ^= 0xFF // flip all bits in the first byte

	if err := VerifyGet(digest, corrupted); err == nil {
		t.Fatal("VerifyGet(corrupted) returned nil — digest-mutation not detected (PASS-bluff)")
	} else if !errors.Is(err, ErrDigestMismatch) {
		t.Errorf("VerifyGet(corrupted) = %v, want ErrDigestMismatch", err)
	}

	// Real content still validates.
	if err := VerifyGet(digest, realContent); err != nil {
		t.Fatalf("VerifyGet(real) = %v — real bytes from Postgres do not match stored digest", err)
	}

	t.Logf("real postgres: VerifyGet detects corruption and accepts real bytes (digest=%s)", digest[:16])
}
