package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDoSuccess(t *testing.T) {
	s := Strategy{MaxAttempts: 3, Delay: 10 * time.Millisecond}
	callCount := 0
	err := Do(context.Background(), s, func() error {
		callCount++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
}

func TestDoExhausted(t *testing.T) {
	s := Strategy{MaxAttempts: 2, Delay: 10 * time.Millisecond}
	err := Do(context.Background(), s, func() error {
		return errors.New("always fails")
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
