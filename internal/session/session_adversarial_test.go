// Package session — adversarial sink-side probes for the gRPC SessionService
// server wrapping pkg/session.Manager.
//
// Hunt brief (mirrors tonight's internal/node defect class):
//
//	(a) SHARED-POINTER MUTATION RACE — does any handler mutate a session pointer
//	    that is also visible to concurrent readers, OUTSIDE the manager's lock?
//	    internal/node had exactly this: a handler reached into a shared map value
//	    and mutated it in place while another goroutine read it -> gRPC data race.
//	(b) UNGUARDED SHARED STATE / lost update — concurrent CreateSession must not
//	    drop or clobber entries in the session map (conservation: all N present).
//	(c) FAIL-OPEN / EXPIRY — a terminated/unknown session must never be served as
//	    a live, valid session; a state transition must not be observed torn.
//	(d) IDENTITY / TOTAL-ORDER — session IDs must be unique (no clobber) and a
//	    filtered/identity listing must be internally consistent.
//
// SINK-SIDE DISCIPLINE: every probe asserts an observable end-user fact (no race
// report, conservation of the created set, the exact post-condition of a
// transition) — never merely "did not panic". These tests are called in-process
// (the Server holds no per-call state; the Manager is the single source of
// truth) and are capped at <=8 concurrent workers so `go test -race -timeout 90s`
// COMPLETES deterministically.
//
// FINDING (see final report): NO REAL BUG in internal/session. The Manager
// returns a deep copySession() from Create/Get/List/ListByOwner/Update and
// performs every mutation under m.mu while holding the lock, so the
// shared-pointer-mutation-race class is structurally absent here. These tests
// PIN that contract so a future regression (e.g. returning the live map pointer,
// or mutating outside the lock) is caught.
package session

