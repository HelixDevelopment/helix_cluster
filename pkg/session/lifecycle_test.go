package session

import (
	"context"
	"testing"
)

// --- Lifecycle FSM transition-guard tests ---
//
// The session lifecycle is a finite state machine. Illegal transitions MUST
// return an error rather than silently succeed (CLAUDE-1: a feature that
// "executes without panic" but lets a terminated session be re-terminated or
// migrated is broken from the end-user's perspective).

func TestLifecycle_AllowedTransitions(t *testing.T) {
	// Sanity: the canonical happy-path transitions must be permitted.
	allowed := []struct {
		from   SessionStatus
		action lifecycleAction
	}{
		{StatusRunning, actionAttach},
		{StatusRunning, actionDetach},
		{StatusRunning, actionMigrate},
		{StatusRunning, actionTerminate},
		{StatusMigrating, actionTerminate},
		{StatusPaused, actionDetach},
		{StatusPaused, actionTerminate},
		{StatusCreating, actionTerminate},
		{StatusFailed, actionTerminate},
	}
	for _, tc := range allowed {
		if err := validateTransition(tc.from, tc.action); err != nil {
			t.Fatalf("expected %s -> %s to be allowed, got error: %v", tc.from, tc.action, err)
		}
	}
}

func TestLifecycle_IllegalTransitionsRejected(t *testing.T) {
	// Table of transitions that MUST be rejected.
	illegal := []struct {
		name   string
		from   SessionStatus
		action lifecycleAction
	}{
		{"attach-terminated", StatusTerminated, actionAttach},
		{"detach-terminated", StatusTerminated, actionDetach},
		{"migrate-terminated", StatusTerminated, actionMigrate},
		{"terminate-terminated", StatusTerminated, actionTerminate},
		{"attach-paused", StatusPaused, actionAttach},
		{"attach-migrating", StatusMigrating, actionAttach},
		{"detach-migrating", StatusMigrating, actionDetach},
		{"migrate-migrating", StatusMigrating, actionMigrate},
		{"attach-creating", StatusCreating, actionAttach},
		{"migrate-creating", StatusCreating, actionMigrate},
		{"migrate-failed", StatusFailed, actionMigrate},
		{"attach-failed", StatusFailed, actionAttach},
	}
	for _, tc := range illegal {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTransition(tc.from, tc.action)
			if err == nil {
				t.Fatalf("illegal transition %s -> %s MUST return an error, got nil", tc.from, tc.action)
			}
			var ite *InvalidTransitionError
			if !asInvalidTransition(err, &ite) {
				t.Fatalf("expected *InvalidTransitionError, got %T: %v", err, err)
			}
			if ite.From != tc.from || ite.Action != tc.action {
				t.Fatalf("error fields mismatch: got from=%s action=%s", ite.From, ite.Action)
			}
		})
	}
}

// TestLifecycle_MutationGate is the paired mutation gate: it proves the guard
// actually rejects. If validateTransition were a no-op that always returned nil
// (the "mutation"), this test would FAIL because the illegal transition would be
// accepted. We assert BOTH directions explicitly.
func TestLifecycle_MutationGate(t *testing.T) {
	// A known-illegal transition must error...
	if err := validateTransition(StatusTerminated, actionMigrate); err == nil {
		t.Fatalf("mutation gate: migrate-from-terminated must be rejected")
	}
	// ...and a known-legal transition must NOT error. A guard that rejects
	// everything is equally broken.
	if err := validateTransition(StatusRunning, actionTerminate); err != nil {
		t.Fatalf("mutation gate: terminate-from-running must be allowed, got %v", err)
	}
}

// --- Manager-level enforcement: the FSM must be wired into real operations. ---

func TestManager_Attach_RejectsTerminated(t *testing.T) {
	mgr := NewManager(nil)
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})

	if err := mgr.Terminate(context.Background(), s.ID); err != nil {
		t.Fatalf("terminate failed: %v", err)
	}
	// Attaching to a terminated session must be rejected by the FSM guard.
	if err := mgr.Attach(context.Background(), s.ID, "alice"); err == nil {
		t.Fatalf("attach to terminated session must be rejected")
	}
}

func TestManager_Migrate_RejectsTerminated(t *testing.T) {
	mgr := NewManager(nil)
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	if err := mgr.Terminate(context.Background(), s.ID); err != nil {
		t.Fatalf("terminate failed: %v", err)
	}

	// Migrating a terminated session must be rejected, and status must NOT
	// silently flip to Migrating (sink-side evidence per CLAUDE-1).
	if err := mgr.Migrate(context.Background(), s.ID, "node-b"); err == nil {
		t.Fatalf("migrate of terminated session must be rejected")
	}
	got, _ := mgr.Get(s.ID)
	if got.Status != StatusTerminated {
		t.Fatalf("rejected migrate must leave status Terminated, got %s", got.Status)
	}
	if got.NodeID == "node-b" {
		t.Fatalf("rejected migrate must NOT mutate NodeID")
	}
}

func TestManager_Terminate_RejectsDoubleTerminate(t *testing.T) {
	mgr := NewManager(nil)
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	if err := mgr.Terminate(context.Background(), s.ID); err != nil {
		t.Fatalf("first terminate failed: %v", err)
	}
	if err := mgr.Terminate(context.Background(), s.ID); err == nil {
		t.Fatalf("second terminate of already-terminated session must be rejected")
	}
}

func TestManager_Migrate_RejectsWhileMigrating(t *testing.T) {
	mgr := NewManager(nil)
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	if err := mgr.Migrate(context.Background(), s.ID, "node-b"); err != nil {
		t.Fatalf("first migrate failed: %v", err)
	}
	// Session is now Migrating; a concurrent migrate must be rejected.
	if err := mgr.Migrate(context.Background(), s.ID, "node-c"); err == nil {
		t.Fatalf("migrate of already-migrating session must be rejected")
	}
}

// TestManager_HappyPath_StillWorks guards against the FSM over-restricting and
// breaking the canonical Create -> Attach -> Detach -> Migrate -> Terminate flow.
func TestManager_HappyPath_StillWorks(t *testing.T) {
	mgr := NewManager(nil)
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})

	if err := mgr.Attach(context.Background(), s.ID, "alice"); err != nil {
		t.Fatalf("attach should be allowed from Running: %v", err)
	}
	if err := mgr.Detach(context.Background(), s.ID, "alice"); err != nil {
		t.Fatalf("detach should be allowed from Running: %v", err)
	}
	if err := mgr.Migrate(context.Background(), s.ID, "node-b"); err != nil {
		t.Fatalf("migrate should be allowed from Running: %v", err)
	}
	if err := mgr.Terminate(context.Background(), s.ID); err != nil {
		t.Fatalf("terminate should be allowed from Migrating: %v", err)
	}
}
