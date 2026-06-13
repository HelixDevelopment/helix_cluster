package ratelimit

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// adversarial_race_test.go — REAL adversarial -race tests for the token-bucket
// accounting in UserLimiter (the limiter with the injectable Clock seam, so the
// admitted count in a frozen window is DETERMINISTIC, not tolerance-based).
//
// UserLimiter.Allow performs a multi-field read-modify-write across TWO buckets:
//
//	ub.refill(now); ul.global.refill(now)        // RMW on tokens/lastNs (both buckets)
//	if ub.tokens < 1 || ul.global.tokens < 1 { return false }
//	ub.tokens -= 1; ul.global.tokens -= 1        // double-decrement
//
// That is the classic over-admission / lost-update race spot. The existing
// TestUserLimiter_ConcurrentSafe only asserts a per-user UPPER bound with a
// deliberately-huge global cap (global never binds) and never asserts exactness.
// These tests add the missing teeth:
//
//  1. GLOBAL cap is the binding ceiling under heavy concurrent contention from
//     many distinct users: admitted MUST be <= GlobalCapacity AND exactly
//     == GlobalCapacity (every global token handed out, none lost, none
//     duplicated). A broken/unlocked double-decrement would either admit MORE
//     than GlobalCapacity (over-admission bite) or LESS (lost-update bite).
//  2. Token conservation across a frozen-clock refill quantum under concurrency.
//  3. -race over the two-bucket RMW finds no data race.
//
// All counting is deterministic because the FakeClock is frozen for the duration
// of each concurrent hammer (no goroutine advances it), so the algebra
// "admitted == min(sum per-user caps, global cap)" holds exactly — no tolerance.

// TestUserLimiter_GlobalCapNeverExceeded_Concurrent is the headline -race bite.
//
// Frozen clock + refill=0 ⇒ the global bucket is a HARD ceiling of exactly
// GlobalCapacity tokens for the whole run. Per-user capacity is set high enough
// that the GLOBAL bucket — the shared, double-decremented field — is the binding
// constraint. Many goroutines across many distinct users hammer Allow().
//
// Asserts:
//   - admitted <= GlobalCapacity            (NEVER over-admit: the over-admission bite)
//   - admitted == GlobalCapacity            (conservation: every global token handed
//     out exactly once — catches lost updates that would admit FEWER)
//   - GlobalTokens() == 0 afterwards        (sink-side: the shared counter landed at 0,
//     not negative, not positive)
//
// Why it bites: if the `ul.global.tokens -= 1.0` decrement were not serialized by
// ul.mu, two goroutines could both observe global.tokens == 1.0, both pass the
// `>= 1` check, and both decrement → admitted would exceed GlobalCapacity and the
// counter would go negative. Under -race the unsynchronized write is also flagged.
func TestUserLimiter_GlobalCapNeverExceeded_Concurrent(t *testing.T) {
	const globalCap = 200
	const users = 16
	const goroutines = 64
	const perGoroutine = 100 // 64*100 = 6400 attempts >> globalCap

	clk := NewFakeClock(1_000_000_000)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       globalCap, // per-user is NOT the binding constraint
		UserRefillPerSec:   0,         // frozen accounting: caps are hard ceilings
		GlobalCapacity:     globalCap, // the shared, double-decremented ceiling
		GlobalRefillPerSec: 0,
	}, clk)

	var admitted int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		// Spread callers across many users so the per-user buckets are not the
		// bottleneck — the contention lands squarely on the shared global bucket.
		uid := fmt.Sprintf("user-%d", g%users)
		go func(uid string) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if ok, _ := ul.Allow(uid); ok {
					atomic.AddInt64(&admitted, 1)
				}
			}
		}(uid)
	}
	wg.Wait()

	got := atomic.LoadInt64(&admitted)

	// THE BITE 1 — never over-admit beyond the global ceiling.
	if got > globalCap {
		t.Fatalf("OVER-ADMISSION: admitted=%d exceeds GlobalCapacity=%d "+
			"(unsynchronized double-decrement let extra requests through)", got, globalCap)
	}
	// THE BITE 2 — conservation: every global token handed out exactly once.
	if got != globalCap {
		t.Fatalf("CONSERVATION VIOLATED: admitted=%d but GlobalCapacity=%d "+
			"(lost update: a token was dropped or never granted)", got, globalCap)
	}
	// Sink-side: the shared counter must have landed at exactly 0.
	if gt := ul.GlobalTokens(); gt != 0 {
		t.Fatalf("global bucket should be drained to 0, got %d "+
			"(counter went negative or positive under concurrency)", gt)
	}
}

