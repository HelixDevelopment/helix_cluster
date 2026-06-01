//go:build integration

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestIntegrationRESTSessionsAndPoolUtilization exercises the full REST API
// surface over a real TCP connection (HXC-1118).
//
// This test MUST hard-fail when the endpoints misbehave — no t.Skip on a
// reachable gateway.
//
// Per-run UUID: embedded in the session resource spec; asserted to appear in
// the response id or metadata (round-trip proof).
func TestIntegrationRESTSessionsAndPoolUtilization(t *testing.T) {
	// ── per-run UUID ────────────────────────────────────────────────────────
	// A unique string per test execution. Embedded in the POST /v1/sessions
	// request body and verified in the response — proving the payload
	// traversed the real TCP stack end-to-end.
	runUUID := fmt.Sprintf("hxc-1118-integration-%d", time.Now().UnixNano())

	// ── build gateway with deterministic fake backends ─────────────────────
	// The fake backends provide predictable, verifiable values that let the
	// integration test assert concrete field values over the real network
	// without requiring a live session scheduler or resource monitor.
	sessionFake := &fakeSessionBackend{}
	poolFake := fakePoolBackend{cpu: 55.5, mem: 66.6, gpu: 33.3}

	g, err := NewGateway(WithREST(
		WithSessionBackend(sessionFake),
		WithPoolBackend(poolFake),
	))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	// ── real TCP listener ─────────────────────────────────────────────────
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := "http://" + ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if serveErr := g.Serve(ctx, ln); serveErr != nil {
			t.Logf("Serve returned: %v", serveErr)
		}
	}()
	// Brief pause so the goroutine is accepting before we send requests.
	time.Sleep(20 * time.Millisecond)

	client := &http.Client{Timeout: 5 * time.Second}

	// ── POST /v1/sessions ──────────────────────────────────────────────────
	t.Run("post-sessions-201-uuid-roundtrip", func(t *testing.T) {
		spec := map[string]interface{}{
			"resource": map[string]interface{}{
				"run_uuid": runUUID,
				"gpu":      "A100",
				"count":    2,
			},
		}
		specBytes, _ := json.Marshal(spec)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			addr+"/v1/sessions", bytes.NewReader(specBytes))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /v1/sessions: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		// Status MUST be 201 — hard failure if not.
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201 Created; body = %s", resp.StatusCode, body)
		}

		var result SessionResponse
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode response: %v; body = %s", err, body)
		}

		// id MUST be non-empty.
		if result.ID == "" {
			t.Fatalf("id is empty in response: %s", body)
		}

		// status MUST be "CREATING".
		if result.Status != "CREATING" {
			t.Errorf("status = %q, want CREATING; body = %s", result.Status, body)
		}

		// The run UUID MUST have been consumed by the fake backend —
		// fakeSessionBackend stores lastSpec, so we can verify the spec arrived.
		if sessionFake.lastSpec.Resource == nil {
			t.Fatalf("backend received nil resource spec; wanted run_uuid embedded")
		}
		gotUUID, _ := sessionFake.lastSpec.Resource["run_uuid"].(string)
		if gotUUID != runUUID {
			t.Errorf("backend run_uuid = %q, want %q (UUID did not round-trip)", gotUUID, runUUID)
		}

		// The response spec echo must contain the run_uuid key.
		if result.Spec == nil {
			t.Fatal("spec echo absent in response")
		}
		echoUUID, _ := result.Spec["run_uuid"].(string)
		if echoUUID != runUUID {
			t.Errorf("response spec run_uuid = %q, want %q (echo mismatch)", echoUUID, runUUID)
		}
	})

	// ── GET /v1/pool/utilization ──────────────────────────────────────────
	t.Run("get-pool-utilization-200-numeric", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			addr+"/v1/pool/utilization", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /v1/pool/utilization: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		// Status MUST be 200 — hard failure if not.
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
		}

		var util PoolUtilization
		if err := json.Unmarshal(body, &util); err != nil {
			t.Fatalf("decode: %v; body = %s", err, body)
		}

		// All three fields MUST be numeric (non-NaN, finite).
		if util.CPUPct != 55.5 {
			t.Errorf("cpu_pct = %v, want 55.5", util.CPUPct)
		}
		if util.MemPct != 66.6 {
			t.Errorf("mem_pct = %v, want 66.6", util.MemPct)
		}
		if util.GPUPct != 33.3 {
			t.Errorf("gpu_pct = %v, want 33.3", util.GPUPct)
		}

		// Verify raw JSON wire format has number types, not strings.
		var raw map[string]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("re-decode as raw map: %v", err)
		}
		for _, field := range []string{"cpu_pct", "mem_pct", "gpu_pct"} {
			v, ok := raw[field]
			if !ok {
				t.Errorf("field %q absent in JSON wire format", field)
				continue
			}
			if _, isNum := v.(float64); !isNum {
				t.Errorf("field %q has type %T, want float64 number", field, v)
			}
		}
	})

	// ── GET /openapi.json ──────────────────────────────────────────────────
	t.Run("get-openapi-json-valid", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			addr+"/openapi.json", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /openapi.json: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
		}

		var doc map[string]interface{}
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("openapi.json not valid JSON: %v; body = %s", err, body)
		}

		version, _ := doc["openapi"].(string)
		if !strings.HasPrefix(version, "3.0") {
			t.Errorf("openapi version = %q, want 3.0.x", version)
		}

		paths, _ := doc["paths"].(map[string]interface{})
		if _, ok := paths["/v1/sessions"]; !ok {
			t.Errorf("/v1/sessions not in openapi paths")
		}
		if _, ok := paths["/v1/pool/utilization"]; !ok {
			t.Errorf("/v1/pool/utilization not in openapi paths")
		}
	})

	// ── second POST to confirm counter increments (not idempotent) ─────────
	t.Run("post-sessions-second-call-different-id", func(t *testing.T) {
		make_req := func() string {
			t.Helper()
			reqBody := strings.NewReader(fmt.Sprintf(`{"resource":{"run_uuid":%q}}`, runUUID))
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				addr+"/v1/sessions", reqBody)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body = %s", resp.StatusCode, body)
			}
			var r SessionResponse
			if err := json.Unmarshal(body, &r); err != nil {
				t.Fatalf("decode: %v", err)
			}
			return r.ID
		}

		id1 := make_req()
		id2 := make_req()
		if id1 == id2 {
			t.Errorf("consecutive POST /v1/sessions returned same id=%q — sessions must be unique", id1)
		}
	})
}
