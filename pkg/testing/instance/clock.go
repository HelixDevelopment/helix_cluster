// Package instance provides a Provisioner/Instance lifecycle FSM for
// virtual cluster testing (HXC-1231).
//
// The FSM is strictly separate from pkg/testing/device (which backs the
// pool/hardware layer). Name collisions are avoided by design: this package
// defines its own State enum and types.
package instance

import (
	"sync"
	"sync/atomic"
	"time"
)

// VirtualClock is a monotonic nanosecond-precision clock whose time advances
// only when Advance is called. It is safe for concurrent use.
type VirtualClock struct {
	ns int64 // atomic nanoseconds since Unix epoch
	mu sync.RWMutex
}

// NewVirtualClock creates a VirtualClock starting at the given nanosecond
// offset from the Unix epoch (0 = Unix epoch).
func NewVirtualClock(epochNs int64) *VirtualClock {
	c := &VirtualClock{}
	atomic.StoreInt64(&c.ns, epochNs)
	return c
}

// Now returns the current virtual time.
func (c *VirtualClock) Now() time.Time {
	ns := atomic.LoadInt64(&c.ns)
	return time.Unix(0, ns)
}

// Advance moves the virtual clock forward by d. Panics if d is negative.
func (c *VirtualClock) Advance(d time.Duration) {
	if d < 0 {
		panic("instance.VirtualClock.Advance: negative duration")
	}
	atomic.AddInt64(&c.ns, int64(d))
}

// NowNs returns the raw nanosecond counter (for arithmetic).
func (c *VirtualClock) NowNs() int64 {
	return atomic.LoadInt64(&c.ns)
}
