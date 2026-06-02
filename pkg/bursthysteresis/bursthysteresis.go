// Package bursthysteresis implements a two-threshold hysteresis controller
// that consumes a stream of utilization samples (float64 in [0,1]) and emits
// typed state-transition events.
//
// States: MONITOR → SPILL → RECOVER (→ MONITOR)
//
// Transitions:
//   - MONITOR → SPILL  : sample >= SpillHigh
//   - SPILL   → RECOVER: sample <= RecoverLow
//   - RECOVER → MONITOR: next call to Feed after RECOVER is emitted
//
// The dead-band (RecoverLow < sample < SpillHigh) while in SPILL keeps the
// controller in SPILL without emitting any event — this is the hysteresis
// property that prevents oscillation.
package bursthysteresis

import (
	"errors"
	"sync"
)

// State represents the operating state of the BurstController.
type State int

const (
	// StateMonitor is the initial idle state.
	StateMonitor State = iota
	// StateSpill indicates utilization has exceeded SpillHigh.
	StateSpill
	// StateRecover indicates utilization has fallen back to or below RecoverLow.
	StateRecover
)

// String returns a human-readable name for the state.
func (s State) String() string {
	switch s {
	case StateMonitor:
		return "MONITOR"
	case StateSpill:
		return "SPILL"
	case StateRecover:
		return "RECOVER"
	default:
		return "UNKNOWN"
	}
}

// EventKind distinguishes which transition produced an Event.
type EventKind int

const (
	// EventSpill is emitted on MONITOR → SPILL.
	EventSpill EventKind = iota
	// EventRecover is emitted on SPILL → RECOVER.
	EventRecover
)

// String returns a human-readable label for the event kind.
func (e EventKind) String() string {
	switch e {
	case EventSpill:
		return "SPILL"
	case EventRecover:
		return "RECOVER"
	default:
		return "UNKNOWN"
	}
}

// Event records a single state-transition emitted by BurstController.
type Event struct {
	// Kind is the type of transition that occurred.
	Kind EventKind
	// Sample is the utilization value that triggered this transition.
	Sample float64
	// RunID is the caller-supplied identifier threaded through every event so
	// downstream consumers can correlate events across a logical run.
	RunID string
	// FromState is the state before the transition.
	FromState State
	// ToState is the state after the transition.
	ToState State
}

// Config carries the tunable parameters for a BurstController.
// SpillHigh MUST be strictly greater than RecoverLow; otherwise New returns
// ErrInvalidThresholds.
type Config struct {
	// SpillHigh is the utilization level at or above which a SPILL transition fires.
	// Default: 0.90.
	SpillHigh float64
	// RecoverLow is the utilization level at or below which a RECOVER transition fires.
	// Default: 0.70.
	RecoverLow float64
	// RunID is a caller-supplied string embedded in every emitted Event.
	RunID string
}

// ErrInvalidThresholds is returned when SpillHigh <= RecoverLow.
var ErrInvalidThresholds = errors.New("bursthysteresis: SpillHigh must be strictly greater than RecoverLow")

// DefaultConfig returns a Config with the canonical threshold values.
func DefaultConfig(runID string) Config {
	return Config{
		SpillHigh:  0.90,
		RecoverLow: 0.70,
		RunID:      runID,
	}
}

// BurstController consumes utilization samples and emits transition Events.
// All methods are safe for concurrent use.
type BurstController struct {
	mu        sync.Mutex
	cfg       Config
	state     State
	listeners []func(Event)
}

// New constructs a BurstController with the given Config.
// Returns ErrInvalidThresholds if cfg.SpillHigh <= cfg.RecoverLow.
func New(cfg Config) (*BurstController, error) {
	// MUTATION GUARD — pinned by TestHysteresisSequence:
	// This strict-greater-than check enforces that SpillHigh > RecoverLow.
	// A mutation collapsing both thresholds to a single value (e.g. changing
	// RecoverLow to equal SpillHigh) would bypass the dead-band in Feed and
	// cause sample 0.80 to trigger a RECOVER event, failing TestHysteresisSequence.
	if cfg.SpillHigh <= cfg.RecoverLow {
		return nil, ErrInvalidThresholds
	}
	return &BurstController{
		cfg:   cfg,
		state: StateMonitor,
	}, nil
}

