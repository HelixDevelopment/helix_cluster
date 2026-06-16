package dstcompress

import (
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dst"
)

// withTimeout guards every adversarial probe so a hang surfaces as a failure,
// never an infinite test run (hard rule: never leave a hanging test).
func withTimeout(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("probe exceeded %s — possible hang", d)
	}
}

// drainOrder schedules payloads 0..n-1 via BatchSchedule at the same delay and
// returns the payloads in the order the engine actually dispatched them.
func drainOrder(t *testing.T, seed int64, n int, delay int64) []int {
	t.Helper()
	eng := dst.NewEngine(seed)
	order := make([]int, 0, n)
	eng.RegisterHandler("probe", func(e *dst.Event) {
		order = append(order, e.Payload.(int))
	})
	payloads := make([]interface{}, n)
	for i := range payloads {
		payloads[i] = i
	}
	ids := BatchSchedule(eng, "probe", payloads, delay)
	if len(ids) != n {
		t.Fatalf("BatchSchedule returned %d ids, want %d", len(ids), n)
	}
	eng.Run(n * 2)
	return order
}

// TestAdversarial_BatchSchedule_NoLossNoDupNoCorruption is the round-trip
// correctness probe: regardless of dispatch ORDER, the SET of payloads that
// fire must be EXACTLY the set scheduled — no payload dropped, duplicated, or
// corrupted. This is the lossless invariant for the batch path. It holds on
// current code and would fail if BatchSchedule silently dropped or mangled
// payloads.
func TestAdversarial_BatchSchedule_NoLossNoDupNoCorruption(t *testing.T) {
	withTimeout(t, 10*time.Second, func() {
		for _, n := range []int{0, 1, 2, 3, 16, 37, 64, 100, 257} {
			got := drainOrder(t, 1, n, int64(time.Second))
			if len(got) != n {
				t.Errorf("n=%d: fired %d events, want %d", n, len(got), n)
				continue
			}
			// Sort and compare against the identity set 0..n-1: proves every
			// scheduled payload arrived exactly once, uncorrupted.
			sorted := append([]int(nil), got...)
			sort.Ints(sorted)
			for i := 0; i < n; i++ {
				if sorted[i] != i {
					t.Errorf("n=%d: payload set corrupted at %d: got %d; full=%v", n, i, sorted[i], got)
					break
				}
			}
		}
	})
}

// TestAdversarial_BatchSchedule_DispatchOrderIsDeterministic pins that the
// same-timestamp dispatch order is reproducible across runs and across seeds
// (the order is independent of the PRNG seed — it is a pure function of the
// heap). A determinism BLUFF (e.g. map-iteration leakage) would fail this.
func TestAdversarial_BatchSchedule_DispatchOrderIsDeterministic(t *testing.T) {
	withTimeout(t, 10*time.Second, func() {
		const n = 50
		base := drainOrder(t, 1, n, int64(time.Second))
		for i := 0; i < 8; i++ {
			again := drainOrder(t, 1, n, int64(time.Second))
			if !reflect.DeepEqual(base, again) {
				t.Fatalf("dispatch order not reproducible across runs:\n  a=%v\n  b=%v", base, again)
			}
		}
		// Different seed must not change same-timestamp dispatch order.
		if other := drainOrder(t, 999999, n, int64(time.Second)); !reflect.DeepEqual(base, other) {
			t.Fatalf("dispatch order depends on PRNG seed (should not):\n  seed1=%v\n  seed2=%v", base, other)
		}
	})
}

