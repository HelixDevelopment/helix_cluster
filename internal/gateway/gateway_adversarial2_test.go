package gateway

// Adversarial round 2 for internal/gateway — PROXY + ROUTING correctness.
//
// Round 1 (gateway_adversarial_test.go) covered fail-open auth, prefix-boundary
// scope selection, concurrency, and the documented pre-auth REST/inference
// dispatch. This round targets what round 1 missed: PATH NORMALIZATION bypass
// of the auth-scope decision, and routing/forwarding consistency.
//
// Every assertion is sink-side: we drive the real *Gateway.ServeHTTP via
// httptest and assert on the HTTP status and/or whether a recording spy
// upstream was reached and WITH WHAT PATH.

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pathSpyUpstream records the exact path the backend received, plus a hit flag.
type pathSpyUpstream struct {
	hits int32
	last atomic.Value // string
}

func (s *pathSpyUpstream) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.hits, 1)
		s.last.Store(r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"upstream":"ok"}`))
	})
}
func (s *pathSpyUpstream) reached() bool { return atomic.LoadInt32(&s.hits) > 0 }
func (s *pathSpyUpstream) lastPath() string {
	if v := s.last.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// newPathSpyGateway wires the scheduler route at a path-recording spy.
func newPathSpyGateway(t *testing.T, policy AuthPolicy) (*Gateway, *pathSpyUpstream) {
	t.Helper()
	spy := &pathSpyUpstream{}
	backend := httptest.NewServer(spy.handler())
	t.Cleanup(backend.Close)
	g, err := NewGateway(WithAuth(policy))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}
	g.SetProxy("/api/v1/scheduler/", httputil.NewSingleHostReverseProxy(target))
	return g, spy
}

// ─────────────────────────────────────────────────────────────────────────────
// (1) PATH-TRAVERSAL AUTH-SCOPE BYPASS — the core round-2 bug.
//
// A nested admin rule "/api/v1/scheduler/admin/" requires scope
// "scheduler:admin"; the broader "/api/v1/scheduler/" requires only
// "scheduler:read". A caller holding ONLY scheduler:read sends
//
//	/api/v1/scheduler/foo/../admin/purge
//
// which a normalizing backend (any Go http.ServeMux, file server, or a second
// proxy) collapses to /api/v1/scheduler/admin/purge. If the gateway evaluates
// auth on the RAW path, it matches only the weaker "/api/v1/scheduler/" rule and
// ADMITS the request — a scope bypass onto the admin endpoint.
//
// MUTATION GUARD: removing the isCanonicalPath() reject in ServeHTTP makes this
// test FAIL (the read-only token reaches the admin endpoint with status 200).
// ─────────────────────────────────────────────────────────────────────────────
func TestAdversarial2_DotDotTraversal_DoesNotBypassAdminScope(t *testing.T) {
	policy := AuthPolicy{
		Secret: advSecret,
		Prefixes: map[string]string{
			"/api/v1/scheduler/":       "scheduler:read",
			"/api/v1/scheduler/admin/": "scheduler:admin",
		},
		DefaultScope: "",
	}
	readOnly := mintToken(t, "scheduler:read", time.Hour)

	// Hostile traversal paths that normalize onto the admin sub-path.
	hostile := []string{
		"/api/v1/scheduler/foo/../admin/purge",
		"/api/v1/scheduler/./admin/purge",
		"/api/v1/scheduler/a/b/../../admin/purge",
	}
	for _, p := range hostile {
		t.Run(p, func(t *testing.T) {
			g, spy := newPathSpyGateway(t, policy)
			rec := do(g, http.MethodGet, p, "Bearer "+readOnly)
			if rec.Code == http.StatusOK || spy.reached() {
				t.Fatalf("AUTH-SCOPE BYPASS: read-only token on %q got status %d, "+
					"upstream reached=%v (path=%q) — a stronger admin rule was "+
					"defeated by a traversal segment", p, rec.Code, spy.reached(), spy.lastPath())
			}
			// Correct behavior: rejected before reaching auth/backend (400).
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("non-canonical path %q got %d, want 400", p, rec.Code)
			}
		})
	}
}

// (2) Double-slash and trailing-dot normalization variants must also not slip a
// request onto the backend in non-canonical form.
func TestAdversarial2_NonCanonicalPaths_Rejected(t *testing.T) {
	policy := AuthPolicy{
		Secret:       advSecret,
		Prefixes:     map[string]string{"/api/v1/scheduler/": "scheduler:read"},
		DefaultScope: "scheduler:read",
	}
	readTok := mintToken(t, "scheduler:read", time.Hour)
	for _, p := range []string{
		"/api/v1/scheduler//jobs",       // empty segment
		"/api/v1/scheduler/jobs/../..",  // escapes the prefix entirely
		"/api/v1/scheduler/../../secret", // climbs above the route
	} {
		t.Run(p, func(t *testing.T) {
			g, spy := newPathSpyGateway(t, policy)
			rec := do(g, http.MethodGet, p, "Bearer "+readTok)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("non-canonical path %q got %d, want 400 (reached=%v path=%q)",
					p, rec.Code, spy.reached(), spy.lastPath())
			}
			if spy.reached() {
				t.Fatalf("non-canonical path %q reached upstream as %q", p, spy.lastPath())
			}
		})
	}
}

// (3) POSITIVE CONTROL — a clean, canonical path with the correct scope STILL
// works. This proves the fix is a precise traversal reject, not a blanket deny
// that would make (1)/(2) pass vacuously.
func TestAdversarial2_CanonicalPath_StillProxied(t *testing.T) {
	policy := AuthPolicy{
		Secret:       advSecret,
		Prefixes:     map[string]string{"/api/v1/scheduler/": "scheduler:read"},
		DefaultScope: "scheduler:read",
	}
	g, spy := newPathSpyGateway(t, policy)
	good := mintToken(t, "scheduler:read", time.Hour)
	rec := do(g, http.MethodGet, "/api/v1/scheduler/jobs/123", "Bearer "+good)
	if rec.Code != http.StatusOK || !spy.reached() {
		t.Fatalf("canonical path was not proxied: status %d reached=%v", rec.Code, spy.reached())
	}
	if got := spy.lastPath(); got != "/api/v1/scheduler/jobs/123" {
		t.Fatalf("backend received mangled path %q, want /api/v1/scheduler/jobs/123", got)
	}
	// And the canonical admin sub-path with only read scope must still be denied
	// (403), proving the longest-prefix admin rule is enforced on clean paths too.
	pol2 := AuthPolicy{
		Secret: advSecret,
		Prefixes: map[string]string{
			"/api/v1/scheduler/":       "scheduler:read",
			"/api/v1/scheduler/admin/": "scheduler:admin",
		},
		DefaultScope: "",
	}
	g2, spy2 := newPathSpyGateway(t, pol2)
	rec = do(g2, http.MethodGet, "/api/v1/scheduler/admin/purge", "Bearer "+good)
	if rec.Code != http.StatusForbidden || spy2.reached() {
		t.Fatalf("clean admin path with read scope: status %d reached=%v, want 403/not-reached",
			rec.Code, spy2.reached())
	}
}

// (4) Traversal reject is enforced even with auth DISABLED (open gateway): an
// open gateway must not forward a non-canonical path to a backend either, since
// the backend's own normalization can cross a routing boundary. Sink-side.
func TestAdversarial2_OpenGateway_TraversalStillRejected(t *testing.T) {
	spy := &pathSpyUpstream{}
	backend := httptest.NewServer(spy.handler())
	t.Cleanup(backend.Close)
	g, err := NewGateway() // no WithAuth → open
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	target, _ := url.Parse(backend.URL)
	g.SetProxy("/api/v1/scheduler/", httputil.NewSingleHostReverseProxy(target))

	rec := do(g, http.MethodGet, "/api/v1/scheduler/foo/../admin/purge", "")
	if rec.Code != http.StatusBadRequest || spy.reached() {
		t.Fatalf("open gateway forwarded a traversal path: status %d reached=%v path=%q",
			rec.Code, spy.reached(), spy.lastPath())
	}
}

// (5) The REST surface is dispatched pre-auth (round-1 documented contract), but
// it MUST NOT be reachable via a traversal path that normalizes onto it from a
// different prefix. e.g. "/v1/sessions/../pool/utilization" should be rejected
// 400, not silently normalized onto the pool endpoint.
func TestAdversarial2_RESTSurface_TraversalRejected(t *testing.T) {
	g, err := NewGateway(WithREST())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	for _, p := range []string{
		"/v1/pool/../pool/utilization",
		"/v1/sessions/../pool/utilization",
		"/v1//pool/utilization",
	} {
		rec := do(g, http.MethodGet, p, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("REST traversal %q got %d, want 400", p, rec.Code)
		}
	}
	// Canonical REST path still works.
	if rec := do(g, http.MethodGet, "/v1/pool/utilization", ""); rec.Code != http.StatusOK {
		t.Fatalf("canonical /v1/pool/utilization got %d, want 200", rec.Code)
	}
}

// (6) Concurrency: hammer the gate with traversal paths under concurrent
// SetProxy. None may be admitted; run with -race to catch data races on the
// shared proxies map and the new normalization guard.
func TestAdversarial2_ConcurrentTraversal_NoneAdmitted(t *testing.T) {
	policy := AuthPolicy{
		Secret:       advSecret,
		Prefixes:     map[string]string{"/api/v1/scheduler/": "scheduler:admin"},
		DefaultScope: "scheduler:admin",
	}
	g, spy := newPathSpyGateway(t, policy)
	backend := httptest.NewServer(spy.handler())
	t.Cleanup(backend.Close)
	target, _ := url.Parse(backend.URL)

	const workers = 12
	const iters = 30
	var wg sync.WaitGroup
	var admitted int32

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < workers*iters; i++ {
			g.SetProxy("/api/v1/scheduler/", httputil.NewSingleHostReverseProxy(target))
		}
	}()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				rec := do(g, http.MethodGet, "/api/v1/scheduler/x/../admin/purge", "")
				if rec.Code != http.StatusBadRequest {
					atomic.AddInt32(&admitted, 1)
				}
			}
		}()
	}
	wg.Wait()
	if n := atomic.LoadInt32(&admitted); n != 0 {
		t.Fatalf("%d traversal requests were NOT rejected with 400 under concurrency", n)
	}
	if spy.reached() {
		t.Fatalf("FAIL-OPEN: a traversal request reached upstream under concurrency")
	}
}
