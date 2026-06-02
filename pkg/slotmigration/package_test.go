package slotmigration

import (
	"testing"
)

// runUUID is a fixed per-suite identifier logged with observable values so the
// closure evidence is traceable.
const runUUID = "slotmigration-HXC-1396-9f3c1a7e"

// driveToTerminal advances the controller one tick at a time until it reaches a
// terminal phase or the tick budget is exhausted. It records the per-step owner
// and asserts the single-owner invariant at EVERY observable step (clause 2).
// It returns the number of steps taken and whether the invariant held.
func driveToTerminal(t *testing.T, c *Controller, slot, session string, budget int) (steps int, invariantHeld bool) {
	t.Helper()
	invariantHeld = true
	for steps = 0; steps < budget; steps++ {
		// Observe BEFORE advancing: at this observable point exactly one node
		// must own the slot and the session must still be alive.
		if oc := c.OwnerCount(slot); oc != 1 {
			t.Errorf("%s: step %d pre: OwnerCount=%d, want 1 (invariant violated)", runUUID, steps, oc)
			invariantHeld = false
		}
		if !c.SessionAlive(session) {
			t.Errorf("%s: step %d pre: session %q not alive (continuity violated)", runUUID, steps, session)
			invariantHeld = false
		}
		ph, err := c.Step()
		if err != nil {
			t.Fatalf("%s: step %d: unexpected error: %v", runUUID, steps, err)
		}
		// Observe AFTER advancing: invariant must STILL hold (the COMMIT flip
		// must not transiently produce 0 or 2 owners).
		if oc := c.OwnerCount(slot); oc != 1 {
			t.Errorf("%s: step %d post(%s): OwnerCount=%d, want 1 (invariant violated)", runUUID, steps, ph, oc)
			invariantHeld = false
		}
		if !c.SessionAlive(session) {
			t.Errorf("%s: step %d post(%s): session %q not alive (continuity violated)", runUUID, steps, ph, session)
			invariantHeld = false
		}
		if ph == PhaseCommitted || ph == PhaseAborted {
			steps++
			return steps, invariantHeld
		}
	}
	return steps, invariantHeld
}

// TestMigrationCompletesAtomicallyWithinBudget covers CLOSURE CLAUSE 1:
// a migration runs to completion; source slot ends EMPTY of the source (dst
// owns), dst owns slot+session, the session is NEVER lost, and completion
// happens within a bounded logical-tick budget (<10s modeled as ticks).
//
// Mutation: if commitNow did NOT flip ownership to dst (left src as owner), the
// post-condition Owner==dst fails. If commitNow did not preserve the session,
// SessionAlive fails. If the machine could not finish within the budget the
// bounded-completion assertion fails.
func TestMigrationCompletesAtomicallyWithinBudget(t *testing.T) {
	clk := NewLogicalClock()
	// Model the <10s budget as a 10-tick logical budget; TRANSFER costs 3 ticks.
	const tickBudget = 10
	c := NewController(clk, 3)

	const (
		slot    = "slot-42"
		session = "sess-abc"
		src     = "nodeA"
		dst     = "nodeB"
	)
	if err := c.Seed(slot, session, src); err != nil {
		t.Fatalf("%s: seed: %v", runUUID, err)
	}

	// BEFORE: source owns the slot, session alive on source.
	ownBefore, _ := c.Owner(slot)
	t.Logf("%s: BEFORE owner(%s)=%q sessionAlive(%s)=%v elapsed=%d phase=%s",
		runUUID, slot, ownBefore, session, c.SessionAlive(session), c.Elapsed(), c.Phase())
	if ownBefore != src {
		t.Fatalf("%s: precondition: owner=%q, want %q", runUUID, ownBefore, src)
	}

	if err := c.StartMigration(slot, session, src, dst); err != nil {
		t.Fatalf("%s: start: %v", runUUID, err)
	}

	steps, ok := driveToTerminal(t, c, slot, session, tickBudget)
	if !ok {
		t.Errorf("%s: single-owner / continuity invariant was violated during run", runUUID)
	}

	// AFTER: destination owns slot+session; source no longer owns it.
	ownAfter, owned := c.Owner(slot)
	elapsed := c.Elapsed()
	t.Logf("%s: AFTER owner(%s)=%q owned=%v sessionAlive(%s)=%v phase=%s steps=%d elapsed=%d",
		runUUID, slot, ownAfter, owned, session, c.SessionAlive(session), c.Phase(), steps, elapsed)

	if c.Phase() != PhaseCommitted {
		t.Fatalf("%s: phase=%s, want COMMITTED", runUUID, c.Phase())
	}
	if !owned || ownAfter != dst {
		t.Errorf("%s: after commit owner=%q (owned=%v), want %q (destination must own)", runUUID, ownAfter, owned, dst)
	}
	if ownAfter == src {
		t.Errorf("%s: source %q still owns slot after commit — source slot not emptied", runUUID, src)
	}
	if !c.SessionAlive(session) {
		t.Errorf("%s: session %q lost after migration", runUUID, session)
	}
	// Bounded completion: must finish strictly within the logical budget.
	if elapsed <= 0 {
		t.Errorf("%s: elapsed=%d, expected positive logical duration", runUUID, elapsed)
	}
	if elapsed >= tickBudget {
		t.Errorf("%s: elapsed=%d ticks, exceeded budget of %d (bounded completion violated)", runUUID, elapsed, tickBudget)
	}
}

