package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/jwt"
)

var authSecret = []byte("gateway-test-secret")

// hitBackend builds a stub upstream that records how many requests reach it and
// returns 200 "upstream-ok". The counter is the sink-side evidence that lets us
// prove unauthorized requests are rejected BEFORE proxying.
func hitBackend(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream-ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// authGateway wires a Gateway with enforcement on /api/v1/build/ requiring the
// "build:write" scope and the stub backend installed behind that prefix.
func authGateway(t *testing.T, backendURL string) *Gateway {
	t.Helper()
	g, err := NewGateway(WithAuth(AuthPolicy{
		Secret: authSecret,
		Prefixes: map[string]string{
			"/api/v1/build/": "build:write",
		},
		DefaultScope: "core:read",
	}))
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	tgt, _ := url.Parse(backendURL)
	g.SetProxy("/api/v1/build/", newReverseProxy(tgt))
	return g
}

func mustToken(t *testing.T, claims map[string]interface{}, d time.Duration) string {
	t.Helper()
	tok, err := jwt.GenerateToken(claims, authSecret, d)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return tok
}

// TestAuthValidTokenWithScopeProxies proves a request bearing a valid token
// that holds the required scope reaches the upstream (hit counter == 1), gets
// 200 with the upstream body, and keeps the X-Helix-Gateway marker.
//
// Mutation: in authorize, change the scope check to always return nil (drop the
// `if _, ok := claimScopes(...)` block) — test still passes here, but it's the
// 403 test below that kills that. The killing mutation for THIS test: make
// authorize always return a 401 authError — upstream hit==0, status 401, fails.
func TestAuthValidTokenWithScopeProxies(t *testing.T) {
	be, hits := hitBackend(t)
	g := authGateway(t, be.URL)

	token := mustToken(t, map[string]interface{}{"scope": "build:write core:read"}, time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/build/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rr.Body.String() != "upstream-ok" {
		t.Errorf("body = %q, want upstream-ok", rr.Body.String())
	}
	if rr.Header().Get(gatewayHeader) != "true" {
		t.Errorf("missing %s marker on proxied response", gatewayHeader)
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Errorf("upstream hits = %d, want 1", n)
	}
}

// TestAuthMissingTokenRejected proves a request with no Authorization header is
// rejected 401 and the upstream is NEVER contacted (hit counter == 0).
//
// Mutation: in authorize, return nil instead of the 401 authError when the
// bearer prefix is absent -> upstream hit==1 and status 200, test fails.
func TestAuthMissingTokenRejected(t *testing.T) {
	be, hits := hitBackend(t)
	g := authGateway(t, be.URL)

	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/build/status", nil))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("upstream hits = %d, want 0 (must reject before proxy)", n)
	}
}

// TestAuthInvalidSignatureRejected proves a token signed with the WRONG secret
// is rejected 401, upstream not hit.
//
// Mutation: in authorize, ignore the error from jwt.ParseToken (use the claims
// regardless) -> a forged token would proceed; status becomes 403/200, fails.
func TestAuthInvalidSignatureRejected(t *testing.T) {
	be, hits := hitBackend(t)
	g := authGateway(t, be.URL)

	forged, err := jwt.GenerateToken(
		map[string]interface{}{"scope": "build:write"},
		[]byte("WRONG-secret"), time.Minute)
	if err != nil {
		t.Fatalf("generate forged: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/build/status", nil)
	req.Header.Set("Authorization", "Bearer "+forged)
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for bad signature", rr.Code)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("upstream hits = %d, want 0", n)
	}
}

// TestAuthExpiredTokenRejected proves an expired (negative-duration) but
// correctly-signed token is rejected 401, upstream not hit.
//
// Mutation: in jwt.ValidateToken the expiry check is what flags this; at the
// gateway level, returning nil from authorize when ParseToken errors would let
// it through. Concretely: drop the `if err != nil` 401 branch -> status 200,
// upstream hit==1, fails.
func TestAuthExpiredTokenRejected(t *testing.T) {
	be, hits := hitBackend(t)
	g := authGateway(t, be.URL)

	expired := mustToken(t, map[string]interface{}{"scope": "build:write"}, -time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/build/status", nil)
	req.Header.Set("Authorization", "Bearer "+expired)
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for expired token", rr.Code)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("upstream hits = %d, want 0", n)
	}
}

// TestAuthValidTokenWrongScopeForbidden proves a valid, unexpired, correctly
// signed token that LACKS the required scope is rejected 403, upstream not hit.
//
// Mutation: in authorize, delete the `if _, ok := claimScopes(claims)[required]`
// block (or always return nil after token validation) -> status 200, upstream
// hit==1, fails. This is the test that pins real RBAC, not mere authentication.
func TestAuthValidTokenWrongScopeForbidden(t *testing.T) {
	be, hits := hitBackend(t)
	g := authGateway(t, be.URL)

	// Holds core:read but NOT build:write, which /api/v1/build/ requires.
	token := mustToken(t, map[string]interface{}{"scope": "core:read"}, time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/build/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for wrong scope", rr.Code)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("upstream hits = %d, want 0 (403 must not proxy)", n)
	}
}

// TestAuthScopesFromArrayClaim proves scope extraction also accepts an
// array-valued "scopes" claim (not just the space-delimited string form), so a
// caller authorized via array claims is proxied (hit==1, 200).
//
// Mutation: in claimScopes, drop the []interface{} case -> array scopes are
// invisible, this valid request becomes 403, test fails.
func TestAuthScopesFromArrayClaim(t *testing.T) {
	be, hits := hitBackend(t)
	g := authGateway(t, be.URL)

	token := mustToken(t, map[string]interface{}{
		"scopes": []interface{}{"core:read", "build:write"},
	}, time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/build/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (array scope must authorize)", rr.Code)
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Errorf("upstream hits = %d, want 1", n)
	}
}

// TestAuthDefaultScopeAppliesToUnlistedPrefix proves a path with no explicit
// prefix rule falls back to DefaultScope ("core:read"): a token with only
// build:write is forbidden on /api/v1/node/, while core:read is allowed.
//
// Mutation: in RequiredScope, return "" instead of p.DefaultScope on no match
// -> the build:write-only token would be allowed (200), failing the 403 leg.
func TestAuthDefaultScopeAppliesToUnlistedPrefix(t *testing.T) {
	be, hits := hitBackend(t)
	g, err := NewGateway(WithAuth(AuthPolicy{
		Secret:       authSecret,
		Prefixes:     map[string]string{"/api/v1/build/": "build:write"},
		DefaultScope: "core:read",
	}))
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	tgt, _ := url.Parse(be.URL)
	g.SetProxy("/api/v1/node/", newReverseProxy(tgt))

	// Wrong scope for the default-protected prefix -> 403, no upstream hit.
	wrong := mustToken(t, map[string]interface{}{"scope": "build:write"}, time.Minute)
	reqWrong := httptest.NewRequest(http.MethodGet, "/api/v1/node/n1", nil)
	reqWrong.Header.Set("Authorization", "Bearer "+wrong)
	rrWrong := httptest.NewRecorder()
	g.ServeHTTP(rrWrong, reqWrong)
	if rrWrong.Code != http.StatusForbidden {
		t.Fatalf("wrong-scope status = %d, want 403", rrWrong.Code)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Fatalf("upstream hits after 403 = %d, want 0", n)
	}

	// Correct default scope -> 200, upstream hit.
	ok := mustToken(t, map[string]interface{}{"scope": "core:read"}, time.Minute)
	reqOK := httptest.NewRequest(http.MethodGet, "/api/v1/node/n1", nil)
	reqOK.Header.Set("Authorization", "Bearer "+ok)
	rrOK := httptest.NewRecorder()
	g.ServeHTTP(rrOK, reqOK)
	if rrOK.Code != http.StatusOK {
		t.Fatalf("default-scope status = %d, want 200", rrOK.Code)
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Errorf("upstream hits = %d, want 1", n)
	}
}

// TestAuthDisabledPreservesOpenBehavior proves backward compatibility: a
// gateway built WITHOUT WithAuth proxies an unauthenticated request straight to
// the upstream (hit==1, 200), exactly as before.
//
// Mutation: make authorize reject when g.auth == nil (e.g. drop the early
// `if policy == nil { return nil }`) -> unauthenticated request gets 401,
// upstream hit==0, breaking legacy callers; test fails.
func TestAuthDisabledPreservesOpenBehavior(t *testing.T) {
	be, hits := hitBackend(t)
	g, err := NewGateway() // no auth option
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	tgt, _ := url.Parse(be.URL)
	g.SetProxy("/api/v1/build/", httputil.NewSingleHostReverseProxy(tgt))

	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/build/status", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (open behavior)", rr.Code)
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Errorf("upstream hits = %d, want 1", n)
	}
}

