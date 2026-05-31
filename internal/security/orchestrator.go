// Package security provides the security orchestration service for Helix Cluster OS.
package security

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/security"
)

// Certificate represents an issued TLS certificate.
type Certificate struct {
	ID          string
	NodeID      string
	CertPEM     []byte
	KeyPEM      []byte
	Serial      string
	IssuedAt    time.Time
	ExpiresAt   time.Time
	Revoked     bool
	RevokedAt   *time.Time
}

// IssueCertificateRequest holds parameters for certificate issuance.
type IssueCertificateRequest struct {
	NodeID     string
	CommonName string
	TTL        time.Duration
}

// Orchestrator manages TLS certificate lifecycle, SPIFFE identities,
// Vault integration, and credential rotation for the cluster.
type Orchestrator struct {
	mu            sync.RWMutex
	certs         map[string]*Certificate // keyed by cert ID
	revokedSerials map[string]bool
	vault         *security.VaultWrapper
	spiffeTrustDomain string
}

// NewOrchestrator creates a new security Orchestrator.
func NewOrchestrator(vault *security.VaultWrapper) *Orchestrator {
	return &Orchestrator{
		certs:          make(map[string]*Certificate),
		revokedSerials: make(map[string]bool),
		vault:          vault,
		spiffeTrustDomain: "helix.local",
	}
}

// IssueCertificate issues a new TLS certificate for a cluster node.
// If a Vault client is configured it delegates to Vault PKI; otherwise
// it generates a self-signed stub certificate for testing.
func (o *Orchestrator) IssueCertificate(ctx context.Context, req IssueCertificateRequest) (*Certificate, error) {
	if req.NodeID == "" {
		return nil, fmt.Errorf("node ID is required")
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	// Attempt Vault issuance when available.
	if o.vault != nil {
		params := security.PKIIssueParams{
			CommonName: req.CommonName,
			TTL:        req.TTL,
		}
		pkiCert, err := o.vault.IssueCertificate(ctx, "pki", "helix-node", params)
		if err == nil && pkiCert != nil {
			cert := &Certificate{
				ID:        fmt.Sprintf("cert-%s-%d", req.NodeID, time.Now().UnixNano()),
				NodeID:    req.NodeID,
				CertPEM:   []byte(pkiCert.Certificate),
				KeyPEM:    []byte(pkiCert.PrivateKey),
				Serial:    pkiCert.Serial,
				IssuedAt:  time.Now(),
				ExpiresAt: time.Now().Add(pkiCert.LeaseDuration),
			}
			o.certs[cert.ID] = cert
			return cert, nil
		}
		// Fall through to local issuance on Vault error.
	}

	// Local stub issuance.
	cert, err := o.issueLocal(req)
	if err != nil {
		return nil, err
	}
	o.certs[cert.ID] = cert
	return cert, nil
}

func (o *Orchestrator) issueLocal(req IssueCertificateRequest) (*Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	ttl := req.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName: req.CommonName,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(ttl),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	cert := &Certificate{
		ID:        fmt.Sprintf("cert-%s-%d", req.NodeID, time.Now().UnixNano()),
		NodeID:    req.NodeID,
		CertPEM:   certPEM,
		KeyPEM:    keyPEM,
		Serial:    template.SerialNumber.String(),
		IssuedAt:  time.Now(),
		ExpiresAt: template.NotAfter,
	}
	return cert, nil
}

// RevokeCertificate revokes a certificate by its ID.
func (o *Orchestrator) RevokeCertificate(_ context.Context, certID string) error {
	if certID == "" {
		return fmt.Errorf("certificate ID is required")
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	cert, ok := o.certs[certID]
	if !ok {
		return fmt.Errorf("certificate %q not found", certID)
	}
	if cert.Revoked {
		return fmt.Errorf("certificate %q already revoked", certID)
	}

	now := time.Now()
	cert.Revoked = true
	cert.RevokedAt = &now
	o.revokedSerials[cert.Serial] = true
	return nil
}

// ValidateIdentity validates a SPIFFE identity string.
func (o *Orchestrator) ValidateIdentity(_ context.Context, spiffeID string) error {
	if spiffeID == "" {
		return fmt.Errorf("SPIFFE ID is required")
	}
	parsed, err := security.ParseSPIFFEID(spiffeID)
	if err != nil {
		return fmt.Errorf("parse SPIFFE ID: %w", err)
	}
	if err := parsed.Validate(); err != nil {
		return fmt.Errorf("invalid SPIFFE ID: %w", err)
	}
	if parsed.TrustDomain != o.spiffeTrustDomain {
		return fmt.Errorf("untrusted domain %q", parsed.TrustDomain)
	}
	return nil
}

// RotateCredentials triggers a credential rotation cycle. Any certificate
// whose validity window has elapsed is revoked (its serial added to the
// revocation set) so that expired material can no longer be presented as a
// valid token. It returns the count of certificates that remain active
// (issued, not revoked, and not yet expired) after the sweep.
func (o *Orchestrator) RotateCredentials(_ context.Context) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now()
	active := 0
	for _, cert := range o.certs {
		if cert.Revoked {
			continue
		}
		if now.After(cert.ExpiresAt) {
			// Expired credential: revoke it as part of rotation.
			revokedAt := now
			cert.Revoked = true
			cert.RevokedAt = &revokedAt
			o.revokedSerials[cert.Serial] = true
			continue
		}
		active++
	}
	return active, nil
}

// GetCertificate retrieves a certificate by ID (for testing).
func (o *Orchestrator) GetCertificate(certID string) (*Certificate, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	c, ok := o.certs[certID]
	return c, ok
}

// IsRevokedSerial checks whether a serial number has been revoked.
func (o *Orchestrator) IsRevokedSerial(serial string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.revokedSerials[serial]
}

// ValidateToken checks whether a token (certificate ID) is valid and returns identity info.
func (o *Orchestrator) ValidateToken(_ context.Context, token string) (bool, string, []string, error) {
	if token == "" {
		return false, "", nil, fmt.Errorf("token is required")
	}

	o.mu.RLock()
	defer o.mu.RUnlock()

	cert, ok := o.certs[token]
	if !ok {
		return false, "", nil, nil
	}
	if cert.Revoked {
		return false, "", nil, nil
	}
	if time.Now().After(cert.ExpiresAt) {
		return false, "", nil, nil
	}

	scopes := []string{"read", "write"}
	return true, cert.NodeID, scopes, nil
}
