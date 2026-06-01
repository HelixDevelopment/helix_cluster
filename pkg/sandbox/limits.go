package sandbox

import (
	"errors"
	"sync/atomic"
	"time"
)

// ErrCapabilityDenied is returned by Guard.Check when the requested capability
// has not been granted. Callers must test with errors.Is.
var ErrCapabilityDenied = errors.New("capability denied")

// ErrLimitExceeded is returned by Guard.Check or Guard.Account when a resource
// limit (MaxOps, MaxBytes, MaxDuration) has been exceeded.
var ErrLimitExceeded = errors.New("limit exceeded")

// ResourceLimits declares the maximum resource consumption a Guard will allow.
// A zero value for any field means "unlimited" for that dimension.
type ResourceLimits struct {
	// MaxOps is the maximum number of successful capability operations (Check
	// calls that would otherwise be allowed). Zero means unlimited.
	MaxOps int64
	// MaxBytes is the maximum cumulative byte count tracked via Guard.Account.
	// Zero means unlimited.
	MaxBytes int64
	// MaxDuration is the maximum wall-clock time from the first Check or
	// Account call before the Guard starts returning ErrLimitExceeded.
	// Zero means unlimited.
	MaxDuration time.Duration
}

// usageTracker maintains op counts, accumulated bytes, and elapsed time
// against a ResourceLimits budget. All public methods are safe for concurrent
// use via atomic operations and a monotonic start-time established on first
// use.
type usageTracker struct {
	limits ResourceLimits

	ops   atomic.Int64
	bytes atomic.Int64

	// startedAt is the nanosecond Unix timestamp recorded on the first
	// operation. Zero means no operation has yet occurred. We use a separate
	// atomic int64 (0 == unset) to avoid importing sync just for Once.
	startedAt atomic.Int64

	// clock is the source of "now"; injected in tests for determinism.
	clock func() time.Time
}

// newUsageTracker constructs a tracker for lim. clock may be nil (defaults to
// time.Now).
func newUsageTracker(lim ResourceLimits, clock func() time.Time) *usageTracker {
	if clock == nil {
		clock = time.Now
	}
	return &usageTracker{limits: lim, clock: clock}
}

// recordStart stamps the start time on the very first call (idempotent for
// subsequent calls).
func (t *usageTracker) recordStart() {
	// CAS 0 -> now, so only the first caller wins.
	now := t.clock().UnixNano()
	t.startedAt.CompareAndSwap(0, now)
}

// addOps increments the op counter by delta and checks MaxOps.
// Returns ErrLimitExceeded if the limit is crossed.
func (t *usageTracker) addOps(delta int64) error {
	t.recordStart()
	newVal := t.ops.Add(delta)
	if t.limits.MaxOps > 0 && newVal > t.limits.MaxOps {
		return ErrLimitExceeded
	}
	return nil
}

// addBytes accumulates bytes and checks MaxBytes.
// Returns ErrLimitExceeded if the limit is crossed.
func (t *usageTracker) addBytes(n int64) error {
	t.recordStart()
	newVal := t.bytes.Add(n)
	if t.limits.MaxBytes > 0 && newVal > t.limits.MaxBytes {
		return ErrLimitExceeded
	}
	return nil
}

// checkDuration returns ErrLimitExceeded if MaxDuration is set and the elapsed
// time since the first recorded operation exceeds it.
func (t *usageTracker) checkDuration() error {
	if t.limits.MaxDuration <= 0 {
		return nil
	}
	start := t.startedAt.Load()
	if start == 0 {
		// No operation has been recorded yet; duration clock hasn't started.
		return nil
	}
	elapsed := t.clock().UnixNano() - start
	if elapsed > t.limits.MaxDuration.Nanoseconds() {
		return ErrLimitExceeded
	}
	return nil
}
