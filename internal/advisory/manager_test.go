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

// TestTryLockReturnsStableLockID proves tryLock mints a unique, non-empty lock
// ID and that re-acquiring the same resource (after release) yields a *different*
// ID. Mutation that fails this: replacing `lockID := uuid.NewString()` with a
// constant such as `lockID := "static"` would make the inequality assertion fail.
func TestTryLockReturnsStableLockID(t *testing.T) {
	lm := newLockManager()

	id1, ok := lm.tryLock("r", "h1", time.Minute)
	require.True(t, ok)
	require.NotEmpty(t, id1)

	_, err := lm.unlock("r", "h1")
	require.NoError(t, err)

	id2, ok := lm.tryLock("r", "h2", time.Minute)
	require.True(t, ok)
	require.NotEmpty(t, id2)
	assert.NotEqual(t, id1, id2, "each acquisition must mint a fresh lock ID")
}

// TestDefaultTTLAppliedOnNonPositive proves a non-positive TTL falls back to
// defaultTTL rather than producing an already-expired entry. Mutation that
// fails this: deleting the `if ttl <= 0 { ttl = defaultTTL }` block — the entry
// would expire at time.Now() and the second tryLock would immediately succeed.
func TestDefaultTTLAppliedOnNonPositive(t *testing.T) {
	lm := newLockManager()

	_, ok := lm.tryLock("r", "h1", 0)
	require.True(t, ok)

	// With defaultTTL (30s) applied, a competing acquire must fail right now.
	_, ok = lm.tryLock("r", "h2", time.Minute)
	assert.False(t, ok, "default TTL must keep the lock held")

	lm.mu.RLock()
	entry := lm.locks["r"]
	lm.mu.RUnlock()
	require.NotNil(t, entry)
	assert.True(t, entry.expires.After(time.Now().Add(25*time.Second)),
		"expiry should be ~defaultTTL out, not near-zero")
}

// TestUnlockUnknownResourceErrors proves unlock surfaces an error (not a silent
// false) for a resource that was never locked. Mutation that fails this:
// changing `return false, fmt.Errorf(...)` to `return false, nil` in the
// not-found branch.
func TestUnlockUnknownResourceErrors(t *testing.T) {
	lm := newLockManager()
	ok, err := lm.unlock("ghost", "h")
	assert.False(t, ok)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock not found")
}

// TestSubscribeReceivesAcquireAndRelease proves notify routes events to a
// prefix subscriber for both acquire and release, with correct holder fields.
// Mutation that fails this: changing the prefix check `strings.HasPrefix` to a
// strict `==` would drop the "p/res" event (prefix "p" != "p/res").
func TestSubscribeReceivesAcquireAndRelease(t *testing.T) {
	lm := newLockManager()
	ch := lm.subscribe("p")

	_, ok := lm.tryLock("p/res", "owner", time.Minute)
	require.True(t, ok)

	acq := recvEvent(t, ch)
	assert.Equal(t, "p/res", acq.Resource)
	assert.Equal(t, "owner", acq.Holder)
	assert.Equal(t, eventAcquired, acq.EventType)

	_, err := lm.unlock("p/res", "owner")
	require.NoError(t, err)

	rel := recvEvent(t, ch)
	assert.Equal(t, "p/res", rel.Resource)
	assert.Equal(t, eventReleased, rel.EventType)
}

// TestUnsubscribeStopsDelivery proves unsubscribe removes the channel from the
// fan-out set so later events are not delivered, and closes the channel.
// Mutation that fails this: making unsubscribe a no-op (removing the slice
// splice) would keep delivering, so the channel would receive the post-
// unsubscribe acquire event instead of being closed/empty.
func TestUnsubscribeStopsDelivery(t *testing.T) {
	lm := newLockManager()
	ch := lm.subscribe("q")
	lm.unsubscribe("q", ch)

	// Channel must be closed by unsubscribe.
	_, open := <-ch
	assert.False(t, open, "unsubscribe must close the channel")

	// Further events must not panic on a closed channel (i.e. it was removed
	// from subs). If unsubscribe failed to remove it, notify would send on a
	// closed channel and panic.
	_, ok := lm.tryLock("q/x", "h", time.Minute)
	require.True(t, ok)
	// give notify a moment; absence of panic is the assertion
	time.Sleep(10 * time.Millisecond)
}

