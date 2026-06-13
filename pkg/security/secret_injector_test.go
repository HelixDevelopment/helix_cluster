package security

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// fakeKV — deterministic in-memory VaultKV seam for unit tests
// ---------------------------------------------------------------------------

// fakeKVEntry holds the versioned history of a single secret path.
type fakeKVEntry struct {
	versions []string // versions[0] == version 1, etc.
}

// fakeKV is an in-process VaultKV implementation used by unit tests only.
// It is goroutine-safe and tracks version numbers faithfully.
type fakeKV struct {
	mu     sync.Mutex
	store  map[string]*fakeKVEntry // key: "mount/path"
	getErr error                   // if non-nil, KVGet always returns this
	putErr error                   // if non-nil, KVPut always returns this
}

func newFakeKV() *fakeKV {
	return &fakeKV{store: make(map[string]*fakeKVEntry)}
}

func (f *fakeKV) key(mount, path string) string {
	return mount + "/" + path
}

// Seed pre-populates a secret so tests can Inject without first writing.
func (f *fakeKV) Seed(mount, path, value string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(mount, path)
	e, ok := f.store[k]
	if !ok {
		e = &fakeKVEntry{}
		f.store[k] = e
	}
	e.versions = append(e.versions, value)
	return len(e.versions) // new version number
}

func (f *fakeKV) KVGet(_ context.Context, mount, path string) (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return "", 0, f.getErr
	}
	k := f.key(mount, path)
	e, ok := f.store[k]
	if !ok || len(e.versions) == 0 {
		return "", 0, fmt.Errorf("fakeKV: %s/%s not found", mount, path)
	}
	latest := len(e.versions)
	return e.versions[latest-1], latest, nil
}

func (f *fakeKV) KVPut(_ context.Context, mount, path, value string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return 0, f.putErr
	}
	k := f.key(mount, path)
	e, ok := f.store[k]
	if !ok {
		e = &fakeKVEntry{}
		f.store[k] = e
	}
	e.versions = append(e.versions, value)
	return len(e.versions), nil
}

// ---------------------------------------------------------------------------
// NewSecretInjector — construction
// ---------------------------------------------------------------------------

func TestNewSecretInjector_NonNil(t *testing.T) {
	inj := NewSecretInjector(newFakeKV(), "secret", "helix/svc")
	if inj == nil {
		t.Fatal("expected non-nil SecretInjector")
	}
	if inj.Get() == nil {
		t.Fatal("expected non-nil holder from Get()")
	}
}

// ---------------------------------------------------------------------------
// Inject — reads current version and populates the holder
// ---------------------------------------------------------------------------

func TestSecretInjector_Inject_ExactValueAndVersion(t *testing.T) {
	kv := newFakeKV()
	const (
		mount = "secret"
		path  = "helix/gateway"
		want  = "initial-credential-v1"
	)
	v := kv.Seed(mount, path, want)
	if v != 1 {
		t.Fatalf("Seed: expected version 1, got %d", v)
	}

	inj := NewSecretInjector(kv, mount, path)
	if err := inj.Inject(context.Background()); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	// §7.1 sink-side: exact value AND exact version must be stored.
	if got := inj.Get().Value(); got != want {
		t.Errorf("Value: want %q, got %q", want, got)
	}
	if got := inj.Get().Version(); got != 1 {
		t.Errorf("Version: want 1, got %d", got)
	}
}

// Inject must fail when KVGet returns an error.
func TestSecretInjector_Inject_GetError(t *testing.T) {
	kv := newFakeKV()
	kv.getErr = errors.New("vault: permission denied")

	inj := NewSecretInjector(kv, "secret", "helix/svc")
	err := inj.Inject(context.Background())
	if err == nil {
		t.Fatal("expected error when KVGet fails, got nil")
	}
	if inj.Get().Value() != "" {
		t.Errorf("holder must stay empty on Inject error, got %q", inj.Get().Value())
	}
}

// Inject must fail when the secret does not exist.
func TestSecretInjector_Inject_SecretNotFound(t *testing.T) {
	kv := newFakeKV() // no seed — path does not exist

	inj := NewSecretInjector(kv, "secret", "helix/missing")
	if err := inj.Inject(context.Background()); err == nil {
		t.Fatal("expected error for missing secret, got nil")
	}
}

