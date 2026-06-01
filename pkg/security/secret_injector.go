package security

import (
	"context"
	"fmt"
	"sync"
)

// VaultKV is the seam interface used by SecretInjector to read and write KV v2
// secrets.  Production wraps apiVaultClient; tests inject a fakeKV.
type VaultKV interface {
	// KVGet reads the secret at mount/path and returns the plaintext value and
	// the KV v2 version number.
	KVGet(ctx context.Context, mount, path string) (value string, version int, error error)

	// KVPut writes value to mount/path and returns the new version number that
	// Vault assigned to this write.
	KVPut(ctx context.Context, mount, path, value string) (newVersion int, error error)
}

// SecretHolder carries the currently-injected secret value and its KV v2
// version.  It is populated by Inject and updated in-place by Rotate.
type SecretHolder struct {
	mu      sync.RWMutex
	value   string
	version int
}

// Value returns the current secret value.  Zero-value ("") means Inject has
// not been called yet.
func (h *SecretHolder) Value() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.value
}

// Version returns the KV v2 version of the currently-held secret.
func (h *SecretHolder) Version() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.version
}

func (h *SecretHolder) set(value string, version int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.value = value
	h.version = version
}

// SecretInjector reads a service credential from Vault KV v2, exposes it via
// a SecretHolder, and supports hot rotation: Rotate writes a new version and
// immediately refreshes the holder so callers obtain the new credential on the
// next Get() — without a process restart.
type SecretInjector struct {
	kv     VaultKV
	mount  string
	path   string
	holder *SecretHolder
}

// NewSecretInjector creates a SecretInjector for the secret at mount/path.
// Call Inject once at start-up to populate the holder, then call Rotate
// whenever the credential must be cycled.
func NewSecretInjector(kv VaultKV, mount, path string) *SecretInjector {
	return &SecretInjector{
		kv:     kv,
		mount:  mount,
		path:   path,
		holder: &SecretHolder{},
	}
}

// Inject fetches the current secret from Vault and stores it in the holder.
// Subsequent calls to Get() return the value fetched here until Rotate is
// called.
func (i *SecretInjector) Inject(ctx context.Context) error {
	val, ver, err := i.kv.KVGet(ctx, i.mount, i.path)
	if err != nil {
		return fmt.Errorf("inject %s/%s: %w", i.mount, i.path, err)
	}
	i.holder.set(val, ver)
	return nil
}

// Get returns the most recently fetched secret value.  It never contacts
// Vault; use Inject or Rotate to refresh.
func (i *SecretInjector) Get() *SecretHolder {
	return i.holder
}

// Rotate writes newValue to Vault, then immediately re-fetches the secret so
// the holder reflects the new version.  After Rotate returns without error,
// Get().Value() == newValue and Get().Version() is the incremented KV v2
// version (v_old + 1).
func (i *SecretInjector) Rotate(ctx context.Context, newValue string) error {
	if _, err := i.kv.KVPut(ctx, i.mount, i.path, newValue); err != nil {
		return fmt.Errorf("rotate write %s/%s: %w", i.mount, i.path, err)
	}
	// Re-fetch to pick up the authoritative value+version from Vault (the
	// source of truth).  This is the "refresh" that allows the holder to serve
	// the new credential without a process restart.
	if err := i.Inject(ctx); err != nil {
		return fmt.Errorf("rotate refresh %s/%s: %w", i.mount, i.path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// apiVaultKV — production VaultKV adapter wrapping apiVaultClient
// ---------------------------------------------------------------------------

// apiVaultKV adapts the existing apiVaultClient to the VaultKV seam so that
// SecretInjector can work against a real Vault server in production and in the
// integration test tier.
type apiVaultKV struct {
	c *apiVaultClient
}

// NewAPIVaultKV wraps an apiVaultClient as a VaultKV seam.
func NewAPIVaultKV(c *apiVaultClient) VaultKV {
	return &apiVaultKV{c: c}
}

// KVGet reads mount/path and extracts the "value" field.  It also returns the
// KV v2 metadata version.  The convention used in Helix is that secrets store
// a single string field under the key "value".
func (a *apiVaultKV) KVGet(ctx context.Context, mount, path string) (string, int, error) {
	secret, err := a.c.client.KVv2(mount).Get(ctx, path)
	if err != nil {
		return "", 0, fmt.Errorf("kvv2 get %s/%s: %w", mount, path, err)
	}
	if secret == nil || secret.Data == nil {
		return "", 0, fmt.Errorf("kvv2 get %s/%s: secret not found", mount, path)
	}

	val, ok := secret.Data["value"].(string)
	if !ok {
		return "", 0, fmt.Errorf("kvv2 get %s/%s: 'value' field missing or not a string", mount, path)
	}

	var version int
	if secret.VersionMetadata != nil {
		version = secret.VersionMetadata.Version
	}

	return val, version, nil
}

// KVPut writes {"value": value} to mount/path and returns the new version.
func (a *apiVaultKV) KVPut(ctx context.Context, mount, path, value string) (int, error) {
	kvSecret, err := a.c.client.KVv2(mount).Put(ctx, path, map[string]interface{}{
		"value": value,
	})
	if err != nil {
		return 0, fmt.Errorf("kvv2 put %s/%s: %w", mount, path, err)
	}
	if kvSecret == nil || kvSecret.VersionMetadata == nil {
		return 0, nil
	}
	return kvSecret.VersionMetadata.Version, nil
}
