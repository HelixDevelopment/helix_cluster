package flowcontrol

// ---------------------------------------------------------------------------
// Adversarial edge suite for pkg/flowcontrol.
//
// The existing *_concurrent_test.go already hammers the concurrency/credit
// window (bug class (b)) and honestly pins the single-owner contract, and
// *_test.go covers happy-path fairness. This file deliberately attacks the
// UNDER-EXPLORED classes the prompt names:
//
//   (a) TOTAL-ORDER AT A NUMERIC/IDENTITY EDGE  -> round-robin cursor vs the
//       in-place sort.Strings in enqueue: rrCursor is a *positional* index, and
//       enqueue re-sorts rrOrder whenever a new flow appears. A new flow whose
//       key sorts BEFORE the cursor's current target shifts every element, so
//       the cursor silently re-aims at a different flow. The package promises
//       "true round-robin" ("the next dispatch goes to a DIFFERENT flow first")
//       and that fair-queuing "prevents a flooding flow from starving a quiet
//       one". This probes whether cursor invalidation breaks that promise.
//
//   (c) NUMERIC POISON / LIMIT BYPASS -> negative clock, uint64 seq wrap.
//
//   (d) HOSTILE-INPUT / accounting integrity -> Finish on an id that is not in
//       flight must not drive inFlight below zero or corrupt the seat count.
//
// All assertions are SINK-SIDE: actual dispatch order, actual admitted counts,
// actual inFlight vs cap — never merely "did not panic".
// ---------------------------------------------------------------------------

import (
	"fmt"
	"testing"
)

const advRunUUID = "fc-adv-7c1f93e2-hxc-flowctl-edge"

// admitMatch is a trivial always-true classifier onto one level, distinguishing
// by raw FlowID. Single level, single per-flow queue cap big enough to hold the
// scenario.
func newAdvController(t *testing.T, clock Clock, concurrency, qlen int) *Controller {
	t.Helper()
	c := NewController(clock)
	if err := c.RegisterPriorityLevel("std", concurrency, qlen); err != nil {
		t.Fatalf("register level: %v", err)
	}
	fs := FlowSchema{
		Name:        "all",
		Level:       "std",
		MatchOrder:  0,
		Match:       func(r Request) bool { return true },
		Distinguish: func(r Request) string { return r.FlowID },
	}
	if err := c.RegisterFlowSchema(fs); err != nil {
		t.Fatalf("register schema: %v", err)
	}
	return c
}

// drainAll finishes every in-flight seat in the level (deterministic: snapshot
// ids first), then returns how many it freed.
func drainAll(t *testing.T, c *Controller, level string) int {
	t.Helper()
	ids := make([]string, 0)
	for id := range c.levels[level].seats {
		ids = append(ids, id)
	}
	for _, id := range ids {
		if err := c.Finish(id); err != nil {
			t.Fatalf("finish %s: %v", id, err)
		}
	}
	return len(ids)
}

