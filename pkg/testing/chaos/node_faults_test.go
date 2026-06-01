package chaos

import (
	"testing"
)

// ---------------------------------------------------------------------------
// NodeRestart
// ---------------------------------------------------------------------------

// TestNodeRestart_StateTransitions verifies the full lifecycle:
// - before Apply: IsRecovering()==false, IsDown()==false
// - after Apply:  IsRecovering()==true,  IsDown()==true
// - after Restore: IsRecovering()==false, IsDown()==false
//
// Anti-bluff: if Apply() is a no-op (does not set recovering=true) the
// second block fails. If Restore() is a no-op the third block fails.
func TestNodeRestart_StateTransitions(t *testing.T) {
	f := &NodeRestart{NodeID: "n1"}

	// Before Apply: neither down nor recovering.
	if f.IsRecovering() {
		t.Fatal("IsRecovering must be false before Apply")
	}
	if f.IsDown() {
		t.Fatal("IsDown must be false before Apply")
	}

	// Apply: node goes down into recovery window.
	if err := f.Apply(); err != nil {
		t.Fatalf("Apply: unexpected error: %v", err)
	}
	// Mutation: if Apply does not flip recovering, these both fail.
	if !f.IsRecovering() {
		t.Fatal("IsRecovering must be true after Apply")
	}
	if !f.IsDown() {
		t.Fatal("IsDown must be true after Apply")
	}

	// Restore: node fully back online.
	if err := f.Restore(); err != nil {
		t.Fatalf("Restore: unexpected error: %v", err)
	}
	// Mutation: if Restore does not clear recovering, these fail.
	if f.IsRecovering() {
		t.Fatal("IsRecovering must be false after Restore")
	}
	if f.IsDown() {
		t.Fatal("IsDown must be false after Restore")
	}
}

// TestNodeRestart_FaultInterface verifies NodeRestart satisfies the Fault
// interface (compile + Name() behavior), double-apply/double-restore guards.
func TestNodeRestart_FaultInterface(t *testing.T) {
	// Assign to interface variable: compile-time proof.
	var f Fault = &NodeRestart{NodeID: "n2"}

	if f.Name() != "node-restart" {
		t.Fatalf("expected name node-restart, got %s", f.Name())
	}

	// roundTrip exercises double-apply and double-restore guards.
	roundTrip(t, f)
}

// TestNodeRestart_IdempotentApply verifies double-Apply returns ErrAlreadyApplied.
func TestNodeRestart_IdempotentApply(t *testing.T) {
	f := &NodeRestart{NodeID: "n3"}
	if err := f.Apply(); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if err := f.Apply(); err == nil {
		t.Fatal("second Apply must return an error")
	}
	// Cleanup.
	_ = f.Restore()
}

// TestNodeRestart_IdempotentRestore verifies double-Restore returns ErrNotApplied.
func TestNodeRestart_IdempotentRestore(t *testing.T) {
	f := &NodeRestart{NodeID: "n4"}
	if err := f.Restore(); err == nil {
		t.Fatal("Restore before Apply must return an error")
	}
}

// ---------------------------------------------------------------------------
// DiskSpaceExhaustion
// ---------------------------------------------------------------------------

// TestDiskSpaceExhaustion_StateTransitions verifies the full lifecycle:
// - before Apply: WritesBlocked()==false
// - after Apply:  WritesBlocked()==true
// - after Restore: WritesBlocked()==false
//
// Anti-bluff: if Apply() is a no-op the "after Apply" assertion fails;
// if Restore() is a no-op the "after Restore" assertion fails.
func TestDiskSpaceExhaustion_StateTransitions(t *testing.T) {
	f := &DiskSpaceExhaustion{NodeID: "n1"}

	// Baseline: writes allowed.
	if f.WritesBlocked() {
		t.Fatal("WritesBlocked must be false before Apply")
	}

	// Apply: writes blocked.
	if err := f.Apply(); err != nil {
		t.Fatalf("Apply: unexpected error: %v", err)
	}
	// Mutation: if Apply does not set blocked=true, this fails.
	if !f.WritesBlocked() {
		t.Fatal("WritesBlocked must be true after Apply")
	}

	// Restore: writes unblocked.
	if err := f.Restore(); err != nil {
		t.Fatalf("Restore: unexpected error: %v", err)
	}
	// Mutation: if Restore does not clear blocked, this fails.
	if f.WritesBlocked() {
		t.Fatal("WritesBlocked must be false after Restore")
	}
}

