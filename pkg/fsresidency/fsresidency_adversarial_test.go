// Adversarial, mutation-verified tests for package fsresidency.
//
// These tests pin the content-integrity contract that a MISSING, TRUNCATED, or
// WRONG-LENGTH file MUST NOT pass verification, and that the offset+length EOF
// boundary is enforced exactly (== filesize included, == filesize+1 rejected).
//
// All I/O is real filesystem I/O against t.TempDir(); this is a filesystem
// package, so real temp files are the correct fixture (not a mock).  No
// wall-clock or random values appear in assertions — file contents are fixed
// byte patterns, so the SHA-256 oracles are deterministic.
package fsresidency

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// emptySHA256Hex is the lower-case hex SHA-256 of the empty byte string.
// Precomputed to act as an independent oracle for the zero-length-range case.
const emptySHA256Hex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// buildPattern returns an n-byte slice where byte[i] = byte(i*7+3), a fixed,
// non-trivial pattern so that adjacent ranges have distinct hashes.
func buildPattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + 3)
	}
	return b
}

// ---------------------------------------------------------------------------
// TestAdversarialPresentVsTruncated (CENTERPIECE)
//
// A full file with all ranges present VERIFIES. The SAME logical ranges against
// a truncated copy (so a range crosses the new EOF) FAIL with ErrRangeTruncated
// and Match=false. This is the core content-integrity guarantee: shrinking the
// file MUST flip the verdict from pass to fail.
// ---------------------------------------------------------------------------
func TestAdversarialPresentVsTruncated(t *testing.T) {
	dir := t.TempDir()

	const fullSize = 200
	full := buildPattern(fullSize)
	fullPath := writeFile(t, dir, "full.bin", full)

	const runID = "adv-present-vs-trunc"

	// Ranges that are entirely in-bounds for the FULL 200-byte file.
	// Range 2 ends at byte 200 (the exact EOF boundary of the full file) and is
	// the one that will cross EOF once the file is truncated to 120 bytes.
	ranges := []Range{
		{Offset: 0, Length: 40, ExpectedSHA256: sha256Hex(full[0:40])},
		{Offset: 50, Length: 30, ExpectedSHA256: sha256Hex(full[50:80])},
		{Offset: 140, Length: 60, ExpectedSHA256: sha256Hex(full[140:200])},
	}

	// --- PRESENT: every range must verify. ---
	rep, err := VerifyRanges(runID, fullPath, ranges)
	if err != nil {
		t.Fatalf("present file: unexpected error: %v", err)
	}
	if !rep.AllMatched {
		t.Fatalf("present file: AllMatched=false, want true; results=%+v", rep.Results)
	}
	for i, res := range rep.Results {
		if res.Err != nil || !res.Match {
			t.Errorf("present range %d: Err=%v Match=%v, want nil/true", i, res.Err, res.Match)
		}
	}

	// --- TRUNCATED: truncate the file to 120 bytes. ---
	// Range 0 (0..40) and range 1 (50..80) are still in-bounds.
	// Range 2 (140..200) now starts past the new EOF (120) -> truncated.
	truncPath := writeFile(t, dir, "trunc.bin", full[:120])

	repT, err := VerifyRanges(runID, truncPath, ranges)
	if err != nil {
		t.Fatalf("truncated file: unexpected top-level error: %v", err)
	}
	if repT.AllMatched {
		t.Fatal("truncated file: AllMatched=true, want false — a range crossing EOF wrongly passed")
	}
	// Ranges 0 and 1 still match (their bytes are unchanged and present).
	if repT.Results[0].Err != nil || !repT.Results[0].Match {
		t.Errorf("truncated range 0: Err=%v Match=%v, want nil/true", repT.Results[0].Err, repT.Results[0].Match)
	}
	if repT.Results[1].Err != nil || !repT.Results[1].Match {
		t.Errorf("truncated range 1: Err=%v Match=%v, want nil/true", repT.Results[1].Err, repT.Results[1].Match)
	}
	// Range 2 MUST fail as truncated.
	if !errors.Is(repT.Results[2].Err, ErrRangeTruncated) {
		t.Errorf("truncated range 2: Err=%v, want ErrRangeTruncated", repT.Results[2].Err)
	}
	if repT.Results[2].Match {
		t.Error("truncated range 2: Match=true, want false — truncated range wrongly verified")
	}
}

