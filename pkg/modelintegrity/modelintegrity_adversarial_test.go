package modelintegrity

import (
	"errors"
	"strings"
	"testing"
)

// This file holds a SECOND wave of adversarial tests for the model-integrity
// gate, focused on surfaces the first adversarial file does not exercise:
//   - check ORDERING (size guard runs before the SHA guard even when BOTH the
//     size and the content are wrong),
//   - digest NORMALIZATION (whitespace + uppercase) feeding the real compare,
//   - the boundary between the format gate (ErrBadDigest) and the cryptographic
//     compare (ErrSHAMismatch) for a well-formed-but-wrong pin,
//   - impossible-to-satisfy pins (negative expected size) failing closed,
//   - MismatchKind/FailedPaths contracts.
//
// Helpers sha256hex(...) and writeFile(...) are defined in modelintegrity_test.go.

// --- Ordering: a doubly-wrong file (wrong size AND wrong bytes) -> SIZE ------

// TestAdversarial_WrongSizeAndWrongBytesReportsSize proves that when a file is
// tampered in BOTH dimensions (its size differs from the pin AND its content
// differs), the gate reports the SIZE mismatch — size is checked before the
// digest, per the doc: "Size is checked before content so a truncation/
// extension is reported as a size mismatch rather than being masked as a digest
// mismatch." The file is still REJECTED; this pins the *classification* so the
// caller's "why" stays truthful.
//
// Mutation that makes this FAIL: move the `if n != e.Size` guard below the SHA
// compare in verifyEntry (or delete it) — the result would be classified SHA,
// not SIZE.
func TestAdversarial_WrongSizeAndWrongBytesReportsSize(t *testing.T) {
	dir := t.TempDir()
	pristine := []byte("genuine-weights-block-of-known-length")
	pinned := sha256hex(pristine)

	// On disk: different length AND different content from the pin.
	onDisk := []byte("totally-different-and-shorter")
	if len(onDisk) == len(pristine) {
		t.Fatalf("test setup: lengths must differ to force the size path")
	}
	writeFile(t, dir, "w.bin", onDisk)

	entries := []ManifestEntry{
		{Path: "w.bin", SHA256: pinned, Size: int64(len(pristine))},
	}
	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("unexpected usage error: %v", err)
	}
	if rep.OK {
		t.Fatalf("SECURITY: doubly-tampered file accepted")
	}
	if got := rep.Files[0].Mismatch; got != MismatchSize {
		t.Fatalf("want MismatchSize (size checked first), got %v (%s)",
			got, rep.Files[0].Detail)
	}
}

// TestAdversarial_VerifyFileWrongSizeAndWrongBytesIsSizeMismatch is the
// VerifyFile-level twin of the above: a doubly-wrong file errors with
// ErrSizeMismatch, not ErrSHAMismatch.
//
// Mutation that makes this FAIL: reorder VerifyFile to compare the digest before
// the `n != expectedSize` check.
func TestAdversarial_VerifyFileWrongSizeAndWrongBytesIsSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	pristine := []byte("the-real-pristine-bytes-AAAA")
	pinned := sha256hex(pristine)
	onDisk := []byte("shorter-wrong-bytes")
	path := writeFile(t, dir, "f.bin", onDisk)

	err := VerifyFile(path, pinned, int64(len(pristine)))
	if err == nil {
		t.Fatalf("SECURITY: doubly-tampered file accepted")
	}
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("want ErrSizeMismatch (size checked before SHA), got %v", err)
	}
	if errors.Is(err, ErrSHAMismatch) {
		t.Fatalf("size-wrong file must not be reported as SHA mismatch, got %v", err)
	}
}

// --- Digest normalization actually feeds the genuine compare ----------------

// TestAdversarial_WhitespaceWrappedDigestStillVerifies proves the documented
// TrimSpace+ToLower normalization is applied to the EXPECTED digest before the
// compare, so a genuine model pinned with a digest that has stray surrounding
// whitespace/newlines (common when a hash is read from a file) still verifies.
// This is an ACCEPT baseline: it would break if normalization were dropped.
//
// Mutation that makes this FAIL: remove strings.TrimSpace in normalizeDigest —
// the wrapped digest would fail the hex/length gate or the compare and a genuine
// model would be wrongly REJECTED.
func TestAdversarial_WhitespaceWrappedDigestStillVerifies(t *testing.T) {
	dir := t.TempDir()
	body := []byte("genuine-content-for-ws-normalization")
	path := writeFile(t, dir, "f.bin", body)

	wrapped := "  \t" + sha256hex(body) + "\n"
	if err := VerifyFile(path, wrapped, int64(len(body))); err != nil {
		t.Fatalf("genuine model with whitespace-wrapped pin must verify, got %v", err)
	}
}

