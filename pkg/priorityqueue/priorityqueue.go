// Package priorityqueue implements a multifactor scheduler priority queue
// whose ordering is a COMPOSITE of age + fairshare + size + QoS.
//
// The defining property of this queue is that the composite priority is NOT
// frozen at insert time: it is recomputed against a logical "now" supplied at
// Pop time. This makes aging real — a job that has waited long enough can
// overtake a job that had a higher priority when it was first submitted.
//
// Composite priority is a documented, deterministic function:
//
//	priority(j, now) = qosBase(j.QoS)
//	                 + ageWeight     * (now - j.SubmitTick)   // aging term
//	                 + fairShareWeight * j.FairShare          // fairness term
//	                 - sizeWeight    * j.Size                 // size penalty
//
// Higher priority value == dequeued first. Ties are broken deterministically
// by earlier SubmitTick, then by lexicographically smaller ID, so the dequeue
// order is fully stable.
//
// The package is pure Go and deterministic: no time.Now(), no I/O, no
// goroutines. Logical time ("now") is injected by the caller as an int64 tick.
package priorityqueue

import (
	"fmt"
	"sort"
)

// QoS is a quality-of-service class. Higher base => scheduled sooner, all else
// being equal.
type QoS int

const (
	// QoSBestEffort is the lowest service class.
	QoSBestEffort QoS = iota
	// QoSBurstable is the middle service class.
	QoSBurstable
	// QoSGuaranteed is the highest service class.
	QoSGuaranteed
)

// qosBase maps a QoS class to its additive base contribution to priority.
// These are spaced so a single QoS step is meaningful but can still be
// overcome by sufficient aging — that interplay is exactly what the queue
// exists to model.
func qosBase(q QoS) int64 {
	switch q {
	case QoSGuaranteed:
		return 300
	case QoSBurstable:
		return 150
	case QoSBestEffort:
		return 0
	default:
		return 0
	}
}

// Job is a unit of schedulable work.
type Job struct {
	// ID uniquely identifies the job and is the final tie-breaker.
	ID string
	// SubmitTick is the logical time at which the job was enqueued. Aging is
	// measured as (now - SubmitTick).
	SubmitTick int64
	// FairShare is the fair-share weight of the job's owner; higher means the
	// owner is entitled to more service.
	FairShare int64
	// Size is a resource-cost proxy; larger jobs are penalized so that, all
	// else equal, smaller jobs schedule first (better packing / lower latency).
	Size int64
	// QoS is the quality-of-service class.
	QoS QoS
}

// Weights configures how strongly each factor contributes to the composite
// priority. Zero is a valid weight (it disables that factor entirely).
type Weights struct {
	Age       int64
	FairShare int64
	Size      int64
}

// DefaultWeights returns a balanced weighting in which aging meaningfully
// accumulates over time, fair-share matters, and size is a modest penalty.
func DefaultWeights() Weights {
	return Weights{Age: 2, FairShare: 5, Size: 1}
}

// Priority computes the composite priority of job j evaluated at logical time
// now, under weights w. It is exported so callers (and tests) can hand-compute
// the expected ordering against the exact same formula the queue uses.
//
// The (now - SubmitTick) age term is what makes ordering depend on now.
func Priority(w Weights, j Job, now int64) int64 {
	age := now - j.SubmitTick
	return qosBase(j.QoS) +
		w.Age*age +
		w.FairShare*j.FairShare -
		w.Size*j.Size
}

// Queue is a multifactor priority queue. It is NOT safe for concurrent use;
// callers must serialize access. Ordering is recomputed at Pop time against
// the supplied now, so priority is never frozen at insert.
type Queue struct {
	weights Weights
	jobs    []Job
}

// New returns an empty Queue using the given weights.
func New(w Weights) *Queue {
	return &Queue{weights: w}
}

// Len returns the number of jobs currently held.
func (q *Queue) Len() int { return len(q.jobs) }

// Push enqueues a job. It does not compute or store any priority — priority is
// always recomputed at Pop time.
func (q *Queue) Push(j Job) {
	q.jobs = append(q.jobs, j)
}

// less reports whether job a should be dequeued before job b at logical time
// now. Primary key: higher composite priority. Tie-breaks (fully
// deterministic): earlier SubmitTick, then lexicographically smaller ID.
func (q *Queue) less(a, b Job, now int64) bool {
	pa := Priority(q.weights, a, now)
	pb := Priority(q.weights, b, now)
	if pa != pb {
		return pa > pb
	}
	if a.SubmitTick != b.SubmitTick {
		return a.SubmitTick < b.SubmitTick
	}
	return a.ID < b.ID
}

// Pop removes and returns the highest composite-priority job evaluated at
// logical time now. The boolean is false (and the Job is zero) if the queue is
// empty. Because priority is recomputed here against now, aging that has
// accrued since Push is reflected in the choice.
func (q *Queue) Pop(now int64) (Job, bool) {
	if len(q.jobs) == 0 {
		return Job{}, false
	}
	best := 0
	for i := 1; i < len(q.jobs); i++ {
		if q.less(q.jobs[i], q.jobs[best], now) {
			best = i
		}
	}
	j := q.jobs[best]
	q.jobs = append(q.jobs[:best], q.jobs[best+1:]...)
	return j, true
}

// Order returns the full dequeue order at logical time now WITHOUT mutating the
// queue. Useful for inspection and for asserting the complete sequence.
func (q *Queue) Order(now int64) []Job {
	out := make([]Job, len(q.jobs))
	copy(out, q.jobs)
	sort.SliceStable(out, func(i, k int) bool {
		return q.less(out[i], out[k], now)
	})
	return out
}

// String renders a job for diagnostic logging.
func (j Job) String() string {
	return fmt.Sprintf("Job{id=%s submit=%d fair=%d size=%d qos=%d}",
		j.ID, j.SubmitTick, j.FairShare, j.Size, j.QoS)
}
