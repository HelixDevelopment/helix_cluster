package modelintegrity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// sha256hex is the working reference digest used by the tests to derive the
// EXPECTED manifest values, exactly the way crypto/sha256 derives the ACTUAL
// value inside the package. Tests therefore assert real hash agreement, not a
// constant echoed back.
func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// writeFile writes content into dir/name (creating parent dirs) and returns the
// absolute path.
func writeFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}

// --- VerifyFile -----------------------------------------------------------

// TestVerifyFile_MatchingBytesAndSizeVerify proves a file whose bytes hash to
// the expected SHA with a matching size passes.
//
// Mutation that makes this FAIL: change the final `return nil` in VerifyFile to
// `return ErrSHAMismatch` (or corrupt `want` before comparison) — a genuine
// match would then be reported as a failure.
func TestVerifyFile_MatchingBytesAndSizeVerify(t *testing.T) {
	dir := t.TempDir()
	body := []byte("helix-model-weights-v1\x00\x01\x02binary")
	path := writeFile(t, dir, "model.safetensors", body)

	if err := VerifyFile(path, sha256hex(body), int64(len(body))); err != nil {
		t.Fatalf("expected VERIFY, got error: %v", err)
	}
}

// TestVerifyFile_FlippedByteIsSHAMismatch is the anti-bluff anchor: a single
// flipped byte keeps the size identical, so a verifier that only checks size
// would wrongly PASS. We assert the failure is specifically a SHA mismatch.
//
// Mutation that makes this FAIL: delete the digest comparison block in
// VerifyFile (the `if got != want { ... }`) so it only checks size — the
// same-length flip then passes and this test's "want ErrSHAMismatch" fails.
func TestVerifyFile_FlippedByteIsSHAMismatch(t *testing.T) {
	dir := t.TempDir()
	body := []byte("helix-model-weights-v1-payload-bytes")
	expectedSHA := sha256hex(body) // digest of the PRISTINE bytes

	tampered := make([]byte, len(body))
	copy(tampered, body)
	tampered[3] ^= 0xFF // flip one byte; length unchanged
	path := writeFile(t, dir, "model.bin", tampered)

	err := VerifyFile(path, expectedSHA, int64(len(tampered)))
	if !errors.Is(err, ErrSHAMismatch) {
		t.Fatalf("want ErrSHAMismatch for same-size byte flip, got %v", err)
	}
	if errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("byte flip must NOT be reported as a size mismatch: %v", err)
	}
}

// TestVerifyFile_TruncatedIsSizeMismatch proves a short file fails with a SIZE
// mismatch (not a SHA mismatch).
//
// Mutation that makes this FAIL: remove the `if n != expectedSize` block so the
// size guard is gone — a truncated file then surfaces as ErrSHAMismatch and the
// errors.Is(ErrSizeMismatch) assertion fails.
func TestVerifyFile_TruncatedIsSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	body := []byte("0123456789abcdef-full-length-content")
	expectedSHA := sha256hex(body)
	expectedSize := int64(len(body))

	path := writeFile(t, dir, "trunc.bin", body[:len(body)-5]) // 5 bytes short

	err := VerifyFile(path, expectedSHA, expectedSize)
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("want ErrSizeMismatch for truncated file, got %v", err)
	}
}

// TestVerifyFile_ExtendedIsSizeMismatch proves an over-length file also fails
// with a SIZE mismatch.
//
// Mutation that makes this FAIL: same as truncation — drop the size guard and
// the extended file is reported as a SHA mismatch instead.
func TestVerifyFile_ExtendedIsSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	body := []byte("baseline-content")
	expectedSHA := sha256hex(body)
	expectedSize := int64(len(body))

	extended := append(append([]byte{}, body...), []byte("EXTRA")...)
	path := writeFile(t, dir, "ext.bin", extended)

	err := VerifyFile(path, expectedSHA, expectedSize)
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("want ErrSizeMismatch for extended file, got %v", err)
	}
}

// TestVerifyFile_MissingFileErrors proves an absent file errors as missing.
//
// Mutation that makes this FAIL: change the os.Open error branch to `return nil`
// — a non-existent file would then be treated as verified and the
// errors.Is(ErrMissingFile) assertion fails.
func TestVerifyFile_MissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	body := []byte("never-written")
	missing := filepath.Join(dir, "does-not-exist.bin")

	err := VerifyFile(missing, sha256hex(body), int64(len(body)))
	if !errors.Is(err, ErrMissingFile) {
		t.Fatalf("want ErrMissingFile for absent file, got %v", err)
	}
}