// -----------------------------------------------------------------------------
// (a) ROUND-ROBIN CURSOR vs in-place sort in enqueue.
//
// Scenario, concurrency=1 so each Tick dispatches exactly one request and the
// cursor advances by exactly one flow — making the round-robin order directly
// observable in the dispatch sequence.
//
//  1. Enqueue flows "b" and "d" (each one request). rrOrder = [b, d].
//  2. Tick -> dispatches "b" (cursor 0). start sets inFlight=1. cursor -> 1 (d).
//  3. Finish it. inFlight=0.
//  4. NOW enqueue a brand-new flow "a" (one request). enqueue sees a new flow,
//     appends then sort.Strings -> rrOrder = [a, b, d]. The OLD cursor value 1
//     used to mean "d"; after the sort, index 1 means "b".
//  5. Tick -> dispatchNext starts scanning at index 1 == "b". But "b" was
//     already served and its queue is empty, so it skips to "d". Fine so far.
//
// The sharper starvation case: make the newly-inserted flow land EXACTLY on the
// cursor so a still-pending, already-served-once flow gets re-served before a
// flow that has waited longer. We assert the OBSERVED dispatch multiset/order
// against the round-robin promise: across a full drain every flow with equal
// backlog is served the same number of times and no flow is served twice in a
// row while another non-empty flow waits.
//
// FAIRNESS PROMISE UNDER TEST (doc): round-robin => with N non-empty equal-depth
// flows, N consecutive dispatches hit N DISTINCT flows (a permutation), never
// the same flow twice while a different non-empty flow is pending.
// -----------------------------------------------------------------------------
func TestRoundRobin_CursorSurvivesEnqueueResort(t *testing.T) {
	clock := NewManualClock(0)
	c := newAdvController(t, clock, 1, 64)

	// Phase 1: two flows present, serve one so the cursor is mid-rotation.
	for _, f := range []string{"b", "d"} {
		_, d, err := c.Offer(Request{ID: f + "1", FlowID: f})
		if err != nil {
			t.Fatalf("offer %s1: %v", f, err)
		}
		// First offer with empty level admits now; second enqueues.
		_ = d
	}
	// One seat, so exactly one of {b,d} is in flight, the other queued.
	drainAll(t, c, "std") // free the admitted one
	c.Tick()              // dispatch the queued one; cursor now mid-rotation
	drainAll(t, c, "std")

	// Phase 2: inject more requests on ALL THREE flows including a brand-new "a"
	// that sorts before the cursor target, forcing the in-place resort to shift
	// indices under the live cursor.
	for _, f := range []string{"a", "b", "d"} {
		for i := 0; i < 3; i++ {
			_, _, err := c.Offer(Request{ID: fmt.Sprintf("%s-r%d", f, i), FlowID: f})
			if err != nil {
				t.Fatalf("offer %s r%d: %v", f, i, err)
			}
		}
	}

	// Now drain one-at-a-time and record the exact dispatch order.
	var order []string
	for {
		drainAll(t, c, "std")
		started := c.Tick()
		if len(started) == 0 {
			if c.QueuedTotal("std") == 0 {
				break
			}
			t.Fatalf("stuck: queued=%d but Tick dispatched nothing", c.QueuedTotal("std"))
		}
		for _, s := range started {
			order = append(order, s.Flow)
		}
	}
	t.Logf("run=%s dispatch order=%v", advRunUUID, order)

	// SINK-SIDE round-robin assertion, robust to the cursor-vs-resort hazard:
	// the genuine "true round-robin" promise is that NO flow is served twice
	// while a DIFFERENT flow that STILL HAS BACKLOG has not yet been served in
	// the current rotation. We replay the order tracking each flow's true
	// remaining backlog (b,d start at 4 because of their phase-1 request; a at
	// 3) and the set of flows already served in the current rotation; a flow may
	// only repeat once every other non-empty flow has been served.
	remaining := map[string]int{"a": 3, "b": 4, "d": 4}
	servedThisRotation := map[string]bool{}
	for i, f := range order {
		if remaining[f] <= 0 {
			t.Fatalf("dispatched flow %s with no remaining backlog at pos %d; order=%v", f, i, order)
		}
		if servedThisRotation[f] {
			// f repeats — legal ONLY if every other still-nonempty flow was
			// already served this rotation (i.e. the rotation completed).
			for _, other := range []string{"a", "b", "d"} {
				if other == f {
					continue
				}
				if remaining[other] > 0 && !servedThisRotation[other] {
					t.Fatalf("round-robin VIOLATED at pos %d: %s served twice while %s (remaining=%d) waited unserved this rotation — cursor mis-aimed after enqueue resort; order=%v",
						i, f, other, remaining[other], order)
				}
			}
			// rotation completed; start a fresh one with this dispatch.
			servedThisRotation = map[string]bool{}
		}
		servedThisRotation[f] = true
		remaining[f]--
	}

	// And every flow must be fully drained exactly its backlog count.
	for _, f := range []string{"a", "b", "d"} {
		got := c.AdmittedCount("std", f)
		// b and d also had the 1 from phase 1.
		want := int64(3)
		if f == "b" || f == "d" {
			want = 4
		}
		if got != want {
			t.Fatalf("flow %s admitted=%d want=%d (lost or duplicated dispatch)", f, got, want)
		}
	}
}

