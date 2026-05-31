package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// startServer launches run() on 127.0.0.1:0 and returns the bound base URL plus
// a stop func that cancels the context and waits for run() to return. This is a
// REAL listener and a REAL server, not httptest of a stubbed handler.
func startServer(t *testing.T, cfg Config) (baseURL string, stop func() error) {
	t.Helper()
	cfg.Addr = "127.0.0.1:0"
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = 2 * time.Second
	}

	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan net.Addr, 1)
	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, cfg, func(a net.Addr) { addrCh <- a }) }()

	var addr net.Addr
	select {
	case addr = <-addrCh:
	case err := <-runErr:
		cancel()
		t.Fatalf("run exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not become ready")
	}

	stop = func() error {
		cancel()
		select {
		case err := <-runErr:
			return err
		case <-time.After(5 * time.Second):
			return fmt.Errorf("run did not return after cancel")
		}
	}
	return "http://" + addr.String(), stop
}

func waitHealthy(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("service never became healthy")
}

// TestRunServesRealRequests proves the service starts, binds, and serves a REAL
// end-to-end policy lifecycle over a real HTTP client: load a deny policy, then
// evaluate input that the engine must deny, asserting concrete decision values.
//
// Mutation that makes it FAIL: in run(), drop the `helixv1`-style wiring by
// replacing `newHandler(engine)` with `http.NewServeMux()` (empty handler) ->
// /policies POST returns 404 and the load step fails. Equivalently, change
// engine.Evaluate's deny precedence in the assertion to expect allowed=true.
func TestRunServesRealRequests(t *testing.T) {
	baseURL, stop := startServer(t, Config{})
	defer stop()
	waitHealthy(t, baseURL)

	// Load a real policy with an explicit deny rule.
	loadBody, _ := json.Marshal(map[string]string{
		"name": "net-policy",
		"rego": "deny if action == \"delete\"",
	})
	resp, err := http.Post(baseURL+"/policies", "application/json", bytes.NewReader(loadBody))
	if err != nil {
		t.Fatalf("POST /policies: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("load policy status = %d, want 201", resp.StatusCode)
	}

	// Evaluate input that must be DENIED by the loaded rule.
	evalBody, _ := json.Marshal(map[string]interface{}{
		"policy": "net-policy",
		"input":  map[string]interface{}{"action": "delete"},
	})
	resp, err = http.Post(baseURL+"/evaluate", "application/json", bytes.NewReader(evalBody))
	if err != nil {
		t.Fatalf("POST /evaluate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("evaluate status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Allowed   bool                   `json:"allowed"`
		Decisions map[string]interface{} `json:"decisions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode evaluate response: %v", err)
	}
	if out.Allowed {
		t.Fatalf("allowed = true, want false (deny rule must match action=delete)")
	}
	if got := out.Decisions["matched"]; got != "deny" {
		t.Fatalf("decisions.matched = %v, want \"deny\"", got)
	}

	// And the listed policy must include what we loaded.
	resp2, err := http.Get(baseURL + "/policies")
	if err != nil {
		t.Fatalf("GET /policies: %v", err)
	}
	defer resp2.Body.Close()
	var names []string
	if err := json.NewDecoder(resp2.Body).Decode(&names); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, n := range names {
		if n == "net-policy" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListPolicies = %v, want it to contain net-policy", names)
	}
}

// TestEvaluateAllowPath proves the allow branch returns a concrete allowed=true
// with matched="allow" over the real client. Mutation that makes it FAIL:
// change the input value to "read" so no allow rule matches -> default-deny.
func TestEvaluateAllowPath(t *testing.T) {
	baseURL, stop := startServer(t, Config{})
	defer stop()
	waitHealthy(t, baseURL)

	loadBody, _ := json.Marshal(map[string]string{
		"name": "p", "rego": "allow if role == \"admin\"",
	})
	r, err := http.Post(baseURL+"/policies", "application/json", bytes.NewReader(loadBody))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r.Body.Close()

	evalBody, _ := json.Marshal(map[string]interface{}{
		"policy": "p", "input": map[string]interface{}{"role": "admin"},
	})
	resp, err := http.Post(baseURL+"/evaluate", "application/json", bytes.NewReader(evalBody))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Allowed   bool                   `json:"allowed"`
		Decisions map[string]interface{} `json:"decisions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Allowed {
		t.Fatalf("allowed = false, want true (allow rule must match role=admin)")
	}
	if got := out.Decisions["matched"]; got != "allow" {
		t.Fatalf("matched = %v, want allow", got)
	}
}

// TestEvaluateUnknownPolicy proves a missing policy yields 404 from the live
// server. Mutation that makes it FAIL: in newHandler, change the Evaluate error
// status from StatusNotFound to StatusOK.
func TestEvaluateUnknownPolicy(t *testing.T) {
	baseURL, stop := startServer(t, Config{})
	defer stop()
	waitHealthy(t, baseURL)

	body, _ := json.Marshal(map[string]interface{}{"policy": "nope", "input": map[string]interface{}{}})
	resp, err := http.Post(baseURL+"/evaluate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown policy", resp.StatusCode)
	}
}

// TestGracefulShutdownStopsServing proves ctx-cancel triggers graceful shutdown
// and the port stops accepting connections. Mutation that makes it FAIL: in
// run(), remove the `case <-ctx.Done()` shutdown branch (e.g. `select{}` only on
// serveErr) so the server keeps serving after cancel -> the post-stop dial
// still succeeds and the test fails.
func TestGracefulShutdownStopsServing(t *testing.T) {
	baseURL, stop := startServer(t, Config{})
	waitHealthy(t, baseURL)

	// Confirm it serves before shutdown.
	if resp, err := http.Get(baseURL + "/health"); err != nil {
		t.Fatalf("pre-shutdown health: %v", err)
	} else {
		resp.Body.Close()
	}

	if err := stop(); err != nil {
		t.Fatalf("run returned error on graceful shutdown: %v", err)
	}

	// After shutdown the listener must be closed: a fresh dial must fail.
	host := baseURL[len("http://"):]
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", host, 200*time.Millisecond)
		if err != nil {
			return // success: port no longer accepts connections
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("port still accepting connections after graceful shutdown")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestConfigValidationRejectsBadPort proves config validation rejects an
// invalid port with a meaningful error and that run() refuses to start. Mutation
// that makes it FAIL: remove the port range check in Config.Validate (`port <0
// || port >65535`) -> 999999 is accepted and the error is nil.
func TestConfigValidationRejectsBadPort(t *testing.T) {
	bad := []string{"999999", "-1", "abc"}
	for _, p := range bad {
		_, err := ConfigFromEnv(func(k string) string {
			if k == "HELIX_POLICY_PORT" {
				return p
			}
			return ""
		})
		if err == nil {
			t.Fatalf("ConfigFromEnv accepted invalid port %q, want error", p)
		}
	}

	// run() must also refuse an invalid config without binding.
	err := run(context.Background(), Config{Addr: ":70000", ShutdownTimeout: time.Second}, nil)
	if err == nil {
		t.Fatal("run accepted out-of-range port, want error")
	}
}

// TestConfigValidationRejectsBadShutdownTimeout proves a non-positive shutdown
// timeout is rejected. Mutation that makes it FAIL: remove the
// `ShutdownTimeout <= 0` check in Config.Validate.
func TestConfigValidationRejectsBadShutdownTimeout(t *testing.T) {
	_, err := ConfigFromEnv(func(k string) string {
		if k == "HELIX_POLICY_SHUTDOWN_TIMEOUT" {
			return "0s"
		}
		return ""
	})
	if err == nil {
		t.Fatal("ConfigFromEnv accepted zero shutdown timeout, want error")
	}
}

// TestConfigFromEnvAppliesPort proves a valid env port is threaded into Addr.
// Mutation that makes it FAIL: in ConfigFromEnv, ignore HELIX_POLICY_PORT.
func TestConfigFromEnvAppliesPort(t *testing.T) {
	cfg, err := ConfigFromEnv(func(k string) string {
		if k == "HELIX_POLICY_PORT" {
			return "51234"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Addr != ":51234" {
		t.Fatalf("Addr = %q, want :51234", cfg.Addr)
	}
}
