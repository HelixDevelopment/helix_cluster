package ratelimit

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// ── HXC-1133 deterministic tests ────────────────────────────────────────────
// All tests use FakeClock so there are no real sleeps.

// TestUserLimiter_Allow_UserBucketDeny proves:
//   - Requests succeed while the per-user bucket has capacity.
//   - The (cap+1)th request is denied with remaining==0.
//   - No token is consumed on denial (bucket stays at 0 — not negative).
//
// §1.1 mutation target (recorded in mutation_proof): removing the `ub.tokens < 1.0`
// guard in Allow allows the over-limit request through and this test fails.
func TestUserLimiter_Allow_UserBucketDeny(t *testing.T) {
	const cap = 3
	clk := NewFakeClock(1_000_000_000)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       cap,
		UserRefillPerSec:   0, // no refill — exhaustion is permanent until clock advances
		GlobalCapacity:     1000,
		GlobalRefillPerSec: 0,
	}, clk)

	// Drain the per-user bucket exactly.
	for i := 1; i <= cap; i++ {
		allowed, remaining := ul.Allow("alice")
		if !allowed {
			t.Fatalf("request %d/%d: expected allowed=true, got false", i, cap)
		}
		wantRemaining := cap - i
		if remaining != wantRemaining {
			t.Fatalf("request %d/%d: remaining=%d want %d", i, cap, remaining, wantRemaining)
		}
	}

	// Per-user bucket is now empty: verify before the denied call.
	userToks := ul.UserTokens("alice")
	if userToks != 0 {
		t.Fatalf("UserTokens(alice) = %d after exhaustion, want 0", userToks)
	}

	// (cap+1)th request must be denied.
	allowed, remaining := ul.Allow("alice")
	if allowed {
		t.Fatalf("over-limit request: expected allowed=false (deny), got true")
	}
	if remaining != 0 {
		t.Fatalf("over-limit request: remaining=%d want 0", remaining)
	}

	// Denial must NOT consume tokens (bucket stays at 0, not negative).
	userToksAfterDeny := ul.UserTokens("alice")
	if userToksAfterDeny != 0 {
		t.Fatalf("UserTokens(alice) after denial = %d, want 0 (denial must not consume tokens)", userToksAfterDeny)
	}
}

// TestUserLimiter_Allow_RecoveryAfterRefill proves that after advancing the fake
// clock past the refill interval, Allow returns true again.
//
// §1.1 mutation target: zero-out refillPerNs (set UserRefillPerSec=0 in
// getOrCreateUser) → Allow still returns false after clock advance → test fails.
func TestUserLimiter_Allow_RecoveryAfterRefill(t *testing.T) {
	const cap = 2
	const refillPerSec = float64(cap) // refill full capacity in 1 second
	clk := NewFakeClock(1_000_000_000)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       cap,
		UserRefillPerSec:   refillPerSec,
		GlobalCapacity:     1000,
		GlobalRefillPerSec: 1000,
	}, clk)

	// Exhaust per-user bucket.
	for i := 0; i < cap; i++ {
		allowed, _ := ul.Allow("bob")
		if !allowed {
			t.Fatalf("drain request %d: expected allowed=true", i+1)
		}
	}
	if allowed, _ := ul.Allow("bob"); allowed {
		t.Fatal("over-limit request after drain: expected allowed=false")
	}

	// Advance clock by 1 full second (1e9 ns) — should fully refill the bucket.
	clk.Advance(1_000_000_000)

	allowed, remaining := ul.Allow("bob")
	if !allowed {
		t.Fatal("after clock advance past refill interval: expected allowed=true, got false")
	}
	// After refill to cap and consuming one, remaining should be cap-1.
	if remaining != cap-1 {
		t.Fatalf("remaining after refill+consume = %d, want %d", remaining, cap-1)
	}
}

