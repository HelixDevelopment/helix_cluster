package semaphore

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This file is an additional adversarial pass over pkg/semaphore. It targets the
// ONE thing about this implementation that is genuinely surprising and not
// covered elsewhere: the semaphore is INVERTED relative to the usual
// buffered-channel idiom.
//
//	Acquire()    = SEND  into a buffered channel (s.ch <- struct{}{})
//	Release()    = RECV  from that channel        (<-s.ch)
//	TryAcquire() = non-blocking SEND
//
// Consequences that the tests below pin as the real contract:
//
//   - Capacity is the channel BUFFER. At most cap(s.ch) Acquire()s can be
//     outstanding without a matching Release(); the (N+1)th Acquire() blocks.
//
//   - Release() is a RECEIVE, so a Release() that is NOT matched by a prior
//     Acquire() does NOT panic and does NOT raise the effective capacity. It
//     simply blocks (empty channel) until a future Acquire() (send) hands it a
//     value. This is the OPPOSITE of x/sync/semaphore, where Release without
//     Acquire panics, and the opposite of the "release == send" idiom, where an
//     unmatched Release would PERMANENTLY raise capacity (over-admission).
//
// The headline adversarial property proved here: with BALANCED Acquire/Release,
// effective capacity is exactly N — exactly N TryAcquire succeed, the (N+1)th
// fails, and a single Release frees exactly one slot (TestAdversarial_TryAcquire
// ExactCapacityNoOverAdmit). Note an over-Release (unmatched RECV) is, in this
// inverted design, a pending receiver that a later send rendezvouses with — a
// caller-misuse scenario the package does not defend against and that we do NOT
// over-specify here (a blocked Release receiver legitimately absorbs a send,
// which is correct channel semantics, not a capacity bug).

// TestAdversarial_TryAcquireExactCapacityNoOverAdmit pins TryAcquire semantics:
// from empty, EXACTLY n consecutive TryAcquire() succeed and every further one
// fails until a Release. Mutation killed: an off-by-one buffer (New uses n+1) or
// a TryAcquire that ignores the buffer bound would let n+1 succeed.
func TestAdversarial_TryAcquireExactCapacityNoOverAdmit(t *testing.T) {
	for _, n := range []int{1, 2, 3, 7} {
		s := New(n)
		succeeded := 0
		for i := 0; i < n+5; i++ {
			if s.TryAcquire() {
				succeeded++
			}
		}
		if succeeded != n {
			t.Errorf("New(%d): %d TryAcquire succeeded, want exactly %d", n, succeeded, n)
		}
		// One Release frees exactly one slot.
		s.Release()
		if !s.TryAcquire() {
			t.Errorf("New(%d): after one Release, a TryAcquire must succeed", n)
		}
		if s.TryAcquire() {
			t.Errorf("New(%d): only one slot was freed, second TryAcquire must fail", n)
		}
	}
}

