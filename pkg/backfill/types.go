// Package backfill implements SLURM-style conservative backfill scheduling
// over a discrete resource timeline.
//
// The model: a cluster exposes a fixed Capacity of fungible resource units
// (e.g. CPU cores or whole nodes). A priority-ordered queue of jobs each
// requests some number of units (Width) for an estimated WallTime. The
// scheduler walks the queue in priority order; for each job it finds the
// earliest start at which Width units are free for the whole WallTime and
// "reserves" that window. Lower-priority jobs may then be BACKFILLED into
// earlier idle capacity, but ONLY if doing so cannot delay any
// already-computed reservation — this is the conservative-backfill
// guarantee that distinguishes it from EASY (aggressive) backfill, which
// only protects the single highest-priority reservation.
package backfill

// Job is a unit of work to be scheduled on the resource timeline.
//
// Jobs are presented to Schedule in PRIORITY ORDER (index 0 is highest
// priority). Ties are broken by queue position (FIFO), matching SLURM's
// "priority then submit order" convention.
type Job struct {
	// ID uniquely identifies the job within a scheduling round.
	ID string
	// Width is the number of resource units the job needs simultaneously
	// for its entire run. Must be >= 1 and <= cluster Capacity.
	Width int
	// WallTime is the estimated run length in abstract time units. Must be
	// >= 1. The job occupies [StartTime, StartTime+WallTime).
	WallTime int
}

// Assignment is the scheduled placement of a single job.
type Assignment struct {
	// JobID is the Job.ID this assignment is for.
	JobID string
	// StartTime is the instant the job begins (inclusive).
	StartTime int
	// EndTime is StartTime + WallTime (exclusive).
	EndTime int
	// Backfilled is true when the job ran earlier than strict FIFO order
	// would have allowed, i.e. it jumped ahead of a higher-priority job
	// that was still waiting for resources.
	Backfilled bool
}

// Plan is the result of a scheduling round: one Assignment per input job,
// in the SAME order the jobs were supplied (priority order).
type Plan struct {
	// Capacity is the cluster width the schedule was computed against.
	Capacity int
	// Assignments holds one entry per input job, in input (priority) order.
	Assignments []Assignment
}

// AssignmentFor returns the assignment for the given job ID and whether it was
// found.
func (s *Plan) AssignmentFor(jobID string) (Assignment, bool) {
	for _, a := range s.Assignments {
		if a.JobID == jobID {
			return a, true
		}
	}
	return Assignment{}, false
}