// TestAdversarial_UppercaseDigestInManifestVerifies proves uppercase pins are
// normalized in the MANIFEST path too (verifyEntry compares the always-lowercase
// computed digest against the normalized `want`). The first adversarial file
// only covers uppercase at the VerifyFile level.
//
// Mutation that makes this FAIL: drop strings.ToLower in normalizeDigest — the
// uppercase pin would never equal the lowercase computed hex and a genuine model
// would be REJECTED.
func TestAdversarial_UppercaseDigestInManifestVerifies(t *testing.T) {
	dir := t.TempDir()
	body := []byte("genuine-manifest-content")
	writeFile(t, dir, "m.bin", body)

	entries := []ManifestEntry{
		{Path: "m.bin", SHA256: strings.ToUpper(sha256hex(body)), Size: int64(len(body))},
	}
	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("uppercase pin should normalize, unexpected usage error: %v", err)
	}
	if !rep.OK {
		t.Fatalf("genuine model with uppercase pin must verify, failures: %v", rep.FailedPaths())
	}
	// The recorded ExpectedSHA256 must be the normalized (lower-case) form.
	if got := rep.Files[0].ExpectedSHA256; got != strings.ToLower(got) {
		t.Fatalf("ExpectedSHA256 must be normalized lower-case, got %q", got)
	}
}

// --- Format gate vs crypto compare boundary ---------------------------------

// TestAdversarial_WellFormedWrongDigestIsSHANotBadDigest proves that a
// 64-char, all-hex, but WRONG digest crosses the format gate cleanly and is
// rejected by the genuine cryptographic compare (ErrSHAMismatch), NOT by the
// format gate (ErrBadDigest). This isolates the crypto check: a fail-open bug
// where the compare is skipped for valid-format digests would surface here.
//
// Mutation that makes this FAIL: replace `if got != want` with `return nil`
// (or `want = got`) — a valid-format wrong digest would be ACCEPTED.
func TestAdversarial_WellFormedWrongDigestIsSHANotBadDigest(t *testing.T) {
	dir := t.TempDir()
	body := []byte("on-disk-bytes")
	path := writeFile(t, dir, "f.bin", body)

	// A well-formed (64 hex chars) but deliberately wrong digest: all zeros.
	allZero := strings.Repeat("0", 64)
	if allZero == sha256hex(body) {
		t.Fatalf("test setup: zero digest unexpectedly equals real digest")
	}
	err := VerifyFile(path, allZero, int64(len(body)))
	if err == nil {
		t.Fatalf("SECURITY: well-formed wrong digest accepted")
	}
	if !errors.Is(err, ErrSHAMismatch) {
		t.Fatalf("want ErrSHAMismatch for well-formed wrong digest, got %v", err)
	}
	if errors.Is(err, ErrBadDigest) {
		t.Fatalf("well-formed digest must NOT be rejected by the format gate, got %v", err)
	}
}

// --- Impossible pins fail closed --------------------------------------------

// TestAdversarial_NegativeExpectedSizeFailsClosed proves a manifest entry whose
// expected size is negative can never be satisfied by any real file (file sizes
// are >= 0), so it is rejected as a SIZE mismatch — it must not somehow be
// treated as "skip the size check". Fails closed.
//
// Mutation that makes this FAIL: change the size guard to e.g. `if e.Size >= 0
// && n != e.Size` (skip when negative) — the negative-size entry would fall
// through to the SHA path and could mis-classify or, combined with other
// loosening, accept.
func TestAdversarial_NegativeExpectedSizeFailsClosed(t *testing.T) {
	dir := t.TempDir()
	body := []byte("real-content")
	writeFile(t, dir, "f.bin", body)

	entries := []ManifestEntry{
		{Path: "f.bin", SHA256: sha256hex(body), Size: -1},
	}
	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("unexpected usage error: %v", err)
	}
	if rep.OK {
		t.Fatalf("SECURITY: entry with impossible negative size verified OK")
	}
	if got := rep.Files[0].Mismatch; got != MismatchSize {
		t.Fatalf("want MismatchSize for negative expected size, got %v", got)
	}
}