// TestUserLimiter_PerUserCapNeverExceeded_Concurrent proves the OTHER bucket in
// the double-decrement — the per-user bucket — is also conserved: with a generous
// global cap, each user's bucket is the binding ceiling, and the per-user total
// must be exactly UserCapacity for every user (no over-admission, no lost update),
// even with many goroutines hammering the SAME user key concurrently.
func TestUserLimiter_PerUserCapNeverExceeded_Concurrent(t *testing.T) {
	const userCap = 50
	const users = 8
	const goroutinesPerUser = 16
	const perGoroutine = 40 // 16*40 = 640 attempts/user >> userCap

	clk := NewFakeClock(1_000_000_000)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       userCap,
		UserRefillPerSec:   0,
		GlobalCapacity:     userCap * users * 10, // global never binds
		GlobalRefillPerSec: 0,
	}, clk)

	admittedPerUser := make([]int64, users)
	var wg sync.WaitGroup
	wg.Add(users * goroutinesPerUser)
	for u := 0; u < users; u++ {
		uid := fmt.Sprintf("u%d", u)
		for g := 0; g < goroutinesPerUser; g++ {
			go func(uid string, slot int) {
				defer wg.Done()
				for i := 0; i < perGoroutine; i++ {
					if ok, _ := ul.Allow(uid); ok {
						atomic.AddInt64(&admittedPerUser[slot], 1)
					}
				}
			}(uid, u)
		}
	}
	wg.Wait()

	var total int64
	for u := 0; u < users; u++ {
		got := atomic.LoadInt64(&admittedPerUser[u])
		if got > userCap {
			t.Fatalf("user u%d OVER-ADMISSION: admitted=%d exceeds UserCapacity=%d", u, got, userCap)
		}
		if got != userCap {
			t.Fatalf("user u%d CONSERVATION VIOLATED: admitted=%d want exactly %d", u, got, userCap)
		}
		total += got
		// Sink-side: the per-user bucket must be drained to exactly 0.
		if ut := ul.UserTokens(uid(u)); ut != 0 {
			t.Fatalf("user u%d bucket should be drained to 0, got %d", u, ut)
		}
	}
	if want := int64(userCap * users); total != want {
		t.Fatalf("aggregate admitted=%d, want %d", total, want)
	}
}

// uid is a tiny helper for the per-user drain check above.
func uid(u int) string { return fmt.Sprintf("u%d", u) }

