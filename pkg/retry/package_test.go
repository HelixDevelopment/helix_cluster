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

// --- Mutation tests ---

func TestDo_ContextCancellation_Mutation(t *testing.T) {
	// Mutation: ctx.Done() check removed → retry continues even after cancellation
	ctx, cancel := context.WithCancel(context.Background())
	callCount := 0
	s := Strategy{MaxAttempts: 10, Delay: 50 * time.Millisecond}
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	err := Do(ctx, s, func() error {
		callCount++
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
	if ctx.Err() == nil {
		t.Fatal("context should have been cancelled")
	}
	// Should have exited early due to cancellation, not exhausted all attempts
	if callCount >= 10 {
		t.Error("retry should have stopped early because of context cancellation")
	}
}

func TestDo_MaxAttemptsRespected_Mutation(t *testing.T) {
	// Mutation: loop condition changed to infinite or off-by-one
	callCount := 0
	s := Strategy{MaxAttempts: 3, Delay: 5 * time.Millisecond}
	_ = Do(context.Background(), s, func() error {
		callCount++
		return errors.New("fail")
	})
	if callCount != 3 {
		t.Errorf("expected exactly 3 attempts, got %d", callCount)
	}
}

func TestDo_ReturnsLastError_Mutation(t *testing.T) {
	// Mutation: lastErr not captured → returns generic or nil error
	expectedErr := errors.New("specific failure")
	s := Strategy{MaxAttempts: 2, Delay: 5 * time.Millisecond}
	err := Do(context.Background(), s, func() error {
		return expectedErr
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, expectedErr) {
		t.Errorf("expected wrapped specific error, got %v", err)
	}
}
