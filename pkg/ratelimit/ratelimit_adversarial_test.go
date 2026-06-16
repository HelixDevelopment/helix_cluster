package ratelimit

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ratelimit_adversarial_test.go — hostile numeric + concurrency probes against
// the token-bucket accounting in TokenBucket and UserLimiter.
//
// Contracts under test (from the doc comments on the production code):
//
//   - UserLimiter.Allow:  "allowed only when BOTH buckets have capacity; when
//     either is empty the request is denied". I.e. with a finite/empty bucket the
//     limiter MUST cap admissions at capacity + refilled tokens — never unbounded.
//   - userBucket.refill / TokenBucket.AllowN: tokens are capped at `max`
//     (`if tokens > max { tokens = max }`), so tokens must remain a finite count
//     in [<=max]. A bucket that holds a non-finite (NaN/±Inf) token count has
//     escaped its own invariant.
//
// The recurring killer in this codebase is NaN: a `x < 0` / `x < 1` guard and a
// `x > max` cap are BOTH false for NaN, so a NaN that reaches `tokens` slips past
// every numeric guard at once. These tests prove whether a hostile/degenerate
// rate config (NaN refill) poisons the bucket and bypasses the cap.

// ─────────────────────────────────────────────────────────────────────────────
// HXC-1906 REGRESSION GUARD (fixed): a NaN refill rate must NOT bypass the cap.
// Before the fix, if UserRefillPerSec was NaN (produced upstream by 0.0/0.0, a
// malformed config value, or an arithmetic accident), refillPerNs = NaN/1e9 =
// NaN. On the first refill with elapsed>0:  tokens += elapsed*NaN = NaN.  Then:
//   - cap check  `tokens > max`   is `NaN > max`  = false  → cap SKIPPED
//   - gate check `tokens < 1.0`   is `NaN < 1.0`  = false  → deny SKIPPED
// so every subsequent Allow() was admitted forever — a total rate-limit bypass.
//
// The fix (userBucket.refill) clamps any non-finite token count to max:
//   if math.IsNaN(b.tokens) || b.tokens > b.max { b.tokens = b.max }
// so the ceiling holds: a capacity-N bucket admits exactly N, then denies.
// This test FAILS if the guard is removed (mutation proof of load-bearing).
// ─────────────────────────────────────────────────────────────────────────────
func TestUserLimiter_NaNUserRefill_CapHolds_HXC1906(t *testing.T) {
	const cap = 2
	clk := NewFakeClock(1_000_000_000)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       cap,
		UserRefillPerSec:   math.NaN(), // hostile: NaN refill rate
		GlobalCapacity:     1_000_000,  // global never binds
		GlobalRefillPerSec: 0,
	}, clk)

	// Create the user bucket at the current instant, then advance so the next
	// refill has elapsed>0 and the NaN multiply actually fires.
	_ = ul.UserTokens("mallory")
	clk.Advance(1_000_000_000)

	admitted := 0
	const attempts = 1000
	for i := 0; i < attempts; i++ {
		if ok, _ := ul.Allow("mallory"); ok {
			admitted++
		}
	}

	// With the NaN-sanitizing fix, the cap holds: at most `cap` admitted (the
	// initially-full bucket drains to empty, NaN refill adds nothing usable).
	if admitted != cap {
		t.Fatalf("NaN refill rate must NOT bypass the cap: admitted=%d, want exactly %d "+
			"on a capacity-%d bucket (if admitted==%d the NaN guard in userBucket.refill "+
			"was removed and the rate limit is defeated)", admitted, cap, cap, attempts)
	}
}

// Companion: NaN GlobalRefillPerSec must not poison the SHARED global bucket
// (which would bypass the global ceiling for ALL users at once — the more
// dangerous blast radius). Same fix, same regression guard.
func TestUserLimiter_NaNGlobalRefill_CapHolds_HXC1906(t *testing.T) {
	const globalCap = 2
	clk := NewFakeClock(1_000_000_000)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       1_000_000, // per-user never binds
		UserRefillPerSec:   0,
		GlobalCapacity:     globalCap,
		GlobalRefillPerSec: math.NaN(), // hostile: NaN global refill rate
	}, clk)

	_ = ul.GlobalTokens() // global bucket exists from construction; lastNs=1e9
	clk.Advance(1_000_000_000)

	admitted := 0
	const attempts = 1000
	for i := 0; i < attempts; i++ {
		if ok, _ := ul.Allow("anyuser"); ok {
			admitted++
		}
	}

	if admitted != globalCap {
		t.Fatalf("NaN global refill must NOT bypass the global ceiling: admitted=%d, want "+
			"exactly %d on a global-capacity-%d bucket (admitted==%d => guard removed, "+
			"global limit defeated for every user)", admitted, globalCap, globalCap, attempts)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DOCUMENTED / DEGENERATE-CONTRACT PINS (no fix expected; lock current behavior)
// ─────────────────────────────────────────────────────────────────────────────

// +Inf refill rate is a degenerate but INTERNALLY CONSISTENT config: every call
// tops the bucket fully back to `max` (the `tokens > max` cap DOES hold for Inf),
// so the bucket never exceeds capacity — it just behaves as an "always at least
// 1 token" limiter. The cap invariant (tokens <= max) is preserved. We pin that
// +Inf does NOT drive the token count above `max` (i.e. it is capped, unlike NaN).
func TestUserLimiter_InfRefill_StillCapped_Contract(t *testing.T) {
	const cap = 2
	clk := NewFakeClock(1_000_000_000)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       cap,
		UserRefillPerSec:   math.Inf(1),
		GlobalCapacity:     1_000_000,
		GlobalRefillPerSec: 0,
	}, clk)
	_ = ul.UserTokens("u")
	clk.Advance(1_000_000_000)

	// The post-refill token readout must be a finite count, capped at `cap`.
	// (NaN would floor to 0 and mask poison; Inf floors to a huge int — but the
	// cap keeps it at exactly `cap`.)
	if got := ul.UserTokens("u"); got != cap {
		t.Fatalf("Inf refill should cap tokens at %d, got %d", cap, got)
	}
}

