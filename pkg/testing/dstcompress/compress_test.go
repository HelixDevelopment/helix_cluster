package dstcompress

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dst"
)

// TestCompressionRatio_24hHorizon schedules 24 no-op events spaced one virtual
// hour apart across a 24-hour virtual horizon, then asserts that the measured
// compression ratio is at least 10:1.
//
// Anti-bluff contract:
//   - WallElapsed is measured via real time.Now()/time.Since inside Measure —
//     never hardcoded.
//   - VirtualElapsed is derived from eng.Now() before and after Run — the
//     assertion would fail if Run were a no-op or if the clock did not advance.
//   - Ratio >= 10 would fail if the engine actually slept for real time (e.g. a
//     1 µs sleep per event over 24 h of virtual time would still give ratio > 1e9).
func TestCompressionRatio_24hHorizon(t *testing.T) {
	eng := dst.NewEngine(2024)

	const hours = 24
	const virtualHour = int64(time.Hour) // nanoseconds

	// Register a no-op handler so the engine has something to dispatch.
	eng.RegisterHandler("noop", func(e *dst.Event) {})

	// Schedule one event per virtual hour.
	for h := 1; h <= hours; h++ {
		eng.ScheduleEvent("noop", nil, int64(h)*virtualHour)
	}

	m := Measure(eng, hours*2) // give generous headroom

	t.Logf("24-hour horizon: %s", m)

	// VirtualElapsed must be approximately 24 virtual hours.
	// (The engine advances to the last event's timestamp = 24 h.)
	expectedVirtual := time.Duration(hours) * time.Hour
	if m.VirtualElapsed < expectedVirtual-time.Minute {
		t.Errorf("VirtualElapsed = %s, want ~%s", m.VirtualElapsed, expectedVirtual)
	}

	// All 24 events must have been processed.
	if m.EventsProcessed != hours {
		t.Errorf("EventsProcessed = %d, want %d", m.EventsProcessed, hours)
	}

	// Wall elapsed must be a measured (non-zero) positive duration — the
	// floorNs(1) guard ensures Ratio is finite even if the machine is
	// extremely fast, but WallElapsed itself should reflect real CPU time.
	// Use <= 0 so that a hardcoded zero also fails this check.
	if m.WallElapsed <= 0 {
		t.Errorf("WallElapsed is zero or negative: %s", m.WallElapsed)
	}

	// Core assertion: compression ratio must be at least 10:1.
	// In practice it will be millions-to-one because there is no real sleep.
	if m.Ratio < 10 {
		t.Errorf("Ratio = %.2f, want >= 10 (got %s)", m.Ratio, m)
	}
}

// TestCompressionRatio_1000Nodes schedules sparse events over a long horizon
// with 1000 registered nodes and asserts Ratio >= 8.
//
// Rationale for the looser bound (8 vs 10): with 1000 nodes the handler may
// inspect node state, adding CPU overhead. Even so, virtual time spans hours
// while wall time is microseconds — a ratio of 8 is extremely conservative.
func TestCompressionRatio_1000Nodes(t *testing.T) {
	const nodeCount = 1000
	const eventCount = 50
	const virtualSpan = 6 * int64(time.Hour) // 6 virtual hours

	eng := dst.NewEngine(9999)

	// Register 1000 nodes.
	for i := 0; i < nodeCount; i++ {
		eng.RegisterNode(
			fmt.Sprintf("node-%04d", i),
			fmt.Sprintf("10.%d.%d.%d:7946", (i>>8)&0xff, (i>>4)&0x0f, i&0x0f),
		)
	}

	// Handler that reads node count — light but non-trivial work.
	eng.RegisterHandler("probe", func(e *dst.Event) {
		_ = eng.NodeCount() // real work: acquires read lock, counts nodes
	})

	// Schedule sparse events evenly across 6 virtual hours.
	step := virtualSpan / eventCount
	for i := 1; i <= eventCount; i++ {
		eng.ScheduleEvent("probe", nil, int64(i)*step)
	}

	m := Measure(eng, eventCount*2)

	t.Logf("1000-node scale: %s", m)

	if m.EventsProcessed != eventCount {
		t.Errorf("EventsProcessed = %d, want %d", m.EventsProcessed, eventCount)
	}

	// Virtual time must have advanced to ~6 h.
	if m.VirtualElapsed < 5*time.Hour {
		t.Errorf("VirtualElapsed = %s, want >= 5h", m.VirtualElapsed)
	}

	// Core assertion: ratio >= 8 even with 1000 nodes.
	if m.Ratio < 8 {
		t.Errorf("Ratio = %.2f, want >= 8 (got %s)", m.Ratio, m)
	}
}

