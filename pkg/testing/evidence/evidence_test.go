package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Per Constitution §7.1, this package IS the anti-bluff substrate, so its own
// tests must (a) prove the happy path produces real evidence and (b) prove the
// gate FAILS on every contract violation (the §1.1 false-positive-immunity /
// mutation pairing — the assertions below are the mutants).

func TestToken_UniquePerRecorder(t *testing.T) {
	dir := t.TempDir()
	a, err := New("feat-a", dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b, err := New("feat-b", dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Token() == "" || b.Token() == "" {
		t.Fatal("token must be non-empty")
	}
	if a.Token() == b.Token() {
		t.Fatalf("tokens must be unique per run: %q == %q", a.Token(), b.Token())
	}
	if len(a.Token()) < 16 {
		t.Fatalf("token too short to defeat collisions: %q", a.Token())
	}
}

func TestProveDelta(t *testing.T) {
	e, _ := New("delta", t.TempDir())
	// happy path: before != after -> nil error, recorded
	if err := e.ProveDelta("state changed", "before", "after"); err != nil {
		t.Fatalf("ProveDelta on real change must pass: %v", err)
	}
	// MUTATION: before == after is a bluff -> MUST error
	if err := e.ProveDelta("no change", "same", "same"); err == nil {
		t.Fatal("ProveDelta MUST fail when before==after (bluff), got nil")
	}
}

func TestProvePositive(t *testing.T) {
	e, _ := New("positive", t.TempDir())
	tok := e.Token()
	// happy path: token embedded in resulting state -> nil
	if err := e.ProvePositive("token echoed", "result contains "+tok+" ok", tok); err != nil {
		t.Fatalf("ProvePositive must pass when needle present: %v", err)
	}
	// MUTATION: needle absent -> MUST error (positive evidence, not absence-of-error)
	if err := e.ProvePositive("token missing", "unrelated output", tok); err == nil {
		t.Fatal("ProvePositive MUST fail when needle absent, got nil")
	}
	// MUTATION: empty haystack (probe returned nothing) -> MUST error, never pass
	if err := e.ProvePositive("empty probe", "", tok); err == nil {
		t.Fatal("ProvePositive MUST fail on empty result (probe returned nothing != SKIP)")
	}
	// MUTATION: empty needle is meaningless evidence -> MUST error
	if err := e.ProvePositive("empty needle", "anything", ""); err == nil {
		t.Fatal("ProvePositive MUST fail on empty needle")
	}
}

func TestArtifact_WritesRealFile(t *testing.T) {
	dir := t.TempDir()
	e, _ := New("artifact", dir)
	p, err := e.Artifact("capture.log", []byte("line with "+e.Token()))
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("artifact file unreadable: %v", err)
	}
	if !strings.Contains(string(got), e.Token()) {
		t.Fatalf("artifact must contain the run token")
	}
}

func TestManifest_CapturesAllRecordsAsSinkEvidence(t *testing.T) {
	dir := t.TempDir()
	e, _ := New("manifest", dir)
	_ = e.ProveDelta("d", 1, 2)
	_ = e.ProvePositive("p", "has "+e.Token(), e.Token())
	mp, err := e.Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	raw, err := os.ReadFile(mp)
	if err != nil {
		t.Fatalf("manifest unreadable: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest must be valid JSON: %v", err)
	}
	if m["run_id"] != e.Token() {
		t.Fatalf("manifest run_id must equal token: %v", m["run_id"])
	}
	recs, ok := m["records"].([]any)
	if !ok || len(recs) != 2 {
		t.Fatalf("manifest must record all 2 proofs, got %v", m["records"])
	}
	if !strings.HasPrefix(mp, filepath.Join(dir, "manifest")) {
		t.Fatalf("manifest must live under the run dir, got %q", mp)
	}
}

func TestNew_CreatesRunDir(t *testing.T) {
	dir := t.TempDir()
	e, err := New("dircheck", dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := os.Stat(e.Dir())
	if err != nil || !info.IsDir() {
		t.Fatalf("run dir must exist: %v", err)
	}
	if !strings.Contains(e.Dir(), "dircheck") || !strings.Contains(e.Dir(), e.Token()) {
		t.Fatalf("run dir must encode feature + run id, got %q", e.Dir())
	}
}
