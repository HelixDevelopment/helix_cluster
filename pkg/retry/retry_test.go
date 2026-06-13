package retry

import (
	"context"
	"errors"
	"fmt"
	"math"
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

// --- DoWithResult parity tests (bring generic variant to parity with Do) ---

// TestDoWithResult_MaxAttemptsRespected_Mutation proves the generic variant
// invokes fn exactly MaxAttempts times on persistent failure (sink-side count),
// not merely that an error is returned.
func TestDoWithResult_MaxAttemptsRespected_Mutation(t *testing.T) {
	callCount := 0
	r := &Retry{MaxAttempts: 3, Delay: 5 * time.Millisecond, BackoffStrategy: Fixed}
	_, _ = DoWithResult(context.Background(), r, func() (int, error) {
		callCount++
		return 0, errors.New("fail")
	})
	if callCount != 3 {
		t.Errorf("expected exactly 3 attempts, got %d", callCount)
	}
}

// TestDoWithResult_ReturnsLastError_Mutation proves the final error wraps the
// last underlying error (errors.Is), matching Do's wrapping contract.
func TestDoWithResult_ReturnsLastError_Mutation(t *testing.T) {
	expectedErr := errors.New("specific failure")
	r := &Retry{MaxAttempts: 2, Delay: 5 * time.Millisecond, BackoffStrategy: Fixed}
	_, err := DoWithResult(context.Background(), r, func() (string, error) {
		return "", expectedErr
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, expectedErr) {
		t.Errorf("expected wrapped specific error, got %v", err)
	}
}

// TestDoWithResult_ZeroValueOnFailure proves the returned value is the type's
// zero value when all attempts fail (no leak of a partial/last fn value).
func TestDoWithResult_ZeroValueOnFailure(t *testing.T) {
	r := &Retry{MaxAttempts: 3, Delay: 5 * time.Millisecond, BackoffStrategy: Fixed}
	val, err := DoWithResult(context.Background(), r, func() (string, error) {
		// fn returns a non-zero value alongside the error to prove it is discarded.
		return "leaked-partial", errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if val != "" {
		t.Errorf("expected zero value on failure, got %q", val)
	}

	// Same assertion for a non-string type to pin the generic zero value.
	iv, ierr := DoWithResult(context.Background(), r, func() (int, error) {
		return 42, errors.New("fail")
	})
	if ierr == nil {
		t.Fatal("expected error")
	}
	if iv != 0 {
		t.Errorf("expected zero int on failure, got %d", iv)
	}
}

// TestDoWithResult_NonRetryable_Mutation proves the generic variant short-circuits
// on a non-retryable error after exactly one call and returns the zero value.
func TestDoWithResult_NonRetryable_Mutation(t *testing.T) {
	r := DefaultRetry()
	callCount := 0
	val, err := DoWithResult(context.Background(), r, func() (int, error) {
		callCount++
		return 7, fmt.Errorf("wrapped: %w", ErrNonRetryable)
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if callCount != 1 {
		t.Errorf("expected 1 call for non-retryable error, got %d", callCount)
	}
	if val != 0 {
		t.Errorf("expected zero value on non-retryable failure, got %d", val)
	}
	if !errors.Is(err, ErrNonRetryable) {
		t.Errorf("expected error to wrap ErrNonRetryable, got %v", err)
	}
}

// TestDoWithResult_ContextCancellation_Mutation proves the generic variant
// aborts early on ctx cancellation rather than running to exhaustion.
func TestDoWithResult_ContextCancellation_Mutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	callCount := 0
	r := &Retry{MaxAttempts: 10, Delay: 50 * time.Millisecond, BackoffStrategy: Fixed}
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, err := DoWithResult(ctx, r, func() (int, error) {
		callCount++
		return 0, errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("context should have been cancelled")
	}
	if callCount >= 10 {
		t.Error("retry should have stopped early because of context cancellation")
	}
}

// TestDoWithResult_ExponentialTiming_Mutation proves the generic variant applies
// the exponential backoff schedule between attempts (real wall-clock lower bound).
func TestDoWithResult_ExponentialTiming_Mutation(t *testing.T) {
	r := &Retry{MaxAttempts: 4, Delay: 10 * time.Millisecond, MaxDelay: 1 * time.Second, BackoffStrategy: Exponential}
	start := time.Now()
	_, _ = DoWithResult(context.Background(), r, func() (int, error) {
		return 0, errors.New("fail")
	})
	elapsed := time.Since(start)
	// 10 + 20 + 40 = 70ms minimum between the 4 attempts.
	if elapsed < 60*time.Millisecond {
		t.Errorf("expected exponential delays in DoWithResult, elapsed: %v", elapsed)
	}
}

// --- Transient-recovery tests (the actual point of a retry library) ---

// TestDo_TransientRecovery proves Do recovers after K transient failures and
// asserts the call count is exactly K+1.
func TestDo_TransientRecovery(t *testing.T) {
	const failuresBeforeSuccess = 2
	r := &Retry{MaxAttempts: 5, Delay: 2 * time.Millisecond, BackoffStrategy: Fixed}
	callCount := 0
	err := r.Do(context.Background(), func() error {
		callCount++
		if callCount <= failuresBeforeSuccess {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if callCount != failuresBeforeSuccess+1 {
		t.Errorf("expected %d calls (K failures + 1 success), got %d", failuresBeforeSuccess+1, callCount)
	}
}

// TestDoWithResult_TransientRecovery proves DoWithResult recovers after K
// transient failures, returns the eventual value, and calls fn exactly K+1 times.
func TestDoWithResult_TransientRecovery(t *testing.T) {
	const failuresBeforeSuccess = 3
	r := &Retry{MaxAttempts: 5, Delay: 2 * time.Millisecond, BackoffStrategy: Fixed}
	callCount := 0
	val, err := DoWithResult(context.Background(), r, func() (string, error) {
		callCount++
		if callCount <= failuresBeforeSuccess {
			return "", errors.New("transient")
		}
		return "recovered", nil
	})
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if val != "recovered" {
		t.Errorf("expected recovered value, got %q", val)
	}
	if callCount != failuresBeforeSuccess+1 {
		t.Errorf("expected %d calls (K failures + 1 success), got %d", failuresBeforeSuccess+1, callCount)
	}
}

// --- Jitter upper-bound assertion ---

// TestJitter_UpperBound asserts that with jitter enabled the computed delay never
// exceeds base*factor^n * (1 + maxJitter). Per computeDelay, maxJitter is 25%, so
// every observed delay must lie in [base, base*1.25). Driven directly through
// computeDelay (jitter on, Fixed strategy) so the bound is proven deterministically
// without wall-clock flakiness, across many random draws.
func TestJitter_UpperBound(t *testing.T) {
	base := 80 * time.Millisecond
	r := &Retry{MaxAttempts: 3, Delay: base, Jitter: true, BackoffStrategy: Fixed}
	upper := base + base/4 // base * 1.25
	for i := 0; i < 1000; i++ {
		d := r.computeDelay(0)
		if d < base {
			t.Fatalf("jitter lowered delay below base: got %v, base %v", d, base)
		}
		if d >= upper {
			t.Fatalf("jitter exceeded upper bound: got %v, must be < %v (base*1.25)", d, upper)
		}
	}
}

// TestJitter_UpperBound_Exponential asserts the [d, d*1.25) jitter envelope holds
// for each step of an exponential schedule (post-cap), i.e.
// delay <= base*factor^n * (1+maxJitter) at every attempt.
func TestJitter_UpperBound_Exponential(t *testing.T) {
	base := 10 * time.Millisecond
	maxDelay := 1 * time.Second
	r := &Retry{MaxAttempts: 6, Delay: base, MaxDelay: maxDelay, Jitter: true, BackoffStrategy: Exponential}
	for attempt := 0; attempt < 6; attempt++ {
		// Reconstruct the deterministic pre-jitter delay (base * 2^attempt, capped).
		want := time.Duration(float64(base) * math.Pow(2, float64(attempt)))
		if want > maxDelay {
			want = maxDelay
		}
		upper := want + want/4
		for i := 0; i < 200; i++ {
			d := r.computeDelay(attempt)
			if d < want {
				t.Fatalf("attempt %d: delay %v below pre-jitter base %v", attempt, d, want)
			}
			if d >= upper {
				t.Fatalf("attempt %d: delay %v exceeded upper bound %v (base*1.25)", attempt, d, upper)
			}
		}
	}
}

// TestJitter_TinyDelay_NoNegativeOrPanic proves that a Delay of 1ns does not
// trigger a panic in rand.Int63n (which panics on n<=0). With Delay=1ns and
// Jitter=true, int64(delay)/4 == 0, which is exactly the panic boundary.
// The source guards against this with `if int64(delay)/4 > 0`. If that guard is
// removed, rand.Int63n(0) panics and this test fails.
func TestJitter_TinyDelay_NoNegativeOrPanic(t *testing.T) {
	r := &Retry{
		MaxAttempts:     2,
		Delay:           1, // 1 ns — int64(1)/4 == 0 → panic without guard
		Jitter:          true,
		BackoffStrategy: Fixed,
	}
	// Must not panic, and must return a non-negative delay.
	for i := 0; i < 100; i++ {
		d := r.computeDelay(0)
		if d < 0 {
			t.Fatalf("computeDelay returned negative duration %v for 1ns delay", d)
		}
	}
}

// TestComputeDelay_Table exercises the computeDelay function with jitter disabled
// (Jitter: false) so we get deterministic, exact results. The table covers:
//   - Fixed strategy: delay is always Delay regardless of attempt.
//   - Linear strategy: delay grows as Delay*(attempt+1).
//   - Exponential strategy: delay grows as Delay*2^attempt.
//   - MaxDelay clamp: once the computed delay exceeds MaxDelay it is clamped.
//   - default (unknown strategy): treated as Fixed.
//
// Mutation proof: changing any case branch or the cap condition makes at least
// one row fail.
func TestComputeDelay_Table(t *testing.T) {
	type row struct {
		name      string
		r         *Retry
		attempt   int
		wantExact time.Duration
	}
	base := 10 * time.Millisecond
	max := 50 * time.Millisecond

	rows := []row{
		// Fixed: always base, regardless of attempt.
		{"Fixed/attempt0", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: Fixed}, 0, base},
		{"Fixed/attempt5", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: Fixed}, 5, base},

		// Linear: base*(attempt+1).
		{"Linear/attempt0", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: Linear}, 0, base * 1}, // 10ms
		{"Linear/attempt1", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: Linear}, 1, base * 2}, // 20ms
		{"Linear/attempt2", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: Linear}, 2, base * 3}, // 30ms
		{"Linear/attempt4", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: Linear}, 4, base * 5}, // 50ms clamped
		// Linear/attempt4: base*(4+1)=50ms == max → clamped to max.
		{"Linear/attempt5", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: Linear}, 5, max}, // 60ms clamped to 50ms

		// Exponential: base*2^attempt.
		{"Exponential/attempt0", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: Exponential}, 0, base},                  // 10ms
		{"Exponential/attempt1", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: Exponential}, 1, 20 * time.Millisecond}, // 20ms
		{"Exponential/attempt2", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: Exponential}, 2, 40 * time.Millisecond}, // 40ms
		{"Exponential/attempt3", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: Exponential}, 3, max},                   // 80ms clamped to 50ms

		// Default/unknown strategy: treated as Fixed.
		{"Default/attempt0", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: BackoffStrategy(99)}, 0, base},
		{"Default/attempt3", &Retry{Delay: base, MaxDelay: max, Jitter: false, BackoffStrategy: BackoffStrategy(99)}, 3, base},

		// MaxDelay=0 means no cap: exponential should not be clamped.
		{"Exponential/NoCap", &Retry{Delay: base, MaxDelay: 0, Jitter: false, BackoffStrategy: Exponential}, 3, 80 * time.Millisecond},
	}

	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.r.computeDelay(tc.attempt)
			if got != tc.wantExact {
				t.Errorf("computeDelay(attempt=%d): got %v, want %v", tc.attempt, got, tc.wantExact)
			}
		})
	}
}
