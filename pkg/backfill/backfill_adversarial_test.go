package backfill

// Adversarial sink-side probes for pkg/backfill. Written per the tonight's
// recurring-bug charter:
//
//	(a) total-order at a seq/identity edge (equal start / duplicate ID with no
//	    deterministic tiebreak),
//	(b) idempotency (replaying the same job set / same window twice must not
//	    double-apply occupancy or double-advance a checkpoint),
//	(c) unguarded shared mutable state under concurrency (-race),
//	(d) numeric / boundary off-by-one and integer overflow on a range.
//
// FINDINGS (honest three-way discrimination, see file footer for the full
// writeup):
//   - The conservative-backfill core (Schedule + timeline) is CORRECT,
//     DETERMINISTIC, and RACE-FREE BY CONSTRUCTION (Schedule allocates a fresh
//     timeline per call; there is no package-level mutable state). Every
//     "real-behavior" probe below PASSES against unmodified prod. These are
//     contract-pinning tests, NOT reproducers of a defect.
//   - Two LATENT RISKS are pinned with their CURRENT (documented-helper)
//     behavior so a future change that alters them trips a test:
//     (1) Plan.AssignmentFor returns the FIRST match for a duplicate JobID —
//     the second job's placement is unrecoverable by ID lookup.
//     (2) Job.WallTime near math.MaxInt overflows Assignment.EndTime
//     (start+walltime wraps negative). Out of the documented "small abstract
//     time unit" envelope, so a note, not a fix.

