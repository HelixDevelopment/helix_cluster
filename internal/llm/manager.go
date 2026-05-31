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

// RegisterModel registers a model.
func (m *Manager) RegisterModel(name, path, format string) error {
	if name == "" {
		return fmt.Errorf("model name is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.models[name] = &Model{Name: name, Path: path, Format: format}
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

// ListModels returns all registered models.
func (m *Manager) ListModels() []*Model {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Model, 0, len(m.models))
	for _, model := range m.models {
		list = append(list, model)
	}
	return list
}

// Inference runs inference on a model (stub).
func (m *Manager) Inference(name, prompt string) (string, error) {
	m.mu.RLock()
	_, ok := m.models[name]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("model not found: %s", name)
	}
	// Stub: return a mock response
	return fmt.Sprintf("[stub inference from %s] Response to: %s", name, prompt), nil
}
