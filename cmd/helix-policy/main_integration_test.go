//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestIntegrationCmdEndToEnd exercises the full cmd/helix-policy entrypoint
// end-to-end: server start, pre-loaded scheduling policy, scheduling decisions
// for an unhealthy and a healthy node, and a custom policy loaded at runtime.
//
// A per-run UUID is embedded in every input and asserted in evaluated_input so
// that the round-trip is proven, not just guessed.  This test MUST hard-FAIL
// on wrong decisions — it is not a smoke test.
func TestIntegrationCmdEndToEnd(t *testing.T) {
	runID := uuid.New().String()

	baseURL, stop := startServer(t, Config{ShutdownTimeout: 3 * time.Second})
	defer stop()
	waitHealthy(t, baseURL)

	t.Logf("integration run_id=%s", runID)

	// ---- 1. Pre-loaded scheduling policy: unhealthy node must be DENIED ----
	body, _ := json.Marshal(map[string]interface{}{
		"policy": "scheduling",
		"input": map[string]interface{}{
			"run_id": runID,
			"node":   map[string]interface{}{"health": 30},
		},
	})
	resp, err := http.Post(baseURL+"/evaluate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("evaluate unhealthy: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("evaluate unhealthy status=%d want 200", resp.StatusCode)
	}
	var out struct {
		Allowed        bool                   `json:"allowed"`
		Reason         string                 `json:"reason"`
		EvaluatedInput map[string]interface{} `json:"evaluated_input"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode unhealthy: %v", err)
	}
	if out.Allowed {
		t.Fatalf("HARD-FAIL: unhealthy node (health=30) was ALLOWED, want DENY")
	}
	if got, ok := out.EvaluatedInput["run_id"]; !ok || got != runID {
		t.Fatalf("run_id not round-tripped: evaluated_input[run_id]=%v", got)
	}

	// ---- 2. Pre-loaded scheduling policy: healthy node must be ALLOWED ----
	body, _ = json.Marshal(map[string]interface{}{
		"policy": "scheduling",
		"input": map[string]interface{}{
			"run_id": runID,
			"node":   map[string]interface{}{"health": 75},
		},
	})
	resp2, err := http.Post(baseURL+"/evaluate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("evaluate healthy: %v", err)
	}
	defer resp2.Body.Close()
	var out2 struct {
		Allowed        bool                   `json:"allowed"`
		Reason         string                 `json:"reason"`
		EvaluatedInput map[string]interface{} `json:"evaluated_input"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&out2); err != nil {
		t.Fatalf("decode healthy: %v", err)
	}
	if !out2.Allowed {
		t.Fatalf("HARD-FAIL: healthy node (health=75) was DENIED (reason=%q), want ALLOW", out2.Reason)
	}
	if got, ok := out2.EvaluatedInput["run_id"]; !ok || got != runID {
		t.Fatalf("run_id not round-tripped in healthy case: evaluated_input[run_id]=%v", got)
	}

	// ---- 3. Load a custom policy at runtime and evaluate it ----
	loadBody, _ := json.Marshal(map[string]string{
		"name": "quota",
		"rego": `package quota
default allow = false
allow { input.used <= input.limit }`,
	})
	lr, err := http.Post(baseURL+"/policies", "application/json", bytes.NewReader(loadBody))
	if err != nil {
		t.Fatalf("load quota policy: %v", err)
	}
	lr.Body.Close()
	if lr.StatusCode != http.StatusCreated {
		t.Fatalf("load quota status=%d want 201", lr.StatusCode)
	}

	// Within quota: must allow.
	evalBody, _ := json.Marshal(map[string]interface{}{
		"policy": "quota",
		"input":  map[string]interface{}{"run_id": runID, "used": 50, "limit": 100},
	})
	er, err := http.Post(baseURL+"/evaluate", "application/json", bytes.NewReader(evalBody))
	if err != nil {
		t.Fatalf("evaluate quota within: %v", err)
	}
	defer er.Body.Close()
	var quotaOut struct {
		Allowed bool `json:"allowed"`
	}
	if err := json.NewDecoder(er.Body).Decode(&quotaOut); err != nil {
		t.Fatalf("decode quota within: %v", err)
	}
	if !quotaOut.Allowed {
		t.Fatal("HARD-FAIL: quota within limit must be ALLOWED")
	}

	// Exceeded quota: must deny.
	evalBody, _ = json.Marshal(map[string]interface{}{
		"policy": "quota",
		"input":  map[string]interface{}{"run_id": runID, "used": 150, "limit": 100},
	})
	er2, err := http.Post(baseURL+"/evaluate", "application/json", bytes.NewReader(evalBody))
	if err != nil {
		t.Fatalf("evaluate quota exceeded: %v", err)
	}
	defer er2.Body.Close()
	var quotaOut2 struct {
		Allowed bool `json:"allowed"`
	}
	if err := json.NewDecoder(er2.Body).Decode(&quotaOut2); err != nil {
		t.Fatalf("decode quota exceeded: %v", err)
	}
	if quotaOut2.Allowed {
		t.Fatal("HARD-FAIL: quota exceeded limit must be DENIED")
	}

	t.Logf("integration run_id=%s: all assertions passed", runID)
}
