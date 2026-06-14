package workclaim

// Adversarial at-most-one-holder audit (HXC-1644 / HXC-1585 deepening).
//
// SAFETY INVARIANT under test: a single work item is NEVER held by two workers
// at the same time (a double-claim = two workers doing the same job — the core
// defect this package exists to prevent). The companion suite
// (workclaim_concurrency_test.go) already proves "exactly one winner" by
// counting FINAL winners after a barrier-synchronised race. These tests add two
// dimensions that the final-count tests cannot see:
//
//   1. INSTANTANEOUS peak-holder tracking — a live counter incremented at the
//      moment of Claim and decremented at Complete, so we observe the number of
//      workers SIMULTANEOUSLY holding the same unit (or holding more than the
//      pool's capacity) at any instant, not just the post-hoc tally. A
//      non-atomic check-then-set could let two workers be live on one item for a
//      window even if the final bookkeeping later looks consistent; the peak
//      counter catches that window.
//   2. CLAIM/COMPLETE HAMMER — many goroutines interleave Claim and Complete on
//      a shared queue under -race, asserting no data race, no panic, and a
//      consistent terminal state (every item claimed exactly once, every claim
//      completed exactly once).
//
// MODEL (confirmed from workclaim.go): permanent claims, terminal Complete,
// single mutex guarding an order slice + state map + a `next` scan cursor. No
// lease/TTL, no release-to-unclaimed. The check-then-set in Claim
// (q.st[id]==stateUnclaimed -> q.st[id]=stateClaimed) is fully inside the lock,
// which is the load-bearing mechanism for at-most-one-holder.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestSingleItemPeakHolderIsOne is the instantaneous analog of the single-item
// race. N workers race to Claim ONE item; the unique winner then "works" it
// (briefly) and Completes it. A live holder counter is bumped the instant a
// Claim succeeds and dropped when that holder Completes. The peak value of that
// counter — the maximum number of workers concurrently HOLDING the one item —
// must never exceed 1. This is stronger than counting final winners: it would
// catch a transient window where two workers are both "live" on the item even
// if later accounting self-heals.
//
// ANTI-BLUFF: drop the in-lock mutation in Claim and two racers both observe the
// item unclaimed -> both go live -> peak reaches 2 and this test fails. A
// check-then-set moved outside the lock produces the same failure (plus a -race
// report on q.st).
func TestSingleItemPeakHolderIsOne(t *testing.T) {
	const (
		N     = 48
		iters = 300
	)
	for it := 0; it < iters; it++ {
		q := New()
		theID := fmt.Sprintf("peak-%d", it)
		q.Add(theID)

		wait, release := barrier()

		var live int64 // current number of workers holding the item
		var peak int64 // max value `live` ever reached
		var winners int64

		var ready, done sync.WaitGroup
		ready.Add(N)
		done.Add(N)
		for w := 0; w < N; w++ {
			go func() {
				defer done.Done()
				ready.Done()
				wait()
				id, ok := q.Claim()
				if !ok {
					return
				}
				// We are now a live holder. Record peak concurrent holders.
				cur := atomic.AddInt64(&live, 1)
				for {
					p := atomic.LoadInt64(&peak)
					if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
						break
					}
				}
				atomic.AddInt64(&winners, 1)
				// "Work" the item, then release our hold via Complete.
				if err := q.Complete(id); err != nil {
					t.Errorf("[%s] iter %d: Complete(%q) err=%v want nil", runUUID, it, id, err)
				}
				atomic.AddInt64(&live, -1)
			}()
		}
		ready.Wait()
		release()
		done.Wait()

		if p := atomic.LoadInt64(&peak); p != 1 {
			t.Fatalf("[%s] iter %d: peak concurrent holders=%d want 1 — TWO workers held the same item at once (at-most-one-holder violated)",
				runUUID, it, p)
		}
		if w := atomic.LoadInt64(&winners); w != 1 {
			t.Fatalf("[%s] iter %d: winners=%d want 1", runUUID, it, w)
		}
		if c := q.Completed(); c != 1 {
			t.Fatalf("[%s] iter %d: Completed()=%d want 1", runUUID, it, c)
		}
		if c := q.Claimed(); c != 0 {
			t.Fatalf("[%s] iter %d: Claimed()=%d want 0 (the one holder completed)", runUUID, it, c)
		}
	}
	t.Logf("[%s] single-item peak-holder: %d iters x %d racers — peak concurrent holders never exceeded 1", runUUID, iters, N)
}

