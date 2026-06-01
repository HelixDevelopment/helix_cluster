//go:build integration

package gateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/jwt"
)

// TestIntegrationHXC1213SchedulerScopeAndDenyReason is the integration tier for
// HXC-1213: X-Helix-Deny-Reason header + /api/v1/scheduler/ scope enforcement.
//
// A real Gateway listens on an ephemeral TCP port; a real downstream
// httptest.Server echoes a per-run UUID baked into each request path so the
// test can assert end-to-end round-trip (not just status codes).
//
// Hard-failure contract (CLAUDE-1): this test MUST fail — never t.Skip — when
// the gateway is reachable. t.Skip is reserved solely for the case where the
// network stack itself is unavailable (net.Listen failure), which is
// documented in-line.
//
// Assertions (all three closure criteria for HXC-1213):
//  1. GET /api/v1/scheduler/<uuid> with NO token → 401, UUID absent from body.
//  2. GET /api/v1/scheduler/<uuid> token lacking scheduler:write → 403,
//     X-Helix-Deny-Reason == "insufficient scope", UUID absent from body.
//  3. GET /api/v1/scheduler/<uuid> token WITH scheduler:write → 200,
//     X-Helix-Gateway: true, UUID present in body (round-trip proof).
func TestIntegrationHXC1213SchedulerScopeAndDenyReason(t *testing.T) {
	// ── per-run UUID ────────────────────────────────────────────────────────
	// Unique per execution; embedded in the request path so any false bypass
	// would echo it in the response body and break the absent-UUID assertions.
	runUUID := fmt.Sprintf("hxc-1213-integration-%d", time.Now().UnixNano())

	const secret = "hxc-1213-integration-hs256-secret"
	secretBytes := []byte(secret)

	// ── downstream: echo backend that writes the full request path ──────────
	schedBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "scheduler")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "sched-path:%s", r.URL.Path)
	}))
	defer schedBackend.Close()

	// ── gateway: scheduler:write required for /api/v1/scheduler/ ───────────
	g, err := NewGateway(WithAuth(AuthPolicy{
		Secret: secretBytes,
		Prefixes: map[string]string{
			"/api/v1/scheduler/": "scheduler:write",
		},
		DefaultScope: "core:read",
	}))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	schedURL, _ := url.Parse(schedBackend.URL)
	g.SetProxy("/api/v1/scheduler/", newReverseProxy(schedURL))

	// ── real TCP listener ─────────────────────────────────────────────────
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		// The ONLY legitimate t.Skip site: the OS TCP stack is unavailable.
		t.Skipf("net.Listen unavailable: %v", err)
	}
	gwAddr := "http://" + ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if serveErr := g.Serve(ctx, ln); serveErr != nil {
			t.Logf("Serve returned: %v", serveErr)
		}
	}()
	time.Sleep(20 * time.Millisecond)

	// ── token factory ─────────────────────────────────────────────────────
	mustTok := func(claims map[string]interface{}) string {
		t.Helper()
		tok, err := jwt.GenerateToken(claims, secretBytes, time.Minute)
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		return tok
	}

	// ── helper: GET over real TCP ─────────────────────────────────────────
	get := func(path, bearer string) (int, http.Header, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet, gwAddr+path, nil)
		if err != nil {
			t.Fatalf("NewRequest(%s): %v", path, err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(body)
	}

	// ── (1) no token → 401, UUID absent ──────────────────────────────────
	t.Run("no-token-401-uuid-absent", func(t *testing.T) {
		path := "/api/v1/scheduler/" + runUUID
		code, _, body := get(path, "")
		if code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (no token); body=%q", code, body)
		}
		// UUID must NOT appear — upstream was never reached.
		if strings.Contains(body, runUUID) {
			t.Errorf("UUID found in 401 body (upstream hit): %q", body)
		}
	})

	// ── (2) wrong scope → 403, X-Helix-Deny-Reason set, UUID absent ──────
	t.Run("wrong-scope-403-deny-reason-uuid-absent", func(t *testing.T) {
		path := "/api/v1/scheduler/jobs/" + runUUID
		tok := mustTok(map[string]interface{}{"sub": "ci", "scope": "core:read"})
		code, hdr, body := get(path, tok)

		if code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (wrong scope); body=%q", code, body)
		}

		// PRIMARY HXC-1213 criterion: X-Helix-Deny-Reason must be set.
		reason := hdr.Get("X-Helix-Deny-Reason")
		if reason == "" {
			t.Fatalf("X-Helix-Deny-Reason absent on 403 response (HXC-1213 criterion not met)")
		}
		const wantReason = "insufficient scope"
		if reason != wantReason {
			t.Errorf("X-Helix-Deny-Reason = %q, want %q", reason, wantReason)
		}

		// UUID must NOT appear in the body — upstream was never reached.
		if strings.Contains(body, runUUID) {
			t.Errorf("UUID found in 403 body (upstream hit): %q", body)
		}
	})

	// ── (3) valid scheduler:write → 200 + X-Helix-Gateway + UUID in body ─
	t.Run("valid-scheduler-write-200-uuid-roundtrip", func(t *testing.T) {
		path := "/api/v1/scheduler/tasks/" + runUUID
		tok := mustTok(map[string]interface{}{"sub": "scheduler-agent", "scope": "scheduler:write"})
		code, hdr, body := get(path, tok)

		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (scheduler:write); body=%q", code, body)
		}

		// X-Helix-Gateway must be present — proves the response traversed the proxy.
		if hdr.Get(gatewayHeader) != "true" {
			t.Errorf("missing %s marker on proxied response", gatewayHeader)
		}

		// UUID round-trip: backend echoed the path including the per-run UUID.
		wantBody := "sched-path:" + path
		if body != wantBody {
			t.Errorf("body = %q, want %q (UUID did not round-trip)", body, wantBody)
		}
	})
}