// TestNotifyNonBlockingOnFullSubscriber proves a slow/full subscriber does not
// block lock operations (the select/default drop path). Mutation that fails
// this: removing the `default:` case in notify would make tryLock block forever
// once the buffer (16) fills, and this test would deadlock/time out.
func TestNotifyNonBlockingOnFullSubscriber(t *testing.T) {
	lm := newLockManager()
	_ = lm.subscribe("z") // never drained

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			res := "z/" + string(rune('a'+i%26)) + string(rune('0'+i/26))
			lm.tryLock(res, "h", time.Minute)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notify blocked on a full subscriber channel")
	}
}

// TestLockContextCancelRemovesWaiter proves that a blocked Lock whose context is
// cancelled removes itself from the waiters slice (no leak) and returns not
// acquired. Mutation that fails this: removing the waiter-removal loop in the
// ctx.Done() branch would leave a stale closed-less channel; the subsequent
// assertion on waiter count would be non-zero.
func TestLockContextCancelRemovesWaiter(t *testing.T) {
	lm := newLockManager()

	_, ok := lm.tryLock("res", "owner", time.Minute)
	require.True(t, ok)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	id, acquired := lm.lock(ctx, "res", "blocked", time.Minute)
	assert.False(t, acquired)
	assert.Empty(t, id)

	lm.mu.RLock()
	n := len(lm.waiters["res"])
	lm.mu.RUnlock()
	assert.Equal(t, 0, n, "cancelled waiter must be removed from the waiters slice")
}

// TestLockWokenByExpirationTicker proves a blocked Lock is eventually freed by
// the expirationLoop when the holder lets the TTL lapse without an explicit
// Unlock (no competing acquirer involved). Mutation that fails this: removing
// the `lm.wakeWaiters(res)` call inside expirationLoop would leave the waiter
// parked forever and this test would hit its 2s deadline.
func TestLockWokenByExpirationTicker(t *testing.T) {
	lm := newLockManager()

	// Hold with a short TTL; nobody will explicitly unlock it.
	_, ok := lm.tryLock("res", "owner", 1100*time.Millisecond)
	require.True(t, ok)

	gotID := make(chan string, 1)
	go func() {
		id, _ := lm.lock(context.Background(), "res", "waiter", time.Minute)
		gotID <- id
	}()

	// Ensure the waiter is parked.
	require.Eventually(t, func() bool {
		lm.mu.RLock()
		defer lm.mu.RUnlock()
		return len(lm.waiters["res"]) == 1
	}, time.Second, 5*time.Millisecond, "waiter should park")

	select {
	case id := <-gotID:
		assert.NotEmpty(t, id, "waiter must acquire after the holder's TTL lapses")
	case <-time.After(3 * time.Second):
		t.Fatal("expiration ticker failed to wake a waiter on TTL lapse")
	}
}

// recvEvent reads one event from ch or fails the test on timeout.
func recvEvent(t *testing.T, ch chan *helixv1.LockEvent) *helixv1.LockEvent {
	t.Helper()
	select {
	case ev := <-ch:
		require.NotNil(t, ev)
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lock event")
		return nil
	}
}

// TestConcurrentLockUnlockNoRace stresses acquire/release across goroutines to
// catch data races in the manager under -race. Mutation that fails this:
// removing the mutex guard (e.g. changing `lm.mu.Lock()` to no-op) in tryLock or
// unlock would trip the race detector.
func TestConcurrentLockUnlockNoRace(t *testing.T) {
	lm := newLockManager()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			res := "r" + string(rune('a'+n%5))
			for j := 0; j < 50; j++ {
				if id, ok := lm.tryLock(res, "h", time.Minute); ok {
					assert.NotEmpty(t, id)
					_, _ = lm.unlock(res, "h")
				}
			}
		}(i)
	}
	wg.Wait()
}