// TestAdversarial_BatchSchedule_NotInsertionOrder is a CONTRACT PIN documenting
// the real, observed behavior of the dst engine's min-heap: equal-timestamp
// events are NOT dispatched in insertion (FIFO) order, because
// dst.EventQueue.Less compares only Timestamp and never breaks ties on the
// monotonic event ID. The corrected BatchSchedule doc-comment now states this.
//
// This test exists so that if dst.EventQueue.Less is ever fixed to tie-break on
// ID (restoring true FIFO), this pin fails loudly and BOTH the doc-comment and
// this test must be updated together. It encodes the current observed order:
// payload 0 first, then n-1, n-2, ..., 1.
func TestAdversarial_BatchSchedule_NotInsertionOrder(t *testing.T) {
	withTimeout(t, 5*time.Second, func() {
		const n = 8
		got := drainOrder(t, 1, n, int64(time.Second))

		// Build the currently-observed order: 0, then descending n-1..1.
		want := make([]int, 0, n)
		want = append(want, 0)
		for v := n - 1; v >= 1; v-- {
			want = append(want, v)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("observed same-timestamp dispatch order changed.\n"+
				"got = %v\nwant(pinned) = %v\n"+
				"If dst.EventQueue.Less was fixed to tie-break on ID (true FIFO),\n"+
				"update BOTH this pin and the BatchSchedule doc-comment.", got, want)
		}

		// Anti-bluff: the order is genuinely NOT the FIFO insertion order for n>2.
		fifo := make([]int, n)
		for i := range fifo {
			fifo[i] = i
		}
		if reflect.DeepEqual(got, fifo) {
			t.Fatalf("order is FIFO %v — doc-comment fix is now stale and should "+
				"re-promise FIFO; this contradicts the pinned non-FIFO behavior", fifo)
		}
	})
}

// TestAdversarial_BatchSchedule_HostileInputs proves no panic / no hang on
// hostile batch inputs: empty slice, nil-element payloads, negative delay
// (schedules events in the virtual past relative to a later clock), and zero
// delay. Every scheduled event must still fire exactly once.
func TestAdversarial_BatchSchedule_HostileInputs(t *testing.T) {
	withTimeout(t, 10*time.Second, func() {
		cases := []struct {
			name  string
			n     int
			delay int64
			nilPL bool
		}{
			{"empty", 0, int64(time.Second), false},
			{"single", 1, int64(time.Second), false},
			{"zero-delay", 5, 0, false},
			{"negative-delay", 5, -int64(time.Hour), false},
			{"nil-payloads", 5, int64(time.Second), true},
			{"max-delay", 3, 1<<62 - 1, false},
		}
		for _, c := range cases {
			eng := dst.NewEngine(7)
			var mu sync.Mutex
			fired := 0
			eng.RegisterHandler("h", func(e *dst.Event) {
				mu.Lock()
				fired++
				mu.Unlock()
			})
			payloads := make([]interface{}, c.n)
			for i := range payloads {
				if !c.nilPL {
					payloads[i] = i
				} // else leave nil
			}
			ids := BatchSchedule(eng, "h", payloads, c.delay)
			if len(ids) != c.n {
				t.Errorf("%s: got %d ids, want %d", c.name, len(ids), c.n)
			}
			eng.Run(c.n*2 + 1)
			if fired != c.n {
				t.Errorf("%s: handler fired %d times, want %d", c.name, fired, c.n)
			}
		}
	})
}

// TestAdversarial_BatchSchedule_UniqueMonotonicIDs proves the returned IDs are
// distinct and strictly increasing (the engine's monotonic counter), so a
// caller can correlate IDs to insertion position even though dispatch order is
// not FIFO.
func TestAdversarial_BatchSchedule_UniqueMonotonicIDs(t *testing.T) {
	withTimeout(t, 5*time.Second, func() {
		eng := dst.NewEngine(3)
		eng.RegisterHandler("k", func(e *dst.Event) {})
		const n = 64
		payloads := make([]interface{}, n)
		for i := range payloads {
			payloads[i] = i
		}
		ids := BatchSchedule(eng, "k", payloads, int64(time.Second))
		for i := 1; i < len(ids); i++ {
			if ids[i] <= ids[i-1] {
				t.Fatalf("ids not strictly increasing at %d: %d <= %d (full=%v)", i, ids[i], ids[i-1], ids)
			}
		}
	})
}

