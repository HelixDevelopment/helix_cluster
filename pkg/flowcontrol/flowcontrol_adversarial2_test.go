package flowcontrol

// ---------------------------------------------------------------------------
// Adversarial suite #2 for pkg/flowcontrol — SEAT-CONSERVATION under a
// duplicate live request ID.
//
// Prior pass NOTED a LATENT RISK: "duplicate-live-ID seat leak (doc-forbidden)".
// This file DETERMINES whether that is an ALWAYS-ON bug reachable through the
// PUBLIC API (Offer/Finish), not merely a doc-forbidden corner.
//
// Core invariant under attack (CONSERVATION):
//
//	inFlight == len(seats)                      ...(I1) seat-set is the truth
//	for every ID admitted and later Finish-ed, the net seat change is 0  ...(I2)
//	a level's usable capacity never permanently shrinks below `concurrency`
//	after every admitted request has been finished                       ...(I3)
//
// Root-cause sketch (production code):
//
//	start():  lvl.inFlight++ ; lvl.seats[r.ID] = flow   // map ASSIGN, not add
//	Finish(): delete(seats,id) ; lvl.inFlight--          // one delete, one dec
//
// `seats` is keyed by request ID. Admitting the SAME ID twice increments
// inFlight twice but leaves only ONE seat-map entry (the second overwrites the
// first). The single Finish that the single key permits decrements inFlight
// once; the second decrement is unreachable (ErrUnknownRequest). Net effect:
// inFlight is pinned +1 above the true held-seat count forever -> capacity
// shrinks by one per duplicate -> eventual starvation/deadlock.
//
// Offer() admits-now whenever (inFlight < concurrency && totalQueued == 0); it
// does NOT reject/short-circuit an already-live ID. So the duplicate double
// admit is reachable through the ordinary public API with concurrency >= 2.
//
// All assertions are SINK-SIDE: real inFlight, real len(seats), real residual
// admit capacity probed by a fresh Offer — never "did not panic".
// ---------------------------------------------------------------------------

import (
	"fmt"
	"testing"
)

const adv2RunUUID = "fc-adv2-2f8a61d4-hxc-flowctl-seatconservation"

// liveSeatCount returns the ground-truth number of held seats for a level.
func liveSeatCount(c *Controller, level string) int {
	return len(c.levels[level].seats)
}

// TestAdv2_DuplicateLiveID_DoubleAdmit_ConservesSeats is the load-bearing
// probe. It admits the SAME live ID twice through the public Offer() API, then
// finishes the request, and asserts CONSERVATION: once every distinct admitted
// request has been released, the level is fully drained and usable capacity is
// restored to `concurrency`.
//
// On BUGGY code (duplicate double-admit leaks a seat) this FAILS: after the
// only Finish the level reports inFlight=1 with zero held seats, capacity is
// permanently 1 short, and the leaked seat eventually blocks a real request.
//
// On FIXED code (Offer rejects/no-ops a duplicate live ID, OR start is
// seat-accurate) this PASSES: the second Offer does NOT create a phantom seat,
// so after Finish the level drains to 0 and full capacity returns.
func TestAdv2_DuplicateLiveID_DoubleAdmit_ConservesSeats(t *testing.T) {
	t.Logf("run=%s", adv2RunUUID)
	clk := NewManualClock(0)
	// Concurrency 2, empty queue => both Offers of the same ID hit admit-now.
	c := newAdvController(t, clk, 2, 8)

	// First admit of "dup": seat free, queue empty -> admit-now.
	if _, d, err := c.Offer(Request{ID: "dup", FlowID: "f"}); err != nil || d != DecisionAdmit {
		t.Fatalf("first Offer(dup): decision=%v err=%v want admit", d, err)
	}
	// Second admit of the SAME live ID. A seat is still free (cap 2) and the
	// queue is empty, so the admit-now branch is taken again on BUGGY code,
	// inflating inFlight without adding a distinct seat. The CORRECT contract is
	// to refuse the duplicate (Request.ID must be unique among live requests):
	// either a DecisionReject/ErrDuplicateID, or a seat-accurate no-op. Either
	// way CONSERVATION must hold — that is what this probe enforces sink-side.
	_, d2, err2 := c.Offer(Request{ID: "dup", FlowID: "f"})
	if d2 == DecisionReject {
		// Correct: duplicate refused. A non-nil typed error is expected here.
		if err2 == nil {
			t.Fatalf("duplicate live ID rejected but err=nil; want a typed duplicate-id error")
		}
	} else if err2 != nil {
		t.Fatalf("second Offer(dup): decision=%v unexpected err=%v", d2, err2)
	}

	// (I1) The seat-set is the source of truth: inFlight MUST equal len(seats).
	// A duplicate that bumped inFlight without adding a distinct seat breaks this
	// (BUGGY code: inFlight=2, len(seats)=1). This is the load-bearing assertion.
	if inflight, seats := c.InFlight("std"), liveSeatCount(c, "std"); inflight != seats {
		t.Fatalf("CONSERVATION (I1) broken after duplicate admit: inFlight=%d len(seats)=%d "+
			"(decision2=%v); a phantom seat was created by the duplicate-live-ID admit",
			inflight, seats, d2)
	}
	// Exactly ONE distinct request is live regardless of the duplicate outcome.
	if got := c.InFlight("std"); got != 1 {
		t.Fatalf("inFlight=%d after one distinct ID offered twice; want 1 "+
			"(duplicate admit inflated in-flight)", got)
	}

	// Release every distinct live seat (drainAll snapshots seat IDs, so the
	// single "dup" key is finished exactly once — exactly what a correct caller
	// can do, since the ID is the only handle Finish accepts).
	drainAll(t, c, "std")

	// (I3) After draining, the level MUST be empty and at full capacity. A leaked
	// seat shows up here as residual inFlight that no Finish can ever clear.
	if got := c.InFlight("std"); got != 0 {
		t.Fatalf("SEAT LEAK (I3): after finishing every live seat, inFlight=%d want 0 "+
			"-> capacity permanently shrunk by %d; duplicate-live-ID admit leaked a seat", got, got)
	}
	if got := liveSeatCount(c, "std"); got != 0 {
		t.Fatalf("after drain len(seats)=%d want 0", got)
	}

	// Sink-side proof that usable capacity is truly restored: with cap 2 and an
	// empty level, two fresh distinct requests must BOTH admit-now.
	if _, d, err := c.Offer(Request{ID: "n0", FlowID: "f"}); err != nil || d != DecisionAdmit {
		t.Fatalf("post-drain Offer(n0): decision=%v err=%v want admit (capacity not restored)", d, err)
	}
	if _, d, err := c.Offer(Request{ID: "n1", FlowID: "f"}); err != nil || d != DecisionAdmit {
		t.Fatalf("post-drain Offer(n1): decision=%v err=%v want admit "+
			"(leaked seat consumed real capacity)", d, err)
	}
}

