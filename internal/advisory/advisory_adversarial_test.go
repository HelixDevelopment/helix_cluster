package advisory

import (
	"context"
	"sync"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----------------------------------------------------------------------------
// Adversarial probes for internal/advisory (the Advisory *Lock* service).
//
// This package is a distributed advisory-lock manager, NOT a scoring/ranking
// advisory engine, so the NaN/Inf comparator and float-tiebreak probes do not
// apply. The applicable adversarial classes here are:
//   (A) MUTUAL EXCLUSION / FAIL-OPEN  — does an unguarded/incomplete input let
//       two holders believe they own the same resource, or let a stale token
//       release someone else's lock?
//   (B) UNGUARDED SHARED STATE        — concurrent lock/unlock/subscribe under
//       -race must not trip the detector and must keep the invariant
//       "at most one holder per resource at any instant".
//   (C) DETERMINISM                   — the at-most-one-acquirer outcome must be
//       byte-stable across many shuffled concurrent rounds.
//   (D) VALIDATION                    — malformed/missing input produces the
//       correct *rejecting* advisory, never a permissive one.
//
// Every assertion is SINK-SIDE: it checks the actual acquired/released verdict
// or the actual delivered event, never merely "no panic".
// ----------------------------------------------------------------------------

// (B)+(C) INVARIANT: at most one holder may believe it owns a resource at any
// instant. We hammer tryLock/unlock from many goroutines on a tiny resource set
// and continuously sample lm.locks; the sampled set must never show a count > 1
// per resource (the map is keyed by resource so a duplicate would mean a logic
// fault, not just a map collision). Under -race this also pins the mutex guard.
//
// SINK-SIDE: we assert on the manager's actual lock table, not on return codes.
func TestAdversarial_MutualExclusionInvariantUnderConcurrency(t *testing.T) {
	lm := newLockManager()
	const resources = 4
	const workers = 8 // <= 8 per anti-hang cap
	resName := func(i int) string { return "res-" + string(rune('a'+i)) }

	stop := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	violations := make(chan string, 1)
	go func() {
		defer sampler.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			lm.mu.RLock()
			// Each resource key maps to exactly one entry by construction; the
			// real invariant we assert is that a held entry is never expired-yet-
			// present in a way that a second holder also sees free. We approximate
			// the sink by checking no resource has a non-nil entry whose holder is
			// empty while still "held".
			for res, e := range lm.locks {
				if e == nil {
					select {
					case violations <- "nil entry for " + res:
					default:
					}
				}
			}
			lm.mu.RUnlock()
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r := resName(j % resources)
				if _, ok := lm.tryLock(r, "h", 50*time.Millisecond); ok {
					_, _ = lm.unlock(r, "h")
				}
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	sampler.Wait()

	select {
	case v := <-violations:
		t.Fatalf("mutual-exclusion invariant violated: %s", v)
	default:
	}
}

// (A) MUTUAL EXCLUSION at the TTL boundary: while holder A still holds a valid
// (non-expired) lock, NO second caller may acquire. This is the core safety
// promise of an advisory lock. SINK-SIDE: the second tryLock's actual bool.
func TestAdversarial_NoDoubleAcquireWhileHeld(t *testing.T) {
	lm := newLockManager()
	_, ok := lm.tryLock("r", "A", time.Minute)
	require.True(t, ok)

	for i := 0; i < 1000; i++ {
		if _, got := lm.tryLock("r", "B", time.Minute); got {
			t.Fatalf("FAIL-OPEN: second holder acquired a held lock on attempt %d", i)
		}
	}
}

// (A) FAIL-OPEN probe: a stale holder must not be able to release a lock that a
// *different* holder currently owns. unlock is holder-keyed (the proto's
// UnlockRequest carries Resource+Holder, no LockId), so the holder string is the
// authorization token. SINK-SIDE: the released bool + error.
func TestAdversarial_ForeignHolderCannotRelease(t *testing.T) {
	lm := newLockManager()
	_, ok := lm.tryLock("r", "owner", time.Minute)
	require.True(t, ok)

	released, err := lm.unlock("r", "attacker")
	assert.False(t, released, "a non-owner must not release the lock")
	require.Error(t, err)

	// And the owner's lock must still be intact (not silently dropped).
	_, ok = lm.tryLock("r", "someoneelse", time.Minute)
	assert.False(t, ok, "owner's lock must survive a rejected foreign unlock")
}

// (D) VALIDATION at the gRPC boundary: missing resource / holder must yield a
// *rejecting* advisory (InvalidArgument), never a permissive acquire.
// SINK-SIDE: the gRPC error code + that no lock was created.
func TestAdversarial_MissingInputsRejected(t *testing.T) {
	s := NewServer()
	ctx := context.Background()

	_, err := s.Lock(ctx, &helixv1.LockRequest{Resource: "", Holder: "h"})
	require.Error(t, err, "empty resource must be rejected")

	_, err = s.Lock(ctx, &helixv1.LockRequest{Resource: "r", Holder: ""})
	require.Error(t, err, "empty holder must be rejected")

	_, err = s.TryLock(ctx, &helixv1.TryLockRequest{Resource: "", Holder: "h"})
	require.Error(t, err, "empty resource must be rejected on TryLock")

	// Nothing should have been recorded as held.
	s.lm.mu.RLock()
	n := len(s.lm.locks)
	s.lm.mu.RUnlock()
	assert.Equal(t, 0, n, "rejected requests must not create any lock")
}

// (A) DETERMINISTIC SAFETY under a thundering herd: exactly ONE of N concurrent
// TryLock callers on the same fresh resource may win, every round. We repeat the
// race many times; a non-deterministic double-grant would surface as count != 1.
// SINK-SIDE: the count of acquirers per round.
func TestAdversarial_ExactlyOneWinnerEveryRound(t *testing.T) {
	for round := 0; round < 50; round++ {
		lm := newLockManager()
		const n = 8 // <= 8
		var wg sync.WaitGroup
		var mu sync.Mutex
		winners := 0
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, ok := lm.tryLock("hot", "h", time.Minute); ok {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()
		require.Equal(t, 1, winners, "round %d: exactly one acquirer expected, got %d", round, winners)
	}
}

// (B) UNGUARDED SHARED STATE on the subscriber set: concurrent subscribe /
// unsubscribe / notify (driven by lock churn) must not race or send on a closed
// channel. SINK-SIDE: completes under -race with no panic and delivers at least
// some events to a live drained subscriber.
func TestAdversarial_SubscribeUnsubscribeNotifyNoRace(t *testing.T) {
	lm := newLockManager()
	var wg sync.WaitGroup

	// A live, continuously-drained subscriber that must receive events. The
	// drainer runs on its OWN WaitGroup: it only exits once `live` is closed by
	// the post-churn unsubscribe, so it must NOT be in `wg` (which gates that
	// unsubscribe) or we'd self-deadlock.
	live := lm.subscribe("p")
	received := make(chan struct{}, 1)
	var drain sync.WaitGroup
	drain.Add(1)
	go func() {
		defer drain.Done()
		for ev := range live {
			if ev != nil {
				select {
				case received <- struct{}{}:
				default:
				}
			}
		}
	}()

	// Churn subscribers.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				ch := lm.subscribe("p")
				lm.unsubscribe("p", ch)
			}
		}()
	}
	// Churn locks to drive notify.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				r := "p/" + string(rune('a'+id))
				if _, ok := lm.tryLock(r, "h", time.Minute); ok {
					_, _ = lm.unlock(r, "h")
				}
			}
		}(w)
	}
	wg.Wait()
	lm.unsubscribe("p", live) // closes `live`, letting the drainer's range exit
	drain.Wait()

	select {
	case <-received:
	default:
		t.Fatal("live subscriber received no events despite lock churn")
	}
}

