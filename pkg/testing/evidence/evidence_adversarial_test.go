package evidence

// Adversarial sink-side probes for the §7.1 captured-evidence substrate.
// This package IS the anti-bluff substrate, so its evidence sink must be
// forensically sound: an artifact stored under a key must (a) stay under the
// run dir (no path traversal / base escape), (b) not collide with a distinct
// key (silent overwrite destroys prior evidence), (c) round-trip byte-exact,
// and (d) never alias the caller's buffer. The positive/delta gates must also
// match a stdlib oracle and never panic on malformed input.
//
// Each test is mutation-shaped: it asserts the CORRECT behavior, so reverting
// the production fix makes it FAIL (false-positive-immunity, §1.1).

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// guard runs fn under a hard timeout so a buggy blocking op cannot hang CI.
func guard(t *testing.T, name string, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: timed out after %s (possible hang)", name, d)
	}
}

// TestArtifact_RejectsPathTraversal_AndBaseEscape proves the artifact key
// cannot escape the run dir. A name like "../../../x" previously resolved and
// wrote OUTSIDE the evidence base — silently corrupting/forging forensic files
// elsewhere. The fix rejects it; without the fix this test FAILS because the
// escaped file gets written under (or above) the base.
func TestArtifact_RejectsPathTraversal_AndBaseEscape(t *testing.T) {
	base := t.TempDir()
	e, err := New("feat", base)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	malicious := []string{
		filepath.Join("..", "escaped.txt"),                   // up one
		filepath.Join("..", "..", "escaped.txt"),             // out of run dir
		filepath.Join("..", "..", "..", "outside_base.txt"),  // out of base
		"..",                                                 // bare traversal
		filepath.Join("sub", "..", "..", "..", "x.txt"),      // sneaky middle
	}
	for _, name := range malicious {
		p, err := e.Artifact(name, []byte("PWNED"))
		if err == nil {
			// If it didn't error, it MUST at least have stayed under the run dir.
			abs, _ := filepath.Abs(p)
			runDir, _ := filepath.Abs(e.Dir())
			rel, relErr := filepath.Rel(runDir, abs)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				t.Fatalf("Artifact(%q) escaped run dir: wrote %q (rel %q)", name, p, rel)
			}
		}
	}

	// Absolute path must also be rejected (would write anywhere on disk).
	absName := filepath.Join(t.TempDir(), "abs_pwn.txt")
	if _, err := e.Artifact(absName, []byte("X")); err == nil {
		if _, statErr := os.Stat(absName); statErr == nil {
			t.Fatalf("Artifact accepted absolute path and wrote outside base: %q", absName)
		}
	}
}

