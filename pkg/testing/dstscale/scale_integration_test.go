//go:build integration

package dstscale

import (
	"testing"
	"time"
)

// TestScale_1000Nodes_100kEvents_Perf measures DST engine throughput at
// 1000 nodes / 100 000 events and asserts a conservative floor of 10 000
// events/s.  Wall time, memory, and per-step latency are logged so that
// regressions in future waves are visible in CI artefacts.
//
// The 10 000 ev/s floor is deliberately conservative: the in-process virtual
// clock engine trivially exceeds this on any hardware; lowering the bar would
// turn this into a pass-bluff.  Raise it once a stable baseline is established.
//
// ProcessedRatio and VirtualElapsed are also asserted here for end-to-end
// correctness at the larger event count.
func TestScale_1000Nodes_100kEvents_Perf(t *testing.T) {
	const seed = 12345
	const nodes = 1000
	const events = 100_000
	const minEventsPerSec = 10_000.0
	// All events are scheduled from virtual time 0 with delay in [1000, 1_000_000).
	// VirtualElapsed = max(timestamps) − min(timestamps).
	// For 100_000 events the expected span is nearly the full 999_000 ns window;
	// assert > 0 (events ran) and ≤ max single delay (999_000 ns).
	const maxSingleDelay = 999_000 * time.Nanosecond

	result := RunScale(seed, nodes, events)

	if result.Nodes != nodes {
		t.Errorf("Nodes: want %d, got %d", nodes, result.Nodes)
	}
	if result.Events != events {
		t.Errorf("Events processed: want %d, got %d", events, result.Events)
	}
	if result.ProcessedRatio != 1.0 {
		t.Errorf("ProcessedRatio: want 1.0, got %.6f (%d/%d processed)",
			result.ProcessedRatio, result.Events, events)
	}
	if result.VirtualElapsed <= 0 {
		t.Errorf("VirtualElapsed: want > 0 (events ran with positive delays), got %v",
			result.VirtualElapsed)
	}
	if result.VirtualElapsed > maxSingleDelay {
		t.Errorf("VirtualElapsed: want <= %v (max single-delay window), got %v",
			maxSingleDelay, result.VirtualElapsed)
	}
	if result.EventsPerSec < minEventsPerSec {
		t.Errorf("EventsPerSec too low: want >= %.0f, got %.0f (wall=%v)",
			minEventsPerSec, result.EventsPerSec, result.WallElapsed)
	}

	// Log all measured metrics as evidence (non-asserting diagnostics).
	t.Logf("Nodes          = %d", result.Nodes)
	t.Logf("Events         = %d", result.Events)
	t.Logf("WallElapsed    = %v", result.WallElapsed)
	t.Logf("EventsPerSec   = %.0f  (floor=%.0f)", result.EventsPerSec, minEventsPerSec)
	t.Logf("AllocBytes     = %d", result.AllocBytes)
	t.Logf("BytesPerNode   = %.1f", result.BytesPerNode)
	t.Logf("AvgStepNanos   = %d ns/event", result.AvgStepNanos)
	t.Logf("VirtualElapsed = %v  (maxWindow=%v)", result.VirtualElapsed, maxSingleDelay)
	t.Logf("ProcessedRatio = %.6f", result.ProcessedRatio)
}
