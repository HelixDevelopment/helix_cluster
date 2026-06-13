package lock

// lock_adversarial_test.go — REAL adversarial tests targeting the gaps the three
// existing test files leave open, with anti-bluff teeth and intended for -race.
//
// HONEST SCOPE NOTE (read before assuming a lease/fencing model):
//
//	pkg/lock's documented contract is a SINGLE method —
//	    Lock(ctx, key) (UnlockFunc, error)
//	MemoryLocker implements it as a per-key sync.Mutex held in a map. There is
//	NO lease/TTL, NO fencing token / epoch, NO renew/heartbeat, and NO owner
//	token in this package. Ownership is implicit in holding the sync.Mutex.
//	(The lease/session machinery referenced for the distributed case lives in
//	pkg/etcd — concurrency.NewSession + concurrency.NewMutex — which EtcdLocker
//	merely delegates to and which is OUT OF SCOPE here.)
//
//	Therefore the prompt's "lease expiry + fencing" (item 2) and "renew/
//	heartbeat" (item 3) are NOT MODELLED by pkg/lock; writing a fencing/expiry
//	test against an API that does not exist would be exactly the BLUFF this work
//	is meant to avoid. Instead we prove the STRONGEST real safety properties the
//	package actually offers and that the existing tests do NOT cover:
//
//	  GAP A (TestMemoryLockerMaxOneHolderUnderBarrier): genuine barrier-released
//	    concurrent contention with an ATOMIC max-holder gauge asserting at most
//	    ONE holder is inside the critical section at any instant (max == 1). The
//	    existing TestMemoryLockerConcurrent starts goroutines staggered (no
//	    barrier) and asserts via a non-atomic occupancy flag + lost-update
//	    counter; it has no max-concurrency gauge and is not -race-targeted.
//
//	  GAP B (TestMemoryLockerReleaseIsFireOnce / WrongOwner): release idempotency
//	    and the wrong-owner-release safety property. The returned UnlockFunc must
//	    release AT MOST ONCE: a duplicate/stale release must NOT (a) crash the
//	    process via Go's fatal "sync: unlock of unlocked mutex", nor (b) free a
//	    DIFFERENT owner's lock once the key's mutex has been re-acquired. This is
//	    the closest real analogue, in a fencing-less lock, of the classic
//	    distributed-lock bug where a stale owner corrupts the new owner's tenure.
//	    These properties are untested by the three existing files.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMemoryLockerMaxOneHolderUnderBarrier — GAP A.
//
// N goroutines are barrier-released simultaneously to Acquire the SAME lock; a
// shared atomic gauge counts how many are inside the critical section at once,
// and we assert the observed maximum is exactly 1 across the whole run. The
// outer loop repeats the barrier many times to give a non-exclusive lock many
// chances to overlap.
//
// ANTI-BLUFF: an always-grant / no-op lock lets ≥2 goroutines enter together,
// so `cur` reaches ≥2 and `maxHolders` records ≥2 → the test fails on the very
// first overlap. A non-atomic lost-update counter on the same critical section
// gives an independent second failure signal. Run with -race: simultaneous
// holders also produce a data race on the non-atomic `shared` write.
func TestMemoryLockerMaxOneHolderUnderBarrier(t *testing.T) {
	l := NewMemoryLocker()
	const key = "barrier-key"

	const (
		goroutines = 24
		rounds     = 40
	)

	var maxHolders int64 // atomic high-water mark of concurrent holders
	var shared int64     // non-atomic; correct ONLY under true exclusion

	for r := 0; r < rounds; r++ {
		var cur int64 // atomic live holder count for THIS round
		var wg sync.WaitGroup
		release := make(chan struct{}) // barrier: all goroutines wait here

		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-release // genuine simultaneous contention: everyone wakes together

				unlock, err := l.Lock(context.Background(), key)
				if err != nil {
					t.Errorf("Lock: %v", err)
					return
				}

				// --- critical section ---
				n := atomic.AddInt64(&cur, 1)
				// Record the high-water mark of concurrent holders.
				for {
					old := atomic.LoadInt64(&maxHolders)
					if n <= old || atomic.CompareAndSwapInt64(&maxHolders, old, n) {
						break
					}
				}
				// Non-atomic RMW: a lost update here is an independent proof of
				// overlap and a -race trip-wire if two holders run concurrently.
				v := shared
				shared = v + 1
				atomic.AddInt64(&cur, -1)
				// --- end critical section ---

				_ = unlock()
			}()
		}

		close(release) // fire the barrier
		wg.Wait()
	}

	if got := atomic.LoadInt64(&maxHolders); got != 1 {
		t.Fatalf("max concurrent holders = %d, want exactly 1 (lock not mutually exclusive)", got)
	}
	wantShared := int64(goroutines * rounds)
	if shared != wantShared {
		t.Fatalf("shared counter = %d, want %d (lost updates => lock not mutually exclusive)", shared, wantShared)
	}
	t.Logf("barrier-released %d goroutines x %d rounds: maxHolders=1, shared=%d (no overlap, no lost updates)",
		goroutines, rounds, shared)
}

