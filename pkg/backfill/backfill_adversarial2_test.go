package backfill

// Adversarial pass 2 for pkg/backfill.
//
// The prior pass (backfill_adversarial_test.go) NOTED a "WallTime+start
// overflow" as a LATENT RISK and pinned only the start=0 case, where 0+MaxInt
// does not actually wrap. This pass establishes that the overflow is in fact an
// ALWAYS-ON CORRECTNESS BUG the moment a *nonzero* start participates — which
// happens whenever a second whole-cluster job has to serialize after a job with
// a near-MaxInt WallTime, or whenever fits() evaluates a wide+long candidate
// window. Two concrete, fully-deterministic sinks:
//
//   (1) fits() MISADMIT — fits(start, duration, width) computes
//       end = start + duration. On overflow `end` wraps NEGATIVE, so the
//       interior-boundary guard `if b >= end { break }` is trivially true for
//       every boundary and the loop checks NOTHING. fits() then returns true for
//       a window that fully engulfs a capacity-saturating spike. The scheduler
//       would place a job on top of a fully-booked region — a hard
//       capacity-invariant violation (admits a job that must be rejected).
//
//   (2) reserve()/Schedule() TIMELINE CORRUPTION — the same wrapped `end` is
//       written as a delta-map KEY. A negative key sorts before t=0, so the
//       running prefix sum (usedAt) goes NEGATIVE across the whole timeline and
//       the placed spike vanishes from occupancy. Likewise Assignment.EndTime
//       wraps below StartTime, yielding a non-monotone [start,end) window.
//
// FIX LEFT IN PRODUCTION: addClamp (timeline.go) saturates start+duration at
// math.MaxInt instead of wrapping, applied in fits(), reserve(), and the
// EndTime computation in Schedule(). For in-envelope inputs it is a no-op; at
// the boundary it keeps `end` monotone (>= start) so neither corruption occurs.
//
// Each test below FAILS on the pre-fix (wrapping) arithmetic and PASSES on the
// clamped arithmetic — they are mutation-proving reproducers, not pins.

import (
	"math"
	"sync"
	"testing"
)

// TestFitsDoesNotMisadmitOnWindowOverflow is the load-bearing reproducer. A
// capacity-saturating spike is booked far out on the timeline; a candidate
// window starts well before it but is long enough that start+duration overflows
// int. The window CONTAINS the full-cluster spike and requests the whole
// cluster, so fits() MUST return false. Pre-fix the wrapped (negative) end made
// the boundary loop skip the spike and fits() wrongly returned true.
//
// MUTATION PROOF: revert addClamp in fits() to `end := start + duration` and
// this test fails with "MISADMIT".
func TestFitsDoesNotMisadmitOnWindowOverflow(t *testing.T) {
	tl := newTimeline(4)
	spikeAt := math.MaxInt - 100
	tl.reserve(spikeAt, 50, 4) // [MaxInt-100, MaxInt-50) fully booked (4/4)

	start := 100
	dur := math.MaxInt // start+dur overflows negative without saturation
	// Sanity: this window engulfs the spike (spikeAt is well inside [start, end)).
	if got := tl.fits(start, dur, 4); got {
		t.Fatalf("MISADMIT: fits(start=%d,dur=MaxInt,w=4) returned true, but the window engulfs a full-cluster spike at [%d,%d) and must NOT fit (overflow let the boundary check be skipped)",
			start, spikeAt, spikeAt+50)
	}

	// Control: a window that legitimately ends BEFORE the spike still fits, so
	// the clamp did not turn fits() into a blanket reject.
	if !tl.fits(0, 10, 4) {
		t.Fatalf("control: short early window [0,10) must still fit on an otherwise-idle prefix")
	}
}

// TestReserveDoesNotCorruptTimelineOnOverflow proves the delta-map sink. A
// reserve whose end overflows must NOT plant a negative-keyed delta that drives
// occupancy negative. With the clamp, end saturates to MaxInt, the spike's
// occupancy is real and positive, and usedAt never goes below zero anywhere.
//
// MUTATION PROOF: revert addClamp in reserve() and usedAt(0) becomes -4.
func TestReserveDoesNotCorruptTimelineOnOverflow(t *testing.T) {
	tl := newTimeline(4)
	start := 100
	dur := math.MaxInt
	tl.reserve(start, dur, 4)

	// No boundary key may be negative — a negative key corrupts the prefix sum.
	for k := range tl.delta {
		if k < 0 {
			t.Fatalf("reserve planted a negative delta key %d (overflowed end) — corrupts usedAt across the whole timeline", k)
		}
	}
	// Occupancy must never read negative.
	for _, probe := range []int{0, 50, 100, start} {
		if u := tl.usedAt(probe); u < 0 {
			t.Fatalf("usedAt(%d)=%d is negative — occupancy corrupted by an overflowed reserve", probe, u)
		}
	}
	// The spike is real: at its start the cluster reads fully busy.
	if u := tl.usedAt(start); u != 4 {
		t.Fatalf("usedAt(start=%d) must be 4 (spike committed), got %d", start, u)
	}
}

