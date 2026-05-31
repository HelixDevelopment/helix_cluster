// Package policy provides policy evaluation for Helix Cluster OS.
package policy

import (
	"fmt"
	"sync"
)

// Policy represents a loaded policy.
type Policy struct {
	Name   string
	Rego   string
	Rules  map[string]interface{}
}

// Engine evaluates policies against inputs.
type Engine struct {
	mu       sync.RWMutex
	policies map[string]*Policy
}

// NewEngine creates a new policy engine.
func NewEngine() *Engine {
	return &Engine{policies: make(map[string]*Policy)}
}

// LoadPolicy loads a policy into the engine.
func (e *Engine) LoadPolicy(name, rego string) error {
	if name == "" {
		return fmt.Errorf("policy name is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.policies[name] = &Policy{Name: name, Rego: rego, Rules: make(map[string]interface{})}
	return nil
}

// Evaluate evaluates a policy against input data.
func (e *Engine) Evaluate(policyName string, input map[string]interface{}) (bool, map[string]interface{}, error) {
	e.mu.RLock()
	policy, ok := e.policies[policyName]
	e.mu.RUnlock()
	if !ok {
		return false, nil, fmt.Errorf("policy not found: %s", policyName)
	}

	// Stub evaluation: if input has "allowed" = true, allow
	allowed := false
	if v, ok := input["allowed"]; ok {
		if b, ok := v.(bool); ok {
			allowed = b
		}
	}

	decisions := map[string]interface{}{
		"policy":  policy.Name,
		"evaluated": true,
	}
	return allowed, decisions, nil
}

// ListPolicies returns all loaded policy names.
func (e *Engine) ListPolicies() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	names := make([]string, 0, len(e.policies))
	for name := range e.policies {
		names = append(names, name)
	}
	return names
}