// TestContinuityExactlyOneOwnerDuringTransfer covers CLOSURE CLAUSE 2: across
// the WHOLE run (including the TRANSFER phase and the COMMIT flip), exactly ONE
// node is the authoritative owner at every observable point — never zero, never
// two. It also pins that the owner is the SOURCE for the entire pre-commit span
// and only becomes the destination at the commit step.
//
// Mutation: if ownership were flipped to dst BEFORE the commit step (early flip
// that left src also present, or that emptied src early), the per-step
// assertions here would observe an owner that is dst while still in TRANSFER, or
// an OwnerCount != 1, and fail.
func TestContinuityExactlyOneOwnerDuringTransfer(t *testing.T) {
	clk := NewLogicalClock()
	c := NewController(clk, 4)

	const (
		slot    = "slot-7"
		session = "sess-xyz"
		src     = "src1"
		dst     = "dst1"
	)
	if err := c.Seed(slot, session, src); err != nil {
		t.Fatalf("%s: seed: %v", runUUID, err)
	}
	if err := c.StartMigration(slot, session, src, dst); err != nil {
		t.Fatalf("%s: start: %v", runUUID, err)
	}

	const budget = 12
	sawTransfer := false
	for i := 0; i < budget; i++ {
		phBefore := c.Phase()
		ownBefore, _ := c.Owner(slot)
		ocBefore := c.OwnerCount(slot)

		// Invariant pre-step.
		if ocBefore != 1 {
			t.Fatalf("%s: i=%d pre phase=%s OwnerCount=%d, want 1", runUUID, i, phBefore, ocBefore)
		}
		// Before commit, the authoritative owner MUST be the source.
		if phBefore == PhasePrepare || phBefore == PhaseTransfer {
			if ownBefore != src {
				t.Fatalf("%s: i=%d phase=%s owner=%q, want source %q (premature/early flip)",
					runUUID, i, phBefore, ownBefore, src)
			}
		}
		if phBefore == PhaseTransfer {
			sawTransfer = true
		}

		ph, err := c.Step()
		if err != nil {
			t.Fatalf("%s: i=%d step: %v", runUUID, i, err)
		}
		ownAfter, _ := c.Owner(slot)
		ocAfter := c.OwnerCount(slot)
		t.Logf("%s: i=%d %s->%s owner=%q OwnerCount=%d", runUUID, i, phBefore, ph, ownAfter, ocAfter)

		if ocAfter != 1 {
			t.Fatalf("%s: i=%d post phase=%s OwnerCount=%d, want 1 (flip exposed 0 or 2 owners)",
				runUUID, i, ph, ocAfter)
		}
		if ph == PhaseCommitted {
			if ownAfter != dst {
				t.Fatalf("%s: i=%d committed but owner=%q, want dst %q", runUUID, i, ownAfter, dst)
			}
			break
		}
		if ph == PhaseAborted {
			t.Fatalf("%s: i=%d unexpectedly aborted", runUUID, i)
		}
	}
	if !sawTransfer {
		t.Fatalf("%s: TRANSFER phase was never observed — invariant not exercised across the run", runUUID)
	}
}

