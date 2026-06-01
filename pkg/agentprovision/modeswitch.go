package agentprovision

import (
	"fmt"
	"sync"
)

// Mode classifies a job as either a background batch workload or a
// latency-sensitive interactive workload.
type Mode int

const (
	// ModeBatch represents a background / non-interactive workload.
	// Batch jobs may be preempted by incoming Interactive jobs when the
	// resource pool is saturated.
	ModeBatch Mode = iota

	// ModeInteractive represents a latency-sensitive, foreground workload.
	// Interactive jobs may preempt running Batch jobs to obtain a slot.
	ModeInteractive
)

// String returns a human-readable label for the mode.
func (m Mode) String() string {
	switch m {
	case ModeBatch:
		return "batch"
	case ModeInteractive:
		return "interactive"
	default:
		return fmt.Sprintf("mode(%d)", int(m))
	}
}

// Job describes a unit of work submitted to the ModeManager.
type Job struct {
	// ID is the unique identifier for this job.
	ID string
	// Mode is the current classification of the job (Batch or Interactive).
	Mode Mode
	// Preempted is set to true on the copy stored in the eviction ledger when
	// the job was forcibly removed from the pool to make room for an
	// Interactive job.
	Preempted bool
}

// admissionRecord pairs a Job with its admission sequence number, which is
// used to select the most-recently-admitted Batch victim deterministically.
type admissionRecord struct {
	job Job
	seq uint64 // monotonically increasing per Submit call
}

// ModeManager manages a fixed-capacity pool of running Jobs and enforces the
// preemption policy: an incoming Interactive job may evict the most-recently-
// admitted Batch job when every slot is occupied.
//
// All methods are safe for concurrent use.
type ModeManager struct {
	mu        sync.Mutex
	slots     int                        // total pool capacity
	running   map[string]*admissionRecord // jobID -> record
	evicted   map[string]Job             // eviction ledger: jobID -> final Job state
	admitSeq  uint64                     // counter; incremented on each successful admission
}

// NewModeManager creates a ModeManager with the given fixed pool capacity.
// slots must be >= 1; if a value < 1 is supplied it is silently clamped to 1.
func NewModeManager(slots int) *ModeManager {
	if slots < 1 {
		slots = 1
	}
	return &ModeManager{
		slots:   slots,
		running: make(map[string]*admissionRecord),
		evicted: make(map[string]Job),
	}
}

// Submit attempts to place j into the running pool.
//
// Decision logic:
//
//  1. Free slot available  → place j, return admitted=true.
//  2. Pool full + j is Interactive + at least one running job is Batch
//     → find the most-recently-admitted Batch job (highest seq), mark it
//     Preempted=true, remove it from the pool, place j in its slot, and
//     return admitted=true, preemptedID=<evicted batch id>.
//  3. Pool full + no Batch victim (all Interactive) OR j is Batch
//     → return admitted=false with a descriptive reason.
func (m *ModeManager) Submit(j Job) (admitted bool, preemptedID string, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Case 1: free slot.
	if len(m.running) < m.slots {
		m.admitSeq++
		m.running[j.ID] = &admissionRecord{job: j, seq: m.admitSeq}
		return true, "", ""
	}

	// Pool is full — preemption is only possible for Interactive arrivals.
	if j.Mode != ModeInteractive {
		return false, "", fmt.Sprintf(
			"rejected: pool full (%d/%d) and batch jobs never preempt", len(m.running), m.slots,
		)
	}

	// Find the most-recently-admitted Batch job (highest seq).
	victim := m.mostRecentBatch()
	if victim == nil {
		return false, "", fmt.Sprintf(
			"rejected: pool full (%d/%d) and no batch victim available (all slots hold interactive jobs)",
			len(m.running), m.slots,
		)
	}

	// Evict the victim: record it in the ledger, remove from pool.
	evictedJob := victim.job
	evictedJob.Preempted = true
	m.evicted[evictedJob.ID] = evictedJob
	delete(m.running, evictedJob.ID)

	// Place the new Interactive job in the freed slot.
	m.admitSeq++
	m.running[j.ID] = &admissionRecord{job: j, seq: m.admitSeq}

	return true, evictedJob.ID, ""
}

// mostRecentBatch returns a pointer to the admissionRecord of the running
// Batch job with the highest sequence number (most recently admitted).
// Returns nil if no Batch job is currently running.
// Caller must hold m.mu.
func (m *ModeManager) mostRecentBatch() *admissionRecord {
	var best *admissionRecord
	for _, rec := range m.running {
		if rec.job.Mode == ModeBatch {
			if best == nil || rec.seq > best.seq {
				best = rec
			}
		}
	}
	return best
}

// Complete removes the job identified by jobID from the running pool,
// freeing its slot.  Returns true if the job was found and removed, false
// if jobID was not in the pool (e.g., already completed or never admitted).
func (m *ModeManager) Complete(jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[jobID]
	if ok {
		delete(m.running, jobID)
	}
	return ok
}

// Running returns a snapshot copy of all currently running jobs.
// The caller may mutate the returned slice without affecting pool state.
func (m *ModeManager) Running() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.running))
	for _, rec := range m.running {
		out = append(out, rec.job)
	}
	return out
}

// SwitchMode reclassifies a running job's mode to newMode.
// Returns true if the job was found in the pool and its mode was changed,
// false if the job is not currently running (e.g., not yet admitted,
// already completed, or evicted) or if the mode is already newMode.
//
// Note: SwitchMode updates the live record; subsequent preemption decisions
// reflect the new classification immediately.
func (m *ModeManager) SwitchMode(jobID string, newMode Mode) (changed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.running[jobID]
	if !ok {
		return false
	}
	if rec.job.Mode == newMode {
		return false
	}
	rec.job.Mode = newMode
	return true
}

// WasPreempted reports whether the job identified by jobID was forcibly
// evicted from the pool by the preemption mechanism.  The ledger persists
// across the lifetime of the ModeManager so callers can inspect eviction
// history and decide whether to requeue the job.
func (m *ModeManager) WasPreempted(jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.evicted[jobID]
	return ok && j.Preempted
}
