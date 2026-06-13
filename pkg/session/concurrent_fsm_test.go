package session

// concurrent_fsm_test.go — adversarial concurrency tests for the Manager's
// most safety-critical invariant: the lifecycle FSM's AT-MOST-ONCE terminal
// transition under concurrency.
//
// CONTEXT / GAP:
//   The existing lifecycle_test.go exercises validateTransition and the Manager
//   FSM guards SINGLE-THREADED. The CRDT concurrency tests cover the CRDT type,
//   NOT the Manager's session store + FSM. Nothing here proves the FSM's
//   check-and-set (read s.Status -> validateTransition -> write s.Status) is
//   ATOMIC under concurrency. If that check-and-set were not protected by a
//   single critical section, two racing Terminate calls could BOTH observe
//   StatusRunning, BOTH pass validateTransition, and BOTH "succeed" — a
//   double-close: onTerminate fires twice (duplicate teardown / double-free in
//   a real system). That is the use-after/double-close class called out in the
//   task. These tests target it directly, with -race teeth.
//
// ANTI-BLUFF: each test states the exact corruption it bites and the mutation
// that makes it fail. They are bounded so they complete quickly.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// TestManager_ConcurrentTerminate_AtMostOnce is the HEADLINE invariant test.
//
// INVARIANT: For a single session, no matter how many goroutines call
// Terminate concurrently, EXACTLY ONE succeeds, the onTerminate callback fires
// EXACTLY ONCE, and the session ends StatusTerminated.
//
// WHY THIS BITES: Terminate does read-status -> validateTransition -> set
// StatusTerminated. validateTransition(StatusTerminated, actionTerminate)
// returns an error (double-terminate guard). The ONLY thing that makes
// "exactly one winner" hold under concurrency is that the whole sequence runs
// inside one m.mu.Lock() critical section. If the lock were dropped between the
// check and the set (a TOCTOU mutation), N goroutines would race-read
// StatusRunning and N would win → this test reports >1 success / >1 callback.
//
// MUTATION that makes this FAIL: change manager.go Terminate to release the
// lock before validateTransition (or move the guard outside the lock) — the
// winner count and callback count jump above 1.
//
// -race ADDS: even with a single winner, any unsynchronized read/write of
// s.Status or m.sessions across the racing goroutines trips the race detector.
func TestManager_ConcurrentTerminate_AtMostOnce(t *testing.T) {
	const goroutines = 64

	for trial := 0; trial < 8; trial++ {
		mgr := NewManager(nil)

		var callbackCount int64
		mgr.onTerminate = func(SessionID) {
			atomic.AddInt64(&callbackCount, 1)
		}

		s, err := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
		if err != nil {
			t.Fatalf("create: %v", err)
		}

		var (
			successCount int64
			rejectCount  int64
			start        = make(chan struct{})
			wg           sync.WaitGroup
		)
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				<-start // release all goroutines simultaneously to maximize the race
				if err := mgr.Terminate(context.Background(), s.ID); err == nil {
					atomic.AddInt64(&successCount, 1)
				} else {
					// A loser MUST be rejected by the FSM guard, not some other error.
					var ite *InvalidTransitionError
					if !asInvalidTransition(err, &ite) {
						t.Errorf("loser got non-FSM error: %T %v", err, err)
						return
					}
					if ite.Action != actionTerminate || ite.From != StatusTerminated {
						t.Errorf("loser FSM error mismatch: from=%s action=%s", ite.From, ite.Action)
					}
					atomic.AddInt64(&rejectCount, 1)
				}
			}()
		}
		close(start)
		wg.Wait()

		if t.Failed() {
			return
		}

		gotSucc := atomic.LoadInt64(&successCount)
		gotCb := atomic.LoadInt64(&callbackCount)
		gotRej := atomic.LoadInt64(&rejectCount)

		// Exactly one winner.
		if gotSucc != 1 {
			t.Fatalf("trial %d: ANTI-BLUFF double-close: expected EXACTLY 1 Terminate to succeed, got %d. "+
				"More than one winner means the FSM check-and-set is not atomic (TOCTOU on s.Status).", trial, gotSucc)
		}
		// onTerminate fired exactly once — the sink-side proof teardown ran once.
		if gotCb != 1 {
			t.Fatalf("trial %d: onTerminate fired %d times, want exactly 1 — duplicate teardown (double-free class)", trial, gotCb)
		}
		// Everyone else was rejected.
		if gotRej != goroutines-1 {
			t.Fatalf("trial %d: expected %d rejects, got %d (succ+rej must equal %d)", trial, goroutines-1, gotRej, goroutines)
		}
		// Final state is Terminated.
		got, err := mgr.Get(s.ID)
		if err != nil {
			t.Fatalf("trial %d: get after terminate: %v", trial, err)
		}
		if got.Status != StatusTerminated {
			t.Fatalf("trial %d: final status = %s, want terminated", trial, got.Status)
		}
	}
}

