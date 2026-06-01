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

// startServer launches run() on 127.0.0.1:0 and returns the bound base URL
// plus a stop func that cancels the context and waits for run() to return.
// This is a REAL listener and a REAL server backed by the real OPA engine.
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
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("server did not become ready within 10s")
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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("service never became healthy")
}

// evalResponse is the JSON shape returned by POST /evaluate.
type evalResponse struct {
	Allowed        bool                   `json:"allowed"`
	Reason         string                 `json:"reason"`
	EvaluatedInput map[string]interface{} `json:"evaluated_input"`
	PolicyName     string                 `json:"policy_name"`
}

// ---------------------------------------------------------------------------
// Core scheduling policy tests (the scheduling policy is pre-loaded by run)
// ---------------------------------------------------------------------------

// TestRunSchedulingPolicyPreloaded proves the HelixConstitution scheduling
// policy is available immediately after start without a POST /policies call.
// A node with health=40 must be DENIED and health=80 must be ALLOWED.
//
// Mutation proof: in run(), remove the MustLoadSchedulingPolicy call → POST
// /evaluate returns 404 for the "scheduling" policy → both sub-cases fail.
func TestRunSchedulingPolicyPreloaded(t *testing.T) {
	baseURL, stop := startServer(t, Config{})
	defer stop()
	waitHealthy(t, baseURL)

	type tc struct {
		health    int
		wantAllow bool
	}
	for _, c := range []tc{{40, false}, {80, true}} {
		body, _ := json.Marshal(map[string]interface{}{
			"policy": "scheduling",
			"input": map[string]interface{}{
				"node": map[string]interface{}{"health": c.health},
			},
		})
		resp, err := http.Post(baseURL+"/evaluate", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /evaluate health=%d: %v", c.health, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("health=%d: status=%d want 200", c.health, resp.StatusCode)
		}
		var out evalResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.Allowed != c.wantAllow {
			t.Fatalf("health=%d: Allowed=%v want %v (Reason=%q)", c.health, out.Allowed, c.wantAllow, out.Reason)
		}
		if out.Reason == "" {
			t.Fatalf("health=%d: Reason is empty", c.health)
		}
	}
}

// TestRunServesRealRequests proves the service starts, binds, and serves a REAL
// end-to-end policy lifecycle over a real HTTP client: load a custom rego
// policy, evaluate input the engine must allow, then verify the decision fields.
//
// Mutation proof: in run(), replace newHandler(engine) with
// http.NewServeMux() (empty handler) → /policies POST returns 404 → load
// step fails.
func TestRunServesRealRequests(t *testing.T) {
	baseURL, stop := startServer(t, Config{})
	defer stop()
	waitHealthy(t, baseURL)

	// Load a real OPA rego policy.
	loadBody, _ := json.Marshal(map[string]string{
		"name": "net-policy",
		"rego": `package net_policy
default allow = false
allow { input.action != "delete" }`,
	})
	resp, err := http.Post(baseURL+"/policies", "application/json", bytes.NewReader(loadBody))
	if err != nil {
		t.Fatalf("POST /policies: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("load policy status = %d, want 201", resp.StatusCode)
	}

	// Evaluate input that must be ALLOWED (action != delete).
	evalBody, _ := json.Marshal(map[string]interface{}{
		"policy": "net-policy",
		"input":  map[string]interface{}{"action": "read"},
	})
	resp, err = http.Post(baseURL+"/evaluate", "application/json", bytes.NewReader(evalBody))
	if err != nil {
		t.Fatalf("POST /evaluate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("evaluate status = %d, want 200", resp.StatusCode)
	}
	var out evalResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode evaluate response: %v", err)
	}
	if !out.Allowed {
		t.Fatalf("Allowed = false, want true (allow rule must match action=read); reason=%q", out.Reason)
	}
	if out.PolicyName != "net-policy" {
		t.Fatalf("policy_name = %q, want net-policy", out.PolicyName)
	}
	if out.EvaluatedInput == nil {
		t.Fatal("evaluated_input is nil, want the round-tripped input map")
	}

	// Verify the listed policy includes what we loaded.
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

// TestEvaluateDenyPath proves the deny branch returns allowed=false over the
// real HTTP client.  Mutation: change the rego to `allow { true }` → allowed
// flips to true → test fails.
func TestEvaluateDenyPath(t *testing.T) {
	baseURL, stop := startServer(t, Config{})
	defer stop()
	waitHealthy(t, baseURL)

	loadBody, _ := json.Marshal(map[string]string{
		"name": "deny-all",
		"rego": `package deny_all
default allow = false`,
	})
	r, err := http.Post(baseURL+"/policies", "application/json", bytes.NewReader(loadBody))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r.Body.Close()

	evalBody, _ := json.Marshal(map[string]interface{}{
		"policy": "deny-all",
		"input":  map[string]interface{}{"anything": "here"},
	})
	resp, err := http.Post(baseURL+"/evaluate", "application/json", bytes.NewReader(evalBody))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	defer resp.Body.Close()
	var out evalResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Allowed {
		t.Fatalf("Allowed = true, want false for deny-all policy")
	}
}

// TestEvaluateUnknownPolicy proves a missing policy yields 404 from the live
// server.  Mutation: in newHandler, change the Evaluate error status from
// StatusNotFound to StatusOK → status check fails.
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
// and the port stops accepting connections.  Mutation: in run(), remove the
// `case <-ctx.Done()` shutdown branch → the server keeps serving after cancel
// → the post-stop dial still succeeds → test fails.
func TestGracefulShutdownStopsServing(t *testing.T) {
	baseURL, stop := startServer(t, Config{})
	waitHealthy(t, baseURL)

	if resp, err := http.Get(baseURL + "/health"); err != nil {
		t.Fatalf("pre-shutdown health: %v", err)
	} else {
		resp.Body.Close()
	}

	if err := stop(); err != nil {
		t.Fatalf("run returned error on graceful shutdown: %v", err)
	}

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
// invalid port.  Mutation: remove the port range check in Config.Validate →
// 999999 is accepted → error is nil → test fails.
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

	err := run(context.Background(), Config{Addr: ":70000", ShutdownTimeout: time.Second}, nil)
	if err == nil {
		t.Fatal("run accepted out-of-range port, want error")
	}
}

// TestConfigValidationRejectsBadShutdownTimeout proves a zero timeout is
// rejected.  Mutation: remove the ShutdownTimeout <= 0 check → error is nil →
// test fails.
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
// Mutation: ignore HELIX_POLICY_PORT → Addr stays :50058 → assertion fails.
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

// ---------------------------------------------------------------------------
// Integration test (build tag: integration) — cmd entrypoint end-to-end
// ---------------------------------------------------------------------------