// TestUserLimiter_TokenCountsBeforeAndAfter captures token counts before and
// after requests to prove the bucket decrements and refills correctly.
func TestUserLimiter_TokenCountsBeforeAndAfter(t *testing.T) {
	const cap = 5
	clk := NewFakeClock(0)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       cap,
		UserRefillPerSec:   float64(cap),
		GlobalCapacity:     1000,
		GlobalRefillPerSec: 1000,
	}, clk)

	// Force bucket creation by observing initial count.
	_ = ul.UserTokens("charlie")

	// Before any request: bucket should be full.
	before := ul.UserTokens("charlie")
	if before != cap {
		t.Fatalf("UserTokens before any Allow = %d, want %d (full)", before, cap)
	}

	// After one Allow, tokens should be cap-1.
	allowed, remaining := ul.Allow("charlie")
	if !allowed {
		t.Fatal("first Allow: expected true")
	}
	if remaining != cap-1 {
		t.Fatalf("remaining after first Allow = %d, want %d", remaining, cap-1)
	}
	afterOne := ul.UserTokens("charlie")
	if afterOne != cap-1 {
		t.Fatalf("UserTokens after first Allow = %d, want %d", afterOne, cap-1)
	}

	// Advance by 200ms (0.2s). With refillPerSec=5, should add exactly 1 token.
	clk.Advance(200_000_000) // 200ms in ns

	afterRefill := ul.UserTokens("charlie")
	if afterRefill != cap {
		t.Fatalf("UserTokens after 200ms refill = %d, want %d (1 token added to %d)", afterRefill, cap, afterOne)
	}
}

// TestUserLimiter_GlobalBucketDeny proves the GLOBAL bucket also limits: even if
// the per-user bucket has tokens, a request is denied when the global bucket is
// empty.
//
// §1.1 mutation target: removing the `ul.global.tokens < 1.0` check in Allow lets
// the over-global-limit request through → this test fails.
func TestUserLimiter_GlobalBucketDeny(t *testing.T) {
	clk := NewFakeClock(1_000_000_000)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       100, // plenty of per-user capacity
		UserRefillPerSec:   0,
		GlobalCapacity:     2, // global bucket runs out fast
		GlobalRefillPerSec: 0,
	}, clk)

	// Exhaust the global bucket across two different users.
	allowed1, _ := ul.Allow("user1")
	if !allowed1 {
		t.Fatal("first global token: expected allowed=true")
	}
	allowed2, _ := ul.Allow("user2")
	if !allowed2 {
		t.Fatal("second global token: expected allowed=true")
	}

	// Now global is empty; any user must be denied even though per-user is fine.
	allowed3, remaining3 := ul.Allow("user3")
	if allowed3 {
		t.Fatal("global bucket empty: expected allowed=false (global deny)")
	}
	if remaining3 != 0 {
		t.Fatalf("global bucket deny: remaining=%d, want 0", remaining3)
	}

	globalToks := ul.GlobalTokens()
	if globalToks != 0 {
		t.Fatalf("GlobalTokens after exhaustion = %d, want 0", globalToks)
	}
}

// TestUserLimiter_UserBucketsIsolated proves that exhausting one user's bucket
// does not affect another user's bucket.
func TestUserLimiter_UserBucketsIsolated(t *testing.T) {
	clk := NewFakeClock(0)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       1,
		UserRefillPerSec:   0,
		GlobalCapacity:     1000,
		GlobalRefillPerSec: 0,
	}, clk)

	// Exhaust user-A.
	allowed, _ := ul.Allow("userA")
	if !allowed {
		t.Fatal("userA first Allow: expected true")
	}
	allowed, _ = ul.Allow("userA")
	if allowed {
		t.Fatal("userA second Allow after exhaustion: expected false")
	}

	// user-B has its own fresh bucket and must be allowed.
	allowedB, _ := ul.Allow("userB")
	if !allowedB {
		t.Fatal("userB Allow: expected true (independent bucket from userA)")
	}
}

