package modelintegrity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// This file contains ADVERSARIAL tamper/forgery tests for the model-integrity
// gate. The security property under test (from the package doc): a model is a
// manifest of {Path, SHA-256, Size} entries; VERIFY succeeds iff EVERY on-disk
// file matches BOTH the pinned digest AND the pinned size. A tampered or
// mis-served model MUST be rejected; only the genuine model is accepted.
//
// Every negative test below is constructed so it would PASS (wrongly accept the
// tampered model) if the corresponding integrity check were removed — each
// test documents the exact mutation that makes it bite. The positive baselines
// would conversely FAIL if a genuine model were wrongly rejected.

// --- Genuine-accept baseline (whole model) --------------------------------

// TestAdversarial_GenuineMultiFileModelAccepted is the end-to-end ACCEPT
// baseline: a complete, untampered multi-file model (weights + config +
// tokenizer) sealed with the digests of its real bytes verifies OK, with every
// per-file result clean.
//
// Mutation that makes this FAIL: corrupt `want` in verifyEntry before the
// digest compare (or force res.OK=false) — a genuine model would be rejected.
func TestAdversarial_GenuineMultiFileModelAccepted(t *testing.T) {
	dir := t.TempDir()
	weights := []byte("\x00\x01\x02SAFE-TENSOR-WEIGHTS-shard\xff\xfe")
	config := []byte(`{"arch":"helix","hidden":4096}`)
	tok := []byte("tokenizer\nmerges\n0xCAFEBABE")

	writeFile(t, dir, "model.safetensors", weights)
	writeFile(t, dir, "config.json", config)
	writeFile(t, dir, "tok/tokenizer.model", tok)

	entries := []ManifestEntry{
		{Path: "model.safetensors", SHA256: sha256hex(weights), Size: int64(len(weights))},
		{Path: "config.json", SHA256: sha256hex(config), Size: int64(len(config))},
		{Path: "tok/tokenizer.model", SHA256: sha256hex(tok), Size: int64(len(tok))},
	}

	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("genuine model: unexpected usage error: %v", err)
	}
	if !rep.OK {
		t.Fatalf("genuine untampered model MUST be ACCEPTED, got failures: %v", rep.FailedPaths())
	}
	for _, fr := range rep.Files {
		if !fr.OK || fr.Mismatch != MismatchNone {
			t.Fatalf("entry %s should verify cleanly, got %+v", fr.Path, fr)
		}
	}
}

// --- Recorded-hash tamper: comparison is against the manifest, not an echo --

// TestAdversarial_RecordedHashTamperRejected is the anti-bluff anchor proving
// VerifyFile compares the on-disk digest against the SUPPLIED expected digest,
// not against a value re-derived from the same (tampered) bytes. An attacker
// who flips a byte but does NOT (cannot) update the pinned digest must be
// rejected. We pin the PRISTINE digest, write TAMPERED bytes of equal length.
//
// Mutation that makes this FAIL: replace the comparison so VerifyFile recomputes
// the expected from the file itself (e.g. `want = got`) or always returns nil —
// the tampered file would then be accepted and errors.Is(ErrSHAMismatch) fails.
func TestAdversarial_RecordedHashTamperRejected(t *testing.T) {
	dir := t.TempDir()
	pristine := []byte("genuine-weights-block-AAAAAAAAAAAAAAAA")
	pinned := sha256hex(pristine) // recorded BEFORE tamper

	tampered := make([]byte, len(pristine))
	copy(tampered, pristine)
	tampered[0] ^= 0x80 // same length, different content
	path := writeFile(t, dir, "weights.bin", tampered)

	if sha256hex(tampered) == pinned {
		t.Fatalf("test setup invalid: tamper did not change the digest")
	}

	err := VerifyFile(path, pinned, int64(len(tampered)))
	if err == nil {
		t.Fatalf("SECURITY: tampered model accepted against pinned digest (NO error)")
	}
	if !errors.Is(err, ErrSHAMismatch) {
		t.Fatalf("want ErrSHAMismatch for content tamper vs pinned digest, got %v", err)
	}
}

// --- Path-swap attack (path-binding) --------------------------------------

