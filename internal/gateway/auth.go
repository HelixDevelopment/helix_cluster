package gateway

import (
	"net/http"
	"sort"
	"strings"

	"github.com/HelixDevelopment/helix_cluster/pkg/jwt"
)

// AuthPolicy declares the JWT/RBAC enforcement rules for a Gateway.
//
// Enforcement is OFF unless a policy is installed via WithAuth. With no policy
// the gateway preserves its historical open behavior (backward compatible).
//
// A request is authorized when its path matches a prefix rule (longest prefix
// wins) — or falls back to DefaultScope — and the validated token carries the
// required scope. An empty required scope ("") means "any valid token", i.e.
// authentication-only with no scope check.
type AuthPolicy struct {
	// Secret is the HMAC secret used to validate HS256 bearer tokens. It must
	// be non-empty for enforcement to be meaningful.
	Secret []byte

	// Prefixes maps a path prefix to the scope a caller must hold to reach any
	// route under it. Longest matching prefix wins so more specific rules can
	// override broader ones.
	Prefixes map[string]string

	// DefaultScope is required when no prefix in Prefixes matches the request
	// path. An empty string permits any validated token by default.
	DefaultScope string

	// sortedPrefixes is Prefixes' keys ordered longest-first; built by compile.
	sortedPrefixes []string
}

// compile pre-sorts the prefix rules longest-first so RequiredScope can pick
// the most specific match deterministically.
func (p *AuthPolicy) compile() {
	p.sortedPrefixes = make([]string, 0, len(p.Prefixes))
	for k := range p.Prefixes {
		p.sortedPrefixes = append(p.sortedPrefixes, k)
	}
	sort.Slice(p.sortedPrefixes, func(i, j int) bool {
		return len(p.sortedPrefixes[i]) > len(p.sortedPrefixes[j])
	})
}

// RequiredScope returns the scope a request to path must satisfy. The longest
// matching prefix wins; with no match the DefaultScope applies.
func (p *AuthPolicy) RequiredScope(path string) string {
	for _, prefix := range p.sortedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return p.Prefixes[prefix]
		}
	}
	return p.DefaultScope
}

// Option configures a Gateway at construction time.
type Option func(*Gateway)

// WithAuth enables JWT/RBAC enforcement using the given policy. Passing this
// option turns on the auth gate in ServeHTTP; without it the gateway keeps its
// open behavior, so existing callers are unaffected.
func WithAuth(policy AuthPolicy) Option {
	return func(g *Gateway) {
		policy.compile()
		g.auth = &policy
	}
}

// claimScopes extracts the set of scopes a token grants. It accepts both the
// OAuth-style space-delimited "scope" string claim and array-valued "scope"/
// "scopes"/"roles" claims, so RBAC works regardless of which the issuer used.
func claimScopes(payload map[string]interface{}) map[string]struct{} {
	out := make(map[string]struct{})
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" {
			out[s] = struct{}{}
		}
	}
	for _, key := range []string{"scope", "scopes", "roles"} {
		switch v := payload[key].(type) {
		case string:
			for _, f := range strings.Fields(v) {
				add(f)
			}
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					add(s)
				}
			}
		case []string:
			for _, s := range v {
				add(s)
			}
		}
	}
	return out
}

// authError describes why a request was rejected and the HTTP status to use.
// reason is a human-readable, single-line summary emitted as the
// X-Helix-Deny-Reason response header so callers can diagnose rejections
// without parsing the JSON body.
type authError struct {
	status int
	body   string
	reason string
}

// authorize validates the bearer token on r and checks it carries the scope
// required for r's path. It returns (nil, claims) when the request may proceed
// to the upstream — claims are non-nil so the caller can inject them into the
// request context. It returns (non-nil *authError, nil) on rejection (401/403).
// The upstream is never contacted on a non-nil *authError return.
func (g *Gateway) authorize(r *http.Request) (*authError, map[string]interface{}) {
	policy := g.auth
	if policy == nil {
		return nil, nil // enforcement disabled: open behavior preserved.
	}

	authz := r.Header.Get("Authorization")
	const bearer = "Bearer "
	if !strings.HasPrefix(authz, bearer) {
		return &authError{http.StatusUnauthorized, `{"error":"missing bearer token","status":401}`, "missing bearer token"}, nil
	}
	raw := strings.TrimSpace(authz[len(bearer):])
	if raw == "" {
		return &authError{http.StatusUnauthorized, `{"error":"missing bearer token","status":401}`, "missing bearer token"}, nil
	}

	// ParseToken validates structure, HS256 signature, and expiry in one shot.
	claims, err := jwt.ParseToken(raw, policy.Secret)
	if err != nil {
		return &authError{http.StatusUnauthorized, `{"error":"invalid or expired token","status":401}`, "invalid or expired token"}, nil
	}

	required := policy.RequiredScope(r.URL.Path)
	if required == "" {
		return nil, claims // any valid token is sufficient for this path.
	}
	if _, ok := claimScopes(claims)[required]; !ok {
		return &authError{http.StatusForbidden, `{"error":"insufficient scope","status":403}`, "insufficient scope"}, nil
	}
	return nil, claims
}

// writeAuthError emits a JSON auth rejection. It stamps the gateway marker so
// clients can still tell the response came from the gateway, but it is written
// BEFORE any upstream is contacted.
//
// X-Helix-Deny-Reason is set when e.reason is non-empty so callers can
// diagnose rejections without parsing the JSON body (HXC-1213).
func writeAuthError(w http.ResponseWriter, e *authError) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(gatewayHeader, "true")
	if e.reason != "" {
		w.Header().Set("X-Helix-Deny-Reason", e.reason)
	}
	w.WriteHeader(e.status)
	_, _ = w.Write([]byte(e.body))
}
