package semaphore

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSemaphore_NeverExceedN_HeldUnderContention is the headline adversarial
// test. Unlike the existing invariant test (which increments/decrements the
// live-holder counter with no work in between, leaving only a vanishingly small
// over-admission window), this test makes every holder ACTUALLY HOLD the permit
// for a real, observable interval while inside the critical section. That widens
// the window in which a broken semaphore (off-by-one buffer, unsynchronised
// accounting, non-blocking Acquire) would have N+1 goroutines simultaneously
// inside the section — and the live-holder assertion below would then fire.
//
// Anti-bluff teeth:
//   - `live` counts goroutines currently inside the critical section, updated
//     with atomics, so it is correct under -race regardless of the channel.
//   - Every holder asserts `live <= n` AT ENTRY, AFTER A YIELD, and AT EXIT.
//     Because holders sleep while holding, multiple holders coexist, so a single
//     over-admitted permit is observed by some holder as live == n+1.
//   - `peak` records the maximum live count ever seen; we assert peak == n
//     exactly: <= n proves SAFETY (never over-admit), and == n proves LIVENESS
//     (the semaphore genuinely admits up to N, not artificially fewer). The
//     pair is load-bearing: drop the lower bound and a semaphore that admits 1
//     would pass; drop the upper bound and an over-admitting semaphore would
//     pass. Together they pin the bound exactly.
//
// Run under `go test -race`. -count=10 for repeatability.
func TestSemaphore_NeverExceedN_HeldUnderContention(t *testing.T) {
	const (
		n          = 8   // capacity
		goroutines = 100 // >> n: genuine contention
		rounds     = 20  // each goroutine acquires/releases this many times
	)
	s := New(n)

	var (
		live int64 // goroutines currently inside the critical section
		peak int64 // max live ever observed
		over int64 // count of times live > n was ever seen (must stay 0)
		wg   sync.WaitGroup
	)

	// observe records a live-count sample, updating peak and flagging any
	// over-admission. Returns the sampled value so callers can assert too.
	observe := func(v int64) {
		if v > n {
			atomic.AddInt64(&over, 1)
		}
		for {
			p := atomic.LoadInt64(&peak)
			if v <= p || atomic.CompareAndSwapInt64(&peak, p, v) {
				break
			}
		}
	}

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				s.Acquire()

				// --- critical section: HOLD the permit for a real interval ---
				enter := atomic.AddInt64(&live, 1)
				observe(enter)
				if enter > n {
					t.Errorf("over-admission at entry: live=%d > capacity=%d", enter, n)
				}

				// Yield + sleep so concurrent holders genuinely overlap in time.
				runtime.Gosched()
				time.Sleep(50 * time.Microsecond)

				mid := atomic.LoadInt64(&live)
				observe(mid)
				if mid > n {
					t.Errorf("over-admission mid-hold: live=%d > capacity=%d", mid, n)
				}

				exit := atomic.AddInt64(&live, -1) // value AFTER decrement
				if exit < 0 {
					t.Errorf("live holder count went negative: %d", exit)
				}
				// --- end critical section ---

				s.Release()
			}
		}()
	}
	wg.Wait()

	// SAFETY: no over-admission was ever observed.
	if o := atomic.LoadInt64(&over); o != 0 {
		t.Errorf("SAFETY VIOLATED: observed live > %d on %d occasions", n, o)
	}
	if p := atomic.LoadInt64(&peak); p > n {
		t.Errorf("SAFETY VIOLATED: peak live holders = %d, exceeds capacity %d", p, n)
	}
	// LIVENESS: the semaphore actually reached full capacity. With 100 holders
	// each sleeping while holding, demand vastly exceeds n, so peak MUST hit n.
	// If it didn't, the semaphore is admitting fewer than N (broken liveness) or
	// the test isn't exercising the bound.
	if p := atomic.LoadInt64(&peak); p != n {
		t.Errorf("LIVENESS: peak live holders = %d, want exactly %d "+
			"(< n => under-admission/not stressing the bound)", p, n)
	}
	// Conservation: every holder left the section.
	if l := atomic.LoadInt64(&live); l != 0 {
		t.Errorf("conservation: %d holders still inside section after wg.Wait()", l)
	}

	// Capacity is fully restored: exactly n TryAcquire succeed, the (n+1)th fails.
	for i := 0; i < n; i++ {
		if !s.TryAcquire() {
			t.Errorf("post-run TryAcquire #%d should succeed (capacity %d should be free)", i+1, n)
		}
	}
	if s.TryAcquire() {
		t.Errorf("post-run TryAcquire beyond capacity %d must fail (permits leaked?)", n)
	}
}

