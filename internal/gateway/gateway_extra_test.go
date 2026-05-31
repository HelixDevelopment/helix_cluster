package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// TestRouteSelectsCorrectBackend proves each prefix is dispatched to its own
// backend, not merely that "some" backend is hit. Two distinct backends are
// registered and the response body identifies which one served the request.
func TestRouteSelectsCorrectBackend(t *testing.T) {
	sched := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "scheduler:"+r.URL.Path)
	}))
	defer sched.Close()
	session := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "session:"+r.URL.Path)
	}))
	defer session.Close()

	g, err := NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	su, _ := url.Parse(sched.URL)
	se, _ := url.Parse(session.URL)
	g.SetProxy("/api/v1/scheduler/", httputil.NewSingleHostReverseProxy(su))
	g.SetProxy("/api/v1/session/", httputil.NewSingleHostReverseProxy(se))

	cases := []struct{ path, wantPrefix string }{
		{"/api/v1/scheduler/jobs", "scheduler:/api/v1/scheduler/jobs"},
		{"/api/v1/session/abc", "session:/api/v1/session/abc"},
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		g.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, c.path, nil))
		if got := rr.Body.String(); got != c.wantPrefix {
			t.Errorf("path %s served by %q, want %q", c.path, got, c.wantPrefix)
		}
	}
}

// TestPrefixRequiresTrailingSlash proves a path that matches the prefix only
// without its trailing slash is NOT routed (returns 404). A mutation dropping
// the trailing slash from a route prefix, or switching HasPrefix to a looser
// match, would cause this to route (200) and fail.
func TestPrefixRequiresTrailingSlash(t *testing.T) {
	g, err := NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	// Point build at a live backend so a wrong match would yield 200, not 502.
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer be.Close()
	tgt, _ := url.Parse(be.URL)
	g.SetProxy("/api/v1/build/", httputil.NewSingleHostReverseProxy(tgt))

	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/build", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no trailing slash must not route)", rr.Code)
	}
}

// TestForwardPreservesMethodPathHeaders proves the gateway is a transparent
// proxy: method, full path (including segments after the prefix) and arbitrary
// headers reach the backend unchanged.
func TestForwardPreservesMethodPathHeaders(t *testing.T) {
	var gotMethod, gotPath, gotHdr string
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotHdr = r.Method, r.URL.Path, r.Header.Get("X-Trace-Id")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer be.Close()

	g, err := NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	tgt, _ := url.Parse(be.URL)
	g.SetProxy("/api/v1/node/", httputil.NewSingleHostReverseProxy(tgt))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/node/n1/drain", nil)
	req.Header.Set("X-Trace-Id", "trace-123")
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rr.Code)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("backend method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/api/v1/node/n1/drain" {
		t.Errorf("backend path = %q, want /api/v1/node/n1/drain", gotPath)
	}
	if gotHdr != "trace-123" {
		t.Errorf("backend X-Trace-Id = %q, want trace-123", gotHdr)
	}
}

// TestBackendDownReturnsJSON502 proves the custom ErrorHandler: when a backend
// is unreachable the client receives a 502 with a JSON body and the gateway
// marker header, instead of the stdlib's bare empty-body 502.
// Mutation: remove proxy.ErrorHandler assignment in newReverseProxy -> body is
// empty and Content-Type is unset, failing this test.
func TestBackendDownReturnsJSON502(t *testing.T) {
	// Bind a listener then close it so the address is dead but parseable.
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := be.URL
	be.Close() // backend is now down

	g, err := NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	tgt, _ := url.Parse(deadURL)
	g.SetProxy("/api/v1/build/", newReverseProxy(tgt))

	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/build/status", nil))

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if rr.Header().Get(gatewayHeader) != "true" {
		t.Errorf("missing %s marker header on error response", gatewayHeader)
	}
	if !strings.Contains(rr.Body.String(), `"error":"backend unavailable"`) {
		t.Errorf("body = %q, want JSON error", rr.Body.String())
	}
}

// TestProxiedResponseHasGatewayHeader proves ModifyResponse stamps the marker
// header on a successful proxied response (sink-side evidence the request
// actually traversed the gateway's proxy, not a bypass).
// Mutation: remove proxy.ModifyResponse in newReverseProxy -> header absent.
func TestProxiedResponseHasGatewayHeader(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer be.Close()

	g, err := NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	tgt, _ := url.Parse(be.URL)
	g.SetProxy("/api/v1/scheduler/", newReverseProxy(tgt))

	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/ping", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rr.Header().Get(gatewayHeader) != "true" {
		t.Errorf("proxied response missing %s marker header", gatewayHeader)
	}
}

// TestNewGatewayInvalidBackend proves NewGateway surfaces a parse error rather
// than silently constructing a broken gateway. The default routes are valid, so
// we exercise the error path by temporarily injecting a bad route.
// Mutation: swallow the url.Parse error (return nil) -> err is nil, test fails.
func TestNewGatewayInvalidBackend(t *testing.T) {
	saved := routes
	defer func() { routes = saved }()
	routes = []route{{prefix: "/bad/", backend: "://missing-scheme"}}

	g, err := NewGateway()
	if err == nil {
		t.Fatalf("NewGateway() error = nil, want error for invalid backend URL; g=%v", g)
	}
	if !strings.Contains(err.Error(), "invalid backend URL") {
		t.Errorf("error = %v, want it to mention invalid backend URL", err)
	}
}

// TestHealthNotProxied proves /health is served locally and never dispatched to
// a backend, even if a proxy is registered. The health body must be the local
// JSON, not backend output.
func TestHealthNotProxied(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "BACKEND-SHOULD-NOT-SERVE-HEALTH")
	}))
	defer be.Close()

	g, err := NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	tgt, _ := url.Parse(be.URL)
	// Register a catch-y prefix; /health must still be intercepted first.
	g.SetProxy("/api/v1/build/", httputil.NewSingleHostReverseProxy(tgt))

	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if !strings.Contains(rr.Body.String(), `"status":"healthy"`) {
		t.Errorf("health body = %q, want local healthy JSON", rr.Body.String())
	}
}

// TestSetProxyConcurrentWithServe exercises the RWMutex: SetProxy and ServeHTTP
// run concurrently. Under -race this fails if the proxies map access were
// unguarded. Mutation: drop the mu.Lock/RLock pair -> race detector fires.
func TestSetProxyConcurrentWithServe(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer be.Close()
	tgt, _ := url.Parse(be.URL)

	g, err := NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			g.SetProxy("/api/v1/build/", httputil.NewSingleHostReverseProxy(tgt))
		}()
		go func() {
			defer wg.Done()
			rr := httptest.NewRecorder()
			g.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/build/x", nil))
		}()
	}
	wg.Wait()
}
