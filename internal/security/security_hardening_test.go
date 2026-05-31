package security

import (
	"context"
	"fmt"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/pkg/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ValidateToken: allow/deny decision surface ---------------------------

// TestValidateToken_AllowValid asserts a freshly issued, unrevoked,
// unexpired certificate yields a positive validation with identity + scopes.
// Mutation: in ValidateToken change `return true, cert.NodeID, scopes, nil`
// to `return false, "", nil, nil` -> this test fails.
func TestValidateToken_AllowValid(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "node-allow", TTL: time.Hour})
	require.NoError(t, err)

	valid, identity, scopes, err := orch.ValidateToken(ctx, cert.ID)
	require.NoError(t, err)
	assert.True(t, valid)
	assert.Equal(t, "node-allow", identity)
	assert.Equal(t, []string{"read", "write"}, scopes)
}

// TestValidateToken_DenyRevoked asserts a revoked certificate is denied even
// though it still exists and is unexpired.
// Mutation: remove the `if cert.Revoked { return false... }` block in
// ValidateToken -> a revoked token would validate -> this test fails.
func TestValidateToken_DenyRevoked(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "node-rev", TTL: time.Hour})
	require.NoError(t, err)
	require.NoError(t, orch.RevokeCertificate(ctx, cert.ID))

	valid, identity, scopes, err := orch.ValidateToken(ctx, cert.ID)
	require.NoError(t, err)
	assert.False(t, valid)
	assert.Empty(t, identity)
	assert.Nil(t, scopes)
}

// TestValidateToken_DenyExpired asserts a certificate past its ExpiresAt is
// denied. Mutation: remove the `if time.Now().After(cert.ExpiresAt)` block ->
// expired token validates -> this test fails.
func TestValidateToken_DenyExpired(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "node-exp", TTL: time.Hour})
	require.NoError(t, err)

	// Force expiry directly on the stored cert.
	stored, ok := orch.GetCertificate(cert.ID)
	require.True(t, ok)
	stored.ExpiresAt = time.Now().Add(-time.Minute)

	valid, _, _, err := orch.ValidateToken(ctx, cert.ID)
	require.NoError(t, err)
	assert.False(t, valid)
}

// TestValidateToken_DenyUnknownAndEmpty covers the unknown-token (no error,
// not valid) and empty-token (error) branches distinctly.
// Mutation: change the unknown-token `return false, "", nil, nil` to
// `return true, token, nil, nil` -> this test fails.
func TestValidateToken_DenyUnknownAndEmpty(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	valid, _, _, err := orch.ValidateToken(ctx, "does-not-exist")
	require.NoError(t, err)
	assert.False(t, valid)

	valid, _, _, err = orch.ValidateToken(ctx, "")
	assert.Error(t, err)
	assert.False(t, valid)
}

// --- IsRevokedSerial -------------------------------------------------------

// TestIsRevokedSerial tracks the serial-revocation set transition on revoke.
// Mutation: drop `o.revokedSerials[cert.Serial] = true` in RevokeCertificate
// -> revoked serial no longer reported -> this test fails.
func TestIsRevokedSerial(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "n", TTL: time.Hour})
	require.NoError(t, err)

	assert.False(t, orch.IsRevokedSerial(cert.Serial))
	require.NoError(t, orch.RevokeCertificate(ctx, cert.ID))
	assert.True(t, orch.IsRevokedSerial(cert.Serial))
	assert.False(t, orch.IsRevokedSerial("some-other-serial"))
}

// --- Vault fallback behavior ----------------------------------------------

// TestIssueCertificate_VaultErrorFallsBackToLocal asserts that when Vault PKI
// returns an error the orchestrator still issues a usable local certificate
// (real PEM material), rather than failing.
// Mutation: change the Vault block condition `if err == nil && pkiCert != nil`
// to `if true` (and use pkiCert blindly) would panic; the honest mutation is
// removing the `// Fall through` local issuance after the vault block so the
// method returns the vault error -> this test's NoError fails.
func TestIssueCertificate_VaultErrorFallsBackToLocal(t *testing.T) {
	ctx := context.Background()
	mock := &security.MockVaultClient{
		IssueFn: func(_ context.Context, _, _ string, _ security.PKIIssueParams) (*security.PKICertificate, error) {
			return nil, fmt.Errorf("vault boom")
		},
	}
	orch := NewOrchestrator(security.NewVaultWrapper(mock))

	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "fallback", TTL: time.Hour})
	require.NoError(t, err)
	require.NotNil(t, cert)
	// Local issuance produced real PEM material, not the empty vault result.
	assert.Contains(t, string(cert.CertPEM), "CERTIFICATE")
	assert.Contains(t, string(cert.KeyPEM), "PRIVATE KEY")
	assert.NotEqual(t, "", cert.Serial)
}

