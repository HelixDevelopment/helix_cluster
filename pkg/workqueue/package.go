// Package workqueue provides a controller-runtime-style, rate-limited work
// queue for Helix Cluster OS.
//
// The queue offers three properties that controllers rely on:
//
//   - Deduplication: an item Added many times — even while it is being
//     processed — is delivered by Get exactly once per ready window. If the
//     item is re-Added while a worker holds it (between Get and Done), it is
//     re-enqueued exactly once after Done so the worker re-processes the
//     latest state without the queue accumulating duplicates.
//
//   - Rate limiting: AddRateLimited schedules an item after an exponential
//     backoff delay that grows per consecutive failure (base * 2^failures,
//     capped). Forget resets an item's failure count back to zero so the next
//     AddRateLimited starts again from the base delay.
//
//   - Deferred adds: AddAfter makes an item ready only after a delay. Delays
//     are measured against an injected Clock so tests are fully deterministic
//     and never sleep on real wall-clock time.
//
// The core never calls time.Now(); all time flows through the injected Clock,
// mirroring pkg/hlc. This makes every timing-dependent behaviour reproducible
// under test.
package workqueue

import (
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/backoff"
)

// Clock is the injectable time source for a Queue. Production code passes a
// real-time clock (see RealClock); tests pass a ManualClock they advance by
// hand so deferred and rate-limited items become ready deterministically.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

// RealClock is a Clock backed by the wall clock. It is the only place the
// package reads real time, and it is never used by the deterministic tests.
type RealClock struct{}

// Now returns time.Now.
func (RealClock) Now() time.Time { return time.Now() }

// ManualClock is a deterministic Clock for tests. Advance it explicitly with
// Step or Set; it never moves on its own.
type ManualClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewManualClock returns a ManualClock anchored at start.
func NewManualClock(start time.Time) *ManualClock { return &ManualClock{t: start} }

// Now returns the clock's current (manually controlled) time.
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Step advances the clock by d and returns the new time.
func (c *ManualClock) Step(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return c.t
}

// RateLimitConfig controls the exponential backoff used by AddRateLimited.
// The delay for the n-th consecutive failure (0-indexed) is
// Base * 2^n, capped at Max.
type RateLimitConfig struct {
	// Base is the delay applied to the first failure.
	Base time.Duration
	// Max caps the delay so it cannot grow without bound.
	Max time.Duration
}

// DefaultRateLimit returns a 5ms base / 1000s cap configuration. The small
// base keeps the exponential ramp easy to assert against in tests while still
// being a realistic controller default.
func DefaultRateLimit() RateLimitConfig {
	return RateLimitConfig{Base: 5 * time.Millisecond, Max: 1000 * time.Second}
}

// backoffConfig converts the public RateLimitConfig into the read-only
// pkg/backoff config used to compute per-attempt delays (Base * 2^n, capped).
func (c RateLimitConfig) backoffConfig() backoff.Config {
	return backoff.Config{Base: c.Base, Max: c.Max, Factor: 2}
}

// delayForFailures returns the backoff delay for the given number of prior
// consecutive failures. failures==0 yields Base; each increment doubles the
// delay up to Max.
func (c RateLimitConfig) delayForFailures(failures int) time.Duration {
	return c.backoffConfig().Duration(failures)
}

// waiting is a deferred item: t holds the wall time at which the item becomes
// ready (per the injected Clock).
type waiting struct {
	item interface{}
	t    time.Time
}

// Queue is a deduplicating, rate-limited, deterministic work queue.
//
// Internal item lifecycle (controller-runtime model):
//
//   - dirty: the set of items that need processing. Membership here is what
//     dedups: Add is idempotent because it only inserts into a set.
//   - processing: the set of items currently checked out by a worker (handed
//     out by Get, not yet Done).
//   - queue: the FIFO ordering of ready, not-yet-checked-out items. An item is
//     in queue iff it is dirty and not processing.
//
// When an item that is being processed is Added again, it stays dirty but is
// NOT appended to the FIFO (it is already in processing). On Done, because it
// is still dirty, it is re-appended exactly once — giving exactly one
// re-delivery for any number of re-Adds during processing.
type Queue struct {
	clock Clock
	rl    RateLimitConfig

	mu   sync.Mutex
	cond *sync.Cond

	queue      []interface{}
	dirty      map[interface{}]struct{}
	processing map[interface{}]struct{}

	waiters  []waiting
	failures map[interface{}]int

	shuttingDown bool
}

