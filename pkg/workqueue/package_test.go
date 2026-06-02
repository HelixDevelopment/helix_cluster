package workqueue

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const runUUID = "wq-test-7f3c1a90-5b2e-4d61-9a0c-1e2f3a4b5c6d"

func anchor() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

// TestRateLimitedExponentialGrowthAndForget proves clause 1: repeated
// AddRateLimited on one failing item schedules retry delays that grow
// exponentially (5ms, 10ms, 20ms, 40ms, ...) up to the cap, and Forget resets
// the schedule back to the base delay.
//
// Mutation: if RateLimitConfig.delayForFailures returned a constant (e.g.
// dropped the 2^failures growth, or Failures was never incremented), the
// strict-doubling assertions below would fail. If Forget did not delete the
// failure count, the post-Forget delay would not return to base and that
// assertion would fail.
func TestRateLimitedExponentialGrowthAndForget(t *testing.T) {
	clk := NewManualClock(anchor())
	rl := RateLimitConfig{Base: 5 * time.Millisecond, Max: 40 * time.Millisecond}
	q := New(clk, rl)
	const item = "flaky-reconcile-key"

	want := []time.Duration{
		5 * time.Millisecond,
		10 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond, // cap reached
		40 * time.Millisecond, // stays capped
		40 * time.Millisecond,
	}

	t.Logf("run=%s clause=1 before: failures(%q)=%d", runUUID, item, q.Failures(item))
	for i, expect := range want {
		got := q.AddRateLimited(item)
		t.Logf("run=%s clause=1 attempt=%d scheduledDelay=%s expected=%s failuresAfter=%d",
			runUUID, i, got, expect, q.Failures(item))
		if got != expect {
			t.Fatalf("attempt %d: delay=%s want=%s", i, got, expect)
		}
	}

	// Independently prove the geometric ramp before the cap: each of the first
	// three steps must be exactly double the previous.
	for i := 1; i <= 2; i++ {
		if want[i] != 2*want[i-1] {
			t.Fatalf("ramp broken at %d", i)
		}
		// Recompute via the config to ensure the production path doubles too.
		prev := rl.delayForFailures(i - 1)
		cur := rl.delayForFailures(i)
		if cur != 2*prev {
			t.Fatalf("delayForFailures not doubling: prev(%d)=%s cur(%d)=%s", i-1, prev, i, cur)
		}
	}

	// Forget resets to base.
	beforeFail := q.Failures(item)
	q.Forget(item)
	afterFail := q.Failures(item)
	resetDelay := q.AddRateLimited(item)
	t.Logf("run=%s clause=1 forget: failuresBefore=%d failuresAfter=%d nextDelay=%s",
		runUUID, beforeFail, afterFail, resetDelay)
	if afterFail != 0 {
		t.Fatalf("Forget did not zero failures: got %d", afterFail)
	}
	if resetDelay != 5*time.Millisecond {
		t.Fatalf("after Forget delay=%s want=5ms", resetDelay)
	}
}

// TestConcurrentDedupAndRequeueOnce proves clause 2: many goroutines Add the
// SAME item rapidly, yet Get yields it exactly once per ready window and Len
// reflects the dedup (never more than 1 ready copy). Re-Adding while the item
// is checked out (between Get and Done) yields exactly one re-delivery after
// Done.
//
// Mutation: if enqueueLocked dropped the dirty-set membership check (the dedup
// choke point), concurrent Adds would append many copies and firstGetCount /
// Len would exceed 1, failing here. If Done did not re-enqueue a still-dirty
// item, the re-delivery assertion (secondGet) would block/fail.
func TestConcurrentDedupAndRequeueOnce(t *testing.T) {
	clk := NewManualClock(anchor())
	q := New(clk, DefaultRateLimit())
	const item = "singleton-key"

	const goroutines = 64
	const addsEach = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < addsEach; i++ {
				q.Add(item)
			}
		}()
	}
	wg.Wait()

	lenAfterStorm := q.Len()
	t.Logf("run=%s clause=2 before: %d goroutines x %d adds, Len()=%d",
		runUUID, goroutines, addsEach, lenAfterStorm)
	if lenAfterStorm != 1 {
		t.Fatalf("dedup failed: Len()=%d after %d concurrent Adds, want 1",
			lenAfterStorm, goroutines*addsEach)
	}

	// Get yields it exactly once: a second drain attempt finds the FIFO empty.
	got, shut := q.Get()
	if shut || got != item {
		t.Fatalf("Get got=%v shutdown=%v want item=%v", got, shut, item)
	}
	if l := q.Len(); l != 0 {
		t.Fatalf("after single Get, Len()=%d want 0 (item now processing)", l)
	}
	t.Logf("run=%s clause=2 after firstGet: item=%v LenWhileProcessing=%d", runUUID, got, q.Len())

	// Re-Add many times WHILE checked out (between Get and Done).
	for i := 0; i < 500; i++ {
		q.Add(item)
	}
	if l := q.Len(); l != 0 {
		t.Fatalf("re-Add during processing leaked into FIFO: Len()=%d want 0", l)
	}

	// Done must re-enqueue exactly once.
	q.Done(item)
	if l := q.Len(); l != 1 {
		t.Fatalf("after Done, Len()=%d want exactly 1 re-delivery", l)
	}
	got2, shut2 := q.Get()
	if shut2 || got2 != item {
		t.Fatalf("re-delivery Get got=%v shutdown=%v want %v", got2, shut2, item)
	}
	q.Done(got2)
	finalLen := q.Len()
	t.Logf("run=%s clause=2 after requeue: secondGet=%v finalLen=%d (want 0)", runUUID, got2, finalLen)
	if finalLen != 0 {
		t.Fatalf("after second Done with no pending re-Add, Len()=%d want 0", finalLen)
	}
}