// TestAbortDuringTransferRollsBackCleanly covers CLOSURE CLAUSE 3: a failure
// injected during TRANSFER aborts; ownership returns to (stays with) src, the
// session is still alive, and the destination holds nothing.
//
// Mutation: if abortNow flipped ownership to dst (or left it as dst from a
// premature commit), the owner==src assertion fails; if it dropped the session,
// SessionAlive fails; if dst somehow appeared as an owner, the dst-holds-nothing
// assertion fails.
func TestAbortDuringTransferRollsBackCleanly(t *testing.T) {
	clk := NewLogicalClock()
	c := NewController(clk, 5)

	const (
		slot    = "slot-99"
		session = "sess-keep"
		src     = "alpha"
		dst     = "beta"
	)
	if err := c.Seed(slot, session, src); err != nil {
		t.Fatalf("%s: seed: %v", runUUID, err)
	}
	if err := c.StartMigration(slot, session, src, dst); err != nil {
		t.Fatalf("%s: start: %v", runUUID, err)
	}

	// Advance into TRANSFER.
	if _, err := c.Step(); err != nil { // PREPARE -> TRANSFER
		t.Fatalf("%s: step into transfer: %v", runUUID, err)
	}
	if c.Phase() != PhaseTransfer {
		t.Fatalf("%s: phase=%s, want TRANSFER before abort", runUUID, c.Phase())
	}

	ownBefore, _ := c.Owner(slot)
	t.Logf("%s: BEFORE-ABORT owner(%s)=%q sessionAlive=%v phase=%s ownerCount=%d",
		runUUID, slot, ownBefore, c.SessionAlive(session), c.Phase(), c.OwnerCount(slot))
	if ownBefore != src {
		t.Fatalf("%s: during transfer owner=%q, want %q", runUUID, ownBefore, src)
	}

	// Inject failure and abort.
	if err := c.InjectTransferFailure(); err != nil {
		t.Fatalf("%s: inject failure: %v", runUUID, err)
	}
	if err := c.Abort(); err != nil {
		t.Fatalf("%s: abort: %v", runUUID, err)
	}

	ownAfter, owned := c.Owner(slot)
	t.Logf("%s: AFTER-ABORT owner(%s)=%q owned=%v sessionAlive=%v phase=%s ownerCount=%d",
		runUUID, slot, ownAfter, owned, c.SessionAlive(session), c.Phase(), c.OwnerCount(slot))

	if c.Phase() != PhaseAborted {
		t.Fatalf("%s: phase=%s, want ABORTED", runUUID, c.Phase())
	}
	if !owned || ownAfter != src {
		t.Errorf("%s: after abort owner=%q (owned=%v), want source %q (rollback failed)", runUUID, ownAfter, owned, src)
	}
	if ownAfter == dst {
		t.Errorf("%s: destination %q holds the slot after abort — rollback not clean", runUUID, dst)
	}
	if !c.SessionAlive(session) {
		t.Errorf("%s: session %q lost on abort (must be preserved)", runUUID, session)
	}
	if oc := c.OwnerCount(slot); oc != 1 {
		t.Errorf("%s: OwnerCount=%d after abort, want 1", runUUID, oc)
	}
}

