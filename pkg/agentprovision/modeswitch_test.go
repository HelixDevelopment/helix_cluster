package agentprovision

import (
	"testing"
)

// containsJob returns true if jobs contains a job with the given ID.
func containsJob(jobs []Job, id string) bool {
	for _, j := range jobs {
		if j.ID == id {
			return true
		}
	}
	return false
}

// jobByID returns the Job with the given ID from jobs, and a found flag.
func jobByID(jobs []Job, id string) (Job, bool) {
	for _, j := range jobs {
		if j.ID == id {
			return j, true
		}
	}
	return Job{}, false
}

// ─── Test 1: Interactive preempts the most-recently-admitted Batch job ─────

// TestModeSwitch_InteractivePreemptsBatch fills all slots with Batch jobs, then
// submits an Interactive job.  The test asserts:
//   - admitted == true
//   - preemptedID is non-empty and equals the most-recently-admitted Batch job
//   - WasPreempted(preemptedID) == true
//   - pool size is unchanged (slot reused, not grown)
//   - Running() contains the new Interactive job
//   - Running() does NOT contain the evicted Batch job
//
// Anti-bluff: if Submit does not execute the eviction branch (e.g., always
// places without preemption or always rejects), preemptedID will be empty and
// the assertion `preemptedID != ""` will fail.  If WasPreempted is a no-op
// the `WasPreempted(preemptedID) == true` assertion will fail.
func TestModeSwitch_InteractivePreemptsBatch(t *testing.T) {
	const slots = 3
	mm := NewModeManager(slots)

	// Fill all slots with Batch jobs; admit them in order b0, b1, b2.
	batchIDs := []string{"batch-0", "batch-1", "batch-2"}
	for _, id := range batchIDs {
		admitted, preempted, reason := mm.Submit(Job{ID: id, Mode: ModeBatch})
		if !admitted {
			t.Fatalf("batch job %q should be admitted into empty slot, reason=%q", id, reason)
		}
		if preempted != "" {
			t.Fatalf("batch job %q should not preempt anything, got preemptedID=%q", id, preempted)
		}
	}

	if got := len(mm.Running()); got != slots {
		t.Fatalf("pool size before preemption: want %d, got %d", slots, got)
	}

	// Submit an Interactive job — pool is full, must preempt the most-recently
	// admitted Batch job (batch-2, highest seq).
	admitted, preemptedID, reason := mm.Submit(Job{ID: "interactive-0", Mode: ModeInteractive})
	if !admitted {
		t.Fatalf("interactive job should be admitted via preemption, reason=%q", reason)
	}
	if preemptedID == "" {
		t.Fatal("preemptedID must be non-empty when an eviction occurred")
	}
	// Most-recently-admitted batch job must have been the victim.
	if preemptedID != "batch-2" {
		t.Fatalf("expected most-recently-admitted batch job %q to be evicted, got %q", "batch-2", preemptedID)
	}

	// Eviction ledger must record the victim as preempted.
	if !mm.WasPreempted(preemptedID) {
		t.Fatalf("WasPreempted(%q) == false; eviction ledger was not updated", preemptedID)
	}
	// Non-evicted jobs must not appear in the ledger.
	if mm.WasPreempted("batch-0") {
		t.Fatal("WasPreempted(batch-0) must be false; it was not evicted")
	}

	// Pool size must remain at slots (slot was reused, not grown).
	running := mm.Running()
	if len(running) != slots {
		t.Fatalf("pool size after preemption: want %d, got %d", slots, len(running))
	}

	// Interactive job must be present.
	if !containsJob(running, "interactive-0") {
		t.Fatal("Running() must contain interactive-0 after admission")
	}
	// Evicted batch job must NOT be present.
	if containsJob(running, preemptedID) {
		t.Fatalf("Running() must not contain evicted job %q", preemptedID)
	}
}

// ─── Test 2: Interactive rejects when all slots hold Interactive jobs ──────

// TestModeSwitch_FullInteractive_RejectsInteractive fills all slots with
// Interactive jobs and then submits another Interactive job.  There is no Batch
// victim, so the submission must be rejected.
//
// Anti-bluff: if Submit skipped the victim-search and blindly evicted any
// running job, admitted would be true and the assertion `admitted == false`
// would fail.
func TestModeSwitch_FullInteractive_RejectsInteractive(t *testing.T) {
	const slots = 2
	mm := NewModeManager(slots)

	for i := 0; i < slots; i++ {
		id := "int-full-" + string(rune('a'+i))
		admitted, _, reason := mm.Submit(Job{ID: id, Mode: ModeInteractive})
		if !admitted {
			t.Fatalf("interactive job %q should be admitted into free slot, reason=%q", id, reason)
		}
	}

	admitted, preemptedID, reason := mm.Submit(Job{ID: "int-overflow", Mode: ModeInteractive})
	if admitted {
		t.Fatal("interactive job must be rejected when pool is full of interactive jobs")
	}
	if preemptedID != "" {
		t.Fatalf("no eviction expected, got preemptedID=%q", preemptedID)
	}
	if reason == "" {
		t.Fatal("reason must explain the rejection")
	}
}