// --- Measure / CompressionMetrics edge probes ---

// TestAdversarial_Measure_EmptyEngine proves Measure on an engine with no
// scheduled events does not panic and reports zero virtual elapsed, zero
// events, and a finite (>= 0) ratio (floorNs guards the denominator).
func TestAdversarial_Measure_EmptyEngine(t *testing.T) {
	withTimeout(t, 5*time.Second, func() {
		eng := dst.NewEngine(5)
		m := Measure(eng, 100)
		if m.EventsProcessed != 0 {
			t.Errorf("EventsProcessed = %d, want 0", m.EventsProcessed)
		}
		if m.VirtualElapsed != 0 {
			t.Errorf("VirtualElapsed = %s, want 0", m.VirtualElapsed)
		}
		if m.Ratio < 0 {
			t.Errorf("Ratio = %f, want >= 0 (floorNs must guard denominator)", m.Ratio)
		}
		if m.WallElapsed < 0 {
			t.Errorf("WallElapsed = %s, want >= 0", m.WallElapsed)
		}
	})
}

// TestAdversarial_Measure_ZeroMaxEvents proves Measure(eng, 0) is a safe no-op:
// no events processed, no virtual time advanced, no panic.
func TestAdversarial_Measure_ZeroMaxEvents(t *testing.T) {
	withTimeout(t, 5*time.Second, func() {
		eng := dst.NewEngine(5)
		eng.RegisterHandler("x", func(e *dst.Event) {})
		eng.ScheduleEvent("x", nil, int64(time.Hour))
		m := Measure(eng, 0)
		if m.EventsProcessed != 0 {
			t.Errorf("EventsProcessed = %d, want 0 for maxEvents=0", m.EventsProcessed)
		}
		if m.VirtualElapsed != 0 {
			t.Errorf("VirtualElapsed = %s, want 0 for maxEvents=0", m.VirtualElapsed)
		}
	})
}

// TestAdversarial_floorNs pins the divide-by-zero guard at the numeric edges:
// values < 1 floor to 1; values >= 1 pass through; the most-negative int64 must
// floor to 1 (not stay negative, which would invert the ratio sign).
func TestAdversarial_floorNs(t *testing.T) {
	cases := []struct {
		in, want int64
	}{
		{-1 << 62, 1},
		{-1, 1},
		{0, 1},
		{1, 1},
		{2, 2},
		{1 << 62, 1 << 62},
	}
	for _, c := range cases {
		if got := floorNs(c.in); got != c.want {
			t.Errorf("floorNs(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestAdversarial_Measure_RatioFiniteAndPositive runs a real batch through
// Measure and asserts the ratio is finite and positive — a hardcoded-zero
// WallElapsed (which would make Ratio +Inf) or a zero VirtualElapsed bluff
// would be caught here.
func TestAdversarial_Measure_RatioFiniteAndPositive(t *testing.T) {
	withTimeout(t, 10*time.Second, func() {
		eng := dst.NewEngine(11)
		eng.RegisterHandler("w", func(e *dst.Event) {
			b := make([]byte, 256)
			_ = b
		})
		for i := 1; i <= 200; i++ {
			eng.ScheduleEvent("w", nil, int64(i)*int64(time.Millisecond))
		}
		m := Measure(eng, 200)
		if m.EventsProcessed != 200 {
			t.Errorf("EventsProcessed = %d, want 200", m.EventsProcessed)
		}
		if m.Ratio <= 0 {
			t.Errorf("Ratio = %f, want > 0", m.Ratio)
		}
		// Ratio must be finite (not Inf/NaN).
		if m.Ratio != m.Ratio || m.Ratio > 1e308 {
			t.Errorf("Ratio = %f, want finite", m.Ratio)
		}
		if m.WallElapsed <= 0 {
			t.Errorf("WallElapsed = %s, want > 0", m.WallElapsed)
		}
	})
}
