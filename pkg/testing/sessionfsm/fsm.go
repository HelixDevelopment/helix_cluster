// Package sessionfsm provides a finite-state machine for tracking the
// lifecycle of a test session: Idle -> Setup -> Running -> ChaosInject ->
// Verify -> Done (happy path), with a ->Failed escape from any non-terminal
// state. It is safe for concurrent use.
package sessionfsm

import (
	"fmt"
	"sync"
)

// ─── State ────────────────────────────────────────────────────────────────────

// State is a node in the session test lifecycle FSM.
type State int

const (
	// StateIdle is the initial state of every FSM created by NewFSM.
	StateIdle State = iota
	// StateSetup is entered when the test harness is being configured.
	StateSetup
	// StateRunning is entered once the session under test is live.
	StateRunning
	// StateChaosInject is entered when fault injection is active.
	StateChaosInject
	// StateVerify is entered when the test is asserting post-chaos invariants.
	StateVerify
	// StateDone is the successful terminal state.
	StateDone
	// StateFailed is the failure terminal state reachable from any non-terminal
	// state.
	StateFailed
)

// String renders State for logs and test failure messages.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "Idle"
	case StateSetup:
		return "Setup"
	case StateRunning:
		return "Running"
	case StateChaosInject:
		return "ChaosInject"
	case StateVerify:
		return "Verify"
	case StateDone:
		return "Done"
	case StateFailed:
		return "Failed"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// ─── Transition table ─────────────────────────────────────────────────────────

// legalTransitions encodes every legal FSM edge. Absent entries mean the
// transition is illegal. Terminal states (Done, Failed) have empty sets.
//
// Happy path: Idle -> Setup -> Running -> ChaosInject -> Verify -> Done.
// Escape: any non-terminal state -> Failed.
var legalTransitions = map[State]map[State]bool{
	StateIdle: {
		StateSetup:  true,
		StateFailed: true,
	},
	StateSetup: {
		StateRunning: true,
		StateFailed:  true,
	},
	StateRunning: {
		StateChaosInject: true,
		StateFailed:      true,
	},
	StateChaosInject: {
		StateVerify: true,
		StateFailed: true,
	},
	StateVerify: {
		StateDone:   true,
		StateFailed: true,
	},
	// Terminal states: no outgoing edges.
	StateDone:   {},
	StateFailed: {},
}

// ─── Errors ───────────────────────────────────────────────────────────────────

// IllegalTransitionError is returned by FSM.Advance when the requested
// transition is not present in the legal-transitions table. From and To are
// preserved for structured assertions in tests.
type IllegalTransitionError struct {
	From State
	To   State
}

func (e *IllegalTransitionError) Error() string {
	return fmt.Sprintf("sessionfsm: illegal transition %s -> %s", e.From, e.To)
}

// ─── FSM ──────────────────────────────────────────────────────────────────────

// FSM is a mutex-protected session-test state machine. The zero value is NOT
// valid; use NewFSM.
type FSM struct {
	mu        sync.Mutex
	state     State
	history   []State
	callbacks map[State][]func()
}

// NewFSM creates an FSM in StateIdle with an empty history. The initial state
// is recorded in history so callers can assert the full state sequence from
// the beginning.
func NewFSM() *FSM {
	return &FSM{
		state:     StateIdle,
		history:   []State{StateIdle},
		callbacks: make(map[State][]func()),
	}
}

// On registers fn to be called (synchronously, inside the FSM's lock release)
// every time the FSM enters state s. Multiple callbacks on the same state fire
// in registration order. Callbacks registered for a terminal state that has
// already been reached will not retroactively fire.
func (f *FSM) On(s State, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callbacks[s] = append(f.callbacks[s], fn)
}

// Advance attempts to move the FSM from its current state to `to`.
//
//   - Legal transition: state is updated, `to` is appended to history, and any
//     callbacks registered for `to` are fired (after the lock is released to
//     avoid deadlock if a callback calls back into the FSM).
//   - Illegal transition: returns *IllegalTransitionError with From/To set;
//     current state is left UNCHANGED.
func (f *FSM) Advance(to State) error {
	f.mu.Lock()
	from := f.state
	if !legalTransitions[from][to] {
		f.mu.Unlock()
		return &IllegalTransitionError{From: from, To: to}
	}
	f.state = to
	f.history = append(f.history, to)
	// Collect callbacks to fire outside the lock.
	cbs := make([]func(), len(f.callbacks[to]))
	copy(cbs, f.callbacks[to])
	f.mu.Unlock()

	for _, cb := range cbs {
		cb()
	}
	return nil
}

// Current returns the FSM's current state. Safe for concurrent use.
func (f *FSM) Current() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// History returns a snapshot copy of every state entered since creation,
// starting with StateIdle. The slice is safe to modify by the caller.
func (f *FSM) History() []State {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]State, len(f.history))
	copy(out, f.history)
	return out
}