// TestDiskSpaceExhaustion_FaultInterface verifies DiskSpaceExhaustion satisfies
// the Fault interface, correct Name(), and double-apply/restore guards.
func TestDiskSpaceExhaustion_FaultInterface(t *testing.T) {
	var f Fault = &DiskSpaceExhaustion{NodeID: "n2"}

	if f.Name() != "disk-space-exhaustion" {
		t.Fatalf("expected name disk-space-exhaustion, got %s", f.Name())
	}

	roundTrip(t, f)
}

// TestDiskSpaceExhaustion_IdempotentApply verifies double-Apply returns error.
func TestDiskSpaceExhaustion_IdempotentApply(t *testing.T) {
	f := &DiskSpaceExhaustion{NodeID: "n3"}
	if err := f.Apply(); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if err := f.Apply(); err == nil {
		t.Fatal("second Apply must return an error")
	}
	_ = f.Restore()
}

// ---------------------------------------------------------------------------
// ProcessTerminate
// ---------------------------------------------------------------------------

// TestProcessTerminate_StateTransitions verifies the full lifecycle:
// - before Apply: Killed()==false
// - after Apply:  Killed()==true
// - after Restore: Killed()==false
//
// Anti-bluff: if Apply() is a no-op the second block assertion fails;
// if Restore() is a no-op the third block assertion fails.
func TestProcessTerminate_StateTransitions(t *testing.T) {
	f := &ProcessTerminate{NodeID: "n1", Process: "etcd"}

	// Baseline: process alive.
	if f.Killed() {
		t.Fatal("Killed must be false before Apply")
	}

	// Apply: process killed.
	if err := f.Apply(); err != nil {
		t.Fatalf("Apply: unexpected error: %v", err)
	}
	// Mutation: if Apply does not set killed=true, this fails.
	if !f.Killed() {
		t.Fatal("Killed must be true after Apply")
	}

	// Restore: process resurrected.
	if err := f.Restore(); err != nil {
		t.Fatalf("Restore: unexpected error: %v", err)
	}
	// Mutation: if Restore does not clear killed, this fails.
	if f.Killed() {
		t.Fatal("Killed must be false after Restore")
	}
}

// TestProcessTerminate_FaultInterface verifies ProcessTerminate satisfies the
// Fault interface, correct Name(), and double-apply/restore guards.
func TestProcessTerminate_FaultInterface(t *testing.T) {
	var f Fault = &ProcessTerminate{NodeID: "n2", Process: "scheduler"}

	if f.Name() != "process-terminate" {
		t.Fatalf("expected name process-terminate, got %s", f.Name())
	}

	roundTrip(t, f)
}

// TestProcessTerminate_IdempotentApply verifies double-Apply returns error.
func TestProcessTerminate_IdempotentApply(t *testing.T) {
	f := &ProcessTerminate{NodeID: "n3", Process: "gateway"}
	if err := f.Apply(); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if err := f.Apply(); err == nil {
		t.Fatal("second Apply must return an error")
	}
	_ = f.Restore()
}

// TestProcessTerminate_IdempotentRestore verifies Restore before Apply returns error.
func TestProcessTerminate_IdempotentRestore(t *testing.T) {
	f := &ProcessTerminate{NodeID: "n4", Process: "proxy"}
	if err := f.Restore(); err == nil {
		t.Fatal("Restore before Apply must return an error")
	}
}

// ---------------------------------------------------------------------------
// Cross-fault: all three new types round-trip together in a ChaosRunner
// ---------------------------------------------------------------------------

// TestNewNodeFaults_ChaosRunnerIntegration ensures all three faults can be
// registered, applied, and restored via ChaosRunner without errors.
func TestNewNodeFaults_ChaosRunnerIntegration(t *testing.T) {
	cr := NewChaosRunner()
	cr.AddFault(&NodeRestart{NodeID: "n1"})
	cr.AddFault(&DiskSpaceExhaustion{NodeID: "n1"})
	cr.AddFault(&ProcessTerminate{NodeID: "n1", Process: "scheduler"})

	names := cr.ListFaults()
	if len(names) != 3 {
		t.Fatalf("expected 3 faults, got %d", len(names))
	}

	if err := cr.ApplyAll(); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}

	// Second ApplyAll must fail (idempotency guards intact).
	if err := cr.ApplyAll(); err == nil {
		t.Fatal("second ApplyAll must fail")
	}

	if err := cr.RestoreAll(); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	// Second RestoreAll must fail.
	if err := cr.RestoreAll(); err == nil {
		t.Fatal("second RestoreAll must fail")
	}
}
