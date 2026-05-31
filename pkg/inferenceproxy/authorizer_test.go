package inferenceproxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// TestTokenAuthorizer_ValidTokenAdmits proves a token minted by the authorizer
// (real HMAC-SHA256 over the subject) admits, and is byte-identical to an
// independently computed HMAC — pinning the construction to real crypto.
//
// Mutation: replacing the HMAC in MintToken/Authorize with a constant or plain
// SHA-256 (unkeyed) makes the independently-computed expected tag differ and
// fails the equality assertion; verification of a forged token would also pass.
func TestTokenAuthorizer_ValidTokenAdmits(t *testing.T) {
	secret := []byte("super-secret-key")
	auth := NewTokenAuthorizer(secret, "infer")

	tok := auth.MintToken("alice")

	// Independent real HMAC-SHA256 reference computation.
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("alice"))
	want := hex.EncodeToString(mac.Sum(nil))
	if tok != want {
		t.Fatalf("MintToken = %q, want independent HMAC-SHA256 %q", tok, want)
	}

	if err := auth.Authorize(Request{Subject: "alice", Token: tok, Scope: "infer"}); err != nil {
		t.Fatalf("Authorize(valid token) = %v, want admit (nil)", err)
	}
}

// TestTokenAuthorizer_ForgedTokenDenied proves a token that is NOT the correct
// HMAC is rejected with ErrBadToken, and a token minted for a DIFFERENT subject
// does not authorize the wrong subject (MAC binds to subject).
//
// Mutation: dropping the constant-time MAC compare (e.g. always returning admit)
// fails both rejections; using the token for any subject fails the cross-subject
// rejection.
func TestTokenAuthorizer_ForgedTokenDenied(t *testing.T) {
	secret := []byte("super-secret-key")
	auth := NewTokenAuthorizer(secret, "infer")

	// Garbage hex of the right length is not a valid MAC.
	forged := hex.EncodeToString(make([]byte, sha256.Size))
	if err := auth.Authorize(Request{Subject: "alice", Token: forged, Scope: "infer"}); !errors.Is(err, ErrBadToken) {
		t.Fatalf("Authorize(forged) = %v, want ErrBadToken", err)
	}

	// Token valid for "bob" must not authorize "alice".
	bobTok := auth.MintToken("bob")
	if err := auth.Authorize(Request{Subject: "alice", Token: bobTok, Scope: "infer"}); !errors.Is(err, ErrBadToken) {
		t.Fatalf("Authorize(bob's token as alice) = %v, want ErrBadToken (MAC binds subject)", err)
	}
}

// TestTokenAuthorizer_MissingTokenAndScope pins the missing-token and scope
// rejections.
//
// Mutation: removing the empty-token guard returns ErrBadToken/nil instead of
// ErrMissingToken; removing the scope check admits a wrong-scope request.
func TestTokenAuthorizer_MissingTokenAndScope(t *testing.T) {
	secret := []byte("k")
	auth := NewTokenAuthorizer(secret, "infer")

	if err := auth.Authorize(Request{Subject: "alice", Token: "", Scope: "infer"}); !errors.Is(err, ErrMissingToken) {
		t.Fatalf("Authorize(no token) = %v, want ErrMissingToken", err)
	}

	tok := auth.MintToken("alice")
	if err := auth.Authorize(Request{Subject: "alice", Token: tok, Scope: "admin"}); !errors.Is(err, ErrScopeDenied) {
		t.Fatalf("Authorize(valid token, wrong scope) = %v, want ErrScopeDenied", err)
	}
}

// TestTokenAuthorizer_EmptyScopeSetPermitsAny proves an authorizer built with no
// permitted scopes disables the scope check (any scope admits with a valid MAC).
//
// Mutation: treating an empty scope set as "permit none" would reject this and
// fail the assertion.
func TestTokenAuthorizer_EmptyScopeSetPermitsAny(t *testing.T) {
	secret := []byte("k")
	auth := NewTokenAuthorizer(secret) // no scopes
	tok := auth.MintToken("alice")
	if err := auth.Authorize(Request{Subject: "alice", Token: tok, Scope: "anything"}); err != nil {
		t.Fatalf("Authorize(valid token, empty scope set) = %v, want admit", err)
	}
}

// TestProxy_EndToEnd_TokenGate wires the real TokenAuthorizer into the proxy and
// asserts the full end-user path: a correctly-tokened request reaches the
// backend and returns its response; a forged-token request is rejected with the
// backend never invoked.
//
// Mutation: forwarding before authorizing lets the forged request hit the
// backend, failing the hits==1 (not 2) assertion.
func TestProxy_EndToEnd_TokenGate(t *testing.T) {
	secret := []byte("deploy-key")
	auth := NewTokenAuthorizer(secret, "infer")
	backend := &countingBackend{resp: Response{Model: "qwen", Text: "[ref] answer", Tokens: 3}}
	audit := NewMemAudit()
	p := mustProxy(t, backend, auth, audit, fixedClock(time.Unix(99, 0)))

	good := auth.MintToken("alice")
	resp, err := p.Infer(context.Background(), Request{Subject: "alice", Model: "qwen", Prompt: "q", Token: good, Scope: "infer"})
	if err != nil {
		t.Fatalf("authorized Infer: %v", err)
	}
	if resp.Text != "[ref] answer" || resp.Tokens != 3 {
		t.Fatalf("response = %+v, want exact backend response", resp)
	}

	_, err = p.Infer(context.Background(), Request{Subject: "mallory", Model: "qwen", Prompt: "q", Token: "00", Scope: "infer"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("forged Infer err = %v, want ErrUnauthorized", err)
	}

	if h := backend.hits.Load(); h != 1 {
		t.Fatalf("backend hits = %d, want exactly 1 (only the authorized request)", h)
	}
	if audit.Len() != 2 {
		t.Fatalf("audit entries = %d, want 2 (one allow, one deny)", audit.Len())
	}
}