import (
	"context"
	"fmt"
	"sync"
	"testing"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newInProcServer builds a Server with a nil backend (no real tmux process):
// these probes assert manager-level concurrency/identity/FSM semantics, not
// whether a PTY was launched, so the nil backend keeps them hermetic and fast.
func newInProcServer() *Server {
	return NewServerWithTmuxBackend(nil)
}

func mustCreate(t *testing.T, s *Server, name, owner string) *helixv1.Session {
	t.Helper()
	sess, err := s.CreateSession(context.Background(),
		&helixv1.CreateSessionRequest{Name: name, Owner: owner})
	if err != nil {
		t.Fatalf("CreateSession(%s,%s): %v", name, owner, err)
	}
	return sess
}

// ----------------------------------------------------------------------------
// (a)+(b) SHARED-POINTER MUTATION RACE + UNGUARDED SHARED STATE + CONSERVATION
// ----------------------------------------------------------------------------

// TestAdversarial_ConcurrentUpdateGetList_NoRace_NoLostUpdate is the headline
// probe for the internal/node defect class. It hammers a SINGLE shared session
// with concurrent UpdateSession (writer, mutates Status + Resources) against
// concurrent GetSession and ListSessions (readers) — exactly the access pattern
// that, in internal/node, exposed a handler mutating a shared map pointer in
// place outside the lock.
//
// SINK-SIDE assertions (not "no panic"):
//   - run under -race: ANY in-place mutation of a reader-visible pointer trips
//     the race detector and fails the test.
//   - every reader observes a self-consistent session: a status that is one of
//     the values the writers actually set (never a torn/garbage value), and the
//     immutable identity fields (Id/Name/Owner) never corrupt.
//   - after the storm, the final Get reflects one of the legal terminal writes.
//
// Mutation that makes this FAIL (under -race): if pkg/session.Manager.Update or
// Get returned the live map pointer instead of copySession(), the writer's
// in-place field writes would race the readers and -race would report it.
func TestAdversarial_ConcurrentUpdateGetList_NoRace_NoLostUpdate(t *testing.T) {
	s := newInProcServer()
	ctx := context.Background()

	target := mustCreate(t, s, "shared", "owner")
	id := target.Id

	// The set of statuses writers will cycle through. All are valid per
	// validStatus, so every Update must succeed and persist exactly one of them.
	statuses := []string{"running", "paused", "pending", "creating", "failed"}
	legal := map[string]bool{}
	for _, st := range statuses {
		legal[st] = true
	}

	const workers = 8       // cap concurrency <= 8 (ANTI-HANG)
	const itersPerWorker = 40

	var wg sync.WaitGroup
	errCh := make(chan error, workers*itersPerWorker)

	// Writers: mutate the shared session's Status + Resources.
	for w := 0; w < workers/2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < itersPerWorker; i++ {
				st := statuses[(w+i)%len(statuses)]
				_, err := s.UpdateSession(ctx, &helixv1.UpdateSessionRequest{
					SessionId: id,
					Status:    st,
					Resources: &helixv1.ResourceAllocation{
						CpuMillicores: int32(i),
						MemoryBytes:   int64(i) << 10,
						GpuIds:        []string{fmt.Sprintf("gpu-%d", i)},
					},
				})
				if err != nil {
					errCh <- fmt.Errorf("update: %w", err)
					return
				}
			}
		}(w)
	}

	// Readers: Get + List the shared session concurrently; assert self-consistency.
	for r := 0; r < workers/2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < itersPerWorker; i++ {
				got, err := s.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: id})
				if err != nil {
					errCh <- fmt.Errorf("get: %w", err)
					return
				}
				// Identity must never tear under concurrent writes.
				if got.Id != id || got.Name != "shared" || got.Owner != "owner" {
					errCh <- fmt.Errorf("torn identity: id=%s name=%s owner=%s",
						got.Id, got.Name, got.Owner)
					return
				}
				// Status must always be one of the writers' legal values.
				if !legal[got.Status] {
					errCh <- fmt.Errorf("torn status %q not in writer set", got.Status)
					return
				}
				if _, err := s.ListSessions(ctx, &helixv1.ListSessionsRequest{Owner: "owner"}); err != nil {
					errCh <- fmt.Errorf("list: %w", err)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent op failed: %v", err)
		}
	}

	// Final sink-side check: the surviving status is a legal write, and the
	// session is intact (single source of truth not corrupted).
	final, err := s.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: id})
	if err != nil {
		t.Fatalf("final Get: %v", err)
	}
	if !legal[final.Status] {
		t.Fatalf("final status %q not a legal writer value", final.Status)
	}
}

// TestAdversarial_ConcurrentCreate_Conservation proves UNGUARDED-SHARED-STATE
// safety on the session MAP itself: N concurrent CreateSession calls must yield
// exactly N distinct sessions — none lost to a map write race, none clobbered by
// a duplicate ID.
//
// SINK-SIDE assertions:
//   - exactly N sessions are listed back (conservation: no lost update on the map).
//   - all N IDs are unique (IDENTITY: no clobber).
//
// Mutation that makes this FAIL: removing the manager's m.mu.Lock() around the
// `m.sessions[id] = s` write makes concurrent map writes race (-race fails) and
// can drop entries so len != N.
func TestAdversarial_ConcurrentCreate_Conservation(t *testing.T) {
	s := newInProcServer()
	ctx := context.Background()

	const workers = 8
	const perWorker = 12
	const want = workers * perWorker

	var wg sync.WaitGroup
	idCh := make(chan string, want)
	errCh := make(chan error, want)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				sess, err := s.CreateSession(ctx, &helixv1.CreateSessionRequest{
					Name:  fmt.Sprintf("w%d-%d", w, i),
					Owner: "conserve",
				})
				if err != nil {
					errCh <- err
					return
				}
				idCh <- sess.Id
			}
		}(w)
	}
	wg.Wait()
	close(idCh)
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent create: %v", err)
		}
	}

	// IDENTITY: every returned ID is unique.
	seen := make(map[string]bool, want)
	for id := range idCh {
		if seen[id] {
			t.Fatalf("duplicate session ID returned: %s (identity clobber)", id)
		}
		seen[id] = true
	}
	if len(seen) != want {
		t.Fatalf("got %d unique created IDs, want %d", len(seen), want)
	}

	// CONSERVATION: all N created sessions are present in the manager.
	list, err := s.ListSessions(ctx, &helixv1.ListSessionsRequest{Owner: "conserve"})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list.Sessions) != want {
		t.Fatalf("conservation violated: listed %d, created %d", len(list.Sessions), want)
	}
	// Cross-check: every listed ID was one we created (no phantom entries).
	for _, sess := range list.Sessions {
		if !seen[sess.Id] {
			t.Fatalf("listed phantom session not in created set: %s", sess.Id)
		}
	}
}

