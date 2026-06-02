package thermalwarm

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"
)

func runUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// anchor is a fixed, deterministic clock anchor (no time.Now() in logic).
var anchor = time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

// TestPreWarmEnabled_MovesColdStartOffRequestPath proves CLOSURE clause 1:
// with pre-warm ENABLED, the backend reaches Hot BEFORE the first real request,
// and that request pays zero request-time cold-start.
//
// Mutation: if PreWarm is made a no-op (state stays Cold), the post-advance
// State assertion (want Hot) fails AND the Dispatch ServedHot/ColdStart==0
// assertions fail (the request would itself pay coldStart). If the warm-up never
// expires, State stays Warming and the Hot assertion fails.
func TestPreWarmEnabled_MovesColdStartOffRequestPath(t *testing.T) {
	id := runUUID(t)
	const coldStart = 800 * time.Millisecond

	clk := NewManualClock(anchor)
	c := NewController(clk, coldStart)
	c.Register("be-A")

	// before: Cold.
	got := c.State("be-A")
	t.Logf("run=%s before: backend=be-A state=%s clock=%s", id, got, clk.Now().Format(time.RFC3339Nano))
	if got != Cold {
		t.Fatalf("before pre-warm: state=%s want Cold", got)
	}

	// Trigger pre-warm BEFORE any real request.
	c.PreWarm("be-A")
	if s := c.State("be-A"); s != Warming {
		t.Fatalf("immediately after PreWarm: state=%s want Warming", s)
	}

	// Advance the clock past the cold-start. Derive the advance from coldStart
	// itself (one tick exactly to the boundary) — not a magic literal.
	c.Advance(coldStart)

	// after: Hot, reached BEFORE the first real request is dispatched.
	afterState := c.State("be-A")
	t.Logf("run=%s after-warm: backend=be-A state=%s clock=%s", id, afterState, clk.Now().Format(time.RFC3339Nano))
	if afterState != Hot {
		t.Fatalf("after advancing %s past cold-start: state=%s want Hot", coldStart, afterState)
	}

	// The clock at request time, captured to prove the request pays nothing.
	reqTime := clk.Now()
	res := c.Dispatch("be-A")
	t.Logf("run=%s dispatch: servedHot=%v reqColdStart=%s finalState=%s clockUnchanged=%v",
		id, res.ServedHot, res.ColdStart, res.FinalState, clk.Now().Equal(reqTime))

	if !res.ServedHot {
		t.Fatalf("pre-warmed request: servedHot=false want true (request hit a cold backend)")
	}
	if res.ColdStart != 0 {
		t.Fatalf("pre-warmed request: request-time coldStart=%s want 0 (latency not moved off path)", res.ColdStart)
	}
	// The dispatch must NOT advance the clock, since no warm-up happens on-path.
	if !clk.Now().Equal(reqTime) {
		t.Fatalf("pre-warmed dispatch advanced the clock by %s; expected 0 on-path cold-start",
			clk.Now().Sub(reqTime))
	}
	// The measured (off-path) cold-start equals the configured duration.
	if c.ColdStartDuration() != coldStart {
		t.Fatalf("measured cold-start=%s want %s", c.ColdStartDuration(), coldStart)
	}
}

// TestPreWarmDisabled_RequestPaysColdStart proves CLOSURE clause 2:
// with pre-warm DISABLED, the first request to a Cold backend incurs the full
// cold-start and observes the Cold->Hot transition on the request path.
//
// Mutation: if Dispatch on a Cold backend wrongly reported ServedHot or charged
// zero cold-start, these assertions fail. Derives expected charge from coldStart.
func TestPreWarmDisabled_RequestPaysColdStart(t *testing.T) {
	id := runUUID(t)
	const coldStart = 1250 * time.Millisecond

	clk := NewManualClock(anchor)
	c := NewController(clk, coldStart)
	c.Register("be-B")

	before := c.State("be-B")
	startClock := clk.Now()
	t.Logf("run=%s before: backend=be-B state=%s clock=%s", id, before, startClock.Format(time.RFC3339Nano))
	if before != Cold {
		t.Fatalf("before dispatch (no pre-warm): state=%s want Cold", before)
	}

	// No PreWarm call: the request itself must pay the cold-start.
	res := c.Dispatch("be-B")
	advanced := clk.Now().Sub(startClock)
	t.Logf("run=%s after-dispatch: servedHot=%v reqColdStart=%s finalState=%s clockAdvancedBy=%s",
		id, res.ServedHot, res.ColdStart, res.FinalState, advanced)

	if res.ServedHot {
		t.Fatalf("cold dispatch: servedHot=true want false (it must observe the cold-start)")
	}
	if res.ColdStart != coldStart {
		t.Fatalf("cold dispatch: request-time coldStart=%s want %s (full cold-start on request path)",
			res.ColdStart, coldStart)
	}
	// The on-path warm-up must have advanced the clock by exactly coldStart,
	// proving the latency landed on the request (not moved off path).
	if advanced != coldStart {
		t.Fatalf("cold dispatch advanced clock by %s want %s", advanced, coldStart)
	}
	if res.FinalState != Hot {
		t.Fatalf("after cold dispatch: finalState=%s want Hot", res.FinalState)
	}

	// Contrast with clause 1: here the request DID pay; the pre-warmed path did not.
	if res.ColdStart <= 0 {
		t.Fatalf("disabled path must charge a positive cold-start, got %s", res.ColdStart)
	}
}

