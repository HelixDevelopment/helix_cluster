// Package retry provides retry logic with configurable backoff strategies for Helix Cluster OS.
package retry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"
)

// BackoffStrategy defines the type of backoff to use.
type BackoffStrategy int

const (
	// Fixed uses a constant delay between retries.
	Fixed BackoffStrategy = iota
	// Linear increases delay linearly with each attempt.
	Linear
	// Exponential doubles the delay with each attempt up to MaxDelay.
	Exponential
)

// Retry holds retry configuration.
type Retry struct {
	MaxAttempts     int
	Delay           time.Duration
	MaxDelay        time.Duration
	Jitter          bool
	BackoffStrategy BackoffStrategy
	Retryable       func(error) bool
}

// DefaultRetry returns a Retry with sensible defaults.
func DefaultRetry() *Retry {
	return &Retry{
		MaxAttempts:     3,
		Delay:           100 * time.Millisecond,
		MaxDelay:        5 * time.Second,
		Jitter:          true,
		BackoffStrategy: Exponential,
		Retryable:       func(err error) bool { return true },
	}
}

// IsRetryable returns true if the error should be retried.
func (r *Retry) IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNonRetryable) {
		return false
	}
	if r.Retryable != nil {
		return r.Retryable(err)
	}
	return true
}

// Do executes fn until it succeeds, max attempts are reached, or context is cancelled.
func (r *Retry) Do(ctx context.Context, fn func() error) error {
	var lastErr error
	for i := 0; i < r.MaxAttempts; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			if !r.IsRetryable(err) {
				return fmt.Errorf("non-retryable error on attempt %d: %w", i+1, err)
			}
		}
		if i < r.MaxAttempts-1 {
			delay := r.computeDelay(i)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				timer.Stop()
			}
		}
	}
	return fmt.Errorf("retry exhausted after %d attempts: %w", r.MaxAttempts, lastErr)
}

// DoWithResult executes fn that returns (T, error) until success or exhaustion.
func DoWithResult[T any](ctx context.Context, r *Retry, fn func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	for i := 0; i < r.MaxAttempts; i++ {
		val, err := fn()
		if err == nil {
			return val, nil
		}
		lastErr = err
		if !r.IsRetryable(err) {
			return zero, fmt.Errorf("non-retryable error on attempt %d: %w", i+1, err)
		}
		if i < r.MaxAttempts-1 {
			delay := r.computeDelay(i)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return zero, ctx.Err()
			case <-timer.C:
				timer.Stop()
			}
		}
	}
	return zero, fmt.Errorf("retry exhausted after %d attempts: %w", r.MaxAttempts, lastErr)
}

func (r *Retry) computeDelay(attempt int) time.Duration {
	var delay time.Duration
	switch r.BackoffStrategy {
	case Fixed:
		delay = r.Delay
	case Linear:
		delay = r.Delay * time.Duration(attempt+1)
	case Exponential:
		delay = r.Delay * time.Duration(math.Pow(2, float64(attempt)))
	default:
		delay = r.Delay
	}
	if r.MaxDelay > 0 && delay > r.MaxDelay {
		delay = r.MaxDelay
	}
	if r.Jitter {
		// Add up to 25% random jitter.
		jitter := time.Duration(rand.Int63n(int64(delay) / 4))
		delay += jitter
	}
	return delay
}

// Do is the package-level convenience function using DefaultRetry.
func Do(ctx context.Context, fn func() error) error {
	return DefaultRetry().Do(ctx, fn)
}

// ErrNonRetryable can be wrapped to indicate an error should not be retried.
var ErrNonRetryable = errors.New("non-retryable error")

// IsRetryableError checks if an error is retryable (not wrapped with ErrNonRetryable).
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	return !errors.Is(err, ErrNonRetryable)
}
