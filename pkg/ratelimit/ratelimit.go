// Package ratelimit provides token bucket, sliding window, and per-key rate limiting for Helix Cluster OS.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is the common interface for rate limiters.
type Limiter interface {
	Allow(key string) bool
	AllowN(key string, n int) bool
}

// TokenBucket is a token bucket rate limiter with burst capacity and refill rate.
type TokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	max      float64
	refill   float64
	lastFill time.Time
}

// NewTokenBucket creates a TokenBucket with max tokens and refill rate per second.
func NewTokenBucket(max int, refillPerSec float64) *TokenBucket {
	return &TokenBucket{
		tokens:   float64(max),
		max:      float64(max),
		refill:   refillPerSec,
		lastFill: time.Now(),
	}
}

// Allow checks if one token can be consumed.
func (l *TokenBucket) Allow() bool {
	return l.AllowN(1)
}

// AllowN checks if n tokens can be consumed.
func (l *TokenBucket) AllowN(n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(l.lastFill).Seconds()
	l.tokens += elapsed * l.refill
	if l.tokens > l.max {
		l.tokens = l.max
	}
	l.lastFill = now
	if l.tokens >= float64(n) {
		l.tokens -= float64(n)
		return true
	}
	return false
}

// Allow implements the Limiter interface for TokenBucket (ignores key).
func (l *TokenBucket) AllowKey(key string) bool {
	return l.Allow()
}

// AllowN implements the Limiter interface for TokenBucket (ignores key).
func (l *TokenBucket) AllowKeyN(key string, n int) bool {
	return l.AllowN(n)
}

// SlidingWindow implements a sliding window rate limiter.
type SlidingWindow struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	events []time.Time
}

// NewSlidingWindow creates a sliding window limiter.
func NewSlidingWindow(limit int, window time.Duration) *SlidingWindow {
	return &SlidingWindow{
		window: window,
		limit:  limit,
		events: make([]time.Time, 0, limit),
	}
}

// Allow checks if one request is allowed in the sliding window.
func (s *SlidingWindow) Allow() bool {
	return s.AllowN(1)
}

// AllowN checks if n requests are allowed in the sliding window.
func (s *SlidingWindow) AllowN(n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-s.window)
	// Remove events outside the window.
	var kept []time.Time
	for _, e := range s.events {
		if e.After(cutoff) || e.Equal(cutoff) {
			kept = append(kept, e)
		}
	}
	s.events = kept
	if len(s.events)+n > s.limit {
		return false
	}
	for i := 0; i < n; i++ {
		s.events = append(s.events, now)
	}
	return true
}

// Allow implements the Limiter interface for SlidingWindow (ignores key).
func (s *SlidingWindow) AllowKey(key string) bool {
	return s.Allow()
}

// AllowN implements the Limiter interface for SlidingWindow (ignores key).
func (s *SlidingWindow) AllowKeyN(key string, n int) bool {
	return s.AllowN(n)
}

// PerKeyLimiter manages a map of limiters keyed by string.
type PerKeyLimiter struct {
	mu      sync.Mutex
	factory func() interface {
		Allow() bool
		AllowN(int) bool
	}
	limiters map[string]interface {
		Allow() bool
		AllowN(int) bool
	}
}

// NewPerKeyLimiter creates a per-key limiter using the given factory.
func NewPerKeyLimiter(factory func() interface {
	Allow() bool
	AllowN(int) bool
}) *PerKeyLimiter {
	return &PerKeyLimiter{
		factory: factory,
		limiters: make(map[string]interface {
			Allow() bool
			AllowN(int) bool
		}),
	}
}

// Allow checks if the key is allowed.
func (p *PerKeyLimiter) Allow(key string) bool {
	p.mu.Lock()
	l, ok := p.limiters[key]
	if !ok {
		l = p.factory()
		p.limiters[key] = l
	}
	p.mu.Unlock()
	return l.Allow()
}

// AllowN checks if the key is allowed for n requests.
func (p *PerKeyLimiter) AllowN(key string, n int) bool {
	p.mu.Lock()
	l, ok := p.limiters[key]
	if !ok {
		l = p.factory()
		p.limiters[key] = l
	}
	p.mu.Unlock()
	return l.AllowN(n)
}
