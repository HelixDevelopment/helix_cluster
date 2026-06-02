package gepetto

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"
)

// newRunID returns a random 16-byte hex string for log correlation.
func newRunID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b)
}

// TestReserveScheduleAcrossLoadTiers drives loadFraction across the four
// representative tiers (0.05, 0.30, 0.60, 0.85) and asserts that the returned
// capacity matches the reserve schedule for the below-water tiers and that the
// runID is recorded for each call.
//
// Tier expectations (derived from reserveSchedule):
//
//	0.05 → tier below 0.10 → reservePerGPU = 0
//	0.30 → tier below 0.40 → reservePerGPU = 2
//	0.60 → tier below 0.70 → reservePerGPU = 5
//	0.85 → above highWaterMark (0.80) → 0  (anti-starvation)
//
// Note: 0.85 is tested here for record-assertion completeness; its capacity
// invariant (must be exactly 0) is the sole subject of TestZeroCapacityAboveHighWater.
func TestReserveScheduleAcrossLoadTiers(t *testing.T) {
	a := NewArbitrator(8)

	type tcase struct {
		load         float64
		gpusPerNode  int
		wantCapacity int
	}
	cases := []tcase{
		{load: 0.05, gpusPerNode: 4, wantCapacity: 0},
		{load: 0.30, gpusPerNode: 4, wantCapacity: 2},
		{load: 0.60, gpusPerNode: 4, wantCapacity: 5},
		{load: 0.85, gpusPerNode: 4, wantCapacity: 0}, // anti-starvation
	}

	// Collect run IDs in order so we can verify recording below.
	runIDs := make([]string, len(cases))
	for i, tc := range cases {
		runID := newRunID(t)
		runIDs[i] = runID

		t.Logf("before call[%d]: runID=%s load=%.2f gpus=%d", i, runID, tc.load, tc.gpusPerNode)

		got := a.ChutesCapacity(runID, tc.load, tc.gpusPerNode)

		t.Logf("after  call[%d]: runID=%s load=%.2f resolved=%d (want %d)",
			i, runID, tc.load, got, tc.wantCapacity)

		if got != tc.wantCapacity {
			t.Errorf("ChutesCapacity(load=%.2f) = %d, want %d (runID=%s)",
				tc.load, got, tc.wantCapacity, runID)
		}
	}

	// Assert every runID was recorded with the correct loadFraction and
	// resolved capacity — this is the sink-side behaviour the caller can
	// observe via Records().
	records := a.Records()
	if len(records) != len(cases) {
		t.Fatalf("Records() returned %d entries, want %d", len(records), len(cases))
	}
	for i, rec := range records {
		if rec.RunID != runIDs[i] {
			t.Errorf("record[%d].RunID = %q, want %q", i, rec.RunID, runIDs[i])
		}
		if rec.LoadFraction != cases[i].load {
			t.Errorf("record[%d].LoadFraction = %.4f, want %.4f", i, rec.LoadFraction, cases[i].load)
		}
		if rec.ResolvedCapacity != cases[i].wantCapacity {
			t.Errorf("record[%d].ResolvedCapacity = %d, want %d (runID=%s)",
				i, rec.ResolvedCapacity, cases[i].wantCapacity, rec.RunID)
		}
		t.Logf("record[%d] verified: runID=%s load=%.2f resolved=%d",
			i, rec.RunID, rec.LoadFraction, rec.ResolvedCapacity)
	}
}

