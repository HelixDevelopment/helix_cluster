// Package backoff provides exponential backoff for Helix Cluster OS.
package backoff

import (
	"math"
	"time"
)

// Config holds backoff parameters.
type Config struct {
	Base   time.Duration
	Max    time.Duration
	Factor float64
}

// Default returns a sensible default backoff config.
func Default() Config {
	return Config{Base: 100 * time.Millisecond, Max: 30 * time.Second, Factor: 2}
}

// Duration computes the backoff duration for attempt n (0-indexed).
func (c Config) Duration(n int) time.Duration {
	if n < 0 {
		n = 0
	}
	d := time.Duration(float64(c.Base) * math.Pow(c.Factor, float64(n)))
	if d > c.Max {
		return c.Max
	}
	return d
}
