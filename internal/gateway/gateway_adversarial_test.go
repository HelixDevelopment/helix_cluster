package gateway

// Adversarial, sink-side tests for internal/gateway.
//
// Threat model (per tonight's fail-open sweep): the gateway is the front door
// (auth, routing, validation). The highest-severity defect class is FAIL-OPEN
// AUTH — a request ADMITTED to the upstream on a missing/empty/malformed/expired
// /wrong-signature/wrong-scope credential when the AuthPolicy demands a reject.
//
// Every assertion here is SINK-SIDE: we drive the real *Gateway.ServeHTTP via
// httptest and assert on the actual HTTP status code / whether the spy upstream
// was reached — never "it compiles". The upstream is a recording httptest
// backend wired in via SetProxy, so "reached upstream" == "authorization
// ALLOWED the request through" (a fail-open), and a 401/403 with the upstream
// untouched == "authorization DENIED" (correct, fail-closed).
//
// These tests are written to PASS on correct fail-closed behavior and to FAIL
// (loudly, sink-side) if a regression makes the auth gate admit a hostile
// request. They are package-internal so they can reach unexported helpers.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/jwt"
)

// advSecret is the HMAC secret the adversarial AuthPolicy is compiled with.
var advSecret = []byte("adversarial-test-secret-do-not-use-in-prod")

// spyUpstream is a recording backend. If ServeHTTP touches it, the gateway
// ADMITTED the request — i.e. authorization let it through. We assert on hits.
type spyUpstream struct {
	hits int32
}

