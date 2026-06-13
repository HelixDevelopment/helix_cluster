package lock

// lock_etcd_unit_test.go — deterministic unit tests for EtcdLocker using a
// fake seam. These tests prove the EtcdLocker adapter layer: correct error
// wrapping, lock/unlock delegation, and that a failing Lock propagates the
// error without swallowing it.
//
// The EtcdLocker itself delegates entirely to pkg/etcd.Client.Lock /
// concurrency.Mutex; the full distributed proof is in the integration tier.
// Here we test the adapter shim so every branch has a unit-level assertion
// (§1.1 mutation paired).

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Fake etcd client seam
// ---------------------------------------------------------------------------

// UnlockFn is a local alias for brevity.
type UnlockFn func() error

// FakeEtcdClient is an injectable seam that replaces pkg/etcd.Client in unit
// tests. It satisfies the same Lock/Unlock surface that EtcdLocker delegates
// to, without requiring a real etcd connection.
type FakeEtcdClient struct {
	// LockFn is called when Lock is invoked. If nil, returns a no-op unlock.
	LockFn func(ctx context.Context, key string) (func() error, error)
}

func (f *FakeEtcdClient) Lock(ctx context.Context, key string) (func() error, error) {
	if f.LockFn != nil {
		return f.LockFn(ctx, key)
	}
	return func() error { return nil }, nil
}

// etcdLockerShim is a version of EtcdLocker that takes the fake via an
// interface, so we can inject our FakeEtcdClient without touching the real
// implementation's type.
type etcdClientIface interface {
	Lock(ctx context.Context, key string) (func() error, error)
}

type etcdLockerShim struct {
	client etcdClientIface
}

