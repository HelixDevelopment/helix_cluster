package security

// identity_bindings.go adds a policy-driven identity->scopes binding store to
// the Orchestrator. Today every authenticated identity defaulted to the
// hard-coded ["read","write"] scope set. With a binding registered for an
// identity, Authenticate issues a credential carrying exactly the bound scopes,
// and an explicit scope request is CLAMPED to the binding so a caller cannot
// self-escalate beyond what its identity is permitted (e.g. an identity bound
// to ["read"] cannot mint an ["admin"] token).
//
// BACKWARD COMPATIBILITY: an UNBOUND identity behaves exactly as before:
// explicit requested scopes are honored verbatim, and an empty request falls
// back to the legacy ["read","write"] default. Clamping applies ONLY when a
// binding has been registered for the identity.

// RegisterIdentityScopes binds an identity (the NodeID/identity used at
// issuance time) to the set of scopes it is permitted to carry. Re-registering
// the same identity replaces its binding. A defensive copy is stored so the
// caller's slice cannot mutate the binding afterwards.
//
// Registering an empty (or nil) scope set binds the identity to "no scopes":
// any subsequently issued credential for that identity carries zero scopes
// (fully de-privileged), distinct from the unbound default of ["read","write"].
//
// NOTE: an Orchestrator MUST be constructed via NewOrchestrator, which
// initialises identityScopes (and the certs/revokedSerials maps). A zero-value
// Orchestrator is not supported: its maps are nil and the first map write would
// panic. We deliberately do NOT lazily nil-init identityScopes here, because
// doing so for one map and not the others would give a false impression of
// zero-value safety.
func (o *Orchestrator) RegisterIdentityScopes(identity string, scopes []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	bound := make([]string, len(scopes))
	copy(bound, scopes)
	o.identityScopes[identity] = bound
}

// BoundScopes returns a defensive copy of the scopes bound to identity and
// whether a binding exists. Intended for tests/diagnostics.
func (o *Orchestrator) BoundScopes(identity string) ([]string, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	bound, ok := o.identityScopes[identity]
	if !ok {
		return nil, false
	}
	out := make([]string, len(bound))
	copy(out, bound)
	return out, true
}

// resolveScopesForIdentity computes the effective scopes for a credential being
// issued to identity, given the explicitly requested scopes. It MUST be called
// with o.mu held (read or write) because it reads o.identityScopes.
//
//   - UNBOUND identity: behaves like the package-level resolveScopes — requested
//     scopes are honored verbatim; an empty request defaults to ["read","write"].
//   - BOUND identity, empty request: the bound scopes are granted.
//   - BOUND identity, non-empty request: the request is CLAMPED to the binding,
//     i.e. the result is requested ∩ bound (preserving the order requested by the
//     caller). Any requested scope NOT in the binding is dropped — this is the
//     anti-escalation guarantee.
func (o *Orchestrator) resolveScopesForIdentity(identity string, requested []string) []string {
	bound, isBound := o.identityScopes[identity]
	if !isBound {
		// Backward-compatible path: no binding => legacy behavior.
		return resolveScopes(requested)
	}

	// Empty request under a binding => grant exactly the bound scopes.
	if len(requested) == 0 {
		out := make([]string, len(bound))
		copy(out, bound)
		return out
	}

	// Non-empty request under a binding => clamp to the intersection.
	allowed := make(map[string]struct{}, len(bound))
	for _, s := range bound {
		allowed[s] = struct{}{}
	}
	out := make([]string, 0, len(requested))
	for _, s := range requested {
		if _, ok := allowed[s]; ok {
			out = append(out, s)
		}
	}
	return out
}
