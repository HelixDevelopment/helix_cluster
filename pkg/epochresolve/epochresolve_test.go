package epochresolve

import (
	"errors"
	"testing"
)

// findRecord locates the ResolutionRecord for the given slot in records.
// It fails the test if no matching record is found.
func findRecord(t *testing.T, records []ResolutionRecord, slot string) ResolutionRecord {
	t.Helper()
	for _, r := range records {
		if r.Slot == slot {
			return r
		}
	}
	t.Fatalf("no ResolutionRecord found for slot %q", slot)
	return ResolutionRecord{}
}

// TestHigherEpochWins asserts that when two Claims compete for the same Slot
// and have different Epochs, the claim with the HIGHER Epoch wins — regardless
// of the order in which the claims appear in the input slice.
//
// MUTATION GUARD: inverting the `challenger.Epoch > current.Epoch` branch in
// beats() (i.e. changing > to <) makes this test FAIL because the lower-epoch
// owner would then be selected instead.
func TestHigherEpochWins(t *testing.T) {
	const runID = "run-higher-epoch-001"
	const slot = "partition-0"

	lowEpoch := Claim{Slot: slot, Owner: "node-beta", Epoch: 3}
	highEpoch := Claim{Slot: slot, Owner: "node-alpha", Epoch: 7}

	// Exercise with natural order (high epoch first).
	for _, input := range [][]Claim{
		{highEpoch, lowEpoch},
		{lowEpoch, highEpoch},
	} {
		records, err := Resolve(runID, input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		rec := findRecord(t, records, slot)

		// Assert the RunID is propagated into the record.
		if rec.RunID != runID {
			t.Errorf("RunID: got %q, want %q", rec.RunID, runID)
		}

		// Sink-side assertion: the winning Owner MUST be node-alpha (epoch 7).
		if rec.Winner.Owner != highEpoch.Owner {
			t.Errorf("Winner.Owner: got %q, want %q (high epoch should win)",
				rec.Winner.Owner, highEpoch.Owner)
		}

		// Confirm the winning Epoch is the higher one.
		if rec.Winner.Epoch != highEpoch.Epoch {
			t.Errorf("Winner.Epoch: got %d, want %d", rec.Winner.Epoch, highEpoch.Epoch)
		}
	}
}

// TestEqualEpochTiebreakDeterministic asserts that when two Claims share the
// same Epoch, the lexicographically SMALLER Owner is always chosen, regardless
// of input order.
func TestEqualEpochTiebreakDeterministic(t *testing.T) {
	const runID = "run-tiebreak-042"
	const slot = "shard-7"
	const sharedEpoch = uint64(99)

	claimA := Claim{Slot: slot, Owner: "alpha-controller", Epoch: sharedEpoch}
	claimB := Claim{Slot: slot, Owner: "zeta-controller", Epoch: sharedEpoch}
	// "alpha-controller" < "zeta-controller" lexicographically, so alpha wins.

	for _, input := range [][]Claim{
		{claimA, claimB},
		{claimB, claimA},
	} {
		records, err := Resolve(runID, input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		rec := findRecord(t, records, slot)

		// Assert RunID is faithfully propagated.
		if rec.RunID != runID {
			t.Errorf("RunID: got %q, want %q", rec.RunID, runID)
		}

		// Sink-side assertion: alpha-controller (lex smaller) must win.
		if rec.Winner.Owner != claimA.Owner {
			t.Errorf("Winner.Owner: got %q, want %q (lex-smaller owner should win on tie)",
				rec.Winner.Owner, claimA.Owner)
		}

		// Epoch must be the shared one.
		if rec.Winner.Epoch != sharedEpoch {
			t.Errorf("Winner.Epoch: got %d, want %d", rec.Winner.Epoch, sharedEpoch)
		}
	}
}

// TestMultipleSlots verifies that independent slots resolve independently and
// all ResolutionRecords carry the same RunID.
func TestMultipleSlots(t *testing.T) {
	const runID = "run-multi-slots-007"

	claims := []Claim{
		{Slot: "slot-A", Owner: "owner-1", Epoch: 10},
		{Slot: "slot-A", Owner: "owner-2", Epoch: 5},
		{Slot: "slot-B", Owner: "owner-3", Epoch: 1},
		{Slot: "slot-B", Owner: "owner-4", Epoch: 1}, // tiebreak: owner-3 < owner-4
		{Slot: "slot-C", Owner: "owner-5", Epoch: 99},
	}

	records, err := Resolve(runID, claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}

	// Verify each record carries the RunID.
	for _, r := range records {
		if r.RunID != runID {
			t.Errorf("slot %q: RunID got %q, want %q", r.Slot, r.RunID, runID)
		}
	}

	recA := findRecord(t, records, "slot-A")
	if recA.Winner.Owner != "owner-1" {
		t.Errorf("slot-A: got owner %q, want owner-1", recA.Winner.Owner)
	}

	recB := findRecord(t, records, "slot-B")
	if recB.Winner.Owner != "owner-3" {
		t.Errorf("slot-B tiebreak: got owner %q, want owner-3 (lex smaller)", recB.Winner.Owner)
	}

	recC := findRecord(t, records, "slot-C")
	if recC.Winner.Owner != "owner-5" {
		t.Errorf("slot-C: got owner %q, want owner-5", recC.Winner.Owner)
	}
}

// TestResultSortedBySlot confirms that records are returned in ascending Slot
// order, giving callers a stable, deterministic result.
func TestResultSortedBySlot(t *testing.T) {
	const runID = "run-sort-check"

	claims := []Claim{
		{Slot: "z-slot", Owner: "o1", Epoch: 1},
		{Slot: "a-slot", Owner: "o2", Epoch: 1},
		{Slot: "m-slot", Owner: "o3", Epoch: 1},
	}

	records, err := Resolve(runID, claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"a-slot", "m-slot", "z-slot"}
	for i, want := range expected {
		if records[i].Slot != want {
			t.Errorf("records[%d].Slot = %q, want %q", i, records[i].Slot, want)
		}
		// Also assert RunID in each record.
		if records[i].RunID != runID {
			t.Errorf("records[%d].RunID = %q, want %q", i, records[i].RunID, runID)
		}
	}
}

// TestErrNoClaims verifies that Resolve returns ErrNoClaims on empty input.
func TestErrNoClaims(t *testing.T) {
	_, err := Resolve("run-empty", nil)
	if !errors.Is(err, ErrNoClaims) {
		t.Errorf("expected ErrNoClaims, got %v", err)
	}

	_, err = Resolve("run-empty-slice", []Claim{})
	if !errors.Is(err, ErrNoClaims) {
		t.Errorf("expected ErrNoClaims for empty slice, got %v", err)
	}
}

// TestSingleClaim ensures a solitary claim always wins its slot and the RunID
// is threaded through correctly.
func TestSingleClaim(t *testing.T) {
	const runID = "run-single-claim-88"

	c := Claim{Slot: "lone-slot", Owner: "sole-owner", Epoch: 42}
	records, err := Resolve(runID, []Claim{c})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	rec := records[0]

	if rec.RunID != runID {
		t.Errorf("RunID: got %q, want %q", rec.RunID, runID)
	}
	if rec.Winner != c {
		t.Errorf("Winner: got %+v, want %+v", rec.Winner, c)
	}
}
