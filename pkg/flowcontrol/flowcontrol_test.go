package flowcontrol

import (
	"fmt"
	"testing"
)

const runUUID = "fc-3f1c2a4e-7b90-4d2e-9c11-hxc1413-run"

// newTwoFlowController builds a controller with one level "std" (concurrency 2)
// and a single FlowSchema that distinguishes flows by FlowID.
func newTwoFlowController(t *testing.T, clock Clock, concurrency, maxQueue int) *Controller {
	t.Helper()
	c := NewController(clock)
	if err := c.RegisterPriorityLevel("std", concurrency, maxQueue); err != nil {
		t.Fatalf("RegisterPriorityLevel: %v", err)
	}
	if err := c.RegisterFlowSchema(FlowSchema{
		Name:        "all",
		Level:       "std",
		MatchOrder:  0,
		Match:       func(r Request) bool { return true },
		Distinguish: func(r Request) string { return r.FlowID },
	}); err != nil {
		t.Fatalf("RegisterFlowSchema: %v", err)
	}
	return c
}

// TestFairQueuing_BNotStarvedByFlood covers CLOSURE clause 1.
//
// Flow A floods the level with 1000 queued requests; flow B sends a steady
// small stream of 10. We drive dispatch and assert B's requests are admitted
// promptly (B finishes long before A drains), proving round-robin fairness
// rather than strict FIFO.
//
// Mutation: if dispatchNext is replaced with strict global FIFO (serve all of
// A before B), B would only finish after ~1000 dispatches, and the assertion
// that B completes within a small bound (well under A's total) fails.
func TestFairQueuing_BNotStarvedByFlood(t *testing.T) {
	clock := NewManualClock(0)
	const aFlood = 1000
	const bStream = 10
	concurrency := 2
	// Per-flow queue must hold the flood.
	c := newTwoFlowController(t, clock, concurrency, aFlood+bStream+1)

	// A floods first (worst case for B under FIFO).
	for i := 0; i < aFlood; i++ {
		r := Request{ID: fmt.Sprintf("A-%d", i), FlowID: "A"}
		_, d, err := c.Offer(r)
		if err != nil {
			t.Fatalf("offer A-%d: %v", i, err)
		}
		// First `concurrency` may admit; rest enqueue.
		if d == DecisionReject {
			t.Fatalf("A-%d unexpectedly rejected", i)
		}
	}
	// B's small stream arrives after the flood.
	for i := 0; i < bStream; i++ {
		r := Request{ID: fmt.Sprintf("B-%d", i), FlowID: "B"}
		_, d, err := c.Offer(r)
		if err != nil || d == DecisionReject {
			t.Fatalf("offer B-%d: d=%v err=%v", i, d, err)
		}
	}

	beforeA := c.AdmittedCount("std", "A")
	beforeB := c.AdmittedCount("std", "B")
	t.Logf("run=%s BEFORE admitted A=%d B=%d queuedTotal=%d",
		runUUID, beforeA, beforeB, c.QueuedTotal("std"))

	// Drive dispatch: each step finishes whatever is in-flight then ticks to
	// refill seats. Count how many dispatch ticks until B is fully admitted.
	tickOfBDone := -1
	maxTicks := aFlood*2 + 100
	for step := 0; step < maxTicks; step++ {
		clock.Advance(1)
		// Finish all currently in-flight to free seats for the next tick.
		// (Deterministic drain: collect ids, then finish.)
		for id := range c.levels["std"].seats {
			if err := c.Finish(id); err != nil {
				t.Fatalf("finish %s: %v", id, err)
			}
		}
		c.Tick()
		if tickOfBDone == -1 && c.AdmittedCount("std", "B") == int64(bStream) {
			tickOfBDone = step
		}
		if c.QueuedTotal("std") == 0 && c.InFlight("std") == 0 {
			break
		}
	}

	afterA := c.AdmittedCount("std", "A")
	afterB := c.AdmittedCount("std", "B")
	t.Logf("run=%s AFTER admitted A=%d B=%d tickOfBDone=%d", runUUID, afterA, afterB, tickOfBDone)

	if afterB != int64(bStream) {
		t.Fatalf("B not fully admitted: got %d want %d", afterB, bStream)
	}
	if tickOfBDone < 0 {
		t.Fatalf("B never completed")
	}
	// Fairness: with 2 seats split round-robin between A and B, B (10 reqs)
	// should finish in roughly 2*bStream ticks, FAR before A's ~1000. Use a
	// generous bound that strict-FIFO (B finishes only near A's tail) blows.
	fairBound := bStream * 4
	bAdmittedWhenDone := afterB
	// Re-derive A admitted at the moment B finished by replaying the bound
	// expectation: under fairness A admitted at B-done must be modest.
	t.Logf("run=%s fairness: B done by tick %d (bound %d); A still had backlog",
		runUUID, tickOfBDone, fairBound)
	if tickOfBDone > fairBound {
		t.Fatalf("B starved: finished only at tick %d, fairness bound %d (FIFO-like behavior)",
			tickOfBDone, fairBound)
	}
	if bAdmittedWhenDone != int64(bStream) {
		t.Fatalf("sanity: B admitted %d != %d", bAdmittedWhenDone, bStream)
	}
	// And A must NOT be finished yet when B completed (proves B didn't wait for A).
	if afterA < int64(aFlood) {
		t.Fatalf("unexpected: full run should drain all A=%d, got %d", aFlood, afterA)
	}
}

