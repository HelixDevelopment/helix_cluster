package security

import (
	"context"
	"fmt"
	"time"
)

// VaultClient is a minimal wrapper interface for HashiCorp Vault operations.
// Production implementations delegate to the official Vault client.
type VaultClient interface {
	// KVv2Read reads a secret from a KVv2 mount at the given path.
	KVv2Read(ctx context.Context, mount, path string) (map[string]interface{}, error)
	// KVv2Write writes data to a KVv2 mount at the given path.
	KVv2Write(ctx context.Context, mount, path string, data map[string]interface{}) error
	// PKIIssue requests a new certificate from a PKI mount.
	PKIIssue(ctx context.Context, mount, role string, params PKIIssueParams) (*PKICertificate, error)
	// PKIRenew renews a PKI certificate.
	PKIRenew(ctx context.Context, mount string, leaseID string) (*PKICertificate, error)
}

// PKIIssueParams holds parameters for PKI certificate issuance.
type PKIIssueParams struct {
	CommonName string
	TTL        time.Duration
	AltNames   []string
	IPSANs     []string
}

// PKICertificate holds the result of a PKI issue/renew operation.
type PKICertificate struct {
	Certificate string
	PrivateKey  string
	CAChain     []string
	Serial      string
	LeaseID     string
	LeaseDuration time.Duration
}

// VaultWrapper wraps a VaultClient with convenience methods.
type VaultWrapper struct {
	client VaultClient
}

// NewVaultWrapper creates a new Vault wrapper.
func NewVaultWrapper(client VaultClient) *VaultWrapper {
	return &VaultWrapper{client: client}
}

// ReadSecret reads a KVv2 secret.
func (w *VaultWrapper) ReadSecret(ctx context.Context, mount, path string) (map[string]interface{}, error) {
	if w.client == nil {
		return nil, fmt.Errorf("vault client not initialized")
	}
	return w.client.KVv2Read(ctx, mount, path)
}

// WriteSecret writes a KVv2 secret.
func (w *VaultWrapper) WriteSecret(ctx context.Context, mount, path string, data map[string]interface{}) error {
	if w.client == nil {
		return fmt.Errorf("vault client not initialized")
	}
	return w.client.KVv2Write(ctx, mount, path, data)
}

// IssueCertificate issues a new PKI certificate.
func (w *VaultWrapper) IssueCertificate(ctx context.Context, mount, role string, params PKIIssueParams) (*PKICertificate, error) {
	if w.client == nil {
		return nil, fmt.Errorf("vault client not initialized")
	}
	return w.client.PKIIssue(ctx, mount, role, params)
}

// RenewCertificate renews a PKI certificate by lease ID.
func (w *VaultWrapper) RenewCertificate(ctx context.Context, mount, leaseID string) (*PKICertificate, error) {
	if w.client == nil {
		return nil, fmt.Errorf("vault client not initialized")
	}
	return w.client.PKIRenew(ctx, mount, leaseID)
}

// MockVaultClient is a test double that records calls and returns canned responses.
type MockVaultClient struct {
	ReadFn   func(ctx context.Context, mount, path string) (map[string]interface{}, error)
	WriteFn  func(ctx context.Context, mount, path string, data map[string]interface{}) error
	IssueFn  func(ctx context.Context, mount, role string, params PKIIssueParams) (*PKICertificate, error)
	RenewFn  func(ctx context.Context, mount, leaseID string) (*PKICertificate, error)
}

func (m *MockVaultClient) KVv2Read(ctx context.Context, mount, path string) (map[string]interface{}, error) {
	if m.ReadFn != nil {
		return m.ReadFn(ctx, mount, path)
	}
	return nil, fmt.Errorf("mock ReadFn not set")
}

func (m *MockVaultClient) KVv2Write(ctx context.Context, mount, path string, data map[string]interface{}) error {
	if m.WriteFn != nil {
		return m.WriteFn(ctx, mount, path, data)
	}
	return fmt.Errorf("mock WriteFn not set")
}

func (m *MockVaultClient) PKIIssue(ctx context.Context, mount, role string, params PKIIssueParams) (*PKICertificate, error) {
	if m.IssueFn != nil {
		return m.IssueFn(ctx, mount, role, params)
	}
	return nil, fmt.Errorf("mock IssueFn not set")
}

func (m *MockVaultClient) PKIRenew(ctx context.Context, mount, leaseID string) (*PKICertificate, error) {
	if m.RenewFn != nil {
		return m.RenewFn(ctx, mount, leaseID)
	}
	return nil, fmt.Errorf("mock RenewFn not set")
}