// TestMemoryLockerReleaseIsFireOnce — GAP B (idempotency).
//
// The returned UnlockFunc must be safe to call more than once: the second call
// must be a no-op, not Go's FATAL "sync: unlock of unlocked mutex". That fatal
// is unrecoverable (recover() cannot catch it) and would crash the whole
// process — so we cannot probe it with recover(); we instead assert the
// observable consequence: after a double release, the key is STILL lockable
// exactly once (the mutex was not corrupted into a permanently-free or
// double-counted state) and a fresh holder gets genuine exclusion.
//
// ANTI-BLUFF: against the ORIGINAL unguarded `func(){ lm.Unlock() }` this test
// (and the wrong-owner test below) crash the test binary with a fatal runtime
// error — proving the sync.Once fix is load-bearing, not cosmetic. Under the
// fixed code the second release is an inert no-op and the assertions pass.
func TestMemoryLockerReleaseIsFireOnce(t *testing.T) {
	l := NewMemoryLocker()
	const key = "fire-once-key"

	unlock, err := l.Lock(context.Background(), key)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("first unlock: %v", err)
	}
	// Duplicate release: MUST be an inert no-op, not a fatal crash and not an
	// over-unlock that corrupts the mutex.
	if err := unlock(); err != nil {
		t.Fatalf("second (duplicate) unlock returned error: %v", err)
	}
	if err := unlock(); err != nil { // a third, for good measure
		t.Fatalf("third (duplicate) unlock returned error: %v", err)
	}

	// Sink-side proof the mutex is in a sane state: a fresh acquire succeeds and
	// still excludes. If the over-unlock had corrupted the mutex into a
	// permanently-acquirable state, the held-while-blocked check below would fail.
	freshUnlock, err := l.Lock(context.Background(), key)
	if err != nil {
		t.Fatalf("re-Lock after duplicate releases: %v", err)
	}

	gotIt := make(chan struct{})
	go func() {
		u, err := l.Lock(context.Background(), key)
		if err != nil {
			t.Errorf("contender Lock: %v", err)
			close(gotIt)
			return
		}
		close(gotIt)
		_ = u()
	}()

	// The contender MUST be blocked while freshUnlock's holder is active —
	// proving the duplicate releases did not leave the lock spuriously free.
	select {
	case <-gotIt:
		t.Fatal("a contender acquired while the fresh holder still held it: " +
			"duplicate release corrupted the lock into a non-exclusive state")
	case <-time.After(200 * time.Millisecond):
		// expected: contender is still blocked
	}

	if err := freshUnlock(); err != nil {
		t.Fatalf("fresh unlock: %v", err)
	}
	select {
	case <-gotIt:
		// good: contender proceeded only after the real holder released
	case <-time.After(5 * time.Second):
		t.Fatal("contender never acquired after fresh holder released")
	}
}

// TestMemoryLockerStaleReleaseDoesNotFreeNewOwner — GAP B (wrong-owner free).
//
// This is the safety property that most directly mirrors the distributed bug
// the prompt cares about: an owner whose tenure has ENDED must not be able to
// act (release) in a way that corrupts a DIFFERENT, current owner's tenure.
//
// Scenario:
//  1. A acquires the key and releases it (A's tenure is over).
//  2. B acquires the SAME key (B is now the rightful, exclusive owner).
//  3. A's UnlockFunc is invoked AGAIN (a stale/duplicate release).
//
// With the unguarded original, step 3's lm.Unlock() would free the very mutex B
// holds — A would be releasing B's lock — letting a third party C enter while B
// still believes it holds exclusive ownership. (In practice the original code's
// stale Unlock first hits Go's fatal over-unlock guard, which itself is a
// process-killing safety defect.) The fire-once UnlockFunc makes A's stale
// release a no-op, so B keeps exclusive ownership.
//
// ANTI-BLUFF: assert C CANNOT acquire while B holds, even after A's stale
// release fires. A lock that lets A's stale release free B's tenure would let C
// in → fail. The fire-once guard keeps C out.
func TestMemoryLockerStaleReleaseDoesNotFreeNewOwner(t *testing.T) {
	l := NewMemoryLocker()
	const key = "stale-owner-key"

	// (1) A's tenure: acquire then release.
	unlockA, err := l.Lock(context.Background(), key)
	if err != nil {
		t.Fatalf("A Lock: %v", err)
	}
	if err := unlockA(); err != nil {
		t.Fatalf("A first unlock: %v", err)
	}

	// (2) B becomes the rightful exclusive owner of the same key.
	unlockB, err := l.Lock(context.Background(), key)
	if err != nil {
		t.Fatalf("B Lock: %v", err)
	}

	// (3) A's STALE/duplicate release fires while B holds the lock. It must NOT
	//     free B's tenure (and must not fatally crash the process).
	if err := unlockA(); err != nil {
		t.Fatalf("A stale unlock returned error: %v", err)
	}

	// Anti-bluff assertion: a third party C must STILL be excluded while B holds,
	// proving A's stale release did not hand B's lock away.
	cIn := make(chan struct{})
	go func() {
		u, err := l.Lock(context.Background(), key)
		if err != nil {
			t.Errorf("C Lock: %v", err)
			close(cIn)
			return
		}
		close(cIn)
		_ = u()
	}()

	select {
	case <-cIn:
		t.Fatal("C acquired while B still held the lock: A's stale release freed " +
			"B's tenure (wrong-owner release broke mutual exclusion)")
	case <-time.After(300 * time.Millisecond):
		// expected: C is blocked because B genuinely still owns the lock
	}

	// B releases legitimately; only now may C proceed — confirming B held real,
	// uninterrupted exclusivity throughout A's stale release.
	if err := unlockB(); err != nil {
		t.Fatalf("B unlock: %v", err)
	}
	select {
	case <-cIn:
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("C never acquired after B released")
	}
}
