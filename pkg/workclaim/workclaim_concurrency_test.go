package workclaim

// Adversarial mutual-exclusion suite (HXC-1585 concurrency teeth).
//
// The safety property under test is the SKIP LOCKED invariant stated in
// workclaim.go: a task that has been claimed is invisible to every other
// worker, so under arbitrary concurrency each task is handed out to AT MOST
// ONE claimant. The existing suite (workclaim_test.go) drains a pool in a hot
// loop where each worker grabs "whatever is available"; it never forces N
// goroutines to contend for the SAME single item at the SAME instant. These
// tests close that gap with a release-the-barrier design that makes the
// claimants genuinely race, then assert EXACTLY ONE winner — a correctness
// property that must hold on EVERY run, not probabilistically.
//
// HONEST MODEL NOTES (read before extending):
//   - This Queue models PERMANENT claims with a terminal Complete (claimed ->
//     completed). There is NO lease/TTL, NO injectable clock, and NO
//     release-back-to-unclaimed. Therefore:
//       * "lease/TTL expiry" cannot be driven deterministically and is NOT
//         tested here (there is nothing to advance). See
//         TestNoLeaseExpiryModeled, which documents+asserts the absence so a
//         future lease feature that silently resurrected claims would be caught.
//       * "release enables re-claim" in the literal sense does not exist; the
//         strongest REAL property is that a Completed item is never resurrected
//         (no second claimant ever sees it), which TestCompletedNeverResurfaces
//         proves under concurrency.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// barrier releases all waiting goroutines at once by closing a channel, so the
// claimants below truly contend rather than running one-after-another.
func barrier() (wait func(), release func()) {
	ch := make(chan struct{})
	return func() { <-ch }, func() { close(ch) }
}

// TestConcurrentClaimRaceSingleItem is the HEADLINE mutual-exclusion test.
//
// A queue holds EXACTLY ONE task. N goroutines all block on a barrier, then are
// released simultaneously to call Claim() on that single item. The safety
// property: EXACTLY ONE goroutine receives (id,true); the remaining N-1 receive
// ("",false). This is looped many times; the interleaving must be exercised and
// the invariant must hold EVERY iteration (it is a correctness property, not a
// probability).
//
// ANTI-BLUFF: a fake Claim that always returned ok==true (the classic "claim
// always succeeds" stub) would let MULTIPLE goroutines win the single item;
// winners != 1 fails the test. A fake that always returned ok==false would
// yield winners==0; also fails. Only a real at-most-one-holder claim table
// passes. -race additionally catches any unsynchronised mutation of the shared
// claim state under genuine simultaneous contention.
func TestConcurrentClaimRaceSingleItem(t *testing.T) {
	const (
		N     = 32  // concurrent claimants contending for ONE item
		iters = 400 // distinct races; each must yield exactly one winner
	)

	for it := 0; it < iters; it++ {
		q := New()
		theID := fmt.Sprintf("solo-%d", it)
		q.Add(theID)

		wait, release := barrier()

		var winners int64         // count of goroutines that got ok==true
		var winnerID atomic.Value // the id the winner observed
		var losers int64          // count of goroutines that got ok==false
		var badID int64           // count of winners whose id != theID

		var ready sync.WaitGroup // all goroutines parked on the barrier
		var done sync.WaitGroup  // all goroutines finished their Claim
		ready.Add(N)
		done.Add(N)

		for w := 0; w < N; w++ {
			go func() {
				defer done.Done()
				ready.Done()
				wait() // park until released — forces simultaneous contention
				id, ok := q.Claim()
				if ok {
					atomic.AddInt64(&winners, 1)
					winnerID.Store(id)
					if id != theID {
						atomic.AddInt64(&badID, 1)
					}
				} else {
					atomic.AddInt64(&losers, 1)
				}
			}()
		}

		ready.Wait() // every goroutine is now blocked on the barrier
		release()    // fire the gun: all N race into Claim() together
		done.Wait()

		w := atomic.LoadInt64(&winners)
		l := atomic.LoadInt64(&losers)
		b := atomic.LoadInt64(&badID)

		if w != 1 {
			t.Fatalf("[%s] iter %d: winners=%d want EXACTLY 1 (mutual exclusion violated: %d claimants got the same single item)",
				runUUID, it, w, w)
		}
		if l != N-1 {
			t.Fatalf("[%s] iter %d: losers=%d want %d (winners=%d) — accounting leak",
				runUUID, it, l, N-1, w)
		}
		if b != 0 {
			t.Fatalf("[%s] iter %d: %d winner(s) observed an id != %q (phantom item)", runUUID, it, b, theID)
		}
		// Independent oracle: the queue's own bookkeeping must agree — exactly
		// one claimed, zero still unclaimed.
		if c := q.Claimed(); c != 1 {
			t.Fatalf("[%s] iter %d: Claimed()=%d want 1 after single-item race", runUUID, it, c)
		}
		if u := q.Unclaimed(); u != 0 {
			t.Fatalf("[%s] iter %d: Unclaimed()=%d want 0 after single-item race", runUUID, it, u)
		}
		if got, _ := winnerID.Load().(string); got != theID {
			t.Fatalf("[%s] iter %d: winnerID=%q want %q", runUUID, it, got, theID)
		}
	}
	t.Logf("[%s] single-item race: %d iterations x %d claimants — EXACTLY ONE winner every time", runUUID, iters, N)
}

