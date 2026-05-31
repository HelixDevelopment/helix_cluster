package security

import (
	"context"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	pkgsecurity "github.com/HelixDevelopment/helix_cluster/pkg/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOrchestrator_BoundIdentity_AuthenticateCarriesBoundScopes proves the
// policy-driven binding flows end-to-end: an identity bound to ["operator"]
// authenticates and the resulting token's ValidateToken returns EXACTLY
// ["operator"] -- not the legacy ["read","write"] default.
//
// Mutation: in resolveScopesForIdentity, ignore the binding for empty requests
// (e.g. `if len(requested)==0 { return resolveScopes(requested) }`) -> the
// token would carry ["read","write"] -> the Equal(["operator"]) assertion FAILS.
func TestOrchestrator_BoundIdentity_AuthenticateCarriesBoundScopes(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()
	orch.RegisterIdentityScopes("node-op", []string{"operator"})
	srv := NewGRPCServer(orch, enforcer)

	auth, err := srv.Authenticate(ctx, &helixv1.AuthenticateRequest{Identity: "node-op"})
	require.NoError(t, err)
	require.True(t, auth.Success)

	valid, identity, scopes, err := orch.ValidateToken(ctx, auth.Token)
	require.NoError(t, err)
	require.True(t, valid)
	assert.Equal(t, "node-op", identity)
	assert.Equal(t, []string{"operator"}, scopes, "bound identity must carry exactly its bound scopes")
	assert.NotContains(t, scopes, "read", "unrelated legacy default scopes must not leak in")
	assert.NotContains(t, scopes, "write")
}

// TestGRPCServer_OperatorBound_AllowsOperatorDeniesAdmin is the headline
// real-behavior RBAC test: an identity bound to ["operator"] gets a token that
// is ALLOWED an operator-role action but DENIED an admin-role action, with BOTH
// roles loaded in the enforcer. This asserts the actual allow/deny decision and
// the actual role reached, not mere absence of error.
//
// Mutation: drop the binding lookup so resolveScopesForIdentity always returns
// resolveScopes(req.Scopes) (legacy ["read","write"] for empty request) -> the
// token would carry neither "operator" nor "admin"; the operator action would
// then be DENIED -> assert.True(opResp.Allowed) FAILS. (Symmetrically, if the
// clamp were removed and the token somehow carried "admin", the deny assertion
// below would FAIL.)
func TestGRPCServer_OperatorBound_AllowsOperatorDeniesAdmin(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "operator",
		Permissions: []Permission{{Action: "restart", Resource: "nodes/worker-1"}},
	}))
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "admin",
		Permissions: []Permission{{Action: "*", Resource: "*"}},
	}))
	orch.RegisterIdentityScopes("node-op", []string{"operator"})
	srv := NewGRPCServer(orch, enforcer)

	auth, err := srv.Authenticate(ctx, &helixv1.AuthenticateRequest{Identity: "node-op"})
	require.NoError(t, err)
	require.True(t, auth.Success)

	// Operator-role action is allowed.
	opResp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    auth.Token,
		Resource: "nodes/worker-1",
		Action:   "restart",
	})
	require.NoError(t, err)
	assert.True(t, opResp.Allowed, "operator-bound token must be allowed an operator-role action")

	// Admin-role action is denied: the operator binding never grants admin.
	adminResp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    auth.Token,
		Resource: "cluster/secrets",
		Action:   "delete",
	})
	require.NoError(t, err)
	assert.False(t, adminResp.Allowed, "operator-bound token must be DENIED an admin-only action")
	assert.NotEmpty(t, adminResp.Reason)
}