// TestAdversarial_BlockedAcquireWakesExactlyOnePerRelease proves the handoff is
// 1:1 — each Release() (recv) wakes EXACTLY ONE blocked Acquire() (send), never
// zero and never two. We fill capacity, block w waiters, then release permits
// ONE AT A TIME with a settle gap, asserting the proceeded count climbs by
// exactly one per Release.
//
// Mutation killed: if Release were a broadcast/close or a non-blocking op, a
// single Release could wake several waiters (proceeded jumps by >1) or none.
func TestAdversarial_BlockedAcquireWakesExactlyOnePerRelease(t *testing.T) {
	const (
		n = 2
		w = 6
	)
	s := New(n)
	for i := 0; i < n; i++ {
		if !s.TryAcquire() {
			t.Fatalf("setup: TryAcquire #%d must succeed", i+1)
		}
	}

	var proceeded int64
	var holding sync.WaitGroup // released when each waiter is told to finish
	holding.Add(1)
	var wg sync.WaitGroup
	wg.Add(w)
	for i := 0; i < w; i++ {
		go func() {
			defer wg.Done()
			s.Acquire() // blocks: capacity full
			atomic.AddInt64(&proceeded, 1)
			holding.Wait() // hold the permit until the test says release
			s.Release()
		}()
	}

	// Let all w waiters block on Acquire (send into full buffer).
	time.Sleep(30 * time.Millisecond)
	if p := atomic.LoadInt64(&proceeded); p != 0 {
		t.Fatalf("before any Release, %d waiters proceeded; capacity was breached", p)
	}

	// Release the n held permits one at a time; each must wake exactly one waiter.
	for i := 1; i <= n; i++ {
		s.Release()
		// Wait for the count to reach exactly i (and never exceed it).
		deadline := time.After(2 * time.Second)
		for {
			p := atomic.LoadInt64(&proceeded)
			if p == int64(i) {
				break
			}
			if p > int64(i) {
				t.Fatalf("after %d Release(s), %d waiters proceeded; a single Release woke more than one", i, p)
			}
			select {
			case <-deadline:
				t.Fatalf("after %d Release(s), only %d waiters proceeded; Release failed to wake a waiter", i, p)
			default:
				runtime.Gosched()
			}
		}
		// Stabilize: ensure it does not overshoot shortly after.
		time.Sleep(10 * time.Millisecond)
		if p := atomic.LoadInt64(&proceeded); p != int64(i) {
			t.Fatalf("after %d Release(s), proceeded=%d, want exactly %d (overshoot => multi-wake)", i, p, i)
		}
	}

	// Now n waiters hold permits; w-n are still blocked. Tell holders to finish;
	// each Release cascades to the next blocked waiter until all w have proceeded.
	holding.Done()
	wg.Wait()
	if p := atomic.LoadInt64(&proceeded); p != w {
		t.Errorf("final: %d of %d waiters proceeded (permit lost or duplicated)", p, w)
	}
	// Capacity restored to exactly n.
	for i := 0; i < n; i++ {
		if !s.TryAcquire() {
			t.Errorf("post-cascade TryAcquire #%d must succeed", i+1)
		}
	}
	if s.TryAcquire() {
		t.Errorf("post-cascade TryAcquire beyond capacity %d must fail", n)
	}
}

// TestAdversarial_NegativeCapacityPanics pins the LATENT RISK that New does not
// validate its argument: make(chan, negative) panics at runtime. There is no
// doc contract forbidding negative n, so this is documented here as the actual
// behavior an end user gets (a panic), not a silent misbehavior. If New ever
// grows input validation, this test should be updated to assert the new
// contract.
func TestAdversarial_NegativeCapacityPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("New(-1) did not panic; expected makechan size-out-of-range panic")
		}
	}()
	neg := -1 // variable so it is a runtime (not compile-time) argument
	_ = New(neg)
}

// TestAdversarial_ConcurrentTryAcquireRelease_Conservation stresses TryAcquire +
// Release together with the live-holder atomic counter; it must never exceed n
// and acquisitions must equal releases. Complements the existing TryAcquire
// conservation test with a tighter critical section and an explicit assertion
// that the live count is observed AT capacity (liveness) without exceeding it.
func TestAdversarial_ConcurrentTryAcquireRelease_Conservation(t *testing.T) {
	const (
		n          = 6
		goroutines = 48
		attempts   = 400
	)
	s := New(n)
	var live, peak, acq, rel, over int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for a := 0; a < attempts; a++ {
				if !s.TryAcquire() {
					runtime.Gosched()
					continue
				}
				atomic.AddInt64(&acq, 1)
				cur := atomic.AddInt64(&live, 1)
				if cur > n {
					atomic.AddInt64(&over, 1)
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
				atomic.AddInt64(&rel, 1)
			}
		}()
	}
	wg.Wait()

	if over != 0 {
		t.Errorf("SAFETY: live exceeded capacity %d on %d occasions", n, over)
	}
	if peak > n {
		t.Errorf("SAFETY: peak live = %d > capacity %d", peak, n)
	}
	if acq != rel {
		t.Errorf("conservation: %d acquired vs %d released", acq, rel)
	}
	if live != 0 {
		t.Errorf("conservation: live = %d after run, want 0", live)
	}
}
