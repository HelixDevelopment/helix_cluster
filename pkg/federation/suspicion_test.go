package federation

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/swim"
)

// fakeRelay is the sink-side test double.  It records every SuspicionEvent
// that reaches Forward so tests can assert exact receipt.  It is safe for
// concurrent use.
//
// Mutation anchor: if the production code omits the relay.Forward call
// entirely, every test that asserts len(r.Received()) > 0 fails.
type fakeRelay struct {
	mu       sync.Mutex
	received []SuspicionEvent
	err      error // if non-nil, Forward returns this error
}

func (r *fakeRelay) Forward(_ context.Context, ev SuspicionEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.received = append(r.received, ev)
	return r.err
}

// Received returns a copy of all events the relay has seen so far.
func (r *fakeRelay) Received() []SuspicionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SuspicionEvent, len(r.received))
	copy(out, r.received)
	return out
}

// Count returns the number of forwarded events.
func (r *fakeRelay) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.received)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func newCSS(t *testing.T, relay GatewayRelay) *CrossCellSuspicion {
	t.Helper()
	return NewCrossCellSuspicion("local-cell", relay)
}

func mustObserve(t *testing.T, css *CrossCellSuspicion, ev SuspicionEvent) (changed bool) {
	t.Helper()
	changed, err := css.Observe(context.Background(), ev)
	if err != nil {
		t.Fatalf("Observe(%+v): unexpected error: %v", ev, err)
	}
	return changed
}

// ─── TestHigherIncarnationAliveRefutesSuspect ─────────────────────────────────
// Scenario: Suspect(inc=1) then Alive(inc=2).
// Expected: final state == Alive, fake relay recorded BOTH forwards.
//
// Mutation that makes this fail:
//
//	Change "ev.Incarnation < entry.incarnation → no-op" to always accept →
//	the inverse test (TestStaleAliveDoesNotRefute) then demonstrates the flip
//	problem; but concretely for THIS test, removing the strict incarnation
//	advance check and keeping equal-inc blocking would still pass, so the
//	paired mutation is on the EQUAL branch: change "ev.State <= entry.state"
//	to "ev.State < entry.state" so equal-state no-ops still work but the
//	regression test below breaks — proven in TestEqualIncarnationRegressionBlocked.
//
//	The correct targeted mutation for THIS test: in the lower-incarnation branch
//	change "ev.Incarnation < entry.incarnation" to "ev.Incarnation <= entry.incarnation"
//	so inc=2 Alive is rejected when entry.incarnation==1 … wait, that makes inc=2>1
//	still pass. True mutation: swap the comparison to ">" so lower inc is accepted
//	instead of higher. Then Alive(inc=2) is rejected and state stays Suspect. Fails.
func TestHigherIncarnationAliveRefutesSuspect(t *testing.T) {
	relay := &fakeRelay{}
	css := newCSS(t, relay)

	evSuspect := SuspicionEvent{CellID: "cell-A", MemberID: "node-1", State: swim.StateSuspect, Incarnation: 1}
	evAlive := SuspicionEvent{CellID: "cell-A", MemberID: "node-1", State: swim.StateAlive, Incarnation: 2}

	changedS := mustObserve(t, css, evSuspect)
	changedA := mustObserve(t, css, evAlive)

	if !changedS {
		t.Error("Suspect(inc=1): want changed=true, got false")
	}
	if !changedA {
		t.Error("Alive(inc=2): want changed=true (refutation), got false")
	}

	// Sink-side: assert exact events received by the relay.
	got := relay.Received()
	if len(got) != 2 {
		t.Fatalf("relay received %d events, want 2", len(got))
	}
	if got[0] != evSuspect {
		t.Errorf("relay[0] = %+v, want %+v", got[0], evSuspect)
	}
	if got[1] != evAlive {
		t.Errorf("relay[1] = %+v, want %+v", got[1], evAlive)
	}

	// Final persisted state must be Alive at incarnation 2.
	st, inc, known := css.State("cell-A", "node-1")
	if !known {
		t.Fatal("State: want known=true")
	}
	if st != swim.StateAlive {
		t.Errorf("final state = %v, want StateAlive", st)
	}
	if inc != 2 {
		t.Errorf("final incarnation = %d, want 2", inc)
	}
}

