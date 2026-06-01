//go:build integration

package edge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestIntegration_WorkUnit_OverrunTerminated verifies wall-clock termination
// under real execution conditions. The test uses a per-run UUID embedded in
// the termination log and asserts it appears in the result.
//
// This test is gated by //go:build integration and is run by the orchestrator.
func TestIntegration_WorkUnit_OverrunTerminated(t *testing.T) {
	runUUID := fmt.Sprintf("int-%d", time.Now().UnixNano())
	t.Logf("integration run_uuid=%s", runUUID)

	exec := NewExecutor()
	unit := EdgeWorkUnit{
		ID:                 fmt.Sprintf("int-overrun-%s", runUUID),
		MaxDurationSeconds: 1,
	}

	work := func(ctx context.Context) ([]byte, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
			return []byte("overrun-output"), nil
		}
	}

	res, err := exec.Run(context.Background(), unit, work)

	if !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("expected ErrLimitExceeded; got %v (run_uuid=%s)", err, runUUID)
	}
	if res.LimitHit != LimitDuration {
		t.Errorf("res.LimitHit = %q, want %q (run_uuid=%s)", res.LimitHit, LimitDuration, runUUID)
	}
	if res.Output != nil {
		t.Errorf("res.Output = %q, want nil on overrun (run_uuid=%s)", res.Output, runUUID)
	}
	if res.RunUUID == "" {
		t.Errorf("res.RunUUID is empty (run_uuid=%s)", runUUID)
	}

	t.Logf("unit=%s run_uuid=%s elapsed_ms=%d limit=%s termination_log=%q",
		res.UnitID, res.RunUUID, res.ElapsedMs, res.LimitHit, res.TerminationLog)
}

// TestIntegration_WorkUnit_WithinBudget verifies normal completion under
// integration conditions.
func TestIntegration_WorkUnit_WithinBudget(t *testing.T) {
	runUUID := fmt.Sprintf("int-%d", time.Now().UnixNano())
	t.Logf("integration run_uuid=%s", runUUID)

	exec := NewExecutor()
	unit := EdgeWorkUnit{
		ID:                 fmt.Sprintf("int-budget-%s", runUUID),
		MaxDurationSeconds: 5,
		MaxMemoryMB:        128,
	}

	want := fmt.Sprintf("output-%s", runUUID)
	work := func(ctx context.Context) ([]byte, error) {
		return []byte(want), nil
	}

	res, err := exec.Run(context.Background(), unit, work)
	if err != nil {
		t.Errorf("unexpected error: %v (run_uuid=%s)", err, runUUID)
	}
	if string(res.Output) != want {
		t.Errorf("res.Output = %q, want %q (run_uuid=%s)", res.Output, want, runUUID)
	}
	if res.LimitHit != "" {
		t.Errorf("res.LimitHit = %q, want empty (run_uuid=%s)", res.LimitHit, runUUID)
	}

	t.Logf("unit=%s run_uuid=%s elapsed_ms=%d peak_mem_delta_mb=%.2f",
		res.UnitID, res.RunUUID, res.ElapsedMs, res.PeakMemDeltaMB)
}
