package swim

import (
	"sort"
	"sync"
	"time"
)

// Clock is an injectable time source used by the failure-detector hardening so
// that timeouts can be driven deterministically in tests (no real time.Sleep,
// no wall-clock dependency). The production implementation is SystemClock; the
// test implementation is ManualClock.
//
// AfterFunc schedules fn to run once after d has elapsed (as measured by this
// clock) and returns a stop function. Calling the stop function before fn fires
// cancels it and returns true; if fn already fired (or was already stopped) it
// returns false.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, fn func()) (stop func() bool)
}

// SystemClock is the real, wall-clock backed Clock. It delegates to the stdlib
// time package and is the default used when no Clock is injected.
type SystemClock struct{}

// NewSystemClock returns a Clock backed by the real system time.
func NewSystemClock() *SystemClock { return &SystemClock{} }

// Now returns the current wall-clock time.
func (SystemClock) Now() time.Time { return time.Now() }

// AfterFunc schedules fn after d using a real time.Timer.
func (SystemClock) AfterFunc(d time.Duration, fn func()) func() bool {
	t := time.AfterFunc(d, fn)
	return t.Stop
}

// ManualClock is a deterministic Clock whose notion of "now" only advances when
// Advance is called. Timers scheduled via AfterFunc fire synchronously (from
// within Advance) once their deadline is reached. This lets tests exercise
// timeout logic without sleeping on the wall clock.
type ManualClock struct {
	mu     sync.Mutex
	now    time.Time
	nextID uint64
	timers map[uint64]*manualTimer
}

type manualTimer struct {
	id       uint64
	deadline time.Time
	fn       func()
	fired    bool
	stopped  bool
}

// NewManualClock returns a ManualClock initialised to start.
func NewManualClock(start time.Time) *ManualClock {
	return &ManualClock{
		now:    start,
		timers: make(map[uint64]*manualTimer),
	}
}

// Now returns the manual clock's current time.
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc registers fn to fire when the clock advances to (now + d) or later.
func (c *ManualClock) AfterFunc(d time.Duration, fn func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	t := &manualTimer{
		id:       id,
		deadline: c.now.Add(d),
		fn:       fn,
	}
	c.timers[id] = t

	// If the deadline is already at/behind now, fire immediately on next Advance(0)
	// is not desired; emulate stdlib AfterFunc which fires "after" the duration.
	// A non-positive duration fires on the next Advance call (including Advance(0)).
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if t.fired || t.stopped {
			return false
		}
		t.stopped = true
		delete(c.timers, id)
		return true
	}
}

// Advance moves the clock forward by d and synchronously fires every timer
// whose deadline is at or before the new "now", in deadline order. Callbacks
// run with the clock lock released so they may schedule or stop timers.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now

	due := make([]*manualTimer, 0)
	for _, t := range c.timers {
		if t.stopped || t.fired {
			continue
		}
		if !t.deadline.After(now) {
			due = append(due, t)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].deadline.Equal(due[j].deadline) {
			return due[i].id < due[j].id
		}
		return due[i].deadline.Before(due[j].deadline)
	})
	for _, t := range due {
		t.fired = true
		delete(c.timers, t.id)
	}
	c.mu.Unlock()

	for _, t := range due {
		t.fn()
	}
}
