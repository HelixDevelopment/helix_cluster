// Package thermalwarm implements a deterministic thermal pre-warm /
// scale-from-zero state machine for backends.
//
// A backend has three thermal states: Cold -> Warming -> Hot.
//
//   - When pre-warm is ENABLED, the controller can PreWarm(backendID) BEFORE the
//     first real request arrives. The warm-up takes ColdStartDuration of wall
//     time (driven by an injected clock). Once that duration elapses, the backend
//     transitions to Hot. A subsequent Dispatch then hits an already-Hot backend
//     and pays ZERO request-time cold-start: the latency was moved off the
//     request path.
//
//   - When pre-warm is DISABLED (no PreWarm call before Dispatch), the first
//     Dispatch to a Cold backend itself incurs the full ColdStartDuration: the
//     request observes the Cold -> Warming -> Hot transition and the measured
//     cold-start is charged to that request.
//
// The package is pure Go and deterministic: all time comes from an injected
// Clock. Nothing here calls time.Now().
package thermalwarm

import (
	"sync"
	"time"
)

// State is a backend's thermal state.
type State int

const (
	// Cold means the backend is scaled to zero / not warmed.
	Cold State = iota
	// Warming means a warm-up is in progress (cold-start being paid).
	Warming
	// Hot means the backend is ready to serve with no cold-start.
	Hot
)

func (s State) String() string {
	switch s {
	case Cold:
		return "Cold"
	case Warming:
		return "Warming"
	case Hot:
		return "Hot"
	default:
		return "Unknown"
	}
}

// Clock is an injected, deterministic time source. Logic under test never calls
// time.Now() directly; it reads Now() and is advanced via the controller's
// Advance/Tick helpers (which drive a ManualClock).
type Clock interface {
	Now() time.Time
}

// ManualClock is a deterministic Clock whose time only moves when explicitly
// advanced. It is the canonical Clock for tests.
type ManualClock struct {
	t time.Time
}

// NewManualClock returns a ManualClock anchored at start.
func NewManualClock(start time.Time) *ManualClock {
	return &ManualClock{t: start}
}

// Now returns the current deterministic time.
func (c *ManualClock) Now() time.Time { return c.t }