// --- EnforcePolicy edge behavior ------------------------------------------

// TestEnforcePolicy_UnknownRoleSkipped asserts that a role referenced by a
// policy but not loaded is skipped (denied), while a second, loaded role in
// the same policy can still grant access.
// Mutation: change the `role, ok := p.roles[roleName]; if !ok { continue }`
// to not continue (e.g. treat missing role as allow) -> first assertion fails.
func TestEnforcePolicy_UnknownRoleSkipped(t *testing.T) {
	ctx := context.Background()
	enforcer := NewPolicyEnforcer()

	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "reader",
		Permissions: []Permission{{Action: "read", Resource: "logs"}},
	}))

	// Policy references a missing role plus a real one.
	policy := &Policy{Name: "mix", Subject: "u", Roles: []string{"ghost", "reader"}}

	// ghost role is unknown; only reader grants. read/logs allowed.
	assert.NoError(t, enforcer.EnforcePolicy(ctx, policy, "logs", "read"))
	// nothing grants write on logs.
	assert.Error(t, enforcer.EnforcePolicy(ctx, policy, "logs", "write"))

	// A policy that references ONLY an unknown role grants nothing.
	ghostOnly := &Policy{Name: "g", Subject: "u", Roles: []string{"ghost"}}
	assert.Error(t, enforcer.EnforcePolicy(ctx, ghostOnly, "logs", "read"))
}

// --- COMPLETION: hierarchical resource wildcard ---------------------------

// TestMatchResource_HierarchicalWildcard exercises the newly added "prefix/*"
// wildcard semantics through the public EnforcePolicy path.
// Mutation: delete the `strings.HasSuffix(pattern, "/*")` branch in
// matchResource -> "secrets/*" no longer matches "secrets/db" -> fails.
func TestMatchResource_HierarchicalWildcard(t *testing.T) {
	ctx := context.Background()
	enforcer := NewPolicyEnforcer()

	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "secret-reader",
		Permissions: []Permission{{Action: "read", Resource: "secrets/*"}},
	}))
	policy := &Policy{Name: "p", Subject: "u", Roles: []string{"secret-reader"}}

	// Matches children of secrets/.
	assert.NoError(t, enforcer.EnforcePolicy(ctx, policy, "secrets/db", "read"))
	assert.NoError(t, enforcer.EnforcePolicy(ctx, policy, "secrets/db/creds", "read"))

	// Does NOT match the bare prefix without slash, nor sibling prefixes.
	assert.Error(t, enforcer.EnforcePolicy(ctx, policy, "secrets", "read"))
	assert.Error(t, enforcer.EnforcePolicy(ctx, policy, "secretstore/db", "read"))

	// Action still enforced under wildcard resource.
	assert.Error(t, enforcer.EnforcePolicy(ctx, policy, "secrets/db", "write"))
}

// TestMatchResource_ExactStillExact guards against the wildcard change
// loosening exact matching.
// Mutation: change `return pattern == resource` to `return true` in
// matchResource -> this test's deny assertion fails.
func TestMatchResource_ExactStillExact(t *testing.T) {
	ctx := context.Background()
	enforcer := NewPolicyEnforcer()
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name:        "exact",
		Permissions: []Permission{{Action: "read", Resource: "logs"}},
	}))
	policy := &Policy{Name: "p", Subject: "u", Roles: []string{"exact"}}

	assert.NoError(t, enforcer.EnforcePolicy(ctx, policy, "logs", "read"))
	assert.Error(t, enforcer.EnforcePolicy(ctx, policy, "logsX", "read"))
	assert.Error(t, enforcer.EnforcePolicy(ctx, policy, "metrics", "read"))
}

// --- COMPLETION: RotateCredentials now revokes expired material -----------