// TestAdversarial_PathSwapAttackRejected proves the manifest binds each digest
// to a specific PATH. Attacker swaps the CONTENTS of two files: a.bin now holds
// b's bytes and b.bin holds a's bytes. Each blob is individually a genuine,
// hash-valid model file — but served at the WRONG path. A verifier that checked
// "is this digest present somewhere in the manifest" instead of "does THIS path
// match ITS pinned digest" would be fooled. Verification MUST fail, naming both
// swapped paths as SHA mismatches (sizes differ here only incidentally; we make
// them equal length to force the SHA path).
//
// Mutation that makes this FAIL: change verifyEntry to look the digest up across
// all entries rather than comparing to this entry's `want` — the swap would be
// accepted and the overall-FAIL / per-path assertions fail.
func TestAdversarial_PathSwapAttackRejected(t *testing.T) {
	dir := t.TempDir()
	aBytes := []byte("AAAA-content-of-file-a-equal-len")
	bBytes := []byte("BBBB-content-of-file-b-equal-len")
	if len(aBytes) != len(bBytes) {
		t.Fatalf("test setup: a and b must be equal length to force SHA-path swap")
	}

	// Manifest pins a.bin -> hash(a), b.bin -> hash(b).
	entries := []ManifestEntry{
		{Path: "a.bin", SHA256: sha256hex(aBytes), Size: int64(len(aBytes))},
		{Path: "b.bin", SHA256: sha256hex(bBytes), Size: int64(len(bBytes))},
	}
	// On disk the contents are SWAPPED.
	writeFile(t, dir, "a.bin", bBytes)
	writeFile(t, dir, "b.bin", aBytes)

	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("unexpected usage error: %v", err)
	}
	if rep.OK {
		t.Fatalf("SECURITY: path-swapped model accepted; each blob valid but at wrong path")
	}
	failed := rep.FailedPaths()
	if len(failed) != 2 {
		t.Fatalf("want BOTH swapped paths failed, got %v", failed)
	}
	for _, fr := range rep.Files {
		if fr.OK || fr.Mismatch != MismatchSHA {
			t.Fatalf("swapped %s must fail as SHA mismatch, got %+v", fr.Path, fr)
		}
	}
}

// --- Wrong-but-valid digest (forged pin of different bytes) ----------------

// TestAdversarial_ValidDigestOfDifferentBytesRejected proves that pinning a
// well-formed digest that is the genuine hash of DIFFERENT content does not let
// the wrong file through. The digest is syntactically valid (passes
// normalizeDigest), so this isolates the actual cryptographic comparison from
// the format check.
//
// Mutation that makes this FAIL: drop the `if got != want` comparison — any
// syntactically valid digest would be accepted for any file.
func TestAdversarial_ValidDigestOfDifferentBytesRejected(t *testing.T) {
	dir := t.TempDir()
	served := []byte("the-bytes-actually-on-disk-XX")
	other := []byte("the-bytes-actually-on-disk-YY") // same length, valid hash
	if len(served) != len(other) {
		t.Fatalf("test setup: lengths must match to isolate the SHA check")
	}
	path := writeFile(t, dir, "f.bin", served)

	// Pin the genuine hash of `other`, not of what is on disk.
	err := VerifyFile(path, sha256hex(other), int64(len(served)))
	if err == nil {
		t.Fatalf("SECURITY: file accepted under a valid digest of DIFFERENT bytes")
	}
	if !errors.Is(err, ErrSHAMismatch) {
		t.Fatalf("want ErrSHAMismatch, got %v", err)
	}
}

// --- Zero-length / empty-file tamper --------------------------------------

// TestAdversarial_EmptyFileWhereContentExpected proves a wiped (0-byte) file
// where the manifest expects real content is rejected as a SIZE mismatch — a
// length-zero truncation must not slip through.
//
// Mutation that makes this FAIL: remove the `if n != e.Size` guard — the empty
// file would fall through to the digest check (reported as SHA, not SIZE), or
// if the expected were also empty, an attacker-wiped file would mis-pass.
func TestAdversarial_EmptyFileWhereContentExpected(t *testing.T) {
	dir := t.TempDir()
	expected := []byte("non-empty-genuine-content")
	writeFile(t, dir, "wiped.bin", []byte{}) // 0 bytes on disk

	entries := []ManifestEntry{
		{Path: "wiped.bin", SHA256: sha256hex(expected), Size: int64(len(expected))},
	}
	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("unexpected usage error: %v", err)
	}
	if rep.OK {
		t.Fatalf("SECURITY: empty file accepted where content expected")
	}
	if rep.Files[0].Mismatch != MismatchSize {
		t.Fatalf("want MismatchSize for wiped file, got %v (%s)",
			rep.Files[0].Mismatch, rep.Files[0].Detail)
	}
}

// TestAdversarial_EmptyExpectedVsNonEmptyDisk is the symmetric guard: the
// manifest pins a genuinely-empty artifact (size 0, sha of empty), but disk
// holds injected bytes. This must be rejected as a SIZE mismatch — an attacker
// cannot smuggle a payload into a slot declared empty.
//
// Mutation that makes this FAIL: remove the size guard; the non-empty disk file
// would be judged on digest alone and reported as SHA, failing the SIZE assert
// (and an all-zero-length manifest with no size check could accept arbitrary
// injected content).
func TestAdversarial_EmptyExpectedVsNonEmptyDisk(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "marker.txt", []byte("INJECTED-PAYLOAD"))

	entries := []ManifestEntry{
		{Path: "marker.txt", SHA256: sha256hex([]byte{}), Size: 0},
	}
	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("unexpected usage error: %v", err)
	}
	if rep.OK {
		t.Fatalf("SECURITY: payload injected into a declared-empty slot was accepted")
	}
	if rep.Files[0].Mismatch != MismatchSize {
		t.Fatalf("want MismatchSize, got %v (%s)", rep.Files[0].Mismatch, rep.Files[0].Detail)
	}
}