// ─── TestStaleAliveDoesNotRefute ──────────────────────────────────────────────
// Scenario: Suspect(inc=2) then Alive(inc=1) — stale Alive must NOT override.
// Expected: state stays Suspect(inc=2), relay records only ONE forward.
//
// Mutation that makes this fail:
//
//	Remove the "ev.Incarnation < entry.incarnation → no-op" guard so the
//	lower-incarnation Alive is accepted → state becomes Alive and the test's
//	"st != StateSuspect" assertion fires.
func TestStaleAliveDoesNotRefute(t *testing.T) {
	relay := &fakeRelay{}
	css := newCSS(t, relay)

	evSuspect := SuspicionEvent{CellID: "cell-B", MemberID: "node-2", State: swim.StateSuspect, Incarnation: 2}
	evStaleAlive := SuspicionEvent{CellID: "cell-B", MemberID: "node-2", State: swim.StateAlive, Incarnation: 1}

	mustObserve(t, css, evSuspect)
	changedStale, err := css.Observe(context.Background(), evStaleAlive)
	if err != nil {
		t.Fatalf("Observe stale Alive: unexpected error: %v", err)
	}
	if changedStale {
		t.Error("stale Alive(inc=1) after Suspect(inc=2): want changed=false")
	}

	// Relay must have received exactly the Suspect event.
	if relay.Count() != 1 {
		t.Fatalf("relay count = %d, want 1", relay.Count())
	}
	if relay.Received()[0] != evSuspect {
		t.Errorf("relay[0] = %+v, want %+v", relay.Received()[0], evSuspect)
	}

	st, inc, known := css.State("cell-B", "node-2")
	if !known {
		t.Fatal("State: want known=true")
	}
	if st != swim.StateSuspect {
		t.Errorf("final state = %v, want StateSuspect", st)
	}
	if inc != 2 {
		t.Errorf("final incarnation = %d, want 2", inc)
	}
}

// ─── TestEqualIncarnationRegressionBlocked ────────────────────────────────────
// Scenario: Dead(inc=1) then Alive(inc=1) — equal-incarnation regression.
// Expected: state stays Dead, relay records NO forward for the no-op.
//
// Mutation that makes this fail:
//
//	Change "ev.State <= entry.state" to "ev.State < entry.state" so an
//	equal-state re-observation is forwarded again; that breaks
//	TestNoOpDoesNotForward. For THIS test change to "ev.State != entry.state"
//	so equal-inc Alive (0) < Dead (2) is accepted → state regresses to Alive
//	and the "st != StateDead" assertion fires.
func TestEqualIncarnationRegressionBlocked(t *testing.T) {
	relay := &fakeRelay{}
	css := newCSS(t, relay)

	evDead := SuspicionEvent{CellID: "cell-C", MemberID: "node-3", State: swim.StateDead, Incarnation: 1}
	evAlive := SuspicionEvent{CellID: "cell-C", MemberID: "node-3", State: swim.StateAlive, Incarnation: 1}

	mustObserve(t, css, evDead)
	changedAlive, err := css.Observe(context.Background(), evAlive)
	if err != nil {
		t.Fatalf("Observe equal-inc Alive: unexpected error: %v", err)
	}
	if changedAlive {
		t.Error("Alive(inc=1) after Dead(inc=1): equal-inc regression must be no-op, want changed=false")
	}

	// Relay must have exactly one entry: the Dead event only.
	if relay.Count() != 1 {
		t.Fatalf("relay count = %d, want 1 (only Dead forwarded)", relay.Count())
	}
	if relay.Received()[0] != evDead {
		t.Errorf("relay[0] = %+v, want %+v", relay.Received()[0], evDead)
	}

	st, _, _ := css.State("cell-C", "node-3")
	if st != swim.StateDead {
		t.Errorf("final state = %v, want StateDead", st)
	}
}