// TestShutdownUnblocksWaiters proves clause 3: ShutDown makes Get return
// shutdown=true and unblocks goroutines already blocked in Get.
//
// Mutation: if ShutDown did not set shuttingDown / Broadcast, the blocked Get
// goroutines would never wake and this test would time out (fail).
func TestShutdownUnblocksWaiters(t *testing.T) {
	clk := NewManualClock(anchor())
	q := New(clk, DefaultRateLimit())

	const waiters = 8
	results := make(chan bool, waiters)
	var started int32
	for i := 0; i < waiters; i++ {
		go func() {
			atomic.AddInt32(&started, 1)
			_, shutdown := q.Get() // blocks: queue empty
			results <- shutdown
		}()
	}

	// Wait until all goroutines have entered Get (best-effort spin on the
	// started counter, then a yield to let them reach cond.Wait()).
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&started) < waiters && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	t.Logf("run=%s clause=3 before: startedWaiters=%d shuttingDown=%v",
		runUUID, atomic.LoadInt32(&started), q.ShuttingDown())

	q.ShutDown()

	timeout := time.After(2 * time.Second)
	got := 0
	for got < waiters {
		select {
		case shutdown := <-results:
			if !shutdown {
				t.Fatalf("blocked Get returned shutdown=false after ShutDown")
			}
			got++
		case <-timeout:
			t.Fatalf("ShutDown did not unblock all waiters: got %d/%d", got, waiters)
		}
	}
	t.Logf("run=%s clause=3 after: shuttingDown=%v unblockedWaiters=%d/%d",
		runUUID, q.ShuttingDown(), got, waiters)

	// A fresh Get after shutdown also reports shutdown immediately.
	if _, shutdown := q.Get(); !shutdown {
		t.Fatalf("Get after ShutDown returned shutdown=false")
	}
}

// TestAddAfterBecomesReadyOnClockAdvance proves AddAfter defers an item until
// the injected clock reaches the ready time — never via real sleep.
//
// Mutation: if readyLocked promoted waiters regardless of w.t.After(now) (i.e.
// ignored the deadline), the item would be ready immediately and the
// "not ready yet" assertion would fail.
func TestAddAfterBecomesReadyOnClockAdvance(t *testing.T) {
	clk := NewManualClock(anchor())
	q := New(clk, DefaultRateLimit())
	const item = "deferred-key"

	q.AddAfter(item, 50*time.Millisecond)
	lenEarly := q.Len()
	t.Logf("run=%s addAfter before-advance: Len()=%d (want 0)", runUUID, lenEarly)
	if lenEarly != 0 {
		t.Fatalf("AddAfter became ready too early: Len()=%d want 0", lenEarly)
	}

	clk.Step(49 * time.Millisecond)
	if l := q.Len(); l != 0 {
		t.Fatalf("AddAfter ready 1ms early: Len()=%d want 0", l)
	}
	clk.Step(1 * time.Millisecond) // now exactly at ready time
	lenReady := q.Len()
	t.Logf("run=%s addAfter after-advance(50ms): Len()=%d (want 1)", runUUID, lenReady)
	if lenReady != 1 {
		t.Fatalf("AddAfter not ready at deadline: Len()=%d want 1", lenReady)
	}
	got, shut := q.Get()
	if shut || got != item {
		t.Fatalf("Get got=%v shutdown=%v want %v", got, shut, item)
	}
	q.Done(got)
}

// TestRateLimitedReadyAfterClockAdvance ties AddRateLimited's scheduled delay
// to actual readiness through the injected clock, proving the backoff delay is
// honored (not merely returned).
//
// Mutation: if AddRateLimited enqueued immediately instead of scheduling a
// waiter at now+delay, the item would be ready before the clock advanced and
// the "not ready" assertion would fail.
func TestRateLimitedReadyAfterClockAdvance(t *testing.T) {
	clk := NewManualClock(anchor())
	rl := RateLimitConfig{Base: 8 * time.Millisecond, Max: time.Second}
	q := New(clk, rl)
	const item = "retry-key"

	d := q.AddRateLimited(item)
	if d != 8*time.Millisecond {
		t.Fatalf("first delay=%s want 8ms", d)
	}
	if l := q.Len(); l != 0 {
		t.Fatalf("rate-limited item ready immediately: Len()=%d want 0", l)
	}
	clk.Step(8 * time.Millisecond)
	lenReady := q.Len()
	t.Logf("run=%s rateLimitReady: scheduled=%s afterAdvance Len()=%d (want 1)", runUUID, d, lenReady)
	if lenReady != 1 {
		t.Fatalf("rate-limited item not ready after delay elapsed: Len()=%d want 1", lenReady)
	}
}