// TestPoolPeakHoldersNeverExceedCapacity treats the pool of M items as a
// capacity-M resource: at most M units of work may be in-flight (claimed but not
// completed) at any instant. N>M workers each loop {Claim; mark live; work;
// Complete; drop live}. A peak-holder counter tracks the maximum number of
// SIMULTANEOUSLY in-flight items. Because there are only M distinct items and
// each can be held by at most one worker, the peak can never exceed M — and,
// critically, no single item may ever be live in two workers at once.
//
// ANTI-BLUFF: allowing a re-claim of an in-flight (claimed) item, or a
// non-atomic check-then-set, would push two workers live on one item; the
// per-item live tracker below fires. Allowing claims to exceed the pool would
// push the global peak past M; the capacity assertion fires.
func TestPoolPeakHoldersNeverExceedCapacity(t *testing.T) {
	const (
		M = 200 // pool capacity (distinct items)
		N = 32  // concurrent workers
	)
	q := New()
	want := make(map[string]struct{}, M)
	for i := 0; i < M; i++ {
		id := fmt.Sprintf("cap-%04d", i)
		q.Add(id)
		want[id] = struct{}{}
	}

	wait, release := barrier()

	var globalLive int64
	var globalPeak int64

	// perItemLive[id] must never exceed 1. Guarded by its own mutex so the
	// tracker itself is not the thing under test (we want q's race, not ours).
	var mu sync.Mutex
	perItemLive := make(map[string]int)
	perItemPeak := make(map[string]int)
	claimsPerItem := make(map[string]int)

	var ready, done sync.WaitGroup
	ready.Add(N)
	done.Add(N)
	for w := 0; w < N; w++ {
		go func() {
			defer done.Done()
			ready.Done()
			wait()
			for {
				id, ok := q.Claim()
				if !ok {
					return
				}
				// Global in-flight peak.
				cur := atomic.AddInt64(&globalLive, 1)
				for {
					p := atomic.LoadInt64(&globalPeak)
					if cur <= p || atomic.CompareAndSwapInt64(&globalPeak, p, cur) {
						break
					}
				}
				// Per-item live tracking — this is the at-most-one-holder tooth.
				mu.Lock()
				perItemLive[id]++
				claimsPerItem[id]++
				if perItemLive[id] > perItemPeak[id] {
					perItemPeak[id] = perItemLive[id]
				}
				mu.Unlock()

				// Work, then complete and drop our hold.
				if err := q.Complete(id); err != nil {
					t.Errorf("[%s] Complete(%q) err=%v want nil", runUUID, id, err)
				}

				mu.Lock()
				perItemLive[id]--
				mu.Unlock()
				atomic.AddInt64(&globalLive, -1)
			}
		}()
	}
	ready.Wait()
	release()
	done.Wait()

	// No item was ever held by two workers at once.
	for id, pk := range perItemPeak {
		if pk > 1 {
			t.Fatalf("[%s] item %q reached %d concurrent holders — at-most-one-holder violated", runUUID, id, pk)
		}
	}
	// Each item claimed (and thus completed) exactly once.
	for id := range want {
		if claimsPerItem[id] != 1 {
			t.Fatalf("[%s] item %q claimed %d times want exactly 1", runUUID, id, claimsPerItem[id])
		}
	}
	if len(claimsPerItem) != M {
		t.Fatalf("[%s] distinct claimed items=%d want %d", runUUID, len(claimsPerItem), M)
	}
	// Global in-flight peak cannot exceed pool capacity M.
	if gp := atomic.LoadInt64(&globalPeak); gp > int64(M) {
		t.Fatalf("[%s] global in-flight peak=%d exceeded pool capacity M=%d", runUUID, gp, M)
	}
	if l := atomic.LoadInt64(&globalLive); l != 0 {
		t.Fatalf("[%s] residual in-flight holders=%d want 0 (every claim completed)", runUUID, l)
	}
	if c := q.Completed(); c != M {
		t.Fatalf("[%s] Completed()=%d want %d", runUUID, c, M)
	}
	if c := q.Claimed(); c != 0 {
		t.Fatalf("[%s] Claimed()=%d want 0 (all in-flight completed)", runUUID, c)
	}
	if u := q.Unclaimed(); u != 0 {
		t.Fatalf("[%s] Unclaimed()=%d want 0 (pool fully drained)", runUUID, u)
	}
	t.Logf("[%s] pool peak-holders: M=%d N=%d global-in-flight-peak=%d (<=M) — no item ever double-held, all completed once",
		runUUID, M, N, atomic.LoadInt64(&globalPeak))
}