// ----------------------------------------------------------------------------
// (c) FAIL-OPEN / EXPIRY — a terminated/unknown session must not serve as valid
// ----------------------------------------------------------------------------

// TestAdversarial_TerminatedSessionNotServedAsLive proves no FAIL-OPEN: once a
// session is deleted (terminated), it must NOT be retrievable as a live session,
// and a second terminate must be rejected (no silent double-terminate success).
//
// SINK-SIDE assertions:
//   - Get of a terminated session returns NotFound — it is not served as valid.
//   - It does not appear in a "running" status listing.
//
// Mutation that makes this FAIL: if DeleteSession returned success without the
// manager actually terminating (or if Get ignored the terminated state and
// returned the stale session), the post-delete Get would wrongly succeed.
func TestAdversarial_TerminatedSessionNotServedAsLive(t *testing.T) {
	s := newInProcServer()
	ctx := context.Background()

	created := mustCreate(t, s, "victim", "owner")
	if created.Status != "running" {
		t.Fatalf("precondition: created status = %s, want running", created.Status)
	}

	del, err := s.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: created.Id})
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if !del.Success {
		t.Fatalf("DeleteSession reported success=false")
	}

	// FAIL-OPEN probe: a terminated session must not be served as live.
	// pkg/session.Manager.Terminate transitions to StatusTerminated and the
	// session remains in the map; the gRPC Get still returns it, so the honest
	// sink-side fact is that its status is terminated (NOT running) and a second
	// terminate is rejected. We assert that contract precisely.
	got, err := s.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: created.Id})
	if err != nil {
		// If the impl chose to drop terminated sessions, NotFound is also a
		// correct non-fail-open outcome.
		if status.Code(err) != codes.NotFound {
			t.Fatalf("post-terminate Get error = %v, want NotFound or terminated status", err)
		}
	} else if got.Status == "running" {
		t.Fatalf("FAIL-OPEN: terminated session served as running")
	}

	// Double-terminate must be rejected (FSM guard), not silently succeed.
	_, err = s.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: created.Id})
	if err == nil {
		t.Fatalf("FAIL-OPEN: double-terminate succeeded; want rejection")
	}

	// A terminated session must not appear in a running-only listing.
	list, err := s.ListSessions(ctx, &helixv1.ListSessionsRequest{Owner: "owner", Status: "running"})
	if err != nil {
		t.Fatalf("ListSessions(running): %v", err)
	}
	for _, sess := range list.Sessions {
		if sess.Id == created.Id {
			t.Fatalf("FAIL-OPEN: terminated session %s listed as running", created.Id)
		}
	}
}

