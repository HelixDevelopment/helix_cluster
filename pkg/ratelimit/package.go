// Package ratelimit provides rate limiting for Helix Cluster OS.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a token bucket rate limiter.
type Limiter struct {
	mu       sync.Mutex
	tokens   float64
	max      float64
	refill   float64
	lastFill time.Time
}

// NewLimiter creates a Limiter with max tokens and refill rate per second.
func NewLimiter(max int, refillPerSec float64) *Limiter {
	return &Limiter{
		tokens:   float64(max),
		max:      float64(max),
		refill:   refillPerSec,
		lastFill: time.Now(),
	}
}

// Allow checks if one token can be consumed.
func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(l.lastFill).Seconds()
	l.tokens += elapsed * l.refill
	if l.tokens > l.max {
		l.tokens = l.max
	}
	l.lastFill = now
	if l.tokens >= 1 {
		l.tokens--
		return true
	}
	return false
}