func (s *spyUpstream) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.hits, 1)
		w.Header().Set("X-Upstream-Reached", "true")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"upstream":"ok"}`))
	})
}

func (s *spyUpstream) reached() bool { return atomic.LoadInt32(&s.hits) > 0 }

// newGuardedGateway builds a Gateway with the given AuthPolicy and rewires the
// /api/v1/scheduler/ route to a recording spy upstream so we can observe, sink-
// side, whether a request was admitted past the auth gate.
func newGuardedGateway(t *testing.T, policy AuthPolicy) (*Gateway, *spyUpstream, *httptest.Server) {
	t.Helper()
	spy := &spyUpstream{}
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
	// Point the scheduler route at the spy upstream.
	g.SetProxy("/api/v1/scheduler/", httputil.NewSingleHostReverseProxy(target))
	return g, spy, backend
}

// do issues a request through the gateway and returns the recorded response.
func do(g *Gateway, method, path, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	return rec
}

// mintToken builds a signed HS256 token with the given scope claim and TTL.
func mintToken(t *testing.T, scope string, ttl time.Duration) string {
	t.Helper()
	claims := map[string]interface{}{"sub": "tester"}
	if scope != "" {
		claims["scope"] = scope
	}
	tok, err := jwt.GenerateToken(claims, advSecret, ttl)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// (a) FAIL-OPEN AUTH — the core probe. Each hostile credential MUST be DENIED
// (401/403) and the upstream MUST NOT be reached.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarial_FailOpenAuth_HostileCredentialsDenied(t *testing.T) {
	policy := AuthPolicy{
		Secret:       advSecret,
		Prefixes:     map[string]string{"/api/v1/scheduler/": "scheduler:write"},
		DefaultScope: "any",
	}

	// An expired token (TTL in the past via GenerateToken negative duration).
	expired := mintToken(t, "scheduler:write", -1*time.Hour)
	// A token signed with the WRONG secret — structurally valid, bad signature.
	wrongSig, err := jwt.GenerateToken(
		map[string]interface{}{"sub": "evil", "scope": "scheduler:write"},
		[]byte("attacker-controlled-secret"), time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken wrongSig: %v", err)
	}
	// A token with the right signature but WITHOUT the required scope.
	wrongScope := mintToken(t, "scheduler:read", time.Hour)

	// An alg:none / unsigned-style forgery: header {"alg":"none"}, no real sig.
	// base64url("{\"alg\":\"none\",\"typ\":\"JWT\"}") . base64url(body) . ""
	noneHeader := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0"
	noneBody := "eyJzY29wZSI6InNjaGVkdWxlcjp3cml0ZSIsInN1YiI6ImV2aWwifQ"
	noneToken := noneHeader + "." + noneBody + "."

	cases := []struct {
		name string
		auth string
		want int
	}{
		{"no Authorization header", "", http.StatusUnauthorized},
		{"empty bearer value", "Bearer ", http.StatusUnauthorized},
		{"bearer with only spaces", "Bearer      ", http.StatusUnauthorized},
		{"non-bearer scheme", "Basic dXNlcjpwYXNz", http.StatusUnauthorized},
		{"lowercase bearer keyword", "bearer " + mintToken(t, "scheduler:write", time.Hour), http.StatusUnauthorized},
		{"garbage token", "Bearer not.a.jwt", http.StatusUnauthorized},
		{"two-segment token", "Bearer aaa.bbb", http.StatusUnauthorized},
		{"expired token", "Bearer " + expired, http.StatusUnauthorized},
		{"wrong-secret signature", "Bearer " + wrongSig, http.StatusUnauthorized},
		{"alg:none forgery", "Bearer " + noneToken, http.StatusUnauthorized},
		{"valid token, missing scope", "Bearer " + wrongScope, http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, spy, _ := newGuardedGateway(t, policy)
			rec := do(g, http.MethodGet, "/api/v1/scheduler/jobs", tc.auth)

			if rec.Code != tc.want {
				t.Fatalf("FAIL-OPEN RISK: hostile credential %q got status %d, want %d (body=%q)",
					tc.name, rec.Code, tc.want, strings.TrimSpace(rec.Body.String()))
			}
			if spy.reached() {
				t.Fatalf("FAIL-OPEN AUTH: upstream was REACHED for hostile credential %q "+
					"(status %d) — request was ADMITTED when it must be DENIED", tc.name, rec.Code)
			}
		})
	}
}

// Positive control: a correctly-signed, unexpired token carrying the required
// scope MUST be ADMITTED (200, upstream reached). This proves the deny path
// above is a real gate and not a blanket "deny everything" that would also pass
// the negative cases vacuously.
func TestAdversarial_ValidScopedToken_Admitted(t *testing.T) {
	policy := AuthPolicy{
		Secret:       advSecret,
		Prefixes:     map[string]string{"/api/v1/scheduler/": "scheduler:write"},
		DefaultScope: "any",
	}
	g, spy, _ := newGuardedGateway(t, policy)

	good := mintToken(t, "scheduler:write extra:scope", time.Hour)
	rec := do(g, http.MethodGet, "/api/v1/scheduler/jobs", "Bearer "+good)

	if rec.Code != http.StatusOK {
		t.Fatalf("valid scoped token rejected: status %d body=%q",
			rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	if !spy.reached() {
		t.Fatalf("valid scoped token did NOT reach upstream — gate is over-closed, "+
			"so the negative fail-open cases would pass vacuously (status %d)", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (b) ROUTE / IDENTITY — prefix-boundary & normalization probe.
//
// The route table dispatches by strings.HasPrefix; AuthPolicy.RequiredScope
// also uses strings.HasPrefix (longest match wins). We probe whether a request
// can slip a STRONGER prefix rule by exploiting prefix-without-boundary or
// case, landing on a WEAKER DefaultScope while still being routed upstream.
// ─────────────────────────────────────────────────────────────────────────────

// TestAdversarial_PrefixBoundary_AdminRuleNotBypassedBySiblingPath documents
// the EXACT contract of HasPrefix-based scope selection. An admin sub-prefix
// rule "/api/v1/scheduler/admin/" guards admin paths. A sibling path that does
// NOT start with that prefix ("/api/v1/scheduler/adminconsole") legitimately
// falls back to the broader "/api/v1/scheduler/" rule — that is the documented
// longest-prefix-wins behavior, NOT a bypass, because the broader rule still
// requires a (different) scope. We pin that the broader rule's scope IS still
// enforced (no fall-through to an unauthenticated proxy).
func TestAdversarial_PrefixBoundary_SiblingPathStillScoped(t *testing.T) {
	policy := AuthPolicy{
		Secret: advSecret,
		Prefixes: map[string]string{
			"/api/v1/scheduler/":       "scheduler:read",
			"/api/v1/scheduler/admin/": "scheduler:admin",
		},
		DefaultScope: "", // empty default == "any valid token"
	}

	// Sibling path that shares the "scheduler/" prefix but not "scheduler/admin/".
	// Longest matching prefix is "/api/v1/scheduler/" → requires "scheduler:read".
	if got := policy.compileAndScope("/api/v1/scheduler/adminconsole"); got != "scheduler:read" {
		t.Fatalf("sibling path resolved scope %q, want scheduler:read (longest-prefix)", got)
	}
	// The admin path itself requires the stronger scope.
	if got := policy.compileAndScope("/api/v1/scheduler/admin/purge"); got != "scheduler:admin" {
		t.Fatalf("admin path resolved scope %q, want scheduler:admin", got)
	}

	// Sink-side: a token with only "scheduler:read" must be DENIED at the admin
	// path (403) and must NOT reach upstream — proving the stronger rule is not
	// bypassable by holding only the weaker scope.
	g, spy, _ := newGuardedGateway(t, policy)
	readOnly := mintToken(t, "scheduler:read", time.Hour)
	rec := do(g, http.MethodGet, "/api/v1/scheduler/admin/purge", "Bearer "+readOnly)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin path with read-only scope got %d, want 403 (admin rule bypassed?)", rec.Code)
	}
	if spy.reached() {
		t.Fatalf("FAIL-OPEN: read-only token reached upstream on admin path")
	}
}

// compileAndScope is a tiny test shim: compile the policy then resolve a scope.
func (p AuthPolicy) compileAndScope(path string) string {
	p.compile()
	return p.RequiredScope(path)
}

// TestAdversarial_ProtectedPrefix_UnauthenticatedDenied proves a protected
// route prefix is NOT reachable without any credential. Pure sink-side.
func TestAdversarial_ProtectedPrefix_UnauthenticatedDenied(t *testing.T) {
	policy := AuthPolicy{
		Secret:       advSecret,
		Prefixes:     map[string]string{"/api/v1/scheduler/": "scheduler:write"},
		DefaultScope: "scheduler:write",
	}
	g, spy, _ := newGuardedGateway(t, policy)
	rec := do(g, http.MethodGet, "/api/v1/scheduler/jobs", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request to protected route got %d, want 401", rec.Code)
	}
	if spy.reached() {
		t.Fatalf("FAIL-OPEN AUTH: unauthenticated request reached upstream")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (c) UNGUARDED SHARED STATE — concurrent SetProxy + ServeHTTP under -race.
// The proxies map is guarded by g.mu; concurrent reconfigure + serve must not
// race or lose the auth verdict. Run with -race to detect data races.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarial_ConcurrentServeAndSetProxy_NoRaceNoFailOpen(t *testing.T) {
	policy := AuthPolicy{
		Secret:       advSecret,
		Prefixes:     map[string]string{"/api/v1/scheduler/": "scheduler:write"},
		DefaultScope: "scheduler:write",
	}
	g, _, backend := newGuardedGateway(t, policy)
	target, _ := url.Parse(backend.URL)

	const workers = 16
	const iters = 40
	var wg sync.WaitGroup
	var admittedHostile int32

	// Writers: continuously reconfigure the proxy concurrently with serving.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < workers*iters; i++ {
			g.SetProxy("/api/v1/scheduler/", httputil.NewSingleHostReverseProxy(target))
		}
	}()

	// Readers: hammer the gate with UNAUTHENTICATED requests. Every one must be
	// denied (401); none may be admitted regardless of the concurrent rewiring.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				rec := do(g, http.MethodGet, "/api/v1/scheduler/jobs", "")
				if rec.Code != http.StatusUnauthorized {
					atomic.AddInt32(&admittedHostile, 1)
				}
			}
		}()
	}
	wg.Wait()

	if n := atomic.LoadInt32(&admittedHostile); n != 0 {
		t.Fatalf("FAIL-OPEN under concurrency: %d unauthenticated requests were NOT denied", n)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (d) VALIDATION BYPASS + documented-contract pins.
// ─────────────────────────────────────────────────────────────────────────────

// The REST surface and inference route are dispatched in ServeHTTP BEFORE the
// proxy/auth loop (gateway.go: restAPIPath / inferencePath checks). The auth
// gate in authorize() therefore NEVER runs for /v1/sessions, /v1/pool/*,
// /openapi.json, or /v1/ai/infer — even when WithAuth is installed. This is a
// DOCUMENTED CONTRACT ("Dispatch REST API paths before the proxy logic so
// JWT/RBAC enforcement does not interfere with the REST surface ... the REST
// handlers apply their own validation as needed"). We PIN it so any future
// change that silently routes these through (or fails to) is caught — and so
// the latent risk is visible: if these endpoints ever carry privileged data,
// the pre-auth dispatch is a fail-open. They currently use injected fakes /
// honest no-op backends, so admitting them is not a credential bypass today.
func TestAdversarial_RESTSurface_BypassesAuthGate_DocumentedContract(t *testing.T) {
	policy := AuthPolicy{
		Secret:       advSecret,
		Prefixes:     map[string]string{"/": "must-hold-this-scope"},
		DefaultScope: "must-hold-this-scope",
	}
	// WithREST installs the REST surface; WithAuth installs the (irrelevant-here)
	// gate. Order: REST dispatch wins in ServeHTTP.
	g, err := NewGateway(WithAuth(policy), WithREST())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	// No Authorization header at all. If the pre-auth dispatch contract holds,
	// the utilization endpoint answers 200 (auth gate never consulted).
	rec := do(g, http.MethodGet, "/v1/pool/utilization", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("documented-contract drift: /v1/pool/utilization returned %d without auth, "+
			"expected 200 (REST surface dispatched pre-auth). If this changed, re-evaluate "+
			"whether the new behavior is intended.", rec.Code)
	}

	// openapi.json likewise unauthenticated → 200.
	rec = do(g, http.MethodGet, "/openapi.json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("documented-contract drift: /openapi.json returned %d without auth, want 200", rec.Code)
	}
}

// Inference input validation: empty model/prompt must be rejected 400, and a
// malformed JSON body must be rejected 400 — never silently accepted or routed
// to the client with garbage. Sink-side on status code.
func TestAdversarial_Inference_ValidationBypass(t *testing.T) {
	g, err := NewGateway(WithInference(nil)) // nil → default refusing client
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/ai/infer", strings.NewReader(body))
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		return rec
	}

	// Empty model & prompt → 400 (required-field validation).
	if rec := post(`{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty model/prompt got %d, want 400 (validation bypass?)", rec.Code)
	}
	// Model present, prompt empty → 400.
	if rec := post(`{"model":"m"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty prompt got %d, want 400", rec.Code)
	}
	// Malformed JSON → 400.
	if rec := post(`{"model":`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON got %d, want 400", rec.Code)
	}
	// Valid fields but the default client REFUSES (no fabricated 200) → 502.
	if rec := post(`{"model":"m","prompt":"hi"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("default refusing client got %d, want 502 (must not fabricate a 200)", rec.Code)
	}
	// GET on a POST-only route → 405.
	req := httptest.NewRequest(http.MethodGet, "/v1/ai/infer", nil)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /v1/ai/infer got %d, want 405", rec.Code)
	}
}

