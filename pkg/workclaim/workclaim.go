// Package workclaim implements SKIP LOCKED-style optimistic work-claiming.
//
// It models a shared queue of tasks that many concurrent workers drain. Each
// worker repeatedly calls Claim, which atomically hands out exactly one
// currently-unclaimed task (locking it to the caller) or reports that no
// unclaimed task remains. This mirrors the semantics of an SQL
//
//	SELECT ... FOR UPDATE SKIP LOCKED
//
// pattern: a task that has been claimed by one worker is invisible (skipped)
// to every other worker, so under arbitrary contention each task is claimed
// EXACTLY ONCE — never twice, never dropped.
//
// The Queue is safe for concurrent use by multiple goroutines. All claim
// bookkeeping is guarded by a single mutex; the critical decision (selecting
// and marking a task) is performed while the lock is held, which is precisely
// what guarantees the exactly-once property.
package workclaim

import (
	"errors"
	"sync"
)

// state describes the lifecycle of a single task inside the queue.
type state uint8

const (
	stateUnclaimed state = iota // available to be claimed
	stateClaimed                // handed out to exactly one worker
	stateCompleted              // worker finished the claimed task
)

// ErrUnknownTask is returned by Complete when the task id was never Added.
var ErrUnknownTask = errors.New("workclaim: unknown task id")

// ErrNotClaimed is returned by Complete when the task exists but is not in the
// claimed state (either still unclaimed, or already completed).
var ErrNotClaimed = errors.New("workclaim: task is not in claimed state")

// Queue is a shared pool of tasks that concurrent workers drain via Claim.
// The zero value is not usable; construct with New.
type Queue struct {
	mu sync.Mutex

	// order preserves insertion order so Claim hands out tasks
	// deterministically (FIFO over unclaimed tasks) for any single-threaded
	// observer. Concurrent interleaving is still nondeterministic across
	// goroutines, but the exactly-once invariant holds regardless.
	order []string

	// st maps task id -> lifecycle state.
	st map[string]state

	// next is the index into order at/after which the first possibly-unclaimed
	// task may live. Tasks before next are guaranteed claimed-or-completed,
	// which keeps Claim amortized O(1) instead of rescanning from zero.
	next int
}

// New returns an empty Queue ready for concurrent use.
func New() *Queue {
	return &Queue{
		st: make(map[string]state),
	}
}

// Add registers a task by id in the unclaimed state. Re-adding an id that is
// already present is a no-op (it does not reset an already-claimed task and
// does not create a duplicate slot), so Add is idempotent on id.
func (q *Queue) Add(taskID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.st[taskID]; ok {
		return
	}
	q.st[taskID] = stateUnclaimed
	q.order = append(q.order, taskID)
}

// Claim atomically selects one currently-unclaimed task, marks it claimed, and
// returns its id with ok == true. If no unclaimed task remains it returns
// ("", false). Because the selection and the state mutation happen together
// under the queue lock, two concurrent callers can never receive the same id —
// this is the SKIP LOCKED guarantee.
func (q *Queue) Claim() (taskID string, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i := q.next; i < len(q.order); i++ {
		id := q.order[i]
		if q.st[id] == stateUnclaimed {
			// Mark BEFORE releasing the lock so no concurrent Claim can see
			// this task as unclaimed. Removing this mutation (or moving it
			// outside the lock) reintroduces duplicate claims.
			q.st[id] = stateClaimed
			q.next = i + 1
			return id, true
		}
		// Anything we skip here is claimed/completed; advancing next past a
		// leading run of such tasks keeps future scans short.
		if i == q.next {
			q.next = i + 1
		}
	}
	return "", false
}

// Complete transitions a previously-claimed task to the completed state. It
// returns ErrUnknownTask if the id was never Added, or ErrNotClaimed if the
// task is not currently in the claimed state.
func (q *Queue) Complete(taskID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	cur, ok := q.st[taskID]
	if !ok {
		return ErrUnknownTask
	}
	if cur != stateClaimed {
		return ErrNotClaimed
	}
	q.st[taskID] = stateCompleted
	return nil
}

// Claimed returns the number of tasks currently in the claimed state (claimed
// but not yet completed).
func (q *Queue) Claimed() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, s := range q.st {
		if s == stateClaimed {
			n++
		}
	}
	return n
}

// Unclaimed returns the number of tasks still available to be claimed.
func (q *Queue) Unclaimed() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, s := range q.st {
		if s == stateUnclaimed {
			n++
		}
	}
	return n
}

// Completed returns the number of tasks that have been completed.
func (q *Queue) Completed() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, s := range q.st {
		if s == stateCompleted {
			n++
		}
	}
	return n
}

// Len returns the total number of distinct tasks ever Added.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.order)
}