// ─── TestNoOpDoesNotForward ───────────────────────────────────────────────────
// Scenario: Suspect(inc=1) twice — idempotent re-observation.
// Expected: second Observe returns changed=false, relay count stays 1.
//
// Mutation that makes this fail:
//
//	Move the relay.Forward call (suspicion.go line 144) to before the
//	early-return guards (lines 126–135), so it executes unconditionally on
//	every call regardless of no-op status → relay.Count() becomes 2 on the
//	duplicate Observe and the "!= 1" assertion fires.
func TestNoOpDoesNotForward(t *testing.T) {
	relay := &fakeRelay{}
	css := newCSS(t, relay)

	ev := SuspicionEvent{CellID: "cell-D", MemberID: "node-4", State: swim.StateSuspect, Incarnation: 1}

	mustObserve(t, css, ev)

	changed2, err := css.Observe(context.Background(), ev)
	if err != nil {
		t.Fatalf("second Observe: unexpected error: %v", err)
	}
	if changed2 {
		t.Error("duplicate Observe: want changed=false")
	}
	if relay.Count() != 1 {
		t.Errorf("relay count = %d, want 1 (second no-op must not forward)", relay.Count())
	}

	// Confirm the one recorded event is the exact SuspicionEvent.
	got := relay.Received()[0]
	if got != ev {
		t.Errorf("relay[0] = %+v, want %+v", got, ev)
	}
}

// ─── TestSinkSideExactEventFields ─────────────────────────────────────────────
// Proves that the relay receives the EXACT SuspicionEvent (all four fields).
//
// Mutation that makes this fail:
//
//	Make Forward a no-op (remove the relay.Forward call from Observe) →
//	relay.Count()==0 and the length assertion fires immediately.
func TestSinkSideExactEventFields(t *testing.T) {
	relay := &fakeRelay{}
	css := newCSS(t, relay)

	ev := SuspicionEvent{
		CellID:      "cell-E",
		MemberID:    "node-5",
		State:       swim.StateSuspect,
		Incarnation: 7,
	}

	changed := mustObserve(t, css, ev)
	if !changed {
		t.Fatal("Observe: want changed=true for first observation")
	}

	if relay.Count() != 1 {
		t.Fatalf("relay received %d events, want 1", relay.Count())
	}
	got := relay.Received()[0]
	if got.CellID != ev.CellID {
		t.Errorf("CellID = %q, want %q", got.CellID, ev.CellID)
	}
	if got.MemberID != ev.MemberID {
		t.Errorf("MemberID = %q, want %q", got.MemberID, ev.MemberID)
	}
	if got.State != ev.State {
		t.Errorf("State = %v, want %v", got.State, ev.State)
	}
	if got.Incarnation != ev.Incarnation {
		t.Errorf("Incarnation = %d, want %d", got.Incarnation, ev.Incarnation)
	}
}

// ─── TestRelayErrorSurfaced ───────────────────────────────────────────────────
// When the relay returns an error, Observe returns (true, err) — the local
// state IS updated and changed=true, but the transport failure is reported.
//
// Mutation that makes this fail:
//
//	Swallow the relay error (always return nil) → err==nil and the
//	errors.Is assertion fails.
func TestRelayErrorSurfaced(t *testing.T) {
	sentinel := errors.New("gateway timeout")
	relay := &fakeRelay{err: sentinel}
	css := newCSS(t, relay)

	ev := SuspicionEvent{CellID: "cell-F", MemberID: "node-6", State: swim.StateSuspect, Incarnation: 1}

	changed, err := css.Observe(context.Background(), ev)
	if !changed {
		t.Error("want changed=true even when relay errors")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel %v", err, sentinel)
	}

	// Local state must still be committed.
	st, inc, known := css.State("cell-F", "node-6")
	if !known {
		t.Fatal("State: want known=true after relay error")
	}
	if st != swim.StateSuspect || inc != 1 {
		t.Errorf("state = %v inc=%d, want Suspect/1", st, inc)
	}
}

// ─── TestUnknownMemberReturnsNotKnown ─────────────────────────────────────────
// State for an unseen (cell, member) pair returns known=false.
//
// Mutation that makes this fail:
//
//	Return known=true with zero values from State → the "!known" assertion fires.
func TestUnknownMemberReturnsNotKnown(t *testing.T) {
	relay := &fakeRelay{}
	css := newCSS(t, relay)

	_, _, known := css.State("never-seen", "ghost-node")
	if known {
		t.Error("State on unseen member: want known=false")
	}
}

