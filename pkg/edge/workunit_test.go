package edge

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestExecutor_OverrunIsTerminated verifies that a work function that sleeps
// past MaxDurationSeconds is terminated at the limit and the result reports
// LimitHit=LimitDuration with a non-empty TerminationLog and a RunUUID.
//
// MUTATION target: remove the context.WithTimeout call (skip the kill) and the
// over-runner will complete after ~200ms → test FAILS because LimitHit is empty.
func TestExecutor_OverrunIsTerminated(t *testing.T) {
	if testing.Short() {
		// Allow -short to skip the 1-second sleep.
		t.Skip("skipping over-run test in -short mode (use regular run to exercise duration enforcement)")
	}

	exec := NewExecutor()
	unit := EdgeWorkUnit{
		ID:                 "test-overrun-unit",
		MaxDurationSeconds: 1, // hard limit: 1 second
	}

	// work sleeps for 200ms past the deadline.
	work := func(ctx context.Context) ([]byte, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1200 * time.Millisecond): // 200ms past limit
			return []byte("should-not-reach"), nil
		}
	}

	res, err := exec.Run(context.Background(), unit, work)

	// Must return ErrLimitExceeded.
	if !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("expected ErrLimitExceeded, got %v", err)
	}
	// Sink-side: LimitHit must be duration.
	if res.LimitHit != LimitDuration {
		t.Errorf("res.LimitHit = %q, want %q", res.LimitHit, LimitDuration)
	}
	// Must have a non-empty termination log.
	if res.TerminationLog == "" {
		t.Error("res.TerminationLog is empty, want a termination explanation")
	}
	// RunUUID must be populated.
	if res.RunUUID == "" {
		t.Error("res.RunUUID is empty, want a non-empty per-run UUID")
	}
	// UnitID must match.
	if res.UnitID != unit.ID {
		t.Errorf("res.UnitID = %q, want %q", res.UnitID, unit.ID)
	}
	// Output must be nil on termination.
	if res.Output != nil {
		t.Errorf("res.Output = %q, want nil on limit-exceeded", res.Output)
	}
	// IsLimitExceeded() helper must agree.
	if !res.IsLimitExceeded() {
		t.Error("res.IsLimitExceeded() = false, want true")
	}
}

// TestExecutor_WithinBudgetReturnsOutput verifies that a work function that
// completes before the deadline returns its output with no limit hit.
//
// MUTATION target: making LimitHit unconditionally non-empty would fail this test.
func TestExecutor_WithinBudgetReturnsOutput(t *testing.T) {
	exec := NewExecutor()
	unit := EdgeWorkUnit{
		ID:                 "test-within-budget",
		MaxDurationSeconds: 5, // generous limit
	}

	want := []byte("hello-from-work")
	work := func(ctx context.Context) ([]byte, error) {
		return want, nil
	}

	res, err := exec.Run(context.Background(), unit, work)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if res.LimitHit != "" {
		t.Errorf("res.LimitHit = %q, want empty (no limit exceeded)", res.LimitHit)
	}
	if string(res.Output) != string(want) {
		t.Errorf("res.Output = %q, want %q", res.Output, want)
	}
	if res.RunUUID == "" {
		t.Error("res.RunUUID is empty, want a non-empty per-run UUID")
	}
	if res.UnitID != unit.ID {
		t.Errorf("res.UnitID = %q, want %q", res.UnitID, unit.ID)
	}
	if res.IsLimitExceeded() {
		t.Error("res.IsLimitExceeded() = true, want false")
	}
}

// TestExecutor_NoLimitCompletes verifies that a work unit with
// MaxDurationSeconds=0 (no limit) completes normally.
func TestExecutor_NoLimitCompletes(t *testing.T) {
	exec := NewExecutor()
	unit := EdgeWorkUnit{
		ID:                 "no-limit-unit",
		MaxDurationSeconds: 0,
	}

	want := []byte("no-limit-output")
	work := func(ctx context.Context) ([]byte, error) {
		return want, nil
	}

	res, err := exec.Run(context.Background(), unit, work)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if string(res.Output) != string(want) {
		t.Errorf("res.Output = %q, want %q", res.Output, want)
	}
	if res.LimitHit != "" {
		t.Errorf("res.LimitHit = %q, want empty", res.LimitHit)
	}
}

// TestExecutor_RunUUIDsAreDistinct verifies that two successive Run calls
// produce different RunUUIDs.
func TestExecutor_RunUUIDsAreDistinct(t *testing.T) {
	exec := NewExecutor()
	unit := EdgeWorkUnit{ID: "uuid-test", MaxDurationSeconds: 5}
	noop := func(ctx context.Context) ([]byte, error) { return nil, nil }

	r1, _ := exec.Run(context.Background(), unit, noop)
	r2, _ := exec.Run(context.Background(), unit, noop)

	if r1.RunUUID == r2.RunUUID {
		t.Errorf("RunUUIDs are identical (%q), want distinct per-run values", r1.RunUUID)
	}
}

// TestErrLimitExceededSentinel verifies that errors.Is works through wrapping.
func TestErrLimitExceededSentinel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}

	exec := NewExecutor()
	unit := EdgeWorkUnit{ID: "sentinel-test", MaxDurationSeconds: 1}
	work := func(ctx context.Context) ([]byte, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
			return nil, nil
		}
	}

	_, err := exec.Run(context.Background(), unit, work)
	if !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("errors.Is(err, ErrLimitExceeded) = false; err = %v", err)
	}
}