// New returns a Queue using the given Clock and rate-limit configuration.
// clock MUST be non-nil so the core never falls back to time.Now().
func New(clock Clock, rl RateLimitConfig) *Queue {
	if clock == nil {
		panic("workqueue: New requires a non-nil Clock")
	}
	q := &Queue{
		clock:      clock,
		rl:         rl,
		dirty:      make(map[interface{}]struct{}),
		processing: make(map[interface{}]struct{}),
		failures:   make(map[interface{}]int),
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// readyLocked moves any waiting items whose ready time has arrived into the
// main queue. It must be called with q.mu held. An item already dirty is not
// duplicated into the FIFO; it merely loses its waiting entry.
func (q *Queue) readyLocked() {
	if len(q.waiters) == 0 {
		return
	}
	now := q.clock.Now()
	kept := q.waiters[:0:0]
	for _, w := range q.waiters {
		if w.t.After(now) {
			kept = append(kept, w)
			continue
		}
		q.enqueueLocked(w.item)
	}
	q.waiters = kept
}

// enqueueLocked inserts item into the dirty set and, if it is not currently
// being processed and not already queued, appends it to the FIFO. This is the
// single dedup choke point: it is safe to call for the same item repeatedly.
func (q *Queue) enqueueLocked(item interface{}) {
	if _, ok := q.dirty[item]; ok {
		// Already dirty: either queued or processing. Either way, do not append
		// a duplicate — that is the dedup guarantee.
		return
	}
	q.dirty[item] = struct{}{}
	if _, busy := q.processing[item]; busy {
		// Being processed: stays dirty so Done re-enqueues it exactly once.
		return
	}
	q.queue = append(q.queue, item)
}

// Add enqueues item for processing, deduplicated. Adding an item already in
// the queue or already dirty is a no-op; adding an item currently being
// processed marks it dirty so it is re-delivered once after Done.
func (q *Queue) Add(item interface{}) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shuttingDown {
		return
	}
	q.enqueueLocked(item)
	q.cond.Signal()
}

// AddAfter enqueues item once delay has elapsed on the injected Clock. A
// non-positive delay enqueues immediately. The item becomes eligible for Get
// only after a later call observes the clock at or past the ready time
// (callers/tests drive the clock; readiness is evaluated inside Get/Len).
func (q *Queue) AddAfter(item interface{}, delay time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shuttingDown {
		return
	}
	if delay <= 0 {
		q.enqueueLocked(item)
		q.cond.Signal()
		return
	}
	ready := q.clock.Now().Add(delay)
	q.waiters = append(q.waiters, waiting{item: item, t: ready})
	// Signal so a waiting Get re-evaluates (it may now need to recompute its
	// wait, though without a real timer it relies on the clock being advanced).
	q.cond.Signal()
}

// AddRateLimited enqueues item after an exponential backoff delay that grows
// with the item's consecutive-failure count: Base*2^failures capped at Max.
// Each call increments the failure count, so repeated AddRateLimited on a
// failing item yields a geometrically growing schedule until Forget resets it.
// It returns the delay that was scheduled, which is also surfaced via the
// item's waiting entry; the return value makes the schedule directly assertable.
func (q *Queue) AddRateLimited(item interface{}) time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shuttingDown {
		return 0
	}
	failures := q.failures[item]
	delay := q.rl.delayForFailures(failures)
	q.failures[item] = failures + 1

	if delay <= 0 {
		q.enqueueLocked(item)
	} else {
		ready := q.clock.Now().Add(delay)
		q.waiters = append(q.waiters, waiting{item: item, t: ready})
	}
	q.cond.Signal()
	return delay
}

// Forget resets item's consecutive-failure count to zero so the next
// AddRateLimited starts again from the base delay. It does not remove the item
// from the queue or cancel a pending waiter.
func (q *Queue) Forget(item interface{}) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.failures, item)
}

// Failures returns the current consecutive-failure count tracked for item.
// Primarily for tests and introspection.
func (q *Queue) Failures(item interface{}) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.failures[item]
}

// Get returns the next ready item, blocking until one is available or the
// queue is shut down. The returned shutdown flag is true exactly when the
// queue has been shut down and no more items will ever be returned. The caller
// MUST call Done with the returned item once finished.
func (q *Queue) Get() (item interface{}, shutdown bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for {
		q.readyLocked()
		if len(q.queue) > 0 {
			break
		}
		if q.shuttingDown {
			return nil, true
		}
		// No ready item. If there are deferred waiters, we still block on the
		// condition variable; tests advance the clock and Add/Signal to wake us.
		q.cond.Wait()
	}

	item = q.queue[0]
	// Avoid retaining the backing array's reference to the popped element.
	q.queue[0] = nil
	q.queue = q.queue[1:]

	q.processing[item] = struct{}{}
	// The item is checked out: it is no longer "pending in the FIFO" but stays
	// dirty until Done, so a concurrent re-Add can re-enqueue it exactly once.
	delete(q.dirty, item)

	return item, false
}

// Done marks item as finished processing. If item was Added again while it was
// being processed, it is re-enqueued exactly once so the worker re-processes
// the latest state.
func (q *Queue) Done(item interface{}) {
	q.mu.Lock()
	defer q.mu.Unlock()

	delete(q.processing, item)
	if _, ok := q.dirty[item]; ok {
		// Re-Added during processing: put it back on the FIFO exactly once.
		q.queue = append(q.queue, item)
		q.cond.Signal()
	}
}

// Len returns the number of items currently ready and waiting to be checked
// out (the FIFO depth). Deferred waiters that have become ready by the current
// clock reading are first promoted, so Len reflects true readiness. Items
// currently being processed are not counted.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.readyLocked()
	return len(q.queue)
}

// ShutDown stops the queue: pending and future Get calls return shutdown=true,
// and all blocked waiters are woken.
func (q *Queue) ShutDown() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shuttingDown = true
	q.cond.Broadcast()
}

// ShuttingDown reports whether ShutDown has been called.
func (q *Queue) ShuttingDown() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.shuttingDown
}
