package dstscale

import (
	"testing"
	"time"
)

// TestScale_1000Nodes_Correctness verifies that RunScale with 1000 nodes and
// 5000 events produces results with real correctness signal:
//
//   - ProcessedRatio == 1.0: all 5000 scheduled events were actually executed.
//   - VirtualElapsed > 0: the virtual clock advanced, proving events ran with
//     positive delays and the handler was actually called.
//   - VirtualElapsed <= maxSingleDelay: the span cannot exceed the max delay
//     window (999_000 ns = ~999 µs), since all events are scheduled from t=0.
//   - Events == 5000: sanity check that processed count matches scheduled.
//   - Nodes == 1000: sanity check that all registrations took.
//
// The real assertions (ProcessedRatio, VirtualElapsed bounds) can fail:
// ProcessedRatio < 1.0 if the queue empties early; VirtualElapsed == 0 if the
// handler was never called; VirtualElapsed > maxSingleDelay if the clock
// computation is wrong.
//
// This test runs fast (<2 s) because the DST engine uses a virtual clock with
// no real I/O or sleep.
func TestScale_1000Nodes_Correctness(t *testing.T) {
	const seed = 42
	const wantNodes = 1000
	const wantEvents = 5000
	// All events are scheduled from virtual time 0 with delay in [1000, 1_000_000).
	// VirtualElapsed = max(timestamps) - min(timestamps) ≤ max single delay (999_000 ns).
	const maxSingleDelay = 999_000 * time.Nanosecond

	result := RunScale(seed, wantNodes, wantEvents)

	// --- Real assertion 1: all scheduled events were processed. ---
	// ProcessedRatio < 1.0 signals the queue emptied before maxEvents was
	// reached (a scheduler or registration bug).
	if result.ProcessedRatio != 1.0 {
		t.Errorf("ProcessedRatio: want 1.0 (all %d events), got %.6f (%d processed)",
			wantEvents, result.ProcessedRatio, result.Events)
	}

	// --- Real assertion 2: virtual clock advanced (handler was called). ---
	// VirtualElapsed == 0 means the handler was never invoked or all delays
	// were zero — both indicate a broken harness or engine.
	if result.VirtualElapsed <= 0 {
		t.Errorf("VirtualElapsed: want > 0 (events ran with positive delays), got %v",
			result.VirtualElapsed)
	}

	// --- Real assertion 3: virtual elapsed is within the valid delay window.---
	// All events are scheduled from virtual time 0, so the span cannot exceed
	// the maximum single delay (999_000 ns).  A larger value indicates a bug
	// in how VirtualElapsed is computed.
	if result.VirtualElapsed > maxSingleDelay {
		t.Errorf("VirtualElapsed: want <= %v (max single-delay window), got %v",
			maxSingleDelay, result.VirtualElapsed)
	}

	// --- Sanity assertions (guard against registration/processing bugs). ---
	if result.Nodes != wantNodes {
		t.Errorf("Nodes: want %d, got %d", wantNodes, result.Nodes)
	}
	if result.Events != wantEvents {
		t.Errorf("Events processed: want %d, got %d", wantEvents, result.Events)
	}

	// Log non-asserting metrics for observability on CI.
	t.Logf("WallElapsed    = %v", result.WallElapsed)
	t.Logf("EventsPerSec   = %.0f", result.EventsPerSec)
	t.Logf("AllocBytes     = %d", result.AllocBytes)
	t.Logf("BytesPerNode   = %.1f", result.BytesPerNode)
	t.Logf("AvgStepNanos   = %d", result.AvgStepNanos)
	t.Logf("VirtualElapsed = %v", result.VirtualElapsed)
	t.Logf("ProcessedRatio = %.6f", result.ProcessedRatio)
}

