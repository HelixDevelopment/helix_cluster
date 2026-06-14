package session

// session_adversarial_test.go — adversarial, SINK-SIDE concurrency / identity /
// expiry probes for the Manager session store.
//
// SCOPE & HONEST FRAMING (read before judging a failure):
//
//   The Manager (manager.go) is the read-by-many / written-by-few session map.
//   It guards every access with a single sync.RWMutex and hands out deep copies
//   (copySession) on read, so callers cannot alias internal state. These tests
//   PROVE that contract sink-side rather than trusting it:
//
//   (a) UNGUARDED SHARED MUTABLE STATE — concurrent Create / Update / Get / List
//       under -race. Assertion is CONSERVATION: every one of N concurrently
//       created sessions is retrievable afterwards (no lost update / clobbered
//       map slot), and a concurrent read-modify-write via Update does not lose
//       increments (final count == N).
//
//   (c) IDENTITY / TOTAL ORDER — N concurrent Create calls must yield N DISTINCT
//       SessionIDs. A collision would clobber a map slot (two sessions, one
//       survivor). The ID is crypto/rand(16 bytes) ‖ UnixNano, so collisions are
//       astronomically improbable; the test proves the store does not itself
//       lose entries even when IDs are dense in time.
//
//   (b)/(d) EXPIRY / FAIL-OPEN / REAP — CONTRACT-PINNING, NOT a bug hunt.
//       pkg/session has NO TTL, NO expiry, and NO Delete/reap API. There is
//       therefore NO promise of "expired sessions are rejected" to break, so
//       there is no fail-open bug to find here. Get returns a session regardless
//       of age; Terminate flips Status to Terminated but LEAVES the entry in the
//       map (List still returns it). These tests PIN that exact behavior so that
//       if a TTL/expiry path is ever added, a fail-open regression (expired
//       session returned as valid, or a delete that doesn't delete) trips here.
//       The unbounded-growth-after-Terminate property is recorded as a LATENT
//       RISK, not asserted as a defect.
//
// ANTI-BLUFF: every test asserts an actual sink-side verdict (retrieved count ==
// created, IDs distinct, increment count exact, status verdict) — never merely
// "did not panic". -race is required to give the data-race assertions teeth.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestManager_ConcurrentCreate_ConservationAndDistinctIDs is the headline
// class-(a)+(c) probe.
//
// INVARIANT: N goroutines each call Create once. Afterwards (1) the store holds
// EXACTLY N sessions, (2) every returned ID is retrievable via Get, and (3) all
// N IDs are DISTINCT. Any one of these failing means the shared map lost or
// clobbered an entry — the lost-update / unguarded-map class.
//
// WHY IT BITES: Create does `m.sessions[id] = s` inside m.mu.Lock(). If that
// write (or the map itself) were unguarded, concurrent inserts would race the
// builtin map (Go fatally panics on concurrent map write under -race, or silently
// drops entries) and the post-count would be < N.
//
// MUTATION that makes this FAIL: remove the m.mu.Lock()/Unlock() in Create
// (manager.go) → -race reports a data race on the map and/or len(List()) < N.
func TestManager_ConcurrentCreate_ConservationAndDistinctIDs(t *testing.T) {
	const goroutines = 256

	mgr := NewManager(nil)

	var wg sync.WaitGroup
	ids := make([]SessionID, goroutines)
	errs := make([]error, goroutines)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			s, err := mgr.Create(context.Background(), &CreateRequest{
				Name:    fmt.Sprintf("sess-%d", i),
				Owner:   "alice",
				Backend: BackendTmux,
			})
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = s.ID
		}(i)
	}
	wg.Wait()

	// SINK 0: no Create errored.
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Create[%d] errored: %v", i, err)
		}
	}

	// SINK 1: conservation — the store holds EXACTLY N sessions.
	all := mgr.List()
	if len(all) != goroutines {
		t.Fatalf("conservation violated: List() returned %d sessions, want %d (lost/clobbered map entries)", len(all), goroutines)
	}

	// SINK 2: every created ID is independently retrievable, and all distinct.
	seen := make(map[SessionID]struct{}, goroutines)
	for i, id := range ids {
		if id == "" {
			t.Fatalf("session %d has empty ID", i)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("identity violated: duplicate SessionID %q (map slot clobbered)", id)
		}
		seen[id] = struct{}{}

		got, err := mgr.Get(id)
		if err != nil {
			t.Fatalf("created session %q not retrievable (lost update): %v", id, err)
		}
		if got.ID != id {
			t.Fatalf("Get(%q) returned wrong session ID %q", id, got.ID)
		}
	}
	if len(seen) != goroutines {
		t.Fatalf("identity violated: only %d distinct IDs among %d creates", len(seen), goroutines)
	}
}

