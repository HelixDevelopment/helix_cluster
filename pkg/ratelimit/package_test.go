package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	l := NewLimiter(2, 1)
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

// --- Mutation tests ---

func TestLimiter_MaxCap_Mutation(t *testing.T) {
	// Mutation: tokens cap removed → accumulated tokens can exceed max
	l := NewLimiter(1, 1000)
	l.Allow() // consume the single token
	time.Sleep(10 * time.Millisecond) // accumulate many tokens
	// With cap, even after sleep we should only get 1 token back
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

func TestLimiter_RefillCalculation_Mutation(t *testing.T) {
	// Mutation: refill uses wrong time unit (e.g. milliseconds instead of seconds)
	l := NewLimiter(1, 1)
	l.Allow() // exhaust
	time.Sleep(1100 * time.Millisecond)
	if !l.Allow() {
		t.Error("after ~1 second with refill=1/sec, allow should succeed")
	}
}

func TestLimiter_ZeroTokensInitially_Mutation(t *testing.T) {
	// Mutation: NewLimiter starts with 0 tokens instead of max
	l := NewLimiter(5, 1)
	if !l.Allow() {
		t.Error("new limiter should start with tokens available")
	}
}
