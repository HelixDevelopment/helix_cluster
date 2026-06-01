// Package evidence implements the Constitution §7.1 positive-evidence contract
// as a reusable, project-agnostic Go testing helper. It exists so that every
// non-unit test can prove a feature actually works for end users instead of
// merely not panicking (the "PASS-bluff" the CLAUDE-1 / §11.4 covenant forbids).
//
// The five §7.1 requirements it mechanizes:
//  1. Real ACTION       — the caller invokes a real, user-visible action.
//  2. State DELTA       — ProveDelta fails unless before != after.
//  3. POSITIVE evidence — ProvePositive asserts a value is PRESENT (never
//     absence-of-error); an empty probe result is a FAIL, not a pass/skip.
//  4. UNIQUE token      — Token() is a per-run id to embed in the action and
//     look for in the resulting state, defeating stale-cache false passes.
//  5. Captured evidence — Artifact/Manifest write real sink-side files under
//     <base>/<feature>/<run-id>/ for forensic inspection.
//
// It is decoupled (no project-specific paths): the evidence base dir is passed
// in or taken from QA_RESULTS_DIR (default "qa-results").
package evidence

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Record is one proven assertion, persisted in the manifest as sink evidence.
type Record struct {
	Label   string `json:"label"`
	Kind    string `json:"kind"` // "delta" | "positive" | "artifact"
	Detail  string `json:"detail"`
	Passed  bool   `json:"passed"`
	AtMicro int64  `json:"at_unix_micro"`
}

// Evidence captures the proofs for one test run.
type Evidence struct {
	feature string
	runID   string
	dir     string
	records []Record
}

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is catastrophic for the anti-bluff token; surface it.
		panic("evidence: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// New creates an Evidence recorder and its run directory under baseDir
// (or QA_RESULTS_DIR / "qa-results" when baseDir is empty).
func New(feature, baseDir string) (*Evidence, error) {
	if feature == "" {
		return nil, fmt.Errorf("evidence: feature must be non-empty")
	}
	if baseDir == "" {
		baseDir = os.Getenv("QA_RESULTS_DIR")
		if baseDir == "" {
			baseDir = "qa-results"
		}
	}
	id := newToken()
	dir := filepath.Join(baseDir, feature, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("evidence: mkdir run dir: %w", err)
	}
	return &Evidence{feature: feature, runID: id, dir: dir}, nil
}

// Token returns the per-run unique evidence token (§7.1 #4).
func (e *Evidence) Token() string { return e.runID }

// Dir returns the run's evidence directory.
func (e *Evidence) Dir() string { return e.dir }

func (e *Evidence) record(kind, label, detail string, passed bool) {
	e.records = append(e.records, Record{
		Label: label, Kind: kind, Detail: detail, Passed: passed,
		AtMicro: time.Now().UnixMicro(),
	})
}

// ProveDelta returns an error unless before and after differ (§7.1 #2):
// before == after on a "successful" action is a bluff.
func (e *Evidence) ProveDelta(label string, before, after any) error {
	if reflect.DeepEqual(before, after) {
		err := fmt.Errorf("evidence[%s]: no state delta — before==after (%v); this is a bluff", label, before)
		e.record("delta", label, err.Error(), false)
		return err
	}
	e.record("delta", label, fmt.Sprintf("before=%v after=%v", before, after), true)
	return nil
}

// ProvePositive returns an error unless needle is non-empty and present in
// haystack (§7.1 #3): positive evidence, never absence-of-error, and an empty
// probe result (empty haystack) is a FAIL — not a skip.
func (e *Evidence) ProvePositive(label, haystack, needle string) error {
	if needle == "" {
		err := fmt.Errorf("evidence[%s]: empty needle is meaningless evidence", label)
		e.record("positive", label, err.Error(), false)
		return err
	}
	if haystack == "" {
		err := fmt.Errorf("evidence[%s]: probe returned nothing (empty result is a FAIL, not a SKIP)", label)
		e.record("positive", label, err.Error(), false)
		return err
	}
	if !contains(haystack, needle) {
		err := fmt.Errorf("evidence[%s]: positive evidence %q not found in result", label, needle)
		e.record("positive", label, err.Error(), false)
		return err
	}
	e.record("positive", label, fmt.Sprintf("found %q", needle), true)
	return nil
}

// Artifact writes a captured-evidence file into the run directory and returns
// its path (§7.1 #5).
func (e *Evidence) Artifact(name string, data []byte) (string, error) {
	p := filepath.Join(e.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", fmt.Errorf("evidence: artifact mkdir: %w", err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", fmt.Errorf("evidence: write artifact: %w", err)
	}
	e.record("artifact", name, fmt.Sprintf("%d bytes", len(data)), true)
	return p, nil
}

// Manifest writes manifest.json (run id, feature, all records) and returns its
// path. It is the discoverable forensic record of the run.
func (e *Evidence) Manifest() (string, error) {
	p := filepath.Join(e.dir, "manifest.json")
	m := map[string]any{
		"run_id":   e.runID,
		"feature":  e.feature,
		"records":  e.records,
		"at_micro": time.Now().UnixMicro(),
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", fmt.Errorf("evidence: marshal manifest: %w", err)
	}
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		return "", fmt.Errorf("evidence: write manifest: %w", err)
	}
	return p, nil
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --- testing.TB convenience wrappers (fail the test on contract violation) ---

// NewT creates an Evidence recorder bound to a test: it fails the test on setup
// error and writes the manifest on cleanup.
func NewT(tb testing.TB, feature, baseDir string) *Evidence {
	tb.Helper()
	e, err := New(feature, baseDir)
	if err != nil {
		tb.Fatalf("evidence.NewT: %v", err)
	}
	tb.Cleanup(func() { _, _ = e.Manifest() })
	return e
}

// MustDelta fails the test unless before != after.
func (e *Evidence) MustDelta(tb testing.TB, label string, before, after any) {
	tb.Helper()
	if err := e.ProveDelta(label, before, after); err != nil {
		tb.Fatal(err)
	}
}

// MustPositive fails the test unless needle is present in haystack.
func (e *Evidence) MustPositive(tb testing.TB, label, haystack, needle string) {
	tb.Helper()
	if err := e.ProvePositive(label, haystack, needle); err != nil {
		tb.Fatal(err)
	}
}