// ---------------------------------------------------------------------------
// TestAdversarialWrongLengthFails
//
// A WRONG-LENGTH range (the bytes present but the caller asked for a different
// number of bytes than the expected hash covers) MUST fail. Verification is of
// the exact [Offset, Offset+Length) window, not "some prefix".
// ---------------------------------------------------------------------------
func TestAdversarialWrongLengthFails(t *testing.T) {
	dir := t.TempDir()
	data := buildPattern(64)
	path := writeFile(t, dir, "wronglen.bin", data)

	const runID = "adv-wrong-length"

	// Expected hash is for the first 32 bytes, but we ask to read 33 bytes.
	// The 33-byte window is fully in-bounds, so it reads successfully but the
	// computed hash (of 33 bytes) must NOT equal the 32-byte expected hash.
	ranges := []Range{
		{Offset: 0, Length: 33, ExpectedSHA256: sha256Hex(data[0:32])},
	}
	rep, err := VerifyRanges(runID, path, ranges)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := rep.Results[0]
	if res.Err != nil {
		t.Errorf("wrong-length range: unexpected Err=%v (window is in-bounds)", res.Err)
	}
	if res.Match {
		t.Error("wrong-length range: Match=true, want false — hash covers a different byte count")
	}
	// The computed hash must be the real hash of the 33 bytes actually read.
	if res.ComputedSHA256 != sha256Hex(data[0:33]) {
		t.Errorf("wrong-length range: ComputedSHA256=%q, want hash of 33 bytes %q",
			res.ComputedSHA256, sha256Hex(data[0:33]))
	}
	if rep.AllMatched {
		t.Error("AllMatched=true, want false")
	}
}

