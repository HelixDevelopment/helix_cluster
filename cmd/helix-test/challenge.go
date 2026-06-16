// Challenge runner for HXC-1108: HelixQA live-service Challenge runner.
//
// A Challenge probes an HTTP endpoint, asserts the response body contains an
// expected sink line, writes an evidence file (JSON) that includes the captured
// sink line and a per-run UUID, then exits 0 on pass / non-zero on fail.
//
// The Dialer seam lets tests inject an in-process httptest server instead of a
// real remote service. The exported RunChallenge function is the unit of work;
// cmdChallenge wires it to the CLI.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ChallengeConfig describes a single HelixQA challenge probe.
type ChallengeConfig struct {
	// Name is a human-readable label written to the evidence file.
	Name string
	// URL is the HTTP endpoint to probe.
	URL string
	// ExpectedSink is a substring that must appear in the response body for PASS.
	ExpectedSink string
	// EvidenceDir is the directory where the evidence file is written.
	EvidenceDir string
	// Client is an optional custom HTTP client (nil => http.DefaultClient).
	Client *http.Client
}

// ChallengeResult is written to the evidence file as JSON.
type ChallengeResult struct {
	RunID         string    `json:"run_id"`
	ChallengeName string    `json:"challenge_name"`
	URL           string    `json:"url"`
	Status        string    `json:"status"`    // "PASS" or "FAIL"
	SinkLine      string    `json:"sink_line"` // captured response body (first 4096 bytes)
	ExpectedSink  string    `json:"expected_sink"`
	HTTPStatus    int       `json:"http_status"`
	FailReason    string    `json:"fail_reason,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
}

// RunChallenge executes the challenge, writes the evidence file, and returns
// (evidencePath, exit-code). An exit code of 0 means PASS.
//
// The function never calls os.Exit; the caller is responsible for that.
func RunChallenge(cfg ChallengeConfig, stdout, stderr io.Writer) (string, int) {
	runID := uuid.New().String()
	now := time.Now().UTC()

	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}

	result := &ChallengeResult{
		RunID:         runID,
		ChallengeName: cfg.Name,
		URL:           cfg.URL,
		ExpectedSink:  cfg.ExpectedSink,
		Timestamp:     now,
	}

	// Perform the HTTP probe.
	resp, err := client.Get(cfg.URL)
	if err != nil {
		result.Status = "FAIL"
		result.FailReason = fmt.Sprintf("http get error: %v", err)
		evidencePath := writeEvidence(cfg, result, stderr)
		fmt.Fprintf(stderr, "FAIL [%s] %s: %s\n", runID, cfg.Name, result.FailReason)
		return evidencePath, 1
	}
	defer resp.Body.Close()

	result.HTTPStatus = resp.StatusCode

	// Read body (cap at 4096 bytes to keep evidence manageable).
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		result.Status = "FAIL"
		result.FailReason = fmt.Sprintf("read body error: %v", err)
		evidencePath := writeEvidence(cfg, result, stderr)
		fmt.Fprintf(stderr, "FAIL [%s] %s: %s\n", runID, cfg.Name, result.FailReason)
		return evidencePath, 1
	}

	body := string(bodyBytes)
	result.SinkLine = body

	// Sink-side assertion: body must contain the expected sink line.
	if !strings.Contains(body, cfg.ExpectedSink) {
		result.Status = "FAIL"
		result.FailReason = fmt.Sprintf(
			"sink assertion failed: expected %q in response body, got %q (http_status=%d)",
			cfg.ExpectedSink, body, resp.StatusCode,
		)
		evidencePath := writeEvidence(cfg, result, stderr)
		fmt.Fprintf(stderr, "FAIL [%s] %s: %s\n", runID, cfg.Name, result.FailReason)
		return evidencePath, 1
	}

	result.Status = "PASS"
	evidencePath := writeEvidence(cfg, result, stderr)
	if evidencePath == "" {
		// writeEvidence returns "" when capturing the evidence file failed. A
		// Challenge PASS without sink-side evidence is a CLAUDE-1 PASS-bluff
		// (rule 6: captured evidence is required before declaring a feature
		// works). Refuse to report PASS without proof.
		fmt.Fprintf(stderr, "FAIL [%s] %s: evidence capture failed; refusing to report PASS without sink-side proof\n", runID, cfg.Name)
		return "", 1
	}
	fmt.Fprintf(stdout, "PASS [%s] %s evidence=%s\n", runID, cfg.Name, evidencePath)
	return evidencePath, 0
}

// writeEvidence serialises result to a JSON evidence file under cfg.EvidenceDir
// and returns the full path. On write failure it logs to stderr and returns "".
func writeEvidence(cfg ChallengeConfig, result *ChallengeResult, stderr io.Writer) string {
	dir := cfg.EvidenceDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "evidence mkdir error: %v\n", err)
		return ""
	}

	// Filename: <challenge-name>-<runID>.json (sanitized).
	safeName := strings.NewReplacer("/", "_", " ", "_").Replace(cfg.Name)
	filename := fmt.Sprintf("%s-%s.json", safeName, result.RunID)
	path := filepath.Join(dir, filename)

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "evidence marshal error: %v\n", err)
		return ""
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintf(stderr, "evidence write error: %v\n", err)
		return ""
	}
	return path
}

// cmdChallenge implements the "challenge" subcommand.
//
// Usage: helix-test challenge <url> <expected-sink> [evidence-dir] [challenge-name]
func cmdChallenge(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "Usage: helix-test challenge <url> <expected-sink> [evidence-dir] [challenge-name]")
		return 1
	}
	cfg := ChallengeConfig{
		URL:          args[0],
		ExpectedSink: args[1],
	}
	if len(args) >= 3 {
		cfg.EvidenceDir = args[2]
	}
	cfg.Name = "helix-challenge"
	if len(args) >= 4 {
		cfg.Name = args[3]
	}

	_, code := RunChallenge(cfg, stdout, stderr)
	return code
}
