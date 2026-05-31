package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// freePort asks the kernel for an available TCP port on 127.0.0.1 and returns
// it. Binding 127.0.0.1:0 then closing is host-safe and race-tolerant: the
// window before run() re-binds is tiny and local-only.
func freePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()
	return port
}

// startServer launches run() on a free 127.0.0.1 port and returns the bound
// base URL plus a stop func. It blocks (via the ready callback) until the
// listener is actually open, so there is no bind race for the dialing client.
func startServer(t *testing.T) (baseURL string, stop func()) {
	t.Helper()

	cfg := Config{
		Host:            "127.0.0.1",
		Port:            freePort(t),
		ShutdownTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan string, 1)
	runErr := make(chan error, 1)

	go func() {
		runErr <- run(ctx, cfg, func(a string) { addrCh <- a })
	}()

	var addr string
	select {
	case addr = <-addrCh:
	case err := <-runErr:
		t.Fatalf("run exited before binding: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server to bind")
	}

	stop = func() {
		cancel()
		select {
		case err := <-runErr:
			if err != nil {
				t.Errorf("run returned error: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("timed out waiting for run to shut down")
		}
	}
	return "http://" + addr, stop
}

// TestRunRegisterAndList proves the service actually starts, binds, and serves
// REAL HTTP requests end to end: a real client POSTs a model registration,
// gets 201 + a concrete success body, then GETs /models and reads the model
// back out of internal/llm with the exact fields it registered.
//
// Mutation that breaks it: in run(), build the server with a fresh manager per
// request (so registration does not persist), or drop the
// s.manager.RegisterModel call in handleRegister. The POST would still 201 but
// the subsequent GET would return an empty list, failing the len/name asserts.
func TestRunRegisterAndList(t *testing.T) {
	baseURL, stop := startServer(t)
	defer stop()

	// Register a real model via HTTP.
	regBody, _ := json.Marshal(map[string]string{
		"name": "llama-3", "path": "/models/llama-3.gguf", "format": "gguf",
	})
	resp, err := http.Post(baseURL+"/models/register", "application/json", bytes.NewReader(regBody))
	if err != nil {
		t.Fatalf("register POST: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d, want 201", resp.StatusCode)
	}
	var regResp map[string]bool
	if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
		t.Fatalf("decode register resp: %v", err)
	}
	resp.Body.Close()
	if !regResp["success"] {
		t.Fatalf("register resp = %v, want success=true", regResp)
	}

	// List models back via HTTP and assert the concrete registered values.
	listResp, err := http.Get(baseURL + "/models")
	if err != nil {
		t.Fatalf("list GET: %v", err)
	}
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200", listResp.StatusCode)
	}
	var models []map[string]interface{}
	if err := json.NewDecoder(listResp.Body).Decode(&models); err != nil {
		t.Fatalf("decode list resp: %v", err)
	}
	listResp.Body.Close()

	if len(models) != 1 {
		t.Fatalf("models = %d, want 1: %v", len(models), models)
	}
	if got := models[0]["name"]; got != "llama-3" {
		t.Fatalf("model name = %v, want llama-3", got)
	}
	if got := models[0]["format"]; got != "gguf" {
		t.Fatalf("model format = %v, want gguf", got)
	}
	if got := models[0]["path"]; got != "/models/llama-3.gguf" {
		t.Fatalf("model path = %v, want /models/llama-3.gguf", got)
	}
}

// TestRunInferenceUnloadedModelContract proves a second real HTTP path is wired
// through to internal/llm and that the load-bearing usability contract is
// enforced over the wire: inference on a registered-but-not-loaded model is
// rejected with 404 and the real "model not loaded" error from the manager —
// not a synthetic success. (Per internal/llm, inference is only valid once a
// model is loaded; the HTTP API exposes no load route, so an unloaded model is
// the honest steady state here.)
//
// Mutation that breaks it: in handleInference, write http.StatusOK with a fake
// {"result": "..."} body instead of surfacing the manager error — the 404 and
// substring assertions below then fail.
func TestRunInferenceUnloadedModelContract(t *testing.T) {
	baseURL, stop := startServer(t)
	defer stop()

	regBody, _ := json.Marshal(map[string]string{
		"name": "m1", "path": "/models/m1", "format": "gguf",
	})
	resp, err := http.Post(baseURL+"/models/register", "application/json", bytes.NewReader(regBody))
	if err != nil {
		t.Fatalf("register POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d, want 201", resp.StatusCode)
	}

	infBody, _ := json.Marshal(map[string]string{"model": "m1", "prompt": "hello"})
	infResp, err := http.Post(baseURL+"/inference", "application/json", bytes.NewReader(infBody))
	if err != nil {
		t.Fatalf("inference POST: %v", err)
	}
	body, _ := io.ReadAll(infResp.Body)
	infResp.Body.Close()

	if infResp.StatusCode != http.StatusNotFound {
		t.Fatalf("inference status = %d, want 404 for unloaded model (body=%q)", infResp.StatusCode, body)
	}
	if !strings.Contains(string(body), "model not loaded") {
		t.Fatalf("inference body = %q, want it to contain %q", body, "model not loaded")
	}
}

