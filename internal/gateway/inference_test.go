package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeChutesClient is an in-process Chutes inference client with REAL state.
// It is NOT a network mock: it records the exact (model, prompt, teeRequired)
// it was handed and returns a real canned completion, so a test can prove the
// gateway actually routed the request to it and propagated the result back to
// the HTTP response (CLAUDE-1 sink-side evidence). When failErr is set it
// surfaces a failure so the no-fabrication path can be exercised.
type fakeChutesClient struct {
	mu sync.Mutex

	// gotModel/gotPrompt/gotTEE capture the last call's arguments.
	gotModel  string
	gotPrompt string
	gotTEE    bool
	calls     int

	// completion is the canned answer returned on success.
	completion string
	// failErr, when non-nil, is returned instead of a completion.
	failErr error
}

func (f *fakeChutesClient) Complete(model, prompt string, teeRequired bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.gotModel = model
	f.gotPrompt = prompt
	f.gotTEE = teeRequired
	if f.failErr != nil {
		return "", f.failErr
	}
	return f.completion, nil
}

// newInferenceGateway builds a real Gateway with the inference route wired to
// the supplied in-process client, fronted by httptest — exactly how a consumer
// reaches the feature end to end.
func newInferenceGateway(t *testing.T, client ChutesInferenceClient) *httptest.Server {
	t.Helper()
	g, err := NewGateway(WithInference(client))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return srv
}

func postInfer(t *testing.T, srv *httptest.Server, body InferenceRequest) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(srv.URL+"/v1/ai/infer", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /v1/ai/infer: %v", err)
	}
	return resp
}

// TestInfer_RoutesToClientAndReturnsCompletion proves an internal AI request
// through the gateway reaches the injected client and the gateway returns the
// client's real model response in the body (closure criterion #1).
func TestInfer_RoutesToClientAndReturnsCompletion(t *testing.T) {
	const wantCompletion = "the-real-canned-model-answer-42"
	client := &fakeChutesClient{completion: wantCompletion}
	srv := newInferenceGateway(t, client)

	resp := postInfer(t, srv, InferenceRequest{Model: "chutes/deepseek-r1", Prompt: "hello"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got InferenceResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Sink-side: the body must carry the client's real completion, not a stub.
	if got.Completion != wantCompletion {
		t.Fatalf("completion = %q, want %q", got.Completion, wantCompletion)
	}
	if got.Model != "chutes/deepseek-r1" {
		t.Fatalf("model = %q, want chutes/deepseek-r1", got.Model)
	}
	// Source-side: the client actually received the request we sent.
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.calls != 1 {
		t.Fatalf("client.calls = %d, want 1", client.calls)
	}
	if client.gotPrompt != "hello" || client.gotModel != "chutes/deepseek-r1" {
		t.Fatalf("client got model=%q prompt=%q, want chutes/deepseek-r1/hello", client.gotModel, client.gotPrompt)
	}
}

// TestInfer_TEERoutingMarker proves a TEERequired request shows X-E2EE-Enabled
// set (and routes through the E2EE path) while a non-TEE request does not
// (closure criterion #2). This is the mutation-guard test: removing the
// `if req.TEERequired { set X-E2EE-Enabled }` branch flips both sub-cases to
// the same value and fails here.
func TestInfer_TEERoutingMarker(t *testing.T) {
	tests := []struct {
		name        string
		teeRequired bool
		wantMarker  string // "" means header must be absent
		wantBodyE2E bool
	}{
		{name: "tee required sets marker", teeRequired: true, wantMarker: "true", wantBodyE2E: true},
		{name: "non-tee omits marker", teeRequired: false, wantMarker: "", wantBodyE2E: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeChutesClient{completion: "ok"}
			srv := newInferenceGateway(t, client)

			resp := postInfer(t, srv, InferenceRequest{
				Model:       "m",
				Prompt:      "p",
				TEERequired: tc.teeRequired,
			})
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			gotMarker := resp.Header.Get(e2eeEnabledHeader)
			if gotMarker != tc.wantMarker {
				t.Fatalf("%s header = %q, want %q", e2eeEnabledHeader, gotMarker, tc.wantMarker)
			}
			var body InferenceResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.E2EEEnabled != tc.wantBodyE2E {
				t.Fatalf("body.E2EEEnabled = %v, want %v", body.E2EEEnabled, tc.wantBodyE2E)
			}
			// The E2EE-wrapping path must propagate teeRequired to the client.
			client.mu.Lock()
			gotTEE := client.gotTEE
			client.mu.Unlock()
			if gotTEE != tc.teeRequired {
				t.Fatalf("client.gotTEE = %v, want %v", gotTEE, tc.teeRequired)
			}
		})
	}
}

// TestInfer_ClientFailureSurfacesError proves a client failure becomes an error
// status (not a fake 200 with an invented answer) (closure criterion #3 /
// CLAUDE-1 anti-bluff).
func TestInfer_ClientFailureSurfacesError(t *testing.T) {
	client := &fakeChutesClient{failErr: &InferenceError{Reason: "upstream 503"}}
	srv := newInferenceGateway(t, client)

	resp := postInfer(t, srv, InferenceRequest{Model: "m", Prompt: "p"})
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("status = 200 on client failure; must NOT fabricate a success")
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	raw := make([]byte, 4096)
	n, _ := resp.Body.Read(raw)
	bodyStr := string(raw[:n])
	if !strings.Contains(bodyStr, "inference failed") {
		t.Fatalf("error body = %q, want it to mention the typed inference error", bodyStr)
	}
	// And no completion field should carry an invented answer.
	if strings.Contains(bodyStr, `"completion"`) {
		t.Fatalf("error body leaked a completion field: %q", bodyStr)
	}
}

// TestInfer_DefaultClientRefuses proves the default (un-injected) client never
// fabricates an answer: it returns a typed error mapped to an error status.
func TestInfer_DefaultClientRefuses(t *testing.T) {
	g, err := NewGateway(WithInference(nil))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)

	resp := postInfer(t, srv, InferenceRequest{Model: "m", Prompt: "p"})
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("default client returned 200; must refuse, not fabricate")
	}
}