// TestCompletedNeverResurfaces is the REAL analog of "release enables re-claim"
// for this permanent-claim model. After the sole winner Completes the item, no
// other worker may ever claim it again (a completed/stale holder must never be
// re-handed-out, else two workers could act on the same unit). N late workers
// race Claim() after Complete; ALL must get ok==false.
//
// ANTI-BLUFF: a fake that re-handed-out a completed task (resurrection) would
// give a late winner; the test fails. This is the mutual-exclusion-preserving
// behaviour of THIS model — a terminal Complete must close the item for good.
func TestCompletedNeverResurfaces(t *testing.T) {
	const N = 24
	q := New()
	q.Add("unit")

	id, ok := q.Claim()
	if !ok || id != "unit" {
		t.Fatalf("[%s] initial Claim got (%q,%v) want (\"unit\",true)", runUUID, id, ok)
	}
	if err := q.Complete("unit"); err != nil {
		t.Fatalf("[%s] Complete(unit) err=%v want nil", runUUID, err)
	}
	t.Logf("[%s] after Complete: claimed=%d completed=%d unclaimed=%d", runUUID, q.Claimed(), q.Completed(), q.Unclaimed())

	wait, release := barrier()
	var lateWinners int64
	var ready, done sync.WaitGroup
	ready.Add(N)
	done.Add(N)
	for w := 0; w < N; w++ {
		go func() {
			defer done.Done()
			ready.Done()
			wait()
			if _, ok := q.Claim(); ok {
				atomic.AddInt64(&lateWinners, 1)
			}
		}()
	}
	ready.Wait()
	release()
	done.Wait()

	if lw := atomic.LoadInt64(&lateWinners); lw != 0 {
		t.Fatalf("[%s] %d worker(s) re-claimed a COMPLETED item — resurrection breaks mutual exclusion", runUUID, lw)
	}
	if c := q.Completed(); c != 1 {
		t.Fatalf("[%s] Completed()=%d want 1", runUUID, c)
	}
	t.Logf("[%s] completed item: %d late claimants all got ok=false (no resurrection)", runUUID, N)
}

