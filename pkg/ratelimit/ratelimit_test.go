package ratelimit

import (
	"testing"
	"time"
)

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