// TestOrchestrator_UnboundIdentity_DefaultsReadWrite proves the
// backward-compatibility guarantee survives the new binding store: an identity
// with NO binding still defaults to ["read","write"] when authenticated.
//
// Mutation: change the unbound branch of resolveScopesForIdentity to return nil
// (or treat unbound as "no scopes") -> the token would carry no scopes -> the
// Equal(["read","write"]) assertion FAILS.
func TestOrchestrator_UnboundIdentity_DefaultsReadWrite(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	srv := NewGRPCServer(orch, NewPolicyEnforcer())

	auth, err := srv.Authenticate(ctx, &helixv1.AuthenticateRequest{Identity: "node-unbound"})
	require.NoError(t, err)
	require.True(t, auth.Success)

	valid, _, scopes, err := orch.ValidateToken(ctx, auth.Token)
	require.NoError(t, err)
	require.True(t, valid)
	assert.Equal(t, []string{"read", "write"}, scopes, "unbound identity must keep the legacy read,write default")
}

// TestGRPCServer_IssueToken_AdminRequestClampedToReadBinding is the explicit
// anti-self-escalation test: an identity bound to ["read"] requests a token
// with Scopes:["admin"] via the gRPC IssueToken path. The request MUST be
// clamped -- the issued token must NOT carry "admin", and an admin-only action
// (with an admin role loaded that would grant everything) MUST be denied.
//
// Mutation: remove the clamp so resolveScopesForIdentity returns the requested
// scopes verbatim for bound identities (e.g. `return resolveScopes(requested)`)
// -> the token would carry "admin" -> it would reach the loaded admin role ->
// assert.False(adminResp.Allowed) FAILS and the NotContains("admin") FAILS.
func TestGRPCServer_IssueToken_AdminRequestClampedToReadBinding(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()
	// An all-powerful admin role IS loaded; the only guard is the clamp.
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "admin",
		Permissions: []Permission{{Action: "*", Resource: "*"}},
	}))
	// A "read" role that actually authorizes a read action, to prove the
	// surviving (clamped) scope still works.
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "read",
		Permissions: []Permission{{Action: "get", Resource: "jobs/queue"}},
	}))
	orch.RegisterIdentityScopes("node-clamp", []string{"read"})
	srv := NewGRPCServer(orch, enforcer)

	// Caller tries to self-escalate to admin (and sneak in read).
	issue, err := srv.IssueToken(ctx, &helixv1.IssueTokenRequest{
		Identity: "node-clamp",
		Scopes:   []string{"admin", "read"},
	})
	require.NoError(t, err)

	// The token must carry exactly ["read"]: "admin" clamped out, "read" kept.
	valid, _, scopes, err := orch.ValidateToken(ctx, issue.Token)
	require.NoError(t, err)
	require.True(t, valid)
	assert.Equal(t, []string{"read"}, scopes, "requested admin must be clamped; only bound read survives")
	assert.NotContains(t, scopes, "admin", "self-escalation to admin must be denied/clamped")

	// Admin-only action MUST be denied despite the admin role being loaded.
	adminResp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    issue.Token,
		Resource: "cluster/secrets",
		Action:   "delete",
	})
	require.NoError(t, err)
	assert.False(t, adminResp.Allowed, "clamped token must NOT reach the loaded admin role")

	// The surviving clamped read scope still authorizes its read action.
	readResp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    issue.Token,
		Resource: "jobs/queue",
		Action:   "get",
	})
	require.NoError(t, err)
	assert.True(t, readResp.Allowed, "the clamped-in read scope must still authorize a read action")
}

