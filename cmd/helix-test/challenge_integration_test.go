//go:build integration

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestChallengeIntegration_RealLocalhost runs the challenge runner against an
// httptest server bound to a REAL localhost TCP port (not the loopback-only
// UnstartedServer), exercising the full OS network stack.
//
// This is the integration tier for HXC-1108. It HARD-FAILS if the runner does
// not write an evidence file containing the expected sink line and a per-run UUID.
// t.Skip is NOT used here because the dependency (real TCP loopback) is always
// present on any supported OS.
func TestChallengeIntegration_RealLocalhost(t *testing.T) {
	const expectedBody = "helix-integration-probe-response"
	const challengeName = "integration-smoke"

	// Bind to an OS-assigned port on real localhost.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(expectedBody))
	}))
	ts.Listener = listener
	ts.Start()
	defer ts.Close()

	evidenceDir := t.TempDir()
	cfg := ChallengeConfig{
		Name:         challengeName,
		URL:          ts.URL,
		ExpectedSink: expectedBody,
		EvidenceDir:  evidenceDir,
		Client:       ts.Client(),
	}
	// ts.Client() is correct: httptest.Server.Client() returns *http.Client.

	var out, errb strings.Builder
	type writerFn struct{ fn func([]byte) (int, error) }
	evidencePath, code := RunChallenge(cfg, &writerAdapter{&out}, &writerAdapter{&errb})

	// Hard-fail on any non-pass.
	if code != 0 {
		t.Fatalf("integration challenge FAIL: exit=%d stdout=%q stderr=%q", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "PASS") {
		t.Fatalf("integration challenge: stdout missing PASS, got %q", out.String())
	}

	// Assert evidence file exists.
	if evidencePath == "" {
		t.Fatal("integration challenge: evidencePath is empty")
	}
	if _, err := os.Stat(evidencePath); err != nil {
		t.Fatalf("integration challenge: evidence file missing: %v", err)
	}

	// Decode and assert sink-side facts.
	raw, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatalf("integration challenge: read evidence: %v", err)
	}
	var ev ChallengeResult
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("integration challenge: unmarshal evidence: %v", err)
	}

	// Status must be PASS.
	if ev.Status != "PASS" {
		t.Errorf("integration evidence status = %q, want PASS", ev.Status)
	}
	// SinkLine must contain the expected body (the captured probe response).
	if !strings.Contains(ev.SinkLine, expectedBody) {
		t.Errorf("integration evidence sink_line = %q, must contain %q", ev.SinkLine, expectedBody)
	}
	// Per-run UUID must be present and valid (36 chars).
	if len(ev.RunID) != 36 {
		t.Errorf("integration evidence run_id = %q, expected 36-char UUID", ev.RunID)
	}
	// HTTP status must be 200.
	if ev.HTTPStatus != http.StatusOK {
		t.Errorf("integration evidence http_status = %d, want 200", ev.HTTPStatus)
	}
	// Challenge name must be reflected.
	if ev.ChallengeName != challengeName {
		t.Errorf("integration evidence challenge_name = %q, want %q", ev.ChallengeName, challengeName)
	}
}

// writerAdapter adapts a *strings.Builder to io.Writer for use with RunChallenge.
type writerAdapter struct{ sb *strings.Builder }

func (w *writerAdapter) Write(p []byte) (int, error) {
	return w.sb.Write(p)
}