// ── HXC-1213: scheduler scope + X-Helix-Deny-Reason tests ──────────────────

// schedulerAuthGateway wires a Gateway with enforcement on /api/v1/scheduler/
// requiring the "scheduler:write" scope — the exact closure requirement for
// HXC-1213 — using the authSecret shared across auth tests.
func schedulerAuthGateway(t *testing.T, backendURL string) *Gateway {
	t.Helper()
	g, err := NewGateway(WithAuth(AuthPolicy{
		Secret: authSecret,
		Prefixes: map[string]string{
			"/api/v1/scheduler/": "scheduler:write",
		},
		DefaultScope: "core:read",
	}))
	if err != nil {
		t.Fatalf("schedulerAuthGateway: %v", err)
	}
	tgt, _ := url.Parse(backendURL)
	g.SetProxy("/api/v1/scheduler/", newReverseProxy(tgt))
	return g
}

// TestSchedulerNoTokenReturns401BackendNotHit proves that GET /api/v1/scheduler/
// with NO Authorization header is rejected with HTTP 401, and the backend hit
// counter stays at 0 (upstream never contacted).
//
// Per-run UUID: embedded in the request path so a false-proxy would echo it in
// the backend body; the 0-hit assertion is the stronger proof, but we also
// check the UUID is absent from the response body.
//
// Mutation target (§1.1): make authorize() fail-open on missing scope (skip
// the scope check / always return nil) — the missing-token path returns 401
// before scope, so for this test the killing mutation is: return nil instead
// of the 401 authError when the Authorization header is absent. That mutation
// causes status 200 and hits==1, breaking both assertions.
func TestSchedulerNoTokenReturns401BackendNotHit(t *testing.T) {
	const runUUID = "hxc-1213-no-token-test-uuid-a1b2c3"

	be, hits := hitBackend(t)
	g := schedulerAuthGateway(t, be.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/"+runUUID, nil)
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no token)", rr.Code)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("backend hits = %d, want 0 (must reject before proxy)", n)
	}
	// UUID must not appear in the response (backend was not contacted).
	if body := rr.Body.String(); containsStr(body, runUUID) {
		t.Errorf("UUID found in 401 response body (upstream was hit): %q", body)
	}
}