// TestZeroCapacityAboveHighWater verifies the anti-starvation rule: when
// loadFraction is strictly greater than 0.80 (the highWaterMark constant) the
// returned capacity MUST be exactly zero.
//
// MUTATION GUARD: this test is the direct kill-check for the branch:
//
//	if loadFraction > highWaterMark { return 0 }
//
// Removing or inverting that branch causes loadFraction=0.85 to fall through
// to the reserve schedule (tier below 0.81, reservePerGPU=8), making this
// test fail because got=8 ≠ want=0.
//
// The runID is asserted to be present in Records() so the sink-side log
// evidence is also verified.
func TestZeroCapacityAboveHighWater(t *testing.T) {
	a := NewArbitrator(4)

	// Test a range of values all strictly above 0.80. The values 0.801 and
	// 0.805 sit in the (0.80, 0.81) gap where the reserve schedule's
	// below:0.81 tier (reservePerGPU=8) WOULD apply: these are the only inputs
	// that independently pin the anti-starvation branch, because for load>=0.81
	// resolveFromSchedule already returns 0 via its defensive fallthrough.
	// Disabling the loadFraction>highWaterMark branch makes 0.801/0.805 return
	// 8 instead of 0, failing this test.
	aboveWater := []float64{0.801, 0.805, 0.81, 0.85, 0.90, 0.95, 1.00}

	runIDs := make([]string, len(aboveWater))
	for i, load := range aboveWater {
		runID := newRunID(t)
		runIDs[i] = runID

		t.Logf("before: runID=%s load=%.2f (above highWaterMark=%.2f)", runID, load, highWaterMark)

		got := a.ChutesCapacity(runID, load, 8)

		t.Logf("after:  runID=%s load=%.2f resolved=%d", runID, load, got)

		// MUTATION GUARD assertion — see package-level comment.
		if got != 0 {
			t.Errorf("ChutesCapacity(load=%.2f) = %d, want exactly 0 (anti-starvation rule; runID=%s)",
				load, got, runID)
		}
	}

	// Verify every above-water call was recorded with ResolvedCapacity == 0.
	records := a.Records()
	if len(records) != len(aboveWater) {
		t.Fatalf("Records() has %d entries, want %d", len(records), len(aboveWater))
	}
	for i, rec := range records {
		if rec.RunID != runIDs[i] {
			t.Errorf("record[%d].RunID = %q, want %q", i, rec.RunID, runIDs[i])
		}
		if rec.ResolvedCapacity != 0 {
			t.Errorf("record[%d] load=%.2f: recorded ResolvedCapacity=%d, want 0 (runID=%s)",
				i, rec.LoadFraction, rec.ResolvedCapacity, rec.RunID)
		}
		t.Logf("record[%d] verified: runID=%s load=%.2f resolved=%d (zero as required)",
			i, rec.RunID, rec.LoadFraction, rec.ResolvedCapacity)
	}
}

// TestExactHighWaterBoundary verifies the boundary condition: load exactly
// equal to 0.80 is NOT above the high-water mark and MUST follow the schedule
// (tier below 0.81 → reservePerGPU=8), while 0.8000001 is above and must
// return 0.
func TestExactHighWaterBoundary(t *testing.T) {
	a := NewArbitrator(4)

	runIDAt := newRunID(t)
	runIDAbove := newRunID(t)

	// Exactly at the high-water mark: should use schedule → 8.
	t.Logf("before: runID=%s load=0.80 (exactly at high-water)", runIDAt)
	gotAt := a.ChutesCapacity(runIDAt, 0.80, 2)
	t.Logf("after:  runID=%s load=0.80 resolved=%d (want 8)", runIDAt, gotAt)
	if gotAt != 8 {
		t.Errorf("ChutesCapacity(0.80) = %d, want 8 (not above high-water; runID=%s)", gotAt, runIDAt)
	}

	// One epsilon above: should return 0.
	t.Logf("before: runID=%s load=0.8000001 (just above high-water)", runIDAbove)
	gotAbove := a.ChutesCapacity(runIDAbove, 0.8000001, 2)
	t.Logf("after:  runID=%s load=0.8000001 resolved=%d (want 0)", runIDAbove, gotAbove)
	if gotAbove != 0 {
		t.Errorf("ChutesCapacity(0.8000001) = %d, want 0 (anti-starvation; runID=%s)", gotAbove, runIDAbove)
	}

	// Check records.
	records := a.Records()
	if len(records) != 2 {
		t.Fatalf("Records() has %d entries, want 2", len(records))
	}
	if records[0].RunID != runIDAt || records[0].ResolvedCapacity != 8 {
		t.Errorf("record[0]: got runID=%q cap=%d, want runID=%q cap=8",
			records[0].RunID, records[0].ResolvedCapacity, runIDAt)
	}
	if records[1].RunID != runIDAbove || records[1].ResolvedCapacity != 0 {
		t.Errorf("record[1]: got runID=%q cap=%d, want runID=%q cap=0",
			records[1].RunID, records[1].ResolvedCapacity, runIDAbove)
	}
}

// TestRecordsReturnsCopy verifies that mutating the slice returned by Records()
// does not corrupt the internal log.
func TestRecordsReturnsCopy(t *testing.T) {
	a := NewArbitrator(4)
	runID := newRunID(t)

	a.ChutesCapacity(runID, 0.30, 4)

	first := a.Records()
	if len(first) != 1 {
		t.Fatalf("expected 1 record, got %d", len(first))
	}

	// Mutate the returned slice.
	first[0].RunID = "tampered"
	first[0].ResolvedCapacity = 9999

	// Internal log must be unaffected.
	second := a.Records()
	if len(second) != 1 {
		t.Fatalf("expected 1 record after mutation, got %d", len(second))
	}
	if second[0].RunID != runID {
		t.Errorf("internal log RunID = %q after external mutation, want %q", second[0].RunID, runID)
	}
	if second[0].ResolvedCapacity != 2 {
		t.Errorf("internal log ResolvedCapacity = %d, want 2", second[0].ResolvedCapacity)
	}
	t.Logf("copy isolation verified: runID=%s cap=%d intact", second[0].RunID, second[0].ResolvedCapacity)
}

