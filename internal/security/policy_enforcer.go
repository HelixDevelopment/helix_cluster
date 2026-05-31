package security

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Permission represents a single allowed action on a resource pattern.
type Permission struct {
	Action   string
	Resource string // supports "*" wildcard
}

// Role is a named collection of permissions.
type Role struct {
	Name        string
	Permissions []Permission
}

// Policy binds a subject (user or service) to a set of roles.
type Policy struct {
	Name    string
	Subject string
	Roles   []string
}

// PolicyEnforcer provides simple RBAC enforcement.
type PolicyEnforcer struct {
	mu      sync.RWMutex
	roles   map[string]*Role
	policies map[string]*Policy
}

// NewPolicyEnforcer creates a new PolicyEnforcer.
func NewPolicyEnforcer() *PolicyEnforcer {
	return &PolicyEnforcer{
		roles:    make(map[string]*Role),
		policies: make(map[string]*Policy),
	}
}

// EnforcePolicy checks whether the action on resource is allowed for the
// subject described by the given policy. It returns nil if allowed or an
// error describing the denial reason.
func (p *PolicyEnforcer) EnforcePolicy(_ context.Context, policy *Policy, resource string, action string) error {
	if policy == nil {
		return fmt.Errorf("denied: no policy provided")
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, roleName := range policy.Roles {
		role, ok := p.roles[roleName]
		if !ok {
			continue
		}
		for _, perm := range role.Permissions {
			if matchResource(perm.Resource, resource) && matchAction(perm.Action, action) {
				return nil
			}
		}
	}
	return fmt.Errorf("denied: action %q on resource %q not allowed", action, resource)
}

// LoadPolicy loads a security policy into the enforcer.
func (p *PolicyEnforcer) LoadPolicy(_ context.Context, policy *Policy) error {
	if policy == nil {
		return fmt.Errorf("policy is nil")
	}
	if policy.Name == "" {
		return fmt.Errorf("policy name is required")
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.policies[policy.Name] = policy
	return nil
}

// ListPolicies returns a snapshot of active policies.
func (p *PolicyEnforcer) ListPolicies() []*Policy {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]*Policy, 0, len(p.policies))
	for _, pol := range p.policies {
		out = append(out, pol)
	}
	return out
}

// LoadRole loads a role into the enforcer.
func (p *PolicyEnforcer) LoadRole(_ context.Context, role *Role) error {
	if role == nil {
		return fmt.Errorf("role is nil")
	}
	if role.Name == "" {
		return fmt.Errorf("role name is required")
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.roles[role.Name] = role
	return nil
}

// GetRole retrieves a role by name.
func (p *PolicyEnforcer) GetRole(name string) (*Role, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.roles[name]
	return r, ok
}

// GetPolicy retrieves a policy by name.
func (p *PolicyEnforcer) GetPolicy(name string) (*Policy, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pol, ok := p.policies[name]
	return pol, ok
}

func matchResource(pattern, resource string) bool {
	if pattern == "*" {
		return true
	}
	// Hierarchical prefix wildcard: "secrets/*" matches "secrets/db" and
	// "secrets/db/creds" but not "secrets" itself nor "secretsX/...".
	if strings.HasSuffix(pattern, "/*") {
		prefix := pattern[:len(pattern)-1] // keep trailing slash, e.g. "secrets/"
		return strings.HasPrefix(resource, prefix)
	}
	return pattern == resource
}

func matchAction(pattern, action string) bool {
	if pattern == "*" {
		return true
	}
	return pattern == action
}