// -----------------------------------------------------------------------------
// (a)/fairness, sharper: the classic APF promise is "a flooding flow cannot
// starve a quiet one". Build the cursor-resort hazard directly: serve a flow so
// the cursor sits on it, then introduce a lower-sorting flood flow. If the
// cursor mis-aims, the flood flow can be served repeatedly before the quiet
// flow that was already waiting. Assert the quiet flow is served within one
// full rotation, not starved behind the flood.
// -----------------------------------------------------------------------------
func TestRoundRobin_QuietFlowNotStarvedAfterResort(t *testing.T) {
	clock := NewManualClock(0)
	c := newAdvController(t, clock, 1, 4096)

	// Quiet flow "z" enqueues a single request first (sorts last).
	if _, _, err := c.Offer(Request{ID: "z0", FlowID: "z"}); err != nil {
		t.Fatalf("offer z0: %v", err)
	}
	// "z0" admitted immediately (empty level). Drain so the level is idle but
	// rrOrder now contains nothing yet (z0 was admit-now, never queued).
	drainAll(t, c, "std")

	// Quiet flow "z" enqueues again -> now it queues (we keep a seat busy by
	// offering a filler that we DON'T finish).
	if _, dfill, err := c.Offer(Request{ID: "fill", FlowID: "m"}); err != nil || dfill != DecisionAdmit {
		t.Fatalf("offer fill: d=%v err=%v", dfill, err)
	}
	// Seat now busy with "fill". Everything below must queue.
	if _, dz, err := c.Offer(Request{ID: "z1", FlowID: "z"}); err != nil || dz != DecisionEnqueue {
		t.Fatalf("offer z1: d=%v err=%v", dz, err)
	}
	// Introduce a flood flow "a" (sorts first) AFTER z is already waiting.
	const flood = 50
	for i := 0; i < flood; i++ {
		if _, da, err := c.Offer(Request{ID: fmt.Sprintf("a%d", i), FlowID: "a"}); err != nil || da != DecisionEnqueue {
			t.Fatalf("offer a%d: d=%v err=%v", i, da, err)
		}
	}

	// Release the filler and drive dispatch. With true round-robin, z (waiting
	// longest) must be served within the first full rotation across {a,z}, i.e.
	// among the first 2 dispatches — NOT after the entire flood.
	if err := c.Finish("fill"); err != nil {
		t.Fatalf("finish fill: %v", err)
	}

	var order []string
	for c.QueuedTotal("std") > 0 || c.InFlight("std") > 0 {
		started := c.Tick()
		for _, s := range started {
			order = append(order, s.Flow)
		}
		drainAll(t, c, "std")
		if len(order) > flood+10 {
			break
		}
	}
	t.Logf("run=%s dispatch order (first 8)=%v zPos=%d", advRunUUID, firstN(order, 8), indexOf(order, "z"))

	zPos := indexOf(order, "z")
	if zPos < 0 {
		t.Fatalf("quiet flow z NEVER dispatched; order=%v", firstN(order, 12))
	}
	// Two non-empty flows {a,z}: round-robin must serve z by the 2nd dispatch.
	if zPos > 1 {
		t.Fatalf("quiet flow z STARVED: dispatched only at position %d behind the flood (round-robin would place it at <=1); order prefix=%v",
			zPos, firstN(order, 8))
	}
}

