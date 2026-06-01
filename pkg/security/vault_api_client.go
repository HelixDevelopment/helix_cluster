package security

import (
	"context"
	"fmt"
	"strings"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
)

// apiVaultClient is the production VaultClient implementation that delegates
// to the official HashiCorp Vault API client.
type apiVaultClient struct {
	client *vaultapi.Client
}

// NewAPIVaultClient constructs a production apiVaultClient pointed at the given
// Vault address and authenticated with the given token.
func NewAPIVaultClient(addr, token string) (*apiVaultClient, error) {
	cfg := vaultapi.DefaultConfig()
	cfg.Address = addr

	c, err := vaultapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create vault api client: %w", err)
	}
	c.SetToken(token)
	return &apiVaultClient{client: c}, nil
}

// KVv2Read reads a secret from a KVv2 mount at the given path.
// It returns the secret's data map, or an error if the secret is not found
// or the read fails.
func (a *apiVaultClient) KVv2Read(ctx context.Context, mount, path string) (map[string]interface{}, error) {
	kvSecret, err := a.client.KVv2(mount).Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("kvv2 read %s/%s: %w", mount, path, err)
	}
	if kvSecret == nil || kvSecret.Data == nil {
		return nil, fmt.Errorf("kvv2 read %s/%s: secret not found or deleted", mount, path)
	}
	return kvSecret.Data, nil
}

// KVv2Write writes data to a KVv2 mount at the given path.
func (a *apiVaultClient) KVv2Write(ctx context.Context, mount, path string, data map[string]interface{}) error {
	_, err := a.client.KVv2(mount).Put(ctx, path, data)
	if err != nil {
		return fmt.Errorf("kvv2 write %s/%s: %w", mount, path, err)
	}
	return nil
}

// PKIIssue requests a new certificate from a PKI secrets-engine mount.
// It writes to <mount>/issue/<role> and maps the response fields to
// PKICertificate.
func (a *apiVaultClient) PKIIssue(ctx context.Context, mount, role string, params PKIIssueParams) (*PKICertificate, error) {
	path := mount + "/issue/" + role

	data := map[string]interface{}{
		"common_name": params.CommonName,
	}
	if params.TTL > 0 {
		data["ttl"] = params.TTL.String()
	}
	if len(params.AltNames) > 0 {
		data["alt_names"] = strings.Join(params.AltNames, ",")
	}
	if len(params.IPSANs) > 0 {
		data["ip_sans"] = strings.Join(params.IPSANs, ",")
	}

	secret, err := a.client.Logical().WriteWithContext(ctx, path, data)
	if err != nil {
		return nil, fmt.Errorf("pki issue %s: %w", path, err)
	}
	if secret == nil || secret.Data == nil {
		return nil, fmt.Errorf("pki issue %s: empty response", path)
	}

	return pkiCertFromSecret(secret)
}

// PKIRenew renews an existing PKI lease via sys/leases/renew.
// It maps the renewed lease data back to PKICertificate.
func (a *apiVaultClient) PKIRenew(ctx context.Context, mount string, leaseID string) (*PKICertificate, error) {
	data := map[string]interface{}{
		"lease_id": leaseID,
	}

	secret, err := a.client.Logical().WriteWithContext(ctx, "sys/leases/renew", data)
	if err != nil {
		return nil, fmt.Errorf("pki renew lease %s: %w", leaseID, err)
	}
	if secret == nil {
		return nil, fmt.Errorf("pki renew lease %s: empty response", leaseID)
	}

	cert := &PKICertificate{
		LeaseID:       secret.LeaseID,
		LeaseDuration: time.Duration(secret.LeaseDuration) * time.Second,
	}
	return cert, nil
}

// pkiCertFromSecret maps a Vault *Secret from a PKI issue call into a
// PKICertificate. It returns an error if mandatory fields are missing.
func pkiCertFromSecret(secret *vaultapi.Secret) (*PKICertificate, error) {
	cert := &PKICertificate{
		LeaseID:       secret.LeaseID,
		LeaseDuration: time.Duration(secret.LeaseDuration) * time.Second,
	}

	if v, ok := secret.Data["certificate"].(string); ok {
		cert.Certificate = v
	} else {
		return nil, fmt.Errorf("pki response missing 'certificate' field")
	}

	if v, ok := secret.Data["private_key"].(string); ok {
		cert.PrivateKey = v
	}

	if v, ok := secret.Data["serial_number"].(string); ok {
		cert.Serial = v
	}

	if v, ok := secret.Data["ca_chain"].([]interface{}); ok {
		for _, item := range v {
			if s, ok := item.(string); ok {
				cert.CAChain = append(cert.CAChain, s)
			}
		}
	}

	return cert, nil
}