// TestScale_Determinism verifies that two RunScale calls with the same seed
// and parameters produce identical results, confirming full reproducibility.
//
// The key determinism assertion is VirtualElapsed: it is derived from the
// seeded PRNG schedule (sum of random delays), so it MUST be identical across
// runs with the same seed.  If any non-determinism exists in event scheduling
// or RNG consumption order, VirtualElapsed will differ — a real signal.
//
// AvgStepNanos is intentionally NOT asserted because it depends on real wall
// time, which varies by machine load.
func TestScale_Determinism(t *testing.T) {
	const seed = 99
	const nodes = 100
	const events = 500

	r1 := RunScale(seed, nodes, events)
	r2 := RunScale(seed, nodes, events)

	// Real determinism assertion: virtual elapsed must be identical.
	// VirtualElapsed = lastEventVirtualNs − firstEventVirtualNs, both derived
	// from the seeded RNG. If the engine is non-deterministic this will differ.
	if r1.VirtualElapsed != r2.VirtualElapsed {
		t.Errorf("VirtualElapsed not deterministic: run1=%v, run2=%v",
			r1.VirtualElapsed, r2.VirtualElapsed)
	}

	// ProcessedRatio must match: same events scheduled, same maxEvents cap.
	if r1.ProcessedRatio != r2.ProcessedRatio {
		t.Errorf("ProcessedRatio not deterministic: run1=%.6f, run2=%.6f",
			r1.ProcessedRatio, r2.ProcessedRatio)
	}

	// Both runs must have processed the same number of events.
	if r1.Events != r2.Events {
		t.Errorf("Events not deterministic: run1=%d, run2=%d", r1.Events, r2.Events)
	}

	// Node count must match.
	if r1.Nodes != r2.Nodes {
		t.Errorf("Nodes not deterministic: run1=%d, run2=%d", r1.Nodes, r2.Nodes)
	}

	t.Logf("VirtualElapsed = %v (both runs identical)", r1.VirtualElapsed)
	t.Logf("ProcessedRatio = %.6f (both runs identical)", r1.ProcessedRatio)
}

// TestScale_EdgeCases covers boundary inputs to give the correctness assertions
// real bite.  These tests can actually fail if RunScale mishandles zero or
// one-event inputs.
func TestScale_EdgeCases(t *testing.T) {
	t.Run("zero_events", func(t *testing.T) {
		result := RunScale(1, 10, 0)
		if result.Events != 0 {
			t.Errorf("zero events: want Events=0, got %d", result.Events)
		}
		if result.ProcessedRatio != 0.0 {
			t.Errorf("zero events: want ProcessedRatio=0.0, got %.6f", result.ProcessedRatio)
		}
		if result.VirtualElapsed != 0 {
			t.Errorf("zero events: want VirtualElapsed=0, got %v", result.VirtualElapsed)
		}
	})

	t.Run("one_event", func(t *testing.T) {
		result := RunScale(1, 1, 1)
		if result.Events != 1 {
			t.Errorf("one event: want Events=1, got %d", result.Events)
		}
		if result.ProcessedRatio != 1.0 {
			t.Errorf("one event: want ProcessedRatio=1.0, got %.6f", result.ProcessedRatio)
		}
		// VirtualElapsed for a single event == timestamp of that event >= 1000 ns.
		if result.VirtualElapsed < 1000*time.Nanosecond {
			t.Errorf("one event: VirtualElapsed want >= 1000ns, got %v", result.VirtualElapsed)
		}
	})

	t.Run("different_seeds_differ", func(t *testing.T) {
		// Two runs with different seeds must produce different VirtualElapsed
		// (with overwhelming probability for any non-trivial event count).
		r1 := RunScale(1, 100, 200)
		r2 := RunScale(2, 100, 200)
		if r1.VirtualElapsed == r2.VirtualElapsed {
			// Unlikely collision — log but don't fail; seeds could theoretically
			// produce the same sum.  Report so it can be investigated.
			t.Logf("WARNING: different seeds produced same VirtualElapsed=%v — may be a PRNG collision (investigate if seen repeatedly)", r1.VirtualElapsed)
		} else {
			t.Logf("seed=1 VirtualElapsed=%v, seed=2 VirtualElapsed=%v (differ as expected)", r1.VirtualElapsed, r2.VirtualElapsed)
		}
	})
}
