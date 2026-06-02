// Package jobadmit implements a Kueue-style suspend-then-admit tiered job
// admission controller as a pure-Go, deterministic, self-contained unit.
//
// Model: every submitted job starts SUSPENDED (it is never rejected outright).
// A job carries a Tier (e.g. Paid > Free), a Priority and an integer resource
// Request. A ClusterQuota bounds the total resources that may be admitted at
// once. An admission pass walks the suspended jobs in
//
//	(Tier desc, Priority desc, submit-order asc)
//
// order and admits each job only if its Request still fits the remaining quota.
// Jobs that do not fit stay SUSPENDED so that a later pass (e.g. after capacity
// is freed via Complete) can admit them. Resource accounting is exact: the sum
// of admitted Requests never exceeds Quota.Total.
package jobadmit

import "sort"

// Tier ranks a job's class of service. Higher numeric value == higher
// precedence at admission time (Paid outranks Free).
type Tier int

const (
	// TierFree is the lowest-precedence tier.
	TierFree Tier = iota
	// TierPaid outranks TierFree at admission time.
	TierPaid
)

// State is the lifecycle state of a job inside the Manager.
type State int

const (
	// StateSuspended means the job has been submitted but not yet admitted.
	StateSuspended State = iota
	// StateAdmitted means the job currently holds reserved quota.
	StateAdmitted
	// StateCompleted means the job finished and released its quota.
	StateCompleted
)

func (s State) String() string {
	switch s {
	case StateSuspended:
		return "Suspended"
	case StateAdmitted:
		return "Admitted"
	case StateCompleted:
		return "Completed"
	default:
		return "Unknown"
	}
}

// Job is an admission unit.
type Job struct {
	ID        string
	Tier      Tier
	Priority  int
	Request   int // resource units requested; must be >= 0
	SubmitSeq int // assigned by the Manager in submission order
}

// Quota bounds the total admissible resources.
type Quota struct {
	Total int
}

// Manager tracks submitted jobs and runs admission passes against a Quota.
//
// The zero value is not usable; construct one with NewManager.
type Manager struct {
	quota   Quota
	nextSeq int
	used    int
	jobs    map[string]*Job
	state   map[string]State
}

// NewManager returns a Manager bound to the given quota.
func NewManager(q Quota) *Manager {
	return &Manager{
		quota: q,
		jobs:  make(map[string]*Job),
		state: make(map[string]State),
	}
}

// Submit registers a job. The job always starts SUSPENDED (never rejected),
// even if its Request alone exceeds the quota. The Manager assigns the
// SubmitSeq in monotonic submission order. Submitting a duplicate ID or a
// negative Request is a no-op that returns false.
func (m *Manager) Submit(j Job) bool {
	if _, dup := m.jobs[j.ID]; dup {
		return false
	}
	if j.Request < 0 {
		return false
	}
	j.SubmitSeq = m.nextSeq
	m.nextSeq++
	stored := j
	m.jobs[j.ID] = &stored
	m.state[j.ID] = StateSuspended
	return true
}

// Admit runs a single admission pass. It considers every currently SUSPENDED
// job in (Tier desc, Priority desc, SubmitSeq asc) order and admits each one
// whose Request still fits the remaining quota (Quota.Total - Used). Jobs that
// do not fit remain SUSPENDED. It returns the IDs newly admitted in this pass,
// in admission order.
func (m *Manager) Admit() []string {
	pending := make([]*Job, 0, len(m.jobs))
	for id, st := range m.state {
		if st == StateSuspended {
			pending = append(pending, m.jobs[id])
		}
	}
	sort.Slice(pending, func(a, b int) bool {
		ja, jb := pending[a], pending[b]
		if ja.Tier != jb.Tier {
			return ja.Tier > jb.Tier // higher tier first
		}
		if ja.Priority != jb.Priority {
			return ja.Priority > jb.Priority // higher priority first
		}
		return ja.SubmitSeq < jb.SubmitSeq // earlier submission first
	})

	admitted := make([]string, 0, len(pending))
	for _, j := range pending {
		if m.used+j.Request <= m.quota.Total {
			m.used += j.Request
			m.state[j.ID] = StateAdmitted
			admitted = append(admitted, j.ID)
		}
	}
	return admitted
}

// Complete marks an ADMITTED job as completed and releases its reserved quota.
// It returns false if the job is unknown or is not currently admitted.
func (m *Manager) Complete(id string) bool {
	st, ok := m.state[id]
	if !ok || st != StateAdmitted {
		return false
	}
	m.used -= m.jobs[id].Request
	m.state[id] = StateCompleted
	return true
}

// State returns the current state of a job and whether it is known.
func (m *Manager) State(id string) (State, bool) {
	st, ok := m.state[id]
	return st, ok
}

// Used returns the total resources currently reserved by admitted jobs. It is
// an invariant that Used() <= Quota.Total at all times.
func (m *Manager) Used() int { return m.used }
