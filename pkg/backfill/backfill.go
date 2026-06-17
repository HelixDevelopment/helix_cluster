package backfill

import "fmt"

// ErrInvalidCapacity is returned when Capacity is not positive.
var ErrInvalidCapacity = fmt.Errorf("backfill: capacity must be >= 1")

// ErrJobTooWide is returned when a job requests more units than the cluster
// has, making it permanently unschedulable.
var ErrJobTooWide = fmt.Errorf("backfill: job width exceeds cluster capacity")

// ErrInvalidJob is returned when a job has non-positive Width or WallTime.
var ErrInvalidJob = fmt.Errorf("backfill: job width and walltime must be >= 1")

// Schedule computes a conservative-backfill schedule for jobs against a cluster
// of the given capacity. Jobs MUST be supplied in priority order (highest
// priority first); FIFO breaks ties implicitly via slice order.
//
// Algorithm (conservative backfill):
//  1. Walk jobs in priority order. For each job, find its earliest start given
//     all previously committed reservations and IMMEDIATELY reserve it. Every
//     reservation, once made, is immovable.
//  2. Because a job is only ever placed at the earliest slot consistent with
//     the reservations already on the timeline, a lower-priority job is
//     "backfilled" exactly when its start lands earlier than a higher-priority
//     job that was forced to wait. It can never delay a higher-priority
//     reservation, since that reservation was committed to the timeline FIRST
//     and this job's window had to fit AROUND it.
//
// This ordering — reserve-then-let-the-rest-fit — is what gives the
// conservative guarantee for free: no reservation is ever recomputed.
//
// The returned Plan has one Assignment per input job in input order. An
// assignment is marked Backfilled when it starts strictly before the latest
// reserved start of any higher-priority job (i.e. it jumped the queue).
func Schedule(capacity int, jobs []Job) (*Plan, error) {
	if capacity < 1 {
		return nil, ErrInvalidCapacity
	}
	for _, j := range jobs {
		if j.Width < 1 || j.WallTime < 1 {
			return nil, fmt.Errorf("%w: job %q width=%d walltime=%d", ErrInvalidJob, j.ID, j.Width, j.WallTime)
		}
		if j.Width > capacity {
			return nil, fmt.Errorf("%w: job %q width=%d capacity=%d", ErrJobTooWide, j.ID, j.Width, capacity)
		}
	}

	tl := newTimeline(capacity)
	assignments := make([]Assignment, 0, len(jobs))

	// maxHigherStart tracks the largest reserved start among all
	// higher-priority jobs seen so far. A later (lower-priority) job that
	// starts before this value has demonstrably jumped the queue.
	maxHigherStart := -1

	for _, j := range jobs {
		start := tl.earliestStart(0, j.WallTime, j.Width)
		// Commit immediately and immovably.
		tl.reserve(start, j.WallTime, j.Width)

		backfilled := maxHigherStart >= 0 && start < maxHigherStart
		assignments = append(assignments, Assignment{
			JobID:      j.ID,
			StartTime:  start,
			EndTime:    addClamp(start, j.WallTime),
			Backfilled: backfilled,
		})
		if start > maxHigherStart {
			maxHigherStart = start
		}
	}

	return &Plan{Capacity: capacity, Assignments: assignments}, nil
}