// TestSemaphore_PermitConservation_TryAcquire proves no permit is lost or
// double-issued across many concurrent TryAcquire / Release cycles. Each
// successful TryAcquire is a permit that MUST be matched by exactly one Release;
// the live counter must never exceed capacity, and the total number of
// successful acquisitions across all goroutines must equal the number of
// releases (no double-issue, no loss).
//
// Anti-bluff: TryAcquire MUST return true at most n times concurrently. If
// permit accounting were unsynchronised, the live counter could exceed n on a
// burst of concurrent successful TryAcquire calls — caught here and by -race.
func TestSemaphore_PermitConservation_TryAcquire(t *testing.T) {
	const (
		n          = 5
		goroutines = 64
		attempts   = 500
	)
	s := New(n)

	var (
		live      int64
		peak      int64
		acquired  int64 // total successful TryAcquire across all goroutines
		released  int64 // total Release matched to a successful TryAcquire
		overAdmit int64
		wg        sync.WaitGroup
	)

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for a := 0; a < attempts; a++ {
				if !s.TryAcquire() {
					// Full or contended; back off and retry.
					runtime.Gosched()
					continue
				}
				atomic.AddInt64(&acquired, 1)
				cur := atomic.AddInt64(&live, 1)
				if cur > n {
					atomic.AddInt64(&overAdmit, 1)
					t.Errorf("TryAcquire over-admission: live=%d > capacity=%d", cur, n)
				}
				for {
					p := atomic.LoadInt64(&peak)
					if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
						break
					}
				}
				runtime.Gosched()
				atomic.AddInt64(&live, -1)
				s.Release()
				atomic.AddInt64(&released, 1)
			}
		}()
	}
	wg.Wait()

	if o := atomic.LoadInt64(&overAdmit); o != 0 {
		t.Errorf("SAFETY VIOLATED: TryAcquire over-admitted on %d occasions", o)
	}
	if p := atomic.LoadInt64(&peak); p > n {
		t.Errorf("SAFETY VIOLATED: peak live = %d > capacity %d", p, n)
	}
	if p := atomic.LoadInt64(&peak); p < 1 {
		t.Errorf("LIVENESS: peak live = %d, expected at least 1 successful TryAcquire", p)
	}
	// Conservation: every successful TryAcquire was matched by exactly one Release.
	if acq, rel := atomic.LoadInt64(&acquired), atomic.LoadInt64(&released); acq != rel {
		t.Errorf("permit conservation violated: %d acquired vs %d released", acq, rel)
	}
	if l := atomic.LoadInt64(&live); l != 0 {
		t.Errorf("conservation: live = %d after run, want 0", l)
	}
	// Capacity intact.
	for i := 0; i < n; i++ {
		if !s.TryAcquire() {
			t.Errorf("post-run TryAcquire #%d should succeed", i+1)
		}
	}
	if s.TryAcquire() {
		t.Errorf("post-run TryAcquire beyond capacity %d must fail", n)
	}
}

// TestSemaphore_Handoff_WaiterProceeds proves the blocking-Acquire path under
// contention: when the semaphore is full, blocked Acquire()ers are real pending
// senders that proceed — one per Release — without losing or duplicating a
// permit. n permits are pre-held; w > n waiters block; we Release all permits
// and assert every waiter eventually proceeds exactly once and the final
// capacity is conserved.
func TestSemaphore_Handoff_WaiterProceeds(t *testing.T) {
	const (
		n = 3
		w = 12 // waiters >> capacity
	)
	s := New(n)

	// Fill the semaphore.
	for i := 0; i < n; i++ {
		if !s.TryAcquire() {
			t.Fatalf("setup: TryAcquire #%d should succeed on empty capacity-%d semaphore", i+1, n)
		}
	}
	if s.TryAcquire() {
		t.Fatalf("setup: semaphore should be full at capacity %d", n)
	}

	var proceeded int64
	var wg sync.WaitGroup
	wg.Add(w)
	for i := 0; i < w; i++ {
		go func() {
			defer wg.Done()
			s.Acquire() // blocks until a permit is released
			atomic.AddInt64(&proceeded, 1)
			// Hold briefly, then release so the next waiter can proceed.
			time.Sleep(100 * time.Microsecond)
			s.Release()
		}()
	}

	// Give the waiters time to actually block on Acquire.
	time.Sleep(20 * time.Millisecond)
	if p := atomic.LoadInt64(&proceeded); p != 0 {
		t.Fatalf("no permit released yet, but %d waiters proceeded (permits over-issued?)", p)
	}

	// Release the n held permits; this hands them to waiters, which in turn
	// release, cascading until all w waiters have proceeded.
	for i := 0; i < n; i++ {
		s.Release()
	}

	wg.Wait()
	if p := atomic.LoadInt64(&proceeded); p != w {
		t.Errorf("handoff: %d of %d waiters proceeded (permit lost or duplicated)", p, w)
	}

	// After the dust settles, capacity is exactly n again: the cascade conserved
	// permits (n initial held permits + w waiter acquire/release pairs net to 0).
	for i := 0; i < n; i++ {
		if !s.TryAcquire() {
			t.Errorf("post-handoff TryAcquire #%d should succeed (capacity %d should be free)", i+1, n)
		}
	}
	if s.TryAcquire() {
		t.Errorf("post-handoff TryAcquire beyond capacity %d must fail (permit leaked)", n)
	}
}