// TestUserLimiter_FrozenClock_ExactCountThenRefill is the deterministic-accounting
// bite using the clock seam. With the clock FROZEN, exactly UserCapacity requests
// succeed and the rest are denied. Then advancing the clock by exactly one refill
// quantum makes EXACTLY the refilled number of additional requests succeed — no
// more, no fewer. This catches miscounting in the refill arithmetic and the
// decrement, independent of the race detector.
func TestUserLimiter_FrozenClock_ExactCountThenRefill(t *testing.T) {
	const cap = 8
	// refill = cap tokens per second ⇒ advancing 1s refills the whole bucket;
	// advancing 0.25s refills exactly cap/4 = 2 tokens.
	const refillPerSec = float64(cap)
	clk := NewFakeClock(1_000_000_000)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       cap,
		UserRefillPerSec:   refillPerSec,
		GlobalCapacity:     1_000_000, // global never binds
		GlobalRefillPerSec: 0,
	}, clk)

	// Phase 1: frozen clock ⇒ exactly `cap` succeed, then permanent denials.
	succeeded := 0
	for i := 0; i < cap*3; i++ {
		if ok, _ := ul.Allow("zoe"); ok {
			succeeded++
		}
	}
	if succeeded != cap {
		t.Fatalf("frozen-clock phase: succeeded=%d, want exactly %d", succeeded, cap)
	}
	if ut := ul.UserTokens("zoe"); ut != 0 {
		t.Fatalf("frozen-clock phase: bucket should be 0, got %d", ut)
	}

	// Phase 2: advance exactly 0.25s ⇒ exactly cap/4 = 2 tokens refilled.
	const quantumNs = 250_000_000 // 0.25s
	const wantRefilled = cap / 4  // = 2
	clk.Advance(quantumNs)

	if ut := ul.UserTokens("zoe"); ut != wantRefilled {
		t.Fatalf("after 0.25s refill: UserTokens=%d, want exactly %d", ut, wantRefilled)
	}

	refilledSucceeded := 0
	for i := 0; i < cap*3; i++ {
		if ok, _ := ul.Allow("zoe"); ok {
			refilledSucceeded++
		}
	}
	if refilledSucceeded != wantRefilled {
		t.Fatalf("post-refill phase: succeeded=%d, want exactly %d (the refilled amount)",
			refilledSucceeded, wantRefilled)
	}
}

// TestUserLimiter_ConcurrentConservationAcrossRefill stresses BOTH the concurrent
// RMW and the refill seam together: the clock is frozen during each concurrent
// hammer (deterministic), but between hammers the test advances the clock by a
// known quantum and re-hammers. Total admissions across all rounds must equal the
// initial capacity plus the sum of refilled tokens — exact conservation across the
// refill boundary under -race. A double-count in refill or a lost decrement breaks
// the exact total.
func TestUserLimiter_ConcurrentConservationAcrossRefill(t *testing.T) {
	const cap = 100
	const refillPerSec = float64(cap) // 1s ⇒ full bucket
	const goroutines = 48
	const perGoroutine = 50

	clk := NewFakeClock(1_000_000_000)
	ul := NewUserLimiterWithClock(UserLimiterConfig{
		UserCapacity:       cap,
		UserRefillPerSec:   refillPerSec,
		GlobalCapacity:     1_000_000_000, // global never binds
		GlobalRefillPerSec: 0,
	}, clk)

	hammer := func() int64 {
		var admitted int64
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for g := 0; g < goroutines; g++ {
			go func() {
				defer wg.Done()
				for i := 0; i < perGoroutine; i++ {
					if ok, _ := ul.Allow("alex"); ok {
						atomic.AddInt64(&admitted, 1)
					}
				}
			}()
		}
		wg.Wait()
		return atomic.LoadInt64(&admitted)
	}

	// Round 1: frozen clock, full bucket ⇒ exactly cap admitted.
	r1 := hammer()
	if r1 != cap {
		t.Fatalf("round 1 (frozen, full): admitted=%d, want exactly %d", r1, cap)
	}

	// Advance exactly 0.5s ⇒ exactly cap/2 tokens refilled.
	clk.Advance(500_000_000)
	const wantR2 = cap / 2

	// Round 2: frozen again at the new instant ⇒ exactly cap/2 admitted.
	r2 := hammer()
	if r2 != wantR2 {
		t.Fatalf("round 2 (after 0.5s refill): admitted=%d, want exactly %d", r2, wantR2)
	}

	// Conservation across the whole experiment: total handed out == cap + cap/2,
	// and the bucket is drained to 0 at the frozen final instant.
	if total := r1 + r2; total != cap+wantR2 {
		t.Fatalf("CONSERVATION across refill: total admitted=%d, want %d", total, cap+wantR2)
	}
	if ut := ul.UserTokens("alex"); ut != 0 {
		t.Fatalf("final bucket should be drained to 0, got %d", ut)
	}
}
