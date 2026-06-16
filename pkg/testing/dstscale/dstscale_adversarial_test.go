package dstscale

import (
	"encoding/json"
	"testing"
	"time"
)

// dstscale_adversarial_test.go — adversarial probes for the DST SCALE harness.
//
// dstscale.RunScale claims (scale.go doc): the schedule is "fully reproducible",
// VirtualElapsed is "derived from the seeded schedule (reproducible across
// identical seeds)", and the engine uses "a virtual clock with no real I/O or
// sleep". The highest-value bug for such a harness is a DETERMINISM BLUFF: it
// claims same-seed reproducibility but leaks nondeterminism (map iteration,
// wall clock, goroutine scheduling, unseeded RNG). Any such leak makes every
// scale assertion a PASS-bluff.
//
// We isolate the REPRODUCIBLE subset of ScaleResult. Per scale.go field
// contracts, WallElapsed / EventsPerSec / AvgStepNanos depend on real wall time,
// and AllocBytes / BytesPerNode depend on runtime heap accounting (GC), so they
// are intentionally non-reproducible and excluded. The seed-derived, virtual
// portion that MUST be byte-identical for a fixed seed is:
//
//	Nodes, Events, VirtualElapsed, ProcessedRatio
//
// Each probe that holds on current code is a CONTRACT PIN.

// reproducibleSubset serializes only the seed-deterministic fields of a
// ScaleResult, excluding the documented wall-clock / heap-accounting fields.
func reproducibleSubset(r ScaleResult) string {
	payload := struct {
		Nodes          int
		Events         int
		VirtualElapsed int64
		ProcessedRatio float64
	}{r.Nodes, r.Events, r.VirtualElapsed.Nanoseconds(), r.ProcessedRatio}
	b, _ := json.Marshal(payload)
	return string(b)
}

// ---------------------------------------------------------------------------
// HEADLINE (CONTRACT): same seed -> byte-identical reproducible result across
// MANY runs. Two runs (as the existing TestScale_Determinism does) can hide a
// low-probability map/scheduling leak; 64 runs makes a leak overwhelmingly
// likely to surface.
// ---------------------------------------------------------------------------

func TestAdversarial_SameSeedByteIdenticalAcrossManyRuns(t *testing.T) {
	const (
		seed   = int64(0xC0FFEE)
		nodes  = 250
		events = 3000
		runs   = 64
	)
	first := reproducibleSubset(RunScale(seed, nodes, events))
	for i := 1; i < runs; i++ {
		got := reproducibleSubset(RunScale(seed, nodes, events))
		if got != first {
			t.Fatalf("RunScale not reproducible for fixed seed across runs (determinism bluff):\n run0 =%s\n run%d=%s",
				first, i, got)
		}
	}
	t.Logf("reproducible subset stable across %d runs: %s", runs, first)
}

// ---------------------------------------------------------------------------
// CONTRACT: different seeds -> different VirtualElapsed (the seed actually
// drives the schedule). If these collide, the schedule is NOT seed-derived.
// We try several seed pairs so the assertion has real bite (any single pair
// could in principle collide; all of them colliding would prove a bug).
// ---------------------------------------------------------------------------

func TestAdversarial_DifferentSeedsProduceDifferentSchedules(t *testing.T) {
	const nodes, events = 100, 1000
	base := RunScale(1, nodes, events).VirtualElapsed
	differing := 0
	for s := int64(2); s <= 9; s++ {
		if RunScale(s, nodes, events).VirtualElapsed != base {
			differing++
		}
	}
	if differing == 0 {
		t.Fatalf("seeds 2..9 all produced VirtualElapsed == seed-1's (%v) — schedule is not seed-derived", base)
	}
	t.Logf("%d/8 alternate seeds produced a different VirtualElapsed than seed 1 (schedule is seed-derived)", differing)
}

// ---------------------------------------------------------------------------
// CONTRACT: virtual clock, not wall clock. RunScale measures VirtualElapsed
// from the engine's virtual timestamps. It must be independent of how much real
// time elapses. We sleep a real, measurable interval between two identical-seed
// runs; VirtualElapsed must be byte-identical regardless of wall time, and it
// must be far smaller than the wall time we burned (proving it is virtual).
// ---------------------------------------------------------------------------

func TestAdversarial_VirtualElapsedIsVirtualNotWall(t *testing.T) {
	const seed, nodes, events = int64(77), 50, 2000
	r1 := RunScale(seed, nodes, events)
	time.Sleep(25 * time.Millisecond) // burn real wall time between runs
	r2 := RunScale(seed, nodes, events)

	if r1.VirtualElapsed != r2.VirtualElapsed {
		t.Fatalf("VirtualElapsed changed across a wall-clock sleep (wall leaked into virtual): %v vs %v",
			r1.VirtualElapsed, r2.VirtualElapsed)
	}
	// All delays are drawn from [1000, 1_000_000) ns, so the virtual span is
	// strictly under 1 ms regardless of how long the run actually took on the
	// wall. A 25ms wall sleep dwarfs any legitimate virtual span.
	if r1.VirtualElapsed >= time.Millisecond {
		t.Fatalf("VirtualElapsed %v >= 1ms — exceeds the delay window, clock may not be virtual", r1.VirtualElapsed)
	}
}