// Advance moves the clock forward by d.
func (c *ManualClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// backend holds the mutable thermal state for one backend.
type backend struct {
	id    string
	state State
	// warmStartedAt is the clock time at which the in-progress warm-up began.
	// Valid only while state == Warming.
	warmStartedAt time.Time
}

// State returns the backend's current thermal state, recomputed against the
// controller clock so that an elapsed warm-up is reflected.
func (b *backend) currentState(now time.Time, coldStart time.Duration) State {
	if b.state == Warming {
		if !now.Before(b.warmStartedAt.Add(coldStart)) {
			return Hot
		}
		return Warming
	}
	return b.state
}

// Controller drives thermal pre-warming for a set of backends.
//
// A Controller is safe for concurrent use by multiple goroutines. In a
// scale-from-zero dispatcher, request goroutines call Dispatch/State while a
// warmer goroutine calls PreWarm/Register against the SAME controller; all
// access to the shared backends map and per-backend thermal state is guarded by
// mu. Note that several read-looking methods (State, Dispatch) also persist a
// completed warm-up (b.state = Hot) and so take the write lock.
type Controller struct {
	clock     Clock
	coldStart time.Duration

	mu       sync.Mutex
	backends map[string]*backend
}

// NewController constructs a Controller with the given injected clock and a
// configurable cold-start duration. coldStart must be > 0.
func NewController(clock Clock, coldStart time.Duration) *Controller {
	return &Controller{
		clock:     clock,
		coldStart: coldStart,
		backends:  make(map[string]*backend),
	}
}

// ColdStartDuration reports the configured cold-start duration.
func (c *Controller) ColdStartDuration() time.Duration { return c.coldStart }

// ensure returns the backend for id, creating it Cold if absent. The caller
// MUST hold c.mu.
func (c *Controller) ensure(id string) *backend {
	b, ok := c.backends[id]
	if !ok {
		b = &backend{id: id, state: Cold}
		c.backends[id] = b
	}
	return b
}

// Register adds a backend (initially Cold). Idempotent. Safe for concurrent use.
func (c *Controller) Register(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure(id)
}

// State returns the current thermal state of a backend, reconciled against the
// clock (so an expired warm-up reads Hot even before the next Dispatch/Tick).
func (c *Controller) State(id string) State {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.ensure(id)
	now := c.clock.Now()
	s := b.currentState(now, c.coldStart)
	// Persist a completed warm-up so the transition is sticky.
	if s == Hot && b.state == Warming {
		b.state = Hot
	}
	return s
}

// PreWarm begins a warm-up on a Cold backend, moving it to Warming and recording
// the start time. This is the off-request-path latency injection: callers invoke
// it before the first real request, then advance the clock past ColdStartDuration
// so the backend is Hot when the request arrives.
//
// PreWarm on an already-Hot backend is a no-op (stays Hot). PreWarm on a backend
// that is already Warming does not restart the timer.
func (c *Controller) PreWarm(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.ensure(id)
	now := c.clock.Now()
	switch b.currentState(now, c.coldStart) {
	case Cold:
		b.state = Warming
		b.warmStartedAt = now
	case Hot:
		b.state = Hot
	default:
		// Already Warming: leave the in-flight timer intact.
	}
}

// Tick advances the injected ManualClock (if that is what backs this controller)
// by d. It is a convenience wrapper; if the clock is not a *ManualClock it is a
// no-op and callers should advance their own clock.
//
// Tick mutates only the injected clock, never the backends map, so it does NOT
// take c.mu; this also lets Dispatch call it while already holding the lock
// (sync.Mutex is not reentrant). Advancing a *ManualClock concurrently with
// Dispatch from another goroutine is the clock's own concern — production
// callers use a real (already concurrency-safe) clock; ManualClock is for
// single-threaded deterministic tests.
func (c *Controller) Tick(d time.Duration) {
	if mc, ok := c.clock.(*ManualClock); ok {
		mc.Advance(d)
	}
}

// Advance is an alias for Tick for callers that prefer that verb.
func (c *Controller) Advance(d time.Duration) { c.Tick(d) }

// DispatchResult records the outcome of a Dispatch.
type DispatchResult struct {
	// ServedHot is true if the backend was already Hot when the request arrived,
	// i.e. the request paid no cold-start (pre-warm did its job, or it was
	// already warm).
	ServedHot bool
	// ColdStart is the cold-start latency charged to THIS request. Zero when the
	// backend was already Hot; equal to ColdStartDuration when the request itself
	// had to warm a Cold backend.
	ColdStart time.Duration
	// FinalState is the backend's state after the dispatch (always Hot once a
	// request has been served).
	FinalState State
}

// Dispatch routes a request to a backend and returns the cold-start charged to
// the request.
//
//   - If the backend is already Hot (e.g. pre-warmed and the clock advanced past
//     the warm-up), the request is served immediately: ServedHot=true,
//     ColdStart=0.
//
//   - If the backend is Cold (pre-warm disabled / never warmed), the request
//     itself incurs the full cold-start: the controller's clock is advanced by
//     ColdStartDuration to model the request waiting for warm-up, the backend
//     ends Hot, ServedHot=false, ColdStart=ColdStartDuration.
//
//   - If the backend is Warming when the request arrives, the request waits for
//     the REMAINING warm-up time, the clock is advanced by that remainder, the
//     backend ends Hot, and ColdStart is that remainder.
func (c *Controller) Dispatch(id string) DispatchResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.ensure(id)
	now := c.clock.Now()
	s := b.currentState(now, c.coldStart)

	switch s {
	case Hot:
		b.state = Hot
		return DispatchResult{ServedHot: true, ColdStart: 0, FinalState: Hot}

	case Cold:
		// The request itself pays the full cold-start.
		c.Tick(c.coldStart)
		b.state = Hot
		return DispatchResult{ServedHot: false, ColdStart: c.coldStart, FinalState: Hot}

	default: // Warming
		// Pay only the remaining warm-up time.
		remaining := b.warmStartedAt.Add(c.coldStart).Sub(now)
		if remaining < 0 {
			remaining = 0
		}
		c.Tick(remaining)
		b.state = Hot
		return DispatchResult{ServedHot: false, ColdStart: remaining, FinalState: Hot}
	}
}
