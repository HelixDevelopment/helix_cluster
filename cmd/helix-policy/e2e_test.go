//go:build e2e

// Package main's e2e suite drives the REAL helix-policy binary end-to-end as an
// end user (a service querying the policy engine over HTTP/JSON) would: it
// `go build`s the command, runs that artifact on an env-driven ephemeral (":0")
// port, waits for the listener, and asserts the OBSERVABLE contract:
//   - GET /policies returns the pre-loaded policy list containing "scheduling".
//   - GET /metrics exposes the helix_policy_build_info gauge.
//
// This is a genuine end-to-end exercise (CLAUDE-1): it proves the shipped binary
// actually serves a working policy + metrics surface to a real HTTP client, not
// an in-process call to run()/newHandler. The port is never hard-coded: it is
// passed as ":0" via HELIX_POLICY_PORT and the actually-bound address is read
// from the binary's "helix-policy listening on <addr>" startup log.
//
// Run with: go test -tags e2e ./cmd/helix-policy/... -run TestE2E -v
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestE2EPolicyBinaryServesPoliciesAndMetrics builds and runs the real
// helix-policy binary and asserts the end-user-visible surface.
func TestE2EPolicyBinaryServesPoliciesAndMetrics(t *testing.T) {
	bin := e2eBuild(t, "./cmd/helix-policy")

	// Env-driven ephemeral port: "0" => kernel-assigned free port.
	proc := e2eStart(t, bin, []string{"HELIX_POLICY_PORT=0"})
	defer proc.stop(t)

	addr := proc.waitForAddr(t, "helix-policy listening on ")
	base := "http://" + addr

	// ---- GET /policies : 200 + JSON list containing "scheduling" ----
	resp, err := http.Get(base + "/policies")
	if err != nil {
		t.Fatalf("GET /policies on real binary failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /policies: got status %d, want 200; body=%s", resp.StatusCode, body)
	}
	var names []string
	if err := json.Unmarshal(body, &names); err != nil {
		t.Fatalf("GET /policies: response is not a JSON string array: %v; body=%s", err, body)
	}
	found := false
	for _, n := range names {
		if n == "scheduling" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("GET /policies: pre-loaded list %v does not contain \"scheduling\"", names)
	}
	t.Logf("e2e /policies -> 200 policies=%v", names)

	// ---- GET /metrics : exposes helix_policy_build_info ----
	mresp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics on real binary failed: %v", err)
	}
	mbody, _ := io.ReadAll(mresp.Body)
	_ = mresp.Body.Close()
	if mresp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: got status %d, want 200; body=%s", mresp.StatusCode, mbody)
	}
	if !strings.Contains(string(mbody), "helix_policy_build_info") {
		t.Fatalf("GET /metrics: exposition does not contain helix_policy_build_info; body:\n%s", mbody)
	}
	t.Logf("e2e /metrics -> 200 contains helix_policy_build_info")
}