// ---------------------------------------------------------------------------
// TestAdversarialOffsetLengthBoundary (CENTERPIECE)
//
// Pins the offset+length vs filesize boundary EXACTLY:
//   - offset+length == filesize      -> included (Match=true)
//   - offset+length == filesize - 1  -> included (Match=true)
//   - offset+length == filesize + 1  -> rejected (ErrRangeTruncated, Match=false)
//   - offset      == filesize        -> zero bytes available; len>0 -> truncated
//
// A relaxed bound (e.g. > mutated to >= so an over-EOF read is accepted) would
// flip the +1 case to a pass and break this test.
// ---------------------------------------------------------------------------
func TestAdversarialOffsetLengthBoundary(t *testing.T) {
	dir := t.TempDir()
	const size = 100
	data := buildPattern(size)
	path := writeFile(t, dir, "boundary.bin", data)

	const runID = "adv-boundary"

	type tc struct {
		name      string
		offset    int64
		length    int
		expected  string
		wantMatch bool
		wantTrunc bool
	}
	cases := []tc{
		{"exact-EOF off40 len60", 40, 60, sha256Hex(data[40:100]), true, false},     // ends at 100 == size
		{"exact-EOF off0 len100", 0, 100, sha256Hex(data[0:100]), true, false},      // whole file
		{"one-before-EOF len99", 0, 99, sha256Hex(data[0:99]), true, false},         // ends at 99
		{"over-by-one off40 len61", 40, 61, sha256Hex(data[40:100]), false, true},   // ends at 101 == size+1
		{"over-by-one off0 len101", 0, 101, sha256Hex(data[0:100]), false, true},    // ends at 101
		{"offset-at-EOF off100 len1", 100, 1, sha256Hex([]byte{0x00}), false, true}, // 0 bytes available
		{"offset-past-EOF off150 len10", 150, 10, "deadbeef", false, true},          // far past EOF
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			rep, err := VerifyRanges(runID, path, []Range{
				{Offset: c.offset, Length: c.length, ExpectedSHA256: c.expected},
			})
			if err != nil {
				t.Fatalf("unexpected top-level error: %v", err)
			}
			res := rep.Results[0]
			if res.Match != c.wantMatch {
				t.Errorf("Match=%v, want %v (off=%d len=%d, filesize=%d); computed=%q",
					res.Match, c.wantMatch, c.offset, c.length, size, res.ComputedSHA256)
			}
			if c.wantTrunc {
				if !errors.Is(res.Err, ErrRangeTruncated) {
					t.Errorf("Err=%v, want ErrRangeTruncated", res.Err)
				}
			} else {
				if res.Err != nil {
					t.Errorf("unexpected Err=%v for in-bounds range", res.Err)
				}
			}
			if rep.AllMatched != c.wantMatch {
				t.Errorf("AllMatched=%v, want %v", rep.AllMatched, c.wantMatch)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestAdversarialZeroLengthRangeContract
//
// Pins the CURRENT contract for a zero-length range. os.File.ReadAt with a
// zero-length buffer returns (0, nil) at ANY offset — even past EOF — so the
// computed hash is the empty-string hash. This test documents that:
//   - a zero-length range whose ExpectedSHA256 is the empty-string hash MATCHES
//     even when the offset is far past EOF (a vacuous pass);
//   - a zero-length range with any OTHER expected hash does NOT match.
//
// This is a SHARP EDGE worth flagging: a zero-length range cannot prove byte
// residency. Callers must not use Length==0 as a presence probe.
// ---------------------------------------------------------------------------
func TestAdversarialZeroLengthRangeContract(t *testing.T) {
	dir := t.TempDir()
	data := buildPattern(16)
	path := writeFile(t, dir, "zero.bin", data)

	// Independent oracle: empty-string SHA-256 constant matches the library hash.
	if got := sha256Hex(nil); got != emptySHA256Hex {
		t.Fatalf("empty-hash oracle mismatch: got %q, want %q", got, emptySHA256Hex)
	}

	const runID = "adv-zerolen"

	// Zero-length range past EOF with the empty hash: current behavior is Match=true.
	repPass, err := VerifyRanges(runID, path, []Range{
		{Offset: 9999, Length: 0, ExpectedSHA256: emptySHA256Hex},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	zr := repPass.Results[0]
	if zr.Err != nil {
		t.Errorf("zero-length past-EOF: unexpected Err=%v", zr.Err)
	}
	if !zr.Match {
		t.Error("zero-length past-EOF with empty hash: Match=false; " +
			"CONTRACT CHANGED — was a documented vacuous pass")
	}
	if zr.ComputedSHA256 != emptySHA256Hex {
		t.Errorf("zero-length: ComputedSHA256=%q, want empty-string hash %q",
			zr.ComputedSHA256, emptySHA256Hex)
	}

	// Zero-length range with a NON-empty expected hash must not match.
	repFail, err := VerifyRanges(runID, path, []Range{
		{Offset: 0, Length: 0, ExpectedSHA256: sha256Hex(data)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repFail.Results[0].Match {
		t.Error("zero-length with non-empty expected hash: Match=true, want false")
	}
}

// ---------------------------------------------------------------------------
// TestAdversarialNegativeOffsetSurfaced
//
// A negative Offset must be surfaced as a per-range error (ReadAt returns a
// "negative offset" error that is NOT io.EOF), never a panic and never a pass.
// This complements the existing negative-LENGTH test.
// ---------------------------------------------------------------------------
func TestAdversarialNegativeOffsetSurfaced(t *testing.T) {
	dir := t.TempDir()
	data := buildPattern(20)
	path := writeFile(t, dir, "negoff.bin", data)

	const runID = "adv-negoffset"

	ranges := []Range{
		// Range 0: negative offset — must error, Match=false, no panic.
		{Offset: -1, Length: 4, ExpectedSHA256: sha256Hex(data[0:4])},
		// Range 1: valid range after the bad one — must still be processed.
		{Offset: 0, Length: 4, ExpectedSHA256: sha256Hex(data[0:4])},
	}
	rep, err := VerifyRanges(runID, path, ranges)
	if err != nil {
		t.Fatalf("unexpected top-level error: %v", err)
	}
	r0 := rep.Results[0]
	if r0.Err == nil {
		t.Error("negative offset: Err is nil, want non-nil error")
	}
	// Must NOT be misclassified as truncation; it is a hard I/O error.
	if errors.Is(r0.Err, ErrRangeTruncated) {
		t.Errorf("negative offset: Err=ErrRangeTruncated, want a raw ReadAt error: %v", r0.Err)
	}
	if r0.Match {
		t.Error("negative offset: Match=true, want false")
	}
	// Subsequent valid range still processed and matches.
	if rep.Results[1].Err != nil || !rep.Results[1].Match {
		t.Errorf("range 1 after bad offset: Err=%v Match=%v, want nil/true",
			rep.Results[1].Err, rep.Results[1].Match)
	}
	if rep.AllMatched {
		t.Error("AllMatched=true, want false")
	}
}

// ---------------------------------------------------------------------------
// TestAdversarialRunIDEchoedOnErrorPaths
//
// The RunID must be faithfully echoed even on the error-return paths
// (ErrFileMissing and the wrapped non-NotExist open error), and for tricky
// values (empty, unicode, embedded NUL/quote). The Path field must echo too.
// ---------------------------------------------------------------------------
func TestAdversarialRunIDEchoedOnErrorPaths(t *testing.T) {
	dir := t.TempDir()

	trickyIDs := []string{
		"",
		"run/with:weird\tchars and spaces",
		"unicode-Ω-π-✓",
		"embedded\x00nul",
		`quote"and'apostrophe`,
		strings.Repeat("x", 1024),
	}

	// Missing-file path: report still carries RunID + Path.
	t.Run("MissingFile", func(t *testing.T) {
		missing := filepath.Join(dir, "nope.bin")
		for _, id := range trickyIDs {
			rep, err := VerifyRanges(id, missing, []Range{{Offset: 0, Length: 1, ExpectedSHA256: "x"}})
			if !errors.Is(err, ErrFileMissing) {
				t.Fatalf("id=%q: err=%v, want ErrFileMissing", id, err)
			}
			if rep.RunID != id {
				t.Errorf("missing-file RunID: got %q, want %q", rep.RunID, id)
			}
			if rep.Path != missing {
				t.Errorf("missing-file Path: got %q, want %q", rep.Path, missing)
			}
		}
	})

	// Permission-denied path (wrapped non-NotExist error): report carries RunID.
	t.Run("PermissionDenied", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: chmod 000 does not deny root reads, so the " +
				"wrapped-open-error path cannot be exercised on this host")
		}
		path := writeFile(t, dir, "locked.bin", []byte("secret"))
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

		const id = "perm-run-id-echo"
		rep, err := VerifyRanges(id, path, []Range{{Offset: 0, Length: 6, ExpectedSHA256: "x"}})
		if err == nil {
			t.Fatal("expected non-nil error for permission-denied file")
		}
		if errors.Is(err, ErrFileMissing) {
			t.Errorf("err=ErrFileMissing, want a wrapped permission error: %v", err)
		}
		if rep.RunID != id {
			t.Errorf("perm-denied RunID: got %q, want %q", rep.RunID, id)
		}
		if rep.Path != path {
			t.Errorf("perm-denied Path: got %q, want %q", rep.Path, path)
		}
	})
}

// ---------------------------------------------------------------------------
// TestAdversarialAllPresentBaseline
//
// A clean all-present baseline so an always-FAIL mutation (e.g. forcing Match
// to false) is caught: every range over a fully-present file must verify.
// ---------------------------------------------------------------------------
func TestAdversarialAllPresentBaseline(t *testing.T) {
	dir := t.TempDir()
	data := buildPattern(512)
	path := writeFile(t, dir, "baseline.bin", data)

	const runID = "adv-baseline"

	var ranges []Range
	// Contiguous tiling of the whole file in 64-byte chunks.
	for off := 0; off < len(data); off += 64 {
		ranges = append(ranges, Range{
			Offset:         int64(off),
			Length:         64,
			ExpectedSHA256: sha256Hex(data[off : off+64]),
		})
	}

	rep, err := VerifyRanges(runID, path, ranges)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.AllMatched {
		t.Fatalf("AllMatched=false, want true (always-fail mutation suspected); results=%+v", rep.Results)
	}
	for i, res := range rep.Results {
		if res.Err != nil || !res.Match {
			t.Errorf("range %d: Err=%v Match=%v, want nil/true", i, res.Err, res.Match)
		}
	}
}

// Compile-time references to keep imports honest if the file is edited down.
var _ = io.EOF
var _ = hex.EncodeToString
var _ = sha256.Sum256
