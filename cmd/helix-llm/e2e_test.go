//go:build e2e

// Package main e2e for cmd/helix-llm drives the REAL built LLM-brain binary
// end-to-end (CLAUDE-1): it `go build`s the artifact and execs it the way an
// operator would — on a config-driven port supplied via the documented
// HELIX_LLM_PORT env var — then asserts the observable HTTP contract an end
// user actually consumes:
//
//   - GET  /health          -> 200 {"status":"healthy"}
//   - POST /models/register -> 201 {"success":true}   (a model descriptor)
//   - GET  /models          -> 200 list containing the just-registered model
//
// This exercises ONLY the service's own HTTP surface and its in-process model
// registry (internal/llm.Manager) — it never contacts a real LLM backend
// (HuggingFace, Ollama, etc.), so the test is hermetic and has no external
// dependencies. The port is reserved with net.Listen(":0") then closed, and the
// concrete number handed to the binary (Config.Validate rejects port 0).
//
// Run with: go test -tags e2e ./cmd/helix-llm/ -count=1 -v
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func e2eRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func e2eBuildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "helix-llm")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	// -buildvcs=false is REQUIRED: without it the in-test build fails with
	// "error obtaining VCS status" when run from a worktree / detached checkout.
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/helix-llm")
	cmd.Dir = e2eRepoRoot(t)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build ./cmd/helix-llm failed: %v\n%s", err, out.String())
	}
	return bin
}

// e2eReserveFreePort asks the kernel for an unused TCP port, then closes the
// listener so the binary can bind it. There is a benign race (another process
// could grab it before exec), but on a test host it is reliable and keeps the
// port fully config-driven rather than hardcoded. The concrete number (never 0)
// is what Config.Validate requires.
func e2eReserveFreePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not reserve a free port: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	if err := lis.Close(); err != nil {
		t.Fatalf("could not release reserved port: %v", err)
	}
	return port
}