// TestAdversarial_UnknownSessionRejected proves an unknown ID is never served as
// a valid session through any read/write path (no fail-open on missing state).
//
// Mutation that makes this FAIL: if Get/Update/Delete returned a zero-value
// session with nil error for a missing ID, these NotFound asserts would flip.
func TestAdversarial_UnknownSessionRejected(t *testing.T) {
	s := newInProcServer()
	ctx := context.Background()
	const ghost = "session-this-id-never-existed"

	_, err := s.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: ghost})
	wantCodeLocal(t, err, codes.NotFound)

	_, err = s.UpdateSession(ctx, &helixv1.UpdateSessionRequest{SessionId: ghost, Status: "paused"})
	wantCodeLocal(t, err, codes.NotFound)

	_, err = s.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: ghost})
	wantCodeLocal(t, err, codes.NotFound)
}

// TestAdversarial_ConcurrentTerminateRaceExactlyOnceWinner proves the terminate
// transition is atomic under contention: many goroutines racing to DeleteSession
// the SAME session must yield EXACTLY ONE success — the rest must be rejected.
// This guards against a torn/fail-open transition where two callers both observe
// "not yet terminated" and both succeed.
//
// SINK-SIDE assertion: success count == 1 (single-winner), run under -race.
//
// Mutation that makes this FAIL: dropping the FSM validateTransition guard (or
// the lock) in Manager.Terminate would let multiple terminators win.
func TestAdversarial_ConcurrentTerminateRaceExactlyOnceWinner(t *testing.T) {
	s := newInProcServer()
	ctx := context.Background()

	created := mustCreate(t, s, "racing-terminate", "owner")

	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := s.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: created.Id})
			if err == nil && resp.Success {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("terminate exactly-once violated: %d successes, want 1", successes)
	}
}

// ----------------------------------------------------------------------------
// (d) IDENTITY / TOTAL-ORDER — owner filter is an exact partition, no clobber
// ----------------------------------------------------------------------------

// TestAdversarial_OwnerFilterPartitionIdentity proves the owner filter is an
// exact, non-overlapping partition: a session created for owner A is never
// served under a ListByOwner(B) query, and counts are conserved per owner.
// This catches an identity clobber where two owners' sessions share/leak state.
//
// Mutation that makes this FAIL: if ListByOwner ignored the owner predicate (or
// CreateSession dropped the Owner field), B's listing would include A's sessions.
func TestAdversarial_OwnerFilterPartitionIdentity(t *testing.T) {
	s := newInProcServer()
	ctx := context.Background()

	const aCount, bCount = 5, 7
	for i := 0; i < aCount; i++ {
		mustCreate(t, s, fmt.Sprintf("a-%d", i), "alice")
	}
	for i := 0; i < bCount; i++ {
		mustCreate(t, s, fmt.Sprintf("b-%d", i), "bob")
	}

	la, err := s.ListSessions(ctx, &helixv1.ListSessionsRequest{Owner: "alice"})
	if err != nil {
		t.Fatalf("ListSessions(alice): %v", err)
	}
	lb, err := s.ListSessions(ctx, &helixv1.ListSessionsRequest{Owner: "bob"})
	if err != nil {
		t.Fatalf("ListSessions(bob): %v", err)
	}

	if len(la.Sessions) != aCount {
		t.Fatalf("alice owns %d sessions, want %d", len(la.Sessions), aCount)
	}
	if len(lb.Sessions) != bCount {
		t.Fatalf("bob owns %d sessions, want %d", len(lb.Sessions), bCount)
	}
	for _, sess := range la.Sessions {
		if sess.Owner != "alice" {
			t.Fatalf("IDENTITY leak: alice listing contains owner=%s", sess.Owner)
		}
	}
	for _, sess := range lb.Sessions {
		if sess.Owner != "bob" {
			t.Fatalf("IDENTITY leak: bob listing contains owner=%s", sess.Owner)
		}
	}
}

// wantCodeLocal is a local copy of the gRPC code asserter (the one in
// server_test.go is named wantCode; this avoids any coupling and keeps this file
// self-contained for review).
func wantCodeLocal(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	if got := status.Code(err); got != want {
		t.Fatalf("error code = %s, want %s (err: %v)", got, want, err)
	}
}
