// Package session — second-wave adversarial sink-side probes for the gRPC
// SessionService server wrapping pkg/session.Manager.
//
// This wave targets LIFECYCLE / FSM correctness at the public gRPC surface that
// the first wave (session_adversarial_test.go) did not cover. The first wave
// pinned the pointer/map-aliasing class (Manager returns deep copies; mutations
// are under lock) and proved it structurally absent. Here we hunt the dual hole:
// state-machine FAIL-OPEN through UpdateSession.
//
// HEADLINE FINDING (REAL BUG, fixed in server.go UpdateSession):
//
//	A terminated session could be RESURRECTED to "running" via UpdateSession.
//	pkg/session.Manager.Update has NO FSM guard — it blindly applies the mutator —
//	while Terminate/Migrate/Attach/Detach all validate transitions. Terminate has
//	already killed the backend process, so flipping the status back to "running"
//	reports a live session with no real process (a CLAUDE-1 status bluff) AND
//	defeats the double-terminate guard (un-terminate, then terminate again). The
//	fix lives in the server handler (the only layer this wave may edit): the
//	mutator refuses, under the manager lock, to move a terminated session to any
//	non-terminated status, and the handler returns FailedPrecondition.
//
// SINK-SIDE DISCIPLINE: every probe asserts an observable end-user fact (the
// post-condition status actually stored, the exact gRPC code, conservation of
// resource fields) — never merely "did not panic". All probes are in-process and
// non-blocking so `go test -race` completes deterministically.
package session

import (
	"context"
	"sync"
	"testing"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"google.golang.org/grpc/codes"
)

// TestAdversarial2_TerminatedSessionCannotBeResurrected is the mutation-proving
// probe for the headline FSM fail-open. It terminates a session, then attempts
// every non-terminated status transition via UpdateSession and asserts each is
// rejected with FailedPrecondition AND that the stored status stays "terminated".
//
// SINK-SIDE assertions:
//   - UpdateSession(..., Status: <live>) on a terminated session returns
//     FailedPrecondition (not nil, not a silent success).
//   - A fresh Get still reports "terminated" — the session was NOT revived.
//   - The double-terminate guard remains effective afterward (the session is
//     genuinely still terminal).
//
// Mutation that makes this FAIL (and DID, on the pre-fix code): remove the
// `resurrection`/terminated-state guard in server.go UpdateSession. Then
// StatusFromString("running") is applied to the terminated session, the call
// succeeds with nil error, and the follow-up Get reports "running" — exactly the
// resurrection this test forbids. Verified: pre-fix this asserts FAIL; post-fix
// it passes (ok).
func TestAdversarial2_TerminatedSessionCannotBeResurrected(t *testing.T) {
	s := newInProcServer()
	ctx := context.Background()

	created := mustCreate(t, s, "zombie", "owner")
	if created.Status != "running" {
		t.Fatalf("precondition: created status = %s, want running", created.Status)
	}

	if _, err := s.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: created.Id}); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	// Confirm it is terminated before we try to revive it.
	pre, err := s.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: created.Id})
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if pre.Status != "terminated" {
		t.Fatalf("precondition: post-delete status = %s, want terminated", pre.Status)
	}

	// Every attempt to move it to a non-terminated status must be refused.
	for _, live := range []string{"running", "pending", "creating", "paused", "migrating", "failed"} {
		_, err := s.UpdateSession(ctx, &helixv1.UpdateSessionRequest{
			SessionId: created.Id,
			Status:    live,
		})
		wantCodeLocal(t, err, codes.FailedPrecondition)

		// Sink-side: the session is NOT revived; it remains terminated.
		got, err := s.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: created.Id})
		if err != nil {
			t.Fatalf("Get after resurrection attempt (%s): %v", live, err)
		}
		if got.Status != "terminated" {
			t.Fatalf("FSM FAIL-OPEN: terminated session revived to %q via Update(%s)", got.Status, live)
		}
	}

	// The double-terminate guard must still hold — the session is genuinely terminal.
	if _, err := s.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: created.Id}); err == nil {
		t.Fatalf("double-terminate succeeded after resurrection attempts; FSM corrupted")
	}
}

