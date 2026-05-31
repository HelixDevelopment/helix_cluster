package inference

import (
	"context"
	"fmt"
	"sync"
)

// Router is a capability-aware registry of backends. Given a Request it builds
// an ordered candidate list of backends whose Capabilities support the request,
// then tries them in registration order as a FALLBACK CHAIN: the first backend
// that is Available() AND returns no error wins. A backend that is unavailable
// or returns an error is skipped and the next candidate is tried.
//
// The fallback chain is the load-bearing behavior: a naive router that always
// uses the first registered backend (ignoring Available()/errors) would return
// the wrong backend's response and fail the fallback tests.
type Router struct {
	mu       sync.RWMutex
	backends []Backend
}

// NewRouter creates an empty Router.
func NewRouter() *Router {
	return &Router{}
}

// Register appends a backend to the routing order. Registration order defines
// fallback preference: earlier backends are preferred. A nil backend or one with
// an empty Name is rejected so the registry never holds an unusable entry.
func (r *Router) Register(b Backend) error {
	if b == nil {
		return fmt.Errorf("inference: cannot register nil backend")
	}
	if b.Name() == "" {
		return fmt.Errorf("inference: backend name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends = append(r.backends, b)
	return nil
}

// Backends returns the registered backends in preference order. The slice is a
// copy; mutating it does not affect the registry.
func (r *Router) Backends() []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Backend, len(r.backends))
	copy(out, r.backends)
	return out
}

// Candidates returns, in preference order, the backends whose Capabilities
// support the request. Availability is NOT considered here — this answers "who
// could serve this?" independent of current health.
func (r *Router) Candidates(req Request) []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Backend
	for _, b := range r.backends {
		if b.Capabilities().Supports(req) {
			out = append(out, b)
		}
	}
	return out
}

// Route executes the request against the fallback chain and returns the Response
// of the first capability-matching, Available backend that serves without error.
//
// Error semantics:
//   - empty prompt -> ErrEmptyPrompt (no backend is consulted).
//   - no capability match at all -> ErrUnsupported.
//   - capability matches exist but every one is unavailable or errored -> the
//     last underlying error, wrapped, so the caller sees a real cause.
func (r *Router) Route(ctx context.Context, req Request) (Response, error) {
	if req.Prompt == "" {
		return Response{}, ErrEmptyPrompt
	}
	candidates := r.Candidates(req)
	if len(candidates) == 0 {
		return Response{}, fmt.Errorf("%w: model=%q arch=%q maxTokens=%d",
			ErrUnsupported, req.Model, req.Architecture, req.MaxTokens)
	}
	var lastErr error
	for _, b := range candidates {
		if !b.Available() {
			lastErr = fmt.Errorf("%s: %w", b.Name(), ErrUnavailable)
			continue
		}
		resp, err := b.Infer(ctx, req)
		if err != nil {
			lastErr = err
			continue
		}
		return resp, nil
	}
	return Response{}, fmt.Errorf("inference: all %d candidate backend(s) failed: %w",
		len(candidates), lastErr)
}
