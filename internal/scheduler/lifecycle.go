package scheduler

import (
	"sync"
	"time"
)

// jobPhase is a node in the job lifecycle state machine. The legal transitions
// are:
//
//	scheduled -> running -> succeeded   (happy path)
//	scheduled -> running -> failed      (execution error)
//	scheduled -> cancelled              (cancelled before it ran)
//	running   -> cancelled              (cancelled mid-flight)
//
// succeeded, failed and cancelled are terminal: once reached the machine emits
// no further transitions and closes any open event streams.
type jobPhase string

const (
	phaseScheduled jobPhase = "scheduled"
	phaseRunning   jobPhase = "running"
	phaseSucceeded jobPhase = "succeeded"
	phaseFailed    jobPhase = "failed"
	phaseCancelled jobPhase = "cancelled"
	phasePreempted jobPhase = "preempted"
)

// isTerminal reports whether a phase ends the lifecycle.
func (p jobPhase) isTerminal() bool {
	switch p {
	case phaseSucceeded, phaseFailed, phaseCancelled, phasePreempted:
		return true
	default:
		return false
	}
}

// transitionMsg gives a human-readable message for each phase, surfaced to end
// users over the event stream.
func transitionMsg(p jobPhase) string {
	switch p {
	case phaseScheduled:
		return "job scheduled"
	case phaseRunning:
		return "job running"
	case phaseSucceeded:
		return "job succeeded"
	case phaseFailed:
		return "job failed"
	case phaseCancelled:
		return "job cancelled"
	case phasePreempted:
		return "job preempted"
	default:
		return string(p)
	}
}

// transition is one recorded step in a job's lifecycle. The Timestamp is the
// unix-second clock at which the transition occurred and is guaranteed to be
// strictly non-decreasing across a single job's transitions (see record).
type transition struct {
	Phase     jobPhase
	Message   string
	Timestamp int64
}

// jobLifecycle is the real, in-memory state machine that drives a job through
// its phases and fans out each transition to any number of live event streams.
//
// It is the single source of truth for StreamJobEvents: the stream replays the
// history recorded so far and then blocks on a per-subscriber channel for live
// transitions, closing when a terminal phase is recorded. There is no
// sleep-faked event generation — every emitted event corresponds to an actual
// recorded state change.
type jobLifecycle struct {
	jobID string

	mu          sync.Mutex
	current     jobPhase
	history     []transition
	lastTS      int64
	subscribers map[int]chan transition
	nextSubID   int
	closed      bool
	// nowUnix is the clock, injectable for deterministic tests.
	nowUnix func() int64
}

// newJobLifecycle creates a lifecycle seeded in the scheduled phase, recording
// the initial scheduled transition so a stream opened immediately still sees it.
func newJobLifecycle(jobID string) *jobLifecycle {
	return newJobLifecycleClock(jobID, func() int64 { return time.Now().Unix() })
}

// newJobLifecycleClock is like newJobLifecycle but with an injectable clock so
// tests can assert strictly-increasing timestamps without wall-clock flakiness.
func newJobLifecycleClock(jobID string, nowUnix func() int64) *jobLifecycle {
	l := &jobLifecycle{
		jobID:       jobID,
		subscribers: make(map[int]chan transition),
		nowUnix:     nowUnix,
	}
	l.recordLocked(phaseScheduled)
	return l
}

// advanceTo moves the machine to the next phase if the transition is legal and
// the machine is not already terminal. It returns true if a transition was
// recorded. Illegal or post-terminal transitions are no-ops returning false,
// which keeps the machine honest: you cannot, e.g., go running->scheduled or
// emit events after succeeded.
func (l *jobLifecycle) advanceTo(next jobPhase) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.current.isTerminal() {
		return false
	}
	if !legalTransition(l.current, next) {
		return false
	}
	l.recordLocked(next)
	return true
}

// legalTransition encodes the edges of the state machine.
func legalTransition(from, to jobPhase) bool {
	switch from {
	case phaseScheduled:
		return to == phaseRunning || to == phaseCancelled || to == phasePreempted
	case phaseRunning:
		return to == phaseSucceeded || to == phaseFailed || to == phaseCancelled || to == phasePreempted
	default:
		return false
	}
}

// recordLocked appends a transition, enforces strictly-increasing timestamps,
// fans it out to subscribers, and closes the machine on a terminal phase.
// Caller must hold l.mu.
func (l *jobLifecycle) recordLocked(p jobPhase) {
	ts := l.nowUnix()
	// Guarantee a strictly increasing per-job timeline even when several
	// transitions land within the same wall-clock second. End users (and the
	// ordering assertions in tests) rely on monotonic timestamps.
	if ts <= l.lastTS {
		ts = l.lastTS + 1
	}
	l.lastTS = ts

	tr := transition{Phase: p, Message: transitionMsg(p), Timestamp: ts}
	l.current = p
	l.history = append(l.history, tr)

	for _, ch := range l.subscribers {
		// Buffered channels sized to the max number of transitions, so a send
		// never blocks the state machine on a slow consumer.
		ch <- tr
	}

	if p.isTerminal() {
		l.closed = true
		for id, ch := range l.subscribers {
			close(ch)
			delete(l.subscribers, id)
		}
	}
}

// subscribe returns the history recorded so far plus a channel of future
// transitions and an unsubscribe func. If the machine is already terminal the
// returned channel is closed (nil receive), so the caller drains history and
// exits. The channel is buffered large enough to hold every remaining
// transition so recordLocked never blocks.
func (l *jobLifecycle) subscribe() (history []transition, ch <-chan transition, cancel func()) {
	l.mu.Lock()
	defer l.mu.Unlock()

	hist := make([]transition, len(l.history))
	copy(hist, l.history)

	if l.closed {
		c := make(chan transition)
		close(c)
		return hist, c, func() {}
	}

	// At most three further transitions can occur from any non-terminal phase
	// (running, then one of succeeded/failed/cancelled/preempted). Size generously.
	c := make(chan transition, 4)
	id := l.nextSubID
	l.nextSubID++
	l.subscribers[id] = c

	return hist, c, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if sub, ok := l.subscribers[id]; ok {
			delete(l.subscribers, id)
			close(sub)
		}
	}
}

// phase returns the current phase (for tests / status reporting).
func (l *jobLifecycle) phase() jobPhase {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.current
}