// ---------------------------------------------------------------------------
// CORRECTNESS: VirtualElapsed must stay within the seeded delay window.
// Delays are rng.Int63n(999_000)+1_000, i.e. in [1_000, 999_999]. The dispatched
// span (max timestamp - min timestamp) therefore lies in [0, 998_999] ns. A
// value outside this band means VirtualElapsed (first/last timestamp tracking)
// is computed wrong. We probe a spread of seeds.
// ---------------------------------------------------------------------------

func TestAdversarial_VirtualElapsedWithinDelayWindow(t *testing.T) {
	const nodes, events = 100, 5000
	const maxSpan = 998_999 * time.Nanosecond // (999_999 - 1_000)
	for s := int64(1); s <= 20; s++ {
		got := RunScale(s, nodes, events).VirtualElapsed
		if got < 0 || got > maxSpan {
			t.Fatalf("seed %d: VirtualElapsed %v outside valid span [0, %v]", s, got, maxSpan)
		}
	}
}

// ---------------------------------------------------------------------------
// CORRECTNESS edge cases: zero/one/two events and the ProcessedRatio + single
// vs multi-event VirtualElapsed branches in RunScale.
// ---------------------------------------------------------------------------

func TestAdversarial_EdgeCaseEventCounts(t *testing.T) {
	t.Run("zero_events_no_virtual_span", func(t *testing.T) {
		r := RunScale(5, 10, 0)
		if r.Events != 0 || r.ProcessedRatio != 0 || r.VirtualElapsed != 0 {
			t.Fatalf("zero events: want Events=0 Ratio=0 Virtual=0, got %+v", r)
		}
	})
	t.Run("one_event_virtual_is_timestamp", func(t *testing.T) {
		// Single-event branch: VirtualElapsed == timestamp of that one event,
		// which is its delay in [1000, 999_999].
		r := RunScale(5, 1, 1)
		if r.Events != 1 || r.ProcessedRatio != 1 {
			t.Fatalf("one event: want Events=1 Ratio=1, got %+v", r)
		}
		if r.VirtualElapsed < 1000*time.Nanosecond || r.VirtualElapsed > 999_999*time.Nanosecond {
			t.Fatalf("one event: VirtualElapsed %v outside [1000ns, 999999ns]", r.VirtualElapsed)
		}
	})
	t.Run("two_events_span_nonnegative", func(t *testing.T) {
		// Multi-event branch: span = last - first >= 0, never negative.
		r := RunScale(5, 1, 2)
		if r.Events != 2 || r.ProcessedRatio != 1 {
			t.Fatalf("two events: want Events=2 Ratio=1, got %+v", r)
		}
		if r.VirtualElapsed < 0 {
			t.Fatalf("two events: VirtualElapsed negative: %v", r.VirtualElapsed)
		}
	})
}

// ---------------------------------------------------------------------------
// ROBUSTNESS: degenerate node/event counts must not panic and must degrade
// gracefully (no negative spans, ratio in [0,1]). Negative nodes -> 0 nodes;
// negative events -> 0 scheduled/processed.
// ---------------------------------------------------------------------------

func TestAdversarial_DegenerateCountsDoNotPanic(t *testing.T) {
	cases := []struct{ nodes, events int }{
		{0, 0}, {0, 10}, {10, 0}, {-2, 5}, {5, -3}, {-1, -1},
	}
	for _, c := range cases {
		r := RunScale(1, c.nodes, c.events)
		if r.Nodes < 0 {
			t.Fatalf("nodes=%d events=%d: negative Nodes %d", c.nodes, c.events, r.Nodes)
		}
		if r.Events < 0 {
			t.Fatalf("nodes=%d events=%d: negative Events %d", c.nodes, c.events, r.Events)
		}
		if r.VirtualElapsed < 0 {
			t.Fatalf("nodes=%d events=%d: negative VirtualElapsed %v", c.nodes, c.events, r.VirtualElapsed)
		}
		if r.ProcessedRatio < 0 || r.ProcessedRatio > 1 {
			t.Fatalf("nodes=%d events=%d: ProcessedRatio out of [0,1]: %.6f", c.nodes, c.events, r.ProcessedRatio)
		}
	}
}

// ---------------------------------------------------------------------------
// CONTRACT: all scheduled events are processed when maxEvents == scheduled.
// ProcessedRatio must be exactly 1.0 across a spread of sizes (off-by-one in the
// Run cap would surface as ratio != 1).
// ---------------------------------------------------------------------------

func TestAdversarial_AllScheduledEventsProcessed(t *testing.T) {
	for _, n := range []int{1, 2, 7, 64, 1000} {
		r := RunScale(11, 16, n)
		if r.Events != n {
			t.Fatalf("events=%d: processed %d (off-by-one in Run cap?)", n, r.Events)
		}
		if r.ProcessedRatio != 1.0 {
			t.Fatalf("events=%d: ProcessedRatio %.6f, want 1.0", n, r.ProcessedRatio)
		}
	}
}