// Plain TokenBucket with a NaN refill rate is fail-CLOSED: NaN tokens make the
// `tokens >= n` admission test false, so every Allow is DENIED. That is safe
// (no bypass) but a silent footgun — pin it so a change to fail-OPEN is caught.
func TestTokenBucket_NaNRefill_FailsClosed_Contract(t *testing.T) {
	l := NewTokenBucket(5, math.NaN())
	time.Sleep(time.Millisecond) // ensure elapsed>0 so NaN multiply fires
	for i := 0; i < 10; i++ {
		if l.Allow() {
			t.Fatalf("TokenBucket with NaN refill admitted a request at i=%d; "+
				"NaN-poisoned bucket must fail closed (every Allow denied), not open", i)
		}
	}
}

// Time going backwards (clock skew) must NOT over-credit the UserLimiter bucket.
// userBucket.refill guards with `if nowNs > b.lastNs`, so a backward (or frozen)
// clock adds zero tokens. Pin that a backward jump grants nothing beyond cap.
func TestUserLimiter_BackwardClock_NoOverCredit_Contract(t *testing.T) {
	const cap = 3
	clk := NewFakeClock(10_000_000_000)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       cap,
		UserRefillPerSec:   1_000_000, // huge rate: any positive elapsed would refill fully
		GlobalCapacity:     1_000_000,
		GlobalRefillPerSec: 0,
	}, clk)

	// Drain the bucket at the current instant.
	drained := 0
	for i := 0; i < cap*4; i++ {
		if ok, _ := ul.Allow("t"); ok {
			drained++
		}
	}
	if drained != cap {
		t.Fatalf("setup: drained=%d want %d", drained, cap)
	}

	// Now move the clock BACKWARDS by 5 seconds (skew). refill must add nothing.
	clk.Advance(-5_000_000_000)

	admitted := 0
	for i := 0; i < cap*4; i++ {
		if ok, _ := ul.Allow("t"); ok {
			admitted++
		}
	}
	if admitted != 0 {
		t.Fatalf("backward clock OVER-CREDITED the bucket: admitted=%d after a backward "+
			"clock jump on a drained bucket (refill must be guarded by nowNs > lastNs)", admitted)
	}
}

// Huge forward elapsed must SATURATE to the cap, not overflow to a wild count.
// A near-max-int64 nanosecond jump times a positive refill is an enormous float;
// the `tokens > max` cap must clamp it to exactly `cap`.
func TestUserLimiter_HugeElapsed_SaturatesToCap_Contract(t *testing.T) {
	const cap = 4
	clk := NewFakeClock(0)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       cap,
		UserRefillPerSec:   1_000_000,
		GlobalCapacity:     1_000_000,
		GlobalRefillPerSec: 0,
	}, clk)
	_ = ul.UserTokens("h")             // create at t=0
	clk.Advance(math.MaxInt64 / 2)     // enormous forward jump

	if got := ul.UserTokens("h"); got != cap {
		t.Fatalf("huge elapsed should saturate tokens to cap=%d, got %d "+
			"(overflow or missing cap)", cap, got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CONCURRENCY: conservation under -race for the NaN-free (normal) path. This is
// the control proving the cap holds with sane config, so the NaN bypass above is
// attributable to NaN — not to a generic race.
// ─────────────────────────────────────────────────────────────────────────────
func TestUserLimiter_Adversarial_ConcurrentConservation(t *testing.T) {
	const globalCap = 256
	const goroutines = 64
	const perGoroutine = 200

	clk := NewFakeClock(1_000_000_000)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       globalCap, // per-user not the binding limit
		UserRefillPerSec:   0,
		GlobalCapacity:     globalCap,
		GlobalRefillPerSec: 0,
	}, clk)

	var admitted int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			uid := string(rune('a' + g%26))
			for i := 0; i < perGoroutine; i++ {
				if ok, _ := ul.Allow(uid); ok {
					atomic.AddInt64(&admitted, 1)
				}
			}
		}(g)
	}
	wg.Wait()

	if got := atomic.LoadInt64(&admitted); got > globalCap {
		t.Fatalf("concurrent over-admission: admitted=%d exceeds globalCap=%d", got, globalCap)
	}
}