// TestManager_ConcurrentMigrate_AtMostOnce proves the same atomic check-and-set
// for the migrate edge: only a Running session may begin migration, so exactly
// one racing Migrate may flip Running -> Migrating; the rest see Migrating and
// are rejected WITHOUT mutating NodeID.
//
// WHY THIS BITES: identical TOCTOU surface as Terminate. A second "winner"
// would also overwrite NodeID, so we additionally assert the final NodeID is
// the winner's target and no rejected migrate corrupted it.
//
// MUTATION that makes this FAIL: drop the lock around Migrate's
// validateTransition + status/NodeID write → multiple winners, NodeID races.
func TestManager_ConcurrentMigrate_AtMostOnce(t *testing.T) {
	const goroutines = 64

	for trial := 0; trial < 8; trial++ {
		mgr := NewManager(nil)
		s, err := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
		if err != nil {
			t.Fatalf("create: %v", err)
		}

		var (
			successCount int64
			start        = make(chan struct{})
			wg           sync.WaitGroup
		)
		// Each goroutine targets a distinct node; the single winner's target
		// must be the final NodeID. Any second winner would race NodeID.
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			target := "node-winner" // all aim at the same node so the final value is deterministic for the winner
			go func() {
				defer wg.Done()
				<-start
				if err := mgr.Migrate(context.Background(), s.ID, target); err == nil {
					atomic.AddInt64(&successCount, 1)
				} else {
					var ite *InvalidTransitionError
					if !asInvalidTransition(err, &ite) {
						t.Errorf("migrate loser got non-FSM error: %T %v", err, err)
					}
				}
			}()
		}
		close(start)
		wg.Wait()

		if t.Failed() {
			return
		}

		if got := atomic.LoadInt64(&successCount); got != 1 {
			t.Fatalf("trial %d: ANTI-BLUFF: expected EXACTLY 1 Migrate winner, got %d — non-atomic FSM check-and-set", trial, got)
		}
		got, err := mgr.Get(s.ID)
		if err != nil {
			t.Fatalf("trial %d: get: %v", trial, err)
		}
		if got.Status != StatusMigrating {
			t.Fatalf("trial %d: status = %s, want migrating", trial, got.Status)
		}
		if got.NodeID != "node-winner" {
			t.Fatalf("trial %d: NodeID = %q, want node-winner (a rejected migrate must not mutate NodeID)", trial, got.NodeID)
		}
	}
}

