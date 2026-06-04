package swim

// concurrency_regression_test.go — deterministic regression guards for confirmed
// concurrency defects in pkg/swim:
//
//   F1 protocol.Stop()/Leave() double-teardown ("close of closed channel" panic +
//      racy transport.Close()) — fixed with sync.Once-guarded teardown.
//   F2 HealthyMembers reading Member.State without the member's own lock — race
//      with UpdateState/ClearSuspect — fixed by reading via m.IsHealthy().
//   F4 FailureDetector.Confirm/Refute invoking the protocol callback while still
//      holding fd.mu (fd.mu->p.mu lock-order hazard) — fixed by releasing fd.mu
//      before invoking the callback.
//
// These reproduce deterministically (F1, F4) or under `go test -race` (F2).

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStop_Idempotent_DoubleStop proves Stop() can be called twice without the
// "close of closed channel" panic. Before the sync.Once guard, the second
// close(p.shutdown) panicked.
func TestStop_Idempotent_DoubleStop(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:        "alpha",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  30 * time.Second,
		GossipInterval: 30 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, p.Start())

	require.NoError(t, p.Stop())
	// Second Stop MUST be a no-op, not a panic.
	require.NotPanics(t, func() {
		require.NoError(t, p.Stop())
	}, "double Stop() must not panic (close of closed channel)")
}

// TestLeaveThenStop_NoPanic proves Leave() (which itself calls Stop()) followed
// by an explicit/deferred Stop() does not double-close the shutdown channel —
// the common defer p.Stop() + p.Leave() pattern.
func TestLeaveThenStop_NoPanic(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:        "alpha",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  30 * time.Second,
		GossipInterval: 30 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, p.Start())

	require.NoError(t, p.Leave())
	require.NotPanics(t, func() {
		require.NoError(t, p.Stop())
	}, "Stop() after Leave() must not panic")
}

// TestStop_Concurrent proves concurrent Stop() callers are race- and panic-free.
// Run under -race this also covers the racy double transport.Close().
func TestStop_Concurrent(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:        "alpha",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  30 * time.Second,
		GossipInterval: 30 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, p.Start())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.Stop()
		}()
	}
	wg.Wait()
}

// TestHealthyMembers_RaceWithStateMutation drives HealthyMembers() concurrently
// with UpdateState/ClearSuspect on the same members. Under `go test -race` the
// pre-fix code (reading m.State under only p.mu) reports a data race on
// Member.State; the fix (reading via m.IsHealthy()) is clean.
func TestHealthyMembers_RaceWithStateMutation(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:        "alpha",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  30 * time.Second,
		GossipInterval: 30 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, p.Start())
	defer p.Stop()

	// Seed a few members directly (mirrors how handlers populate the map).
	p.mu.Lock()
	for _, id := range []string{"b", "c", "d"} {
		p.members[id] = &Member{ID: id, Address: "127.0.0.1:1", State: StateAlive, Metadata: map[string]string{}}
		p.members[id].Touch()
	}
	p.mu.Unlock()

	done := make(chan struct{})
	var wg sync.WaitGroup

	// Reader: HealthyMembers() in a tight loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				_ = p.HealthyMembers()
			}
		}
	}()

	// Mutators: flip member State via the member-locked methods.
	for _, id := range []string{"b", "c", "d"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			p.mu.RLock()
			m := p.members[id]
			p.mu.RUnlock()
			inc := uint32(1)
			for {
				select {
				case <-done:
					return
				default:
					inc++
					m.UpdateState(StateSuspect, inc)
					m.ClearSuspect()
					inc++
					m.UpdateState(StateAlive, inc)
				}
			}
		}(id)
	}

	time.Sleep(150 * time.Millisecond)
	close(done)
	wg.Wait()
}

// TestFailureDetector_ConfirmReleasesLockBeforeCallback proves the onConfirm
// callback runs WITHOUT fd.mu held. The callback re-enters the FailureDetector
// (IsSuspected takes fd.mu.RLock); with the pre-fix code that held fd.mu.Lock()
// across the callback, this would self-deadlock. With the fix it returns
// promptly. A watchdog turns a regression into a clear failure instead of a hang.
func TestFailureDetector_ConfirmReleasesLockBeforeCallback(t *testing.T) {
	done := make(chan struct{})
	var fd *FailureDetector
	fd = NewFailureDetector(40*time.Millisecond, func(id string) {
		// Re-enter the detector from inside the callback. This is only safe if
		// fd.mu was released before onConfirm was invoked.
		_ = fd.IsSuspected(id)
		close(done)
	}, nil)

	fd.Suspect("C", 1)

	select {
	case <-done:
		// Correct: callback ran and could re-enter the detector.
	case <-time.After(2 * time.Second):
		t.Fatal("onConfirm did not complete — fd.mu held across callback (lock-order/deadlock)")
	}
}

// TestFailureDetector_RefuteReleasesLockBeforeCallback is the Refute analogue.
func TestFailureDetector_RefuteReleasesLockBeforeCallback(t *testing.T) {
	done := make(chan struct{})
	var fd *FailureDetector
	fd = NewFailureDetector(5*time.Second, nil, func(id string) {
		_ = fd.IsSuspected(id)
		close(done)
	})

	fd.Suspect("C", 1)
	fd.Refute("C")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onRefute did not complete — fd.mu held across callback (lock-order/deadlock)")
	}
}
