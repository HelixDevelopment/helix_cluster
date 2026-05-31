package netutil

import (
	"math"
	"time"
)

// RandSource is the minimal randomness interface the backoff helper needs. It
// is satisfied by *math/rand.Rand (whose Float64 returns a value in [0.0,1.0)).
// Making it an injectable interface keeps Backoff deterministic in tests.
type RandSource interface {
	// Float64 returns a pseudo-random float in [0.0, 1.0).
	Float64() float64
}

// Backoff computes exponential backoff durations with optional symmetric
// jitter. It is a pure value type: Duration has no internal state, so the same
// inputs always yield the same output. This makes it safe for concurrent use
// and deterministic in tests when a fixed RandSource is supplied.
type Backoff struct {
	// Base is the delay for attempt 0.
	Base time.Duration
	// Max caps the returned delay. A zero Max means "no cap".
	Max time.Duration
	// Factor is the exponential multiplier per attempt (e.g. 2.0). Values <= 1
	// are treated as 2.0.
	Factor float64
	// Jitter is the symmetric jitter fraction in [0,1]. A value of 0.5 means the
	// result is scaled by a random factor in [0.5, 1.5]. Zero disables jitter.
	Jitter float64
}

// Duration returns the backoff delay for the given attempt (0-based). If rand
// is nil or Jitter is 0, the pure exponential (capped) value is returned with
// no randomness, which keeps callers deterministic. Negative attempts are
// clamped to 0.
func (b Backoff) Duration(attempt int, rand RandSource) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	factor := b.Factor
	if factor <= 1 {
		factor = 2.0
	}

	// Compute Base * factor^attempt in float to detect overflow, then cap.
	mult := math.Pow(factor, float64(attempt))
	raw := float64(b.Base) * mult

	// Cap to Max (if set) before jitter so jitter never pushes us far past Max
	// unexpectedly; we also re-cap after jitter below.
	capped := raw
	if b.Max > 0 && (math.IsInf(raw, 1) || capped > float64(b.Max)) {
		capped = float64(b.Max)
	}

	// Apply jitter only when meaningful and a source is provided.
	if b.Jitter > 0 && rand != nil {
		j := b.Jitter
		if j > 1 {
			j = 1
		}
		// Map r in [0,1) to a multiplier in [1-j, 1+j).
		r := rand.Float64()
		multiplier := (1 - j) + r*(2*j)
		capped *= multiplier
	}

	if capped < 0 {
		capped = 0
	}
	d := time.Duration(capped)
	if b.Max > 0 && d > b.Max {
		d = b.Max
	}
	return d
}