// TestVerifyFile_MalformedDigestRejected proves a non-hex/short expected digest
// is rejected before touching the filesystem.
//
// Mutation that makes this FAIL: delete the normalizeDigest call at the top of
// VerifyFile — a garbage digest would slip through to the comparison and yield
// ErrSHAMismatch (or pass) instead of ErrBadDigest.
func TestVerifyFile_MalformedDigestRejected(t *testing.T) {
	dir := t.TempDir()
	body := []byte("content")
	path := writeFile(t, dir, "f.bin", body)

	if err := VerifyFile(path, "not-a-valid-sha", int64(len(body))); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("want ErrBadDigest for malformed digest, got %v", err)
	}
}

// --- VerifyManifest -------------------------------------------------------

// TestVerifyManifest_AllPassOverallPass proves a manifest whose every entry
// matches yields an overall-pass Report with no failed paths.
//
// Mutation that makes this FAIL: in verifyEntry, force `res.OK = true`
// unconditionally AND set rep.OK true regardless — but more precisely, change
// the per-file digest comparison so a real file is judged not-OK; the
// all-good manifest would then report OK=false and this test fails. Conversely,
// removing the `n != e.Size` / digest checks here is covered by the tamper test
// below.
func TestVerifyManifest_AllPassOverallPass(t *testing.T) {
	dir := t.TempDir()
	a := []byte("config-json-bytes")
	b := []byte("tokenizer-model-bytes-0xFF")
	writeFile(t, dir, "config.json", a)
	writeFile(t, dir, "sub/tokenizer.model", b)

	entries := []ManifestEntry{
		{Path: "config.json", SHA256: sha256hex(a), Size: int64(len(a))},
		{Path: "sub/tokenizer.model", SHA256: sha256hex(b), Size: int64(len(b))},
	}

	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.OK {
		t.Fatalf("want overall OK, got failures: %v", rep.FailedPaths())
	}
	if len(rep.Files) != 2 {
		t.Fatalf("want 2 file results, got %d", len(rep.Files))
	}
	for _, fr := range rep.Files {
		if fr.Mismatch != MismatchNone || fr.ActualSHA256 != fr.ExpectedSHA256 {
			t.Fatalf("entry %s: want clean match, got %+v", fr.Path, fr)
		}
	}
}

// TestVerifyManifest_OneTamperedEntryOverallFail proves that when exactly one
// entry's bytes are flipped (same size), the Report is overall-fail and names
// EXACTLY that path as a SHA mismatch — while the untouched entry stays OK.
//
// Mutation that makes this FAIL: in verifyEntry, drop the digest comparison
// (`if res.ActualSHA256 != want`) so it only compares size — the same-size
// tamper passes, rep.OK becomes true, FailedPaths() is empty, and both the
// overall-fail and the "names exactly that path" assertions fail.
func TestVerifyManifest_OneTamperedEntryOverallFail(t *testing.T) {
	dir := t.TempDir()
	good := []byte("untouched-weights-shard-0")
	orig := []byte("weights-shard-1-original-bytes")

	writeFile(t, dir, "shard0.bin", good)
	// Manifest pins the ORIGINAL digest; on disk we write a same-length tamper.
	tampered := make([]byte, len(orig))
	copy(tampered, orig)
	tampered[len(tampered)-1] ^= 0x01
	writeFile(t, dir, "shard1.bin", tampered)

	entries := []ManifestEntry{
		{Path: "shard0.bin", SHA256: sha256hex(good), Size: int64(len(good))},
		{Path: "shard1.bin", SHA256: sha256hex(orig), Size: int64(len(orig))},
	}

	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.OK {
		t.Fatalf("want overall FAIL due to tampered shard1.bin, got OK")
	}
	failed := rep.FailedPaths()
	if len(failed) != 1 || failed[0] != "shard1.bin" {
		t.Fatalf("want exactly [shard1.bin] failed, got %v", failed)
	}
	// The named failure must be a SHA mismatch, proving content (not size) check.
	for _, fr := range rep.Files {
		switch fr.Path {
		case "shard0.bin":
			if !fr.OK {
				t.Fatalf("untouched shard0.bin should verify, got %+v", fr)
			}
		case "shard1.bin":
			if fr.OK || fr.Mismatch != MismatchSHA {
				t.Fatalf("shard1.bin want sha-mismatch failure, got %+v", fr)
			}
			if fr.ActualSize != fr.ExpectedSize {
				t.Fatalf("tamper must be same size: expected %d got %d",
					fr.ExpectedSize, fr.ActualSize)
			}
		}
	}
}

