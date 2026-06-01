// Package main — lifecycle.go implements the ephemeral node onboarding
// lifecycle state machine for HXC-1125.
//
// Valid transition path:
//
//	Idle → Provisioning → Configured → Joined → Ready → Draining → Stopped
//
// Any transition not listed in allowedTransitions is rejected with an error.
// All transitions are recorded in the History slice for audit/debug purposes.
package main

import (
	"fmt"
	"sync"
	"time"
)

// State is the onboarding lifecycle state of a node.
type State string

const (
	// StateIdle is the zero/initial state before onboarding begins.
	StateIdle State = "Idle"
	// StateProvisioning — hardware has been detected, config generation in progress.
	StateProvisioning State = "Provisioning"
	// StateConfigured — node config YAML has been written to disk.
	StateConfigured State = "Configured"
	// StateJoined — the node has joined the WireGuard mesh.
	StateJoined State = "Joined"
	// StateReady — the node is fully operational.
	StateReady State = "Ready"
	// StateDraining — the node is gracefully removing itself from the cluster.
	StateDraining State = "Draining"
	// StateStopped — the node has stopped; terminal state.
	StateStopped State = "Stopped"
)

// allowedTransitions defines the legal next states for each source state.
// Every (from, to) pair NOT in this map is an illegal transition.
var allowedTransitions = map[State][]State{
	StateIdle:         {StateProvisioning},
	StateProvisioning: {StateConfigured},
	StateConfigured:   {StateJoined},
	StateJoined:       {StateReady},
	StateReady:        {StateDraining},
	StateDraining:     {StateStopped},
	StateStopped:      {}, // terminal — no further transitions
}

// Transition records a single state change with its timestamp.
type Transition struct {
	From State
	To   State
	At   time.Time
}

// Lifecycle is the state machine tracking an onboarding node's progress from
// Idle through to Ready (or Stopped on shutdown). It is safe for concurrent use.
type Lifecycle struct {
	mu      sync.RWMutex
	current State
	History []Transition
}

// NewLifecycle creates a Lifecycle in the initial Idle state.
func NewLifecycle() *Lifecycle {
	return &Lifecycle{current: StateIdle}
}

// Current returns the current state.
func (l *Lifecycle) Current() State {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.current
}

// Advance transitions the lifecycle to the target state. It returns an error
// when the transition is illegal (i.e., not listed in allowedTransitions for
// the current state). Legal transitions are appended to History.
//
// Anti-bluff: this function returns a non-nil error for illegal transitions —
// the test for Provisioning→Stopped MUST receive an error, not nil.
func (l *Lifecycle) Advance(to State) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	allowed, ok := allowedTransitions[l.current]
	if !ok {
		// Unknown source state — should not happen but guard against it.
		return fmt.Errorf("lifecycle: unknown source state %q", l.current)
	}

	for _, s := range allowed {
		if s == to {
			l.History = append(l.History, Transition{
				From: l.current,
				To:   to,
				At:   time.Now(),
			})
			l.current = to
			return nil
		}
	}

	return fmt.Errorf("lifecycle: illegal transition %s → %s", l.current, to)
}