// --- Documented coverage policy: extra unlisted file is IGNORED ------------

// TestAdversarial_ExtraUnlistedFileIgnored pins the DOCUMENTED policy: the
// manifest is a list of EXPECTED files; VerifyManifest verifies each listed file
// and does NOT scan the directory for extras. An attacker who DROPS an extra,
// unlisted file into the model directory does NOT cause a verification failure,
// because the listed files are all intact. This test documents that behavior so
// any future change to "complete directory coverage" is a deliberate, visible
// decision rather than a silent regression.
//
// NOTE: this is an ACCEPT for the listed set; it is intentionally not a security
// failure under the current manifest-is-the-allowlist design. See report.
func TestAdversarial_ExtraUnlistedFileIgnored(t *testing.T) {
	dir := t.TempDir()
	genuine := []byte("genuine-listed-weights")
	writeFile(t, dir, "model.bin", genuine)
	// Attacker drops an unlisted extra file alongside the model.
	writeFile(t, dir, "EVIL-extra.so", []byte("malicious-unlisted-blob"))

	entries := []ManifestEntry{
		{Path: "model.bin", SHA256: sha256hex(genuine), Size: int64(len(genuine))},
	}
	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("unexpected usage error: %v", err)
	}
	if !rep.OK {
		t.Fatalf("listed files intact: manifest verification should be OK, got %v", rep.FailedPaths())
	}
	if len(rep.Files) != 1 {
		t.Fatalf("manifest verifies only listed entries; want 1 result, got %d", len(rep.Files))
	}
	// Confirm the extra file does physically exist (so we know it was truly ignored).
	if _, err := os.Stat(filepath.Join(dir, "EVIL-extra.so")); err != nil {
		t.Fatalf("test setup: extra file should exist on disk: %v", err)
	}
}

// --- Hash length / format edge cases --------------------------------------

// TestAdversarial_TruncatedExpectedDigestRejected proves a too-short (truncated)
// expected digest is rejected as malformed BEFORE any filesystem read, so a
// truncated pin can never be coerced into a match.
//
// Mutation that makes this FAIL: remove normalizeDigest's length check — a short
// digest would reach the compare and yield ErrSHAMismatch (or, pathologically,
// match a short computed prefix), not ErrBadDigest.
func TestAdversarial_TruncatedExpectedDigestRejected(t *testing.T) {
	dir := t.TempDir()
	body := []byte("content")
	path := writeFile(t, dir, "f.bin", body)

	full := sha256hex(body)
	truncated := full[:len(full)-2] // 62 hex chars
	if err := VerifyFile(path, truncated, int64(len(body))); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("want ErrBadDigest for truncated digest, got %v", err)
	}
}

// TestAdversarial_EmptyExpectedDigestRejected proves an empty expected digest is
// rejected as malformed, not silently treated as "no check".
//
// Mutation that makes this FAIL: remove the length check in normalizeDigest — an
// empty string would slip through.
func TestAdversarial_EmptyExpectedDigestRejected(t *testing.T) {
	dir := t.TempDir()
	body := []byte("content")
	path := writeFile(t, dir, "f.bin", body)

	if err := VerifyFile(path, "", int64(len(body))); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("want ErrBadDigest for empty digest, got %v", err)
	}
}

// TestAdversarial_OverlongExpectedDigestRejected proves a too-long expected
// digest (65+ hex chars) is rejected as malformed.
//
// Mutation that makes this FAIL: change the length check from `!= 2*Size` to a
// `>=` / `<` style bound that allows overlong input.
func TestAdversarial_OverlongExpectedDigestRejected(t *testing.T) {
	dir := t.TempDir()
	body := []byte("content")
	path := writeFile(t, dir, "f.bin", body)

	overlong := sha256hex(body) + "ab" // 66 hex chars
	if err := VerifyFile(path, overlong, int64(len(body))); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("want ErrBadDigest for overlong digest, got %v", err)
	}
}

// TestAdversarial_NonHexExpectedDigestRejected proves a correct-length but
// non-hex expected digest is rejected (the hex.DecodeString guard), so a pin
// full of garbage cannot be smuggled past the format gate.
//
// Mutation that makes this FAIL: remove the hex.DecodeString validation in
// normalizeDigest — the 64-char non-hex string would reach the compare.
func TestAdversarial_NonHexExpectedDigestRejected(t *testing.T) {
	dir := t.TempDir()
	body := []byte("content")
	path := writeFile(t, dir, "f.bin", body)

	// 64 chars, but 'g'..'z' are not valid hex.
	nonHex := "gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg"
	if len(nonHex) != 64 {
		t.Fatalf("test setup: nonHex must be 64 chars, got %d", len(nonHex))
	}
	if err := VerifyFile(path, nonHex, int64(len(body))); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("want ErrBadDigest for non-hex digest, got %v", err)
	}
}
