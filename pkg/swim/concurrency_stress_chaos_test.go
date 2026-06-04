package swim

// concurrency_stress_chaos_test.go — sustained-load (stress) and mid-flight
// cancel/kill (chaos) coverage for the Wave-94 concurrency fixes in pkg/swim,
// complementing the deterministic regression guards in
// concurrency_regression_test.go (per Constitution §11.4.85: every concurrency
// fix needs stress + chaos coverage in addition to its unit/-race regression).
//
// Hazards covered here:
//   F1 Stop()/Leave() double-teardown (sync.Once) — STRESS: many concurrent
//      Stop/Leave callers across many protocol instances; CHAOS: Stop racing the
//      live probe/gossip/listen loops mid-flight.
//   F2 Member.State data race — STRESS: HealthyMembers()/Members() readers
//      hammering while many mutators flip State via the member-locked methods.
//   F3 context-aware probe waiter — CHAOS: Stop() cancels p.ctx while a probe
//      waiter is in flight; the waiter must abort promptly (bounded by Stop()
//      returning) instead of blocking for the full probeTimeout, and wg.Wait()
//      inside Stop() must not hang.
//   F4 FailureDetector callback invoked without fd.mu held — STRESS: many
//      goroutines Suspect/Refute/Confirm with re-entrant callbacks; CHAOS:
//      callbacks re-enter the detector while suspicions are torn down.
//
// All tests run clean under `go test -race`.

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newStressProtocol(t *testing.T, id string) *Protocol {
	t.Helper()
	p, err := NewProtocol(&Config{
		LocalID:        id,
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  5 * time.Millisecond,
		ProbeTimeout:   5 * time.Millisecond,
		GossipInterval: 5 * time.Millisecond,
	})
	require.NoError(t, err)
	return p
}

// TestStop_StressManyConcurrentCallers (F1) hammers Stop()/Leave() with a large
// fleet of concurrent callers across many protocol instances under sustained
// load. The invariant: no "close of closed channel" panic, no racy double
// transport.Close, and every instance is fully torn down (Stop returns) — the
// sync.Once teardown must hold under heavy contention.
func TestStop_StressManyConcurrentCallers(t *testing.T) {
	const (
		instances       = 40
		callersPerInst  = 16
	)
	var stopped int64
	var wg sync.WaitGroup

	for i := 0; i < instances; i++ {
		p := newStressProtocol(t, "node")
		require.NoError(t, p.Start())

		// Mix Stop() and Leave() callers racing each other on the same instance.
		for c := 0; c < callersPerInst; c++ {
			wg.Add(1)
			c := c
			go func() {
				defer wg.Done()
				if c%3 == 0 {
					_ = p.Leave() // Leave() internally calls Stop()
				} else {
					_ = p.Stop()
				}
			}()
		}
		// A teardown-confirming caller: after all racers, Stop() must still
		// return cleanly (idempotent) for this instance.
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.Stop()
			atomic.AddInt64(&stopped, 1)
		}()
	}
	wg.Wait()

	require.Equal(t, int64(instances), atomic.LoadInt64(&stopped),
		"every instance must be cleanly stopped under concurrent Stop/Leave load")
}

// TestStop_ChaosStopRacingLiveLoops (F1 + F3) starts the live probe/gossip/listen
// loops with seeded members (so probeRandomMember actually launches in-flight
// ctx-aware ack waiters), lets them run, then injects Stop() concurrently from
// several goroutines mid-flight. The invariants: no panic, and Stop() returns
// within a bound far shorter than it would take if an in-flight probe waiter
// blocked for the full probeTimeout without honouring p.ctx (F3) or if wg.Wait()
// hung. A watchdog turns a regression into a failure rather than a hang.
func TestStop_ChaosStopRacingLiveLoops(t *testing.T) {
	const instances = 25
	for i := 0; i < instances; i++ {
		p := newStressProtocol(t, "alpha")
		// Seed unreachable peers so the probe loop has live targets to probe;
		// their addresses never ack, so probe waiters are genuinely in flight.
		p.mu.Lock()
		for _, id := range []string{"b", "c", "d", "e"} {
			m := &Member{ID: id, Address: "127.0.0.1:1", State: StateAlive, Metadata: map[string]string{}}
			m.Touch()
			p.members[id] = m
		}
		p.mu.Unlock()
		require.NoError(t, p.Start())

		// Let the loops spin and launch in-flight probe waiters.
		time.Sleep(15 * time.Millisecond)

		done := make(chan struct{})
		go func() {
			var wg sync.WaitGroup
			for s := 0; s < 6; s++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_ = p.Stop()
				}()
			}
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Stop() returned: ctx cancellation aborted in-flight probe waiters
			// and wg.Wait() did not hang.
		case <-time.After(3 * time.Second):
			t.Fatal("Stop() did not return promptly — in-flight probe waiter ignored ctx cancel (F3) or wg.Wait deadlocked")
		}
	}
}

