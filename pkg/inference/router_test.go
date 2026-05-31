package inference

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// newRef is a small helper to build a ReferenceBackend.
func newRef(name string, up bool, models ...string) *ReferenceBackend {
	return &ReferenceBackend{
		BackendName: name,
		Up:          up,
		Caps:        Capabilities{Models: models},
	}
}

// TestRouterPicksCapabilityMatch asserts the router selects the backend whose
// Capabilities support the request, not merely the first registered one.
//
// MUTATION: in Route, replace `r.Candidates(req)` with `r.Backends()` (drop the
// Supports() filter). The first backend "alpha" (which does NOT serve "qwen")
// would then be tried first; its Infer returns ErrUnsupported, and since alpha
// is the only one consulted incorrectly the served Backend would not be "beta".
// The resp.Backend=="beta" assertion fails.
func TestRouterPicksCapabilityMatch(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	must(t, r.Register(newRef("alpha", true, "llama-7b")))
	must(t, r.Register(newRef("beta", true, "qwen-14b")))

	resp, err := r.Route(context.Background(), Request{Model: "qwen-14b", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Route error: %v", err)
	}
	if resp.Backend != "beta" {
		t.Fatalf("served backend = %q, want beta", resp.Backend)
	}
	if resp.Model != "qwen-14b" {
		t.Fatalf("served model = %q, want qwen-14b", resp.Model)
	}
}

// TestRouterFallbackOnUnavailable asserts that when the preferred backend is
// Unavailable the router falls back to the next capable backend and returns ITS
// response.
//
// MUTATION: delete the `if !b.Available() { ...; continue }` guard in Route.
// The router would then call primary.Infer despite Up==false; ReferenceBackend
// returns ErrUnavailable, but a "first wins" router that ignored Available would
// surface that error instead of secondary's response — resp.Backend=="secondary"
// fails.
func TestRouterFallbackOnUnavailable(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	must(t, r.Register(newRef("primary", false, "shared-model")))  // down
	must(t, r.Register(newRef("secondary", true, "shared-model"))) // up

	resp, err := r.Route(context.Background(), Request{Model: "shared-model", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Route error: %v", err)
	}
	if resp.Backend != "secondary" {
		t.Fatalf("served backend = %q, want secondary (fallback)", resp.Backend)
	}
}

// TestRouterFallbackOnError asserts fallback when the preferred backend is
// Available() but Infer returns an error.
//
// MUTATION: in Route, change the `if err != nil { lastErr = err; continue }`
// branch to `return resp, err`. The router would return primary's injected
// failure instead of falling back; resp.Backend=="secondary" and err==nil fail.
func TestRouterFallbackOnError(t *testing.T) {
	t.Parallel()
	primary := newRef("primary", true, "shared-model")
	primary.FailWith = errors.New("gpu oom")
	secondary := newRef("secondary", true, "shared-model")

	r := NewRouter()
	must(t, r.Register(primary))
	must(t, r.Register(secondary))

	resp, err := r.Route(context.Background(), Request{Model: "shared-model", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Route error: %v", err)
	}
	if resp.Backend != "secondary" {
		t.Fatalf("served backend = %q, want secondary (fallback after primary error)", resp.Backend)
	}
}

// TestRouterPreferredWinsWhenHealthy asserts the FIRST capable+available backend
// is used (preference order honored) — the fallback must not run unnecessarily.
//
// MUTATION: reverse iteration order in Route (iterate candidates back-to-front).
// resp.Backend would become "second" instead of "first".
func TestRouterPreferredWinsWhenHealthy(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	must(t, r.Register(newRef("first", true, "shared-model")))
	must(t, r.Register(newRef("second", true, "shared-model")))

	resp, err := r.Route(context.Background(), Request{Model: "shared-model", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Route error: %v", err)
	}
	if resp.Backend != "first" {
		t.Fatalf("served backend = %q, want first (preference order)", resp.Backend)
	}
}

// TestRouterUnsupportedErrors asserts a request no backend can serve fails with
// ErrUnsupported and consults no backend.
//
// MUTATION: change the `if len(candidates) == 0` branch to fall through to the
// loop (or return nil error). With no candidates the loop body never runs and
// lastErr is nil, so errors.Is(err, ErrUnsupported) fails.
func TestRouterUnsupportedErrors(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	must(t, r.Register(newRef("alpha", true, "llama-7b")))

	_, err := r.Route(context.Background(), Request{Model: "does-not-exist", Prompt: "hi"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// TestRouterEmptyPromptErrors asserts an empty prompt is rejected before any
// backend is consulted.
//
// MUTATION: delete the `if req.Prompt == "" { return ..., ErrEmptyPrompt }`
// guard in Route. The request would route to alpha and succeed; errors.Is(err,
// ErrEmptyPrompt) fails.
func TestRouterEmptyPromptErrors(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	must(t, r.Register(newRef("alpha", true, "llama-7b")))

	_, err := r.Route(context.Background(), Request{Model: "llama-7b", Prompt: ""})
	if !errors.Is(err, ErrEmptyPrompt) {
		t.Fatalf("err = %v, want ErrEmptyPrompt", err)
	}
}

// TestRouterAllCandidatesFail asserts that when capable backends exist but all
// fail/are down, the error wraps the underlying cause (not ErrUnsupported, since
// a match DID exist).
//
// MUTATION: in the final return, replace `lastErr` with `nil`. The wrapped cause
// "gpu down" would vanish and the strings.Contains check fails.
func TestRouterAllCandidatesFail(t *testing.T) {
	t.Parallel()
	b := newRef("only", true, "shared-model")
	b.FailWith = errors.New("gpu down")
	r := NewRouter()
	must(t, r.Register(b))

	_, err := r.Route(context.Background(), Request{Model: "shared-model", Prompt: "hi"})
	if err == nil {
		t.Fatal("expected error when all candidates fail")
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, should NOT be ErrUnsupported (a match existed)", err)
	}
	if !strings.Contains(err.Error(), "gpu down") {
		t.Fatalf("err = %v, want underlying cause 'gpu down'", err)
	}
}

// TestRouterArchConstraintFallback asserts architecture constraints participate
// in matching: a backend that only serves arch "llama" is skipped for a "qwen"
// request and the qwen-capable backend serves.
//
// MUTATION: make Capabilities.supportsArch always return true. Then "llama-only"
// would match the qwen request and, being first, serve it — resp.Backend would
// be "llama-only" not "qwen-backend".
func TestRouterArchConstraintFallback(t *testing.T) {
	t.Parallel()
	llamaOnly := &ReferenceBackend{
		BackendName: "llama-only", Up: true,
		Caps: Capabilities{Models: []string{"multi"}, Architectures: []string{"llama"}},
	}
	qwenBackend := &ReferenceBackend{
		BackendName: "qwen-backend", Up: true,
		Caps: Capabilities{Models: []string{"multi"}, Architectures: []string{"qwen"}},
	}
	r := NewRouter()
	must(t, r.Register(llamaOnly))
	must(t, r.Register(qwenBackend))

	resp, err := r.Route(context.Background(),
		Request{Model: "multi", Architecture: "qwen", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Route error: %v", err)
	}
	if resp.Backend != "qwen-backend" {
		t.Fatalf("served backend = %q, want qwen-backend", resp.Backend)
	}
}

// TestRouterMaxTokensCap asserts the MaxTokens cap excludes a backend whose cap
// is below the request budget, forcing fallback to a higher-cap backend.
//
// MUTATION: in Capabilities.Supports, delete the MaxTokens cap check. The small
// backend (cap 100) would match a 500-token request and, being first, serve it —
// resp.Backend would be "small" not "large".
func TestRouterMaxTokensCap(t *testing.T) {
	t.Parallel()
	small := &ReferenceBackend{
		BackendName: "small", Up: true,
		Caps: Capabilities{Models: []string{"m"}, MaxTokens: 100},
	}
	large := &ReferenceBackend{
		BackendName: "large", Up: true,
		Caps: Capabilities{Models: []string{"m"}, MaxTokens: 1000},
	}
	r := NewRouter()
	must(t, r.Register(small))
	must(t, r.Register(large))

	resp, err := r.Route(context.Background(),
		Request{Model: "m", Prompt: "hi", MaxTokens: 500})
	if err != nil {
		t.Fatalf("Route error: %v", err)
	}
	if resp.Backend != "large" {
		t.Fatalf("served backend = %q, want large (small's cap too low)", resp.Backend)
	}
}

// TestRegisterRejectsNilAndEmpty guards the registry invariant.
//
// MUTATION: drop the nil / empty-name checks in Register; both calls would
// return nil and these assertions fail.
func TestRegisterRejectsNilAndEmpty(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	if err := r.Register(nil); err == nil {
		t.Error("expected error registering nil backend")
	}
	if err := r.Register(newRef("", true, "m")); err == nil {
		t.Error("expected error registering empty-name backend")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
