package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	neturl "net/url"
	"strings"
	"testing"
)

// ── Fakes ─────────────────────────────────────────────────────────────────────

// fakeSessionBackend is a deterministic in-process SessionBackend that returns
// a predictable ID derived from a counter, so tests can assert the exact value.
type fakeSessionBackend struct {
	counter int
	// lastSpec records the most-recent spec to verify round-trip.
	lastSpec SessionSpec
}

func (f *fakeSessionBackend) CreateSession(spec SessionSpec) (string, error) {
	f.counter++
	f.lastSpec = spec
	return fmt.Sprintf("fake-session-%04d", f.counter), nil
}

// fakeSessionBackendError always returns an error — used to test the 500 path.
type fakeSessionBackendError struct{}

func (fakeSessionBackendError) CreateSession(_ SessionSpec) (string, error) {
	return "", fmt.Errorf("backend exploded")
}

// fakePoolBackend returns a fixed, non-zero utilization so numeric-field
// assertions are unambiguous.
type fakePoolBackend struct {
	cpu, mem, gpu float64
}

func (f fakePoolBackend) Utilization() (PoolUtilization, error) {
	return PoolUtilization{CPUPct: f.cpu, MemPct: f.mem, GPUPct: f.gpu}, nil
}

// fakePoolBackendError always fails — tests the 500 path.
type fakePoolBackendError struct{}

func (fakePoolBackendError) Utilization() (PoolUtilization, error) {
	return PoolUtilization{}, fmt.Errorf("pool backend down")
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// newTestGatewayWithREST returns a Gateway with the REST surface attached and
// the supplied fake backends injected.
func newTestGatewayWithREST(sessions SessionBackend, pool PoolBackend) *Gateway {
	g, err := NewGateway(WithREST(
		WithSessionBackend(sessions),
		WithPoolBackend(pool),
	))
	if err != nil {
		panic(fmt.Sprintf("newTestGatewayWithREST: %v", err))
	}
	return g
}

// ── POST /v1/sessions ────────────────────────────────────────────────────────

// TestPostSessionsReturns201WithIDAndCreating is the primary sink-side test for
// HXC-1118: the handler MUST return exactly 201 Created, a non-empty id, and
// status=="CREATING".
//
// §1.1 mutation: change w.WriteHeader(http.StatusCreated) to
// w.WriteHeader(http.StatusOK) → status becomes 200, this test fails.
func TestPostSessionsReturns201WithIDAndCreating(t *testing.T) {
	fake := &fakeSessionBackend{}
	g := newTestGatewayWithREST(fake, fakePoolBackend{cpu: 10, mem: 20, gpu: 5})

	body := strings.NewReader(`{"resource":{"gpu":"A100","count":2}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	g.ServeHTTP(rr, req)

	// ── status must be 201 ────────────────────────────────────────────────────
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 Created", rr.Code)
	}

	// ── decode response body ──────────────────────────────────────────────────
	var resp SessionResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v (body=%q)", err, rr.Body.String())
	}

	// ── sink-side: id must be non-empty ───────────────────────────────────────
	if resp.ID == "" {
		t.Errorf("id is empty, want non-empty session id")
	}

	// ── sink-side: status must be exactly "CREATING" ──────────────────────────
	// §1.1 mutation: omit the Status field assignment → status=="" → test fails.
	if resp.Status != "CREATING" {
		t.Errorf("status = %q, want CREATING", resp.Status)
	}
}

// TestPostSessionsIDMatchesFakeBackend proves the id in the response is exactly
// what the backend returned — not a random/different value.
//
// §1.1 mutation: hardcode resp.ID="hardcoded" instead of using backend return
// → id != "fake-session-0001" → test fails.
func TestPostSessionsIDMatchesFakeBackend(t *testing.T) {
	fake := &fakeSessionBackend{}
	g := newTestGatewayWithREST(fake, fakePoolBackend{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rr.Code)
	}
	var resp SessionResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := "fake-session-0001"
	if resp.ID != want {
		t.Errorf("id = %q, want %q", resp.ID, want)
	}
}

// TestPostSessionsSpecRoundTrips proves the resource spec supplied in the
// request body is echoed back inside the response.
func TestPostSessionsSpecRoundTrips(t *testing.T) {
	fake := &fakeSessionBackend{}
	g := newTestGatewayWithREST(fake, fakePoolBackend{})

	body := strings.NewReader(`{"resource":{"kind":"gpu","count":4}}`)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rr.Code)
	}
	var resp SessionResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Spec == nil {
		t.Fatal("spec is nil, want echoed resource")
	}
	// Verify the resource fields round-tripped correctly.
	kind, _ := resp.Spec["kind"].(string)
	if kind != "gpu" {
		t.Errorf("spec.kind = %q, want gpu", kind)
	}
	count, _ := resp.Spec["count"].(float64) // JSON numbers decode as float64.
	if count != 4 {
		t.Errorf("spec.count = %v, want 4", count)
	}
}

// TestPostSessionsBadJSONReturns400 proves malformed request JSON yields 400.
func TestPostSessionsBadJSONReturns400(t *testing.T) {
	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackend{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{bad json`))
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for bad JSON", rr.Code)
	}
}

