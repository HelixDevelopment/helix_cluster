package retry

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDoSuccess(t *testing.T) {
	r := &Retry{MaxAttempts: 3, Delay: 10 * time.Millisecond, BackoffStrategy: Fixed}
	callCount := 0
	err := r.Do(context.Background(), func() error {
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
	r := &Retry{MaxAttempts: 2, Delay: 5 * time.Millisecond, BackoffStrategy: Fixed}
	err := r.Do(context.Background(), func() error {
		return errors.New("always fails")
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDoWithResultSuccess(t *testing.T) {
	r := &Retry{MaxAttempts: 3, Delay: 5 * time.Millisecond, BackoffStrategy: Fixed}
	val, err := DoWithResult(context.Background(), r, func() (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "ok" {
		t.Errorf("expected ok, got %s", val)
	}
}

func TestDoWithResultExhausted(t *testing.T) {
	r := &Retry{MaxAttempts: 2, Delay: 5 * time.Millisecond, BackoffStrategy: Fixed}
	_, err := DoWithResult(context.Background(), r, func() (int, error) {
		return 0, errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExponentialBackoff(t *testing.T) {
	r := &Retry{MaxAttempts: 4, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond, BackoffStrategy: Exponential}
	start := time.Now()
	_ = r.Do(context.Background(), func() error {
		return errors.New("fail")
	})
	elapsed := time.Since(start)
	// Minimum expected: 10 + 20 + 40 = 70ms (capped at 50 for last)
	if elapsed < 60*time.Millisecond {
		t.Errorf("expected exponential delays, elapsed: %v", elapsed)
	}
}

func TestLinearBackoff(t *testing.T) {
	r := &Retry{MaxAttempts: 3, Delay: 10 * time.Millisecond, BackoffStrategy: Linear}
	start := time.Now()
	_ = r.Do(context.Background(), func() error {
		return errors.New("fail")
	})
	elapsed := time.Since(start)
	// Minimum expected: 10 + 20 = 30ms
	if elapsed < 25*time.Millisecond {
		t.Errorf("expected linear delays, elapsed: %v", elapsed)
	}
}

func TestMaxDelayCap(t *testing.T) {
	r := &Retry{MaxAttempts: 5, Delay: 100 * time.Millisecond, MaxDelay: 150 * time.Millisecond, BackoffStrategy: Exponential}
	start := time.Now()
	_ = r.Do(context.Background(), func() error {
		return errors.New("fail")
	})
	elapsed := time.Since(start)
	// Without cap: 100 + 200 + 400 + 800 = 1500ms. With cap: 100 + 150 + 150 + 150 = 550ms.
	if elapsed > 700*time.Millisecond {
		t.Errorf("max delay not respected, elapsed: %v", elapsed)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Retry{MaxAttempts: 10, Delay: 50 * time.Millisecond, BackoffStrategy: Fixed}
	callCount := 0
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	err := r.Do(ctx, func() error {
		callCount++
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
	if ctx.Err() == nil {
		t.Fatal("context should have been cancelled")
	}
	if callCount >= 10 {
		t.Error("retry should have stopped early because of context cancellation")
	}
}

func TestNonRetryableError(t *testing.T) {
	r := DefaultRetry()
	callCount := 0
	err := r.Do(context.Background(), func() error {
		callCount++
		return fmt.Errorf("wrapped: %w", ErrNonRetryable)
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if callCount != 1 {
		t.Errorf("expected 1 call for non-retryable error, got %d", callCount)
	}
}

func TestIsRetryable(t *testing.T) {
	r := DefaultRetry()
	if r.IsRetryable(nil) {
		t.Error("nil error should not be retryable")
	}
	if !r.IsRetryable(errors.New("any")) {
		t.Error("any error should be retryable by default")
	}
	if r.IsRetryable(fmt.Errorf("wrapped: %w", ErrNonRetryable)) {
		t.Error("ErrNonRetryable wrapped should not be retryable")
	}
}

func TestIsRetryableError(t *testing.T) {
	if IsRetryableError(nil) {
		t.Error("nil should not be retryable")
	}
	if !IsRetryableError(errors.New("any")) {
		t.Error("any error should be retryable")
	}
	if IsRetryableError(fmt.Errorf("wrapped: %w", ErrNonRetryable)) {
		t.Error("non-retryable wrapped error should return false")
	}
}

func TestDefaultRetry(t *testing.T) {
	r := DefaultRetry()
	if r.MaxAttempts != 3 {
		t.Errorf("expected default 3 attempts, got %d", r.MaxAttempts)
	}
	if r.BackoffStrategy != Exponential {
		t.Error("expected exponential default strategy")
	}
	if !r.Jitter {
		t.Error("expected jitter enabled by default")
	}
}

func TestPackageLevelDo(t *testing.T) {
	callCount := 0
	err := Do(context.Background(), func() error {
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

// --- Mutation tests ---

func TestDo_ContextCancellation_Mutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	callCount := 0
	r := &Retry{MaxAttempts: 10, Delay: 50 * time.Millisecond, BackoffStrategy: Fixed}
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	err := r.Do(ctx, func() error {
		callCount++
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
	if ctx.Err() == nil {
		t.Fatal("context should have been cancelled")
	}
	if callCount >= 10 {
		t.Error("retry should have stopped early because of context cancellation")
	}
}

func TestDo_MaxAttemptsRespected_Mutation(t *testing.T) {
	callCount := 0
	r := &Retry{MaxAttempts: 3, Delay: 5 * time.Millisecond, BackoffStrategy: Fixed}
	_ = r.Do(context.Background(), func() error {
		callCount++
		return errors.New("fail")
	})
	if callCount != 3 {
		t.Errorf("expected exactly 3 attempts, got %d", callCount)
	}
}

func TestDo_ReturnsLastError_Mutation(t *testing.T) {
	expectedErr := errors.New("specific failure")
	r := &Retry{MaxAttempts: 2, Delay: 5 * time.Millisecond, BackoffStrategy: Fixed}
	err := r.Do(context.Background(), func() error {
		return expectedErr
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, expectedErr) {
		t.Errorf("expected wrapped specific error, got %v", err)
	}
}

func TestMutation_ExponentialDoubles(t *testing.T) {
	r := &Retry{MaxAttempts: 4, Delay: 10 * time.Millisecond, MaxDelay: 1 * time.Second, BackoffStrategy: Exponential}
	start := time.Now()
	_ = r.Do(context.Background(), func() error {
		return errors.New("fail")
	})
	elapsed := time.Since(start)
	// 10 + 20 + 40 = 70ms minimum
	if elapsed < 60*time.Millisecond {
		t.Error("mutation: exponential backoff should double each time")
	}
}

func TestMutation_JitterReducesDeterminism(t *testing.T) {
	r := &Retry{MaxAttempts: 3, Delay: 100 * time.Millisecond, Jitter: true, BackoffStrategy: Fixed}
	var durations []time.Duration
	for i := 0; i < 5; i++ {
		start := time.Now()
		_ = r.Do(context.Background(), func() error {
			return errors.New("fail")
		})
		durations = append(durations, time.Since(start))
	}
	// With jitter, not all durations should be identical.
	allSame := true
	for i := 1; i < len(durations); i++ {
		if durations[i] != durations[0] {
			allSame = false
			break
		}
	}
	if allSame {
		t.Error("mutation: jitter should introduce variability")
	}
}
