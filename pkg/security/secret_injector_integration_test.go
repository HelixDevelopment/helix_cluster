//go:build integration

package security

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
)

// integrationInjectorClient returns a real *apiVaultClient for integration
// tests.  Skips only when VAULT_ADDR / VAULT_TOKEN are genuinely absent — the
// orchestrator always sets them to http://127.0.0.1:8200 / roottoken.
func integrationInjectorClient(t *testing.T) *apiVaultClient {
	t.Helper()
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		t.Skip("VAULT_ADDR not set — skipping inject+rotate integration test")
	}
	token := os.Getenv("VAULT_TOKEN")
	if token == "" {
		t.Skip("VAULT_TOKEN not set — skipping inject+rotate integration test")
	}
	c, err := NewAPIVaultClient(addr, token)
	if err != nil {
		t.Fatalf("NewAPIVaultClient(%s): %v", addr, err)
	}
	return c
}

// ensureKVv2Mount enables a KV v2 secrets engine at mountPath, ignoring
// "already mounted" errors so the test is idempotent across runs.
func ensureKVv2Mount(t *testing.T, c *apiVaultClient, mountPath string) {
	t.Helper()
	ctx := context.Background()
	_, err := c.client.Logical().WriteWithContext(ctx, "sys/mounts/"+mountPath, map[string]interface{}{
		"type": "kv",
		"options": map[string]interface{}{
			"version": "2",
		},
	})
	if err != nil {
		t.Logf("ensureKVv2Mount %s: %v (continuing — may already be mounted)", mountPath, err)
	}
}

// TestIntegration_InjectAndRotate is the primary HXC-1134 integration proof.
//
// It:
//  1. Writes a per-run UUID secret (v1) via the VaultKV seam.
//  2. Creates a SecretInjector and calls Inject — asserts the value+version.
//  3. Rotates to a second per-run UUID (v2) and asserts:
//     - holder.Value() == new UUID  (credential updated WITHOUT restart)
//     - holder.Version() == 2       (KV v2 version incremented)
//  4. Hard-FAILs on any mismatch (t.Skip is never used after connection
//     succeeds).
func TestIntegration_InjectAndRotate(t *testing.T) {
	c := integrationInjectorClient(t)
	ctx := context.Background()

	const mount = "secret"
	ensureKVv2Mount(t, c, mount)

	// Per-run UUIDs guarantee no cross-test collision and prove each run is
	// exercising live Vault I/O, not a cached or static value.
	runID := uuid.New().String()
	secretPath := fmt.Sprintf("helix/integration/injector/%s", runID)

	credV1 := "cred-v1-" + uuid.New().String()
	credV2 := "cred-v2-" + uuid.New().String()

	kv := NewAPIVaultKV(c)

	// ── Step 1: write the initial credential (creates version 1) ──────────
	v1Written, err := kv.KVPut(ctx, mount, secretPath, credV1)
	if err != nil {
		t.Fatalf("KVPut v1: %v", err)
	}
	if v1Written != 1 {
		t.Fatalf("expected KVPut to return version 1, got %d", v1Written)
	}

	// ── Step 2: Inject — holder must reflect v1 exactly ───────────────────
	inj := NewSecretInjector(kv, mount, secretPath)
	if err := inj.Inject(ctx); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	if got := inj.Get().Value(); got != credV1 {
		t.Fatalf("after Inject: Value want %q, got %q", credV1, got)
	}
	if got := inj.Get().Version(); got != 1 {
		t.Fatalf("after Inject: Version want 1, got %d", got)
	}
	t.Logf("inject OK: value=%q version=%d", inj.Get().Value(), inj.Get().Version())

	// ── Step 3: Rotate to v2 — holder picks up new cred WITHOUT restart ───
	if err := inj.Rotate(ctx, credV2); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// §7.1 hard-FAIL: new credential must be live in the holder immediately.
	if got := inj.Get().Value(); got != credV2 {
		t.Fatalf("after Rotate: Value want %q, got %q (stale value — rotation did not refresh holder)",
			credV2, got)
	}
	// Version MUST have incremented from 1 to 2.
	if got := inj.Get().Version(); got != 2 {
		t.Fatalf("after Rotate: Version want 2, got %d (KV v2 version did not bump)", got)
	}
	t.Logf("rotate OK: value=%q version=%d", inj.Get().Value(), inj.Get().Version())

	// ── Step 4: Independent re-read from Vault confirms the sink state ─────
	// We bypass the injector entirely and read via a fresh KVGet to confirm
	// Vault's stored value matches what the holder reports.
	sinkVal, sinkVer, err := kv.KVGet(ctx, mount, secretPath)
	if err != nil {
		t.Fatalf("independent KVGet for sink verification: %v", err)
	}
	if sinkVal != credV2 {
		t.Fatalf("sink KVGet value: want %q, got %q", credV2, sinkVal)
	}
	if sinkVer != 2 {
		t.Fatalf("sink KVGet version: want 2, got %d", sinkVer)
	}
	t.Logf("sink verification OK: vault has value=%q version=%d", sinkVal, sinkVer)
}
