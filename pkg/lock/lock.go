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
// If ctx is cancelled while waiting, Lock returns ctx.Err() immediately and
// guarantees no goroutine remains permanently blocked: the internal acquisition
// goroutine is signalled via an "abandoned" channel so that — whether it has
// already acquired the per-key mutex or is still waiting to acquire it — it
// releases the mutex rather than leaving an orphan owner.
//
// Design: "locked" is UNBUFFERED. This eliminates the send-race that plagues
// buffered designs:
//   - The goroutine's `locked <- struct{}{}` only completes if the outer select
//     is simultaneously reading from it (i.e. the caller takes ownership).
//   - If the outer select instead picks ctx.Done(), the goroutine is still
//     blocked on the `locked <- struct{}{}` send (or on lm.Lock()). Closing
//     "abandoned" makes the goroutine's inner select choose <-abandoned and
//     call lm.Unlock() — no orphan, no leak.
func (m *MemoryLocker) Lock(ctx context.Context, key string) (UnlockFunc, error) {
	m.mu.Lock()
	lm, ok := m.locks[key]
	if !ok {
		lm = &sync.Mutex{}
		m.locks[key] = lm
	}
	m.mu.Unlock()

	// locked is UNBUFFERED: the goroutine's send completes only when the caller
	// receives, which means ownership transfer is atomic with the outer select.
	// abandoned is closed exactly once by the cancellation path to tell the
	// goroutine to release the mutex it just acquired.
	locked := make(chan struct{})
	abandoned := make(chan struct{})

	go func() {
		lm.Lock()
		// lm is now held by this goroutine. Decide who owns it:
		//   • If the outer select is waiting on <-locked, the send completes and
		//     the caller owns the mutex.
		//   • If abandoned is already closed (ctx cancelled before we acquired),
		//     nobody wants the lock — release it immediately.
		select {
		case locked <- struct{}{}:
			// Caller took ownership; it will call lm.Unlock() via UnlockFunc.
		case <-abandoned:
			// Caller is gone; release so future waiters are not permanently blocked.
			lm.Unlock()
		}
	}()

	select {
	case <-locked:
		// Fire-once release. A bare lm.Unlock() is unguarded: calling the
		// returned UnlockFunc twice triggers Go's *fatal* "sync: unlock of
		// unlocked mutex" (a process-killing runtime error that recover() cannot
		// catch), and — worse — a stale/duplicate release after the key's mutex
		// has been re-acquired by a DIFFERENT owner would free that other owner's
		// lock, silently breaking mutual exclusion. Because sync.Mutex carries no
		// owner identity, the only safe contract is that each UnlockFunc releases
		// AT MOST ONCE. sync.Once makes the second+ call a no-op rather than a
		// cross-owner free or a fatal crash.
		var once sync.Once
		return func() error {
			once.Do(func() { lm.Unlock() })
			return nil
		}, nil
	case <-ctx.Done():
		ctxErr := ctx.Err()
		// Signal the goroutine to release the mutex when it eventually acquires it.
		// Because locked is unbuffered, the goroutine cannot have completed its
		// send without the outer select receiving it — so we know the goroutine
		// has NOT transferred ownership to us, and closing abandoned is safe.
		close(abandoned)
		return nil, ctxErr
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