// TestScheduleSerialBigJobsKeepEndMonotoneAndNonOverlapping drives the bug
// through the PUBLIC Schedule API. Two whole-cluster jobs must serialize; the
// first has a near-MaxInt WallTime so the second is forced to a huge start at
// which start+WallTime overflows. Sink-side invariants that MUST hold:
//   - every EndTime >= StartTime (monotone half-open window), and
//   - on a full-width cluster the two jobs do not overlap (B.start >= A.end).
//
// MUTATION PROOF: revert addClamp in Schedule()'s EndTime and B.EndTime wraps
// negative (< B.StartTime), failing the monotonicity assertion.
func TestScheduleSerialBigJobsKeepEndMonotoneAndNonOverlapping(t *testing.T) {
	jobs := []Job{
		{ID: "A", Width: 4, WallTime: math.MaxInt - 3},
		{ID: "B", Width: 4, WallTime: 10},
	}
	s, err := Schedule(4, jobs)
	if err != nil {
		t.Skipf("LATENT RISK now guarded at input validation: Schedule rejected the range with %v — update this reproducer to assert the new contract", err)
	}
	a0, b0 := s.Assignments[0], s.Assignments[1]
	for _, a := range s.Assignments {
		if a.EndTime < a.StartTime {
			t.Fatalf("OVERFLOW: %s window [%d,%d) is non-monotone (end < start) — start+WallTime wrapped", a.JobID, a.StartTime, a.EndTime)
		}
	}
	// Full-width cluster: A and B cannot coexist; B must begin at/after A's end.
	if b0.StartTime < a0.EndTime {
		t.Fatalf("MISADMIT overlap: B.start=%d < A.end=%d on a full-width (4/4) cluster", b0.StartTime, a0.EndTime)
	}
}

// TestEarliestStartOverflowDoesNotPlaceOverSpike guards earliestStart against
// the overflow-misadmit at a NONZERO candidate. Two spikes are booked: a wide
// early one and a wide late one. The only honest placement for a long
// whole-cluster job is after BOTH. The candidate set includes the early spike's
// end as a NONZERO start; at that start fits() computes start+MaxInt which wraps
// negative and (pre-fix) falsely reports a fit, so earliestStart returns a slot
// that sits ON TOP of the later spike.
//
// MUTATION PROOF: revert addClamp in fits() and earliestStart returns
// earlyEnd (40), which overlaps the later full-cluster spike — this test then
// fails the "must not start before lateSpike end" assertion.
func TestEarliestStartOverflowDoesNotPlaceOverSpike(t *testing.T) {
	tl := newTimeline(4)
	tl.reserve(10, 30, 4) // early spike  [10,40)  full cluster
	const earlyEnd = 40
	lateSpike := math.MaxInt - 100
	tl.reserve(lateSpike, 50, 4) // late spike [MaxInt-100, MaxInt-50) full cluster

	// A whole-cluster job of duration MaxInt. The earliest HONEST start is at or
	// after the late spike's end; it can never be the early candidate 40, because
	// a window [40, 40+MaxInt) engulfs the late full-cluster spike.
	got := tl.earliestStart(0, math.MaxInt, 4)
	if got < lateSpike+50 {
		t.Fatalf("earliestStart=%d places a full-cluster job over the late spike at [%d,%d) — overflow made fits() admit a wrapping window (expected start >= %d)",
			got, lateSpike, lateSpike+50, lateSpike+50)
	}
}

// --- Belt-and-suspenders: confirm the fix is invisible to normal scheduling ---

// TestClampNoOpInEnvelope verifies that for ordinary (small) inputs the clamped
// arithmetic is byte-identical to plain addition — the fix must not perturb any
// in-envelope placement.
func TestClampNoOpInEnvelope(t *testing.T) {
	jobs := []Job{
		{ID: "A", Width: 2, WallTime: 6},
		{ID: "B", Width: 4, WallTime: 5},
		{ID: "C", Width: 2, WallTime: 4},
		{ID: "D", Width: 1, WallTime: 7},
	}
	s, err := Schedule(4, jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range s.Assignments {
		if a.EndTime != a.StartTime+jobWall(jobs, a.JobID) {
			t.Fatalf("clamp perturbed an in-envelope EndTime for %s: got %d want %d", a.JobID, a.EndTime, a.StartTime+jobWall(jobs, a.JobID))
		}
	}
}

func jobWall(jobs []Job, id string) int {
	for _, j := range jobs {
		if j.ID == id {
			return j.WallTime
		}
	}
	return -1
}

// TestConcurrentScheduleWithOverflowJobRaceFree re-confirms race-freedom (the
// scheduler allocates a fresh timeline per call) while the overflow path is
// exercised, so the clamp introduces no shared mutable state. Run with -race.
func TestConcurrentScheduleWithOverflowJobRaceFree(t *testing.T) {
	jobs := []Job{
		{ID: "A", Width: 4, WallTime: math.MaxInt - 3},
		{ID: "B", Width: 4, WallTime: 10},
	}
	golden, err := Schedule(4, jobs)
	if err != nil {
		t.Skipf("range guarded: %v", err)
	}
	const goroutines = 32
	var wg sync.WaitGroup
	bad := make(chan string, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Schedule(4, jobs)
			if err != nil {
				bad <- "unexpected error: " + err.Error()
				return
			}
			for i := range s.Assignments {
				if s.Assignments[i] != golden.Assignments[i] {
					bad <- "divergent Plan under concurrency"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(bad)
	if msg, ok := <-bad; ok {
		t.Fatalf("concurrent overflow-path schedule failure: %s", msg)
	}
}