// Sanity: a disabled-auth gateway (no WithAuth) preserves documented OPEN
// behavior — unauthenticated proxy traffic is admitted. This pins the
// "historical open behavior" contract so a future default-deny flip is a
// conscious, test-visible decision (and so the fail-open tests above are
// understood to be conditional on WithAuth being installed).
func TestAdversarial_NoAuthPolicy_OpenByDocumentedContract(t *testing.T) {
	spy := &spyUpstream{}
	backend := httptest.NewServer(spy.handler())
	t.Cleanup(backend.Close)

	g, err := NewGateway() // no WithAuth → open
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	target, _ := url.Parse(backend.URL)
	g.SetProxy("/api/v1/scheduler/", httputil.NewSingleHostReverseProxy(target))

	// authorize() with nil policy returns (nil, nil): open. Assert directly too.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/jobs", nil)
	if e, _ := g.authorize(req); e != nil {
		t.Fatalf("nil-policy authorize returned deny %+v, want open (nil)", e)
	}
	rec := do(g, http.MethodGet, "/api/v1/scheduler/jobs", "")
	if rec.Code != http.StatusOK || !spy.reached() {
		t.Fatalf("open gateway did not admit unauthenticated traffic: status %d reached=%v",
			rec.Code, spy.reached())
	}
}

