//go:build e2e

// Package main's e2e suite drives the REAL helix-health binary end-to-end as an
// end user (a load balancer / orchestrator probe) would: it `go build`s the
// command into a temp dir, runs that artifact as a subprocess on env-driven
// ephemeral (":0") ports, waits for the listeners to come up, and then asserts
// the OBSERVABLE HTTP probe contract (/health 200 + JSON status, /readyz 200).
//
// This is a genuine end-to-end exercise (CLAUDE-1): it proves the shipped binary
// actually serves working health probes to a real HTTP client — not an in-process
// call to run()/newHTTPServer. No port is hard-coded: ports are passed via env as
// ":0" and the actually-bound address is discovered by parsing the binary's own
// "listening on <addr>" startup log, so the test never races the bind and never
// collides with a fixed port.
//
// Run with: go test -tags e2e ./cmd/helix-health/... -run TestE2E -v
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// TestE2EHealthBinaryServesProbes builds and runs the real helix-health binary,
// then asserts the end-user-visible HTTP probe surface:
//   - GET /health returns 200 with a JSON body carrying a non-empty "status".
//   - GET /readyz returns 200 (the process self-registers a Healthy check so it
//     reports ready out of the box).
func TestE2EHealthBinaryServesProbes(t *testing.T) {
	bin := e2eBuild(t, "./cmd/helix-health")

	// Env-driven ports discovered dynamically (no hard-coded ports): reserve two
	// free ephemeral ports and hand them to the binary via its documented env
	// vars. The binary's config validation rejects a literal "0", so we pass the
	// concrete kernel-assigned numbers instead.
	grpcPort := e2eFreePort(t)
	httpPort := e2eFreePort(t)
	proc := e2eStart(t, bin, []string{
		portEnv("HELIX_HEALTH_PORT", grpcPort),
		portEnv("HELIX_HEALTH_HTTP_PORT", httpPort),
	})
	defer proc.stop(t)

	// The HTTP probe surface logs "helix-health HTTP listening on <addr>".
	httpAddr := proc.waitForAddr(t, "HTTP listening on ")
	base := "http://" + httpAddr

	// ---- /health : 200 + JSON {"status": "..."} ----
	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatalf("GET /health on real binary failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health: got status %d, want 200; body=%s", resp.StatusCode, body)
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("GET /health: response is not JSON: %v; body=%s", err, body)
	}
	if health.Status == "" {
		t.Fatalf("GET /health: JSON has empty/missing status field; body=%s", body)
	}
	t.Logf("e2e /health -> 200 status=%q", health.Status)

	// ---- /readyz : 200 (process is ready) ----
	rresp, err := http.Get(base + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz on real binary failed: %v", err)
	}
	rbody, _ := io.ReadAll(rresp.Body)
	_ = rresp.Body.Close()
	if rresp.StatusCode != http.StatusOK {
		t.Fatalf("GET /readyz: got status %d, want 200; body=%s", rresp.StatusCode, rbody)
	}
	t.Logf("e2e /readyz -> 200 body=%s", rbody)
}
