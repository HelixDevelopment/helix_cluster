package semaphore

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSemaphore(t *testing.T) {
	s := New(1)
	s.Acquire()
	if s.TryAcquire() {
		t.Error("expected TryAcquire to fail when full")
	}
	s.Release()
	if !s.TryAcquire() {
		t.Error("expected TryAcquire to succeed after release")
	}
}

// --- Mutation tests ---

func TestSemaphore_CapacityRespected_Mutation(t *testing.T) {
	// Mutation: channel buffer size wrong → semaphore allows more than N concurrent holders
	s := New(2)
	s.Acquire()
	s.Acquire()
	if s.TryAcquire() {
		t.Error("capacity=2: third TryAcquire should fail")
	}
	s.Release()
	if !s.TryAcquire() {
		t.Error("after one release, TryAcquire should succeed")
	}
}

func TestSemaphore_AcquireBlocks_Mutation(t *testing.T) {
	// Mutation: Acquire uses TryAcquire (non-blocking) instead of blocking send
	s := New(1)
	s.Acquire()
	done := make(chan struct{})
	go func() {
		s.Acquire() // should block until Release
		close(done)
	}()
	select {
	case <-done:
		t.Error("second Acquire should block while semaphore is held")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
	s.Release()
	select {
	case <-done:
		// expected
	case <-time.After(200 * time.Millisecond):
		t.Error("second Acquire should unblock after Release")
	}
}

func TestSemaphore_ReleaseOnUnheld_Blocks_Mutation(t *testing.T) {
	// Contract: this semaphore acquires by SENDING into the buffered channel and
	// releases by RECEIVING. A Release() on an unheld semaphore receives from an
	// empty channel and therefore MUST block until a matching Acquire() arrives.
	//
	// Mutation guard: if Release() were (wrongly) made non-blocking — e.g. a
	// `select { case <-s.ch: default: }` — then the extra Release would return
	// immediately and `done` would close early, failing this test.
	s := New(1)
	s.Acquire()
	s.Release() // balanced: channel now empty

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Release() // receive on empty channel -> must block
	}()

	// Assert the over-release BLOCKS: done must NOT close within the window.
	select {
	case <-done:
		t.Fatal("Release() on an unheld semaphore returned early; it must block on the empty channel")
	case <-time.After(100 * time.Millisecond):
		// expected: still blocked
	}

	// Prove the blocked Release() is a real, unblockable pending receiver:
	// a subsequent Acquire() (send) hands its slot to the waiting Release(),
	// which then completes. This also cleans up the goroutine (no leak).
	s.Acquire()
	select {
	case <-done:
		// expected: pending Release() consumed the Acquire() send and returned
	case <-time.After(2 * time.Second):
		t.Fatal("blocked Release() did not complete after a matching Acquire()")
	}
}

// TestSemaphore_ConcurrentCapacityInvariant verifies the core safety property
// under real concurrency: with capacity N, the number of goroutines that hold
// the semaphore simultaneously NEVER exceeds N. Run under `go test -race`.
func TestSemaphore_ConcurrentCapacityInvariant(t *testing.T) {
	const (
		n = 4   // capacity
		m = 200 // M >> N concurrent contenders
	)
	s := New(n)

	var (
		current int64 // currently-held slots
		peak    int64 // max ever observed
		wg      sync.WaitGroup
	)

	wg.Add(m)
	for i := 0; i < m; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.Acquire()
				// --- critical section ---
				held := atomic.AddInt64(&current, 1)
				if held > n {
					t.Errorf("invariant violated: %d holders > capacity %d", held, n)
				}
				for {
					p := atomic.LoadInt64(&peak)
					if held <= p || atomic.CompareAndSwapInt64(&peak, p, held) {
						break
					}
				}
				atomic.AddInt64(&current, -1)
				// --- end critical section ---
				s.Release()
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&current); got != 0 {
		t.Errorf("after all goroutines finished, held slots = %d, want 0", got)
	}
	if got := atomic.LoadInt64(&peak); got > n {
		t.Errorf("peak concurrent holders = %d, exceeds capacity %d", got, n)
	}
	// Sanity: with M >> N and contention, the semaphore should actually reach
	// full capacity at least once; otherwise the test isn't exercising the bound.
	if got := atomic.LoadInt64(&peak); got < 1 {
		t.Errorf("peak concurrent holders = %d, expected the semaphore to be used", got)
	}

	// Round-trip integrity: after K matched pairs, exactly N TryAcquire succeed.
	for i := 0; i < n; i++ {
		if !s.TryAcquire() {
			t.Errorf("TryAcquire #%d should succeed on an empty capacity-%d semaphore", i+1, n)
		}
	}
	if s.TryAcquire() {
		t.Errorf("TryAcquire beyond capacity %d should fail", n)
	}
}

// TestSemaphore_ZeroCapacity_Barrier verifies New(0): TryAcquire always fails
// and Acquire blocks indefinitely (no slot can ever be sent into a 0-buffer
// channel without a concurrent receiver).
func TestSemaphore_ZeroCapacity_Barrier(t *testing.T) {
	s := New(0)

	if s.TryAcquire() {
		t.Fatal("New(0): TryAcquire must always return false")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Acquire() // send on 0-buffer channel with no receiver -> blocks
	}()

	select {
	case <-done:
		t.Fatal("New(0): Acquire must block indefinitely (no capacity)")
	case <-time.After(100 * time.Millisecond):
		// expected: blocked
	}

	// Clean up the blocked goroutine: a Release() (receive) pairs with the
	// pending Acquire() (send) on the unbuffered channel.
	s.Release()
	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("New(0): blocked Acquire() did not complete after a matching Release()")
	}
}
