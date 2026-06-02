// Package fsresidency proves that a model file is really present on disk
// by reading byte ranges and verifying their SHA-256 hashes.
//
// Design notes:
//   - All I/O is performed via os.File.ReadAt so the file must be genuinely
//     present on the host filesystem; there is no in-memory substitution path.
//   - The hash-equality comparison (marked MUTATION GUARD below) is the single
//     line whose forced mutation to always-true would make
//     TestMissingOrTruncatedFileFails fail, because a range past EOF that
//     produces no bytes would get an incorrect hash but would be reported as
//     matched.
package fsresidency

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrFileMissing is returned when the target file cannot be opened.
var ErrFileMissing = errors.New("fsresidency: file not found or not accessible")

// ErrRangeTruncated is returned when a range extends past the end of the file,
// meaning fewer bytes than requested could be read.
var ErrRangeTruncated = errors.New("fsresidency: range extends past end of file")

// Range describes one byte range to verify.
type Range struct {
	// Offset is the byte position within the file where reading begins (0-based).
	Offset int64
	// Length is the exact number of bytes to read.
	Length int
	// ExpectedSHA256 is the lower-case hex-encoded SHA-256 of the bytes at
	// [Offset, Offset+Length). An empty string is treated as "no expectation"
	// and will never match a computed hash (match=false).
	ExpectedSHA256 string
}

// RangeResult holds the outcome for a single Range verification.
type RangeResult struct {
	// Range is the original range specification.
	Range Range
	// ComputedSHA256 is the hex-encoded SHA-256 of the bytes that were
	// actually read.  It is empty when the file could not be read at all.
	ComputedSHA256 string
	// Match is true iff the file is present, the full Length bytes were read,
	// and ComputedSHA256 == Range.ExpectedSHA256.
	//
	// MUTATION GUARD — pinned by TestMissingOrTruncatedFileFails:
	// forcing this assignment to always `true` breaks that test because a
	// truncated range (EOF before Length bytes) would wrongly report match=true.
	Match bool
	// Err is non-nil for a hard I/O error on this range (distinct from a hash
	// mismatch, which leaves Err nil but sets Match=false).
	Err error
}

// Report is the top-level result returned by VerifyRanges.
type Report struct {
	// RunID echoes the caller-supplied run identifier for correlation.
	RunID string
	// Path is the file that was verified.
	Path string
	// Results contains one RangeResult per input Range, in the same order.
	Results []RangeResult
	// AllMatched is true iff every range was successfully read and its hash
	// equalled the expected value.
	AllMatched bool
}

// VerifyRanges opens path, reads each Range at its stated Offset/Length, and
// compares the SHA-256 of the bytes to Range.ExpectedSHA256.
//
// Error behaviour:
//   - If the file cannot be opened, VerifyRanges returns (Report{}, ErrFileMissing).
//     The returned Report still carries the RunID and Path.
//   - If a range's read returns io.EOF or io.ErrUnexpectedEOF (fewer bytes than
//     requested), the RangeResult.Err is set to ErrRangeTruncated and Match is
//     false; processing continues with the remaining ranges.
//   - Other per-range I/O errors are stored in RangeResult.Err with Match=false;
//     they do not abort the loop.
//
// runID is an opaque correlation string chosen by the caller; it is recorded
// verbatim in the returned Report.
func VerifyRanges(runID string, path string, ranges []Range) (Report, error) {
	report := Report{
		RunID:   runID,
		Path:    path,
		Results: make([]RangeResult, len(ranges)),
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return report, ErrFileMissing
		}
		return report, fmt.Errorf("fsresidency: open %q: %w", path, err)
	}
	defer f.Close()

	allMatched := true

	for i, r := range ranges {
		result := RangeResult{Range: r}

		if r.Length < 0 {
			result.Err = fmt.Errorf("fsresidency: range %d: negative length %d", i, r.Length)
			result.Match = false
			allMatched = false
			report.Results[i] = result
			continue
		}

		buf := make([]byte, r.Length)
		n, readErr := f.ReadAt(buf, r.Offset)

		if readErr != nil {
			// io.EOF from ReadAt: the OS spec says io.EOF is returned when the
			// read reaches or passes the end of the file.
			//   - n == r.Length && err == io.EOF  → exact boundary hit, valid.
			//   - n < r.Length  && err == io.EOF  → truncated (fewer bytes than
			//     requested were available).  macOS returns io.EOF here; Linux
			//     typically returns io.ErrUnexpectedEOF.  Both cases are
			//     detected by the n < r.Length guard below.
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
				if n < r.Length {
					// Truncated: compute hash of what we did get, but force Match=false.
					buf = buf[:n]
					h := sha256.Sum256(buf)
					result.ComputedSHA256 = hex.EncodeToString(h[:])
					result.Err = ErrRangeTruncated
					result.Match = false // MUTATION GUARD — pinned by TestMissingOrTruncatedFileFails
					allMatched = false
					report.Results[i] = result
					continue
				}
				// n == r.Length: valid exact-boundary read; fall through to hash.
			} else {
				// Some other I/O error.
				result.Err = fmt.Errorf("fsresidency: range %d ReadAt: %w", i, readErr)
				result.Match = false
				allMatched = false
				report.Results[i] = result
				continue
			}
		}

		// All r.Length bytes were read successfully.
		// Slice to n defensively (ReadAt guarantees n <= r.Length).
		buf = buf[:n]

		h := sha256.Sum256(buf)
		computed := hex.EncodeToString(h[:])
		result.ComputedSHA256 = computed

		// MUTATION GUARD — pinned by TestMissingOrTruncatedFileFails:
		// This comparison MUST be an equality check. Mutating it to `true`
		// would make a truncated/mismatching range wrongly report Match=true,
		// causing TestMissingOrTruncatedFileFails to fail.
		result.Match = (computed == r.ExpectedSHA256)

		if !result.Match {
			allMatched = false
		}
		report.Results[i] = result
	}

	report.AllMatched = allMatched
	return report, nil
}