// TestPostSessionsBackendErrorReturns500 proves a backend failure yields 500.
func TestPostSessionsBackendErrorReturns500(t *testing.T) {
	g := newTestGatewayWithREST(fakeSessionBackendError{}, fakePoolBackend{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for backend error", rr.Code)
	}
}

// TestPostSessionsWrongMethodReturns405 proves GET /v1/sessions → 405.
func TestPostSessionsWrongMethodReturns405(t *testing.T) {
	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackend{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

// TestPostSessionsContentTypeIsJSON proves response carries application/json.
func TestPostSessionsContentTypeIsJSON(t *testing.T) {
	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackend{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
	g.ServeHTTP(rr, req)

	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// ── GET /v1/pool/utilization ─────────────────────────────────────────────────

// TestGetPoolUtilizationReturns200WithNumericFields is the primary sink-side
// test for the utilization endpoint.
//
// §1.1 mutation: change CPUPct to a string field → json.Decode fails or the
// value is not a number → the numeric-assertion fails.
func TestGetPoolUtilizationReturns200WithNumericFields(t *testing.T) {
	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackend{cpu: 42.5, mem: 67.3, gpu: 11.1})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/pool/utilization", nil)
	g.ServeHTTP(rr, req)

	// ── status must be 200 ────────────────────────────────────────────────────
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	// ── decode into the concrete struct ───────────────────────────────────────
	var util PoolUtilization
	if err := json.NewDecoder(rr.Body).Decode(&util); err != nil {
		t.Fatalf("decode body: %v (body=%q)", err, rr.Body.String())
	}

	// ── sink-side: all three fields must be present as numbers ────────────────
	if util.CPUPct != 42.5 {
		t.Errorf("cpu_pct = %v, want 42.5", util.CPUPct)
	}
	if util.MemPct != 67.3 {
		t.Errorf("mem_pct = %v, want 67.3", util.MemPct)
	}
	if util.GPUPct != 11.1 {
		t.Errorf("gpu_pct = %v, want 11.1", util.GPUPct)
	}
}

// TestGetPoolUtilizationRawJSONHasNumericFields decodes via a
// map[string]interface{} (the raw JSON layer) to confirm the wire format
// carries numbers, not strings or null — covering the scenario where a
// caller does not use the Go struct.
//
// §1.1 mutation: change the json tag on CPUPct to omitempty and set value 0
// → the field would be absent → the "ok" assertion fails.
func TestGetPoolUtilizationRawJSONHasNumericFields(t *testing.T) {
	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackend{cpu: 1.0, mem: 2.0, gpu: 3.0})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/pool/utilization", nil)
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var raw map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, field := range []string{"cpu_pct", "mem_pct", "gpu_pct"} {
		v, ok := raw[field]
		if !ok {
			t.Errorf("field %q absent in response JSON", field)
			continue
		}
		if _, isNum := v.(float64); !isNum {
			t.Errorf("field %q = %T(%v), want float64 number", field, v, v)
		}
	}
}

// TestGetPoolUtilizationBackendErrorReturns500 proves a backend failure yields 500.
func TestGetPoolUtilizationBackendErrorReturns500(t *testing.T) {
	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackendError{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/pool/utilization", nil)
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for backend error", rr.Code)
	}
}

// TestGetPoolUtilizationWrongMethodReturns405 proves POST /v1/pool/utilization → 405.
func TestGetPoolUtilizationWrongMethodReturns405(t *testing.T) {
	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackend{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/pool/utilization", nil)
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

// ── GET /openapi.json ─────────────────────────────────────────────────────────

// TestOpenAPIDocServedAndValid is the primary test for OpenAPI 3.0 conformance.
//
// It asserts:
//  1. GET /openapi.json → 200.
//  2. The body parses as valid JSON.
//  3. The "openapi" field starts with "3.0" (OpenAPI 3.0.x version).
//  4. Both required paths (/v1/sessions POST and /v1/pool/utilization GET) are
//     present.
//
// §1.1 mutation: change the version field to "2.0.0" → the version-prefix check
// fails. Second mutation: remove /v1/sessions from the paths object → the path
// presence check fails.
func TestOpenAPIDocServedAndValid(t *testing.T) {
	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackend{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	g.ServeHTTP(rr, req)

	// ── status must be 200 ────────────────────────────────────────────────────
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for /openapi.json", rr.Code)
	}

	// ── body must parse as JSON ───────────────────────────────────────────────
	body, _ := io.ReadAll(rr.Body)
	var doc map[string]interface{}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v\nbody: %s", err, body)
	}

	// ── "openapi" field must be "3.0.x" ──────────────────────────────────────
	version, _ := doc["openapi"].(string)
	if !strings.HasPrefix(version, "3.0") {
		t.Errorf("openapi version = %q, want 3.0.x", version)
	}

	// ── /v1/sessions path must be present ────────────────────────────────────
	paths, _ := doc["paths"].(map[string]interface{})
	if paths == nil {
		t.Fatal("openapi doc missing 'paths' object")
	}
	if _, ok := paths["/v1/sessions"]; !ok {
		t.Errorf("/v1/sessions not found in openapi paths; keys = %v", pathKeys(paths))
	}

	// ── /v1/pool/utilization path must be present ─────────────────────────────
	if _, ok := paths["/v1/pool/utilization"]; !ok {
		t.Errorf("/v1/pool/utilization not found in openapi paths; keys = %v", pathKeys(paths))
	}
}

// TestOpenAPIWrongMethodReturns405 proves POST /openapi.json → 405.
func TestOpenAPIWrongMethodReturns405(t *testing.T) {
	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackend{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openapi.json", nil)
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

// ── Mutation-proof boundary tests ─────────────────────────────────────────────

// TestMutation_201vs200_SessionsEndpoint is the explicit §1.1 mutation record:
// the handler MUST return 201, not 200.  This test is the killing assertion for
// that mutation class.
func TestMutation_201vs200_SessionsEndpoint(t *testing.T) {
	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackend{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
	g.ServeHTTP(rr, req)

	if rr.Code == http.StatusOK {
		t.Fatalf("handler returned 200 instead of 201 — §1.1 mutation would pass; it must not")
	}
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rr.Code)
	}
}

// TestMutation_StatusFieldAbsent_SessionsEndpoint is the explicit §1.1 record
// for the "drop the status field" mutation: decoding the response into a raw
// map asserts the key is present and equals "CREATING".
func TestMutation_StatusFieldAbsent_SessionsEndpoint(t *testing.T) {
	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackend{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
	g.ServeHTTP(rr, req)

	var raw map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	status, ok := raw["status"]
	if !ok {
		t.Fatal("'status' key absent in response — mutation dropped the field")
	}
	if status != "CREATING" {
		t.Errorf("status = %v, want CREATING — mutation changed the value", status)
	}
}

// ── REST paths do not interfere with /health ────────────────────────────────

// TestRESTDoesNotBreakHealthEndpoint proves /health still works after attaching
// the REST surface.
func TestRESTDoesNotBreakHealthEndpoint(t *testing.T) {
	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackend{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"status":"healthy"`) {
		t.Errorf("health body = %q, want healthy JSON", rr.Body.String())
	}
}

// TestRESTDoesNotBreakProxyRoutes proves the proxy prefix routes still work
// after attaching the REST surface.
func TestRESTDoesNotBreakProxyRoutes(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "proxy-ok")
	}))
	defer be.Close()

	g := newTestGatewayWithREST(&fakeSessionBackend{}, fakePoolBackend{})

	tgt, _ := neturl.Parse(be.URL)
	g.SetProxy("/api/v1/build/", httputil.NewSingleHostReverseProxy(tgt))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/build/status", nil)
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("proxy route status = %d, want 200 after REST surface attached", rr.Code)
	}
	if rr.Body.String() != "proxy-ok" {
		t.Errorf("proxy route body = %q, want proxy-ok", rr.Body.String())
	}
}

// ── pathKeys helper ──────────────────────────────────────────────────────────

func pathKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
