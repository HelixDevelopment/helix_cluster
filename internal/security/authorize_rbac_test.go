package security

import (
	"context"
	"testing"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGRPCServer_AuthorizeDoesNotGrantAdminToOrdinaryToken pins down a real
// privilege-escalation defect: Authorize must derive the caller's roles from
// the validated token (its scopes), NOT hard-code an "admin" role for every
// authenticated identity. Here an "admin" role IS loaded into the enforcer, so
// the only thing standing between an ordinary token and full admin rights is
// that the token's roles must not silently include "admin".
//
// Mutation: revert Authorize to `Roles: []string{"admin"}` -> an ordinary
// token would match the loaded admin role and be ALLOWED -> this test fails.
func TestGRPCServer_AuthorizeDoesNotGrantAdminToOrdinaryToken(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()
	// An admin role granting everything IS present in the system.
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "admin",
		Permissions: []Permission{{Action: "*", Resource: "*"}},
	}))
	srv := NewGRPCServer(orch, enforcer)

	// An ordinary identity authenticates and gets a real token. Its scopes are
	// read/write (see Orchestrator.ValidateToken), never admin.
	auth, err := srv.Authenticate(ctx, &helixv1.AuthenticateRequest{Identity: "node-ordinary"})
	require.NoError(t, err)
	require.True(t, auth.Success)

	// Attempt an admin-only action. Authentication succeeded, but authorization
	// MUST be denied: an ordinary token does not carry the admin role.
	resp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    auth.Token,
		Resource: "cluster/secrets",
		Action:   "delete",
	})
	require.NoError(t, err)
	assert.False(t, resp.Allowed, "ordinary token must NOT be granted admin-only action")
	assert.NotEmpty(t, resp.Reason)
}

// TestGRPCServer_AuthorizeThreadsTokenScopesIntoPolicy proves the positive
// half: the roles checked by the enforcer are the token's actual scopes
// (read/write), so a permission attached to the "write" role is honored.
//
// Mutation: hard-code Authorize's Roles to []string{"admin"} (or any set that
// omits "write") -> the write permission is never matched -> this test fails.
func TestGRPCServer_AuthorizeThreadsTokenScopesIntoPolicy(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()
	// "write" is one of the scopes ValidateToken returns for a valid token.
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "write",
		Permissions: []Permission{{Action: "put", Resource: "jobs/queue"}},
	}))
	srv := NewGRPCServer(orch, enforcer)

	auth, err := srv.Authenticate(ctx, &helixv1.AuthenticateRequest{Identity: "node-writer"})
	require.NoError(t, err)
	require.True(t, auth.Success)

	resp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    auth.Token,
		Resource: "jobs/queue",
		Action:   "put",
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed, "token's write scope must authorize the matching permission")
}
