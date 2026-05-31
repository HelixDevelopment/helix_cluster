package inference

import (
	"context"
	"fmt"

	"github.com/HelixDevelopment/helix_cluster/internal/llm"
)

// LocalBackend adapts the existing internal/llm.Manager to the Backend
// interface. Its capabilities are derived from the manager's REAL registered
// model list, and a request is only served when the manager reports the model as
// currently loaded — i.e. it reflects genuine registry/load state, not a static
// allow-list.
//
// HONESTY: actual token generation is delegated to llm.Manager.Inference, which
// is itself a documented reference stub (no embedded GGUF/llama runtime). The
// adapter therefore claims a REFERENCE, not real LLM inference. What is real and
// end-user-load-bearing here is the gating: an unregistered or unloaded model is
// refused, exactly as a real serving backend would refuse it.
type LocalBackend struct {
	// BackendName is the Name() reported by this backend.
	BackendName string
	// Manager is the live internal/llm model registry this backend reflects.
	Manager *llm.Manager
	// MaxTokens is the per-request token cap advertised in Capabilities; 0 means
	// no explicit cap.
	MaxTokens int
}

// NewLocalBackend constructs a LocalBackend over the given manager.
func NewLocalBackend(name string, mgr *llm.Manager) *LocalBackend {
	return &LocalBackend{BackendName: name, Manager: mgr}
}

// Name implements Backend.
func (l *LocalBackend) Name() string { return l.BackendName }

// Available reports whether the backend can serve any traffic at all: it needs a
// manager and at least one currently-loaded model. A manager with only
// registered-but-unloaded models is NOT available, mirroring real serving.
func (l *LocalBackend) Available() bool {
	if l.Manager == nil {
		return false
	}
	for _, m := range l.Manager.ListModels() {
		if m.LoadedAt != nil {
			return true
		}
	}
	return false
}

// Capabilities derives the served model set from the manager's REAL registered
// models. The model list reflects the live registry at call time.
func (l *LocalBackend) Capabilities() Capabilities {
	caps := Capabilities{MaxTokens: l.MaxTokens}
	if l.Manager == nil {
		return caps
	}
	models := l.Manager.ListModels()
	caps.Models = make([]string, 0, len(models))
	for _, m := range models {
		caps.Models = append(caps.Models, m.Name)
	}
	return caps
}

// Infer serves a request via the underlying manager. It enforces the real
// registry contract: the model must be registered and loaded. A loaded model
// produces reference output; an unloaded/unknown one is refused with a typed
// error so the Router can fall back.
func (l *LocalBackend) Infer(_ context.Context, req Request) (Response, error) {
	if req.Prompt == "" {
		return Response{}, ErrEmptyPrompt
	}
	if l.Manager == nil {
		return Response{}, fmt.Errorf("%s: %w", l.BackendName, ErrUnavailable)
	}
	if _, ok := l.Manager.GetModel(req.Model); !ok {
		return Response{}, fmt.Errorf("%s: %w: %s", l.BackendName, ErrUnsupported, req.Model)
	}
	if !l.Manager.IsLoaded(req.Model) {
		return Response{}, fmt.Errorf("%s: %w: %s", l.BackendName, ErrModelNotReady, req.Model)
	}
	out, err := l.Manager.Inference(req.Model, req.Prompt)
	if err != nil {
		return Response{}, fmt.Errorf("%s: inference failed: %w", l.BackendName, err)
	}
	budget := req.MaxTokens
	if budget <= 0 {
		budget = 16
	}
	return Response{
		Backend: l.BackendName,
		Model:   req.Model,
		Text:    out,
		Tokens:  budget,
	}, nil
}

// compile-time assertion.
var _ Backend = (*LocalBackend)(nil)