// TestRunHealth proves the /health route serves a concrete healthy payload over
// real HTTP.
//
// Mutation that breaks it: in handleHealth, change "healthy" to "unhealthy" (or
// drop the /health route from newMux) — the assertion below then fails.
func TestRunHealth(t *testing.T) {
	baseURL, stop := startServer(t)
	defer stop()

	resp, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("health GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
	var h map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	resp.Body.Close()
	if h["status"] != "healthy" {
		t.Fatalf("health status = %q, want healthy", h["status"])
	}
}

// TestRunGracefulShutdownStopsServing proves ctx cancellation actually stops
// the service: after stop() returns, the bound TCP port no longer accepts new
// connections.
//
// Mutation that breaks it: in run(), replace the `case <-ctx.Done():` shutdown
// path with `select{}` (block forever), or remove the httpServer.Shutdown call.
// The listener would keep serving and the post-shutdown dial below would
// succeed, failing this test (and stop() would also time out).
func TestRunGracefulShutdownStopsServing(t *testing.T) {
	baseURL, stop := startServer(t)

	// Sanity: it serves before shutdown.
	resp, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("pre-shutdown health failed: %v", err)
	}
	resp.Body.Close()

	// Trigger ctx cancel + wait for run() to fully return.
	stop()

	addr := strings.TrimPrefix(baseURL, "http://")
	conn, dErr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if dErr == nil {
		conn.Close()
		t.Fatalf("expected port %s to stop accepting after shutdown, but dial succeeded", addr)
	}
}

// TestLoadConfigRejectsBadPort proves config validation rejects clearly-bad
// input with a real error instead of starting a broken server.
//
// Mutation that breaks it: in Config.Validate(), change the guard to
// `if c.Port < 0` (or delete the port check). Then port 70000 is accepted and
// LoadConfig returns nil error, failing this test.
func TestLoadConfigRejectsBadPort(t *testing.T) {
	cases := []struct {
		name string
		val  string
	}{
		{"out-of-range-high", "70000"},
		{"out-of-range-zero", "0"},
		{"negative", "-1"},
		{"non-integer", "not-a-port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := func(k string) string {
				if k == "HELIX_LLM_PORT" {
					return tc.val
				}
				return ""
			}
			if _, err := LoadConfig(env); err == nil {
				t.Fatalf("expected error for HELIX_LLM_PORT=%q, got nil", tc.val)
			}
		})
	}
}

// TestLoadConfigDefaults proves a clean environment yields the documented
// default port, and that a valid override is honoured.
//
// Mutation that breaks it: change defaultPort to a different value, or make
// LoadConfig ignore HELIX_LLM_PORT — either assertion below then fails.
func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadConfig with empty env: %v", err)
	}
	if cfg.Port != defaultPort {
		t.Fatalf("expected default port %d, got %d", defaultPort, cfg.Port)
	}

	cfg2, err := LoadConfig(func(k string) string {
		if k == "HELIX_LLM_PORT" {
			return "51234"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("LoadConfig with valid override: %v", err)
	}
	if cfg2.Port != 51234 {
		t.Fatalf("expected overridden port 51234, got %d", cfg2.Port)
	}
}

// TestRunRejectsInvalidConfig proves run() refuses to bind on invalid config
// rather than silently doing something undefined.
//
// Mutation that breaks it: remove the `cfg.Validate()` call at the top of
// run(). The function would then attempt net.Listen on an invalid port and the
// error path / message would differ, failing this assertion.
func TestRunRejectsInvalidConfig(t *testing.T) {
	err := run(context.Background(), Config{Port: 0, ShutdownTimeout: time.Second}, nil)
	if err == nil {
		t.Fatal("expected run to reject invalid config, got nil error")
	}
}
