// Package metrics provides metrics collection for Helix Cluster OS.
package metrics

import "sync/atomic"

// Counter is a simple atomic counter.
type Counter struct {
	val int64
}

// Inc increments the counter by 1.
func (c *Counter) Inc() {
	atomic.AddInt64(&c.val, 1)
}

// Value returns the current counter value.
func (c *Counter) Value() int64 {
	return atomic.LoadInt64(&c.val)
}

// Add adds n to the counter.
func (c *Counter) Add(n int64) {
	atomic.AddInt64(&c.val, n)
}