// TestRotateCredentials_RevokesExpired asserts the rotation sweep performs a
// real state change: an expired certificate is revoked, its serial denied,
// and the returned active count excludes it; fresh certs remain active.
// Mutation: in RotateCredentials remove the `cert.Revoked = true` /
// revokedSerials update inside the expired branch -> expired cert stays
// usable and IsRevokedSerial stays false -> this test fails.
func TestRotateCredentials_RevokesExpired(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	fresh, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "fresh", TTL: time.Hour})
	require.NoError(t, err)
	stale, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "stale", TTL: time.Hour})
	require.NoError(t, err)

	// Force the stale cert past expiry.
	storedStale, ok := orch.GetCertificate(stale.ID)
	require.True(t, ok)
	storedStale.ExpiresAt = time.Now().Add(-time.Minute)

	active, err := orch.RotateCredentials(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, active, "only the fresh cert should remain active")

	// Sink-side proof: expired cert is now revoked + serial denied.
	storedStale, ok = orch.GetCertificate(stale.ID)
	require.True(t, ok)
	assert.True(t, storedStale.Revoked)
	assert.NotNil(t, storedStale.RevokedAt)
	assert.True(t, orch.IsRevokedSerial(storedStale.Serial))

	// And the expired cert can no longer be used as a valid token.
	valid, _, _, err := orch.ValidateToken(ctx, stale.ID)
	require.NoError(t, err)
	assert.False(t, valid)

	// Fresh cert is untouched and still validates.
	valid, _, _, err = orch.ValidateToken(ctx, fresh.ID)
	require.NoError(t, err)
	assert.True(t, valid)
}

// --- gRPC server allow/deny end-to-end ------------------------------------

// TestGRPCServer_AuthorizeDeniesInvalidToken asserts the server denies
// authorization for a token that does not correspond to any issued cert.
// Mutation: in Authorize change the `if err != nil || !valid` branch to
// proceed regardless -> an invalid token would be authorized -> fails.
func TestGRPCServer_AuthorizeDeniesInvalidToken(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()
	require.NoError(t, enforcer.LoadRole(ctx, &Role{Name: "admin", Permissions: []Permission{{Action: "*", Resource: "*"}}}))
	srv := NewGRPCServer(orch, enforcer)

	resp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{Token: "bogus", Resource: "x", Action: "read"})
	require.NoError(t, err)
	assert.False(t, resp.Allowed)
	assert.Equal(t, "invalid token", resp.Reason)
}

// TestGRPCServer_AuthorizeDeniesWhenPolicyMissing asserts that a VALID token
// is still denied when the enforcer has no granting role/policy loaded — i.e.
// authentication alone does not imply authorization.
// Mutation: in Authorize ignore the EnforcePolicy error and always return
// Allowed:true -> this test fails.
func TestGRPCServer_AuthorizeDeniesWhenPolicyMissing(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer() // no roles loaded -> admin role absent
	srv := NewGRPCServer(orch, enforcer)

	auth, err := srv.Authenticate(ctx, &helixv1.AuthenticateRequest{Identity: "node-z"})
	require.NoError(t, err)
	require.True(t, auth.Success)

	resp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{Token: auth.Token, Resource: "secrets", Action: "delete"})
	require.NoError(t, err)
	assert.False(t, resp.Allowed)
	assert.NotEmpty(t, resp.Reason)
}

// TestGRPCServer_AuthenticateDeniesUntrustedDomain asserts that an explicit
// SPIFFE identity in a foreign trust domain is rejected (no token issued).
// Mutation: in ValidateIdentity drop the trust-domain check -> Authenticate
// would succeed for foreign domains -> this test fails.
func TestGRPCServer_AuthenticateDeniesUntrustedDomain(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()
	srv := NewGRPCServer(orch, enforcer)

	resp, err := srv.Authenticate(ctx, &helixv1.AuthenticateRequest{Identity: "spiffe://evil.example/node/x"})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Empty(t, resp.Token)
}

// --- GetRole / GetPolicy miss ---------------------------------------------

// TestGetRoleAndPolicy_Miss asserts lookups for absent names report not-found.
// Mutation: change `r, ok := p.roles[name]; return r, ok` to `return r, true`
// -> miss reported as found -> this test fails.
func TestGetRoleAndPolicy_Miss(t *testing.T) {
	enforcer := NewPolicyEnforcer()
	_, ok := enforcer.GetRole("nope")
	assert.False(t, ok)
	_, ok = enforcer.GetPolicy("nope")
	assert.False(t, ok)
}