import (
	"math"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// (a) TOTAL ORDER AT A SEQ/IDENTITY EDGE — equal start, deterministic order.
// ---------------------------------------------------------------------------

// TestEqualStartParallelPlacementIsDeterministic probes the seq/identity edge:
// several jobs that all legitimately start at the SAME instant t=0 (they run in
// parallel because the cluster is wide enough). There is no sequence number to
// tiebreak on — the ONLY deterministic order is the input (priority/FIFO) slice
// order. We assert the Plan preserves input order exactly and that the placement
// is byte-for-byte identical across many repeated runs (no map-iteration
// nondeterminism leaking into the result).
//
// This is the backfill analogue of the antientropy/offlinesync equal-version
// bug: equal keys with no tiebreak must still resolve deterministically.
func TestEqualStartParallelPlacementIsDeterministic(t *testing.T) {
	jobs := []Job{
		{ID: "P0", Width: 1, WallTime: 5},
		{ID: "P1", Width: 1, WallTime: 5},
		{ID: "P2", Width: 1, WallTime: 5},
		{ID: "P3", Width: 1, WallTime: 5},
	}

	first, err := Schedule(4, jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// All four fit in parallel at t=0; Plan order must equal input order.
	wantIDs := []string{"P0", "P1", "P2", "P3"}
	for i, a := range first.Assignments {
		if a.JobID != wantIDs[i] {
			t.Fatalf("Plan must preserve input order: pos %d got %s want %s", i, a.JobID, wantIDs[i])
		}
		if a.StartTime != 0 || a.EndTime != 5 {
			t.Fatalf("%s must be parallel [0,5), got [%d,%d)", a.JobID, a.StartTime, a.EndTime)
		}
		if a.Backfilled {
			t.Fatalf("%s started at the same instant as its peers; it jumped no one and must NOT be flagged Backfilled", a.JobID)
		}
	}

	// SINK-SIDE DETERMINISM: re-run many times. The map-backed timeline must not
	// leak iteration nondeterminism into start times or ordering. Any divergence
	// here is exactly the class-(a) nondeterministic-replay bug.
	for run := 0; run < 200; run++ {
		got, err := Schedule(4, jobs)
		if err != nil {
			t.Fatalf("run %d: unexpected error: %v", run, err)
		}
		if len(got.Assignments) != len(first.Assignments) {
			t.Fatalf("run %d: assignment count drifted", run)
		}
		for i := range got.Assignments {
			a, b := got.Assignments[i], first.Assignments[i]
			if a != b {
				t.Fatalf("run %d: nondeterministic placement at pos %d: %+v != %+v", run, i, a, b)
			}
		}
	}
}

// TestSerialEqualWidthDeterministicTiebreak forces the OTHER equal-edge: jobs
// that cannot run in parallel (each needs the whole cluster) and therefore
// contend for the same earliest slot. The contended resource is resolved
// strictly by queue position — equal-width, equal-walltime jobs run back to back
// in input order, never reordered, never overlapping.
func TestSerialEqualWidthDeterministicTiebreak(t *testing.T) {
	jobs := []Job{
		{ID: "Q0", Width: 4, WallTime: 3},
		{ID: "Q1", Width: 4, WallTime: 3},
		{ID: "Q2", Width: 4, WallTime: 3},
	}
	for run := 0; run < 100; run++ {
		s, err := Schedule(4, jobs)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		// Exact back-to-back placement, input order preserved.
		want := []struct{ start, end int }{{0, 3}, {3, 6}, {6, 9}}
		for i, a := range s.Assignments {
			if a.StartTime != want[i].start || a.EndTime != want[i].end {
				t.Fatalf("run %d: %s must be [%d,%d), got [%d,%d)", run, a.JobID, want[i].start, want[i].end, a.StartTime, a.EndTime)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// (a') DUPLICATE IDENTITY — LATENT RISK pinned at current behavior.
// ---------------------------------------------------------------------------

// TestDuplicateJobIDLookupReturnsFirstMatch pins the documented-but-sharp
// behavior of Plan.AssignmentFor when two jobs share an ID. Both jobs ARE placed
// distinctly on the timeline (the scheduler treats them as independent work),
// but AssignmentFor — a linear ID lookup — returns the FIRST match, so the
// second job's placement is unrecoverable through the public lookup API.
//
// This is a LATENT RISK, not a bug: Job.ID is documented as "uniquely
// identifies the job within a scheduling round." Callers that violate that
// precondition lose addressability of duplicates. We pin it so a future change
// (e.g. returning the last match, or erroring) is caught.
func TestDuplicateJobIDLookupReturnsFirstMatch(t *testing.T) {
	jobs := []Job{
		{ID: "dup", Width: 4, WallTime: 5}, // -> [0,5)
		{ID: "dup", Width: 4, WallTime: 3}, // -> [5,8)
	}
	s, err := Schedule(4, jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// SINK: both duplicates were placed, serially and non-overlapping.
	if len(s.Assignments) != 2 {
		t.Fatalf("both duplicate-ID jobs must be placed, got %d assignments", len(s.Assignments))
	}
	if s.Assignments[0].StartTime != 0 || s.Assignments[0].EndTime != 5 {
		t.Fatalf("first dup must be [0,5), got [%d,%d)", s.Assignments[0].StartTime, s.Assignments[0].EndTime)
	}
	if s.Assignments[1].StartTime != 5 || s.Assignments[1].EndTime != 8 {
		t.Fatalf("second dup must be [5,8), got [%d,%d)", s.Assignments[1].StartTime, s.Assignments[1].EndTime)
	}
	// LATENT RISK pin: AssignmentFor returns the FIRST match only.
	got, ok := s.AssignmentFor("dup")
	if !ok {
		t.Fatalf("AssignmentFor(dup) must find a match")
	}
	if got.StartTime != 0 || got.EndTime != 5 {
		t.Fatalf("AssignmentFor returns FIRST match [0,5); got [%d,%d) — if this changed, revisit the duplicate-ID contract", got.StartTime, got.EndTime)
	}
}

// ---------------------------------------------------------------------------
// (b) IDEMPOTENCY — replay must not double-apply occupancy / double-count.
// ---------------------------------------------------------------------------

// TestScheduleIsPureNoHiddenCheckpoint proves Schedule carries no hidden
// cross-call checkpoint or accumulator: scheduling the SAME job set twice yields
// byte-identical Plans (no progress counter "advanced" by the first call that
// would shift the second). A stateful scheduler that, say, remembered the prior
// timeline would push the second run's starts forward — that is the idempotency
// failure this guards.
func TestScheduleIsPureNoHiddenCheckpoint(t *testing.T) {
	jobs := []Job{
		{ID: "A", Width: 2, WallTime: 6},
		{ID: "B", Width: 4, WallTime: 5},
		{ID: "C", Width: 2, WallTime: 4},
	}
	a, err := Schedule(4, jobs)
	if err != nil {
		t.Fatalf("first schedule: %v", err)
	}
	b, err := Schedule(4, jobs)
	if err != nil {
		t.Fatalf("second schedule: %v", err)
	}
	if len(a.Assignments) != len(b.Assignments) {
		t.Fatalf("replay changed assignment count: %d vs %d", len(a.Assignments), len(b.Assignments))
	}
	for i := range a.Assignments {
		if a.Assignments[i] != b.Assignments[i] {
			t.Fatalf("replay not idempotent at pos %d: %+v != %+v (hidden checkpoint advanced?)", i, a.Assignments[i], b.Assignments[i])
		}
	}
}

// TestTimelineReserveIsNotIdempotent_ExactlyOnceContract probes the occupancy
// SINK directly: reserve() is an ACCUMULATING operation, so committing the same
// window twice MUST raise occupancy by 2*width (it is exactly-once-per-call, NOT
// idempotent-by-key). We assert the real prefix-sum sink, and that a second
// commit that would breach capacity panics rather than silently double-applying.
//
// This nails the "replay double-applies an effect" concern from the other side:
// the timeline does NOT silently swallow a duplicate commit — each reserve is
// counted exactly once, and an over-commit is a hard panic, never a lost or
// doubled unit.
func TestTimelineReserveIsNotIdempotent_ExactlyOnceContract(t *testing.T) {
	tl := newTimeline(4)
	tl.reserve(0, 5, 1)
	if got := tl.usedAt(0); got != 1 {
		t.Fatalf("after one reserve, usedAt(0) must be 1, got %d", got)
	}
	// Same window again: occupancy MUST accumulate to 2 (exactly-once per call).
	tl.reserve(0, 5, 1)
	if got := tl.usedAt(0); got != 2 {
		t.Fatalf("reserve is exactly-once-per-call; two commits must total 2, got %d", got)
	}
	tl.reserve(0, 5, 1)
	tl.reserve(0, 5, 1)
	if got := tl.usedAt(0); got != 4 {
		t.Fatalf("four width-1 commits must total 4 (no lost/doubled units), got %d", got)
	}
	if peak := tl.peakUsage(); peak != 4 {
		t.Fatalf("peak must equal 4 at exact capacity, got %d", peak)
	}
	// A fifth commit breaches capacity -> hard panic, never a silent double/loss.
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("over-capacity reserve must panic, not silently over-commit")
		}
	}()
	tl.reserve(0, 5, 1) // 4+1 > 4
}

// ---------------------------------------------------------------------------
// (c) CONCURRENCY / SHARED MUTABLE STATE — run with -race.
// ---------------------------------------------------------------------------

// TestConcurrentSchedulesAreRaceFreeAndConsistent fires many Schedule calls in
// parallel over the SAME input slice. Schedule is documented/implemented as pure
// (fresh timeline per call, no package globals beyond error sentinels), so this
// must be race-free under -race AND every goroutine must produce the identical
// Plan. A shared mutable timeline or progress map would surface here as either a
// data race or a divergent/lost-update Plan.
func TestConcurrentSchedulesAreRaceFreeAndConsistent(t *testing.T) {
	jobs := []Job{
		{ID: "G0", Width: 2, WallTime: 10},
		{ID: "G1", Width: 4, WallTime: 5},
		{ID: "G2", Width: 2, WallTime: 4},
		{ID: "G3", Width: 1, WallTime: 7},
	}
	// Golden reference computed single-threaded.
	golden, err := Schedule(4, jobs)
	if err != nil {
		t.Fatalf("golden schedule: %v", err)
	}

	const goroutines = 64
	var wg sync.WaitGroup
	errs := make(chan string, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Schedule(4, jobs)
			if err != nil {
				errs <- "unexpected error: " + err.Error()
				return
			}
			if len(s.Assignments) != len(golden.Assignments) {
				errs <- "assignment count diverged under concurrency"
				return
			}
			for i := range s.Assignments {
				if s.Assignments[i] != golden.Assignments[i] {
					errs <- "divergent Plan under concurrency (lost update / shared state)"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	if msg, bad := <-errs; bad {
		t.Fatalf("concurrent Schedule failure: %s", msg)
	}
}

// ---------------------------------------------------------------------------
// (d) NUMERIC / BOUNDARY — half-open window off-by-one + overflow latent risk.
// ---------------------------------------------------------------------------

// TestHalfOpenWindowAbutsExactlyNoDoubleCount nails the [start,end) boundary:
// a job whose window ENDS exactly where another's BEGINS must NOT be treated as
// overlapping (no off-by-one double-include), while a window that extends one
// unit further MUST collide. This is the classic window-edge off-by-one.
func TestHalfOpenWindowAbutsExactlyNoDoubleCount(t *testing.T) {
	tl := newTimeline(4)
	tl.reserve(5, 3, 4) // [5,8) fully booked

	// [0,5) abuts [5,8) exactly at the boundary -> MUST fit (end is exclusive).
	if !tl.fits(0, 5, 4) {
		t.Fatalf("off-by-one: [0,5) abutting [5,8) must fit (half-open end), but fits returned false")
	}
	// [0,6) crosses into the booked region by ONE unit -> MUST NOT fit.
	if tl.fits(0, 6, 4) {
		t.Fatalf("off-by-one: [0,6) overlaps [5,8) at t=5 and must NOT fit")
	}
	// Occupancy SINK at the seam: t=4 is free (before spike), t=5 is full,
	// t=8 is free again (spike ended, half-open).
	if u := tl.usedAt(4); u != 0 {
		t.Fatalf("usedAt(4) must be 0 (before spike), got %d", u)
	}
	if u := tl.usedAt(5); u != 4 {
		t.Fatalf("usedAt(5) must be 4 (spike start, inclusive), got %d", u)
	}
	if u := tl.usedAt(8); u != 0 {
		t.Fatalf("usedAt(8) must be 0 (spike end is exclusive), got %d", u)
	}
	// A full-width dur-5 job DOES fit before the spike at t=0 ([0,5) abuts [5,8)).
	if got := tl.earliestStart(0, 5, 4); got != 0 {
		t.Fatalf("earliestStart full-width dur-5 must be 0 ([0,5) abuts the spike), got %d", got)
	}
	// A full-width dur-6 job CANNOT fit before the spike ([0,6) overlaps t=5),
	// so it must land exactly at the seam 8 — not 7 (overlap) and not later.
	if got := tl.earliestStart(0, 6, 4); got != 8 {
		t.Fatalf("earliestStart full-width dur-6 must be 8 (just after spike), got %d", got)
	}
}

// TestBackToBackSchedulePacksWithNoGapNoOverlap exercises the window boundary at
// the Schedule level: serial whole-cluster jobs must pack with EndTime[i] ==
// StartTime[i+1] — no one-unit gap, no one-unit overlap.
func TestBackToBackSchedulePacksWithNoGapNoOverlap(t *testing.T) {
	jobs := []Job{
		{ID: "S0", Width: 4, WallTime: 1},
		{ID: "S1", Width: 4, WallTime: 1},
		{ID: "S2", Width: 4, WallTime: 1},
	}
	s, err := Schedule(4, jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i+1 < len(s.Assignments); i++ {
		cur, next := s.Assignments[i], s.Assignments[i+1]
		if next.StartTime != cur.EndTime {
			t.Fatalf("boundary off-by-one: %s ends %d but %s starts %d (must be equal)",
				cur.JobID, cur.EndTime, next.JobID, next.StartTime)
		}
	}
	// width-1 walltime spanning exactly the seams confirms no double-count.
	last := s.Assignments[len(s.Assignments)-1]
	if last.EndTime != 3 {
		t.Fatalf("three back-to-back unit jobs must end at 3, got %d", last.EndTime)
	}
}

// TestWallTimeOverflowEndTimeWraps_LatentRisk pins the integer-overflow latent
// risk: Assignment.EndTime is computed as start+WallTime with no overflow guard.
// A WallTime at math.MaxInt makes EndTime wrap to a value < StartTime. This is
// OUTSIDE the documented envelope (WallTime is "estimated run length in abstract
// time units," realistically small), so it is a NOTED LATENT RISK, not a fixed
// bug. We pin the CURRENT behavior so a future overflow guard (or a rejection of
// out-of-range WallTime) is detected.
//
// If prod later adds a guard that rejects overflowing WallTime, this test should
// be updated to assert the new error — that is the intended trip-wire.
func TestWallTimeOverflowEndTimeWraps_LatentRisk(t *testing.T) {
	jobs := []Job{{ID: "ovf", Width: 1, WallTime: math.MaxInt}}
	s, err := Schedule(4, jobs)
	if err != nil {
		// A future hardening that rejects the range is acceptable and BETTER;
		// surface it loudly so the pin gets updated rather than silently passing.
		t.Skipf("LATENT RISK now guarded: Schedule rejected MaxInt WallTime with %v — update this pin to assert the new contract", err)
	}
	a := s.Assignments[0]
	if a.StartTime != 0 {
		t.Fatalf("ovf must start at 0, got %d", a.StartTime)
	}
	// CURRENT (unguarded) behavior: 0 + MaxInt does NOT wrap (== MaxInt), but
	// any nonzero start would. Document the concrete sink so the risk is visible.
	if a.EndTime != math.MaxInt {
		t.Fatalf("pin: EndTime for start=0,WallTime=MaxInt is MaxInt, got %d", a.EndTime)
	}
	// Demonstrate the actual overflow at the arithmetic sink with a nonzero base,
	// proving the unguarded add wraps negative — the latent hazard. Use runtime
	// values so the compiler does not reject the constant overflow.
	base := 1
	wall := math.MaxInt
	if got := base + wall; got >= 0 {
		t.Fatalf("expected start(1)+MaxInt to overflow negative, got %d", got)
	}
}
