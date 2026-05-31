// Package llm provides LLM model management for Helix Cluster OS.
package llm

import (
	"fmt"
	"sync"
	"time"
)

// Model represents a registered LLM model.
type Model struct {
	Name      string
	Path      string
	Format    string
	LoadedAt  *time.Time
	LoadCount int
}

// Manager manages LLM models.
type Manager struct {
	mu     sync.RWMutex
	models map[string]*Model
}

// NewManager creates a new LLM manager.
func NewManager() *Manager {
	return &Manager{models: make(map[string]*Model)}
}

// RegisterModel registers a model. Re-registering an existing name updates its
// path and format but preserves runtime load state (LoadedAt/LoadCount): a model
// that is currently loaded must not be silently torn down by a metadata update.
func (m *Manager) RegisterModel(name, path, format string) error {
	if name == "" {
		return fmt.Errorf("model name is required")
	}
	if path == "" {
		return fmt.Errorf("model path is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.models[name]; ok {
		existing.Path = path
		existing.Format = format
		return nil
	}
	m.models[name] = &Model{Name: name, Path: path, Format: format}
	return nil
}

// UnregisterModel removes a model from the registry. It reports an error if the
// model is unknown so callers can distinguish a no-op from a real removal.
func (m *Manager) UnregisterModel(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[name]; !ok {
		return fmt.Errorf("model not found: %s", name)
	}
	delete(m.models, name)
	return nil
}

// LoadModel marks a model as loaded.
func (m *Manager) LoadModel(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	model, ok := m.models[name]
	if !ok {
		return fmt.Errorf("model not found: %s", name)
	}
	now := time.Now()
	model.LoadedAt = &now
	model.LoadCount++
	return nil
}

// UnloadModel marks a model as unloaded.
func (m *Manager) UnloadModel(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	model, ok := m.models[name]
	if !ok {
		return fmt.Errorf("model not found: %s", name)
	}
	model.LoadedAt = nil
	return nil
}

// ListModels returns a snapshot of all registered models. Each returned *Model
// is a copy: callers cannot mutate the manager's internal state through the
// returned pointers (e.g. forging a LoadedAt timestamp).
func (m *Manager) ListModels() []*Model {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Model, 0, len(m.models))
	for _, model := range m.models {
		list = append(list, model.clone())
	}
	return list
}

// GetModel returns a snapshot copy of a single registered model. The bool is
// false when the model is unknown.
func (m *Manager) GetModel(name string) (*Model, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	model, ok := m.models[name]
	if !ok {
		return nil, false
	}
	return model.clone(), true
}

// IsLoaded reports whether the named model is currently loaded.
func (m *Manager) IsLoaded(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	model, ok := m.models[name]
	return ok && model.LoadedAt != nil
}

// clone returns a deep copy of the model so internal pointers (LoadedAt) are
// never shared with callers.
func (model *Model) clone() *Model {
	cp := *model
	if model.LoadedAt != nil {
		t := *model.LoadedAt
		cp.LoadedAt = &t
	}
	return &cp
}

// Inference runs inference on a model.
//
// HONEST STUB: this package does not embed a real inference engine — wiring an
// actual GGUF/llama runtime requires a CGO/system dependency that is out of
// scope for this pure-Go package. The response body below is therefore
// synthetic. The surrounding contract, however, is real and enforced: inference
// is rejected unless the model is registered AND currently loaded, and an empty
// prompt is rejected. These guards are what an end user actually depends on; a
// "response" produced for an unloaded model would be a usability lie.
func (m *Manager) Inference(name, prompt string) (string, error) {
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	m.mu.RLock()
	model, ok := m.models[name]
	loaded := ok && model.LoadedAt != nil
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("model not found: %s", name)
	}
	if !loaded {
		return "", fmt.Errorf("model not loaded: %s", name)
	}
	// Stub: return a mock response. See HONEST STUB note above.
	return fmt.Sprintf("[stub inference from %s] Response to: %s", name, prompt), nil
}
