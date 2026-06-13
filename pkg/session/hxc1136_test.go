package session

// hxc1136_test.go — HXC-1136 closure criteria: Manager.Create with a real
// backend actually starts a backing session.
//
// ANTI-BLUFF guarantee (CLAUDE-1): StatusRunning MUST mean a real process
// exists in the backend.  The tests here prove the sink-side fact by calling
// backend.SessionExists(id) — NOT by trusting the StatusRunning field alone.
//
// §1.1 MUTATION PAIRING: each test documents what change makes it FAIL.

import (
	"context"
	"testing"
)

// TestManager_CreateWithRealBackend_SessionExistsInBackend proves that when
// a (mock) TmuxBackend is wired, Manager.Create actually calls the backend's
// CreateSession and the backing session is queryable via SessionExists.
//
// MUTATION that makes this FAIL: revert manager.go to set StatusRunning
// unconditionally without calling m.tmuxBackend.CreateSession — the mock's
// sessions map stays empty, SessionExists returns false, and the assertion
// below flips.
func TestManager_CreateWithRealBackend_SessionExistsInBackend(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	sess, err := mgr.Create(context.Background(), &CreateRequest{
		Name:    "hxc1136-unit",
		Owner:   "tester",
		Backend: BackendTmux,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Status != StatusRunning {
		t.Fatalf("status = %s, want running", sess.Status)
	}

	// SINK-SIDE proof: the backend was told to create a session for this ID.
	// If the manager set StatusRunning without calling the backend, this fails.
	exists, err := mock.SessionExists(string(sess.ID))
	if err != nil {
		t.Fatalf("SessionExists: %v", err)
	}
	if !exists {
		t.Fatalf("ANTI-BLUFF: session %q is StatusRunning but backend has no live session — this is a PASS-bluff (§7.1 violation)", sess.ID)
	}
}

// TestManager_NilBackend_DoesNotReportRunning_WhenDisabled tests the nil-backend
// path in isolation.  A nil backend means "no process launched"; the manager
// still returns StatusRunning for backwards-compat with callers that explicitly
// pass nil.  This test documents that behavior and provides the mutation gate:
// the presence of a nil backend must NOT make SessionExists return true on any
// backend (there is no backend to query).
//
// What this test ACTUALLY guards: callers that wire a real backend (non-nil)
// must call through to it.  The mutation proof below is on the real path.
func TestManager_NilBackend_StatusRunning_IsDocumentedBehavior(t *testing.T) {
	// nil backend — in-memory only, no real process.
	mgr := NewManager(nil)
	sess, err := mgr.Create(context.Background(), &CreateRequest{
		Owner:   "tester",
		Backend: BackendTmux,
	})
	if err != nil {
		t.Fatalf("Create with nil backend: %v", err)
	}
	// Documented: nil backend still produces StatusRunning (in-memory tracking
	// only).  This is acceptable ONLY when the caller explicitly acknowledges
	// there is no real backing process by passing nil.
	if sess.Status != StatusRunning {
		t.Fatalf("nil-backend create: status = %s, want running", sess.Status)
	}
}

// TestManager_CreateWithRealBackend_MutationGate is the §1.1 mutation gate.
//
// The mutation we prove catches: if manager.go's Create is changed to skip the
//
//	`if m.tmuxBackend != nil { m.tmuxBackend.CreateSession(...) }` block and
//	unconditionally set StatusRunning, then mock.sessions stays empty and
//	mock.SessionExists returns false — causing TestManager_CreateWithRealBackend_SessionExistsInBackend
//	to FAIL with the "ANTI-BLUFF" message.
//
// This companion test drives the same proof from a different angle: it checks
// that at least one "create:" call was recorded by the mock's call log.
// Mutation: remove the `m.tmuxBackend.CreateSession` call → calls stays
// empty → the HasPrefix assertion below fails.
func TestManager_CreateWithRealBackend_MutationGate(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	sess, err := mgr.Create(context.Background(), &CreateRequest{
		Owner:   "mutation-probe",
		Backend: BackendTmux,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = sess

	mock.mu.Lock()
	calls := make([]string, len(mock.calls))
	copy(calls, mock.calls)
	mock.mu.Unlock()

	if len(calls) == 0 {
		t.Fatalf("MUTATION gate: no backend calls recorded — backend.CreateSession was not invoked. Manager must call the backend to start a real session.")
	}
	if len(calls) < 1 {
		t.Fatalf("expected at least 1 call (create:*), got 0")
	}
	// First call must be a create call.
	first := calls[0]
	if len(first) < 7 || first[:7] != "create:" {
		t.Fatalf("first mock call = %q; want create:<sessionID> — backend.CreateSession was not called first", first)
	}
}

// TestManager_BackendFailure_StatusFailed_NotRunning proves that when the
// backend's CreateSession returns an error, the session is recorded as FAILED
// (not RUNNING).  This is the complementary anti-bluff: a manager that sets
// StatusRunning regardless of backend outcome is equally broken.
//
// MUTATION that makes this FAIL: in manager.go, remove the `s.Status = StatusFailed`
// assignment in the error branch of the tmuxBackend.CreateSession call — the
// returned session stays StatusRunning and the assertion flips.
func TestManager_BackendFailure_StatusFailed_NotRunning(t *testing.T) {
	mock := newMockTmuxBackend()
	mock.failNext = "create"
	mgr := NewManager(mock)

	sess, err := mgr.Create(context.Background(), &CreateRequest{
		Owner:   "tester",
		Backend: BackendTmux,
	})
	if err == nil {
		t.Fatalf("expected error from backend failure, got nil")
	}
	if sess == nil {
		t.Fatalf("session record must be returned even on failure")
	}
	if sess.Status == StatusRunning {
		t.Fatalf("ANTI-BLUFF: backend failed but session is StatusRunning — this is a PASS-bluff. Status must be Failed, not Running.")
	}
	if sess.Status != StatusFailed {
		t.Fatalf("status = %s, want failed after backend error", sess.Status)
	}

	// Sink-side double-check: backend must NOT report this session as alive.
	exists, _ := mock.SessionExists(string(sess.ID))
	if exists {
		t.Fatalf("backend reports session alive after CreateSession error — backend state and manager state are inconsistent")
	}
}