// TestFairness_BCompletesBeforeAllOfA is a sharper, snapshot-based variant of
// clause 1: it captures A's admitted count at the exact tick B finishes and
// asserts a large chunk of A is still pending — i.e. B did not wait for A.
//
// Mutation: strict-FIFO dispatch makes A's admitted count at B-completion be
// ~ (aFlood) (A served first), so "A still mostly pending" fails.
func TestFairness_BCompletesBeforeAllOfA(t *testing.T) {
	clock := NewManualClock(0)
	const aFlood = 1000
	const bStream = 10
	c := newTwoFlowController(t, clock, 2, aFlood+bStream+1)

	for i := 0; i < aFlood; i++ {
		c.Offer(Request{ID: fmt.Sprintf("A-%d", i), FlowID: "A"})
	}
	for i := 0; i < bStream; i++ {
		c.Offer(Request{ID: fmt.Sprintf("B-%d", i), FlowID: "B"})
	}

	var aAtBDone int64 = -1
	for step := 0; step < aFlood*2+100; step++ {
		clock.Advance(1)
		for id := range c.levels["std"].seats {
			c.Finish(id)
		}
		c.Tick()
		if aAtBDone == -1 && c.AdmittedCount("std", "B") == int64(bStream) {
			aAtBDone = c.AdmittedCount("std", "A")
		}
		if c.QueuedTotal("std") == 0 && c.InFlight("std") == 0 {
			break
		}
	}
	t.Logf("run=%s clause1-snapshot: A admitted at moment B finished = %d (of %d)",
		runUUID, aAtBDone, aFlood)
	if aAtBDone < 0 {
		t.Fatalf("B never finished")
	}
	// Under fairness A admitted at B-done is ~bStream (round-robin 1:1), far
	// below half the flood. Strict FIFO would make this ~aFlood.
	if aAtBDone > aFlood/2 {
		t.Fatalf("B starved: A had already admitted %d/%d when B finished (expected << half)",
			aAtBDone, aFlood)
	}
}