// TestNewArbitratorNegativeCapacity verifies that NewArbitrator clamps a
// negative logCapacity argument to zero, producing a usable arbitrator with an
// empty log rather than panicking or allocating a negative-size slice.
//
// Adversarial-review fix: the clamping branch (logCapacity < 0 → 0) at
// NewArbitrator was unreachable from any test, leaving a defensive guard
// untested and invisible to mutation analysis.
func TestNewArbitratorNegativeCapacity(t *testing.T) {
	// A negative hint must not panic and must produce a working arbitrator.
	a := NewArbitrator(-1)
	if a == nil {
		t.Fatal("NewArbitrator(-1) returned nil")
	}

	// Must have an empty log immediately after construction.
	recs := a.Records()
	if len(recs) != 0 {
		t.Fatalf("Records() after NewArbitrator(-1): got %d entries, want 0", len(recs))
	}

	// Must be fully operational — append a record and retrieve it.
	runID := newRunID(t)
	t.Logf("before: runID=%s load=0.30 (post-negative-capacity construction)", runID)
	got := a.ChutesCapacity(runID, 0.30, 4)
	t.Logf("after:  runID=%s resolved=%d (want 2)", runID, got)
	if got != 2 {
		t.Errorf("ChutesCapacity(0.30) after NewArbitrator(-1) = %d, want 2", got)
	}
	recs = a.Records()
	if len(recs) != 1 {
		t.Fatalf("Records() = %d entries after one call, want 1", len(recs))
	}
	if recs[0].RunID != runID {
		t.Errorf("record RunID = %q, want %q", recs[0].RunID, runID)
	}
	t.Logf("negative-capacity construction verified: arbitrator functional, record intact")
}

// TestResolveFromScheduleDefensiveFallthrough verifies that resolveFromSchedule
// returns zero for a loadFraction that falls through every tier bound — i.e. a
// value ≥ 0.81 that somehow bypasses the anti-starvation guard (e.g. via
// direct call in a future refactor).
//
// Adversarial-review fix: the defensive return-0 path at the bottom of
// resolveFromSchedule (load ≥ 0.81, no tier matched) was never exercised by
// any test. The function is package-private, so we call it directly here.
func TestResolveFromScheduleDefensiveFallthrough(t *testing.T) {
	for _, load := range []float64{0.81, 0.85, 0.90, 1.00, 1.5, 999.0} {
		got := resolveFromSchedule(load)
		t.Logf("resolveFromSchedule(%.4f) = %d", load, got)
		if got != 0 {
			t.Errorf("resolveFromSchedule(%.4f) = %d, want 0 (defensive fallthrough)", load, got)
		}
	}
	// Also confirm negative input returns zero (NaN-equivalent defensive path).
	got := resolveFromSchedule(-0.01)
	t.Logf("resolveFromSchedule(-0.01) = %d", got)
	if got != 0 {
		t.Errorf("resolveFromSchedule(-0.01) = %d, want 0 (defensive path)", got)
	}
}

// TestConcurrentChutesCapacity exercises concurrent access to ChutesCapacity
// and Records() to surface data races when run with -race.
func TestConcurrentChutesCapacity(t *testing.T) {
	a := NewArbitrator(32)
	const goroutines = 8
	const callsEach = 16

	done := make(chan struct{}, goroutines)

	loads := []float64{0.05, 0.30, 0.60, 0.75, 0.85, 0.95}

	for g := 0; g < goroutines; g++ {
		go func(g int) {
			for c := 0; c < callsEach; c++ {
				load := loads[(g+c)%len(loads)]
				runID := fmt.Sprintf("g%d-c%d", g, c)
				a.ChutesCapacity(runID, load, 4)
			}
			// Interleave a Records() read.
			_ = a.Records()
			done <- struct{}{}
		}(g)
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}

	records := a.Records()
	want := goroutines * callsEach
	if len(records) != want {
		t.Errorf("Records() has %d entries, want %d", len(records), want)
	}
	t.Logf("concurrent test complete: %d records recorded", len(records))
}