// --- Missing entry is classified MISSING, not silently OK -------------------

// TestAdversarial_MissingFileIsMissingNotVerified proves an absent file in the
// manifest is classified MismatchMissing and drags the whole report to not-OK —
// an attacker cannot make a model "verify" by deleting a file the gate fails to
// open. It also confirms FailedPaths names exactly the missing path.
//
// Mutation that makes this FAIL: in verifyEntry, on os.Open error set res.OK =
// true (or leave Mismatch as MismatchNone) — the missing file would be treated
// as verified.
func TestAdversarial_MissingFileIsMissingNotVerified(t *testing.T) {
	dir := t.TempDir()
	present := []byte("present-file-content")
	writeFile(t, dir, "present.bin", present)
	// "gone.bin" is intentionally NOT written.

	entries := []ManifestEntry{
		{Path: "present.bin", SHA256: sha256hex(present), Size: int64(len(present))},
		{Path: "gone.bin", SHA256: sha256hex([]byte("whatever")), Size: 8},
	}
	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("unexpected usage error: %v", err)
	}
	if rep.OK {
		t.Fatalf("SECURITY: manifest with a missing file verified OK")
	}
	if rep.Files[1].Mismatch != MismatchMissing {
		t.Fatalf("want MismatchMissing for absent file, got %v", rep.Files[1].Mismatch)
	}
	failed := rep.FailedPaths()
	if len(failed) != 1 || failed[0] != "gone.bin" {
		t.Fatalf("FailedPaths must name exactly the missing file, got %v", failed)
	}
}

// --- MismatchKind.String contract -------------------------------------------

// TestAdversarial_MismatchKindStringContract pins the diagnostic strings so a
// silent renaming/swap (which would corrupt operator-facing "why rejected"
// messages and could mask a SHA failure as "ok") is caught.
//
// Mutation that makes this FAIL: swap any case label (e.g. MismatchSHA ->
// "ok") in MismatchKind.String.
func TestAdversarial_MismatchKindStringContract(t *testing.T) {
	cases := map[MismatchKind]string{
		MismatchNone:    "ok",
		MismatchMissing: "missing",
		MismatchSize:    "size-mismatch",
		MismatchSHA:     "sha-mismatch",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Fatalf("MismatchKind(%d).String() = %q, want %q", int(k), got, want)
		}
	}
	// An out-of-range kind must not collide with a real classification.
	if got := MismatchKind(99).String(); got == "ok" {
		t.Fatalf("unknown MismatchKind must not render as %q (fail-open diagnostic)", "ok")
	}
}

// --- FailedPaths is empty on a clean report ---------------------------------

// TestAdversarial_FailedPathsEmptyWhenClean proves FailedPaths returns nothing
// for a fully-verified model — a non-empty list on a clean model would be a
// false alarm, and (inverted) a bug that hides failures would show here.
//
// Mutation that makes this FAIL: in FailedPaths, change `if !f.OK` to `if f.OK`.
func TestAdversarial_FailedPathsEmptyWhenClean(t *testing.T) {
	dir := t.TempDir()
	a := []byte("alpha-bytes")
	b := []byte("beta-bytes-2")
	writeFile(t, dir, "a.bin", a)
	writeFile(t, dir, "b.bin", b)

	entries := []ManifestEntry{
		{Path: "a.bin", SHA256: sha256hex(a), Size: int64(len(a))},
		{Path: "b.bin", SHA256: sha256hex(b), Size: int64(len(b))},
	}
	rep, err := VerifyManifest(dir, entries)
	if err != nil {
		t.Fatalf("unexpected usage error: %v", err)
	}
	if !rep.OK {
		t.Fatalf("clean model must verify, failures: %v", rep.FailedPaths())
	}
	if fp := rep.FailedPaths(); len(fp) != 0 {
		t.Fatalf("FailedPaths must be empty on a clean report, got %v", fp)
	}
}
