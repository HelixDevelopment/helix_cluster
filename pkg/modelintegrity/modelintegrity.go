// Package modelintegrity implements a model-integrity verification gate, in the
// style of a HuggingFace cache integrity check, before a model is served.
//
// A model is described by a manifest: a list of expected files, each pinned to
// an exact content digest (SHA-256, lower-case hex) and an exact byte size.
// Before serving, every file on disk is verified against its manifest entry by
// streaming the file through crypto/sha256 and comparing BOTH the resulting
// digest AND the observed byte size to the expected values. Verification fails
// closed: a missing file, a content tamper (same size, different bytes), or a
// length tamper (truncation/extension) each produce a distinct, specific
// failure so the caller can tell WHY a model was rejected.
//
// The package is stdlib-only. crypto/sha256 is the working reference hash: it
// is a real, standards-conformant SHA-256 implementation, so a VERIFY result is
// genuine cryptographic evidence that the on-disk bytes match the pinned digest,
// not a structural rubber-stamp.
package modelintegrity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MismatchKind classifies why a single file failed verification.
type MismatchKind int

const (
	// MismatchNone indicates the file matched both digest and size.
	MismatchNone MismatchKind = iota
	// MismatchMissing indicates the file could not be opened (absent/unreadable).
	MismatchMissing
	// MismatchSize indicates the file's byte size differed from the manifest.
	MismatchSize
	// MismatchSHA indicates the file's SHA-256 digest differed from the manifest
	// while its size matched: a same-length content tamper.
	MismatchSHA
)

// String renders the mismatch kind for diagnostics.
func (k MismatchKind) String() string {
	switch k {
	case MismatchNone:
		return "ok"
	case MismatchMissing:
		return "missing"
	case MismatchSize:
		return "size-mismatch"
	case MismatchSHA:
		return "sha-mismatch"
	default:
		return fmt.Sprintf("MismatchKind(%d)", int(k))
	}
}

// ManifestEntry pins one model file to an exact content digest and byte size.
type ManifestEntry struct {
	// Path is the file location relative to the manifest root directory.
	Path string
	// SHA256 is the expected SHA-256 digest as lower-case hex (64 chars).
	SHA256 string
	// Size is the expected file size in bytes.
	Size int64
}

// FileResult is the verification outcome for a single manifest entry.
type FileResult struct {
	// Path echoes the manifest entry path that was verified.
	Path string
	// OK is true iff the file matched both digest and size.
	OK bool
	// Mismatch classifies the failure; MismatchNone when OK is true.
	Mismatch MismatchKind
	// ExpectedSHA256 is the manifest digest (lower-case hex).
	ExpectedSHA256 string
	// ActualSHA256 is the digest computed from disk (lower-case hex). It is
	// empty when the file was missing.
	ActualSHA256 string
	// ExpectedSize is the manifest size in bytes.
	ExpectedSize int64
	// ActualSize is the size observed on disk; 0 when the file was missing.
	ActualSize int64
	// Detail is a human-readable explanation of a failure; empty when OK.
	Detail string
}

// Report is the aggregate outcome of verifying a whole manifest.
type Report struct {
	// Root is the directory the manifest was verified against.
	Root string
	// Files holds one FileResult per manifest entry, in manifest order.
	Files []FileResult
	// OK is true iff every file in the manifest verified.
	OK bool
}

// FailedPaths returns the relative paths of every file that did not verify,
// in manifest order. It is empty when the report is OK.
func (r Report) FailedPaths() []string {
	var out []string
	for _, f := range r.Files {
		if !f.OK {
			out = append(out, f.Path)
		}
	}
	return out
}

// ErrMissingFile is returned (wrapped) by VerifyFile when the target file does
// not exist or cannot be opened. Callers may test for it with errors.Is.
var ErrMissingFile = errors.New("modelintegrity: file missing")

// ErrSizeMismatch is returned (wrapped) by VerifyFile when the file's byte size
// differs from the expected size.
var ErrSizeMismatch = errors.New("modelintegrity: size mismatch")

// ErrSHAMismatch is returned (wrapped) by VerifyFile when the file's SHA-256
// digest differs from the expected digest while its size matched.
var ErrSHAMismatch = errors.New("modelintegrity: sha256 mismatch")

// ErrBadDigest is returned by VerifyFile when the expected digest is not a
// valid 64-character lower-case hex SHA-256 string.
var ErrBadDigest = errors.New("modelintegrity: malformed expected sha256")