// TestInfer_BadRequests covers method + validation guards without touching the
// client.
func TestInfer_BadRequests(t *testing.T) {
	client := &fakeChutesClient{completion: "ok"}
	srv := newInferenceGateway(t, client)

	// Wrong method.
	getResp, err := http.Get(srv.URL + "/v1/ai/infer")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", getResp.StatusCode)
	}

	// Missing prompt.
	resp := postInfer(t, srv, InferenceRequest{Model: "m"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing-prompt status = %d, want 400", resp.StatusCode)
	}

	// Client must not have been called for any rejected request.
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.calls != 0 {
		t.Fatalf("client.calls = %d, want 0 (rejected requests must not reach client)", client.calls)
	}
}

// TestInfer_DoesNotBreakExistingRoutes proves wiring the inference route does
// not disturb existing gateway behavior (health + REST surface still work).
func TestInfer_DoesNotBreakExistingRoutes(t *testing.T) {
	g, err := NewGateway(WithInference(&fakeChutesClient{completion: "ok"}), WithREST())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)

	hResp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer hResp.Body.Close()
	if hResp.StatusCode != http.StatusOK {
		t.Fatalf("/health status = %d, want 200", hResp.StatusCode)
	}

	// The REST surface must still be reachable (unrelated route untouched).
	uResp, err := http.Get(srv.URL + "/v1/pool/utilization")
	if err != nil {
		t.Fatalf("GET /v1/pool/utilization: %v", err)
	}
	defer uResp.Body.Close()
	if uResp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/pool/utilization status = %d, want 200", uResp.StatusCode)
	}
}

// TestInferenceError_Unwrap proves the typed error wraps its cause so callers
// can use errors.Is/As.
func TestInferenceError_Unwrap(t *testing.T) {
	cause := errors.New("dial tcp: refused")
	e := &InferenceError{Reason: "network", Err: cause}
	if !errors.Is(e, cause) {
		t.Fatalf("errors.Is(e, cause) = false, want true")
	}
	if !strings.Contains(e.Error(), "network") {
		t.Fatalf("Error() = %q, want it to contain reason", e.Error())
	}
}
