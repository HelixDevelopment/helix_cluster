package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestServer starts an httptest server that returns body with HTTP 200.
// The caller must call ts.Close() when done; use t.Cleanup for convenience.
func newTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// runChallengeCapture is a thin wrapper that builds a ChallengeConfig and calls
// RunChallenge, returning (evidencePath, exitCode, stdout, stderr).
func runChallengeCapture(url, expectedSink, evidenceDir, name string, client *http.Client) (string, int, string, string) {
	var out, errb bytes.Buffer
	cfg := ChallengeConfig{
		URL:          url,
		ExpectedSink: expectedSink,
		EvidenceDir:  evidenceDir,
		Name:         name,
		Client:       client,
	}
	path, code := RunChallenge(cfg, &out, &errb)
	return path, code, out.String(), errb.String()
}

// readEvidence reads and unmarshals the evidence JSON file at path.
func readEvidence(t *testing.T, path string) *ChallengeResult {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read evidence file %q: %v", path, err)
	}
	var r ChallengeResult
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal evidence %q: %v", path, err)
	}
	return &r
}

// ---------------------------------------------------------------------------
// HXC-1108 unit tests
// ---------------------------------------------------------------------------

// TestChallengePassWritesEvidenceFile: challenge against an in-process httptest
// server returning the expected body exits 0 and writes an evidence file that
// contains the expected sink line AND a non-empty per-run UUID.
//
// Mutation target: change ExpectedSink to a string NOT in the response body —
// RunChallenge must then return exit 1 and the evidence must mark FAIL.
func TestChallengePassWritesEvidenceFile(t *testing.T) {
	ts := newTestServer(t, "helix-cluster status=OK version=8")
	evidenceDir := t.TempDir()

	evidencePath, code, stdout, _ := runChallengeCapture(
		ts.URL, "status=OK", evidenceDir, "smoke-pass", ts.Client(),
	)

	// Exit code must be 0.
	if code != 0 {
		t.Fatalf("challenge pass: exit code = %d, want 0", code)
	}

	// stdout must report PASS.
	if !strings.Contains(stdout, "PASS") {
		t.Fatalf("challenge pass: stdout missing PASS, got %q", stdout)
	}

	// Evidence file must exist.
	if evidencePath == "" {
		t.Fatal("challenge pass: evidencePath is empty")
	}
	if _, err := os.Stat(evidencePath); err != nil {
		t.Fatalf("challenge pass: evidence file not found: %v", err)
	}

	// Sink-side: decode and assert fields.
	ev := readEvidence(t, evidencePath)

	if ev.Status != "PASS" {
		t.Errorf("evidence status = %q, want PASS", ev.Status)
	}
	// The captured sink line must contain the expected substring.
	if !strings.Contains(ev.SinkLine, "status=OK") {
		t.Errorf("evidence sink_line = %q, does not contain %q", ev.SinkLine, "status=OK")
	}
	// Per-run UUID must be non-empty.
	if ev.RunID == "" {
		t.Error("evidence run_id is empty")
	}
	// UUID must be 36 chars (standard UUID format).
	if len(ev.RunID) != 36 {
		t.Errorf("evidence run_id = %q, expected 36-char UUID", ev.RunID)
	}
	// ExpectedSink must be recorded.
	if ev.ExpectedSink != "status=OK" {
		t.Errorf("evidence expected_sink = %q, want %q", ev.ExpectedSink, "status=OK")
	}
}

// TestChallengeWrongEndpointFails: pointing at a wrong endpoint (body does NOT
// contain the expected sink) must return non-zero and mark the evidence FAIL.
//
// This is the §1.1 mutation target: the wrong-endpoint path MUST fail. If
// RunChallenge were to return 0 unconditionally this test catches it.
func TestChallengeWrongEndpointFails(t *testing.T) {
	// Server returns body that does NOT contain the expected sink.
	ts := newTestServer(t, "error: service unavailable")
	evidenceDir := t.TempDir()

	evidencePath, code, _, stderr := runChallengeCapture(
		ts.URL, "status=OK", evidenceDir, "smoke-fail", ts.Client(),
	)

	// Must exit non-zero.
	if code == 0 {
		t.Fatalf("wrong-endpoint challenge: exit code = 0, want non-zero")
	}

	// stderr must contain FAIL.
	if !strings.Contains(stderr, "FAIL") {
		t.Fatalf("wrong-endpoint challenge: stderr missing FAIL, got %q", stderr)
	}

	// Evidence file must exist and mark FAIL.
	if evidencePath == "" {
		t.Fatal("wrong-endpoint challenge: evidencePath is empty")
	}
	ev := readEvidence(t, evidencePath)

	if ev.Status != "FAIL" {
		t.Errorf("evidence status = %q, want FAIL", ev.Status)
	}
	// SinkLine must be the actual body (evidence of what was received).
	if !strings.Contains(ev.SinkLine, "service unavailable") {
		t.Errorf("evidence sink_line = %q, should contain actual response body", ev.SinkLine)
	}
	if ev.RunID == "" || len(ev.RunID) != 36 {
		t.Errorf("evidence run_id = %q, expected 36-char UUID even on FAIL", ev.RunID)
	}
}