// ─── Test 3: Batch never preempts, even when pool is full ─────────────────

// TestModeSwitch_BatchNeverPreempts fills all slots with Interactive jobs and
// submits a Batch job.  Batch jobs are never allowed to preempt, so the
// submission must be rejected regardless of what is running.
//
// Anti-bluff: if Submit allowed Batch jobs to preempt, admitted would be true
// and the assertion `admitted == false` would fail.
func TestModeSwitch_BatchNeverPreempts(t *testing.T) {
	const slots = 2
	mm := NewModeManager(slots)

	for i := 0; i < slots; i++ {
		id := "int-hold-" + string(rune('a'+i))
		admitted, _, reason := mm.Submit(Job{ID: id, Mode: ModeInteractive})
		if !admitted {
			t.Fatalf("interactive job %q should be admitted, reason=%q", id, reason)
		}
	}

	admitted, preemptedID, reason := mm.Submit(Job{ID: "batch-late", Mode: ModeBatch})
	if admitted {
		t.Fatal("batch job must be rejected when pool is full; batch never preempts")
	}
	if preemptedID != "" {
		t.Fatalf("no eviction expected for batch arrival, got preemptedID=%q", preemptedID)
	}
	if reason == "" {
		t.Fatal("reason must be non-empty on rejection")
	}
}

// ─── Test 4: Complete frees a slot; next Submit succeeds without preemption ─

// TestModeSwitch_CompleteFreesSlot fills the pool, completes one job, then
// submits a new job into the freed slot without any preemption occurring.
//
// Anti-bluff: if Complete did not free the slot, the subsequent Submit would
// either be rejected or would require preemption; the assertion
// `preemptedID == ""` would fail in the preemption case.
func TestModeSwitch_CompleteFreesSlot(t *testing.T) {
	const slots = 2
	mm := NewModeManager(slots)

	admitted0, _, r0 := mm.Submit(Job{ID: "job-0", Mode: ModeBatch})
	if !admitted0 {
		t.Fatalf("job-0 should be admitted, reason=%q", r0)
	}
	admitted1, _, r1 := mm.Submit(Job{ID: "job-1", Mode: ModeBatch})
	if !admitted1 {
		t.Fatalf("job-1 should be admitted, reason=%q", r1)
	}

	// Pool is now full.  Complete job-0 to free a slot.
	freed := mm.Complete("job-0")
	if !freed {
		t.Fatal("Complete(job-0) should return true (job was running)")
	}

	// Submit job-2 (Batch).  The freed slot must absorb it without preemption.
	admitted2, preemptedID, reason := mm.Submit(Job{ID: "job-2", Mode: ModeBatch})
	if !admitted2 {
		t.Fatalf("job-2 should be admitted into freed slot, reason=%q", reason)
	}
	if preemptedID != "" {
		t.Fatalf("job-2 should not require preemption, got preemptedID=%q", preemptedID)
	}

	// Pool must now contain job-1 and job-2, not job-0.
	running := mm.Running()
	if len(running) != slots {
		t.Fatalf("pool size want %d, got %d", slots, len(running))
	}
	if containsJob(running, "job-0") {
		t.Fatal("Running() must not contain completed job-0")
	}
	if !containsJob(running, "job-1") {
		t.Fatal("Running() must contain job-1")
	}
	if !containsJob(running, "job-2") {
		t.Fatal("Running() must contain job-2")
	}
}

// ─── Test 5: SwitchMode changes state; preemption outcome reflects the change

