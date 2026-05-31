package integration

import (
	"context"
	"testing"
	"time"

	internalsecurity "github.com/HelixDevelopment/helix_cluster/internal/security"
	"github.com/HelixDevelopment/helix_cluster/pkg/security"
	"github.com/stretchr/testify/suite"
)

type SecurityPolicySuite struct {
	IntegrationSuite
}

func (s *SecurityPolicySuite) TestIssueCertificateValidateIdentityCheckPolicyAllowsAction() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	// Create orchestrator with a mock Vault client.
	mockVault := &security.MockVaultClient{
		IssueFn: func(_ context.Context, mount, role string, params security.PKIIssueParams) (*security.PKICertificate, error) {
			return &security.PKICertificate{
				Certificate:   "mock-cert-pem",
				PrivateKey:    "mock-key-pem",
				Serial:        "mock-serial-001",
				LeaseID:       "lease-001",
				LeaseDuration: time.Hour,
			}, nil
		},
	}
	vaultWrapper := security.NewVaultWrapper(mockVault)
	orch := internalsecurity.NewOrchestrator(vaultWrapper)

	// Issue certificate.
	cert, err := orch.IssueCertificate(ctx, internalsecurity.IssueCertificateRequest{
		NodeID:     "node-1",
		CommonName: "node-1.helix.local",
		TTL:        time.Hour,
	})
	s.Require().NoError(err)
	s.NotNil(cert)
	s.NotEmpty(cert.ID)
	s.False(cert.Revoked)

	// Validate SPIFFE identity.
	spiffeID := "spiffe://helix.local/node/node-1"
	err = orch.ValidateIdentity(ctx, spiffeID)
	s.Require().NoError(err)

	// Check policy allows action using PolicyEnforcer.
	enforcer := internalsecurity.NewPolicyEnforcer()
	err = enforcer.LoadRole(ctx, &internalsecurity.Role{
		Name: "node-role",
		Permissions: []internalsecurity.Permission{
			{Action: "read", Resource: "cluster/nodes"},
		},
	})
	s.Require().NoError(err)

	policy := &internalsecurity.Policy{
		Name:    "node-1-policy",
		Subject: cert.NodeID,
		Roles:   []string{"node-role"},
	}
	err = enforcer.LoadPolicy(ctx, policy)
	s.Require().NoError(err)

	err = enforcer.EnforcePolicy(ctx, policy, "cluster/nodes", "read")
	s.Require().NoError(err)
}

func (s *SecurityPolicySuite) TestRevokeCertificateSubsequentValidationFails() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	mockVault := &security.MockVaultClient{
		IssueFn: func(_ context.Context, mount, role string, params security.PKIIssueParams) (*security.PKICertificate, error) {
			return &security.PKICertificate{
				Certificate:   "mock-cert-pem",
				PrivateKey:    "mock-key-pem",
				Serial:        "mock-serial-002",
				LeaseID:       "lease-002",
				LeaseDuration: time.Hour,
			}, nil
		},
	}
	vaultWrapper := security.NewVaultWrapper(mockVault)
	orch := internalsecurity.NewOrchestrator(vaultWrapper)

	// Issue certificate.
	cert, err := orch.IssueCertificate(ctx, internalsecurity.IssueCertificateRequest{
		NodeID:     "node-2",
		CommonName: "node-2.helix.local",
		TTL:        time.Hour,
	})
	s.Require().NoError(err)
	s.NotNil(cert)

	// Verify serial is not revoked.
	s.False(orch.IsRevokedSerial(cert.Serial))

	// Revoke certificate.
	err = orch.RevokeCertificate(ctx, cert.ID)
	s.Require().NoError(err)

	// Verify serial is now revoked.
	s.True(orch.IsRevokedSerial(cert.Serial))

	// Retrieve certificate and confirm revoked flag.
	revokedCert, ok := orch.GetCertificate(cert.ID)
	s.Require().True(ok)
	s.True(revokedCert.Revoked)
	s.NotNil(revokedCert.RevokedAt)
}

func TestSecurityPolicySuite(t *testing.T) {
	suite.Run(t, new(SecurityPolicySuite))
}