// ---------------------------------------------------------------------------
// Rotate — writes new version, refresh picks it up; version bumps
// ---------------------------------------------------------------------------

func TestSecretInjector_Rotate_NewValueAndVersionBump(t *testing.T) {
	kv := newFakeKV()
	const (
		mount   = "secret"
		path    = "helix/scheduler"
		initial = "cred-version-one"
		rotated = "cred-version-two"
	)
	kv.Seed(mount, path, initial)

	inj := NewSecretInjector(kv, mount, path)
	if err := inj.Inject(context.Background()); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	// Capture state BEFORE rotation.
	valueBefore := inj.Get().Value()
	versionBefore := inj.Get().Version()

	if valueBefore != initial {
		t.Fatalf("pre-rotate value: want %q, got %q", initial, valueBefore)
	}
	if versionBefore != 1 {
		t.Fatalf("pre-rotate version: want 1, got %d", versionBefore)
	}

	// Perform rotation.
	if err := inj.Rotate(context.Background(), rotated); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// §7.1 sink-side: EXACT new value AND version incremented after Rotate.
	if got := inj.Get().Value(); got != rotated {
		t.Errorf("post-rotate Value: want %q, got %q", rotated, got)
	}
	if got := inj.Get().Version(); got != versionBefore+1 {
		t.Errorf("post-rotate Version: want %d, got %d", versionBefore+1, got)
	}
}

// Rotate must fail (and not update the holder) when KVPut fails.
func TestSecretInjector_Rotate_PutError_HolderUnchanged(t *testing.T) {
	kv := newFakeKV()
	const (
		mount   = "secret"
		path    = "helix/pki"
		initial = "original-cred"
	)
	kv.Seed(mount, path, initial)

	inj := NewSecretInjector(kv, mount, path)
	if err := inj.Inject(context.Background()); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	// Arm the fault AFTER the successful Inject.
	kv.putErr = errors.New("vault: rate limited")

	err := inj.Rotate(context.Background(), "new-value")
	if err == nil {
		t.Fatal("expected Rotate error when KVPut fails, got nil")
	}

	// Holder must still hold the original value — Rotate must be atomic from
	// the caller's perspective.
	if got := inj.Get().Value(); got != initial {
		t.Errorf("holder value must be unchanged on Rotate failure: want %q, got %q", initial, got)
	}
	if got := inj.Get().Version(); got != 1 {
		t.Errorf("holder version must be unchanged on Rotate failure: want 1, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// §1.1 Mutation target: skipping the Inject call inside Rotate must make
// TestSecretInjector_Rotate_NewValueAndVersionBump fail with "stale value".
// (We do NOT ship the mutant; this comment documents the experiment and is
// verified in the mutation_proof section of the StructuredOutput.)
// ---------------------------------------------------------------------------

// TestSecretInjector_Rotate_MultipleRotations verifies that multiple successive
// rotations each produce the correct value+version.
func TestSecretInjector_Rotate_MultipleRotations(t *testing.T) {
	kv := newFakeKV()
	kv.Seed("sec", "svc/token", "v1")

	inj := NewSecretInjector(kv, "sec", "svc/token")
	if err := inj.Inject(context.Background()); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	for i, want := range []string{"v2", "v3", "v4"} {
		if err := inj.Rotate(context.Background(), want); err != nil {
			t.Fatalf("Rotate[%d]: %v", i, err)
		}
		if got := inj.Get().Value(); got != want {
			t.Errorf("Rotate[%d] Value: want %q, got %q", i, want, got)
		}
		// Version starts at 1 (seed), then each Rotate bumps by 1.
		wantVer := i + 2 // seed=1, first rotate=2, second=3, ...
		if got := inj.Get().Version(); got != wantVer {
			t.Errorf("Rotate[%d] Version: want %d, got %d", i, wantVer, got)
		}
	}
}

// ---------------------------------------------------------------------------
// SecretHolder — concurrent read safety
// ---------------------------------------------------------------------------

func TestSecretHolder_ConcurrentReads(t *testing.T) {
	kv := newFakeKV()
	kv.Seed("s", "p", "initial")

	inj := NewSecretInjector(kv, "s", "p")
	if err := inj.Inject(context.Background()); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = inj.Get().Value()
			_ = inj.Get().Version()
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// apiVaultKV compile-time interface check
// ---------------------------------------------------------------------------

var _ VaultKV = (*apiVaultKV)(nil)
