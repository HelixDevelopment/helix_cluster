package security

import (
	"context"
	"testing"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOrchestrator_PerCredentialScopes_ReadOnly proves that the scopes bound to
// a credential at issue time flow back verbatim through ValidateToken: a cert
// issued with Scopes:["read"] returns EXACTLY ["read"], not the legacy
// hard-coded ["read","write"].
//
// Mutation: revert ValidateToken to `scopes := []string{"read","write"}` ->
// the returned scopes would be ["read","write"] and the exact-equality
// assertion (and the "write must be absent" assertion) would FAIL.
func TestOrchestrator_PerCredentialScopes_ReadOnly(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{
		NodeID: "node-ro",
		Scopes: []string{"read"},
	})
	require.NoError(t, err)

	valid, identity, scopes, err := orch.ValidateToken(ctx, cert.ID)
	require.NoError(t, err)
	require.True(t, valid)
	assert.Equal(t, "node-ro", identity)
	assert.Equal(t, []string{"read"}, scopes, "must return exactly the issued scopes")
	assert.NotContains(t, scopes, "write", "write must not appear for a read-only credential")
}

// TestOrchestrator_PerCredentialScopes_Admin proves an arbitrary scope ("admin")
// flows through unchanged, demonstrating these really are per-credential and not
// a fixed allow-list.
//
// Mutation: hard-code ValidateToken's scopes to anything not equal to ["admin"]
// (e.g. the legacy ["read","write"]) -> equality assertion FAILS.
func TestOrchestrator_PerCredentialScopes_Admin(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{
		NodeID: "node-admin",
		Scopes: []string{"admin"},
	})
	require.NoError(t, err)

	valid, _, scopes, err := orch.ValidateToken(ctx, cert.ID)
	require.NoError(t, err)
	require.True(t, valid)
	assert.Equal(t, []string{"admin"}, scopes)
}

// TestOrchestrator_PerCredentialScopes_DefaultBackCompat proves the
// backward-compatibility guarantee: a credential issued with NO scopes defaults
// to ["read","write"], matching the legacy hard-coded behavior so existing
// callers and tests keep working.
//
// Mutation: change resolveScopes' default from {"read","write"} to nil/empty ->
// the returned scopes would be empty -> equality assertion FAILS. (This also
// pins the legacy contract relied on by TestGRPCServer_CheckPolicy and
// authorize_rbac_test.go.)
func TestOrchestrator_PerCredentialScopes_DefaultBackCompat(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "node-default"})
	require.NoError(t, err)

	valid, _, scopes, err := orch.ValidateToken(ctx, cert.ID)
	require.NoError(t, err)
	require.True(t, valid)
	assert.Equal(t, []string{"read", "write"}, scopes, "empty scopes must default to read,write")
}

// TestOrchestrator_ScopesAreDefensivelyCopied proves the stored credential is
// immune to mutation through either the caller's input slice or the returned
// slice. This is a real isolation property, not a no-panic check.
//
// Mutation: make resolveScopes return `requested` directly (no copy) -> mutating
// caller's slice would corrupt the stored scopes -> the post-mutation
// ValidateToken assertion FAILS. Likewise, make ValidateToken return cert.Scopes
// directly -> mutating the returned slice would corrupt the store -> the second
// ValidateToken assertion FAILS.
func TestOrchestrator_ScopesAreDefensivelyCopied(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	input := []string{"read"}
	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "node-copy", Scopes: input})
	require.NoError(t, err)

	// Mutating the caller's input slice must not affect stored scopes.
	input[0] = "admin"

	_, _, scopes1, err := orch.ValidateToken(ctx, cert.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, scopes1, "stored scopes must not be affected by caller mutation")

	// Mutating the returned slice must not affect the next read.
	scopes1[0] = "admin"
	_, _, scopes2, err := orch.ValidateToken(ctx, cert.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"read"}, scopes2, "returned slice must be a copy, not the stored backing array")
}

// TestGRPCServer_Authorize_ReadOnlyTokenDeniedWriteAllowedRead proves the END-TO-END
// authorization decision really keys off per-credential scopes: a read-only
// token is DENIED a write action but ALLOWED a read action, with both roles
// loaded in the enforcer.
//
// Mutation: revert ValidateToken to the legacy ["read","write"] -> the
// read-only token would also carry "write" -> the write action would be
// ALLOWED -> the assert.False(deny) FAILS.
func TestGRPCServer_Authorize_ReadOnlyTokenDeniedWriteAllowedRead(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "read",
		Permissions: []Permission{{Action: "get", Resource: "jobs/queue"}},
	}))
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "write",
		Permissions: []Permission{{Action: "put", Resource: "jobs/queue"}},
	}))
	srv := NewGRPCServer(orch, enforcer)

	// Issue a read-only token via the gRPC IssueToken path (scopes threaded
	// through the proto request).
	issue, err := srv.IssueToken(ctx, &helixv1.IssueTokenRequest{
		Identity: "node-ro",
		Scopes:   []string{"read"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, issue.Token)

	// Read action is allowed (token carries "read").
	allowResp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    issue.Token,
		Resource: "jobs/queue",
		Action:   "get",
	})
	require.NoError(t, err)
	assert.True(t, allowResp.Allowed, "read-only token must be allowed a read action")

	// Write action is denied (token does NOT carry "write").
	denyResp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    issue.Token,
		Resource: "jobs/queue",
		Action:   "put",
	})
	require.NoError(t, err)
	assert.False(t, denyResp.Allowed, "read-only token must be DENIED a write-only action")
	assert.NotEmpty(t, denyResp.Reason)
}

// TestGRPCServer_Authorize_AdminScopeReachesAdminRole proves a credential issued
// with Scopes:["admin"] can reach a loaded admin role end-to-end -- confirming
// the per-cert scope genuinely flows into the RBAC decision.
//
// Mutation: revert ValidateToken to the legacy ["read","write"] -> the token
// would NOT carry "admin" -> the admin-only action would be DENIED ->
// assert.True(Allowed) FAILS.
func TestGRPCServer_Authorize_AdminScopeReachesAdminRole(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "admin",
		Permissions: []Permission{{Action: "*", Resource: "*"}},
	}))
	// "read"/"write" roles deliberately NOT loaded: only an admin-scoped token
	// can reach the admin role.
	srv := NewGRPCServer(orch, enforcer)

	issue, err := srv.IssueToken(ctx, &helixv1.IssueTokenRequest{
		Identity: "node-admin",
		Scopes:   []string{"admin"},
	})
	require.NoError(t, err)

	resp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    issue.Token,
		Resource: "cluster/secrets",
		Action:   "delete",
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed, "admin-scoped token must reach the loaded admin role")
}

// TestGRPCServer_IssueToken_DefaultScopesBackCompat proves the gRPC IssueToken
// path still defaults to read/write when no scopes are supplied, so an
// ordinary token authorizes a write-role action exactly as before.
//
// Mutation: change resolveScopes default to nil -> the default token would
// carry no scopes -> the write action would be DENIED -> assert.True FAILS.
func TestGRPCServer_IssueToken_DefaultScopesBackCompat(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "write",
		Permissions: []Permission{{Action: "put", Resource: "jobs/queue"}},
	}))
	srv := NewGRPCServer(orch, enforcer)

	issue, err := srv.IssueToken(ctx, &helixv1.IssueTokenRequest{Identity: "node-default"})
	require.NoError(t, err)

	resp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    issue.Token,
		Resource: "jobs/queue",
		Action:   "put",
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed, "default-scoped token must retain legacy write access")
}
