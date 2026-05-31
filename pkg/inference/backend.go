// Package inference provides a pluggable inference Backend abstraction and a
// capability-aware Router with a fallback chain for Helix Cluster OS.
//
// SCOPE / HONESTY: real high-throughput serving engines (vLLM, SGLang, a GGUF
// runtime) are OUT OF SCOPE for this pure-Go package — wiring them requires
// CGO/system/GPU dependencies. This package therefore ships a deterministic
// ReferenceBackend and a LocalBackend that reflects the REAL internal/llm model
// registry (registered + loaded state). The token generation produced here is a
// labeled reference string, NOT real LLM inference; the contract that surrounds
// it (capability matching, availability gating, fallback, error reporting) is
// real and is what end users depend on. Any future real engine plugs in behind
// the Backend interface without changing callers.
package inference

import (
	"context"
	"errors"
	"fmt"
)

// Sentinel errors so callers (and tests) can branch on the failure class with
// errors.Is rather than string matching.
var (
	// ErrUnsupported is returned when no registered backend can serve a request.
	ErrUnsupported = errors.New("inference: no backend supports request")
	// ErrUnavailable is returned by a backend that is currently not serving.
	ErrUnavailable = errors.New("inference: backend unavailable")
	// ErrEmptyPrompt is returned for a request with no prompt.
	ErrEmptyPrompt = errors.New("inference: prompt is required")
	// ErrModelNotReady is returned when the requested model is not loaded/ready.
	ErrModelNotReady = errors.New("inference: model not ready")
)

// Capabilities describes what a Backend can serve. Matching is deterministic and
// takes these as INPUT — no hardware is probed by this package.
type Capabilities struct {
	// Models is the set of model names this backend can serve.
	Models []string
	// Architectures is the set of model architectures (e.g. "llama", "qwen").
	// An empty slice means the backend does not constrain on architecture.
	Architectures []string
	// MaxTokens is the largest MaxTokens value this backend will accept. A value
	// <= 0 means the backend imposes no explicit cap.
	MaxTokens int
}

// supportsModel reports whether the named model is in the capability set.
func (c Capabilities) supportsModel(name string) bool {
	for _, m := range c.Models {
		if m == name {
			return true
		}
	}
	return false
}

// supportsArch reports whether the architecture is allowed. An empty constraint
// list accepts any architecture (including an empty request architecture).
func (c Capabilities) supportsArch(arch string) bool {
	if len(c.Architectures) == 0 {
		return true
	}
	for _, a := range c.Architectures {
		if a == arch {
			return true
		}
	}
	return false
}

// Supports reports whether this capability set can serve the request. It checks
// model membership, architecture constraint, and the MaxTokens cap.
func (c Capabilities) Supports(req Request) bool {
	if !c.supportsModel(req.Model) {
		return false
	}
	if !c.supportsArch(req.Architecture) {
		return false
	}
	if c.MaxTokens > 0 && req.MaxTokens > c.MaxTokens {
		return false
	}
	return true
}

// Request is a single inference request.
type Request struct {
	// Model is the requested model name (required).
	Model string
	// Architecture optionally constrains which backend may serve (e.g. "llama").
	Architecture string
	// Prompt is the input text (required).
	Prompt string
	// MaxTokens is the requested generation budget; 0 means backend default.
	MaxTokens int
}

// Response is the result of an inference request.
type Response struct {
	// Backend is the Name() of the backend that actually served the request.
	// This is the load-bearing field for fallback assertions.
	Backend string
	// Model echoes the served model name.
	Model string
	// Text is the generated output (reference text for ReferenceBackend).
	Text string
	// Tokens is the number of reference tokens emitted.
	Tokens int
}

// Backend is a pluggable inference provider. Implementations are responsible for
// reporting their own availability and capabilities; the Router never assumes a
// backend is healthy.
type Backend interface {
	// Name returns a stable identifier for this backend.
	Name() string
	// Available reports whether the backend can currently serve traffic.
	Available() bool
	// Capabilities returns the static capability descriptor used for matching.
	Capabilities() Capabilities
	// Infer runs a single request. It MUST return a non-nil error when it cannot
	// serve, so the Router can fall back to the next backend.
	Infer(ctx context.Context, req Request) (Response, error)
}

// referenceGenerate produces deterministic reference output for a request. It is
// labeled as a reference so no caller can mistake it for real model output.
func referenceGenerate(backend string, req Request) Response {
	budget := req.MaxTokens
	if budget <= 0 {
		budget = 16
	}
	text := fmt.Sprintf("[reference:%s model=%s] echo: %s", backend, req.Model, req.Prompt)
	return Response{
		Backend: backend,
		Model:   req.Model,
		Text:    text,
		Tokens:  budget,
	}
}

// ReferenceBackend is a deterministic, fully in-process Backend. It serves any
// model declared in its Caps and is gated via the Up flag so tests can model an
// unavailable or failing provider without real infrastructure.
type ReferenceBackend struct {
	// BackendName is the Name() reported by this backend.
	BackendName string
	// Caps is the capability descriptor advertised for matching.
	Caps Capabilities
	// Up controls Available(); set false to simulate an unavailable backend.
	Up bool
	// FailWith, when non-nil, is returned by Infer to simulate a serving error
	// even though the backend reports Available()==true.
	FailWith error
}

// Name implements Backend.
func (b *ReferenceBackend) Name() string { return b.BackendName }

// Available implements Backend.
func (b *ReferenceBackend) Available() bool { return b.Up }

// Capabilities implements Backend.
func (b *ReferenceBackend) Capabilities() Capabilities { return b.Caps }

// Infer implements Backend. It validates the request, honors the injected
// FailWith error, and otherwise returns deterministic reference output.
func (b *ReferenceBackend) Infer(_ context.Context, req Request) (Response, error) {
	if req.Prompt == "" {
		return Response{}, ErrEmptyPrompt
	}
	if !b.Up {
		return Response{}, fmt.Errorf("%s: %w", b.BackendName, ErrUnavailable)
	}
	if b.FailWith != nil {
		return Response{}, fmt.Errorf("%s: %w", b.BackendName, b.FailWith)
	}
	if !b.Caps.Supports(req) {
		return Response{}, fmt.Errorf("%s: %w", b.BackendName, ErrUnsupported)
	}
	return referenceGenerate(b.BackendName, req), nil
}

// compile-time assertion.
var _ Backend = (*ReferenceBackend)(nil)