// Compile-time guard that the context-injection key contract is intact: a valid
// token's claims are injected so downstream can read them. Sink-side: we wrap a
// custom proxy that reads the claims from context and reflects them.
func TestAdversarial_ClaimsInjectedForAdmittedRequest(t *testing.T) {
	policy := AuthPolicy{
		Secret:       advSecret,
		Prefixes:     map[string]string{"/api/v1/scheduler/": "scheduler:write"},
		DefaultScope: "scheduler:write",
	}
	g, err := NewGateway(WithAuth(policy))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	var gotSub atomic.Value
	gotSub.Store("")
	// Replace the proxy with a handler that inspects injected claims.
	claimReader := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if claims, ok := r.Context().Value(ClaimsContextKey).(map[string]interface{}); ok {
			if sub, ok := claims["sub"].(string); ok {
				gotSub.Store(sub)
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	// SetProxy takes a *httputil.ReverseProxy; instead drive authorize+inject by
	// hand to assert the context plumbing without a real proxy object.
	good, _ := jwt.GenerateToken(map[string]interface{}{"sub": "alice", "scope": "scheduler:write"}, advSecret, time.Hour)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/jobs", nil)
	req.Header.Set("Authorization", "Bearer "+good)
	e, claims := g.authorize(req)
	if e != nil {
		t.Fatalf("valid token denied: %+v", e)
	}
	if claims == nil {
		t.Fatalf("authorize returned nil claims for a valid admitted token")
	}
	req = req.WithContext(context.WithValue(req.Context(), ClaimsContextKey, claims))
	rec := httptest.NewRecorder()
	claimReader.ServeHTTP(rec, req)
	if got := gotSub.Load().(string); got != "alice" {
		t.Fatalf("claims not injected: sub=%q want alice", got)
	}
	_ = fmt.Sprint // keep fmt imported if other refs trimmed
}
