// Package retry provides retry logic for Helix Cluster OS.
package retry

import (
	"context"
	"fmt"
	"time"
)

// Strategy defines a retry strategy.
type Strategy struct {
	MaxAttempts int
	Delay       time.Duration
}

// Do executes fn until it succeeds or max attempts are reached.
func Do(ctx context.Context, s Strategy, fn func() error) error {
	var lastErr error
	for i := 0; i < s.MaxAttempts; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.Delay):
		}
	}
	return fmt.Errorf("retry exhausted after %d attempts: %w", s.MaxAttempts, lastErr)
}