// TestBatchSchedule verifies that BatchSchedule schedules exactly
// len(payloads) events that all fire, and that the returned IDs are distinct
// and correspond to real events (i.e., the handler is actually invoked for
// each one).
func TestBatchSchedule(t *testing.T) {
	const n = 37 // arbitrary non-round number

	eng := dst.NewEngine(777)

	var fired int64
	eng.RegisterHandler("batch", func(e *dst.Event) {
		atomic.AddInt64(&fired, 1)
	})

	payloads := make([]interface{}, n)
	for i := range payloads {
		payloads[i] = i // each payload is its own index
	}

	const delay = int64(500 * time.Millisecond)
	ids := BatchSchedule(eng, "batch", payloads, delay)

	// IDs slice must have the right length.
	if len(ids) != n {
		t.Fatalf("BatchSchedule returned %d IDs, want %d", len(ids), n)
	}

	// IDs must be distinct (each ScheduleEvent call increments the engine's
	// monotonic counter).
	seen := make(map[int64]struct{}, n)
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate event ID %d in BatchSchedule result", id)
		}
		seen[id] = struct{}{}
	}

	// Run the engine until the queue is drained.
	processed := eng.Run(n * 2)

	if processed != n {
		t.Errorf("Run processed %d events, want %d", processed, n)
	}

	// Sink-side evidence: every scheduled event must have fired exactly once.
	if got := atomic.LoadInt64(&fired); got != int64(n) {
		t.Errorf("handler fired %d times, want %d", got, n)
	}
}

// TestBatchSchedule_SameTimestamp asserts that all events scheduled by
// BatchSchedule with the same delay share an identical virtual timestamp,
// proving they are truly independent same-timestamp events (not staggered).
//
// The handler captures eng.Now().UnixNano() — the engine's virtual clock at
// dispatch time — rather than the payload, so the assertion proves the engine
// actually advanced its clock to the same timestamp for every batch event.
func TestBatchSchedule_SameTimestamp(t *testing.T) {
	eng := dst.NewEngine(42)

	const delay = int64(1 * int64(time.Second))
	const batchSize = 10

	// Capture the engine's virtual clock (UnixNano) at each dispatch.
	// This is the only way to prove same-timestamp dispatch — payload equality
	// would only verify a constant equals itself.
	firedVirtualNs := make([]int64, 0, batchSize)
	eng.RegisterHandler("ts-capture", func(e *dst.Event) {
		firedVirtualNs = append(firedVirtualNs, eng.Now().UnixNano())
	})

	payloads := make([]interface{}, batchSize)
	for i := range payloads {
		payloads[i] = i // distinct payloads; timestamp proof uses eng.Now()
	}

	ids := BatchSchedule(eng, "ts-capture", payloads, delay)

	if len(ids) != batchSize {
		t.Fatalf("expected %d IDs, got %d", batchSize, len(ids))
	}

	eng.Run(batchSize * 2)

	if len(firedVirtualNs) != batchSize {
		t.Fatalf("expected %d firings, got %d", batchSize, len(firedVirtualNs))
	}

	// The engine clock starts at 0 (time.Unix(0,0)); all events were scheduled
	// with the same delay from that epoch, so all must fire when the virtual
	// clock equals delay nanoseconds (i.e. eng.Now().UnixNano() == delay).
	expectedNs := delay

	// Every event must have fired at exactly the same virtual timestamp, and
	// that timestamp must equal delay.  This proves the batch events were not
	// staggered — all share the same virtual clock value.
	for i, ns := range firedVirtualNs {
		if ns != expectedNs {
			t.Errorf("event %d: virtual clock at dispatch = %d, want %d (delta %d ns)",
				i, ns, expectedNs, ns-expectedNs)
		}
	}
}

// TestMeasure_WallElapsedIsReal confirms that WallElapsed is sourced from real
// wall time (not hardcoded or fabricated).  We inject a tiny measurable delay
// via a handler that does real work (allocations), then assert WallElapsed > 0.
// This test would fail if Measure returned a hardcoded zero for WallElapsed.
func TestMeasure_WallElapsedIsReal(t *testing.T) {
	eng := dst.NewEngine(1111)

	// Handler does a small but real allocation to ensure wall time > 0.
	eng.RegisterHandler("work", func(e *dst.Event) {
		buf := make([]byte, 1024)
		_ = buf
	})

	const eventCount = 100
	for i := 1; i <= eventCount; i++ {
		eng.ScheduleEvent("work", nil, int64(i)*int64(time.Millisecond))
	}

	m := Measure(eng, eventCount)

	t.Logf("wall-elapsed realness check: %s", m)

	// WallElapsed must be a valid positive duration from real time.Now().
	// Use <= 0 so that a hardcoded zero also causes this dedicated test to fail —
	// a zero would slip past a strict < 0 check.
	if m.WallElapsed <= 0 {
		t.Errorf("WallElapsed is zero or negative (%s) — not sourced from real wall clock?", m.WallElapsed)
	}

	// The ratio must be finite and positive (floorNs ensures denominator >= 1).
	if m.Ratio <= 0 {
		t.Errorf("Ratio = %f, must be > 0", m.Ratio)
	}

	// Virtual time must have advanced (100 ms virtual span).
	if m.VirtualElapsed < 90*time.Millisecond {
		t.Errorf("VirtualElapsed = %s, want >= 90ms", m.VirtualElapsed)
	}
}