// TestChallengeUnreachableEndpointFails: a connection-refused URL must exit
// non-zero and write a FAIL evidence file.
func TestChallengeUnreachableEndpointFails(t *testing.T) {
	evidenceDir := t.TempDir()
	// Port 1 is effectively always refused on any OS.
	evidencePath, code, _, stderr := runChallengeCapture(
		"http://127.0.0.1:1/no-such-service", "anything", evidenceDir, "unreachable", nil,
	)

	if code == 0 {
		t.Fatalf("unreachable challenge: exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr, "FAIL") {
		t.Fatalf("unreachable challenge: stderr missing FAIL, got %q", stderr)
	}
	if evidencePath == "" {
		t.Fatal("unreachable challenge: evidencePath is empty")
	}
	ev := readEvidence(t, evidencePath)
	if ev.Status != "FAIL" {
		t.Errorf("evidence status = %q, want FAIL", ev.Status)
	}
	if ev.RunID == "" {
		t.Error("evidence run_id is empty on unreachable fail")
	}
}

// TestChallengeEvidenceFileInNamedDir: verify the evidence file ends up in the
// configured dir with a filename that contains the challenge name and run UUID.
func TestChallengeEvidenceFileInNamedDir(t *testing.T) {
	ts := newTestServer(t, "helix:ready")
	evidenceDir := t.TempDir()

	evidencePath, code, _, _ := runChallengeCapture(
		ts.URL, "helix:ready", evidenceDir, "my-challenge", ts.Client(),
	)

	if code != 0 {
		t.Fatalf("expected pass, got exit %d", code)
	}

	// Must reside in the expected dir.
	if filepath.Dir(evidencePath) != evidenceDir {
		t.Errorf("evidence path dir = %q, want %q", filepath.Dir(evidencePath), evidenceDir)
	}

	// Filename must contain the challenge name.
	base := filepath.Base(evidencePath)
	if !strings.Contains(base, "my-challenge") {
		t.Errorf("evidence filename %q does not contain challenge name %q", base, "my-challenge")
	}

	// Filename must end in .json.
	if !strings.HasSuffix(base, ".json") {
		t.Errorf("evidence filename %q must end in .json", base)
	}
}

// TestChalengeCLIDispatch: "helix-test challenge <url> <sink>" uses the main
// CLI dispatch path (via run()), covering the cmdChallenge wiring.
//
// Mutation: removing the "challenge" case from the switch in run() would make
// this test fail with an "Unknown command" error.
func TestChallengeCLIDispatch(t *testing.T) {
	ts := newTestServer(t, "version=1 status=alive")
	t.Setenv("HELIX_TEST_SNAPSHOT_DIR", t.TempDir()) // isolate snapshot env

	var out, errb bytes.Buffer
	code := run(
		[]string{"challenge", ts.URL, "status=alive", t.TempDir(), "cli-dispatch"},
		&out, &errb,
	)
	if code != 0 {
		t.Fatalf("challenge CLI dispatch: exit code = %d, stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "PASS") {
		t.Fatalf("challenge CLI dispatch: stdout missing PASS, got %q", out.String())
	}
}

// TestChallengeUsageMissingArgs: "helix-test challenge" with too few args
// must exit 1 with a usage line.
func TestChallengeUsageMissingArgs(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"challenge"}, &out, &errb)
	if code != 1 {
		t.Fatalf("challenge no-args: exit code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "Usage:") {
		t.Fatalf("challenge no-args: stderr missing usage, got %q", errb.String())
	}
}

// TestChallengeEvidenceContainsRunID: two back-to-back challenges against the
// same endpoint must produce DIFFERENT run UUIDs — proving each run is unique.
//
// Mutation: using a fixed constant for RunID instead of uuid.New().String()
// would make both IDs identical and fail this assertion.
func TestChallengeEvidenceUniqueRunIDs(t *testing.T) {
	ts := newTestServer(t, "ok")
	dir := t.TempDir()

	p1, _, _, _ := runChallengeCapture(ts.URL, "ok", dir, "r1", ts.Client())
	p2, _, _, _ := runChallengeCapture(ts.URL, "ok", dir, "r2", ts.Client())

	ev1 := readEvidence(t, p1)
	ev2 := readEvidence(t, p2)

	if ev1.RunID == ev2.RunID {
		t.Errorf("two consecutive challenge runs have the same run_id %q; must be unique", ev1.RunID)
	}
}
