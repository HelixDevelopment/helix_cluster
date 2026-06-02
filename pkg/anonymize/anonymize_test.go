package anonymize

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// newRunUUID derives a deterministic run identifier from the test name and a
// salt, with no dependence on time.Now() or randomness, so logs are stable and
// CLAUDE-1 compliant (no wall clock in logic under test).
func newRunUUID(t *testing.T, salt string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + "|" + salt))
	h := hex.EncodeToString(sum[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// independentDigest recomputes the expected per-block HMAC-SHA256 hex using an
// independent length-framed construction. Expected values are DERIVED from the
// inputs here, never copied from production output, so a mutation in the
// production hashing path causes a mismatch.
func independentDigest(domain string, tenantKey []byte, block string) string {
	framed := func(s string) []byte {
		var l [8]byte
		n := uint64(len(s))
		for i := 0; i < 8; i++ {
			l[7-i] = byte(n >> (8 * uint(i)))
		}
		return append(l[:], []byte(s)...)
	}
	mac := hmac.New(sha256.New, tenantKey)
	mac.Write(framed(domain))
	mac.Write(framed(block))
	return hex.EncodeToString(mac.Sum(nil))
}

// rawSubstrings returns the distinct non-whitespace block strings of text so a
// test can assert none of them leak into the anonymized output.
func rawSubstrings(text string) []string {
	return SplitBlocks(text)
}

// Clause 1: SAME prompt twice under SAME tenant key -> IDENTICAL block hashes,
// AND raw text never appears in the output.
//
// Mutation: if Anonymize echoed raw text (e.g. returned the block instead of
// its digest), the raw-absent assertion below would fail. If the digest were
// nondeterministic, the identical-hash assertion would fail.
func TestAnonymize_SameTenantDeterministicAndRawAbsent(t *testing.T) {
	runUUID := newRunUUID(t, "clause1")
	a := New("prompt-v1")
	tenantKey := []byte("tenant-alpha-secret-key")
	prompt := "patient John Doe ssn 123-45-6789 visited clinic"

	t.Logf("run=%s clause=1 before: raw_prompt=%q blocks=%v", runUUID, prompt, SplitBlocks(prompt))

	first := a.Anonymize(tenantKey, prompt)
	second := a.Anonymize(tenantKey, prompt)

	t.Logf("run=%s clause=1 after: first=%v second=%v", runUUID, first, second)

	if len(first) != len(second) {
		t.Fatalf("run=%s block count differs: %d vs %d", runUUID, len(first), len(second))
	}
	if len(first) != len(SplitBlocks(prompt)) {
		t.Fatalf("run=%s output blocks=%d want=%d", runUUID, len(first), len(SplitBlocks(prompt)))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("run=%s block %d not deterministic: %q vs %q", runUUID, i, first[i], second[i])
		}
		// Derived expectation: each digest must equal the independent HMAC.
		want := independentDigest("prompt-v1", tenantKey, SplitBlocks(prompt)[i])
		if first[i] != want {
			t.Fatalf("run=%s block %d digest=%q want=%q", runUUID, i, first[i], want)
		}
		// Digest must be 64 hex chars (SHA-256), not raw passthrough.
		if len(first[i]) != hex.EncodedLen(sha256.Size) {
			t.Fatalf("run=%s block %d digest len=%d want=%d", runUUID, i, len(first[i]), hex.EncodedLen(sha256.Size))
		}
	}

	// Raw-absent: no raw block substring may appear anywhere in the output.
	joined := strings.Join(first, " ")
	for _, raw := range rawSubstrings(prompt) {
		if strings.Contains(joined, raw) {
			t.Fatalf("run=%s raw substring %q leaked into output %q", runUUID, raw, joined)
		}
	}
}

// Clause 2: SAME prompt under TWO DIFFERENT tenant keys -> DIFFERENT block
// hashes (cross-tenant unlinkability).
//
// Mutation: if the HMAC were keyed by a constant (ignoring tenantKey), the two
// tenants would produce IDENTICAL digests and this test would fail.
func TestAnonymize_DifferentTenantsDiffer(t *testing.T) {
	runUUID := newRunUUID(t, "clause2")
	a := New("prompt-v1")
	keyA := []byte("tenant-alpha-secret-key")
	keyB := []byte("tenant-bravo-secret-key")
	prompt := "alpha bravo charlie delta"

	t.Logf("run=%s clause=2 before: prompt=%q keyA=%q keyB=%q", runUUID, prompt, keyA, keyB)

	outA := a.Anonymize(keyA, prompt)
	outB := a.Anonymize(keyB, prompt)

	t.Logf("run=%s clause=2 after: outA=%v outB=%v", runUUID, outA, outB)

	if len(outA) != len(outB) {
		t.Fatalf("run=%s block counts differ: %d vs %d", runUUID, len(outA), len(outB))
	}
	if len(outA) == 0 {
		t.Fatalf("run=%s expected non-empty block output", runUUID)
	}
	for i := range outA {
		if outA[i] == outB[i] {
			t.Fatalf("run=%s block %d cross-tenant hash collided: %q (key must be honored)", runUUID, i, outA[i])
		}
		// Each side must match its own independently-derived, key-specific digest.
		blk := SplitBlocks(prompt)[i]
		if want := independentDigest("prompt-v1", keyA, blk); outA[i] != want {
			t.Fatalf("run=%s block %d keyA digest=%q want=%q", runUUID, i, outA[i], want)
		}
		if want := independentDigest("prompt-v1", keyB, blk); outB[i] != want {
			t.Fatalf("run=%s block %d keyB digest=%q want=%q", runUUID, i, outB[i], want)
		}
	}
}

// Clause 3: distinct block content -> distinct hashes (no collision on distinct
// blocks), under one fixed tenant key.
//
// Mutation: if hashBlock ignored the block content (e.g. hashed only the key or
// a constant), all distinct blocks would map to one digest and this fails.
func TestAnonymize_DistinctContentDistinctHashes(t *testing.T) {
	runUUID := newRunUUID(t, "clause3")
	a := New("prompt-v1")
	tenantKey := []byte("tenant-alpha-secret-key")
	prompt := "apple banana cherry date egg fig grape"

	t.Logf("run=%s clause=3 before: prompt=%q blocks=%v", runUUID, prompt, SplitBlocks(prompt))

	out := a.Anonymize(tenantKey, prompt)

	t.Logf("run=%s clause=3 after: out=%v", runUUID, out)

	blocks := SplitBlocks(prompt)
	if len(out) != len(blocks) {
		t.Fatalf("run=%s out=%d blocks=%d", runUUID, len(out), len(blocks))
	}

	seen := make(map[string]string, len(out))
	for i, d := range out {
		if prev, dup := seen[d]; dup {
			t.Fatalf("run=%s distinct blocks %q and %q collided to digest %q", runUUID, prev, blocks[i], d)
		}
		seen[d] = blocks[i]
		// Same block twice must still produce the same digest (determinism within).
		if want := a.Anonymize(tenantKey, blocks[i]); len(want) != 1 || want[0] != d {
			t.Fatalf("run=%s block %q not self-consistent: %v vs %q", runUUID, blocks[i], want, d)
		}
	}

	// Identical block repeated must dedup to one digest (correlation property).
	dupOut := a.Anonymize(tenantKey, "repeat repeat repeat")
	if len(dupOut) != 3 {
		t.Fatalf("run=%s dup blocks=%d want=3", runUUID, len(dupOut))
	}
	if dupOut[0] != dupOut[1] || dupOut[1] != dupOut[2] {
		t.Fatalf("run=%s identical blocks must share digest: %v", runUUID, dupOut)
	}
}
