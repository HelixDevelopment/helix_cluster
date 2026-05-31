// Package policy provides policy evaluation for Helix Cluster OS.
package policy

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Policy represents a loaded policy.
type Policy struct {
	Name  string
	Rego  string
	Rules map[string]interface{}

	// allow and deny hold the rules parsed from Rego. They are not part of the
	// public contract but drive Evaluate. deny takes precedence over allow.
	allow []rule
	deny  []rule
}

// rule is a single parsed condition of the form `<key> == <value>`.
type rule struct {
	key   string
	value string
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

// LoadPolicy loads a policy into the engine, replacing any policy with the same
// name. The Rego text is parsed into allow/deny rules; a malformed rule line is
// rejected so that a policy is never silently stored in a half-parsed state.
func (e *Engine) LoadPolicy(name, rego string) error {
	if name == "" {
		return fmt.Errorf("policy name is required")
	}
	allow, deny, err := parseRego(rego)
	if err != nil {
		return fmt.Errorf("policy %q: %w", name, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.policies[name] = &Policy{
		Name:  name,
		Rego:  rego,
		Rules: make(map[string]interface{}),
		allow: allow,
		deny:  deny,
	}
	return nil
}

// parseRego extracts allow/deny rules from policy text. Recognised lines have
// the form:
//
//	allow if <key> == <value>
//	deny if <key> == <value>
//
// All other non-empty lines (package declarations, comments, blank lines) are
// ignored so that legacy Rego stubs continue to load. A line that begins with
// allow/deny but does not match the expected shape is a hard error.
func parseRego(rego string) (allow, deny []rule, err error) {
	for i, raw := range strings.Split(rego, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var target *[]rule
		switch {
		case strings.HasPrefix(line, "allow if "):
			target = &allow
			line = strings.TrimPrefix(line, "allow if ")
		case strings.HasPrefix(line, "deny if "):
			target = &deny
			line = strings.TrimPrefix(line, "deny if ")
		default:
			// Not a rule line we evaluate (e.g. `package x`, `allow { true }`).
			continue
		}
		key, value, ok := strings.Cut(line, "==")
		if !ok {
			return nil, nil, fmt.Errorf("line %d: rule must contain '==': %q", i+1, raw)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			return nil, nil, fmt.Errorf("line %d: rule has empty key or value: %q", i+1, raw)
		}
		*target = append(*target, rule{key: key, value: unquote(value)})
	}
	return allow, deny, nil
}

// unquote strips one layer of matching double quotes from a value literal.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// Evaluate evaluates a policy against input data.
//
// Decision semantics, in order of precedence:
//  1. If any deny rule matches the input, the result is denied.
//  2. Otherwise if any allow rule matches, the result is allowed.
//  3. Otherwise, if the policy declared no allow/deny rules, fall back to the
//     legacy behaviour of honouring a boolean input["allowed"].
//  4. Otherwise (rules exist but none matched), the result is denied
//     (default-deny).
func (e *Engine) Evaluate(policyName string, input map[string]interface{}) (bool, map[string]interface{}, error) {
	e.mu.RLock()
	policy, ok := e.policies[policyName]
	e.mu.RUnlock()
	if !ok {
		return false, nil, fmt.Errorf("policy not found: %s", policyName)
	}

	matched := ""
	allowed := false
	switch {
	case matchAny(policy.deny, input):
		allowed = false
		matched = "deny"
	case len(policy.allow) > 0 || len(policy.deny) > 0:
		allowed = matchAny(policy.allow, input)
		if allowed {
			matched = "allow"
		} else {
			matched = "default-deny"
		}
	default:
		// Legacy fallback: no rules declared, honour input["allowed"].
		if v, ok := input["allowed"]; ok {
			if b, ok := v.(bool); ok {
				allowed = b
			}
		}
		matched = "legacy"
	}

	decisions := map[string]interface{}{
		"policy":    policy.Name,
		"evaluated": true,
		"allowed":   allowed,
		"matched":   matched,
	}
	return allowed, decisions, nil
}

// matchAny reports whether any rule in rules is satisfied by input. A rule is
// satisfied when input[key] stringifies to the rule's value.
func matchAny(rules []rule, input map[string]interface{}) bool {
	for _, r := range rules {
		v, ok := input[r.key]
		if !ok {
			continue
		}
		if stringify(v) == r.value {
			return true
		}
	}
	return false
}

// stringify renders a JSON-decoded input value to its canonical string form so
// that rule literals (always strings) can be compared against typed input.
func stringify(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		// json.Unmarshal decodes all numbers as float64.
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// GetPolicy returns a copy of the named policy's public fields. The boolean is
// false if no such policy is loaded. The returned Policy is a defensive copy:
// mutating it does not affect engine state.
func (e *Engine) GetPolicy(name string) (Policy, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p, ok := e.policies[name]
	if !ok {
		return Policy{}, false
	}
	rules := make(map[string]interface{}, len(p.Rules))
	for k, v := range p.Rules {
		rules[k] = v
	}
	return Policy{Name: p.Name, Rego: p.Rego, Rules: rules}, true
}

// RemovePolicy removes a policy by name. It returns an error if the policy was
// not loaded, so callers can distinguish a real deletion from a no-op.
func (e *Engine) RemovePolicy(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.policies[name]; !ok {
		return fmt.Errorf("policy not found: %s", name)
	}
	delete(e.policies, name)
	return nil
}

// PolicyCount returns the number of loaded policies.
func (e *Engine) PolicyCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.policies)
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
