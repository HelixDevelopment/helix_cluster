package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Compile-time interface conformance: prove the central Limiter abstraction
// is actually satisfied by its implementor(s). PerKeyLimiter is the only type
// implementing the keyed Limiter interface.
var _ Limiter = (*PerKeyLimiter)(nil)

func TestTokenBucket(t *testing.T) {
	l := NewTokenBucket(2, 1)
	if !l.Allow() {
		t.Error("expected first allow to succeed")
	}
	if !l.Allow() {
		t.Error("expected second allow to succeed")
	}
	if l.Allow() {
		t.Error("expected third allow to fail")
	}
	time.Sleep(2 * time.Second)
	if !l.Allow() {
		t.Error("expected allow after refill")
	}
}

func TestTokenBucketAllowN(t *testing.T) {
	l := NewTokenBucket(5, 1)
	if !l.AllowN(3) {
		t.Error("expected AllowN(3) to succeed")
	}
	if l.AllowN(3) {
		t.Error("expected AllowN(3) to fail (only 2 left)")
	}
	if !l.AllowN(2) {
		t.Error("expected AllowN(2) to succeed")
	}
}

func TestSlidingWindow(t *testing.T) {
	sw := NewSlidingWindow(3, time.Second)
	if !sw.Allow() {
		t.Error("expected first allow")
	}
	if !sw.Allow() {
		t.Error("expected second allow")
	}
	if !sw.Allow() {
		t.Error("expected third allow")
	}
	if sw.Allow() {
		t.Error("expected fourth allow to fail")
	}
	time.Sleep(1100 * time.Millisecond)
	if !sw.Allow() {
		t.Error("expected allow after window slides")
	}
}

func TestSlidingWindowAllowN(t *testing.T) {
	sw := NewSlidingWindow(5, time.Second)
	if !sw.AllowN(3) {
		t.Error("expected AllowN(3)")
	}
	if !sw.AllowN(2) {
		t.Error("expected AllowN(2)")
	}
	if sw.AllowN(1) {
		t.Error("expected AllowN(1) to fail")
	}
}

func TestPerKeyLimiter(t *testing.T) {
	p := NewPerKeyLimiter(func() interface{ Allow() bool; AllowN(int) bool } {
		return NewTokenBucket(2, 1)
	})
	if !p.Allow("key1") {
		t.Error("expected key1 first allow")
	}
	if !p.Allow("key1") {
		t.Error("expected key1 second allow")
	}
	if p.Allow("key1") {
		t.Error("expected key1 third allow to fail")
	}
	if !p.Allow("key2") {
		t.Error("expected key2 first allow (independent)")
	}
}

func TestPerKeyLimiterAllowN(t *testing.T) {
	p := NewPerKeyLimiter(func() interface{ Allow() bool; AllowN(int) bool } {
		return NewTokenBucket(5, 1)
	})
	if !p.AllowN("a", 3) {
		t.Error("expected AllowN(3) for key a")
	}
	if p.AllowN("a", 3) {
		t.Error("expected AllowN(3) to fail for key a")
	}
	if !p.AllowN("b", 5) {
		t.Error("expected AllowN(5) for key b")
	}
}

// --- Mutation tests ---

func TestTokenBucket_MaxCap_Mutation(t *testing.T) {
	l := NewTokenBucket(1, 1000)
	l.Allow() // consume the single token
	time.Sleep(10 * time.Millisecond) // accumulate many tokens
	count := 0
	for l.Allow() {
		count++
		if count > 1 {
			break
		}
	}
	if count > 1 {
		t.Error("tokens should be capped at max; only one allow expected after refill")
	}
}

func TestTokenBucket_RefillCalculation_Mutation(t *testing.T) {
	l := NewTokenBucket(1, 1)
	l.Allow() // exhaust
	time.Sleep(1100 * time.Millisecond)
	if !l.Allow() {
		t.Error("after ~1 second with refill=1/sec, allow should succeed")
	}
}

func TestTokenBucket_ZeroTokensInitially_Mutation(t *testing.T) {
	l := NewTokenBucket(5, 1)
	if !l.Allow() {
		t.Error("new limiter should start with tokens available")
	}
}

// TestTokenBucket_StartsFull_Mutation proves the bucket starts at FULL
// capacity (exactly `cap` tokens), not partially filled. refill=0 isolates the
// initial state so no time-based refill can mask an undersized initial fill.
// A bucket that started with, say, 1 token would fail at the 2nd Allow().
func TestTokenBucket_StartsFull_Mutation(t *testing.T) {
	const cap = 5
	l := NewTokenBucket(cap, 0) // refill=0: only the initial fill is ever available
	for i := 0; i < cap; i++ {
		if !l.Allow() {
			t.Fatalf("expected Allow #%d/%d to succeed: bucket must start at full capacity", i+1, cap)
		}
	}
	if l.Allow() {
		t.Errorf("expected Allow #%d to fail: bucket started with more than %d tokens", cap+1, cap)
	}
}

