package console

import (
	"fmt"
	"sync"
	"time"
)

// BootState is a single state in the console node boot-coordination lifecycle
// (roadmap §4.3). A console node advances through these states as it is
// detected, provisioned, joins the mesh, becomes Ready, and is eventually
// drained and taken Offline.
type BootState string

const (
	// StatePowerOff is the initial state: the node is powered down or not yet
	// seen by the control plane.
	StatePowerOff BootState = "PowerOff"

	// StateDetected means the node hardware signature has been detected
	// (see Detector) but provisioning has not begun.
	StateDetected BootState = "Detected"

	// StateProvisioning means the node is being prepared (image, config,
	// attestation) prior to joining the cluster mesh.
	StateProvisioning BootState = "Provisioning"

	// StateJoining means the node is joining the SWIM/Raft mesh.
	StateJoining BootState = "Joining"

	// StateReady means the node has joined the mesh and is schedulable.
	StateReady BootState = "Ready"

	// StateDraining means the node is being gracefully removed from
	// scheduling and its workloads evacuated.
	StateDraining BootState = "Draining"

	// StateOffline is the terminal state: the node has left the cluster and
	// accepts no further transitions.
	StateOffline BootState = "Offline"
)

// allBootStates lists every valid BootState. It is used for validation only.
var allBootStates = map[BootState]struct{}{
	StatePowerOff:     {},
	StateDetected:     {},
	StateProvisioning: {},
	StateJoining:      {},
	StateReady:        {},
	StateDraining:     {},
	StateOffline:      {},
}

// legalBootTransitions is the adjacency map of the boot FSM. A transition from
// `from` to `to` is legal iff `to` appears in legalBootTransitions[from].
//
// The happy path is a strict forward march:
//
//	PowerOff -> Detected -> Provisioning -> Joining -> Ready -> Draining -> Offline
//
// A few recovery/early-exit edges are permitted (e.g. a node can be Drained
// from Ready, or fail provisioning straight to Offline), but illegal jumps such
// as PowerOff -> Ready are rejected. StateOffline is terminal: it has no
// outgoing edges.
var legalBootTransitions = map[BootState][]BootState{
	StatePowerOff:     {StateDetected},
	StateDetected:     {StateProvisioning, StateOffline},
	StateProvisioning: {StateJoining, StateOffline},
	StateJoining:      {StateReady, StateOffline},
	StateReady:        {StateDraining, StateOffline},
	StateDraining:     {StateOffline},
	StateOffline:      {}, // terminal
}

// TransitionEvent records a single state transition for audit / callback use.
type TransitionEvent struct {
	From BootState
	To   BootState
	At   time.Time
}

// TransitionGuard is an optional predicate evaluated before a transition is
// committed. Returning a non-nil error aborts the transition and leaves the
// machine's state unchanged. Guards must not block; they are invoked while the
// machine's internal lock is held.
type TransitionGuard func(from, to BootState) error

// TransitionHook is invoked exactly once after a transition has been committed.
// It receives the recorded event. Hooks are invoked while the machine's
// internal lock is held, so they must not call back into the BootMachine.
type TransitionHook func(ev TransitionEvent)

// IsValid reports whether s is a known BootState.
func (s BootState) IsValid() bool {
	_, ok := allBootStates[s]
	return ok
}

// IsTerminal reports whether s is a terminal state with no outgoing
// transitions.
func (s BootState) IsTerminal() bool {
	edges, ok := legalBootTransitions[s]
	return ok && len(edges) == 0
}

// String returns the string representation of the state.
func (s BootState) String() string { return string(s) }

// BootMachine is a concurrency-safe finite state machine modelling a console
// node's join/boot lifecycle. It enforces legal transitions only, runs an
// optional guard before each transition, timestamps every transition, records
// a history of events, and fires an optional hook on each committed
// transition.
//
// BootMachine deliberately performs NO real power/IPMI/BMC actions: the
// hardware actuation seam is out of scope and lives behind the FSM. Callers
// drive the machine in response to real-world events; the machine only tracks
// and validates the lifecycle.
type BootMachine struct {
	mu      sync.Mutex
	state   BootState
	history []TransitionEvent
	guard   TransitionGuard
	hook    TransitionHook
	now     func() time.Time
}

// NewBootMachine returns a BootMachine starting in StatePowerOff.
func NewBootMachine() *BootMachine {
	return &BootMachine{
		state: StatePowerOff,
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// SetGuard installs an optional transition guard, replacing any existing one.
// Passing nil removes the guard. Safe for concurrent use.
func (m *BootMachine) SetGuard(g TransitionGuard) {
	m.mu.Lock()
	m.guard = g
	m.mu.Unlock()
}

// SetHook installs an optional transition hook, replacing any existing one.
// Passing nil removes the hook. Safe for concurrent use.
func (m *BootMachine) SetHook(h TransitionHook) {
	m.mu.Lock()
	m.hook = h
	m.mu.Unlock()
}

// Current returns the current state. Safe for concurrent use.
func (m *BootMachine) Current() BootState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// CanTransition reports whether a transition from the current state to `to`
// is legal. It does NOT evaluate the guard (guards may have side-effect-free
// preconditions that depend on external state); it only checks the static
// adjacency map. Safe for concurrent use.
func (m *BootMachine) CanTransition(to BootState) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return isLegalBootTransition(m.state, to)
}

// isLegalBootTransition reports whether from->to is an edge in the FSM.
func isLegalBootTransition(from, to BootState) bool {
	if !to.IsValid() {
		return false
	}
	for _, dst := range legalBootTransitions[from] {
		if dst == to {
			return true
		}
	}
	return false
}

// IllegalTransitionError describes a rejected transition.
type IllegalTransitionError struct {
	From BootState
	To   BootState
}

func (e *IllegalTransitionError) Error() string {
	return fmt.Sprintf("illegal boot transition: %s -> %s", e.From, e.To)
}

// Transition attempts to move the machine to state `to`.
//
// It returns an *IllegalTransitionError (leaving state unchanged) if the
// transition is not a legal edge in the FSM — this includes any attempt to
// advance out of the terminal StateOffline. If a guard is installed and it
// returns an error, the transition is aborted, the guard's error is wrapped
// and returned, and the state is left unchanged. On success the new state is
// committed, an event is timestamped and appended to history, and the hook (if
// any) fires before the call returns. Safe for concurrent use.
func (m *BootMachine) Transition(to BootState) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	from := m.state
	if !isLegalBootTransition(from, to) {
		return &IllegalTransitionError{From: from, To: to}
	}

	if m.guard != nil {
		if err := m.guard(from, to); err != nil {
			return fmt.Errorf("boot transition %s -> %s guard rejected: %w", from, to, err)
		}
	}

	ev := TransitionEvent{From: from, To: to, At: m.now()}
	m.state = to
	m.history = append(m.history, ev)

	if m.hook != nil {
		m.hook(ev)
	}
	return nil
}

// History returns a copy of the recorded transition events in order. Safe for
// concurrent use.
func (m *BootMachine) History() []TransitionEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TransitionEvent, len(m.history))
	copy(out, m.history)
	return out
}