// ─── TestMultipleCellsIsolated ─────────────────────────────────────────────────
// Two different cells can each have the same memberID at different states
// without interference.
//
// Mutation that makes this fail:
//
//	Use memberID as the sole map key (ignoring cellID) so cell-G's node-7
//	overwrites cell-H's node-7 → the State lookup for cell-H returns cell-G's
//	value and the equality check fires.
func TestMultipleCellsIsolated(t *testing.T) {
	relay := &fakeRelay{}
	css := newCSS(t, relay)

	evG := SuspicionEvent{CellID: "cell-G", MemberID: "node-7", State: swim.StateSuspect, Incarnation: 1}
	evH := SuspicionEvent{CellID: "cell-H", MemberID: "node-7", State: swim.StateDead, Incarnation: 3}

	mustObserve(t, css, evG)
	mustObserve(t, css, evH)

	stG, incG, _ := css.State("cell-G", "node-7")
	stH, incH, _ := css.State("cell-H", "node-7")

	if stG != swim.StateSuspect || incG != 1 {
		t.Errorf("cell-G/node-7: state=%v inc=%d, want Suspect/1", stG, incG)
	}
	if stH != swim.StateDead || incH != 3 {
		t.Errorf("cell-H/node-7: state=%v inc=%d, want Dead/3", stH, incH)
	}
	if relay.Count() != 2 {
		t.Errorf("relay count = %d, want 2", relay.Count())
	}
}

// ─── TestEqualIncarnationAdvanceAccepted ──────────────────────────────────────
// Equal incarnation with a HIGHER state value must be accepted (Alive→Suspect
// is an advance even at equal incarnation).
//
// Mutation that makes this fail:
//
//	Change "ev.State <= entry.state" to "ev.State < entry.state" (strict less)
//	so equal-state no-ops are not blocked; this test continues to pass, but
//	TestNoOpDoesNotForward would also need to break. The correct specific
//	mutation for THIS test: change to "ev.State != entry.state" so the equal-
//	incarnation advance (Alive→Suspect) is mistakenly treated as a no-op →
//	changed=false and the assertion fires. Actually the cleanest targeted
//	mutation: use "ev.State < entry.state" AND accept equal-state — that makes
//	Alive(0) < Suspect(1) so it IS accepted (no regression), but Dead(2) →
//	Alive(0) equal-inc regression test fails. Simplest single-line mutation
//	for THIS test: drop the equal-incarnation branch entirely so all equal-inc
//	events are forwarded → TestNoOpDoesNotForward fails (count becomes 2).
//	For strict isolation: flip the condition to "ev.State >= entry.state" so
//	an advance is blocked → changed=false here.
func TestEqualIncarnationAdvanceAccepted(t *testing.T) {
	relay := &fakeRelay{}
	css := newCSS(t, relay)

	evAlive := SuspicionEvent{CellID: "cell-I", MemberID: "node-8", State: swim.StateAlive, Incarnation: 5}
	evSuspect := SuspicionEvent{CellID: "cell-I", MemberID: "node-8", State: swim.StateSuspect, Incarnation: 5}

	mustObserve(t, css, evAlive)

	changed := mustObserve(t, css, evSuspect)
	if !changed {
		t.Error("Suspect(inc=5) after Alive(inc=5): equal-inc advance must be accepted, want changed=true")
	}
	if relay.Count() != 2 {
		t.Errorf("relay count = %d, want 2", relay.Count())
	}

	st, inc, _ := css.State("cell-I", "node-8")
	if st != swim.StateSuspect {
		t.Errorf("final state = %v, want StateSuspect", st)
	}
	if inc != 5 {
		t.Errorf("final incarnation = %d, want 5", inc)
	}
}

// ─── TestInvalidEventRejected ─────────────────────────────────────────────────
// Events with empty CellID or MemberID are rejected with an error.
//
// Mutation that makes this fail:
//
//	Remove the validation block at the top of Observe → err==nil and
//	the "err == nil" assertion fires.
func TestInvalidEventRejected(t *testing.T) {
	cases := []struct {
		name string
		ev   SuspicionEvent
	}{
		{"empty CellID", SuspicionEvent{CellID: "", MemberID: "x", State: swim.StateAlive, Incarnation: 1}},
		{"empty MemberID", SuspicionEvent{CellID: "c", MemberID: "", State: swim.StateAlive, Incarnation: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relay := &fakeRelay{}
			css := newCSS(t, relay)
			changed, err := css.Observe(context.Background(), tc.ev)
			if err == nil {
				t.Error("want error for invalid event, got nil")
			}
			if changed {
				t.Error("want changed=false for invalid event")
			}
			if relay.Count() != 0 {
				t.Error("relay must not be called for invalid event")
			}
		})
	}
}