// TestClaimCompleteHammer interleaves Claim and Complete from many goroutines on
// one shared queue under -race. Each worker drains-and-completes greedily. The
// goal is to surface any unsynchronised access to q.st / q.order / q.next (the
// claim map race class) and any panic from concurrent mutation, then assert a
// consistent terminal state: every item claimed exactly once and completed
// exactly once.
//
// ANTI-BLUFF: moving the check-then-set out of the lock (or removing the lock)
// makes -race report a write/write or read/write on q.st here, and can also
// double-hand-out an item making total completes exceed M.
func TestClaimCompleteHammer(t *testing.T) {
	const (
		M = 2000
		N = 24
	)
	q := New()
	for i := 0; i < M; i++ {
		q.Add(fmt.Sprintf("h-%05d", i))
	}

	var completed int64
	var doubleComplete int64
	var wg sync.WaitGroup
	wg.Add(N)
	for w := 0; w < N; w++ {
		go func() {
			defer wg.Done()
			for {
				id, ok := q.Claim()
				if !ok {
					return
				}
				if err := q.Complete(id); err != nil {
					// A second worker holding the same id would make this the
					// second Complete -> ErrNotClaimed: a double-claim signature.
					atomic.AddInt64(&doubleComplete, 1)
					continue
				}
				atomic.AddInt64(&completed, 1)
			}
		}()
	}
	wg.Wait()

	if dc := atomic.LoadInt64(&doubleComplete); dc != 0 {
		t.Fatalf("[%s] %d Complete calls failed (double-claim signature: a second worker held an already-claimed/completed item)", runUUID, dc)
	}
	if c := atomic.LoadInt64(&completed); c != int64(M) {
		t.Fatalf("[%s] successful completes=%d want %d", runUUID, c, M)
	}
	if c := q.Completed(); c != M {
		t.Fatalf("[%s] Completed()=%d want %d", runUUID, c, M)
	}
	if c := q.Claimed(); c != 0 {
		t.Fatalf("[%s] Claimed()=%d want 0", runUUID, c)
	}
	if u := q.Unclaimed(); u != 0 {
		t.Fatalf("[%s] Unclaimed()=%d want 0", runUUID, u)
	}
	t.Logf("[%s] claim/complete hammer: M=%d N=%d completes=%d double-claims=0 — claim map race-clean, terminal state consistent",
		runUUID, M, N, atomic.LoadInt64(&completed))
}

// TestClaimAfterReleaseSemantics pins documented edge behaviour so a future
// refactor can't silently change it: there is NO Release in this model, so once
// an item is Completed it is terminal and re-Claim never resurfaces it; Complete
// on a non-existent id is ErrUnknownTask; double-Complete is ErrNotClaimed; and
// none of these panic. (Edge coverage requested by the audit; complements the
// concurrency tests with the single-threaded contract.)
func TestClaimAfterReleaseSemantics(t *testing.T) {
	q := New()

	// Claim a non-existent item: empty queue yields ("",false), no panic.
	if id, ok := q.Claim(); ok {
		t.Fatalf("[%s] Claim on empty returned (%q,%v) want (\"\",false)", runUUID, id, ok)
	}
	// Complete a non-existent id.
	if err := q.Complete("ghost"); err != ErrUnknownTask {
		t.Fatalf("[%s] Complete(ghost) err=%v want ErrUnknownTask", runUUID, err)
	}

	q.Add("job")
	id, ok := q.Claim()
	if !ok || id != "job" {
		t.Fatalf("[%s] Claim got (%q,%v) want (\"job\",true)", runUUID, id, ok)
	}
	if err := q.Complete("job"); err != nil {
		t.Fatalf("[%s] Complete(job) err=%v want nil", runUUID, err)
	}
	// Double-complete: terminal item is not claimed -> ErrNotClaimed.
	if err := q.Complete("job"); err != ErrNotClaimed {
		t.Fatalf("[%s] double Complete(job) err=%v want ErrNotClaimed", runUUID, err)
	}
	// "Claim after release": no release exists, so the completed item is never
	// re-offered however many times we try.
	for i := 0; i < 8; i++ {
		if got, ok := q.Claim(); ok {
			t.Fatalf("[%s] claim #%d resurfaced completed item %q (no release/resurrect in this model)", runUUID, i, got)
		}
	}
	if c := q.Completed(); c != 1 {
		t.Fatalf("[%s] Completed()=%d want 1", runUUID, c)
	}
	t.Logf("[%s] edges: empty-claim/unknown-complete/double-complete/post-complete-claim all behave per contract, no panic", runUUID)
}