// VerifyFile verifies a single file at path against an expected SHA-256 (given
// as lower-case hex) and an expected byte size. It streams the file through
// crypto/sha256 (constant memory, independent of file size) and compares BOTH
// the digest AND the observed size.
//
// It returns nil iff the file exists, its size equals expectedSize, AND its
// digest equals expectedSHA256hex. Otherwise it returns an error wrapping one
// of ErrMissingFile, ErrSizeMismatch, or ErrSHAMismatch. Size is checked before
// content so a truncation/extension is reported as a size mismatch rather than
// being masked as a digest mismatch.
func VerifyFile(path, expectedSHA256hex string, expectedSize int64) error {
	want, err := normalizeDigest(expectedSHA256hex)
	if err != nil {
		return err
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrMissingFile, path, err)
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		// A read failure mid-stream cannot prove integrity; fail closed.
		return fmt.Errorf("%w: %s: %v", ErrMissingFile, path, err)
	}

	if n != expectedSize {
		return fmt.Errorf("%w: %s: expected %d bytes, got %d",
			ErrSizeMismatch, path, expectedSize, n)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("%w: %s: expected %s, got %s",
			ErrSHAMismatch, path, want, got)
	}
	return nil
}

// VerifyManifest verifies every entry in entries against the files found under
// rootDir, joining each entry's relative path to rootDir. It returns a Report
// with one FileResult per entry (in entry order); Report.OK is true iff every
// entry verified.
//
// The returned error is non-nil only for a usage fault that prevents producing
// a meaningful report (an empty rootDir or a manifest entry with a malformed
// digest); per-file integrity failures are NOT errors — they are recorded in
// the Report so the caller sees the full picture in one pass.
func VerifyManifest(rootDir string, entries []ManifestEntry) (Report, error) {
	if rootDir == "" {
		return Report{}, errors.New("modelintegrity: empty root directory")
	}

	rep := Report{Root: rootDir, OK: true, Files: make([]FileResult, 0, len(entries))}
	for _, e := range entries {
		want, err := normalizeDigest(e.SHA256)
		if err != nil {
			return Report{}, fmt.Errorf("%w (path %q)", err, e.Path)
		}
		rep.Files = append(rep.Files, verifyEntry(rootDir, e, want))
	}
	for i := range rep.Files {
		if !rep.Files[i].OK {
			rep.OK = false
			break
		}
	}
	return rep, nil
}

// verifyEntry resolves and verifies one entry, classifying any mismatch. want
// is the already-normalized expected digest.
func verifyEntry(rootDir string, e ManifestEntry, want string) FileResult {
	res := FileResult{
		Path:           e.Path,
		ExpectedSHA256: want,
		ExpectedSize:   e.Size,
	}
	full := filepath.Join(rootDir, filepath.FromSlash(e.Path))

	f, err := os.Open(full)
	if err != nil {
		res.Mismatch = MismatchMissing
		res.Detail = fmt.Sprintf("cannot open %s: %v", full, err)
		return res
	}
	defer f.Close()

	var h hash.Hash = sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		res.Mismatch = MismatchMissing
		res.Detail = fmt.Sprintf("read error on %s: %v", full, err)
		return res
	}
	res.ActualSize = n
	res.ActualSHA256 = hex.EncodeToString(h.Sum(nil))

	if n != e.Size {
		res.Mismatch = MismatchSize
		res.Detail = fmt.Sprintf("expected %d bytes, got %d", e.Size, n)
		return res
	}
	if res.ActualSHA256 != want {
		res.Mismatch = MismatchSHA
		res.Detail = fmt.Sprintf("expected sha256 %s, got %s", want, res.ActualSHA256)
		return res
	}

	res.OK = true
	res.Mismatch = MismatchNone
	return res
}

// normalizeDigest validates and lower-cases an expected SHA-256 hex digest.
func normalizeDigest(d string) (string, error) {
	d = strings.ToLower(strings.TrimSpace(d))
	if len(d) != 2*sha256.Size {
		return "", fmt.Errorf("%w: want %d hex chars, got %d",
			ErrBadDigest, 2*sha256.Size, len(d))
	}
	if _, err := hex.DecodeString(d); err != nil {
		return "", fmt.Errorf("%w: %v", ErrBadDigest, err)
	}
	return d, nil
}