// -----------------------------------------------------------------------------
// (d) Finish on an unknown / already-finished id must be a clean no-op error,
// never driving inFlight negative or below the true seat count. Probe the
// accounting floor directly.
// -----------------------------------------------------------------------------
func TestFinish_UnknownAndDouble_NoNegativeInFlight(t *testing.T) {
	clock := NewManualClock(0)
	c := newAdvController(t, clock, 4, 16)

	// Admit two.
	for _, id := range []string{"x", "y"} {
		if _, d, err := c.Offer(Request{ID: id, FlowID: "f"}); err != nil || d != DecisionAdmit {
			t.Fatalf("offer %s: d=%v err=%v", id, d, err)
		}
	}
	if got := c.InFlight("std"); got != 2 {
		t.Fatalf("inFlight=%d want 2", got)
	}

	// Unknown id -> error, no accounting change.
	if err := c.Finish("nope"); err != ErrUnknownRequest {
		t.Fatalf("Finish(unknown)=%v want ErrUnknownRequest", err)
	}
	if got := c.InFlight("std"); got != 2 {
		t.Fatalf("inFlight after unknown finish=%d want 2 (must not change)", got)
	}

	// Finish x once (ok), then again (must be unknown now, no double-decrement).
	if err := c.Finish("x"); err != nil {
		t.Fatalf("Finish(x)=%v want nil", err)
	}
	if err := c.Finish("x"); err != ErrUnknownRequest {
		t.Fatalf("double Finish(x)=%v want ErrUnknownRequest", err)
	}
	if got := c.InFlight("std"); got != 1 {
		t.Fatalf("inFlight after x,x finish=%d want 1 (no negative drift)", got)
	}
	if got := len(c.levels["std"].seats); got != c.InFlight("std") {
		t.Fatalf("seat/inFlight desync: seats=%d inFlight=%d", got, c.InFlight("std"))
	}
}

// -----------------------------------------------------------------------------
// (c) Numeric edge: a negative / non-monotonic clock must not break FIFO-within
// -flow ordering, because ordering inside a flow is by queue position (FIFO),
// and the stable tie-break is the monotonic seq counter, NOT arrival. Pin that
// arrival timestamps (even negative) never reorder a flow's FIFO. SINK-SIDE:
// dispatch order within a single flow equals enqueue order regardless of clock.
// -----------------------------------------------------------------------------
func TestNegativeClock_DoesNotReorderFIFO(t *testing.T) {
	clock := NewManualClock(0)
	c := newAdvController(t, clock, 1, 64)

	// Keep the single seat busy so everything queues in one flow.
	if _, d, err := c.Offer(Request{ID: "busy", FlowID: "f"}); err != nil || d != DecisionAdmit {
		t.Fatalf("offer busy: d=%v err=%v", d, err)
	}
	// Enqueue r0..r4 with a clock that runs BACKWARDS (poison arrival values).
	for i := 0; i < 5; i++ {
		clock.Advance(int64(-1000 + i)) // negative, increasing
		if _, d, err := c.Offer(Request{ID: fmt.Sprintf("r%d", i), FlowID: "f"}); err != nil || d != DecisionEnqueue {
			t.Fatalf("offer r%d: d=%v err=%v", i, d, err)
		}
	}
	if err := c.Finish("busy"); err != nil {
		t.Fatalf("finish busy: %v", err)
	}

	// Dispatch all and assert FIFO order r0,r1,r2,r3,r4 (seat=1 => one per tick).
	var ids []string
	for c.QueuedTotal("std") > 0 || c.InFlight("std") > 0 {
		c.Tick()
		for id := range c.levels["std"].seats {
			ids = append(ids, id)
		}
		drainAll(t, c, "std")
	}
	t.Logf("run=%s FIFO dispatch ids=%v", advRunUUID, ids)
	want := []string{"r0", "r1", "r2", "r3", "r4"}
	if len(ids) != len(want) {
		t.Fatalf("dispatched %v want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("FIFO violated under negative clock: got %v want %v", ids, want)
		}
	}
}

// --- small helpers ---

func firstN(s []string, n int) []string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