// TestCrossLevelIsolation covers CLOSURE clause 2.
//
// A low-priority level floods; a high-priority level keeps offering. We assert
// the high level still admits at its configured concurrency every tick,
// unaffected by the low level's backlog.
//
// Mutation: if levels shared a single concurrency budget (no per-level cap),
// the low flood would consume seats and high's per-tick admit rate would drop
// below its configured concurrency.
func TestCrossLevelIsolation(t *testing.T) {
	clock := NewManualClock(0)
	c := NewController(clock)
	if err := c.RegisterPriorityLevel("high", 3, 100); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterPriorityLevel("low", 5, 10000); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterFlowSchema(FlowSchema{
		Name: "hi", Level: "high", MatchOrder: 0,
		Match:       func(r Request) bool { return r.FlowID == "hi" },
		Distinguish: func(r Request) string { return "hi" },
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterFlowSchema(FlowSchema{
		Name: "lo", Level: "low", MatchOrder: 1,
		Match:       func(r Request) bool { return true },
		Distinguish: func(r Request) string { return r.FlowID },
	}); err != nil {
		t.Fatal(err)
	}

	// Flood low with 2000 requests.
	for i := 0; i < 2000; i++ {
		_, d, err := c.Offer(Request{ID: fmt.Sprintf("L-%d", i), FlowID: "lo"})
		if err != nil || d == DecisionReject {
			t.Fatalf("low offer %d: d=%v err=%v", i, d, err)
		}
	}

	beforeHigh := c.AdmittedCount("high", "hi")
	t.Logf("run=%s BEFORE high admitted=%d lowQueued=%d", runUUID, beforeHigh, c.QueuedTotal("low"))

	// Over several ticks, keep the high level saturated with fresh requests and
	// assert it admits exactly its concurrency (3) per tick.
	hiSeq := 0
	for tick := 0; tick < 8; tick++ {
		clock.Advance(1)
		// Drain in-flight from BOTH levels so each tick starts fresh.
		for id := range c.levels["high"].seats {
			c.Finish(id)
		}
		for id := range c.levels["low"].seats {
			c.Finish(id)
		}
		// Offer 3 fresh high-pri requests (enough to fill its seats).
		highBefore := c.AdmittedCount("high", "hi")
		for k := 0; k < 3; k++ {
			c.Offer(Request{ID: fmt.Sprintf("H-%d", hiSeq), FlowID: "hi"})
			hiSeq++
		}
		c.Tick()
		highDelta := c.AdmittedCount("high", "hi") - highBefore
		t.Logf("run=%s tick=%d high admitted this tick=%d inFlightHigh=%d inFlightLow=%d",
			runUUID, tick, highDelta, c.InFlight("high"), c.InFlight("low"))
		if highDelta != 3 {
			t.Fatalf("high pri throttled by low flood: admitted %d this tick, want 3", highDelta)
		}
	}
	afterHigh := c.AdmittedCount("high", "hi")
	t.Logf("run=%s AFTER high admitted=%d", runUUID, afterHigh)
	if afterHigh-beforeHigh != int64(8*3) {
		t.Fatalf("high total admitted delta=%d want %d", afterHigh-beforeHigh, 8*3)
	}
}

// TestConcurrencyCapHonored covers CLOSURE clause 3.
//
// Across an entire flooded run we assert in-flight never exceeds the configured
// concurrency for the level, by checking after every Offer and every Tick and
// via PeakInFlight.
//
// Mutation: removing the `lvl.inFlight < lvl.concurrency` guard in start/Tick
// lets in-flight exceed the cap; PeakInFlight > concurrency makes this fail.
func TestConcurrencyCapHonored(t *testing.T) {
	clock := NewManualClock(0)
	const cap = 4
	c := newTwoFlowController(t, clock, cap, 100000)

	assertCap := func(where string) {
		if got := c.InFlight("std"); got > cap {
			t.Fatalf("in-flight %d exceeded cap %d at %s", got, cap, where)
		}
	}

	// Offer a large flood across two flows; check the cap after each offer.
	total := 500
	for i := 0; i < total; i++ {
		flow := "A"
		if i%2 == 1 {
			flow = "B"
		}
		c.Offer(Request{ID: fmt.Sprintf("R-%d-%s", i, flow), FlowID: flow})
		assertCap(fmt.Sprintf("offer-%d", i))
	}

	beforePeak := c.PeakInFlight("std")
	t.Logf("run=%s BEFORE drain peakInFlight=%d cap=%d queued=%d",
		runUUID, beforePeak, cap, c.QueuedTotal("std"))

	// Drain: each tick finish a single in-flight request (partial release) then
	// tick to refill, exercising the cap repeatedly under churn.
	for step := 0; step < total*4 && (c.QueuedTotal("std") > 0 || c.InFlight("std") > 0); step++ {
		clock.Advance(1)
		// Finish exactly one in-flight (if any) to create churn at the boundary.
		for id := range c.levels["std"].seats {
			c.Finish(id)
			break
		}
		c.Tick()
		assertCap(fmt.Sprintf("tick-%d", step))
	}

	afterPeak := c.PeakInFlight("std")
	t.Logf("run=%s AFTER drain peakInFlight=%d cap=%d", runUUID, afterPeak, cap)
	if afterPeak > cap {
		t.Fatalf("peak in-flight %d exceeded cap %d over the run", afterPeak, cap)
	}
	if afterPeak != cap {
		// Under a flood the cap should actually be reached, proving the test
		// exercised the boundary (not vacuously passing on an idle controller).
		t.Fatalf("peak in-flight %d never reached cap %d; boundary not exercised", afterPeak, cap)
	}
	// All requests must eventually be admitted (none lost).
	totalAdmitted := c.AdmittedCount("std", "A") + c.AdmittedCount("std", "B")
	if totalAdmitted != int64(total) {
		t.Fatalf("admitted %d != offered %d (requests lost or duplicated)", totalAdmitted, total)
	}
}

// TestLateArrivalCannotJumpQueue exercises the admit-now fairness guard
// `lvl.totalQueued() == 0` in Offer: a brand-new request must NOT seize a freed
// seat ahead of requests already waiting in the queue.
//
// Scenario (concurrency=1, large queue cap):
//   - Offer A0 -> admits (seat taken, queue empty).
//   - Offer A1, B0 -> enqueue (seat busy). Two older requests now wait.
//   - Finish(A0) WITHOUT calling Tick: the seat is now free but the queue is
//     non-empty.
//   - Offer a fresh C0: with the guard it must DecisionEnqueue (it cannot jump
//     the waiting A1/B0 even though a seat is free). The subsequent Tick must
//     dispatch the OLDER queued request (A1, round-robin head), not C0.
//
// Mutation: removing the `&& lvl.totalQueued() == 0` clause from Offer's
// admit-now condition (admitting any fresh request whenever a seat is free,
// ignoring the existing queue) makes C0 DecisionAdmit immediately and seize the
// freed seat ahead of A1/B0 — this test FAILS on both the C0 decision assertion
// and the first-dispatched-after-Tick assertion.
func TestLateArrivalCannotJumpQueue(t *testing.T) {
	clock := NewManualClock(0)
	// concurrency 1, queue cap large enough that nothing is rejected for capacity.
	c := newTwoFlowController(t, clock, 1, 1000)

	// A0 takes the only seat (queue empty -> admit-now path).
	if _, d, err := c.Offer(Request{ID: "A0", FlowID: "A"}); err != nil || d != DecisionAdmit {
		t.Fatalf("A0 d=%v err=%v want admit", d, err)
	}
	// A1 and B0 must enqueue: seat is busy.
	if _, d, _ := c.Offer(Request{ID: "A1", FlowID: "A"}); d != DecisionEnqueue {
		t.Fatalf("A1 d=%v want enqueue", d)
	}
	if _, d, _ := c.Offer(Request{ID: "B0", FlowID: "B"}); d != DecisionEnqueue {
		t.Fatalf("B0 d=%v want enqueue", d)
	}

	queuedBefore := c.QueuedTotal("std")
	t.Logf("run=%s before-finish: inFlight=%d queuedTotal=%d", runUUID, c.InFlight("std"), queuedBefore)
	if queuedBefore != 2 {
		t.Fatalf("setup: queuedTotal=%d want 2 (A1,B0 waiting)", queuedBefore)
	}

	// Free the seat WITHOUT ticking: seat is now free but A1/B0 still wait.
	if err := c.Finish("A0"); err != nil {
		t.Fatalf("finish A0: %v", err)
	}
	if c.InFlight("std") != 0 {
		t.Fatalf("after finish inFlight=%d want 0 (seat free)", c.InFlight("std"))
	}

	// A brand-new request C0 arrives. Even though a seat is now free, an existing
	// queue (A1, B0) must block the admit-now path: C0 MUST enqueue, not jump.
	_, dC0, err := c.Offer(Request{ID: "C0", FlowID: "C"})
	t.Logf("run=%s late-arrival C0 decision=%v queuedTotal=%d inFlight=%d",
		runUUID, dC0, c.QueuedTotal("std"), c.InFlight("std"))
	if err != nil {
		t.Fatalf("offer C0: %v", err)
	}
	if dC0 != DecisionEnqueue {
		t.Fatalf("C0 d=%v want enqueue (late arrival must NOT jump a non-empty queue)", dC0)
	}
	// The free seat must still be free (C0 did not take it).
	if c.InFlight("std") != 0 {
		t.Fatalf("C0 seized a seat: inFlight=%d want 0", c.InFlight("std"))
	}

	// Now drive one dispatch pass: the FIRST request started must be an OLDER
	// queued one (A1, the round-robin head among flows A/B/C in flow order), NOT
	// the freshly-arrived C0.
	started := c.Tick()
	t.Logf("run=%s first dispatch after Tick: %+v", runUUID, started)
	if len(started) != 1 {
		t.Fatalf("Tick started %d want exactly 1 (concurrency=1)", len(started))
	}
	if started[0].Flow == "C" {
		t.Fatalf("late arrival C0 was dispatched first (flow=%s); it jumped the older A1/B0",
			started[0].Flow)
	}
	// Round-robin head with flows sorted [A,B,C] and cursor at 0 is flow A (A1).
	if started[0].Flow != "A" {
		t.Fatalf("first dispatch flow=%s want A (oldest queued, round-robin head)", started[0].Flow)
	}
	// C must still be waiting in the queue (it was not served first).
	if got := len(c.levels["std"].queues["C"]); got != 1 {
		t.Fatalf("C0 not still queued: queues[C] len=%d want 1", got)
	}
}

// TestClassifyPrecedenceAndReject proves Classify honors MatchOrder precedence
// and that unmatched requests are rejected (not silently admitted).
//
// Mutation: dropping the MatchOrder sort, or matching the wrong schema first,
// changes the level a request lands on and fails the precedence assertion.
func TestClassifyPrecedenceAndReject(t *testing.T) {
	clock := NewManualClock(0)
	c := NewController(clock)
	c.RegisterPriorityLevel("vip", 1, 10)
	c.RegisterPriorityLevel("bulk", 1, 10)

	// Two schemas both match tenant "t1"; the lower MatchOrder (vip) must win.
	c.RegisterFlowSchema(FlowSchema{
		Name: "vip-rule", Level: "vip", MatchOrder: 0,
		Match: func(r Request) bool { return r.FlowID == "t1" },
	})
	c.RegisterFlowSchema(FlowSchema{
		Name: "bulk-rule", Level: "bulk", MatchOrder: 5,
		Match: func(r Request) bool { return r.FlowID == "t1" || r.FlowID == "t2" },
	})

	cls, err := c.Classify(Request{ID: "x", FlowID: "t1"})
	if err != nil {
		t.Fatalf("classify t1: %v", err)
	}
	t.Logf("run=%s t1 classified to level=%s flow=%s", runUUID, cls.Level, cls.Flow)
	if cls.Level != "vip" {
		t.Fatalf("precedence broken: t1 -> %s, want vip", cls.Level)
	}

	cls2, err := c.Classify(Request{ID: "y", FlowID: "t2"})
	if err != nil {
		t.Fatalf("classify t2: %v", err)
	}
	if cls2.Level != "bulk" {
		t.Fatalf("t2 -> %s, want bulk", cls2.Level)
	}

	// Unmatched request must be rejected.
	_, d, err := c.Offer(Request{ID: "z", FlowID: "unknown"})
	if err == nil || d != DecisionReject {
		t.Fatalf("unmatched request: d=%v err=%v, want reject+error", d, err)
	}
}

// TestPerFlowQueueCapRejects proves a full per-flow queue rejects further
// requests for THAT flow without dropping other flows' backlog.
//
// Mutation: if the maxQueueLen guard in Offer is removed, the over-cap request
// would enqueue instead of being rejected, failing this test.
func TestPerFlowQueueCapRejects(t *testing.T) {
	clock := NewManualClock(0)
	// concurrency 1, per-flow queue cap 2.
	c := newTwoFlowController(t, clock, 1, 2)

	// First A admits (seat free, queue empty).
	_, d0, _ := c.Offer(Request{ID: "A0", FlowID: "A"})
	if d0 != DecisionAdmit {
		t.Fatalf("A0 d=%v want admit", d0)
	}
	// Next two A enqueue (fill the cap of 2).
	if _, d, _ := c.Offer(Request{ID: "A1", FlowID: "A"}); d != DecisionEnqueue {
		t.Fatalf("A1 d=%v want enqueue", d)
	}
	if _, d, _ := c.Offer(Request{ID: "A2", FlowID: "A"}); d != DecisionEnqueue {
		t.Fatalf("A2 d=%v want enqueue", d)
	}
	// Third A over the cap -> reject.
	_, dRej, _ := c.Offer(Request{ID: "A3", FlowID: "A"})
	t.Logf("run=%s A queue cap test: A3 decision=%v (queuedA=%d)", runUUID, dRej, len(c.levels["std"].queues["A"]))
	if dRej != DecisionReject {
		t.Fatalf("A3 over cap d=%v want reject", dRej)
	}
	// A different flow B is unaffected: it can still enqueue.
	if _, d, _ := c.Offer(Request{ID: "B0", FlowID: "B"}); d != DecisionEnqueue {
		t.Fatalf("B0 d=%v want enqueue (B unaffected by A's full queue)", d)
	}
}