// TestAdv2_DuplicateLiveID_DoesNotInflateInFlight asserts the narrower,
// always-on accounting fact directly: a second Offer of a live ID must not push
// inFlight above the number of DISTINCT live requests. With cap 3 and one
// distinct live ID, inFlight must be 1 regardless of how many times that same
// ID is offered.
func TestAdv2_DuplicateLiveID_DoesNotInflateInFlight(t *testing.T) {
	t.Logf("run=%s", adv2RunUUID)
	c := newAdvController(t, NewManualClock(0), 3, 8)

	for i := 0; i < 3; i++ {
		// First Offer admits; the next two are duplicates of a LIVE id. The
		// correct contract refuses them (DecisionReject); the buggy code admits
		// them and inflates inFlight past the seat-set size. We tolerate either
		// decision but enforce CONSERVATION (I1) after every Offer.
		_, d, err := c.Offer(Request{ID: "same", FlowID: "f"})
		if i == 0 && (err != nil || d != DecisionAdmit) {
			t.Fatalf("first Offer(same): decision=%v err=%v want admit", d, err)
		}
		if inflight, seats := c.InFlight("std"), liveSeatCount(c, "std"); inflight != seats {
			t.Fatalf("after Offer #%d: inFlight=%d != len(seats)=%d (phantom seat)", i, inflight, seats)
		}
	}
	// Exactly ONE distinct request is live; inFlight must reflect that.
	if got := c.InFlight("std"); got != 1 {
		t.Fatalf("inFlight=%d after offering one ID three times; want 1 "+
			"(each duplicate admit inflated the in-flight count)", got)
	}
}

// TestAdv2_UniqueIDs_ConserveSeats is the CONTROL: the same drain/refill
// protocol over DISTINCT IDs must conserve seats on both buggy and fixed code.
// It guards against the fix (or the probe) over-rejecting legitimate traffic:
// distinct IDs must always admit, drain, and fully restore capacity.
func TestAdv2_UniqueIDs_ConserveSeats(t *testing.T) {
	t.Logf("run=%s", adv2RunUUID)
	c := newAdvController(t, NewManualClock(0), 4, 16)

	const n = 12
	admitted := 0
	for i := 0; i < n; i++ {
		_, d, err := c.Offer(Request{ID: fmt.Sprintf("u%d", i), FlowID: "f"})
		if err != nil {
			t.Fatalf("Offer u%d: err=%v", i, err)
		}
		if d == DecisionAdmit {
			admitted++
		}
		// (I1) holds after every single mutation.
		if inflight, seats := c.InFlight("std"), liveSeatCount(c, "std"); inflight != seats {
			t.Fatalf("after Offer u%d: inFlight=%d != len(seats)=%d", i, inflight, seats)
		}
		if c.InFlight("std") > 4 {
			t.Fatalf("over-admission: inFlight=%d > cap 4", c.InFlight("std"))
		}
	}
	if admitted != 4 {
		t.Fatalf("admitted-now=%d want 4 (cap)", admitted)
	}

	// Drain + tick to completion; bounded so a leak can never hang the suite.
	for round := 0; round < n+8; round++ {
		drainAll(t, c, "std")
		c.Tick()
		if c.InFlight("std") == 0 && c.QueuedTotal("std") == 0 {
			break
		}
	}
	if got := c.InFlight("std"); got != 0 {
		t.Fatalf("control: after full drain inFlight=%d want 0", got)
	}
	if got := liveSeatCount(c, "std"); got != 0 {
		t.Fatalf("control: after full drain len(seats)=%d want 0", got)
	}
	// Capacity fully restored: 4 fresh distinct requests admit-now.
	for i := 0; i < 4; i++ {
		if _, d, err := c.Offer(Request{ID: fmt.Sprintf("post%d", i), FlowID: "f"}); err != nil || d != DecisionAdmit {
			t.Fatalf("post-drain Offer post%d: decision=%v err=%v want admit", i, d, err)
		}
	}
}
