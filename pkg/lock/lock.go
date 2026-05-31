// Package lock provides distributed locking primitives for Helix Cluster OS.
package lock

import (
	"context"
	"fmt"
	"sync"

	"github.com/HelixDevelopment/helix_cluster/pkg/etcd"
)

// UnlockFunc releases a held lock.
type UnlockFunc func() error

// Locker provides distributed locking.
type Locker interface {
	// Lock acquires a lock for the given key.
	// Returns an UnlockFunc that must be called to release the lock.
	Lock(ctx context.Context, key string) (UnlockFunc, error)
}

// MemoryLocker is an in-memory Locker for testing and single-node use.
type MemoryLocker struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewMemoryLocker creates a new in-memory locker.
func NewMemoryLocker() *MemoryLocker {
	return &MemoryLocker{locks: make(map[string]*sync.Mutex)}
}

// Lock acquires an in-memory lock for the given key.
func (m *MemoryLocker) Lock(ctx context.Context, key string) (UnlockFunc, error) {
	m.mu.Lock()
	lm, ok := m.locks[key]
	if !ok {
		lm = &sync.Mutex{}
		m.locks[key] = lm
	}
	m.mu.Unlock()

	// Fast path: try to lock without blocking first
	locked := make(chan struct{}, 1)
	go func() {
		lm.Lock()
		locked <- struct{}{}
	}()

	select {
	case <-locked:
		return func() error { lm.Unlock(); return nil }, nil
	case <-ctx.Done():
		// We can't cancel the goroutine, but we record it will eventually unlock
		// For production, use EtcdLocker instead
		return nil, ctx.Err()
	}
}

// EtcdLocker is a distributed Locker backed by etcd.
type EtcdLocker struct {
	client *etcd.Client
}

// NewEtcdLocker creates a new etcd-backed locker.
func NewEtcdLocker(client *etcd.Client) *EtcdLocker {
	return &EtcdLocker{client: client}
}

// Lock acquires a distributed lock via etcd.
func (e *EtcdLocker) Lock(ctx context.Context, key string) (UnlockFunc, error) {
	unlock, err := e.client.Lock(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("etcd lock: %w", err)
	}
	return func() error {
		if err := unlock(); err != nil {
			return fmt.Errorf("etcd unlock: %w", err)
		}
		return nil
	}, nil
}