// ─── TestMemberStatusAliasMatchesSwimConstants ───────────────────────────────
// Ensures MemberStatus is a transparent alias, not a redefinition, and that
// the swim iota ordering satisfies Alive < Suspect < Dead < Left (which the
// equal-incarnation advance logic in CrossCellSuspicion.Observe depends on).
//
// The compile-time assignments below (lines 453-456) are the real protection
// against MemberStatus being redefined as a distinct type — a type mismatch
// would fail to compile.  The ordering assertions guard against someone
// silently re-numbering the swim.MemberState iota, which would make
// "state may only advance" semantics invert or break.
//
// Mutation that makes this fail:
//
//	Swap swim.StateAlive and swim.StateSuspect in the iota (making Alive=1,
//	Suspect=0) → int(swim.StateAlive) < int(swim.StateSuspect) becomes false
//	and the first ordering assertion fires.
func TestMemberStatusAliasMatchesSwimConstants(t *testing.T) {
	// Compile-time alias transparency check: these assignments do not compile
	// if MemberStatus is ever changed from "= swim.MemberState" to a new type.
	var _ MemberStatus = swim.StateAlive
	var _ MemberStatus = swim.StateSuspect
	var _ MemberStatus = swim.StateDead
	var _ MemberStatus = swim.StateLeft

	// Runtime ordering check: CrossCellSuspicion.Observe's "state may only
	// advance" logic requires the strict ordering Alive < Suspect < Dead < Left.
	// These assertions detect a silent iota re-numbering in pkg/swim.
	if int(swim.StateAlive) >= int(swim.StateSuspect) {
		t.Errorf("swim ordering violated: StateAlive(%d) must be < StateSuspect(%d)",
			swim.StateAlive, swim.StateSuspect)
	}
	if int(swim.StateSuspect) >= int(swim.StateDead) {
		t.Errorf("swim ordering violated: StateSuspect(%d) must be < StateDead(%d)",
			swim.StateSuspect, swim.StateDead)
	}
	if int(swim.StateDead) >= int(swim.StateLeft) {
		t.Errorf("swim ordering violated: StateDead(%d) must be < StateLeft(%d)",
			swim.StateDead, swim.StateLeft)
	}
}

// ─── TestEqualIncarnationEqualStateIsNoOp ─────────────────────────────────────
// Verifies the documented divergence from swim.Member.UpdateState:
// a same-state same-incarnation re-observation is treated as a no-op and is
// NOT forwarded to the relay.
//
// swim.Member.UpdateState uses strict-less-than for the equal-incarnation
// same-state case (state < m.State), so it ACCEPTS and overwrites.
// CrossCellSuspicion.Observe uses less-than-or-equal (state <= entry.state),
// so it REJECTS the duplicate — keeping relay.Forward idempotent.
//
// Mutation that makes this fail:
//
//	Change "ev.State <= entry.state" (suspicion.go line 132) to
//	"ev.State < entry.state" (matching swim.UpdateState strict-less) so the
//	equal-state same-incarnation re-observation is accepted and forwarded →
//	relay.Count() becomes 2 and the "!= 1" assertion fires.
func TestEqualIncarnationEqualStateIsNoOp(t *testing.T) {
	relay := &fakeRelay{}
	css := newCSS(t, relay)

	ev := SuspicionEvent{CellID: "cell-J", MemberID: "node-9", State: swim.StateSuspect, Incarnation: 3}

	// First observation: accepted and forwarded.
	changed1 := mustObserve(t, css, ev)
	if !changed1 {
		t.Fatal("first Observe: want changed=true")
	}
	if relay.Count() != 1 {
		t.Fatalf("after first Observe: relay count = %d, want 1", relay.Count())
	}

	// Identical re-observation (same state, same incarnation): must be a no-op.
	changed2, err := css.Observe(context.Background(), ev)
	if err != nil {
		t.Fatalf("second Observe (duplicate): unexpected error: %v", err)
	}
	if changed2 {
		t.Error("duplicate same-state same-incarnation Observe: want changed=false (relay-idempotent no-op)")
	}
	// Relay must still have exactly one entry — the duplicate was not forwarded.
	if relay.Count() != 1 {
		t.Errorf("after duplicate Observe: relay count = %d, want 1 (diverges from swim.UpdateState by design)",
			relay.Count())
	}

	// Persisted state is unchanged.
	st, inc, known := css.State("cell-J", "node-9")
	if !known {
		t.Fatal("State: want known=true")
	}
	if st != swim.StateSuspect || inc != 3 {
		t.Errorf("state = %v inc=%d, want Suspect/3", st, inc)
	}
}