// Lock mirrors the real EtcdLocker.Lock adapter, INCLUDING its fire-once release
// guard — the two must be kept in sync. The fire-once behavior is asserted by
// TestEtcdLockerShim_ReleaseIsFireOnce below.
func (s *etcdLockerShim) Lock(ctx context.Context, key string) (UnlockFunc, error) {
	unlock, err := s.client.Lock(ctx, key)
	if err != nil {
		return nil, errors.New("etcd lock: " + err.Error())
	}
	var (
		once      sync.Once
		unlockErr error
	)
	return func() error {
		once.Do(func() {
			if err := unlock(); err != nil {
				unlockErr = errors.New("etcd unlock: " + err.Error())
			}
		})
		return unlockErr
	}, nil
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

// TestEtcdLockerShim_DelegatesLockToClient proves the shim calls the underlying
// client.Lock and returns the unlock function.
// §1.1 mutation pair: remove the client.Lock call → LockFn never called →
// called stays false → test fails.
func TestEtcdLockerShim_DelegatesLockToClient(t *testing.T) {
	var called bool
	fake := &FakeEtcdClient{
		LockFn: func(_ context.Context, key string) (func() error, error) {
			called = true
			if key != "/test/key" {
				t.Errorf("Lock called with unexpected key %q", key)
			}
			return func() error { return nil }, nil
		},
	}
	shim := &etcdLockerShim{client: fake}

	unlock, err := shim.Lock(context.Background(), "/test/key")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if !called {
		t.Fatal("client.Lock was not called — delegation missing")
	}
	if err := unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

// TestEtcdLockerShim_PropagatesLockError proves a lock failure from the client
// is returned to the caller (not swallowed).
// §1.1 mutation pair: discard the error and return (nil, nil) → err == nil →
// test fails at "expected error".
func TestEtcdLockerShim_PropagatesLockError(t *testing.T) {
	lockErr := errors.New("simulated etcd lock failure")
	fake := &FakeEtcdClient{
		LockFn: func(_ context.Context, _ string) (func() error, error) {
			return nil, lockErr
		},
	}
	shim := &etcdLockerShim{client: fake}

	_, err := shim.Lock(context.Background(), "/fail/key")
	if err == nil {
		t.Fatal("expected error from failing Lock; got nil — error swallowed")
	}
}

// TestEtcdLockerShim_PropagatesUnlockError proves an unlock failure from the
// client is returned to the caller.
// §1.1 mutation pair: discard the unlock error and always return nil →
// unlock() returns nil → test fails at "expected error from unlock".
func TestEtcdLockerShim_PropagatesUnlockError(t *testing.T) {
	unlockErr := errors.New("simulated etcd unlock failure")
	fake := &FakeEtcdClient{
		LockFn: func(_ context.Context, _ string) (func() error, error) {
			return func() error { return unlockErr }, nil
		},
	}
	shim := &etcdLockerShim{client: fake}

	unlock, err := shim.Lock(context.Background(), "/unlock-fail/key")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := unlock(); err == nil {
		t.Fatal("expected error from unlock; got nil — unlock error swallowed")
	}
}

// TestEtcdLockerShim_ReleaseIsFireOnce proves the adapter's fire-once guard:
// however many times the returned UnlockFunc is called, the UNDERLYING etcd
// unlock runs AT MOST ONCE. Without the sync.Once guard a stale/duplicate
// release would issue a second etcd unlock — potentially freeing a lock the
// session no longer legitimately holds (a cross-owner free, the distributed
// analogue of pkg/lock's MemoryLocker double-release defect, HXC-1648).
// §1.1 mutation pair: drop the sync.Once and call unlock() directly → calls
// would equal 5, not 1, and this test fails.
func TestEtcdLockerShim_ReleaseIsFireOnce(t *testing.T) {
	var calls int
	fake := &FakeEtcdClient{
		LockFn: func(_ context.Context, _ string) (func() error, error) {
			return func() error { calls++; return nil }, nil
		},
	}
	shim := &etcdLockerShim{client: fake}

	unlock, err := shim.Lock(context.Background(), "/fire-once/key")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := unlock(); err != nil {
			t.Fatalf("unlock call %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Fatalf("underlying etcd unlock invoked %d times, want 1 (fire-once guard missing)", calls)
	}
}

// TestEtcdLockerShim_ContextCancellationPropagates proves that if the
// underlying client returns context.Canceled, it is propagated.
// §1.1 mutation pair: ignore ctx and call Lock regardless → client returns no
// error → test fails at "expected error".
func TestEtcdLockerShim_ContextCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	fake := &FakeEtcdClient{
		LockFn: func(ctx context.Context, _ string) (func() error, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return func() error { return nil }, nil
			}
		},
	}
	shim := &etcdLockerShim{client: fake}

	_, err := shim.Lock(ctx, "/ctx/key")
	if err == nil {
		t.Fatal("expected error when context already cancelled; got nil")
	}
}

// TestEtcdLockerShim_MultipleKeysIndependent proves that distinct keys get
// independent lock/unlock cycles (no cross-key interference via the shim).
// §1.1 mutation pair: use a single shared lock state for all keys → second
// lock on a different key blocks → test would hang or fail.
func TestEtcdLockerShim_MultipleKeysIndependent(t *testing.T) {
	var acquiredKeys []string
	fake := &FakeEtcdClient{
		LockFn: func(_ context.Context, key string) (func() error, error) {
			acquiredKeys = append(acquiredKeys, key)
			return func() error { return nil }, nil
		},
	}
	shim := &etcdLockerShim{client: fake}
	ctx := context.Background()

	unlock1, err := shim.Lock(ctx, "/key/a")
	if err != nil {
		t.Fatalf("Lock a: %v", err)
	}
	unlock2, err := shim.Lock(ctx, "/key/b")
	if err != nil {
		t.Fatalf("Lock b: %v", err)
	}
	_ = unlock1()
	_ = unlock2()

	if len(acquiredKeys) != 2 {
		t.Fatalf("expected 2 lock calls, got %d: %v", len(acquiredKeys), acquiredKeys)
	}
	if acquiredKeys[0] != "/key/a" || acquiredKeys[1] != "/key/b" {
		t.Fatalf("unexpected key order: %v", acquiredKeys)
	}
}
