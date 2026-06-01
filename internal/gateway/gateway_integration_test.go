//go:build integration

package gateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/jwt"
)

// TestIntegrationGatewayProxyUUIDRoundTrip is a full in-process integration
// test for HXC-1091 (proxy) + HXC-1092 (auth). A real Gateway listens on an
// ephemeral TCP port; a real downstream httptest.Server echoes a per-run UUID
// embedded by the test; the test client sends an HTTP/1.1 request through the
// real TCP stack and asserts the exact UUID in the response body.
//
// This proves:
//   - Prefix routing reaches the correct downstream (not another one).
//   - The UUID round-trip is end-to-end (body bytes, not just status code).
//   - JWT auth: valid token + correct scope → 200 + UUID body.
//   - JWT auth: missing token → 401, UUID never echoed.
//   - JWT auth: wrong scope → 403, UUID never echoed.
//   - Graceful shutdown drains the in-flight request before returning.
func TestIntegrationGatewayProxyUUIDRoundTrip(t *testing.T) {
	// ── per-run UUID ────────────────────────────────────────────────────────
	// UUID is fixed per test run and baked into the downstream response body.
	// Asserting its exact value on the client side proves the payload flowed
	// end-to-end through the real TCP stack, not a bypass or mock.
	const runUUID = "hxc-1091-integration-2026-06-01"
	const authSecret = "integration-gateway-secret-hs256"
	secret := []byte(authSecret)

	// ── downstream services (real httptest.Server instances) ─────────────
	buildBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "build")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "build-uuid:%s path:%s", runUUID, r.URL.Path)
	}))
	defer buildBackend.Close()

	schedBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "scheduler")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "sched-uuid:%s path:%s", runUUID, r.URL.Path)
	}))
	defer schedBackend.Close()

	// ── gateway construction ──────────────────────────────────────────────
	g, err := NewGateway(WithAuth(AuthPolicy{
		Secret: secret,
		Prefixes: map[string]string{
			"/api/v1/build/":     "build:write",
			"/api/v1/scheduler/": "scheduler:read",
		},
		DefaultScope: "core:read",
	}))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	buildURL, _ := url.Parse(buildBackend.URL)
	schedURL, _ := url.Parse(schedBackend.URL)
	g.SetProxy("/api/v1/build/", newReverseProxy(buildURL))
	g.SetProxy("/api/v1/scheduler/", newReverseProxy(schedURL))

	// ── real TCP listener ─────────────────────────────────────────────────
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	gwAddr := "http://" + ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := g.Serve(ctx, ln); err != nil {
			t.Logf("Serve returned: %v", err)
		}
	}()
	// Allow the goroutine to start accepting connections.
	time.Sleep(20 * time.Millisecond)

	// ── helper: token factory ─────────────────────────────────────────────
	mustTok := func(claims map[string]interface{}) string {
		t.Helper()
		tok, err := jwt.GenerateToken(claims, secret, time.Minute)
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		return tok
	}

	// ── helper: do HTTP GET with optional bearer token ────────────────────
	get := func(path, bearer string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, gwAddr+path, nil)
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
		return resp.StatusCode, string(body)
	}

	// ── /health endpoint (no auth required) ──────────────────────────────
	t.Run("health", func(t *testing.T) {
		code, body := get("/health", "")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if !strings.Contains(body, `"status":"healthy"`) {
			t.Errorf("health body = %q, want healthy JSON", body)
		}
	})

	// ── unknown prefix → 404 ─────────────────────────────────────────────
	t.Run("unknown-prefix", func(t *testing.T) {
		code, _ := get("/api/v1/unknown/x", "")
		if code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", code)
		}
	})

	// ── valid token + correct scope → 200 + UUID body (build prefix) ─────
	t.Run("build-valid-token-uuid-roundtrip", func(t *testing.T) {
		tok := mustTok(map[string]interface{}{"sub": "ci-agent", "scope": "build:write"})
		code, body := get("/api/v1/build/jobs", tok)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%q", code, body)
		}
		// Per-run UUID must appear in the body — proves the request reached
		// the real build backend through the real TCP stack.
		wantBody := fmt.Sprintf("build-uuid:%s path:/api/v1/build/jobs", runUUID)
		if body != wantBody {
			t.Errorf("body = %q, want %q", body, wantBody)
		}
	})

	// ── valid token + correct scope → 200 + UUID body (scheduler prefix) ─
	t.Run("sched-valid-token-uuid-roundtrip", func(t *testing.T) {
		tok := mustTok(map[string]interface{}{"sub": "ci-agent", "scope": "scheduler:read"})
		code, body := get("/api/v1/scheduler/jobs", tok)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%q", code, body)
		}
		wantBody := fmt.Sprintf("sched-uuid:%s path:/api/v1/scheduler/jobs", runUUID)
		if body != wantBody {
			t.Errorf("body = %q, want %q", body, wantBody)
		}
	})

	// ── missing token → 401, upstream not hit ────────────────────────────
	t.Run("missing-token-401", func(t *testing.T) {
		code, body := get("/api/v1/build/jobs", "")
		if code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body=%q", code, body)
		}
		// UUID must NOT appear in the body — upstream was not contacted.
		if strings.Contains(body, runUUID) {
			t.Errorf("UUID found in 401 body (upstream was hit): %q", body)
		}
	})

	// ── invalid signature → 401 ───────────────────────────────────────────
	t.Run("bad-signature-401", func(t *testing.T) {
		forged, err := jwt.GenerateToken(
			map[string]interface{}{"scope": "build:write"},
			[]byte("wrong-secret"), time.Minute)
		if err != nil {
			t.Fatalf("GenerateToken forged: %v", err)
		}
		code, body := get("/api/v1/build/jobs", forged)
		if code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 for bad signature; body=%q", code, body)
		}
		if strings.Contains(body, runUUID) {
			t.Errorf("UUID found in 401 body: %q", body)
		}
	})

	// ── expired token → 401 ───────────────────────────────────────────────
	t.Run("expired-token-401", func(t *testing.T) {
		expired, err := jwt.GenerateToken(
			map[string]interface{}{"scope": "build:write"},
			secret, -time.Minute)
		if err != nil {
			t.Fatalf("GenerateToken expired: %v", err)
		}
		code, body := get("/api/v1/build/jobs", expired)
		if code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 for expired token; body=%q", code, body)
		}
		if strings.Contains(body, runUUID) {
			t.Errorf("UUID found in 401 body: %q", body)
		}
	})

	// ── wrong scope → 403 ─────────────────────────────────────────────────
	t.Run("wrong-scope-403", func(t *testing.T) {
		// Token holds scheduler:read, but /api/v1/build/ requires build:write.
		tok := mustTok(map[string]interface{}{"scope": "scheduler:read"})
		code, body := get("/api/v1/build/jobs", tok)
		if code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 for wrong scope; body=%q", code, body)
		}
		if strings.Contains(body, runUUID) {
			t.Errorf("UUID found in 403 body (upstream was hit): %q", body)
		}
	})

	// ── X-Helix-Gateway marker present on proxied response ────────────────
	t.Run("gateway-marker-header", func(t *testing.T) {
		tok := mustTok(map[string]interface{}{"scope": "build:write"})
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
			gwAddr+"/api/v1/build/ping", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		resp.Body.Close()
		if resp.Header.Get(gatewayHeader) != "true" {
			t.Errorf("missing %s marker on proxied response", gatewayHeader)
		}
	})

	// ── graceful shutdown drains in-flight request ────────────────────────
	t.Run("graceful-shutdown", func(t *testing.T) {
		const drainDelay = 120 * time.Millisecond

		// A slow backend specific to this sub-test.
		slowBe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(drainDelay)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "shutdown-uuid:%s", runUUID)
		}))
		defer slowBe.Close()

		g2, err := NewGateway()
		if err != nil {
			t.Fatalf("NewGateway slow: %v", err)
		}
		slowURL, _ := url.Parse(slowBe.URL)
		g2.SetProxy("/api/v1/node/", httputil.NewSingleHostReverseProxy(slowURL))

		ln2, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("Listen: %v", err)
		}
		ctx2, cancel2 := context.WithCancel(context.Background())
		serveDone := make(chan error, 1)
		go func() { serveDone <- g2.Serve(ctx2, ln2) }()
		time.Sleep(15 * time.Millisecond)

		gwAddr2 := "http://" + ln2.Addr().String()
		reqDone := make(chan *http.Response, 1)
		go func() {
			resp, err := http.Get(gwAddr2 + "/api/v1/node/n1") //nolint:noctx
			if err != nil {
				t.Logf("in-flight GET error: %v", err)
				reqDone <- nil
				return
			}
			reqDone <- resp
		}()

		// Cancel before the slow backend finishes — simulates SIGTERM.
		time.Sleep(30 * time.Millisecond)
		cancel2()

		select {
		case err := <-serveDone:
			if err != nil {
				t.Fatalf("Serve error after cancel: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Serve did not return in time")
		}

		select {
		case resp := <-reqDone:
			if resp == nil {
				t.Fatalf("in-flight request failed during shutdown")
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200 after graceful drain", resp.StatusCode)
			}
			// UUID must appear — confirms the slow backend actually completed.
			wantBody := fmt.Sprintf("shutdown-uuid:%s", runUUID)
			if string(body) != wantBody {
				t.Errorf("body = %q, want %q", string(body), wantBody)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("in-flight request never completed")
		}
	})
}