// TestUserLimiter_KeyFormat proves the canonical key format for observability:
// clusteros:ratelimit:<userID> and clusteros:ratelimit:global.
//
// §1.1 mutation target: change the format string in userKey → assertion fails.
func TestUserLimiter_KeyFormat(t *testing.T) {
	uid := "test-user-42"
	got := userKey(uid)
	want := "clusteros:ratelimit:test-user-42"
	if got != want {
		t.Fatalf("userKey(%q) = %q, want %q", uid, got, want)
	}
	if globalKeyStr != "clusteros:ratelimit:global" {
		t.Fatalf("globalKeyStr = %q, want %q", globalKeyStr, "clusteros:ratelimit:global")
	}
}

// TestUserLimiter_GlobalRefillRecovery proves the global bucket also refills
// when the clock advances.
func TestUserLimiter_GlobalRefillRecovery(t *testing.T) {
	clk := NewFakeClock(0)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       100,
		UserRefillPerSec:   100,
		GlobalCapacity:     1,
		GlobalRefillPerSec: 1.0, // 1 token per second
	}, clk)

	// Exhaust global bucket.
	allowed, _ := ul.Allow("u1")
	if !allowed {
		t.Fatal("expected allowed on first call")
	}
	allowed, _ = ul.Allow("u1")
	if allowed {
		t.Fatal("expected denied on second call (global empty)")
	}

	// Advance 1 second to fully refill the global bucket.
	clk.Advance(1_000_000_000)

	allowed, _ = ul.Allow("u1")
	if !allowed {
		t.Fatal("expected allowed after global bucket refill")
	}
}

// TestUserLimiter_ConcurrentSafe proves UserLimiter is race-safe under -race.
// Multiple goroutines call Allow concurrently on shared and distinct user keys.
// The total grants must never exceed per-user capacity * number_of_users.
func TestUserLimiter_ConcurrentSafe(t *testing.T) {
	const cap = 10
	const goroutines = 20
	const users = 4
	clk := NewFakeClock(0)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       cap,
		UserRefillPerSec:   0,
		GlobalCapacity:     cap * users * 2,
		GlobalRefillPerSec: 0,
	}, clk)

	var totalGranted int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		userID := fmt.Sprintf("user-%d", g%users)
		go func(uid string) {
			defer wg.Done()
			for i := 0; i < cap*3; i++ {
				if allowed, _ := ul.Allow(uid); allowed {
					atomic.AddInt64(&totalGranted, 1)
				}
			}
		}(userID)
	}
	wg.Wait()

	// Each of `users` buckets has cap=10 tokens max.
	maxPossible := int64(cap * users)
	if totalGranted > maxPossible {
		t.Errorf("totalGranted=%d exceeds maxPossible=%d: race-condition or bucket overflow", totalGranted, maxPossible)
	}
}

// TestUserLimiter_DenyDoesNotConsumeTokens is the §1.1 mutation-facing guard
// proving that a denied call (bucket empty) leaves the bucket unchanged — it
// must NOT go negative. This is the direct observable invariant that the
// removed-guard mutation would break.
func TestUserLimiter_DenyDoesNotConsumeTokens(t *testing.T) {
	clk := NewFakeClock(0)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       2,
		UserRefillPerSec:   0,
		GlobalCapacity:     100,
		GlobalRefillPerSec: 0,
	}, clk)

	ul.Allow("eve") // consume 1 → 1 remaining
	ul.Allow("eve") // consume 1 → 0 remaining
	toksBefore := ul.UserTokens("eve")
	if toksBefore != 0 {
		t.Fatalf("precondition: expected 0 tokens, got %d", toksBefore)
	}

	// Attempt 3 denied calls.
	for i := 0; i < 3; i++ {
		allowed, remaining := ul.Allow("eve")
		if allowed {
			t.Fatalf("call %d: expected deny, got allow", i+1)
		}
		if remaining != 0 {
			t.Fatalf("call %d: remaining=%d, want 0", i+1, remaining)
		}
	}

	// Tokens must still be exactly 0, not -3.
	toksAfter := ul.UserTokens("eve")
	if toksAfter != 0 {
		t.Fatalf("tokens after 3 denied calls = %d, want 0 (denial must not decrease below 0)", toksAfter)
	}
}