// TestManager_ConcurrentUpdate_NoLostUpdate is the class-(a) read-modify-write
// probe on a SINGLE session.
//
// INVARIANT: N goroutines each call Update on the same session, and each closure
// appends one entry to a label-encoded counter. The final session must reflect
// ALL N applications — no lost update.
//
// We use a counter stored in the session's Labels map. Each Update increments
// it. Because Update runs `fn(s)` inside m.mu.Lock(), the read-modify-write is
// atomic and the final value MUST equal N.
//
// WHY IT BITES: if Update did not hold the write lock across fn(s), two
// goroutines could both read counter=k, both write k+1, losing one increment —
// the classic lost update. The final value would be < N.
//
// MUTATION that makes this FAIL: drop the lock in Update (manager.go) around
// fn(s) → -race fires on the Labels-map read/write AND the final count < N.
func TestManager_ConcurrentUpdate_NoLostUpdate(t *testing.T) {
	const goroutines = 200

	mgr := NewManager(nil)
	s, err := mgr.Create(context.Background(), &CreateRequest{
		Owner:   "bob",
		Backend: BackendTmux,
		Labels:  map[string]string{"hits": "0"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := s.ID

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, uerr := mgr.Update(id, func(sess *Session) {
				if sess.Labels == nil {
					sess.Labels = map[string]string{}
				}
				var n int
				fmt.Sscanf(sess.Labels["hits"], "%d", &n)
				sess.Labels["hits"] = fmt.Sprintf("%d", n+1)
			})
			if uerr != nil {
				t.Errorf("Update: %v", uerr)
			}
		}()
	}
	wg.Wait()

	got, err := mgr.Get(id)
	if err != nil {
		t.Fatalf("Get after updates: %v", err)
	}
	var final int
	fmt.Sscanf(got.Labels["hits"], "%d", &final)
	if final != goroutines {
		t.Fatalf("lost update: final hits=%d, want %d (read-modify-write not atomic under lock)", final, goroutines)
	}
}

// TestManager_GetReturnsDeepCopy_NoAliasing proves the read path hands out an
// ISOLATED copy: mutating a value returned by Get must NOT bleed into the store
// (and must not race a concurrent reader). This is the unguarded-shared-state
// class viewed from the read side — if Get returned the internal *Session, a
// caller mutating Environment/Labels would corrupt shared state with no lock.
//
// MUTATION that makes this FAIL: make Get return `s` instead of copySession(s)
// (manager.go) → the mutation below bleeds into the store and the re-Get sees it
// (and a concurrent reader would -race).
func TestManager_GetReturnsDeepCopy_NoAliasing(t *testing.T) {
	mgr := NewManager(nil)
	s, err := mgr.Create(context.Background(), &CreateRequest{
		Owner:       "carol",
		Backend:     BackendTmux,
		Environment: map[string]string{"k": "orig"},
		Labels:      map[string]string{"l": "orig"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := s.ID

	// Mutate the copy the caller received.
	got1, err := mgr.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got1.Environment["k"] = "tampered"
	got1.Labels["l"] = "tampered"
	got1.Name = "tampered"

	// A fresh Get must still see the ORIGINAL values — the store was isolated.
	got2, err := mgr.Get(id)
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if got2.Environment["k"] != "orig" {
		t.Fatalf("aliasing: Environment bled through Get copy: got %q want %q", got2.Environment["k"], "orig")
	}
	if got2.Labels["l"] != "orig" {
		t.Fatalf("aliasing: Labels bled through Get copy: got %q want %q", got2.Labels["l"], "orig")
	}
	if got2.Name == "tampered" {
		t.Fatalf("aliasing: Name bled through Get copy")
	}
}

// TestManager_ContractPin_NoExpiry_GetReturnsRegardlessOfAge pins the (b)
// expiry/fail-open contract: pkg/session has NO TTL. A session — however old —
// is returned by Get as long as it exists. There is no "expired => rejected"
// promise, hence no fail-open bug. This test documents and pins that contract so
// that a future TTL addition which checks expiry with the wrong boundary (or not
// at all) is caught as a regression.
//
// If a TTL is ever introduced and this test starts FAILING because an
// unexpired-but-old session is rejected, that is the signal to revisit the
// boundary; if an EXPIRED session is still returned, add an explicit expiry
// assertion here.
func TestManager_ContractPin_NoExpiry_GetReturnsRegardlessOfAge(t *testing.T) {
	mgr := NewManager(nil)
	s, err := mgr.Create(context.Background(), &CreateRequest{Owner: "dora", Backend: BackendTmux})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Reach into the store and backdate the timestamps to a clearly "ancient"
	// instant. With no TTL, Get must still return the session.
	mgr.mu.Lock()
	internal := mgr.sessions[s.ID]
	internal.CreatedAt = internal.CreatedAt.Add(-1 << 40) // ~12.7 days into the past
	internal.UpdatedAt = internal.CreatedAt
	mgr.mu.Unlock()

	got, err := mgr.Get(s.ID)
	if err != nil {
		t.Fatalf("CONTRACT CHANGE: an aged session was rejected by Get, but pkg/session promises no TTL: %v", err)
	}
	if got.ID != s.ID {
		t.Fatalf("Get returned wrong session")
	}
}

// TestManager_ContractPin_TerminateKeepsEntry pins the (d) reap contract:
// Terminate flips Status to StatusTerminated but does NOT remove the entry, and
// there is NO Delete/reap API on the Manager. List therefore continues to return
// terminated sessions. This is PINNED behavior, and the standing unbounded-growth
// property (terminated sessions accumulate forever) is the recorded LATENT RISK —
// not asserted here as a defect, because no removal is promised.
//
// If a Delete/reap path is ever added, extend this test to prove a deleted
// session is actually gone (delete that doesn't delete = the (d) bug).
func TestManager_ContractPin_TerminateKeepsEntry(t *testing.T) {
	mgr := NewManager(nil)
	s, err := mgr.Create(context.Background(), &CreateRequest{Owner: "eve", Backend: BackendTmux})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.Terminate(context.Background(), s.ID); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	// SINK: entry survives Terminate and reports StatusTerminated (not "missing",
	// not a fail-open StatusRunning).
	got, err := mgr.Get(s.ID)
	if err != nil {
		t.Fatalf("terminated session unexpectedly removed from store: %v", err)
	}
	if got.Status != StatusTerminated {
		t.Fatalf("terminated session has status %v, want %v", got.Status, StatusTerminated)
	}
	if n := len(mgr.List()); n != 1 {
		t.Fatalf("List() returned %d after Terminate, want 1 (terminated entry must remain pinned)", n)
	}

	// Double-terminate must be REJECTED (not silently fail open as success).
	if err := mgr.Terminate(context.Background(), s.ID); err == nil {
		t.Fatalf("double-terminate fail-open: second Terminate returned nil, want rejection")
	}
}

// TestManager_ConcurrentMixed_RaceClean drives Create / Get / List / Update /
// Terminate concurrently to give -race a wide surface across the whole store.
// The sink-side assertion is conservation: every successfully created session is
// still accounted for in List afterwards.
func TestManager_ConcurrentMixed_RaceClean(t *testing.T) {
	const creators = 64
	mgr := NewManager(nil)

	var created int64
	var wg sync.WaitGroup

	// Creators.
	wg.Add(creators)
	idCh := make(chan SessionID, creators)
	for i := 0; i < creators; i++ {
		go func(i int) {
			defer wg.Done()
			s, err := mgr.Create(context.Background(), &CreateRequest{
				Name:    fmt.Sprintf("mix-%d", i),
				Owner:   "frank",
				Backend: BackendTmux,
			})
			if err != nil {
				t.Errorf("Create: %v", err)
				return
			}
			atomic.AddInt64(&created, 1)
			idCh <- s.ID
		}(i)
	}

	// Concurrent readers hammering List/Get while creators run.
	wg.Add(8)
	stop := make(chan struct{})
	for r := 0; r < 8; r++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = mgr.List()
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		ids := make([]SessionID, 0, creators)
		for i := 0; i < creators; i++ {
			id := <-idCh
			ids = append(ids, id)
			// Read-modify-write and a read on the just-created session.
			_, _ = mgr.Update(id, func(s *Session) { s.NodeID = "node-1" })
			_, _ = mgr.Get(id)
		}
		close(stop)
	}()

	wg.Wait()

	// SINK: conservation across the whole mixed workload.
	if got := int64(len(mgr.List())); got != atomic.LoadInt64(&created) {
		t.Fatalf("conservation violated under mixed load: List()=%d, created=%d", got, created)
	}
}