// TestSchedulerWrongScopeReturns403WithDenyReason proves that GET
// /api/v1/scheduler/ with a valid JWT that LACKS "scheduler:write" returns:
//   - HTTP 403
//   - non-empty X-Helix-Deny-Reason response header (exact value asserted)
//   - backend hit counter == 0
//
// This is the PRIMARY closure criterion for HXC-1213: the header must exist and
// its value must describe the reason.
//
// Per-run UUID: embedded in the request path to make a false-proxy detectable.
//
// Mutation target (§1.1): comment out the scope-check block in authorize()
// (lines `if _, ok := claimScopes(claims)[required]; !ok { ... }`) — the
// request now proxies through, status becomes 200, hits becomes 1, and the
// X-Helix-Deny-Reason assertion is never reached. Both the status check and
// the hits check FAIL, proving the mutation is caught.
func TestSchedulerWrongScopeReturns403WithDenyReason(t *testing.T) {
	const runUUID = "hxc-1213-wrong-scope-uuid-d4e5f6"

	be, hits := hitBackend(t)
	g := schedulerAuthGateway(t, be.URL)

	// Token holds "core:read" but /api/v1/scheduler/ requires "scheduler:write".
	token := mustToken(t, map[string]interface{}{"scope": "core:read"}, time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/jobs/"+runUUID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (wrong scope)", rr.Code)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("backend hits = %d, want 0 (403 must not proxy)", n)
	}

	// PRIMARY HXC-1213 assertion: X-Helix-Deny-Reason MUST be present and
	// describe the missing scope. Assert the exact value.
	reason := rr.Header().Get("X-Helix-Deny-Reason")
	if reason == "" {
		t.Fatalf("X-Helix-Deny-Reason header absent on 403 response (HXC-1213 closure criterion)")
	}
	const wantReason = "insufficient scope"
	if reason != wantReason {
		t.Errorf("X-Helix-Deny-Reason = %q, want %q", reason, wantReason)
	}

	// UUID must not appear in the 403 response body.
	if body := rr.Body.String(); containsStr(body, runUUID) {
		t.Errorf("UUID found in 403 body (upstream was hit): %q", body)
	}
}