// TestAlreadyHot_ServesImmediately proves CLOSURE clause 3:
// an already-Hot backend serves immediately with no additional cold-start.
//
// Mutation: if Dispatch charged any cold-start to a Hot backend, or advanced the
// clock, these assertions fail.
func TestAlreadyHot_ServesImmediately(t *testing.T) {
	id := runUUID(t)
	const coldStart = 500 * time.Millisecond

	clk := NewManualClock(anchor)
	c := NewController(clk, coldStart)
	c.Register("be-C")

	// Warm it to Hot via pre-warm + advance (off-path).
	c.PreWarm("be-C")
	c.Advance(coldStart)
	if s := c.State("be-C"); s != Hot {
		t.Fatalf("setup: state=%s want Hot", s)
	}

	hotClock := clk.Now()
	t.Logf("run=%s before second dispatch: state=%s clock=%s", id, c.State("be-C"), hotClock.Format(time.RFC3339Nano))

	res := c.Dispatch("be-C")
	t.Logf("run=%s second dispatch: servedHot=%v coldStart=%s clockAdvancedBy=%s",
		id, res.ServedHot, res.ColdStart, clk.Now().Sub(hotClock))

	if !res.ServedHot {
		t.Fatalf("already-Hot dispatch: servedHot=false want true")
	}
	if res.ColdStart != 0 {
		t.Fatalf("already-Hot dispatch: coldStart=%s want 0 (no additional cold-start)", res.ColdStart)
	}
	if !clk.Now().Equal(hotClock) {
		t.Fatalf("already-Hot dispatch advanced clock by %s want 0", clk.Now().Sub(hotClock))
	}
}

// TestWarmUpNotYetExpired_StillWarming guards the "never expiring" mutation from
// the opposite side: before the cold-start elapses the backend must still read
// Warming (not prematurely Hot), and dispatching mid-warm pays only the REMAINDER.
//
// Mutation: if currentState returned Hot before the boundary, the Warming
// assertion fails; if Dispatch charged the full coldStart instead of the
// remainder, the remainder assertion fails.
func TestWarmUpNotYetExpired_StillWarming(t *testing.T) {
	id := runUUID(t)
	const coldStart = 1000 * time.Millisecond
	const partial = 300 * time.Millisecond // elapsed before request
	const remainder = coldStart - partial

	clk := NewManualClock(anchor)
	c := NewController(clk, coldStart)
	c.Register("be-D")

	c.PreWarm("be-D")
	c.Advance(partial) // less than coldStart

	mid := c.State("be-D")
	t.Logf("run=%s mid-warm: state=%s elapsed=%s", id, mid, partial)
	if mid != Warming {
		t.Fatalf("at elapsed=%s (< coldStart=%s): state=%s want Warming", partial, coldStart, mid)
	}

	start := clk.Now()
	res := c.Dispatch("be-D")
	advanced := clk.Now().Sub(start)
	t.Logf("run=%s mid-warm dispatch: servedHot=%v coldStart=%s remainderExpected=%s",
		id, res.ServedHot, res.ColdStart, remainder)

	if res.ServedHot {
		t.Fatalf("mid-warm dispatch: servedHot=true want false")
	}
	if res.ColdStart != remainder {
		t.Fatalf("mid-warm dispatch: coldStart=%s want remainder %s", res.ColdStart, remainder)
	}
	if advanced != remainder {
		t.Fatalf("mid-warm dispatch advanced clock by %s want %s", advanced, remainder)
	}
	if res.FinalState != Hot {
		t.Fatalf("mid-warm dispatch finalState=%s want Hot", res.FinalState)
	}
}
