// Package billingfsm implements a scale-to-zero metered hot/cold billing
// state machine. It is pure Go and fully deterministic: all time advances
// through an injected clock / explicit Tick calls, never time.Now().
//
// Model:
//
//	Cold  - scaled-to-zero. The workload is not running and NO compute
//	        seconds are billed while in this state (this is what makes
//	        scale-to-zero save money).
//	Hot   - running. Every wall-clock second spent in Hot is billed.
//
// Transitions (with hysteresis via separate thresholds/timers):
//
//	Cold -> Hot : observed load rises above ScalingThreshold.
//	Hot  -> Cold: the workload has been idle (no requests) for at least
//	              ShutdownAfter; it then scales to zero.
//
// A request (Observe with load>0, or the dedicated request path) resets the
// idle timer, delaying any pending scale-to-zero.
package billingfsm

import "time"

// State is the billing/lifecycle state of the workload.
type State int

const (
	// Cold means scaled-to-zero: not running, not billed for compute.
	Cold State = iota
	// Hot means running: billed per active second.
	Hot
)

// String renders a State for logs.
func (s State) String() string {
	switch s {
	case Cold:
		return "Cold"
	case Hot:
		return "Hot"
	default:
		return "Unknown"
	}
}

// Clock is the injected time source. Implementations must be deterministic in
// tests (e.g. a manually advanced fake). The Controller never calls
// time.Now() directly.
type Clock interface {
	Now() time.Time
}

// Controller is the metered scale-to-zero billing state machine.
//
// Construct with New. All state advances through Observe/Tick; the controller
// holds no goroutines and is not safe for concurrent use (callers serialize).
type Controller struct {
	// scalingThreshold: load strictly greater than this scales Cold->Hot.
	scalingThreshold float64
	// shutdownAfter: idle duration at/after which Hot scales to Cold.
	shutdownAfter time.Duration
	clock         Clock

	state State

	// lastTick is the wall-clock time of the most recent advance. Hot
	// seconds between lastTick and a new now are accrued into billedNanos.
	lastTick time.Time
	// lastRequest is the wall-clock time of the most recent request (load
	// activity). Idle is measured from this point.
	lastRequest time.Time

	// billedNanos accumulates billed time, in nanoseconds, accrued ONLY
	// while in Hot.
	billedNanos int64
}

// New builds a Controller. scalingThreshold is the load level a workload must
// strictly exceed to scale up; shutdownAfter is how long the workload may be
// idle before scaling to zero; clock supplies deterministic time.
func New(scalingThreshold float64, shutdownAfter time.Duration, clock Clock) *Controller {
	now := clock.Now()
	return &Controller{
		scalingThreshold: scalingThreshold,
		shutdownAfter:    shutdownAfter,
		clock:            clock,
		state:            Cold,
		lastTick:         now,
		lastRequest:      now,
	}
}

// State returns the current lifecycle/billing state.
func (c *Controller) State() State { return c.state }

// BilledSeconds returns the total billed time, in whole-and-fractional
// seconds, accrued only while Hot.
func (c *Controller) BilledSeconds() float64 {
	return float64(c.billedNanos) / float64(time.Second)
}

// accrue advances internal billing from lastTick up to now. Any elapsed time
// is billed ONLY if the current state is Hot; Cold time bills nothing.
func (c *Controller) accrue(now time.Time) {
	if now.Before(c.lastTick) {
		// Non-monotonic input; clamp to avoid negative accrual.
		c.lastTick = now
		return
	}
	elapsed := now.Sub(c.lastTick)
	if c.state == Hot {
		c.billedNanos += int64(elapsed)
	}
	c.lastTick = now
}

// Tick advances the machine to wall-clock time now without any new load. It
// first bills any elapsed Hot time, then evaluates idle-based shutdown:
// if Hot and idle (now - lastRequest) >= ShutdownAfter, it scales to Cold.
func (c *Controller) Tick(now time.Time) {
	c.accrue(now)
	if c.state == Hot {
		idle := now.Sub(c.lastRequest)
		if idle >= c.shutdownAfter {
			c.state = Cold
		}
	}
}

// Observe advances the machine to the controller clock's current time while
// reporting the instantaneous load. It bills elapsed Hot time, then:
//
//   - load > 0 counts as a request and resets the idle timer.
//   - load strictly above ScalingThreshold scales Cold->Hot.
//   - if no request activity, an idle Hot workload past ShutdownAfter scales
//     to Cold.
//
// Returns the resulting State.
func (c *Controller) Observe(load float64) State {
	return c.ObserveAt(c.clock.Now(), load)
}

// ObserveAt is Observe with an explicit timestamp, for deterministic driving.
func (c *Controller) ObserveAt(now time.Time, load float64) State {
	c.accrue(now)

	// Any positive load is request activity: reset the idle timer. This is
	// the hysteresis anchor for scale-to-zero.
	if load > 0 {
		c.lastRequest = now
	}

	switch c.state {
	case Cold:
		if load > c.scalingThreshold {
			c.state = Hot
		}
	case Hot:
		idle := now.Sub(c.lastRequest)
		if idle >= c.shutdownAfter {
			c.state = Cold
		}
	}
	return c.state
}
