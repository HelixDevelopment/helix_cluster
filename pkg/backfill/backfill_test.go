package backfill

import (
	"errors"
	"testing"
)

// TestBackfillSmallJobJumpsAheadOfWaitingBigJob is the central real-behavior
// test for conservative backfill. Setup (Capacity = 4):
//
//	J0 (highest prio): width 2, walltime 10  -> reserved at t=0,  holds 2/4 over [0,10)
//	J1 (next prio):    width 4, walltime 5   -> needs the whole cluster; blocked by J0
//	                                            until t=10, so reserved at [10,15)
//	J2 (lowest prio):  width 2, walltime 4   -> fits in the idle 2 units alongside J0
//
// Conservative backfill MUST place J2 at t=0 (it jumps ahead of the
// higher-priority, still-waiting J1) AND MUST leave J1's reservation at t=10
// completely undisturbed. We assert BOTH facts with exact values.
//
// MUTATION: a backfiller that ignores the reservation guarantee — e.g. one
// that placed jobs in plain FIFO/earliest order without first committing J1's
// reservation — would let J2's earlier placement be irrelevant, OR a buggy
// reorder would push J1 earlier/later. Concretely, change backf.go's loop to
// schedule jobs by ascending WallTime instead of the given priority order
// (`sort jobs by WallTime`), and J1's reserved start moves off 10 -> the
// "J1 start == 10" assertion below FAILS.
func TestBackfillSmallJobJumpsAheadOfWaitingBigJob(t *testing.T) {
	jobs := []Job{
		{ID: "J0", Width: 2, WallTime: 10},
		{ID: "J1", Width: 4, WallTime: 5},
		{ID: "J2", Width: 2, WallTime: 4},
	}

	sched, err := Schedule(4, jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	j0, _ := sched.AssignmentFor("J0")
	j1, _ := sched.AssignmentFor("J1")
	j2, _ := sched.AssignmentFor("J2")

	// Exact reservation of the highest-priority job.
	if j0.StartTime != 0 {
		t.Fatalf("J0 must start at 0, got %d", j0.StartTime)
	}
	// The big high-priority job's reservation: blocked until J0 frees the
	// cluster at t=10. This is THE conservative-backfill anchor.
	if j1.StartTime != 10 {
		t.Fatalf("J1 reserved start must be 10 (undelayed), got %d", j1.StartTime)
	}
	if j1.EndTime != 15 {
		t.Fatalf("J1 end must be 15, got %d", j1.EndTime)
	}
	// The small low-priority job backfills to t=0.
	if j2.StartTime != 0 {
		t.Fatalf("J2 must backfill to 0, got %d", j2.StartTime)
	}
	if !j2.Backfilled {
		t.Fatalf("J2 must be flagged Backfilled (it jumped ahead of J1)")
	}
	// And the two assertions that together prove conservative backfill:
	// J2 starts EARLIER than J1, while J1's reserved start is NOT delayed.
	if !(j2.StartTime < j1.StartTime) {
		t.Fatalf("backfill broken: J2 start %d must be earlier than J1 start %d", j2.StartTime, j1.StartTime)
	}
	if j1.StartTime != 10 {
		t.Fatalf("reservation guarantee violated: J1 start must remain 10, got %d", j1.StartTime)
	}
}

// TestNoBackfillWhenItWouldDelayReservation proves the guard: a low-priority
// job that WOULD have to delay the high-priority reservation is NOT moved
// ahead of it. Setup (Capacity = 4):
//
//	J0 (high): width 3, walltime 10 -> reserved [0,10), holds 3/4
//	J1 (next): width 4, walltime 5  -> needs all 4; blocked until t=10 -> [10,15)
//	J2 (low):  width 2, walltime 4  -> only 1 unit is free during [0,10); 2 don't
//	                                   fit there, so it canNOT backfill ahead of J1
//
// J2 must therefore start at t=10 or later — AFTER J1 in time — and must NOT
// be flagged Backfilled. With J0 holding 3 and J1 holding 4 on [10,15), the
// earliest J2 can fit its 2 units is t=15.
//
// MUTATION: drop the `tl.usedAt(b)+width > capacity` interior-boundary check
// in timeline.fits (return true after only checking the start instant). Then
// fits() would wrongly accept J2 at t=0 (start-occupancy 3+2=5 already fails,
// so instead delete the start check too / weaken to `>=`), placing J2 at 0 and
// over-committing the timeline — the "J2 start == 15" and peak<=capacity
// assertions FAIL.
func TestNoBackfillWhenItWouldDelayReservation(t *testing.T) {
	jobs := []Job{
		{ID: "J0", Width: 3, WallTime: 10},
		{ID: "J1", Width: 4, WallTime: 5},
		{ID: "J2", Width: 2, WallTime: 4},
	}

	sched, err := Schedule(4, jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	j1, _ := sched.AssignmentFor("J1")
	j2, _ := sched.AssignmentFor("J2")

	if j1.StartTime != 10 {
		t.Fatalf("J1 reserved start must be 10, got %d", j1.StartTime)
	}
	// J2 cannot jump the queue: only 1 unit free during [0,10), it needs 2.
	if j2.StartTime != 15 {
		t.Fatalf("J2 must NOT backfill; expected start 15 (after J1), got %d", j2.StartTime)
	}
	if j2.Backfilled {
		t.Fatalf("J2 must NOT be flagged Backfilled; it stayed after J1")
	}
	if !(j2.StartTime >= j1.EndTime) {
		t.Fatalf("J2 start %d must be at or after J1 end %d", j2.StartTime, j1.EndTime)
	}
}

// TestFIFOWhenNoBackfillPossible verifies that with a single-wide cluster where
// every job needs the whole machine, jobs run strictly back-to-back in queue
// order with no overlap and no backfill. Capacity = 1.
//
//	J0: width 1, walltime 3 -> [0,3)
//	J1: width 1, walltime 2 -> [3,5)
//	J2: width 1, walltime 4 -> [5,9)
//
// MUTATION: change earliestStart's notBefore handling to always return 0
// (`return notBefore` -> `return 0`). Then all three jobs would be placed at 0,
// overlapping on a width-1 cluster; the strict-increasing start assertions and
// the no-backfill assertion FAIL (and reserve() would panic on the invariant).
func TestFIFOWhenNoBackfillPossible(t *testing.T) {
	jobs := []Job{
		{ID: "J0", Width: 1, WallTime: 3},
		{ID: "J1", Width: 1, WallTime: 2},
		{ID: "J2", Width: 1, WallTime: 4},
	}

	sched, err := Schedule(1, jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	j0, _ := sched.AssignmentFor("J0")
	j1, _ := sched.AssignmentFor("J1")
	j2, _ := sched.AssignmentFor("J2")

	if j0.StartTime != 0 || j0.EndTime != 3 {
		t.Fatalf("J0 must be [0,3), got [%d,%d)", j0.StartTime, j0.EndTime)
	}
	if j1.StartTime != 3 || j1.EndTime != 5 {
		t.Fatalf("J1 must be [3,5), got [%d,%d)", j1.StartTime, j1.EndTime)
	}
	if j2.StartTime != 5 || j2.EndTime != 9 {
		t.Fatalf("J2 must be [5,9), got [%d,%d)", j2.StartTime, j2.EndTime)
	}
	for _, a := range sched.Assignments {
		if a.Backfilled {
			t.Fatalf("no job may be backfilled in pure FIFO, but %s was", a.JobID)
		}
	}
}

// TestCapacityInvariantNeverExceeded replays a full schedule onto a fresh
// timeline and asserts that peak occupancy never exceeds capacity at ANY
// instant. This is the global safety invariant of the whole package.
//
// Capacity = 5, a mix that forces overlapping placements and at least one
// backfill.
//
// MUTATION: in timeline.reserve, change `tl.delta[end] -= width` to a no-op
// (delete that line). Jobs would then never release their resources, so the
// running prefix sum climbs past capacity and peakUsage() exceeds 5 -> this
// test FAILS.
func TestCapacityInvariantNeverExceeded(t *testing.T) {
	jobs := []Job{
		{ID: "A", Width: 3, WallTime: 6},
		{ID: "B", Width: 5, WallTime: 4},
		{ID: "C", Width: 2, WallTime: 3},
		{ID: "D", Width: 4, WallTime: 5},
		{ID: "E", Width: 1, WallTime: 2},
	}

	sched, err := Schedule(5, jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Independently replay every assignment to verify the invariant holds on
	// a clean timeline (not trusting the scheduler's own bookkeeping).
	check := newTimeline(5)
	for _, a := range sched.Assignments {
		// width is recovered from the original job set by matching ID.
		var w int
		for _, j := range jobs {
			if j.ID == a.JobID {
				w = j.Width
			}
		}
		if !check.fits(a.StartTime, a.EndTime-a.StartTime, w) {
			t.Fatalf("assignment %s [%d,%d) width %d does not fit at commit time", a.JobID, a.StartTime, a.EndTime, w)
		}
		check.reserve(a.StartTime, a.EndTime-a.StartTime, w)
	}
	if peak := check.peakUsage(); peak > 5 {
		t.Fatalf("capacity invariant violated: peak usage %d > capacity 5", peak)
	}
	// Sanity: at least one job actually overlapped (otherwise the invariant is
	// trivially satisfied and proves nothing).
	if peak := check.peakUsage(); peak < 2 {
		t.Fatalf("test setup too weak: expected overlapping usage, peak only %d", peak)
	}
}

// TestEmptyAndValidation covers boundary/validation behavior with exact errors.
//
// MUTATION: remove the `j.Width > capacity` check in Schedule. The too-wide
// job would then be accepted and earliestStart would loop without ever
// fitting; the ErrJobTooWide assertion FAILS.
func TestEmptyAndValidation(t *testing.T) {
	if _, err := Schedule(0, nil); err != ErrInvalidCapacity {
		t.Fatalf("expected ErrInvalidCapacity, got %v", err)
	}
	if s, err := Schedule(4, nil); err != nil || len(s.Assignments) != 0 {
		t.Fatalf("empty job list must yield empty schedule, got err=%v len=%d", err, len(s.Assignments))
	}
	_, err := Schedule(4, []Job{{ID: "huge", Width: 5, WallTime: 1}})
	if err == nil {
		t.Fatalf("expected error for too-wide job")
	}
	// Verify it is the specific sentinel via errors.Is semantics.
	if !errors.Is(err, ErrJobTooWide) {
		t.Fatalf("expected wrapped ErrJobTooWide, got %v", err)
	}

	_, err = Schedule(4, []Job{{ID: "bad", Width: 0, WallTime: 1}})
	if err == nil {
		t.Fatalf("expected error for zero-width job")
	}
}
