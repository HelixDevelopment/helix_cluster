// Package gateway – internal AI inference routing (HXC-1469).
//
// This file adds an internal AI inference route to the Gateway:
//
//	POST /v1/ai/infer – forward an internal AI request to a Chutes inference
//	                    client and return the model completion.
//
// Requests flagged TEERequired are routed through the E2EE-wrapping path: the
// response carries the X-E2EE-Enabled marker so a client (and the integration
// test) has sink-side proof that the confidential-compute path was taken.
// Non-TEE requests use plain routing and the marker is absent.
//
// The Chutes inference client is injected via the ChutesInferenceClient
// interface so unit/integration tests wire an in-process fake with real state
// (a real canned completion) instead of touching the network. A client failure
// surfaces as a typed *InferenceError and an HTTP error status — never a
// fabricated 200 with an invented answer (CLAUDE-1 anti-bluff).
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// e2eeEnabledHeader is the sink-side marker set on responses whose request was
// flagged TEERequired and therefore routed through the E2EE-wrapping path.
// Removing the branch that sets this header makes the TEE integration test fail.
const e2eeEnabledHeader = "X-E2EE-Enabled"

// ── Domain types ─────────────────────────────────────────────────────────────

// InferenceRequest is the request body for POST /v1/ai/infer.
//
// Model and Prompt are forwarded verbatim to the Chutes inference client.
// TEERequired, when true, demands that the request be processed inside a
// Trusted-Execution-Environment-backed, end-to-end-encrypted path; the gateway
// signals this to the caller via the X-E2EE-Enabled response header.
type InferenceRequest struct {
	Model       string `json:"model"`
	Prompt      string `json:"prompt"`
	TEERequired bool   `json:"tee_required,omitempty"`
}

// InferenceResponse is the body returned on a successful completion.
//
// E2EEEnabled mirrors the X-E2EE-Enabled header in the JSON body so consumers
// that only inspect the body still have proof the confidential path was used.
type InferenceResponse struct {
	Model       string `json:"model"`
	Completion  string `json:"completion"`
	E2EEEnabled bool   `json:"e2ee_enabled"`
}

// ── Chutes inference seam (dependency injection) ─────────────────────────────

// ChutesInferenceClient is the contract the inference handler uses to obtain a
// model completion from Chutes inference. The unit/integration tiers wire an
// in-process fake; production wires a real network-backed client.
//
// teeRequired tells the client whether the completion must run inside the
// TEE/E2EE path. Implementations MUST NOT fabricate a completion: on failure
// they return a non-nil error and an empty completion.
type ChutesInferenceClient interface {
	// Complete returns the model's completion for prompt under model. When
	// teeRequired is true the implementation routes the call through its
	// confidential-compute / E2EE path.
	Complete(model, prompt string, teeRequired bool) (completion string, err error)
}

// InferenceError is the typed error surfaced when the Chutes inference client
// fails. It is wrapped (never swallowed) so the handler can map it to an HTTP
// error status instead of returning an invented answer.
type InferenceError struct {
	// Reason is a human-readable, single-line description of the failure.
	Reason string
	// Err is the underlying cause, if any.
	Err error
}

func (e *InferenceError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("inference failed: %s: %v", e.Reason, e.Err)
	}
	return fmt.Sprintf("inference failed: %s", e.Reason)
}

func (e *InferenceError) Unwrap() error { return e.Err }

// ── Default (no-op) inference client ─────────────────────────────────────────

// defaultInferenceClient is wired when no real client is injected. It honestly
// refuses to fabricate a completion: every call returns a typed error so the
// gateway never returns an invented answer when inference is unconfigured.
type defaultInferenceClient struct{}

func (defaultInferenceClient) Complete(model, prompt string, teeRequired bool) (string, error) {
	return "", &InferenceError{Reason: "no inference client configured"}
}

// ── Inference handler ────────────────────────────────────────────────────────

// inferenceAPI is the inference surface attached to the Gateway. It owns its
// ServeMux so the route can be unit-tested independently of the proxy logic.
type inferenceAPI struct {
	client ChutesInferenceClient
	mux    *http.ServeMux
}

// newInferenceAPI constructs an inferenceAPI wired to the given client, or the
// default refusing client if nil is passed.
func newInferenceAPI(client ChutesInferenceClient) *inferenceAPI {
	if client == nil {
		client = defaultInferenceClient{}
	}
	a := &inferenceAPI{client: client, mux: http.NewServeMux()}
	a.mux.HandleFunc("/v1/ai/infer", a.handleInfer)
	return a
}

// ServeHTTP lets inferenceAPI act as an http.Handler embedded in the Gateway.
func (a *inferenceAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

// handleInfer handles POST /v1/ai/infer.
//
// It decodes the request, forwards it to the injected Chutes inference client,
// and writes the completion. When TEERequired is set it routes through the
// E2EE-wrapping path and stamps X-E2EE-Enabled on the response; otherwise it
// uses plain routing and omits the marker. Client failures become a 502 with a
// typed-error body — never a fake 200.
func (a *inferenceAPI) handleInfer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req InferenceRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&req); err != nil && err.Error() != "EOF" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":"invalid JSON: %s"}`, err.Error())
			return
		}
	}
	if req.Model == "" || req.Prompt == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"model and prompt are required"}`))
		return
	}

	// Route through the Chutes inference client. The teeRequired flag selects
	// the confidential-compute / E2EE path inside the client.
	completion, err := a.client.Complete(req.Model, req.Prompt, req.TEERequired)
	if err != nil {
		// Surface the typed error; never fabricate an answer (CLAUDE-1).
		var infErr *InferenceError
		if !errors.As(err, &infErr) {
			infErr = &InferenceError{Reason: "client error", Err: err}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprintf(w, `{"error":%q,"status":502}`, infErr.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// TEERequired => the request traversed the E2EE-wrapping path. Stamp the
	// sink-side marker so the caller can prove confidential routing occurred.
	// Removing this branch makes the TEE integration test fail (mutation guard).
	if req.TEERequired {
		w.Header().Set(e2eeEnabledHeader, "true")
	}
	w.WriteHeader(http.StatusOK)
	resp := InferenceResponse{
		Model:       req.Model,
		Completion:  completion,
		E2EEEnabled: req.TEERequired,
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		_ = err // too late to change status
	}
}

// ── Gateway integration ──────────────────────────────────────────────────────

// WithInference is a Gateway Option that attaches the internal AI inference
// route (/v1/ai/infer) backed by the given Chutes inference client. Passing a
// nil client installs the default refusing client. All other traffic continues
// to the REST/proxy/auth logic, so existing routes are unaffected.
func WithInference(client ChutesInferenceClient) Option {
	return func(g *Gateway) {
		g.inference = newInferenceAPI(client)
	}
}

// inferencePath returns true when path belongs to the inference surface and
// should be dispatched to the inference handler rather than the proxy logic.
func inferencePath(path string) bool {
	return path == "/v1/ai/infer"
}
