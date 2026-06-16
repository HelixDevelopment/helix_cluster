// Package ratelimit provides token bucket, sliding window, and per-key rate limiting for Helix Cluster OS.
package ratelimit

import (
	"fmt"
	"math"
	"sync"
)

// Clock is an injectable time source so tests can advance time deterministically
// without real sleeps. Production code uses RealClock.
type Clock interface {
	// Now returns the current time as a Unix nanosecond timestamp.
	NowNano() int64
}

// RealClock returns real wall-clock time. It is the default used by NewUserLimiter.
type RealClock struct{}

// NowNano returns the current real wall-clock time in nanoseconds.
func (RealClock) NowNano() int64 {
	// Use a package-level import-free helper to avoid importing time in this file
	// — time is already imported in ratelimit.go so there is no extra cost.
	return realNowNano()
}

// userBucket is a single token-bucket for one user (or the global bucket).
// All fields are protected by the UserLimiter-level mu.
type userBucket struct {
	// tokens is the current fill level (fractional tokens allowed internally).
	tokens float64
	// max is the bucket capacity in integer tokens.
	max float64
	// refillPerNs is the number of tokens added per nanosecond.
	refillPerNs float64
	// lastNs is the clock reading (NowNano) when tokens was last recalculated.
	lastNs int64
}

// refill adds tokens accrued since lastNs, capped at max.
func (b *userBucket) refill(nowNs int64) {
	if nowNs > b.lastNs {
		elapsed := float64(nowNs - b.lastNs)
		b.tokens += elapsed * b.refillPerNs
		// A non-finite refill rate (NaN/±Inf, e.g. from a malformed config or
		// 0.0/0.0 upstream) makes b.tokens NaN, and `NaN > max` is false — so the
		// cap below would be SKIPPED and the poisoned NaN would persist forever.
		// Every downstream guard (`tokens < 1`, `tokens >= n`) is also false for
		// NaN, which silently DEFEATS the rate limit (unbounded admission). Clamp
		// any non-finite value to max so the ceiling always holds, fail-closed.
		if math.IsNaN(b.tokens) || b.tokens > b.max {
			b.tokens = b.max
		}
		b.lastNs = nowNs
	}
}

// UserLimiterConfig controls the limits applied by UserLimiter.
type UserLimiterConfig struct {
	// UserCapacity is the maximum token capacity for a per-user bucket.
	UserCapacity int
	// UserRefillPerSec is the rate at which per-user tokens are added each second.
	UserRefillPerSec float64
	// GlobalCapacity is the maximum token capacity for the shared global bucket.
	GlobalCapacity int
	// GlobalRefillPerSec is the rate at which global tokens are added each second.
	GlobalRefillPerSec float64
}

// UserLimiter enforces per-user AND global rate limits using token buckets.
//
// Key layout in the logical namespace:
//
//	per-user: clusteros:ratelimit:<userID>
//	global:   clusteros:ratelimit:global
//
// Allow checks both the per-user bucket and the global bucket.  A request is
// allowed only when BOTH buckets have capacity; when either is empty the request
// is denied and the returned remaining is 0.
//
// The Clock is injectable so deterministic unit tests can advance time without
// sleeping.
type UserLimiter struct {
	mu     sync.Mutex
	cfg    UserLimiterConfig
	clock  Clock
	users  map[string]*userBucket // keyed by canonical user key
	global *userBucket
}

// NewUserLimiter creates a UserLimiter with the given config and a real clock.
func NewUserLimiter(cfg UserLimiterConfig) *UserLimiter {
	return NewUserLimiterWithClock(cfg, RealClock{})
}

// NewUserLimiterWithClock creates a UserLimiter backed by the given Clock (useful
// for tests with a FakeClock).
func NewUserLimiterWithClock(cfg UserLimiterConfig, clk Clock) *UserLimiter {
	now := clk.NowNano()
	globalRefillNs := cfg.GlobalRefillPerSec / 1e9
	return &UserLimiter{
		cfg:   cfg,
		clock: clk,
		users: make(map[string]*userBucket),
		global: &userBucket{
			tokens:      float64(cfg.GlobalCapacity),
			max:         float64(cfg.GlobalCapacity),
			refillPerNs: globalRefillNs,
			lastNs:      now,
		},
	}
}

// userKey returns the canonical logical key for a user bucket.
func userKey(userID string) string {
	return fmt.Sprintf("clusteros:ratelimit:%s", userID)
}

// globalKeyStr is the canonical logical key for the global bucket (constant).
const globalKeyStr = "clusteros:ratelimit:global"

// getOrCreateUser returns the bucket for userID, creating it lazily if absent.
// Caller must hold ul.mu.
func (ul *UserLimiter) getOrCreateUser(userID string, nowNs int64) *userBucket {
	_ = userKey(userID) // keeps the key format in use; the map uses the raw userID for efficiency
	b, ok := ul.users[userID]
	if !ok {
		b = &userBucket{
			tokens:      float64(ul.cfg.UserCapacity),
			max:         float64(ul.cfg.UserCapacity),
			refillPerNs: ul.cfg.UserRefillPerSec / 1e9,
			lastNs:      nowNs,
		}
		ul.users[userID] = b
	}
	return b
}

// Allow checks whether userID's request should be allowed.
//
// It returns:
//   - allowed: true if both the per-user bucket AND the global bucket had at
//     least one token; false otherwise.
//   - remaining: the per-user token count AFTER consuming one token when allowed,
//     or 0 when denied (no token is consumed on denial).
//
// Thread-safe.
func (ul *UserLimiter) Allow(userID string) (allowed bool, remaining int) {
	ul.mu.Lock()
	defer ul.mu.Unlock()

	now := ul.clock.NowNano()
	ub := ul.getOrCreateUser(userID, now)

	// Refill both buckets up to the current time.
	ub.refill(now)
	ul.global.refill(now)

	// Check both buckets before consuming any token (atomic decision).
	if ub.tokens < 1.0 || ul.global.tokens < 1.0 {
		return false, 0
	}

	// Consume one token from each bucket.
	ub.tokens -= 1.0
	ul.global.tokens -= 1.0

	return true, int(math.Floor(ub.tokens))
}

// UserTokens returns the current (post-refill) token count for the given user.
// It does NOT consume a token. Useful for observability and tests.
func (ul *UserLimiter) UserTokens(userID string) int {
	ul.mu.Lock()
	defer ul.mu.Unlock()
	now := ul.clock.NowNano()
	b := ul.getOrCreateUser(userID, now)
	b.refill(now)
	return int(math.Floor(b.tokens))
}

// GlobalTokens returns the current (post-refill) token count for the global bucket.
// Useful for observability and tests.
func (ul *UserLimiter) GlobalTokens() int {
	ul.mu.Lock()
	defer ul.mu.Unlock()
	now := ul.clock.NowNano()
	ul.global.refill(now)
	return int(math.Floor(ul.global.tokens))
}

// FakeClock is a deterministic clock for use in tests.  Callers advance it via
// Advance(nanoseconds).
type FakeClock struct {
	mu  sync.Mutex
	now int64
}

// NewFakeClock creates a FakeClock whose epoch is t0 nanoseconds.
func NewFakeClock(t0 int64) *FakeClock {
	return &FakeClock{now: t0}
}

// NowNano returns the current fake time.
func (f *FakeClock) NowNano() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance adds delta nanoseconds to the fake clock.
func (f *FakeClock) Advance(deltaNs int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now += deltaNs
}
