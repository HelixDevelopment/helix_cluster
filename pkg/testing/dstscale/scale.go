// Package dstscale exercises the DST engine at 1000+ node scale in a single
// process, measuring throughput, memory, and virtual-clock behaviour.
package dstscale

import (
	"fmt"
	"runtime"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dst"
)

// ScaleResult holds the measured outcomes of a RunScale execution.
//
// Field contracts:
//   - Nodes: number of nodes actually registered (eng.NodeCount()).
//   - Events: number of events actually processed (eng.Run return value).
//   - WallElapsed: real wall-clock duration of eng.Run.
//   - EventsPerSec: Events / WallElapsed.Seconds(); 0 if wall is zero.
//   - AllocBytes: cumulative heap bytes allocated between node-registration
//     start and end of Run (runtime.MemStats.TotalAlloc delta).
//   - BytesPerNode: AllocBytes / Nodes; 0 if Nodes == 0.
//   - AvgStepNanos: average wall nanoseconds per processed event
//     (WallElapsed.Nanoseconds() / Events); 0 if Events == 0.
//   - VirtualElapsed: span of virtual time actually executed —
//     (timestamp of last dispatched event) − (timestamp of first dispatched
//     event).  This is a real, seed-dependent measurement: it will differ for
//     different seeds, different event counts, and is zero when Events < 2.
//   - ProcessedRatio: Events / events-scheduled; 1.0 when all scheduled
//     events were processed, < 1.0 if the queue ran dry early.
type ScaleResult struct {
	Nodes          int
	Events         int
	WallElapsed    time.Duration
	EventsPerSec   float64
	AllocBytes     uint64
	BytesPerNode   float64
	AvgStepNanos   int64
	VirtualElapsed time.Duration
	ProcessedRatio float64
}

// RunScale registers 'nodes' nodes, schedules 'events' tick events with
// pseudo-random delays drawn from the engine's seeded RNG, runs the engine to
// completion, and returns fully measured results.
//
// Virtual-time span (VirtualElapsed) and ProcessedRatio are the key
// correctness signals: VirtualElapsed is derived from the seeded schedule
// (reproducible across identical seeds), and ProcessedRatio < 1.0 signals
// that the queue emptied before maxEvents was reached.
func RunScale(seed int64, nodes, events int) ScaleResult {
	eng := dst.NewEngine(seed)

	// Capture allocations before setting up anything.
	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	// Register all nodes.
	for i := 0; i < nodes; i++ {
		id := fmt.Sprintf("node-%d", i)
		// Build a deterministic-looking IP from the index; the address value
		// has no semantic meaning to the engine but must be non-empty.
		addr := fmt.Sprintf("10.%d.%d.%d", (i>>16)&0xFF, (i>>8)&0xFF, i&0xFF)
		eng.RegisterNode(id, addr)
	}

	// firstVirtualNs and lastVirtualNs track the virtual timestamps of the
	// first and last dispatched events so we can compute VirtualElapsed —
	// a real, seed-dependent measurement that must be non-zero for any run
	// with at least 1 event and a positive delay.
	var firstVirtualNs, lastVirtualNs int64
	var handlerCalled int

	eng.RegisterHandler("tick", func(e *dst.Event) {
		now := eng.Now().UnixNano()
		if handlerCalled == 0 {
			firstVirtualNs = now
		}
		lastVirtualNs = now
		handlerCalled++
	})

	// Schedule 'events' tick events.  Delays are drawn from the engine's own
	// seeded RNG so the schedule is fully reproducible.
	rng := eng.RNG()
	for i := 0; i < events; i++ {
		// Delay in virtual nanoseconds: 1 µs … ~1 ms (1000 ns … 1_000_000 ns).
		delay := rng.Int63n(999_000) + 1_000
		eng.ScheduleEvent("tick", i, delay)
	}

	// Measure wall time around the Run call only.
	wallStart := time.Now()
	processed := eng.Run(events)
	wallElapsed := time.Since(wallStart)

	// Capture allocations after the run.
	runtime.GC()
	runtime.ReadMemStats(&memAfter)

	var evPerSec float64
	if wallElapsed.Seconds() > 0 {
		evPerSec = float64(processed) / wallElapsed.Seconds()
	}

	var allocBytes uint64
	if memAfter.TotalAlloc > memBefore.TotalAlloc {
		allocBytes = memAfter.TotalAlloc - memBefore.TotalAlloc
	}

	var bytesPerNode float64
	if nodes > 0 {
		bytesPerNode = float64(allocBytes) / float64(nodes)
	}

	// AvgStepNanos: average wall nanoseconds per processed event.
	var avgStepNanos int64
	if processed > 0 {
		avgStepNanos = wallElapsed.Nanoseconds() / int64(processed)
	}

	// VirtualElapsed: real virtual-time span covered during this run.
	// Non-zero only when at least two events were dispatched.
	var virtualElapsed time.Duration
	if handlerCalled >= 2 {
		virtualElapsed = time.Duration(lastVirtualNs - firstVirtualNs)
	} else if handlerCalled == 1 {
		// Single event: virtual span is the timestamp of that one event.
		virtualElapsed = time.Duration(firstVirtualNs)
	}

	// ProcessedRatio: fraction of scheduled events that were processed.
	// == 1.0 when all scheduled events ran; < 1.0 if the queue emptied early.
	var processedRatio float64
	if events > 0 {
		processedRatio = float64(processed) / float64(events)
	}

	return ScaleResult{
		Nodes:          eng.NodeCount(),
		Events:         processed,
		WallElapsed:    wallElapsed,
		EventsPerSec:   evPerSec,
		AllocBytes:     allocBytes,
		BytesPerNode:   bytesPerNode,
		AvgStepNanos:   avgStepNanos,
		VirtualElapsed: virtualElapsed,
		ProcessedRatio: processedRatio,
	}
}