// TestStepDrivesViaInjectedFailureToAbort proves the Step-driven abort path (no
// explicit Abort call): a failure injected before TRANSFER causes Step itself to
// land in ABORTED with clean rollback.
//
// Mutation: if Step ignored the injected failure flag and proceeded to COMMIT,
// the phase would be COMMITTED and owner==dst, failing the assertions below.
func TestStepDrivesViaInjectedFailureToAbort(t *testing.T) {
	clk := NewLogicalClock()
	c := NewController(clk, 2)

	const (
		slot    = "slot-3"
		session = "sess-3"
		src     = "n1"
		dst     = "n2"
	)
	if err := c.Seed(slot, session, src); err != nil {
		t.Fatalf("%s: seed: %v", runUUID, err)
	}
	if err := c.StartMigration(slot, session, src, dst); err != nil {
		t.Fatalf("%s: start: %v", runUUID, err)
	}
	if err := c.InjectTransferFailure(); err != nil {
		t.Fatalf("%s: inject: %v", runUUID, err)
	}

	// PREPARE step sees the armed failure and aborts.
	ph, err := c.Step()
	if err != nil {
		t.Fatalf("%s: step: %v", runUUID, err)
	}
	own, _ := c.Owner(slot)
	t.Logf("%s: STEP-ABORT phase=%s owner=%q sessionAlive=%v ownerCount=%d",
		runUUID, ph, own, c.SessionAlive(session), c.OwnerCount(slot))

	if ph != PhaseAborted {
		t.Fatalf("%s: phase=%s, want ABORTED via Step", runUUID, ph)
	}
	if own != src {
		t.Errorf("%s: owner=%q after step-abort, want %q", runUUID, own, src)
	}
	if !c.SessionAlive(session) {
		t.Errorf("%s: session lost on step-abort", runUUID)
	}
}

// TestStartMigrationGuards proves StartMigration rejects illegal inputs and an
// in-flight overlap, so the controller cannot enter an inconsistent state.
//
// Mutation: if StartMigration dropped the ErrUnowned guard, migrating a slot the
// source does not own would succeed; if it dropped the ErrBusy guard, a second
// concurrent migration would overwrite the first.
func TestStartMigrationGuards(t *testing.T) {
	clk := NewLogicalClock()
	c := NewController(clk, 2)

	if err := c.Seed("s", "sess", "owner1"); err != nil {
		t.Fatalf("%s: seed: %v", runUUID, err)
	}

	// Empty arg.
	if err := c.StartMigration("", "sess", "owner1", "owner2"); err != ErrEmptyArg {
		t.Errorf("%s: empty slot: got %v, want ErrEmptyArg", runUUID, err)
	}
	// Same node.
	if err := c.StartMigration("s", "sess", "owner1", "owner1"); err != ErrSameNode {
		t.Errorf("%s: same node: got %v, want ErrSameNode", runUUID, err)
	}
	// Source does not own the slot.
	if err := c.StartMigration("s", "sess", "strangerX", "owner2"); err != ErrUnowned {
		t.Errorf("%s: unowned: got %v, want ErrUnowned", runUUID, err)
	}

	// Valid start, then a second start must be rejected as busy.
	if err := c.StartMigration("s", "sess", "owner1", "owner2"); err != nil {
		t.Fatalf("%s: valid start: %v", runUUID, err)
	}
	if err := c.StartMigration("s", "sess", "owner1", "owner3"); err != ErrBusy {
		t.Errorf("%s: overlapping start: got %v, want ErrBusy", runUUID, err)
	}
	t.Logf("%s: guards OK; in-flight migration to owner2 preserved", runUUID)

	// After commit, a fresh migration is permitted (no longer busy).
	for i := 0; i < 5 && c.Phase() != PhaseCommitted; i++ {
		if _, err := c.Step(); err != nil {
			t.Fatalf("%s: drive: %v", runUUID, err)
		}
	}
	if c.Phase() != PhaseCommitted {
		t.Fatalf("%s: did not commit; phase=%s", runUUID, c.Phase())
	}
	if err := c.StartMigration("s", "sess", "owner2", "owner4"); err != nil {
		t.Errorf("%s: post-commit start should succeed, got %v", runUUID, err)
	}
}