// TestVerifyManifest_MissingEntryNamedMissing proves a manifest entry whose file
// is absent fails as missing and is named in FailedPaths.
//
// Mutation that makes this FAIL: in verifyEntry, change the os.Open error branch
// to set res.OK = true — the absent file would be treated as verified and both
// the overall-fail and the MismatchMissing assertions fail.
func TestVerifyManifest_MissingEntryNamedMissing(t *testing.T) {
	dir := t.TempDir()
	present := []byte("present-bytes")
	writeFile(t, dir, "present.bin", present)

	entries := []ManifestEntry{
		{Path: "present.bin", SHA256: sha256hex(present), Size: int64(len(present))},
		{Path: "absent.bin", SHA256: sha256hex([]byte("x")), Size: 1},
	}

	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.OK {
		t.Fatalf("want overall FAIL due to missing absent.bin")
	}
	if got := rep.FailedPaths(); len(got) != 1 || got[0] != "absent.bin" {
		t.Fatalf("want [absent.bin] failed, got %v", got)
	}
	for _, fr := range rep.Files {
		if fr.Path == "absent.bin" && fr.Mismatch != MismatchMissing {
			t.Fatalf("absent.bin want MismatchMissing, got %v", fr.Mismatch)
		}
	}
}

// TestVerifyManifest_SizeMismatchEntry proves a length-tampered entry is
// classified as a SIZE mismatch within the Report (distinct from SHA).
//
// Mutation that makes this FAIL: remove the `if n != e.Size` block in
// verifyEntry — the short file then falls through to the digest check and is
// reported as MismatchSHA, failing the MismatchSize assertion.
func TestVerifyManifest_SizeMismatchEntry(t *testing.T) {
	dir := t.TempDir()
	full := []byte("full-length-shard-content-here")
	writeFile(t, dir, "shard.bin", full[:len(full)-3]) // 3 bytes short on disk

	entries := []ManifestEntry{
		{Path: "shard.bin", SHA256: sha256hex(full), Size: int64(len(full))},
	}

	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.OK {
		t.Fatalf("want overall FAIL for size-tampered shard")
	}
	if rep.Files[0].Mismatch != MismatchSize {
		t.Fatalf("want MismatchSize, got %v (%s)", rep.Files[0].Mismatch, rep.Files[0].Detail)
	}
}

// TestVerifyManifest_EmptyRootErrors proves a usage fault (empty root) is a hard
// error, not a silent pass.
//
// Mutation that makes this FAIL: delete the `if rootDir == ""` guard — the call
// would proceed and return a (possibly OK) Report with nil error, failing the
// expectation of a non-nil error.
func TestVerifyManifest_EmptyRootErrors(t *testing.T) {
	if _, err := VerifyManifest("", nil); err == nil {
		t.Fatalf("want error for empty root directory, got nil")
	}
}

// TestVerifyManifest_MalformedDigestEntryErrors proves a manifest carrying a
// malformed expected digest is rejected as a usage fault.
//
// Mutation that makes this FAIL: remove the normalizeDigest check in
// VerifyManifest's loop — the bad digest would be compared as-is, producing a
// per-file SHA mismatch and a nil error, failing this assertion.
func TestVerifyManifest_MalformedDigestEntryErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.bin", []byte("data"))
	entries := []ManifestEntry{{Path: "f.bin", SHA256: "ZZZ", Size: 4}}

	if _, err := VerifyManifest(dir, entries); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("want ErrBadDigest, got %v", err)
	}
}

// TestVerifyFile_UppercaseDigestNormalized proves the expected digest is
// case-insensitive: an UPPER-case hex digest of the real bytes still verifies.
//
// Mutation that makes this FAIL: drop strings.ToLower in normalizeDigest — the
// upper-case expected digest would never equal the lower-case computed digest
// and a genuine match would be reported as ErrSHAMismatch.
func TestVerifyFile_UppercaseDigestNormalized(t *testing.T) {
	dir := t.TempDir()
	body := []byte("case-insensitive-digest-check")
	path := writeFile(t, dir, "f.bin", body)

	upper := []byte(sha256hex(body))
	for i, c := range upper {
		if c >= 'a' && c <= 'f' {
			upper[i] = c - 32
		}
	}
	if err := VerifyFile(path, string(upper), int64(len(body))); err != nil {
		t.Fatalf("upper-case digest of real bytes should VERIFY, got %v", err)
	}
}