// TestLimiter_InterfaceExercised drives a value THROUGH the Limiter interface
// (not the concrete type) so the abstraction is genuinely exercised, not just
// declared. Proves PerKeyLimiter behaves correctly when used polymorphically.
func TestLimiter_InterfaceExercised(t *testing.T) {
	var lim Limiter = NewPerKeyLimiter(func() interface {
		Allow() bool
		AllowN(int) bool
	} {
		return NewTokenBucket(1, 0)
	})
	if !lim.Allow("k") {
		t.Error("expected first Allow via Limiter interface to succeed")
	}
	if lim.Allow("k") {
		t.Error("expected second Allow via Limiter interface to fail (capacity 1)")
	}
	if !lim.AllowN("other", 1) {
		t.Error("expected AllowN via Limiter interface on a fresh key to succeed")
	}
}

// TestTokenBucket_ConcurrentGrantsNeverExceedCap is a -race concurrency test
// asserting the "granted <= max" invariant: under heavy concurrent AllowN(1)
// on a no-refill bucket, the total number of granted tokens must never exceed
// the bucket capacity. This proves the mutex actually guards the counter; with
// a broken/removed lock the race detector trips and/or grants exceed cap.
func TestTokenBucket_ConcurrentGrantsNeverExceedCap(t *testing.T) {
	const cap = 100
	const goroutines = 64
	const perGoroutine = 50 // 64*50 = 3200 attempts >> cap

	l := NewTokenBucket(cap, 0) // refill=0 so capacity is a hard ceiling
	var granted int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if l.AllowN(1) {
					atomic.AddInt64(&granted, 1)
				}
			}
		}()
	}
	wg.Wait()
	if granted > cap {
		t.Errorf("granted %d tokens but capacity is %d: invariant granted<=max violated", granted, cap)
	}
	if granted != cap {
		t.Errorf("expected exactly %d grants (every token handed out), got %d", cap, granted)
	}
}

// TestPerKeyLimiter_ConcurrentGrantsNeverExceedCap exercises the same
// granted<=max invariant through PerKeyLimiter on a single shared key under
// -race, proving its map access + delegated counter are lock-safe.
func TestPerKeyLimiter_ConcurrentGrantsNeverExceedCap(t *testing.T) {
	const cap = 100
	const goroutines = 64
	const perGoroutine = 50

	p := NewPerKeyLimiter(func() interface {
		Allow() bool
		AllowN(int) bool
	} {
		return NewTokenBucket(cap, 0)
	})
	var granted int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if p.AllowN("shared", 1) {
					atomic.AddInt64(&granted, 1)
				}
			}
		}()
	}
	wg.Wait()
	if granted > cap {
		t.Errorf("granted %d tokens across one key but capacity is %d: invariant violated", granted, cap)
	}
	if granted != cap {
		t.Errorf("expected exactly %d grants on the shared key, got %d", cap, granted)
	}
}

// TestTokenBucket_DeniedAllowN_NoSideEffect proves a DENIED AllowN consumes no
// tokens: a request that cannot fit must leave the bucket untouched so a later
// fitting request still succeeds. refill=0 isolates the no-side-effect property
// from time-based refill.
func TestTokenBucket_DeniedAllowN_NoSideEffect(t *testing.T) {
	l := NewTokenBucket(3, 0)
	if l.AllowN(5) {
		t.Fatal("expected AllowN(5) to be denied on a capacity-3 bucket")
	}
	// The denied request must not have consumed any tokens.
	if !l.AllowN(3) {
		t.Error("expected AllowN(3) to succeed after a denied AllowN(5): denial must not consume tokens")
	}
	if l.AllowN(1) {
		t.Error("bucket should now be exhausted (all 3 tokens consumed by the fitting request)")
	}
}

// TestSlidingWindow_DeniedAllowN_NoSideEffect proves a rejected SlidingWindow
// AllowN does not consume window slots (atomic all-or-nothing): after a denied
// oversized batch, a subsequent fitting batch must still fit completely.
func TestSlidingWindow_DeniedAllowN_NoSideEffect(t *testing.T) {
	sw := NewSlidingWindow(3, time.Second)
	if sw.AllowN(5) {
		t.Fatal("expected AllowN(5) to be denied on a limit-3 window")
	}
	// Rejection must not have recorded any events.
	if !sw.AllowN(3) {
		t.Error("expected AllowN(3) to succeed after a denied AllowN(5): rejection must not consume window slots")
	}
	if sw.AllowN(1) {
		t.Error("window should be full now; AllowN(1) must fail")
	}
}

func TestSlidingWindow_Slides_Mutation(t *testing.T) {
	sw := NewSlidingWindow(2, 200*time.Millisecond)
	if !sw.Allow() || !sw.Allow() {
		t.Fatal("expected first two allows")
	}
	if sw.Allow() {
		t.Error("third should fail")
	}
	time.Sleep(250 * time.Millisecond)
	if !sw.Allow() {
		t.Error("window should have slid, allowing new request")
	}
}

func TestPerKeyLimiter_Isolated_Mutation(t *testing.T) {
	p := NewPerKeyLimiter(func() interface{ Allow() bool; AllowN(int) bool } {
		return NewTokenBucket(1, 1)
	})
	p.Allow("x")
	if !p.Allow("y") {
		t.Error("keys should be isolated; y should allow even if x exhausted")
	}
}
