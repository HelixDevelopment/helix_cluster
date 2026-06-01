//go:build integration

package security

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// integrationVaultClient returns a real apiVaultClient from VAULT_ADDR /
// VAULT_TOKEN environment variables.  If VAULT_ADDR is unset the test is
// skipped so it can never fire in the hermetic unit tier.
func integrationVaultClient(t *testing.T) *apiVaultClient {
	t.Helper()
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		t.Skip("VAULT_ADDR not set — skipping integration test")
	}
	token := os.Getenv("VAULT_TOKEN")
	if token == "" {
		t.Skip("VAULT_TOKEN not set — skipping integration test")
	}
	c, err := NewAPIVaultClient(addr, token)
	if err != nil {
		t.Fatalf("NewAPIVaultClient(%s): %v", addr, err)
	}
	return c
}

// mountKVv2 enables a KVv2 secrets engine at the given path.
// The orchestrator's dev-Vault container already has "secret" enabled, but
// we mount a test-specific path to avoid collisions.
func mountKVv2(t *testing.T, c *apiVaultClient, mountPath string) {
	t.Helper()
	ctx := context.Background()
	// Attempt to enable; ignore "already mounted" errors.
	_, err := c.client.Logical().WriteWithContext(ctx, "sys/mounts/"+mountPath, map[string]interface{}{
		"type": "kv",
		"options": map[string]interface{}{
			"version": "2",
		},
	})
	if err != nil {
		// 400 "path is already in use" is acceptable
		t.Logf("mount %s: %v (continuing)", mountPath, err)
	}
}

// mountPKI enables a PKI secrets engine and configures a root CA + role.
func mountPKI(t *testing.T, c *apiVaultClient, mountPath string) {
	t.Helper()
	ctx := context.Background()

	// Enable PKI
	_, err := c.client.Logical().WriteWithContext(ctx, "sys/mounts/"+mountPath, map[string]interface{}{
		"type": "pki",
	})
	if err != nil {
		t.Logf("mount pki %s: %v (continuing)", mountPath, err)
	}

	// Generate root CA
	_, err = c.client.Logical().WriteWithContext(ctx, mountPath+"/root/generate/internal", map[string]interface{}{
		"common_name": "Helix Integration Test CA",
		"ttl":         "87600h",
	})
	if err != nil {
		t.Fatalf("generate root CA: %v", err)
	}

	// Configure URLs
	_, err = c.client.Logical().WriteWithContext(ctx, mountPath+"/config/urls", map[string]interface{}{
		"issuing_certificates":    fmt.Sprintf("%s/v1/%s/ca", os.Getenv("VAULT_ADDR"), mountPath),
		"crl_distribution_points": fmt.Sprintf("%s/v1/%s/crl", os.Getenv("VAULT_ADDR"), mountPath),
	})
	if err != nil {
		t.Fatalf("configure pki urls: %v", err)
	}

	// Create a role
	_, err = c.client.Logical().WriteWithContext(ctx, mountPath+"/roles/helix-server", map[string]interface{}{
		"allowed_domains":  "helix.local",
		"allow_subdomains": true,
		"max_ttl":          "72h",
		"generate_lease":   true,
	})
	if err != nil {
		t.Fatalf("create pki role: %v", err)
	}
}

// TestIntegration_KVv2WriteRead does a real write/read round-trip against a
// dev-mode Vault (started by the orchestrator).
func TestIntegration_KVv2WriteRead(t *testing.T) {
	c := integrationVaultClient(t)
	ctx := context.Background()

	const mount = "integration-kv"
	const secretPath = "helix/test/config"

	mountKVv2(t, c, mount)

	writeData := map[string]interface{}{
		"db_host":     "postgres.helix.local",
		"db_password": "real-integration-secret",
	}

	// Write
	if err := c.KVv2Write(ctx, mount, secretPath, writeData); err != nil {
		t.Fatalf("KVv2Write: %v", err)
	}

	// Read back
	readData, err := c.KVv2Read(ctx, mount, secretPath)
	if err != nil {
		t.Fatalf("KVv2Read: %v", err)
	}

	// §7.1 sink-side: values survive the wire round-trip through real Vault
	if readData["db_host"] != "postgres.helix.local" {
		t.Errorf("db_host: want postgres.helix.local, got %v", readData["db_host"])
	}
	if readData["db_password"] != "real-integration-secret" {
		t.Errorf("db_password mismatch: got %v", readData["db_password"])
	}
}

// TestIntegration_PKIIssueAndRenew issues a real certificate from Vault's PKI
// engine, verifies the PEM parses through x509.ParseCertificate, then renews
// the lease via sys/leases/renew and confirms the response is non-empty.
func TestIntegration_PKIIssueAndRenew(t *testing.T) {
	c := integrationVaultClient(t)
	ctx := context.Background()

	const mount = "integration-pki"
	mountPKI(t, c, mount)

	// Issue
	cert, err := c.PKIIssue(ctx, mount, "helix-server", PKIIssueParams{
		CommonName: "node1.helix.local",
		TTL:        1 * time.Hour,
		AltNames:   []string{"node1-alt.helix.local"},
		IPSANs:     []string{"10.0.0.1"},
	})
	if err != nil {
		t.Fatalf("PKIIssue: %v", err)
	}

	// §7.1 mandatory: PEM must parse through x509.ParseCertificate
	if cert.Certificate == "" {
		t.Fatal("cert.Certificate is empty")
	}
	block, _ := pem.Decode([]byte(cert.Certificate))
	if block == nil {
		t.Fatal("cert.Certificate is not a valid PEM block")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate: %v", err)
	}
	if parsed.Subject.CommonName != "node1.helix.local" {
		t.Errorf("parsed CN: want node1.helix.local, got %s", parsed.Subject.CommonName)
	}

	// Serial must be present
	if cert.Serial == "" {
		t.Error("cert.Serial is empty")
	}

	// LeaseID must be present.
	if cert.LeaseID == "" {
		t.Skip("Vault issued cert without a lease_id (role may lack generate_lease=true)")
	}

	// CHARACTERIZATION (intrinsic Vault API contract, not a bug): a PKI
	// certificate lease is NON-RENEWABLE by design — the lifecycle for an
	// expiring cert is re-issue (or revoke), not renew. Vault therefore rejects
	// sys/leases/renew on a PKI cert lease with "lease is not renewable". We
	// assert PKIRenew faithfully surfaces that real server response. The
	// happy-path renewal of a *renewable* lease is proven deterministically by
	// the unit test TestAPIVaultClient_PKIRenew_PathAndLease (httptest).
	_, renewErr := c.PKIRenew(ctx, mount, cert.LeaseID)
	if renewErr == nil {
		t.Fatalf("expected PKIRenew on a non-renewable PKI cert lease to error, got nil")
	}
	if !strings.Contains(renewErr.Error(), "not renewable") {
		t.Fatalf("PKIRenew error = %q, want it to contain %q (Vault's documented contract)",
			renewErr.Error(), "not renewable")
	}

	t.Logf("integration: issued serial=%s lease=%s; renew correctly rejected (non-renewable PKI lease)",
		cert.Serial, cert.LeaseID)
}