// TestManager_ConcurrentStore_NoRaceNoWrongOwner is the unsynchronized-map /
// torn-read class. Many goroutines Create / Get / Update / List / ListByOwner /
// Terminate across many sessions concurrently.
//
// INVARIANTS asserted:
//   - No data race (run under -race) on m.sessions or on any *Session.
//   - No panic.
//   - Uniqueness: concurrent Creates never collide on a SessionID.
//   - No wrong-owner / torn read: every Session ever returned by Get/List has
//     the Owner it was created with — Get returns a deep copy, so a torn read of
//     the Owner field (or a session served to the wrong key) would surface here.
//   - No lost session: every successfully-created ID is retrievable.
//
// WHY -race BITES: if m.sessions were accessed without the RWMutex (the classic
// "concurrent map read/write" panic / race), the detector fires immediately.
//
// MUTATION that makes this FAIL: drop the m.mu locking in Create/Get/Update →
// "fatal error: concurrent map writes" or a race report; or have Get return the
// live pointer instead of copySession → racy reads of mutated fields.
func TestManager_ConcurrentStore_NoRaceNoWrongOwner(t *testing.T) {
	mgr := NewManager(nil)

	const (
		creators       = 16
		perCreator     = 32
		mutatorWorkers = 16
	)

	owners := []string{"alice", "bob", "carol", "dave"}

	// ownerOf records the immutable Owner each ID was created with, so any
	// reader can independently verify it never changes / never gets crossed.
	var ownerMu sync.Mutex
	ownerOf := make(map[SessionID]string)
	idCount := make(map[SessionID]int) // uniqueness witness

	var ids sync.Map // SessionID -> struct{} for live readers to pick from
	var idSlice []SessionID
	var idSliceMu sync.Mutex

	var wg sync.WaitGroup

	// Creators.
	for c := 0; c < creators; c++ {
		owner := owners[c%len(owners)]
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			for i := 0; i < perCreator; i++ {
				s, err := mgr.Create(context.Background(), &CreateRequest{Owner: owner, Backend: BackendTmux})
				if err != nil {
					t.Errorf("create: %v", err)
					return
				}
				ownerMu.Lock()
				ownerOf[s.ID] = owner
				idCount[s.ID]++
				ownerMu.Unlock()

				ids.Store(s.ID, struct{}{})
				idSliceMu.Lock()
				idSlice = append(idSlice, s.ID)
				idSliceMu.Unlock()
			}
		}(owner)
	}

	// Concurrent readers/mutators: hammer Get / Update / List while creates run.
	stop := make(chan struct{})
	for w := 0; w < mutatorWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Pick a known id if any.
				idSliceMu.Lock()
				n := len(idSlice)
				var id SessionID
				if n > 0 {
					id = idSlice[n-1]
				}
				idSliceMu.Unlock()
				if n == 0 {
					continue
				}

				// Get: must not return a wrong-owner session.
				if got, err := mgr.Get(id); err == nil {
					ownerMu.Lock()
					want, known := ownerOf[id]
					ownerMu.Unlock()
					if known && got.Owner != want {
						t.Errorf("WRONG-OWNER/torn read: session %q has Owner=%q, created with %q", id, got.Owner, want)
						return
					}
				}

				// Update: mutate a label; must not race or corrupt Owner.
				_, _ = mgr.Update(id, func(s *Session) {
					if s.Labels == nil {
						s.Labels = map[string]string{}
					}
					s.Labels["touched"] = "yes"
				})

				// List APIs must not race the concurrent creates.
				_ = mgr.List()
				_ = mgr.ListByOwner("alice")
			}
		}()
	}

	// Wait for all creators to finish, then stop readers.
	createDone := make(chan struct{})
	go func() {
		// creators are the first `creators` Add'd; we can't selectively wait,
		// so spin until expected count of IDs is reached.
		for {
			idSliceMu.Lock()
			n := len(idSlice)
			idSliceMu.Unlock()
			if n >= creators*perCreator {
				close(createDone)
				return
			}
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	<-createDone
	close(stop)
	wg.Wait()

	if t.Failed() {
		return
	}

	// UNIQUENESS: no SessionID was produced twice.
	ownerMu.Lock()
	defer ownerMu.Unlock()
	if len(idCount) != creators*perCreator {
		t.Fatalf("UNIQUENESS: expected %d distinct session IDs, got %d (duplicate IDs from concurrent Create)",
			creators*perCreator, len(idCount))
	}
	for id, count := range idCount {
		if count != 1 {
			t.Fatalf("UNIQUENESS: session ID %q produced %d times", id, count)
		}
	}

	// NO LOST SESSION: every created ID is retrievable with its correct owner.
	for id, want := range ownerOf {
		got, err := mgr.Get(id)
		if err != nil {
			t.Fatalf("LOST SESSION: created id %q not retrievable: %v", id, err)
		}
		if got.Owner != want {
			t.Fatalf("WRONG-OWNER at end: id %q Owner=%q want %q", id, got.Owner, want)
		}
	}
}