// TestSchedulerValidTokenProxiedWithGatewayHeader proves that GET
// /api/v1/scheduler/ with a valid JWT that HOLDS "scheduler:write":
//   - is proxied to the upstream (hit counter == 1)
//   - receives HTTP 200
//   - has X-Helix-Gateway: true on the response
//
// Per-run UUID: embedded in the request path and expected in the upstream
// response body. The backend echoes the full path including the UUID, so
// finding the UUID in the body proves the exact request reached the exact
// backend via the gateway's proxy — not a shortcut.
//
// Mutation target (§1.1): see comment on TestSchedulerWrongScopeReturns403
// — a fail-open mutation makes the 403 test pass erroneously; the complementary
// mutation for THIS test is to make authorize() always return a 401/403
// authError, which causes hits==0 and status!=200, failing here.
func TestSchedulerValidTokenProxiedWithGatewayHeader(t *testing.T) {
	const runUUID = "hxc-1213-valid-token-uuid-g7h8i9"

	// Use a backend that echoes the full path so we can assert the UUID
	// round-trip, proving routing actually happened.
	var gotPath string
	echoBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("proxied-path:" + r.URL.Path))
	}))
	t.Cleanup(echoBackend.Close)

	g, err := NewGateway(WithAuth(AuthPolicy{
		Secret: authSecret,
		Prefixes: map[string]string{
			"/api/v1/scheduler/": "scheduler:write",
		},
		DefaultScope: "core:read",
	}))
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	tgt, _ := url.Parse(echoBackend.URL)
	g.SetProxy("/api/v1/scheduler/", newReverseProxy(tgt))

	// Token WITH scheduler:write — must proxy through.
	token := mustToken(t, map[string]interface{}{"scope": "scheduler:write"}, time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/jobs/"+runUUID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (valid scheduler:write token)", rr.Code)
	}

	// X-Helix-Gateway must be present on the proxied response (stamp by ModifyResponse).
	if rr.Header().Get(gatewayHeader) != "true" {
		t.Errorf("missing %s marker on proxied response", gatewayHeader)
	}

	// UUID round-trip: the backend echoed the path, which includes the UUID.
	wantBody := "proxied-path:/api/v1/scheduler/jobs/" + runUUID
	if body := rr.Body.String(); body != wantBody {
		t.Errorf("body = %q, want %q (UUID did not round-trip through proxy)", body, wantBody)
	}
	// Also assert the backend's captured path so any routing bypass is visible.
	if gotPath != "/api/v1/scheduler/jobs/"+runUUID {
		t.Errorf("backend received path %q, want /api/v1/scheduler/jobs/%s", gotPath, runUUID)
	}
}

// containsStr is a test helper that avoids importing strings in test files
// that don't otherwise need it.
func containsStr(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) &&
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}()
}

// TestAuthLongestPrefixWins proves RequiredScope picks the most specific rule:
// a more specific sub-prefix overrides a broader one.
//
// Mutation: in compile, sort ascending (shortest-first) instead of descending
// -> the broad rule would shadow the specific one and RequiredScope returns the
// wrong scope; the specific-scope token below would be 403, test fails.
func TestAuthLongestPrefixWins(t *testing.T) {
	p := AuthPolicy{
		Prefixes: map[string]string{
			"/api/v1/build/":       "build:read",
			"/api/v1/build/admin/": "build:admin",
		},
		DefaultScope: "core:read",
	}
	p.compile()

	if got := p.RequiredScope("/api/v1/build/admin/wipe"); got != "build:admin" {
		t.Errorf("RequiredScope(admin path) = %q, want build:admin", got)
	}
	if got := p.RequiredScope("/api/v1/build/status"); got != "build:read" {
		t.Errorf("RequiredScope(build path) = %q, want build:read", got)
	}
	if got := p.RequiredScope("/api/v1/other/x"); got != "core:read" {
		t.Errorf("RequiredScope(unlisted) = %q, want default core:read", got)
	}
}
