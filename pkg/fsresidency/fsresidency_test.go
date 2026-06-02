// Tests for package fsresidency.
//
// All tests use real filesystem I/O via t.TempDir().  No in-memory
// substitutions are used.  Determinism is guaranteed by constructing the file
// contents with fixed byte patterns — no wall-clock or random values appear in
// assertions.
package fsresidency

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// sha256Hex is a test helper that returns the lower-case hex SHA-256 of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// writeFile creates a file under dir with the given content and returns its
// path.  It calls t.Fatal on any I/O error.
func writeFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatalf("writeFile %q: %v", p, err)
	}
	return p
}

// ---------------------------------------------------------------------------
// TestPresentFileAllRangesVerify
// ---------------------------------------------------------------------------

// TestPresentFileAllRangesVerify verifies that VerifyRanges correctly reads
// multiple non-overlapping byte ranges from a present file and reports
// Match=true for every range when the expected hashes are correct.
//
// Mutation guard pinned here: the equality comparison inside VerifyRanges
// (result.Match = (computed == r.ExpectedSHA256)).
// If that line is mutated to always-true, the deliberate wrong-hash range
// added at the end of allWrongRangesSubtest would also pass, breaking that
// sub-assertion.
func TestPresentFileAllRangesVerify(t *testing.T) {
	dir := t.TempDir()

	// Build a deterministic 256-byte file: byte[i] = byte(i).
	content := make([]byte, 256)
	for i := range content {
		content[i] = byte(i)
	}
	path := writeFile(t, dir, "model.bin", content)

	const runID = "run-present-001"

	ranges := []Range{
		// Range 0: bytes 0..15 (16 bytes)
		{Offset: 0, Length: 16, ExpectedSHA256: sha256Hex(content[0:16])},
		// Range 1: bytes 64..127 (64 bytes)
		{Offset: 64, Length: 64, ExpectedSHA256: sha256Hex(content[64:128])},
		// Range 2: bytes 200..255 (56 bytes)
		{Offset: 200, Length: 56, ExpectedSHA256: sha256Hex(content[200:256])},
		// Range 3: single byte at offset 128
		{Offset: 128, Length: 1, ExpectedSHA256: sha256Hex(content[128:129])},
	}

	report, err := VerifyRanges(runID, path, ranges)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Sink-side: RunID must be echoed.
	if report.RunID != runID {
		t.Errorf("RunID: got %q, want %q", report.RunID, runID)
	}
	if report.Path != path {
		t.Errorf("Path: got %q, want %q", report.Path, path)
	}

	if len(report.Results) != len(ranges) {
		t.Fatalf("Results len: got %d, want %d", len(report.Results), len(ranges))
	}

	for i, res := range report.Results {
		if res.Err != nil {
			t.Errorf("range %d: unexpected Err: %v", i, res.Err)
		}
		if !res.Match {
			t.Errorf("range %d: Match=false, want true; computed=%q expected=%q",
				i, res.ComputedSHA256, ranges[i].ExpectedSHA256)
		}
		if res.ComputedSHA256 != ranges[i].ExpectedSHA256 {
			t.Errorf("range %d: ComputedSHA256=%q, want %q",
				i, res.ComputedSHA256, ranges[i].ExpectedSHA256)
		}
	}

	if !report.AllMatched {
		t.Error("AllMatched=false, want true")
	}

	// Extra sub-check: a range with a WRONG expected hash must NOT match.
	wrongRanges := []Range{
		{Offset: 0, Length: 16, ExpectedSHA256: "00000000000000000000000000000000" +
			"00000000000000000000000000000000"}, // 64 zeroes ≠ real hash
	}
	report2, err2 := VerifyRanges(runID, path, wrongRanges)
	if err2 != nil {
		t.Fatalf("wrong-hash sub-check unexpected error: %v", err2)
	}
	if report2.Results[0].Match {
		t.Error("wrong-hash sub-check: Match=true, want false — mutation guard violated")
	}
}

