package inference

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/internal/llm"
)

// TestLocalBackendCapabilitiesReflectRegistry asserts the backend's served-model
// set is derived from the REAL internal/llm registry, including a model added
// after construction.
//
// MUTATION: in Capabilities(), return a hard-coded `Capabilities{}` (ignore the
// manager). The "phi-2" model would be absent and the membership check fails.
func TestLocalBackendCapabilitiesReflectRegistry(t *testing.T) {
	t.Parallel()
	mgr := llm.NewManager()
	must(t, mgr.RegisterModel("phi-2", "/models/phi-2", "gguf"))

	lb := NewLocalBackend("local", mgr)
	caps := lb.Capabilities()
	if !caps.supportsModel("phi-2") {
		t.Fatalf("capabilities = %+v, want phi-2 reflected from registry", caps)
	}

	// Register a second model AFTER the backend exists; capabilities are live.
	must(t, mgr.RegisterModel("mistral-7b", "/models/mistral", "gguf"))
	if !lb.Capabilities().supportsModel("mistral-7b") {
		t.Fatal("capabilities did not reflect newly registered mistral-7b")
	}
}

// TestLocalBackendAvailableTracksLoadedState asserts Available() reflects REAL
// load state: registered-only is NOT available; loading flips it to available;
// unloading flips it back.
//
// MUTATION: make Available() return `l.Manager != nil`. The registered-but-not-
// loaded assertion (want false) fails.
func TestLocalBackendAvailableTracksLoadedState(t *testing.T) {
	t.Parallel()
	mgr := llm.NewManager()
	must(t, mgr.RegisterModel("phi-2", "/models/phi-2", "gguf"))
	lb := NewLocalBackend("local", mgr)

	if lb.Available() {
		t.Fatal("backend Available() == true with no loaded model, want false")
	}
	must(t, mgr.LoadModel("phi-2"))
	if !lb.Available() {
		t.Fatal("backend Available() == false after LoadModel, want true")
	}
	must(t, mgr.UnloadModel("phi-2"))
	if lb.Available() {
		t.Fatal("backend Available() == true after UnloadModel, want false")
	}
}

// TestLocalBackendInferRequiresLoaded asserts Infer refuses a registered-but-
// unloaded model with ErrModelNotReady, then serves once loaded.
//
// MUTATION: delete the `if !l.Manager.IsLoaded(req.Model) { return ErrModelNotReady }`
// guard. The first Infer would reach manager.Inference which itself errors
// "model not loaded" — but the typed errors.Is(err, ErrModelNotReady) check
// fails because the sentinel is gone.
func TestLocalBackendInferRequiresLoaded(t *testing.T) {
	t.Parallel()
	mgr := llm.NewManager()
	must(t, mgr.RegisterModel("phi-2", "/models/phi-2", "gguf"))
	lb := NewLocalBackend("local", mgr)

	_, err := lb.Infer(context.Background(), Request{Model: "phi-2", Prompt: "hi"})
	if !errors.Is(err, ErrModelNotReady) {
		t.Fatalf("err = %v, want ErrModelNotReady for unloaded model", err)
	}

	must(t, mgr.LoadModel("phi-2"))
	resp, err := lb.Infer(context.Background(), Request{Model: "phi-2", Prompt: "ping"})
	if err != nil {
		t.Fatalf("Infer after load: %v", err)
	}
	if resp.Backend != "local" || resp.Model != "phi-2" {
		t.Fatalf("resp = %+v, want backend=local model=phi-2", resp)
	}
	// The served text is the manager's real (reference) inference output.
	if !strings.Contains(resp.Text, "ping") {
		t.Fatalf("resp.Text = %q, want it to carry the prompt 'ping'", resp.Text)
	}
}

// TestLocalBackendInferUnknownModel asserts an unregistered model yields
// ErrUnsupported.
//
// MUTATION: delete the GetModel existence check. The call would fall to
// IsLoaded (false) and return ErrModelNotReady, so errors.Is(err, ErrUnsupported)
// fails.
func TestLocalBackendInferUnknownModel(t *testing.T) {
	t.Parallel()
	mgr := llm.NewManager()
	lb := NewLocalBackend("local", mgr)

	_, err := lb.Infer(context.Background(), Request{Model: "ghost", Prompt: "hi"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported for unknown model", err)
	}
}

// TestRouterFallsBackFromLocalToReference is the integration scenario: a
// LocalBackend whose model is registered but NOT loaded is preferred, and the
// router falls back to a ReferenceBackend that serves the same model.
//
// MUTATION: in Route, remove the error-skip (`continue`) so the first candidate's
// error returns immediately. The LocalBackend's ErrModelNotReady would surface
// instead of the reference response; resp.Backend=="ref" fails.
func TestRouterFallsBackFromLocalToReference(t *testing.T) {
	t.Parallel()
	mgr := llm.NewManager()
	must(t, mgr.RegisterModel("shared", "/models/shared", "gguf")) // registered, NOT loaded
	local := NewLocalBackend("local", mgr)

	ref := &ReferenceBackend{
		BackendName: "ref", Up: true,
		Caps: Capabilities{Models: []string{"shared"}},
	}

	r := NewRouter()
	must(t, r.Register(local)) // preferred but model not loaded
	must(t, r.Register(ref))   // fallback

	// LocalBackend is a capability candidate (model registered) but its Infer
	// fails because the model is not loaded -> router falls back to ref.
	cands := r.Candidates(Request{Model: "shared", Prompt: "hi"})
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2 (local registered + ref)", len(cands))
	}

	resp, err := r.Route(context.Background(), Request{Model: "shared", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Route error: %v", err)
	}
	if resp.Backend != "ref" {
		t.Fatalf("served backend = %q, want ref (fallback from unloaded local)", resp.Backend)
	}

	// Now load the model on the local backend: the preferred local backend wins.
	must(t, mgr.LoadModel("shared"))
	resp, err = r.Route(context.Background(), Request{Model: "shared", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Route error after load: %v", err)
	}
	if resp.Backend != "local" {
		t.Fatalf("served backend = %q, want local (preferred once loaded)", resp.Backend)
	}
}