// TestPoolCapacityAtMostOneHolder is the "no lost/duplicate across many items"
// property under genuine contention: M items, N>M workers all released from a
// barrier together. Each worker greedily drains. Invariants:
//   - total successful claims == M EXACTLY (no double-claim inflating past M,
//     no phantom),
//   - each item is held by AT MOST ONE worker (no id appears in two workers'
//     claim lists),
//   - N-M of the over-subscribed workers necessarily come up empty on at least
//     their final claim, but the pool is never over-drained.
//
// ANTI-BLUFF: a Claim that always returned success would let all N workers claim
// every item -> total would balloon to N*M and duplicates would appear; both
// the total==M and at-most-one-holder checks fail. The capacity ceiling is the
// tooth: you cannot hand out more than you put in.
func TestPoolCapacityAtMostOneHolder(t *testing.T) {
	const (
		M = 750 // items in the pool
		N = 64  // workers — deliberately N > M so workers fight over the tail
	)
	q := New()
	want := make(map[string]struct{}, M)
	for i := 0; i < M; i++ {
		id := fmt.Sprintf("item-%04d", i)
		q.Add(id)
		want[id] = struct{}{}
	}

	wait, release := barrier()
	perWorker := make([][]string, N)
	var ready, done sync.WaitGroup
	ready.Add(N)
	done.Add(N)
	for w := 0; w < N; w++ {
		go func(w int) {
			defer done.Done()
			ready.Done()
			wait() // all N start draining at the same instant
			for {
				id, ok := q.Claim()
				if !ok {
					return
				}
				perWorker[w] = append(perWorker[w], id)
			}
		}(w)
	}
	ready.Wait()
	release()
	done.Wait()

	holder := make(map[string]int) // id -> worker that holds it
	total := 0
	for w := 0; w < N; w++ {
		for _, id := range perWorker[w] {
			total++
			if prev, dup := holder[id]; dup {
				t.Fatalf("[%s] item %q held by BOTH worker %d and worker %d — at-most-one-holder violated", runUUID, id, prev, w)
			}
			holder[id] = w
			if _, known := want[id]; !known {
				t.Fatalf("[%s] phantom item %q claimed (never Added)", runUUID, id)
			}
		}
	}

	if total != M {
		t.Fatalf("[%s] total successful claims=%d want EXACTLY %d (capacity ceiling breached or items dropped)", runUUID, total, M)
	}
	if len(holder) != M {
		t.Fatalf("[%s] distinct held items=%d want %d", runUUID, len(holder), M)
	}
	for id := range want {
		if _, held := holder[id]; !held {
			t.Fatalf("[%s] item %q was dropped (never held)", runUUID, id)
		}
	}
	if q.Unclaimed() != 0 {
		t.Fatalf("[%s] Unclaimed()=%d want 0 after full drain", runUUID, q.Unclaimed())
	}
	if q.Claimed() != M {
		t.Fatalf("[%s] Claimed()=%d want %d after full drain", runUUID, q.Claimed(), M)
	}

	workersThatClaimed := 0
	for w := 0; w < N; w++ {
		if len(perWorker[w]) > 0 {
			workersThatClaimed++
		}
	}
	t.Logf("[%s] pool capacity: M=%d items, N=%d workers, total-claims=%d distinct=%d workers-with-work=%d — at most one holder per item",
		runUUID, M, N, total, len(holder), workersThatClaimed)
}

// TestNoLeaseExpiryModeled documents and asserts the HONEST absence of a
// lease/TTL in this Queue. There is no clock to advance and no expiry path, so
// a claimed-but-not-completed item stays claimed forever and is never
// re-offered. If a future change introduced a lease that silently expired a
// claim back to unclaimed WITHOUT the original holder releasing it, this test
// would catch the resurrection (a stale holder still "owning" while a second
// worker re-claims is exactly the two-claimant hazard). It is intentionally NOT
// a t.Skip: it asserts the current contract.
func TestNoLeaseExpiryModeled(t *testing.T) {
	q := New()
	q.Add("leased")
	id, ok := q.Claim()
	if !ok || id != "leased" {
		t.Fatalf("[%s] Claim got (%q,%v) want (\"leased\",true)", runUUID, id, ok)
	}
	// No clock, no sleep can change this: the claim does not expire. Re-Claim
	// must find nothing, and the item must remain owned (claimed), never
	// silently returned to the unclaimed pool.
	for i := 0; i < 10; i++ {
		if got, ok := q.Claim(); ok {
			t.Fatalf("[%s] claim #%d resurfaced %q — claim must NOT expire (no lease modeled)", runUUID, i, got)
		}
	}
	if c := q.Claimed(); c != 1 {
		t.Fatalf("[%s] Claimed()=%d want 1 (claim persists, no expiry)", runUUID, c)
	}
	if u := q.Unclaimed(); u != 0 {
		t.Fatalf("[%s] Unclaimed()=%d want 0 (item not returned to pool by any lease)", runUUID, u)
	}
	t.Logf("[%s] no lease/TTL modeled: claim persists, item never resurfaces without Complete (expiry test N/A by design)", runUUID)
}