// TestOrchestrator_VaultPath_BoundIdentityClampsRequestedScopes proves the
// anti-escalation clamp is enforced on the Vault PKI issuance path too -- not
// only the local self-signed path. The orchestrator is backed by a Vault client
// that succeeds (returns a real PKICertificate), so IssueCertificate takes the
// `pkiCert != nil` branch (orchestrator.go) rather than falling through to
// issueLocal. The identity is bound to ["read"] yet requests ["admin","read"];
// the issued cert -- which has a Vault serial, proving the Vault branch ran --
// must carry exactly ["read"], and the surviving scope must authorize a real
// read action while the dropped admin scope must NOT reach the loaded admin role.
//
// Mutation: in IssueCertificate's Vault branch, set
// `Scopes: resolveScopes(req.Scopes)` (or `req.Scopes` directly) instead of
// `o.resolveScopesForIdentity(req.NodeID, req.Scopes)` -> the Vault-issued token
// would carry ["admin","read"] -> the Equal(["read"]) assertion FAILS and the
// admin Authorize would be Allowed, failing assert.False(adminResp.Allowed).
func TestOrchestrator_VaultPath_BoundIdentityClampsRequestedScopes(t *testing.T) {
	ctx := context.Background()
	mock := &pkgsecurity.MockVaultClient{
		IssueFn: func(_ context.Context, _, _ string, _ pkgsecurity.PKIIssueParams) (*pkgsecurity.PKICertificate, error) {
			return &pkgsecurity.PKICertificate{
				Certificate:   "VAULT-CERT",
				PrivateKey:    "VAULT-KEY",
				Serial:        "vault-serial-clamp",
				LeaseDuration: time.Hour,
			}, nil
		},
	}
	orch := NewOrchestrator(pkgsecurity.NewVaultWrapper(mock))
	enforcer := NewPolicyEnforcer()
	// An all-powerful admin role IS loaded; the only guard is the clamp.
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "admin",
		Permissions: []Permission{{Action: "*", Resource: "*"}},
	}))
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "read",
		Permissions: []Permission{{Action: "get", Resource: "jobs/queue"}},
	}))
	orch.RegisterIdentityScopes("vault-clamp", []string{"read"})

	// Issue directly through the orchestrator's Vault path, self-escalating.
	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{
		NodeID: "vault-clamp",
		Scopes: []string{"admin", "read"},
	})
	require.NoError(t, err)
	// Proves the Vault branch (not issueLocal) actually produced this cert.
	require.Equal(t, "vault-serial-clamp", cert.Serial, "cert must come from the Vault PKI path")
	assert.Equal(t, []byte("VAULT-CERT"), cert.CertPEM)

	// The Vault-issued token must carry exactly ["read"]: admin clamped out.
	valid, identity, scopes, err := orch.ValidateToken(ctx, cert.ID)
	require.NoError(t, err)
	require.True(t, valid)
	assert.Equal(t, "vault-clamp", identity)
	assert.Equal(t, []string{"read"}, scopes, "Vault path must clamp requested admin to the bound read scope")
	assert.NotContains(t, scopes, "admin", "self-escalation via the Vault path must be denied/clamped")

	srv := NewGRPCServer(orch, enforcer)

	// Admin-only action MUST be denied despite the admin role being loaded.
	adminResp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    cert.ID,
		Resource: "cluster/secrets",
		Action:   "delete",
	})
	require.NoError(t, err)
	assert.False(t, adminResp.Allowed, "Vault-issued clamped token must NOT reach the loaded admin role")

	// The surviving clamped read scope still authorizes its read action.
	readResp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    cert.ID,
		Resource: "jobs/queue",
		Action:   "get",
	})
	require.NoError(t, err)
	assert.True(t, readResp.Allowed, "the clamped-in read scope must still authorize a read action on the Vault path")
}

// TestOrchestrator_RegisterIdentityScopes_DefensiveCopyAndReplace proves the
// binding store copies its input (caller mutation cannot corrupt a binding) and
// that re-registering replaces the binding.
//
// Mutation: store the caller's slice directly in RegisterIdentityScopes (drop
// the copy) -> mutating the caller's slice would corrupt the binding -> the
// post-mutation BoundScopes assertion FAILS.
func TestOrchestrator_RegisterIdentityScopes_DefensiveCopyAndReplace(t *testing.T) {
	orch := NewOrchestrator(nil)

	input := []string{"operator"}
	orch.RegisterIdentityScopes("node-z", input)
	input[0] = "admin" // must not affect the stored binding

	bound, ok := orch.BoundScopes("node-z")
	require.True(t, ok)
	assert.Equal(t, []string{"operator"}, bound, "binding must be a defensive copy, immune to caller mutation")

	// Re-registering replaces the binding.
	orch.RegisterIdentityScopes("node-z", []string{"read"})
	bound2, ok := orch.BoundScopes("node-z")
	require.True(t, ok)
	assert.Equal(t, []string{"read"}, bound2, "re-registration must replace the prior binding")
}
