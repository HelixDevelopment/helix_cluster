package inferenceproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
)

// Authorization-failure reasons, returned by TokenAuthorizer. They are wrapped
// by the proxy under ErrUnauthorized; callers can also match these directly.
var (
	// ErrMissingToken indicates the request carried no token.
	ErrMissingToken = errors.New("inferenceproxy: missing token")
	// ErrBadToken indicates the token's MAC did not verify against the secret.
	ErrBadToken = errors.New("inferenceproxy: token signature invalid")
	// ErrScopeDenied indicates a valid token lacks the scope the request needs.
	ErrScopeDenied = errors.New("inferenceproxy: scope not permitted")
)

// TokenAuthorizer is a WORKING reference Authorizer. A request is admitted iff
// its Token is a valid HMAC-SHA256(secret, Subject) tag (hex-encoded) AND the
// request Scope is in the permitted set. Verification uses crypto/hmac with a
// constant-time comparison (crypto/subtle), so it resists timing oracles and
// forged tokens. This is a real keyed-MAC check, not a placeholder.
type TokenAuthorizer struct {
	secret []byte
	scopes map[string]struct{}
}

// NewTokenAuthorizer builds an authorizer keyed by secret that permits the given
// scopes. An empty scope set permits any scope (scope check disabled); a
// non-empty set admits only requests whose Scope is listed.
func NewTokenAuthorizer(secret []byte, permittedScopes ...string) *TokenAuthorizer {
	scopes := make(map[string]struct{}, len(permittedScopes))
	for _, s := range permittedScopes {
		scopes[s] = struct{}{}
	}
	// Copy the secret so later caller mutation cannot weaken verification.
	sec := make([]byte, len(secret))
	copy(sec, secret)
	return &TokenAuthorizer{secret: sec, scopes: scopes}
}

// MintToken returns the canonical token for a subject: the hex-encoded
// HMAC-SHA256(secret, subject). A client presents this as Request.Token. This is
// the same computation Authorize verifies, so a minted token always admits.
func (a *TokenAuthorizer) MintToken(subject string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(subject))
	return hex.EncodeToString(mac.Sum(nil))
}

// Authorize implements Authorizer. It verifies the token MAC over the subject,
// then enforces scope. It returns nil only when BOTH checks pass.
func (a *TokenAuthorizer) Authorize(req Request) error {
	if req.Token == "" {
		return ErrMissingToken
	}
	presented, err := hex.DecodeString(req.Token)
	if err != nil {
		return ErrBadToken
	}
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(req.Subject))
	expected := mac.Sum(nil)
	// Constant-time compare; hmac.Equal wraps subtle.ConstantTimeCompare.
	if subtle.ConstantTimeCompare(presented, expected) != 1 {
		return ErrBadToken
	}
	if len(a.scopes) > 0 {
		if _, ok := a.scopes[req.Scope]; !ok {
			return ErrScopeDenied
		}
	}
	return nil
}

// compile-time assertion.
var _ Authorizer = (*TokenAuthorizer)(nil)
