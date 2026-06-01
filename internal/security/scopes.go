// Package security — scopes.go defines the canonical Helix Cluster OS scope
// set and the route-to-required-scope mapping used by the policy enforcer.
//
// Scope constants follow the "<domain>:<privilege>" convention so the token
// bearer's capabilities are self-describing and can be audited without
// consulting external documentation.
package security

import "strings"

// Canonical Helix Cluster OS scope constants.
const (
	// Node management scopes.
	ScopeNodeRead  = "node:read"
	ScopeNodeWrite = "node:write"

	// Session management scopes.
	ScopeSessionRead  = "session:read"
	ScopeSessionWrite = "session:write"

	// Pool management scopes.
	ScopePoolRead = "pool:read"

	// Scheduler management scopes.
	ScopeScheduleWrite = "schedule:write"

	// Advisory/control-plane admin scope.
	ScopeAdvisoryAdmin = "advisory:admin"
)

// AllDefinedScopes is the full enumeration of scopes the cluster recognises.
// Any scope not in this list is unknown to the system and will not be granted
// by any route mapping; it may however be carried by a credential if the
// issuer placed it there explicitly (forward-compat).
var AllDefinedScopes = []string{
	ScopeNodeRead,
	ScopeNodeWrite,
	ScopeSessionRead,
	ScopeSessionWrite,
	ScopePoolRead,
	ScopeScheduleWrite,
	ScopeAdvisoryAdmin,
}

// routeScopeMap maps a "<method> <route-prefix>" pattern (case-insensitive on
// the method, exact-prefix match on the route) to the minimum scope required
// to perform that operation.  The first matching entry wins.
//
// The table encodes the Helix security model:
//   - Reads (GET / LIST / DESCRIBE) require the :read scope for the domain.
//   - Mutations (POST / PUT / PATCH / DELETE / CREATE / UPDATE) require the
//     :write or :admin scope for the domain.
//   - Scheduler mutations require schedule:write regardless of the HTTP verb
//     because scheduler actions can have cluster-wide impact.
var routeScopeMap = []routeScopeEntry{
	// ── Advisory (control-plane admin) ──────────────────────────────────────
	{method: "any", prefix: "/advisory/", scope: ScopeAdvisoryAdmin},

	// ── Scheduler ───────────────────────────────────────────────────────────
	{method: "any", prefix: "/schedule/", scope: ScopeScheduleWrite},

	// ── Sessions — mutating ─────────────────────────────────────────────────
	{method: "post", prefix: "/session", scope: ScopeSessionWrite},
	{method: "put", prefix: "/session", scope: ScopeSessionWrite},
	{method: "patch", prefix: "/session", scope: ScopeSessionWrite},
	{method: "delete", prefix: "/session", scope: ScopeSessionWrite},

	// ── Sessions — read-only ────────────────────────────────────────────────
	{method: "get", prefix: "/session", scope: ScopeSessionRead},

	// ── Nodes — mutating ────────────────────────────────────────────────────
	{method: "post", prefix: "/node", scope: ScopeNodeWrite},
	{method: "put", prefix: "/node", scope: ScopeNodeWrite},
	{method: "patch", prefix: "/node", scope: ScopeNodeWrite},
	{method: "delete", prefix: "/node", scope: ScopeNodeWrite},

	// ── Nodes — read-only ───────────────────────────────────────────────────
	{method: "get", prefix: "/node", scope: ScopeNodeRead},

	// ── Pool — read-only (mutations go through the scheduler) ───────────────
	{method: "get", prefix: "/pool", scope: ScopePoolRead},
}

type routeScopeEntry struct {
	method string // "any" matches all methods
	prefix string // matched as a URL-path prefix (case-sensitive)
	scope  string // required scope constant
}

// AuthzDecision is the result of an Authorize call.
type AuthzDecision struct {
	// Allowed is true iff the identity holds all required scopes.
	Allowed bool
	// RequiredScope is the single scope the route demands.
	RequiredScope string
	// EvaluatedScopes is the snapshot of the identity's scope set that was
	// consulted during the decision.  The caller can surface this in logs or
	// error responses without an additional round-trip.
	EvaluatedScopes []string
	// Reason is a human-readable denial message when Allowed is false.
	Reason string
}

// Authorize resolves whether identity (identified by its scope set held in
// identityScopes) may perform action on route.
//
// route is a URL path such as "/session/create"; action is the HTTP method or
// a logical verb ("GET", "POST", "session-create", etc.).  A leading verb in
// the action string ("GET /session/create") is split automatically.
//
// The returned AuthzDecision always contains the EvaluatedScopes that were
// consulted, enabling the caller to surface them for audit/debug purposes.
//
// This function is intentionally PURE (no receiver state): it works from the
// explicitly supplied identityScopes slice, making it trivially testable.
func Authorize(identityScopes []string, action, route string) AuthzDecision {
	// Normalise: if action contains a space the caller may have supplied
	// "METHOD /route" in the action field; split and re-assign.
	method := strings.ToLower(action)
	resolvedRoute := route
	if parts := strings.SplitN(action, " ", 2); len(parts) == 2 {
		method = strings.ToLower(strings.TrimSpace(parts[0]))
		if resolvedRoute == "" {
			resolvedRoute = strings.TrimSpace(parts[1])
		}
	}

	// Defensive copy of the caller-supplied scope set so the returned decision
	// holds an immutable snapshot even if the caller mutates its slice later.
	evaluated := make([]string, len(identityScopes))
	copy(evaluated, identityScopes)

	required, ok := requiredScope(method, resolvedRoute)
	if !ok {
		// No matching route entry → deny by default (fail-closed).
		return AuthzDecision{
			Allowed:         false,
			RequiredScope:   "",
			EvaluatedScopes: evaluated,
			Reason:          "no scope rule defined for route: " + resolvedRoute,
		}
	}

	// Build a set from identityScopes for O(1) lookup.
	held := make(map[string]struct{}, len(identityScopes))
	for _, s := range identityScopes {
		held[s] = struct{}{}
	}

	if _, has := held[required]; has {
		return AuthzDecision{
			Allowed:         true,
			RequiredScope:   required,
			EvaluatedScopes: evaluated,
		}
	}

	return AuthzDecision{
		Allowed:         false,
		RequiredScope:   required,
		EvaluatedScopes: evaluated,
		Reason:          "identity lacks required scope " + required,
	}
}

// requiredScope returns the scope demanded by the (method, route) pair.
// method must already be lower-cased. ok is false when no entry matches.
func requiredScope(method, route string) (string, bool) {
	for _, e := range routeScopeMap {
		if e.method != "any" && e.method != method {
			continue
		}
		if strings.HasPrefix(route, e.prefix) {
			return e.scope, true
		}
	}
	return "", false
}