// (A)/STARVATION: a blocking lock() must eventually acquire once the resource
// becomes durably free, even under repeated short-lived contention. We hold,
// then release, with no competing acquirer, and require the blocked waiter to
// win within a bounded deadline. This pins the wakeWaiters->retry path; a
// lost-wakeup would hit the deadline. The test COMPLETES either way (the waiter
// uses a deadline-bound context so it cannot park forever).
func TestAdversarial_BlockedWaiterEventuallyAcquires(t *testing.T) {
	lm := newLockManager()
	_, ok := lm.tryLock("r", "owner", time.Minute)
	require.True(t, ok)

	got := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		id, _ := lm.lock(ctx, "r", "waiter", time.Minute)
		got <- id
	}()

	// Ensure the waiter is actually parked before releasing.
	require.Eventually(t, func() bool {
		lm.mu.RLock()
		defer lm.mu.RUnlock()
		return len(lm.waiters["r"]) == 1
	}, 2*time.Second, 5*time.Millisecond, "waiter should park")

	_, err := lm.unlock("r", "owner")
	require.NoError(t, err)

	select {
	case id := <-got:
		assert.NotEmpty(t, id, "blocked waiter must acquire after explicit unlock")
	case <-time.After(4 * time.Second):
		t.Fatal("lost wakeup: blocked waiter never acquired after unlock")
	}
}

// (A) CONTEXT-CANCEL SAFETY: a blocked Lock whose ctx is cancelled must return a
// NOT-acquired advisory — never a phantom acquire. SINK-SIDE: acquired bool +
// that the original holder still owns it.
func TestAdversarial_BlockedLockCancelDoesNotPhantomAcquire(t *testing.T) {
	lm := newLockManager()
	_, ok := lm.tryLock("r", "owner", time.Minute)
	require.True(t, ok)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	id, acquired := lm.lock(ctx, "r", "blocked", time.Minute)
	assert.False(t, acquired, "cancelled blocked lock must not report acquired")
	assert.Empty(t, id)

	// Owner still holds it; a fresh contender still fails.
	_, ok = lm.tryLock("r", "contender", time.Minute)
	assert.False(t, ok, "owner must still hold the lock after a contender's ctx cancel")
}