// TestAdversarial2_TerminatedSessionResourceUpdateAlsoRefused proves the guard is
// not bypassable by smuggling a status change alongside a resource update, and
// that a resource-only update (no status) does NOT accidentally mutate a
// terminated session's resources either (the mutator returns early on a refused
// status, but a resource-only request must be evaluated on its own merits).
//
// CONTRACT pinned: on a terminated session,
//   - Status: "running" + Resources -> FailedPrecondition, nothing applied.
//   - Resources only (no status) -> allowed (status unchanged, stays terminated),
//     because updating bookkeeping/resource fields of a terminal record is benign
//     and does not falsely advertise a live process.
//
// Mutation that makes this FAIL: if the guard keyed only on Resources==nil, or
// applied the status before checking, the combined request would revive the
// session.
func TestAdversarial2_TerminatedSessionResourceUpdateAlsoRefused(t *testing.T) {
	s := newInProcServer()
	ctx := context.Background()

	created := mustCreate(t, s, "zombie2", "owner")
	if _, err := s.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: created.Id}); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	// Status revival smuggled with a resource update must still be refused whole.
	_, err := s.UpdateSession(ctx, &helixv1.UpdateSessionRequest{
		SessionId: created.Id,
		Status:    "running",
		Resources: &helixv1.ResourceAllocation{CpuMillicores: 999, MemoryBytes: 1 << 20},
	})
	wantCodeLocal(t, err, codes.FailedPrecondition)

	got, err := s.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: created.Id})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "terminated" {
		t.Fatalf("revived to %q via smuggled status+resources", got.Status)
	}
	// The refused update must NOT have applied the resources either (mutator
	// returns early on refusal).
	if got.Resources != nil && got.Resources.CpuMillicores == 999 {
		t.Fatalf("refused update still applied resources (cpu=999) to terminated session")
	}

	// A resource-only update (no status) is permitted and keeps the session terminal.
	if _, err := s.UpdateSession(ctx, &helixv1.UpdateSessionRequest{
		SessionId: created.Id,
		Resources: &helixv1.ResourceAllocation{CpuMillicores: 7},
	}); err != nil {
		t.Fatalf("resource-only update on terminated session: %v", err)
	}
	got2, err := s.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: created.Id})
	if err != nil {
		t.Fatalf("Get2: %v", err)
	}
	if got2.Status != "terminated" {
		t.Fatalf("resource-only update changed status to %q; want still terminated", got2.Status)
	}
	if got2.Resources == nil || got2.Resources.CpuMillicores != 7 {
		t.Fatalf("resource-only update not applied: %+v", got2.Resources)
	}
}

// TestAdversarial2_TerminatedToTerminatedUpdateIsNoOpAccepted proves the guard is
// scoped precisely: setting a terminated session's status to "terminated" again
// is NOT a resurrection and must be accepted (idempotent), not spuriously
// rejected. This guards against an over-broad guard that rejects any Update on a
// terminated session.
//
// Mutation that makes this FAIL: if the guard fired on `s.Status==terminated`
// regardless of the target status, this idempotent no-op would wrongly return
// FailedPrecondition.
func TestAdversarial2_TerminatedToTerminatedUpdateIsNoOpAccepted(t *testing.T) {
	s := newInProcServer()
	ctx := context.Background()

	created := mustCreate(t, s, "zombie3", "owner")
	if _, err := s.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: created.Id}); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	got, err := s.UpdateSession(ctx, &helixv1.UpdateSessionRequest{
		SessionId: created.Id,
		Status:    "terminated",
	})
	if err != nil {
		t.Fatalf("terminated->terminated update rejected: %v", err)
	}
	if got.Status != "terminated" {
		t.Fatalf("idempotent terminated update yielded %q", got.Status)
	}
}

// TestAdversarial2_ConcurrentTerminateVsResurrect is the TOCTOU probe: many
// goroutines race UpdateSession(Status:"running") against a single DeleteSession.
// Because the resurrection decision is made INSIDE the manager-locked mutator,
// the outcome must be consistent: once the session is terminated, no concurrent
// or subsequent Update may revive it. Run under -race.
//
// SINK-SIDE assertion: after the storm, the session's final status is
// "terminated" — never "running". (An Update that wins the race BEFORE terminate
// may legally set running; but the post-terminate final state must be terminal,
// because the guard refuses every revive once terminated, and Terminate from a
// non-terminal state always succeeds exactly once.)
//
// Mutation that makes this FAIL: doing the terminated-check OUTSIDE the lock
// (Get-then-Update at the handler) would let an Update that read "running" before
// terminate land its write AFTER terminate — reviving the session. The in-mutator
// guard prevents that.
func TestAdversarial2_ConcurrentTerminateVsResurrect(t *testing.T) {
	s := newInProcServer()
	ctx := context.Background()

	created := mustCreate(t, s, "race-zombie", "owner")
	id := created.Id

	const revivers = 7
	var wg sync.WaitGroup

	// One terminator.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = s.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: id})
	}()

	// Many would-be resurrectors.
	for i := 0; i < revivers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				_, _ = s.UpdateSession(ctx, &helixv1.UpdateSessionRequest{
					SessionId: id,
					Status:    "running",
				})
			}
		}()
	}
	wg.Wait()

	// Drain any further revive attempts to make the terminal state stick, then
	// assert the session cannot be running once terminated.
	for j := 0; j < revivers; j++ {
		_, _ = s.UpdateSession(ctx, &helixv1.UpdateSessionRequest{SessionId: id, Status: "running"})
	}
	final, err := s.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: id})
	if err != nil {
		t.Fatalf("final Get: %v", err)
	}
	if final.Status != "terminated" {
		t.Fatalf("FSM FAIL-OPEN under contention: final status = %q, want terminated", final.Status)
	}
	// And it must still reject a second terminate.
	if _, err := s.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: id}); err == nil {
		t.Fatalf("double-terminate succeeded after race; session was revived")
	}
}