// e2eStartServer execs the real binary bound to the reserved port and waits until
// GET /health answers 200. It returns the base URL and a teardown func that
// kills the process and waits for it to reap cleanly.
func e2eStartServer(t *testing.T, bin string, port int) (base string, teardown func()) {
	t.Helper()

	cmd := exec.Command(bin)
	// Config-driven: the port arrives only through the documented env var.
	cmd.Env = append(cmd.Environ(),
		"HELIX_LLM_PORT="+strconv.Itoa(port),
		"HELIX_LLM_HOST=127.0.0.1",
	)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("could not start helix-llm: %v", err)
	}

	teardown = func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // reap; ignore the kill-induced non-zero exit.
	}

	base = fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		// Bail early if the process died during startup (bad bind, panic, ...).
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			teardown()
			t.Fatalf("helix-llm exited during startup; logs:\n%s", logs.String())
		}
		resp, err := client.Get(base + "/health")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var hb struct {
					Status string `json:"status"`
				}
				if jsonErr := json.Unmarshal(body, &hb); jsonErr == nil && hb.Status == "healthy" {
					return base, teardown
				}
			}
		}
		if time.Now().After(deadline) {
			teardown()
			t.Fatalf("helix-llm /health never returned 200 healthy within 10s; logs:\n%s", logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestHelixLLMServiceLifecycle proves the shipped binary serves its real HTTP
// API to an end user: it boots on a reserved port, reports healthy, accepts a
// model registration, and then lists that model back — all without ever
// touching an external LLM backend.
func TestHelixLLMServiceLifecycle(t *testing.T) {
	bin := e2eBuildBinary(t)
	port := e2eReserveFreePort(t)
	base, teardown := e2eStartServer(t, bin, port)
	defer teardown()

	client := &http.Client{Timeout: 5 * time.Second}

	// SINK-SIDE 1: /health is already proven 200+healthy by e2eStartServer; re-check
	// the exact contract here so the assertion is explicit in the test body.
	{
		resp, err := client.Get(base + "/health")
		if err != nil {
			t.Fatalf("GET /health failed: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /health: want 200, got %d; body: %s", resp.StatusCode, body)
		}
		var hb struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(body, &hb); err != nil {
			t.Fatalf("GET /health body not JSON: %v; body: %s", err, body)
		}
		if hb.Status != "healthy" {
			t.Fatalf("GET /health: want status=healthy, got %q", hb.Status)
		}
	}

	// SINK-SIDE 2: register a minimal VALID model descriptor. The handler decodes
	// {name, path, format}; the registry requires non-empty name AND path, so this
	// descriptor must yield 201 {"success":true}.
	const modelName = "e2e-llm-model"
	{
		descriptor := map[string]string{
			"name":   modelName,
			"path":   "/var/lib/helix/models/e2e-llm-model.gguf",
			"format": "gguf",
		}
		payload, err := json.Marshal(descriptor)
		if err != nil {
			t.Fatalf("marshal descriptor: %v", err)
		}
		resp, err := client.Post(base+"/models/register", "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("POST /models/register failed: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		// 201 is the documented success; accept 200 too per the task's tolerance.
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /models/register: want 201/200, got %d; body: %s", resp.StatusCode, body)
		}
		var rr struct {
			Success bool `json:"success"`
		}
		if err := json.Unmarshal(body, &rr); err != nil {
			t.Fatalf("register response not JSON: %v; body: %s", err, body)
		}
		if !rr.Success {
			t.Fatalf("register did not report success; body: %s", body)
		}
	}

	// SINK-SIDE 3: the just-registered model is observable via GET /models.
	{
		resp, err := client.Get(base + "/models")
		if err != nil {
			t.Fatalf("GET /models failed: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /models: want 200, got %d; body: %s", resp.StatusCode, body)
		}
		var models []struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			Format string `json:"format"`
		}
		if err := json.Unmarshal(body, &models); err != nil {
			t.Fatalf("GET /models body not a JSON array: %v; body: %s", err, body)
		}
		found := false
		for _, m := range models {
			if m.Name == modelName {
				found = true
				if m.Format != "gguf" {
					t.Fatalf("listed model %q has wrong format %q (want gguf)", modelName, m.Format)
				}
				if !strings.HasSuffix(m.Path, "e2e-llm-model.gguf") {
					t.Fatalf("listed model %q has wrong path %q", modelName, m.Path)
				}
			}
		}
		if !found {
			t.Fatalf("GET /models did not contain just-registered model %q; body: %s", modelName, body)
		}
	}
}

// TestHelixLLMRejectsInvalidModel proves the load-bearing input guard on the
// real artifact: a descriptor with an empty name MUST be rejected (no path can
// make a nameless model valid), and the model MUST NOT appear in /models. A
// silent accept would be a CLAUDE-1 PASS-bluff.
func TestHelixLLMRejectsInvalidModel(t *testing.T) {
	bin := e2eBuildBinary(t)
	port := e2eReserveFreePort(t)
	base, teardown := e2eStartServer(t, bin, port)
	defer teardown()

	client := &http.Client{Timeout: 5 * time.Second}

	payload, _ := json.Marshal(map[string]string{"name": "", "path": "/p", "format": "gguf"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/models/register", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /models/register (invalid) failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		t.Fatalf("HARD-FAIL: nameless model accepted (status %d); body: %s", resp.StatusCode, body)
	}

	// And it must not have leaked into the registry.
	lr, err := client.Get(base + "/models")
	if err != nil {
		t.Fatalf("GET /models failed: %v", err)
	}
	lb, _ := io.ReadAll(lr.Body)
	_ = lr.Body.Close()
	if !bytes.Equal(bytes.TrimSpace(lb), []byte("[]")) {
		// Tolerate a non-empty list only if it genuinely contains no empty-named entry.
		var models []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(lb, &models); err != nil {
			t.Fatalf("GET /models body not a JSON array: %v; body: %s", err, lb)
		}
		for _, m := range models {
			if m.Name == "" {
				t.Fatalf("HARD-FAIL: empty-named model leaked into registry; body: %s", lb)
			}
		}
	}
}