// ---------------------------------------------------------------------------
// TestMissingOrTruncatedFileFails
//
// MUTATION GUARD: the comparison `result.Match = (computed == r.ExpectedSHA256)`
// inside VerifyRanges.  Forcing it to always `true` would make the truncated-
// range assertion below fail, because the bytes-past-EOF range would wrongly
// report match=true even though it read 0 bytes.
// ---------------------------------------------------------------------------

// TestMissingOrTruncatedFileFails verifies two failure paths:
//  1. A file that does not exist yields ErrFileMissing and no panic.
//  2. A range whose Offset+Length exceeds the file size reports Match=false
//     and Err=ErrRangeTruncated, while an in-bounds range on the same call
//     still reports correctly.
func TestMissingOrTruncatedFileFails(t *testing.T) {
	dir := t.TempDir()

	const runID = "run-truncated-002"

	// -----------------------------------------------------------------------
	// Sub-test 1: missing file
	// -----------------------------------------------------------------------
	t.Run("MissingFile", func(t *testing.T) {
		missing := filepath.Join(dir, "does_not_exist.bin")

		report, err := VerifyRanges(runID, missing, []Range{
			{Offset: 0, Length: 8, ExpectedSHA256: sha256Hex([]byte("anything"))},
		})

		// Must surface ErrFileMissing.
		if !errors.Is(err, ErrFileMissing) {
			t.Fatalf("err: got %v, want ErrFileMissing", err)
		}

		// RunID must still be echoed in the partial report.
		if report.RunID != runID {
			t.Errorf("RunID: got %q, want %q", report.RunID, runID)
		}
	})

	// -----------------------------------------------------------------------
	// Sub-test 2: range past EOF on a truncated file
	// -----------------------------------------------------------------------
	t.Run("RangePastEOF", func(t *testing.T) {
		// Write a 32-byte file.
		content := make([]byte, 32)
		for i := range content {
			content[i] = byte(i + 1)
		}
		path := writeFile(t, dir, "small.bin", content)

		ranges := []Range{
			// Range 0: valid in-bounds range (bytes 0..15).
			{Offset: 0, Length: 16, ExpectedSHA256: sha256Hex(content[0:16])},
			// Range 1: starts inside the file but extends past EOF.
			// File is 32 bytes; asking for 20 bytes at offset 20 → only 12 available.
			{Offset: 20, Length: 20, ExpectedSHA256: sha256Hex(content[20:32])},
			// Range 2: starts entirely past EOF (offset 100 in a 32-byte file).
			{Offset: 100, Length: 10, ExpectedSHA256: sha256Hex([]byte("irrelevant"))},
		}

		report, err := VerifyRanges(runID, path, ranges)
		if err != nil {
			t.Fatalf("unexpected top-level error: %v", err)
		}

		// RunID echoed.
		if report.RunID != runID {
			t.Errorf("RunID: got %q, want %q", report.RunID, runID)
		}

		if len(report.Results) != 3 {
			t.Fatalf("Results len: got %d, want 3", len(report.Results))
		}

		// Range 0 must pass (in-bounds, correct hash).
		r0 := report.Results[0]
		if r0.Err != nil {
			t.Errorf("range 0: unexpected Err: %v", r0.Err)
		}
		if !r0.Match {
			t.Errorf("range 0: Match=false, want true; computed=%q", r0.ComputedSHA256)
		}

		// Range 1: truncated — MUTATION GUARD assertion.
		// If the equality check is mutated to always-true, this assertion fails.
		r1 := report.Results[1]
		if !errors.Is(r1.Err, ErrRangeTruncated) {
			t.Errorf("range 1: Err: got %v, want ErrRangeTruncated", r1.Err)
		}
		if r1.Match {
			// MUTATION GUARD: a mutated always-true comparison reaches here.
			t.Error("range 1 (truncated): Match=true, want false — " +
				"MUTATION GUARD VIOLATED: hash equality was not checked")
		}

		// Range 2: starts past EOF — also truncated (0 bytes read).
		r2 := report.Results[2]
		if !errors.Is(r2.Err, ErrRangeTruncated) {
			t.Errorf("range 2: Err: got %v, want ErrRangeTruncated", r2.Err)
		}
		if r2.Match {
			t.Error("range 2 (past EOF): Match=true, want false — " +
				"MUTATION GUARD VIOLATED")
		}

		// AllMatched must be false because ranges 1 and 2 failed.
		if report.AllMatched {
			t.Error("AllMatched=true, want false")
		}
	})

	// -----------------------------------------------------------------------
	// Sub-test 3: removed file (was present, then deleted) — also ErrFileMissing
	// -----------------------------------------------------------------------
	t.Run("RemovedFile", func(t *testing.T) {
		path := writeFile(t, dir, "ephemeral.bin", []byte("hello"))

		// Remove the file before calling VerifyRanges.
		if err := os.Remove(path); err != nil {
			t.Fatalf("os.Remove: %v", err)
		}

		_, err := VerifyRanges(runID, path, []Range{
			{Offset: 0, Length: 5, ExpectedSHA256: sha256Hex([]byte("hello"))},
		})
		if !errors.Is(err, ErrFileMissing) {
			t.Fatalf("err: got %v, want ErrFileMissing", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TestVerifyRangesRunIDEchoed validates that the RunID is always present in
// the report, even for zero-range calls, asserting the sink-side log field.
// ---------------------------------------------------------------------------
func TestVerifyRangesRunIDEchoed(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "tiny.bin", []byte{0x01, 0x02, 0x03})

	const runID = "run-echo-003"

	report, err := VerifyRanges(runID, path, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.RunID != runID {
		t.Errorf("RunID: got %q, want %q", report.RunID, runID)
	}
	if len(report.Results) != 0 {
		t.Errorf("Results len: got %d, want 0", len(report.Results))
	}
	if !report.AllMatched {
		t.Error("AllMatched=false for empty range list, want true")
	}
}

// ---------------------------------------------------------------------------
// TestNegativeLengthRange verifies that a Range with Length < 0 is handled
// gracefully: Err is non-nil, Match is false, and processing continues for
// subsequent ranges.
//
// Adversarial-review coverage fix: the negative-length guard (lines 109-115
// of fsresidency.go) had zero test coverage.
// ---------------------------------------------------------------------------
func TestNegativeLengthRange(t *testing.T) {
	dir := t.TempDir()
	content := []byte("negative length test")
	path := writeFile(t, dir, "neg.bin", content)

	const runID = "run-neglength-005"

	ranges := []Range{
		// Range 0: negative length — must error without panic.
		{Offset: 0, Length: -1, ExpectedSHA256: sha256Hex(content)},
		// Range 1: valid range after the bad one — must still be processed.
		{Offset: 0, Length: len(content), ExpectedSHA256: sha256Hex(content)},
	}

	report, err := VerifyRanges(runID, path, ranges)
	if err != nil {
		t.Fatalf("unexpected top-level error: %v", err)
	}
	if report.RunID != runID {
		t.Errorf("RunID: got %q, want %q", report.RunID, runID)
	}
	if len(report.Results) != 2 {
		t.Fatalf("Results len: got %d, want 2", len(report.Results))
	}

	// Range 0: negative length — Err set, Match false.
	r0 := report.Results[0]
	if r0.Err == nil {
		t.Error("range 0 (negative length): Err is nil, want non-nil error")
	}
	if r0.Match {
		t.Error("range 0 (negative length): Match=true, want false")
	}

	// Range 1: valid — must succeed.
	r1 := report.Results[1]
	if r1.Err != nil {
		t.Errorf("range 1: unexpected Err: %v", r1.Err)
	}
	if !r1.Match {
		t.Errorf("range 1: Match=false, want true; computed=%q", r1.ComputedSHA256)
	}

	// AllMatched must be false because range 0 failed.
	if report.AllMatched {
		t.Error("AllMatched=true, want false")
	}
}

// ---------------------------------------------------------------------------
// TestOpenPermissionDeniedError verifies that when os.Open fails with an
// error that is NOT os.IsNotExist (e.g. permission denied), VerifyRanges
// returns the wrapped error (not ErrFileMissing).
//
// Adversarial-review coverage fix: the fmt.Errorf("fsresidency: open …")
// branch at line 100 of fsresidency.go had zero test coverage.
// ---------------------------------------------------------------------------
func TestOpenPermissionDeniedError(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "locked.bin", []byte("secret"))

	// Make the file unreadable.
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("os.Chmod: %v", err)
	}
	// Restore permissions so t.TempDir cleanup can remove the file.
	t.Cleanup(func() { os.Chmod(path, 0o600) })

	const runID = "run-permdeny-006"

	_, err := VerifyRanges(runID, path, []Range{
		{Offset: 0, Length: 6, ExpectedSHA256: sha256Hex([]byte("secret"))},
	})

	// Must return an error.
	if err == nil {
		t.Fatal("expected non-nil error for permission-denied file, got nil")
	}
	// Must NOT be ErrFileMissing (the file exists, it is just unreadable).
	if errors.Is(err, ErrFileMissing) {
		t.Errorf("err is ErrFileMissing, want a wrapped permission-denied error; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestReadAtNonEOFError verifies that a per-range ReadAt error that is
// neither io.EOF nor io.ErrUnexpectedEOF is stored in RangeResult.Err with
// Match=false, and that processing continues for subsequent ranges.
//
// The non-EOF else-branch in VerifyRanges (lines 141-148) is exercised by
// passing a directory as the path: os.Open on a directory succeeds, but
// ReadAt on it returns an "is a directory" error on both Linux and macOS.
//
// Adversarial-review coverage fix: that else-branch had zero test coverage.
// ---------------------------------------------------------------------------
func TestReadAtNonEOFError(t *testing.T) {
	// Use a subdirectory as the "file" path.  os.Open succeeds on a directory;
	// ReadAt then returns a non-EOF system error ("is a directory") which
	// exercises the else-branch in VerifyRanges.
	subdir := filepath.Join(t.TempDir(), "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("os.Mkdir: %v", err)
	}

	const runID = "run-readaterr-007"

	ranges := []Range{
		{Offset: 0, Length: 8, ExpectedSHA256: sha256Hex([]byte("anything"))},
	}

	report, err := VerifyRanges(runID, subdir, ranges)
	// The top-level open succeeds (opening a directory is fine), so no error
	// is returned at that level.
	if err != nil {
		t.Fatalf("unexpected top-level error: %v", err)
	}
	if report.RunID != runID {
		t.Errorf("RunID: got %q, want %q", report.RunID, runID)
	}
	if len(report.Results) != 1 {
		t.Fatalf("Results len: got %d, want 1", len(report.Results))
	}

	r0 := report.Results[0]
	// A non-EOF ReadAt error must be stored in Err.
	if r0.Err == nil {
		t.Error("range 0: Err is nil, want non-nil (non-EOF ReadAt error)")
	}
	// Match must be false.
	if r0.Match {
		t.Error("range 0: Match=true, want false for non-EOF ReadAt error")
	}
	// AllMatched must be false.
	if report.AllMatched {
		t.Error("AllMatched=true, want false")
	}
}

// ---------------------------------------------------------------------------
// TestVerifyRangesHashMismatchReportedCorrectly checks that a deliberately
// wrong ExpectedSHA256 yields Match=false and the computed hash is recorded.
// ---------------------------------------------------------------------------
func TestVerifyRangesHashMismatchReportedCorrectly(t *testing.T) {
	dir := t.TempDir()
	content := []byte("the quick brown fox")
	path := writeFile(t, dir, "fox.bin", content)

	const runID = "run-mismatch-004"

	wrongHash := "badbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadb"
	ranges := []Range{
		{Offset: 0, Length: len(content), ExpectedSHA256: wrongHash},
	}

	report, err := VerifyRanges(runID, path, ranges)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.RunID != runID {
		t.Errorf("RunID: got %q, want %q", report.RunID, runID)
	}
	if report.Results[0].Match {
		t.Error("Match=true for wrong hash, want false")
	}
	// ComputedSHA256 must be the real hash, not the wrong one.
	expectedReal := sha256Hex(content)
	if report.Results[0].ComputedSHA256 != expectedReal {
		t.Errorf("ComputedSHA256: got %q, want %q",
			report.Results[0].ComputedSHA256, expectedReal)
	}
	if report.AllMatched {
		t.Error("AllMatched=true, want false")
	}
}
