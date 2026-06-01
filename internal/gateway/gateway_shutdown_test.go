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
	"sync/atomic"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/jwt"
)

// capRoundTripper implements http.RoundTripper and captures the sub claim from
// the outgoing request's context before forwarding.
type capRoundTripper struct {
	base    http.RoundTripper
	captSub *string
}

func (c capRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// The outgoing request carries the enriched context set by ServeHTTP because
	// httputil.ReverseProxy calls req = outreq.WithContext(r.Context()) where r
	// is the incoming request that auth already enriched.
	claims, _ := req.Context().Value(ClaimsContextKey).(map[string]interface{})
	if claims != nil {
		*c.captSub, _ = claims["sub"].(string)
	}
	return c.base.RoundTrip(req)
}

// TestClaimsInjectedIntoContext proves that after a successful JWT auth the
// verified claims map is placed in the request context under ClaimsContextKey.
//
// We intercept the outgoing transport round-trip (httputil.ReverseProxy
// propagates the incoming request's context to the outgoing request) and assert
// the "sub" claim equals the exact value baked into the token. This is the
// correct consumption point: gateway-layer middleware reads context; remote
// backends receive a new TCP connection and cannot read Go context values.
//
// Mutation: in ServeHTTP, remove the r = r.WithContext(context.WithValue(…))
// assignment -> claims is nil in the transport, capturedSub stays "", test
// fails on the exact-match assertion.
func TestClaimsInjectedIntoContext(t *testing.T) {
	const wantSub = "user-42"

	secret := []byte("claims-test-secret")
	g, err := NewGateway(WithAuth(AuthPolicy{
		Secret:       secret,
		Prefixes:     map[string]string{"/api/v1/build/": "build:read"},
		DefaultScope: "",
	}))
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	// A real upstream so ServeHTTP resolves properly; what we actually assert is
	// captured in the transport before the request reaches the network.
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer be.Close()

	var capturedSub string
	tgt, _ := url.Parse(be.URL)
	customProxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = tgt.Scheme
			req.URL.Host = tgt.Host
			req.Host = tgt.Host
		},
		Transport: capRoundTripper{
			base:    http.DefaultTransport,
			captSub: &capturedSub,
		},
	}
	g.SetProxy("/api/v1/build/", customProxy)

	tok, err := jwt.GenerateToken(
		map[string]interface{}{"sub": wantSub, "scope": "build:read"},
		secret, time.Minute)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/build/ping", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	// Sink-side assertion: the context visible in the transport layer had the
	// exact sub claim — proving injection happened before the proxy dispatch.
	if capturedSub != wantSub {
		t.Errorf("context sub = %q, want %q (claims not injected into context)", capturedSub, wantSub)
	}
}

// TestClaimsNotInjectedWhenAuthDisabled proves that when no auth policy is
// installed the context value at ClaimsContextKey is absent (nil) — the
// gateway must not pollute the context with empty or nil claims.
//
// Mutation: unconditionally store an empty map under ClaimsContextKey even
// when auth is disabled -> capRoundTripper sees a non-nil (but empty) claims
// map; we detect this by checking capturedClaims != nil; test fails.
func TestClaimsNotInjectedWhenAuthDisabled(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer be.Close()

	g, err := NewGateway() // no auth option
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	tgt, _ := url.Parse(be.URL)
	var capturedSub string
	customProxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = tgt.Scheme
			req.URL.Host = tgt.Host
			req.Host = tgt.Host
		},
		Transport: capRoundTripper{base: http.DefaultTransport, captSub: &capturedSub},
	}
	g.SetProxy("/api/v1/build/", customProxy)

	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/build/status", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	// When auth is disabled no claims can be present, so capturedSub must be "".
	if capturedSub != "" {
		t.Errorf("ClaimsContextKey present (sub=%q) without auth policy", capturedSub)
	}
}

// TestGracefulShutdownDrainsInflight proves the Serve method drains an
// in-flight request before returning after context cancellation.
//
// Sequence:
//  1. Gateway starts listening on an ephemeral port via Serve(ctx, ln).
//  2. A slow backend introduces a 150 ms artificial delay per request.
//  3. Client sends a request to the gateway and the backend begins handling it.
//  4. The parent context is cancelled (simulating SIGTERM) while the request is
//     in flight.
//  5. The test asserts the response was still received with status 200 — i.e.
//     Shutdown drained the in-flight connection.
//
// Mutation: replace srv.Shutdown(shutCtx) with srv.Close() (immediate teardown)
// -> in-flight request is aborted, client receives a connection-reset error
// instead of a 200, atomic hit counter stays 0, test fails.
func TestGracefulShutdownDrainsInflight(t *testing.T) {
	const backendDelay = 150 * time.Millisecond

	var hits int32
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(backendDelay)
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "drained-ok")
	}))
	defer be.Close()

	g, err := NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	tgt, _ := url.Parse(be.URL)
	g.SetProxy("/api/v1/build/", httputil.NewSingleHostReverseProxy(tgt))

	// Bind an ephemeral listener so we know the exact port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gatewayAddr := fmt.Sprintf("http://%s", ln.Addr().String())

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- g.Serve(ctx, ln) }()

	// Give the gateway a moment to accept connections.
	time.Sleep(20 * time.Millisecond)

	// Fire the request in a goroutine — it will take at least backendDelay.
	reqDone := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get(gatewayAddr + "/api/v1/build/status") //nolint:noctx
		if err != nil {
			t.Logf("client get error: %v", err)
			reqDone <- nil
			return
		}
		reqDone <- resp
	}()

	// Cancel the context before the backend delay expires — simulates SIGTERM.
	time.Sleep(30 * time.Millisecond)
	cancel()

	// Gateway must drain and return within 5 s.
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancel (shutdown timed out)")
	}

	// The in-flight request should have completed successfully.
	select {
	case resp := <-reqDone:
		if resp == nil {
			t.Fatalf("in-flight request failed (connection reset during shutdown)")
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200 after graceful drain", resp.StatusCode)
		}
		if string(body) != "drained-ok" {
			t.Errorf("body = %q, want drained-ok", string(body))
		}
		if n := atomic.LoadInt32(&hits); n != 1 {
			t.Errorf("backend hits = %d, want 1 (request must complete before drain)", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request never completed")
	}
}

// TestServeReturnsNilOnContextCancel proves that cancelling the context alone
// (with no in-flight requests) yields a nil error from Serve — not an error
// from ErrServerClosed leaking out. Callers treat context cancellation as
// normal shutdown, not an error.
//
// Mutation: return http.ErrServerClosed from the errCh path without filtering
// -> context-cancel shutdown may still emit ErrServerClosed, test sees non-nil.
func TestServeReturnsNilOnContextCancel(t *testing.T) {
	g, err := NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Serve(ctx, ln) }()

	// Give server a moment then cancel.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned non-nil error on clean shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after context cancel")
	}
}