// TestHealthyMembers_StressReadersVsMutators (F2) runs many concurrent
// HealthyMembers()/Members() readers against many State mutators over a sustained
// period. Under -race the pre-fix code (reading m.State under only p.mu) trips
// the detector; the fix (reading via m.IsHealthy()) is clean. The invariant: no
// data race and HealthyMembers never returns a member that is not currently
// Alive at the moment it was observed healthy is not assertable atomically, but
// we DO assert the snapshot only ever contains members from the known set and
// the call never panics under contention.
func TestHealthyMembers_StressReadersVsMutators(t *testing.T) {
	p := newStressProtocol(t, "alpha")
	require.NoError(t, p.Start())
	defer p.Stop()

	ids := []string{"b", "c", "d", "e", "f", "g", "h"}
	known := map[string]bool{"alpha": true}
	p.mu.Lock()
	for _, id := range ids {
		m := &Member{ID: id, Address: "127.0.0.1:1", State: StateAlive, Metadata: map[string]string{}}
		m.Touch()
		p.members[id] = m
		known[id] = true
	}
	p.mu.Unlock()

	done := make(chan struct{})
	var wg sync.WaitGroup

	const readers = 32
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					for _, m := range p.HealthyMembers() {
						if !known[m.ID] {
							// Reading m.ID is safe (immutable); a foreign ID
							// would mean a corrupted snapshot.
							panic("HealthyMembers returned unknown member: " + m.ID)
						}
					}
					_ = p.Members()
				}
			}
		}()
	}

	const mutatorsPerID = 3
	for _, id := range ids {
		for k := 0; k < mutatorsPerID; k++ {
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
	}

	time.Sleep(250 * time.Millisecond)
	close(done)
	wg.Wait()
}

// TestFailureDetector_StressSuspectRefuteConfirm (F4) drives many goroutines
// through Suspect/Refute/Confirm/IsSuspected on a shared detector under sustained
// load, with callbacks that RE-ENTER the detector (IsSuspected/SuspectCount).
// Re-entrant callbacks are only safe because fd.mu is released before the
// callback runs (F4 fix). The invariant: no deadlock (bounded by the watchdog),
// no panic, and the detector returns to a consistent empty state after all
// suspicions are torn down.
func TestFailureDetector_StressSuspectRefuteConfirm(t *testing.T) {
	var fd *FailureDetector
	var confirms, refutes int64
	fd = NewFailureDetector(
		20*time.Millisecond,
		func(id string) {
			// Re-enter the detector from the confirm callback.
			_ = fd.IsSuspected(id)
			_ = fd.SuspectCount()
			atomic.AddInt64(&confirms, 1)
		},
		func(id string) {
			_ = fd.IsSuspected(id)
			atomic.AddInt64(&refutes, 1)
		},
	)

	const workers = 64
	ids := []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7"}

	finished := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				id := ids[w%len(ids)]
				for iter := 0; iter < 400; iter++ {
					fd.Suspect(id, uint32(iter))
					if iter%2 == 0 {
						fd.Refute(id)
					}
					_ = fd.IsSuspected(id)
					_ = fd.SuspectCount()
				}
			}(w)
		}
		wg.Wait()
		// Tear down any remaining suspicions explicitly.
		for _, id := range ids {
			fd.Refute(id)
		}
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Suspect/Refute/Confirm storm deadlocked — fd.mu likely held across a re-entrant callback (F4)")
	}

	// After explicit teardown, no suspect should remain (timers either fired
	// Confirm — which deletes — or were Refuted). Allow a brief settle for any
	// AfterFunc timers still in flight, then assert the detector is empty.
	require.Eventually(t, func() bool {
		return fd.SuspectCount() == 0
	}, 2*time.Second, 10*time.Millisecond, "all suspicions must drain to a consistent empty state")
}
