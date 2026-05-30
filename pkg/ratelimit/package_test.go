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
