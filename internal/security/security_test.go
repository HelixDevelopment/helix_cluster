package security

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/pkg/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrchestrator_IssueCertificate(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	req := IssueCertificateRequest{
		NodeID:     "node-1",
		CommonName: "node-1.helix.local",
		TTL:        time.Hour,
	}

	cert, err := orch.IssueCertificate(ctx, req)
	require.NoError(t, err)
	assert.NotEmpty(t, cert.ID)
	assert.Equal(t, "node-1", cert.NodeID)
	assert.NotEmpty(t, cert.Serial)
	assert.False(t, cert.Revoked)
	assert.True(t, strings.Contains(string(cert.CertPEM), "CERTIFICATE"))
	assert.True(t, strings.Contains(string(cert.KeyPEM), "PRIVATE KEY"))

	// Retrieved certificate should match.
	stored, ok := orch.GetCertificate(cert.ID)
	require.True(t, ok)
	assert.Equal(t, cert.ID, stored.ID)
}

func TestOrchestrator_RevokeCertificate(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "node-2"})
	require.NoError(t, err)

	err = orch.RevokeCertificate(ctx, cert.ID)
	require.NoError(t, err)

	stored, ok := orch.GetCertificate(cert.ID)
	require.True(t, ok)
	assert.True(t, stored.Revoked)
	assert.NotNil(t, stored.RevokedAt)
	assert.True(t, orch.IsRevokedSerial(stored.Serial))

	// Double revoke should error.
	err = orch.RevokeCertificate(ctx, cert.ID)
	assert.Error(t, err)

	// Revoke unknown certificate should error.
	err = orch.RevokeCertificate(ctx, "unknown")
	assert.Error(t, err)
}

func TestOrchestrator_ValidateIdentity(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	tests := []struct {
		name      string
		spiffeID  string
		wantError bool
	}{
		{"valid identity", "spiffe://helix.local/node/node-1", false},
		{"wrong trust domain", "spiffe://other.local/node/node-1", true},
		{"missing path", "spiffe://helix.local", true},
		{"empty id", "", true},
		{"invalid scheme", "http://helix.local/node", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := orch.ValidateIdentity(ctx, tt.spiffeID)
			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOrchestrator_RotateCredentials(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)

	// Issue a few certificates.
	for i := 0; i < 3; i++ {
		_, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "node"})
		require.NoError(t, err)
	}

	count, err := orch.RotateCredentials(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestOrchestrator_VaultIntegration(t *testing.T) {
	ctx := context.Background()
	mock := &security.MockVaultClient{
		IssueFn: func(_ context.Context, mount, role string, params security.PKIIssueParams) (*security.PKICertificate, error) {
			return &security.PKICertificate{
				Certificate:   "CERT",
				PrivateKey:    "KEY",
				Serial:        "vault-serial-1",
				LeaseDuration: time.Hour,
			}, nil
		},
	}
	vault := security.NewVaultWrapper(mock)
	orch := NewOrchestrator(vault)

	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "vault-node"})
	require.NoError(t, err)
	assert.Equal(t, "vault-serial-1", cert.Serial)
	assert.Equal(t, []byte("CERT"), cert.CertPEM)
}

func TestPolicyEnforcer_AllowDeny(t *testing.T) {
	ctx := context.Background()
	enforcer := NewPolicyEnforcer()

	// Load roles.
	adminRole := &Role{
		Name: "admin",
		Permissions: []Permission{
			{Action: "*", Resource: "*"},
		},
	}
	readerRole := &Role{
		Name: "reader",
		Permissions: []Permission{
			{Action: "read", Resource: "logs"},
			{Action: "read", Resource: "metrics"},
		},
	}
	require.NoError(t, enforcer.LoadRole(ctx, adminRole))
	require.NoError(t, enforcer.LoadRole(ctx, readerRole))

	// Load policies.
	adminPolicy := &Policy{Name: "admin-policy", Subject: "alice", Roles: []string{"admin"}}
	readerPolicy := &Policy{Name: "reader-policy", Subject: "bob", Roles: []string{"reader"}}
	require.NoError(t, enforcer.LoadPolicy(ctx, adminPolicy))
	require.NoError(t, enforcer.LoadPolicy(ctx, readerPolicy))

	// Admin can do anything.
	err := enforcer.EnforcePolicy(ctx, adminPolicy, "secrets", "delete")
	assert.NoError(t, err)

	// Reader can read logs.
	err = enforcer.EnforcePolicy(ctx, readerPolicy, "logs", "read")
	assert.NoError(t, err)

	// Reader cannot delete logs.
	err = enforcer.EnforcePolicy(ctx, readerPolicy, "logs", "delete")
	assert.Error(t, err)

	// Reader cannot read unknown resource.
	err = enforcer.EnforcePolicy(ctx, readerPolicy, "secrets", "read")
	assert.Error(t, err)

	// Nil policy is denied.
	err = enforcer.EnforcePolicy(ctx, nil, "logs", "read")
	assert.Error(t, err)
}