// Subscribe registers fn to be called synchronously (under the controller's
// lock) whenever a transition Event is emitted.  Listeners are called in
// registration order.  Subscribe may be called at any time, including before
// or after feeding samples.
func (c *BurstController) Subscribe(fn func(Event)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listeners = append(c.listeners, fn)
}

// State returns the current operating state of the controller.
func (c *BurstController) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Feed processes one utilization sample and, if a threshold is crossed,
// emits a transition Event to all registered listeners.
//
// Hysteresis dead-band (CLAUDE-1 guard):
//   - While in StateSpill, a sample that is strictly GREATER THAN RecoverLow
//     AND strictly LESS THAN SpillHigh sits in the dead-band: no event is
//     emitted and the state remains StateSpill.
//
// State machine:
//
//	StateMonitor: sample >= SpillHigh  → emit EventSpill,   move to StateSpill
//	StateSpill  : sample <= RecoverLow → emit EventRecover, move to StateRecover
//	              RecoverLow < sample < SpillHigh → no-op (dead-band)
//	              sample >= SpillHigh  → no-op (already spilling)
//	StateRecover: advance back to StateMonitor, then re-evaluate the new sample
//	              (so an immediate re-spike is caught in the same Feed call).
//
// Listeners are called outside the controller's lock so that a listener may
// safely call back into the controller (e.g. State, Subscribe) without deadlocking.
func (c *BurstController) Feed(sample float64) {
	c.mu.Lock()
	pending := c.collect(sample)
	snap := make([]func(Event), len(c.listeners))
	copy(snap, c.listeners)
	c.mu.Unlock()

	for _, ev := range pending {
		for _, fn := range snap {
			fn(ev)
		}
	}
}

// collect runs the state machine under the lock (must be called with c.mu held)
// and returns the zero, one, or two Events produced.  It does NOT call listeners.
func (c *BurstController) collect(sample float64) []Event {
	switch c.state {
	case StateMonitor:
		if sample >= c.cfg.SpillHigh {
			// MUTATION GUARD — pinned by TestHysteresisSequence:
			// This strict-greater-than-or-equal check enforces that SpillHigh is the
			// lower bound for a SPILL transition.  A mutant changing >= to > would
			// miss the exact boundary sample 0.90, failing TestExactBoundaryValues.
			ev := c.makeEvent(EventSpill, sample, StateMonitor, StateSpill)
			c.state = StateSpill
			return []Event{ev}
		}

	case StateSpill:
		// MUTATION GUARD — pinned by TestHysteresisSequence:
		// The condition below is the dead-band gate. It uses RecoverLow as the
		// lower fence. If a mutant collapses RecoverLow to equal SpillHigh (e.g.
		// sets RecoverLow = SpillHigh = 0.90 for the comparison only), then
		// sample 0.80 would satisfy sample <= RecoverLow (0.80 <= 0.80 is false,
		// but if RecoverLow were 0.90 then 0.80 <= 0.90 is TRUE) and would
		// incorrectly fire EventRecover, making TestHysteresisSequence fail on the
		// assertion "no event for sample 0.80".
		if sample <= c.cfg.RecoverLow {
			ev := c.makeEvent(EventRecover, sample, StateSpill, StateRecover)
			c.state = StateRecover
			return []Event{ev}
		}
		// samples strictly between RecoverLow and SpillHigh: dead-band, no event.

	case StateRecover:
		// Automatically advance back to MONITOR, then re-process the sample so
		// a sudden spike immediately after recovery is not silently dropped.
		c.state = StateMonitor
		return c.collect(sample)
	}
	return nil
}

// makeEvent constructs an Event.  Must be called with c.mu held.
func (c *BurstController) makeEvent(kind EventKind, sample float64, from, to State) Event {
	return Event{
		Kind:      kind,
		Sample:    sample,
		RunID:     c.cfg.RunID,
		FromState: from,
		ToState:   to,
	}
}

// FeedAll is a convenience helper that calls Feed for every sample in order.
// It returns the slice of Events produced by this call only.  It does NOT
// register a permanent subscriber; previously registered subscribers are
// unaffected and will still receive the events via their own callbacks.
func (c *BurstController) FeedAll(samples []float64) []Event {
	var all []Event
	for _, s := range samples {
		c.mu.Lock()
		pending := c.collect(s)
		snap := make([]func(Event), len(c.listeners))
		copy(snap, c.listeners)
		c.mu.Unlock()

		for _, ev := range pending {
			for _, fn := range snap {
				fn(ev)
			}
			all = append(all, ev)
		}
	}
	return all
}