// TestModeSwitch_SwitchModeChangesPreemptOutcome fills a 2-slot pool with one
// Batch and one Interactive job, then:
//
//  1. Verifies the pool is full and that an incoming Interactive job would
//     preempt the Batch job (proving the initial state is correct).
//  2. Restores the pool to the original state (complete the interactive that
//     was submitted in step 1, re-submit the batch that was evicted is not
//     needed — we rebuild fresh).
//  3. Switches the remaining Batch job's mode to Interactive via SwitchMode.
//  4. Fills the second slot again with another Interactive job.
//  5. Submits a new Interactive job — now ALL running jobs are Interactive
//     (the former Batch was reclassified), so there is no Batch victim and the
//     submission must be rejected.
//
// Anti-bluff: if SwitchMode were a no-op, the job would remain classified as
// Batch and step 5 would succeed (admitted=true) instead of being rejected —
// directly falsifying the assertion `admitted == false`.
func TestModeSwitch_SwitchModeChangesPreemptOutcome(t *testing.T) {
	const slots = 2
	mm := NewModeManager(slots)

	// Fill pool: one Batch + one Interactive.
	admittedB, _, rB := mm.Submit(Job{ID: "batch-x", Mode: ModeBatch})
	if !admittedB {
		t.Fatalf("batch-x should be admitted, reason=%q", rB)
	}
	admittedI, _, rI := mm.Submit(Job{ID: "int-x", Mode: ModeInteractive})
	if !admittedI {
		t.Fatalf("int-x should be admitted, reason=%q", rI)
	}

	// Sanity: confirm preemption works before the mode switch.
	// An Interactive job arrives while pool is full — batch-x is the victim.
	admSanity, preemptSanity, rSanity := mm.Submit(Job{ID: "int-sanity", Mode: ModeInteractive})
	if !admSanity {
		t.Fatalf("int-sanity should preempt batch-x, reason=%q", rSanity)
	}
	if preemptSanity != "batch-x" {
		t.Fatalf("sanity preempt: expected batch-x evicted, got %q", preemptSanity)
	}
	// Pool now: {int-x, int-sanity}.

	// Complete int-sanity, and submit a fresh Batch job to restore the original
	// scenario: one Batch + one Interactive.
	mm.Complete("int-sanity")
	admittedB2, _, rB2 := mm.Submit(Job{ID: "batch-y", Mode: ModeBatch})
	if !admittedB2 {
		t.Fatalf("batch-y should be admitted into freed slot, reason=%q", rB2)
	}
	// Pool now: {int-x, batch-y}.

	// Verify SwitchMode returns true (mode actually changed).
	changed := mm.SwitchMode("batch-y", ModeInteractive)
	if !changed {
		t.Fatal("SwitchMode(batch-y, ModeInteractive) should return true")
	}

	// Confirm the Running() snapshot reflects the new mode.
	running := mm.Running()
	j, found := jobByID(running, "batch-y")
	if !found {
		t.Fatal("batch-y must still be in Running() after SwitchMode")
	}
	if j.Mode != ModeInteractive {
		t.Fatalf("batch-y mode after SwitchMode: want ModeInteractive, got %v", j.Mode)
	}

	// Pool is full {int-x=Interactive, batch-y=Interactive(reclassified)}.
	// Submit a new Interactive job — no Batch victim exists → must be rejected.
	admAfter, preemptAfter, reasonAfter := mm.Submit(Job{ID: "int-after", Mode: ModeInteractive})
	if admAfter {
		t.Fatalf(
			"after SwitchMode, no batch victim remains; int-after must be rejected but got admitted=true (preemptedID=%q)",
			preemptAfter,
		)
	}
	if preemptAfter != "" {
		t.Fatalf("no eviction expected after mode switch, got preemptedID=%q", preemptAfter)
	}
	if reasonAfter == "" {
		t.Fatal("reason must be non-empty on rejection after mode switch")
	}

	// Double-check SwitchMode on a non-running job returns false.
	if mm.SwitchMode("ghost-job", ModeBatch) {
		t.Fatal("SwitchMode on a non-running job must return false")
	}
	// Double-check SwitchMode with same mode returns false.
	if mm.SwitchMode("int-x", ModeInteractive) {
		t.Fatal("SwitchMode with same mode must return false (no-change)")
	}
}

// ─── Mode.String() coverage ────────────────────────────────────────────────

// TestMode_String verifies Mode.String() returns the expected labels.
// Anti-bluff: if String() returned a constant or empty string for both modes,
// the cross-check `batch != interactive` would still pass structurally — but
// the exact-match assertions would fail.
func TestMode_String(t *testing.T) {
	if got := ModeBatch.String(); got != "batch" {
		t.Fatalf("ModeBatch.String() want %q, got %q", "batch", got)
	}
	if got := ModeInteractive.String(); got != "interactive" {
		t.Fatalf("ModeInteractive.String() want %q, got %q", "interactive", got)
	}
	// The two labels must differ.
	if ModeBatch.String() == ModeInteractive.String() {
		t.Fatal("ModeBatch and ModeInteractive must have distinct String() representations")
	}
}

// ─── Complete idempotency ──────────────────────────────────────────────────

// TestModeSwitch_CompleteMissingJobReturnsFalse verifies that calling Complete
// on a job that is not in the pool returns false (idempotent, no panic).
//
// Anti-bluff: if Complete always returned true regardless of whether the job
// was found, the assertion `freed == false` would fail.
func TestModeSwitch_CompleteMissingJobReturnsFalse(t *testing.T) {
	mm := NewModeManager(2)
	freed := mm.Complete("never-admitted")
	if freed {
		t.Fatal("Complete on a non-existent job must return false")
	}
}

// ─── WasPreempted returns false for jobs that were completed normally ──────

// TestModeSwitch_WasPreempted_NormalCompletion verifies that a job that
// completes normally (via Complete) is NOT recorded as preempted.
//
// Anti-bluff: if WasPreempted returned true for any completed job, this
// assertion would fail.
func TestModeSwitch_WasPreempted_NormalCompletion(t *testing.T) {
	mm := NewModeManager(2)
	admitted, _, reason := mm.Submit(Job{ID: "job-normal", Mode: ModeBatch})
	if !admitted {
		t.Fatalf("job-normal should be admitted, reason=%q", reason)
	}
	mm.Complete("job-normal")

	if mm.WasPreempted("job-normal") {
		t.Fatal("normally-completed job must not be recorded as preempted")
	}
}