func TestPolicyEnforcer_ListPolicies(t *testing.T) {
	ctx := context.Background()
	enforcer := NewPolicyEnforcer()

	p1 := &Policy{Name: "p1", Subject: "u1", Roles: []string{"r1"}}
	p2 := &Policy{Name: "p2", Subject: "u2", Roles: []string{"r2"}}
	require.NoError(t, enforcer.LoadPolicy(ctx, p1))
	require.NoError(t, enforcer.LoadPolicy(ctx, p2))

	policies := enforcer.ListPolicies()
	assert.Len(t, policies, 2)
}

func TestPolicyEnforcer_LoadPolicyValidation(t *testing.T) {
	ctx := context.Background()
	enforcer := NewPolicyEnforcer()

	assert.Error(t, enforcer.LoadPolicy(ctx, nil))
	assert.Error(t, enforcer.LoadPolicy(ctx, &Policy{Name: ""}))
}

func TestConcurrentOperations(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()

	// Pre-load a role and policy for concurrent checks.
	role := &Role{Name: "worker", Permissions: []Permission{{Action: "read", Resource: "*"}}}
	require.NoError(t, enforcer.LoadRole(ctx, role))
	policy := &Policy{Name: "worker-policy", Subject: "worker", Roles: []string{"worker"}}
	require.NoError(t, enforcer.LoadPolicy(ctx, policy))

	var wg sync.WaitGroup
	workers := 50
	iterations := 20

	// Concurrent certificate issuance.
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "node"})
				require.NoError(t, err)
			}
		}(i)
	}

	// Concurrent policy enforcement.
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = enforcer.EnforcePolicy(ctx, policy, "data", "read")
			}
		}(i)
	}

	// Concurrent listing.
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = enforcer.ListPolicies()
			}
		}(i)
	}

	wg.Wait()
}

func TestGRPCServer_CheckPolicy(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()

	role := &Role{Name: "admin", Permissions: []Permission{{Action: "*", Resource: "*"}}}
	require.NoError(t, enforcer.LoadRole(ctx, role))
	policy := &Policy{Name: "admin-policy", Subject: "alice", Roles: []string{"admin"}}
	require.NoError(t, enforcer.LoadPolicy(ctx, policy))

	server := NewGRPCServer(orch, enforcer)

	// We reuse existing proto messages for the gRPC server tests.
	// IssueCert uses IssueTokenRequest / IssueTokenResponse.
	// RevokeCert uses ValidateTokenRequest / AuthorizeResponse.
	// ValidateIdentity uses AuthenticateRequest / AuthenticateResponse.
	// RotateCreds uses ValidateTokenRequest / IssueTokenResponse.
	// CheckPolicy uses AuthorizeRequest / AuthorizeResponse.

	// Test IssueCert.
	issueResp, err := server.IssueCert(ctx, &helixv1.IssueTokenRequest{Identity: "node-x"})
	require.NoError(t, err)
	assert.NotEmpty(t, issueResp.Token)
	assert.Greater(t, issueResp.ExpiresAt, time.Now().Unix())

	// Test RevokeCert.
	revokeResp, err := server.RevokeCert(ctx, &helixv1.ValidateTokenRequest{Token: issueResp.Token})
	require.NoError(t, err)
	assert.True(t, revokeResp.Allowed)

	// Test ValidateIdentity.
	idResp, err := server.ValidateIdentity(ctx, &helixv1.AuthenticateRequest{Identity: "spiffe://helix.local/node/node-x"})
	require.NoError(t, err)
	assert.True(t, idResp.Success)

	// Test RotateCreds.
	rotResp, err := server.RotateCreds(ctx, &helixv1.ValidateTokenRequest{})
	require.NoError(t, err)
	assert.NotEmpty(t, rotResp.Token)

	// Test CheckPolicy (allow).
	checkResp, err := server.CheckPolicy(ctx, &helixv1.AuthorizeRequest{
		Token:    "admin-policy",
		Resource: "secrets",
		Action:   "delete",
	})
	require.NoError(t, err)
	assert.True(t, checkResp.Allowed)

	// Test CheckPolicy (deny - unknown policy).
	checkResp, err = server.CheckPolicy(ctx, &helixv1.AuthorizeRequest{
		Token:    "unknown-policy",
		Resource: "secrets",
		Action:   "delete",
	})
	require.NoError(t, err)
	assert.False(t, checkResp.Allowed)
}