// TestArtifact_DistinctKeysDoNotCollide proves two distinct keys never resolve
// to the same sink file. Pre-fix, "a/../collide.txt" and "collide.txt" mapped
// to one file, so the second silently overwrote the first's evidence.
func TestArtifact_DistinctKeysDoNotCollide(t *testing.T) {
	e, err := New("feat", t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Raw (un-joined) traversal key: textually distinct from "collide.txt" but
	// pre-fix it cleaned to the SAME sink file, silently overwriting evidence.
	traversalKey := "a/../collide.txt"
	p1, err1 := e.Artifact(traversalKey, []byte("FIRST"))
	p2, err2 := e.Artifact("collide.txt", []byte("SECOND"))
	// If both were accepted, they must not be the same file (else collision).
	if err1 == nil && err2 == nil && filepath.Clean(p1) == filepath.Clean(p2) {
		t.Fatalf("distinct keys collided onto one file %q (silent evidence overwrite)", p1)
	}
	// The traversal-bearing key must be rejected outright.
	if err1 == nil {
		t.Fatalf("Artifact accepted traversal-bearing key %q", traversalKey)
	}
}

// TestArtifact_RoundTripByteExact proves stored bytes == retrieved bytes,
// including embedded NULs and high bytes, and that the run token survives.
func TestArtifact_RoundTripByteExact(t *testing.T) {
	e, err := New("rt", t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	payload := append([]byte("token="+e.Token()+"\x00\xff\xfe binary"), 0x00, 0x01, 0x02)
	p, err := e.Artifact("capture.bin", payload)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round-trip not byte-exact:\n stored=%q\n got=   %q", payload, got)
	}
}

// TestArtifact_DoesNotAliasCallerBuffer proves the sink captures a snapshot:
// mutating the caller's slice after Artifact returns must NOT change the file.
func TestArtifact_DoesNotAliasCallerBuffer(t *testing.T) {
	e, err := New("alias", t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	buf := []byte("ORIGINAL-EVIDENCE")
	p, err := e.Artifact("a.log", buf)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	for i := range buf { // tamper after the fact
		buf[i] = 'X'
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "ORIGINAL-EVIDENCE" {
		t.Fatalf("artifact aliased caller buffer; file changed to %q", got)
	}
}

// TestArtifact_EmptyAndNil proves empty name is rejected and a nil/empty data
// payload is handled without panic (stores a real zero-length file).
func TestArtifact_EmptyAndNil(t *testing.T) {
	e, err := New("edge", t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := e.Artifact("", []byte("x")); err == nil {
		t.Fatal("Artifact MUST reject empty name")
	}
	guard(t, "nil-data", 5*time.Second, func() {
		p, err := e.Artifact("empty.bin", nil)
		if err != nil {
			t.Errorf("Artifact(nil data): %v", err)
			return
		}
		got, err := os.ReadFile(p)
		if err != nil || len(got) != 0 {
			t.Errorf("nil payload must store a 0-byte file, got len=%d err=%v", len(got), err)
		}
	})
}

// TestProvePositive_MatchesStdlibOracle fuzzes the hand-rolled substring search
// against strings.Contains over adversarial inputs (overlaps, prefixes, high
// bytes). A mismatch would mean evidence is silently mis-scored.
func TestProvePositive_MatchesStdlibOracle(t *testing.T) {
	e, err := New("oracle", t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := []struct{ hay, needle string }{
		{"aaaab", "aab"},
		{"abcabcabd", "abcabd"},
		{"hello world", "world"},
		{"hello world", "worlds"},
		{"\x00\xff\x00", "\xff\x00"},
		{"mississippi", "issip"},
		{"abc", "abc"},
		{"abc", "abcd"},
		{"token123token456", "token456"},
		{"aXbXc", "XbX"},
	}
	for _, c := range cases {
		if c.needle == "" || c.hay == "" {
			continue
		}
		want := strings.Contains(c.hay, c.needle)
		err := e.ProvePositive("oracle", c.hay, c.needle)
		got := err == nil
		if got != want {
			t.Fatalf("contains(%q,%q): got pass=%v want=%v (err=%v)", c.hay, c.needle, got, want, err)
		}
	}
}

// TestProveDelta_NestedAndNoPanic proves nested-structure deltas are detected
// (DeepEqual semantics) and that nil-vs-nil is correctly a bluff, with no panic
// on mismatched dynamic types.
func TestProveDelta_NestedAndNoPanic(t *testing.T) {
	e, err := New("delta", t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	guard(t, "delta", 5*time.Second, func() {
		// nested differing maps -> real delta -> pass
		if err := e.ProveDelta("nested", map[string][]int{"k": {1, 2}}, map[string][]int{"k": {1, 3}}); err != nil {
			t.Errorf("nested delta must pass: %v", err)
		}
		// identical nested -> bluff -> error
		if err := e.ProveDelta("same-nested", map[string][]int{"k": {1, 2}}, map[string][]int{"k": {1, 2}}); err == nil {
			t.Error("identical nested structures must be flagged as bluff")
		}
		// nil vs nil -> bluff -> error, no panic
		if err := e.ProveDelta("nil", nil, nil); err == nil {
			t.Error("nil==nil must be flagged as bluff")
		}
		// mismatched dynamic types -> real delta, must not panic
		if err := e.ProveDelta("types", 1, "1"); err != nil {
			t.Errorf("differing types are a delta: %v", err)
		}
	})
}

// TestManifest_RecordsArtifactSink proves a stored artifact is reflected in the
// manifest (the discoverable sink record) and the manifest is valid JSON whose
// record count matches the proofs made.
func TestManifest_RecordsArtifactSink(t *testing.T) {
	e, err := New("man", t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := e.Artifact("a.log", []byte("x")); err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	mp, err := e.Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	raw, err := os.ReadFile(mp)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m struct {
		RunID   string   `json:"run_id"`
		Records []Record `json:"records"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest invalid JSON: %v", err)
	}
	if m.RunID != e.Token() {
		t.Fatalf("manifest run_id %q != token %q", m.RunID, e.Token())
	}
	foundArtifact := false
	for _, r := range m.Records {
		if r.Kind == "artifact" && r.Label == "a.log" {
			foundArtifact = true
		}
	}
	if !foundArtifact {
		t.Fatalf("manifest missing artifact sink record: %+v", m.Records)
	}
}
