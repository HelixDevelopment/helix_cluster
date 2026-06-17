package federation

// federation_adversarial2_test.go — second adversarial pass focused on the
// SINGLE latent risk flagged by the prior audit:
//
//	"relay.Forward-after-unlock reorder (benign, receiver incarnation-monotonic)"
//
// CrossCellSuspicion.Observe commits the local member table UNDER the lock but
// calls relay.Forward AFTER releasing it (suspicion.go: "Forward ... outside the
// lock to avoid holding the mutex across a network call"). Two goroutines racing
// on the SAME (cell,member) key can therefore hand the relay two forwards in an
// order that DISAGREES with the order the local table committed them — i.e. a
// strictly-newer event can hit the wire BEFORE a strictly-older one.
//
// The prior pass labelled this "benign, receiver incarnation-monotonic" but did
// not PROVE it. A recent case (pkg/backfill) was mislabelled "latent" and turned
// out to be an always-on bug, so this pass DISCRIMINATES the three outcomes:
//
//   - REAL BUG: some delivery order leaves a correct monotonic receiver at the
//     WRONG final (state, incarnation) — a lost update / state divergence.
//   - DOCUMENTED CONTRACT: EVERY delivery order (every permutation, the real
//     concurrent window, the equal-incarnation window) leaves a monotonic
//     receiver at the SAME final state as the sender's table — reorder is benign
//     and we pin exactly WHY (receiver monotonicity is load-bearing).
//   - LATENT: cannot be observed with this harness.
//
// VERDICT (proven below): DOCUMENTED CONTRACT. The reorder is benign for ANY
// incarnation-monotonic receiver, across ALL delivery permutations including the
// equal-incarnation tie case the existing chaos_test does not cover. The
// load-bearing precondition (a NON-monotonic receiver DOES diverge) is pinned in
// TestAdversarial2_NonMonotonicReceiverWouldDiverge so the contract can never be
// silently weakened on the receiver side.
//
// Every assertion is SINK-SIDE: it asserts the resolved (state, incarnation) a
// real downstream CrossCellSuspicion table reports after consuming the forwards.

import (
	"context"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/swim"
)

// captureRelay records, in arrival order, exactly the events Forward received.
// It is itself mutex-guarded so the harness adds no race of its own.
type captureRelay struct {
	mu     sync.Mutex
	events []SuspicionEvent
}

func (r *captureRelay) Forward(_ context.Context, ev SuspicionEvent) error {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
	return nil
}

func (r *captureRelay) snapshot() []SuspicionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SuspicionEvent, len(r.events))
	copy(out, r.events)
	return out
}

// noForwardRelay lets a downstream receiver apply forwarded events through its
// own real Observe merge WITHOUT re-forwarding (so the harness does not recurse).
type noForwardRelay struct{}

func (noForwardRelay) Forward(context.Context, SuspicionEvent) error { return nil }

// applyToFreshReceiver feeds the given events, IN THE GIVEN ORDER, to a brand-new
// downstream CrossCellSuspicion (the authoritative cross-cell sink) using the
// SAME monotonic merge production uses, and returns its final resolved state for
// (cellID, memberID).
func applyToFreshReceiver(t *testing.T, cellID, memberID string, evs []SuspicionEvent) (swim.MemberState, uint32, bool) {
	t.Helper()
	rx := NewCrossCellSuspicion("downstream", noForwardRelay{})
	for _, ev := range evs {
		_, _ = rx.Observe(context.Background(), ev)
	}
	return rx.State(cellID, memberID)
}

// permute yields every permutation of evs (Heap's algorithm), invoking fn on a
// fresh copy each time. The forward sets under test are tiny (<= 4 events) so the
// factorial blow-up is bounded and cheap.
func permute(evs []SuspicionEvent, fn func([]SuspicionEvent)) {
	n := len(evs)
	work := make([]SuspicionEvent, n)
	copy(work, evs)
	c := make([]int, n)
	emit := func() {
		cp := make([]SuspicionEvent, n)
		copy(cp, work)
		fn(cp)
	}
	emit()
	for i := 0; i < n; {
		if c[i] < i {
			if i%2 == 0 {
				work[0], work[i] = work[i], work[0]
			} else {
				work[c[i]], work[i] = work[i], work[c[i]]
			}
			emit()
			c[i]++
			i = 0
		} else {
			c[i] = 0
			i++
		}
	}
}

// ---------------------------------------------------------------------------
// PROOF 1 — every delivery permutation of an accepted forward set converges.
// ---------------------------------------------------------------------------

// TestAdversarial2_RelayReorderBenignAllPermutations is the central discriminator.
//
// We drive a strictly-escalating sequence of DISTINCT accepted observations into
// cellA (each forwarded exactly once), capture the exact forward set, then deliver
// that set to a fresh monotonic receiver in EVERY possible order. The receiver
// must converge to cellA's own final table state for EVERY permutation.
//
// If any permutation diverged, the relay-after-unlock reorder would be a REAL
// lost-update bug (a stale forward could win at the sink). It does NOT diverge —
// proving the reorder is benign and pinning that benignity as a contract.
func TestAdversarial2_RelayReorderBenignAllPermutations(t *testing.T) {
	rel := &captureRelay{}
	a := NewCrossCellSuspicion("cell-a", rel)

	// Distinct, strictly-advancing observations — all accepted, all forwarded.
	// Mix of incarnation bumps and equal-incarnation state advances so the forward
	// set exercises BOTH ordering rules.
	plan := []SuspicionEvent{
		{CellID: "cell-a", MemberID: "m1", State: swim.StateAlive, Incarnation: 1},
		{CellID: "cell-a", MemberID: "m1", State: swim.StateSuspect, Incarnation: 1}, // equal-inc advance
		{CellID: "cell-a", MemberID: "m1", State: swim.StateDead, Incarnation: 4},    // inc bump
		{CellID: "cell-a", MemberID: "m1", State: swim.StateLeft, Incarnation: 9},    // inc bump
	}
	for _, ev := range plan {
		changed, err := a.Observe(context.Background(), ev)
		if !changed || err != nil {
			t.Fatalf("seed observe %+v: changed=%v err=%v (expected accepted+forwarded)", ev, changed, err)
		}
	}

	// cellA's own converged truth — the answer the sink MUST reach.
	wantState, wantInc, ok := a.State("cell-a", "m1")
	if !ok || wantState != swim.StateLeft || wantInc != 9 {
		t.Fatalf("sender table did not converge as expected: (%v,%d,%v)", wantState, wantInc, ok)
	}

	forwards := rel.snapshot()
	if len(forwards) != len(plan) {
		t.Fatalf("expected %d forwards (one per accepted change), got %d: %+v", len(plan), len(forwards), forwards)
	}

	perms := 0
	permute(forwards, func(order []SuspicionEvent) {
		perms++
		gotState, gotInc, known := applyToFreshReceiver(t, "cell-a", "m1", order)
		if !known {
			t.Fatalf("receiver never recorded member after order %+v", order)
		}
		if gotState != wantState || gotInc != wantInc {
			t.Fatalf("REORDER LOST-UPDATE: delivery order %+v converged receiver to (%v,%d), "+
				"want sender truth (%v,%d). relay-after-unlock reorder is NOT benign.",
				order, gotState, gotInc, wantState, wantInc)
		}
	})
	if perms != 24 { // 4! — confirms we actually exercised every order.
		t.Fatalf("expected 24 permutations exercised, got %d", perms)
	}
}

// ---------------------------------------------------------------------------
// PROOF 2 — the equal-incarnation reorder case (NOT covered by chaos_test).
// ---------------------------------------------------------------------------

// TestAdversarial2_EqualIncarnationReorderBenign isolates the subtlest window:
// two forwards at the SAME incarnation that strictly advance state
// (Suspect@7 then Dead@7). Both are accepted+forwarded by the sender. If they
// arrive reversed (Dead@7 then Suspect@7) the receiver's equal-incarnation rule
// (ev.State <= entry.state => no-op) must DROP the stale Suspect and hold Dead@7.
//
// The existing chaos_test only reorders incarnation-distinct events; this pins
// the equal-incarnation tie, which is exactly where a "<" vs "<=" receiver bug
// would silently regress state.
func TestAdversarial2_EqualIncarnationReorderBenign(t *testing.T) {
	suspect := SuspicionEvent{CellID: "cell-a", MemberID: "m", State: swim.StateSuspect, Incarnation: 7}
	dead := SuspicionEvent{CellID: "cell-a", MemberID: "m", State: swim.StateDead, Incarnation: 7}

	// In-order delivery.
	s1, i1, _ := applyToFreshReceiver(t, "cell-a", "m", []SuspicionEvent{suspect, dead})
	// Reversed delivery (the reorder window).
	s2, i2, _ := applyToFreshReceiver(t, "cell-a", "m", []SuspicionEvent{dead, suspect})

	if s1 != swim.StateDead || i1 != 7 {
		t.Fatalf("in-order equal-inc converged to (%v,%d), want (Dead,7)", s1, i1)
	}
	if s2 != swim.StateDead || i2 != 7 {
		t.Fatalf("REORDERED equal-inc converged to (%v,%d), want (Dead,7) — stale Suspect@7 "+
			"regressed the receiver. equal-incarnation reorder is NOT benign.", s2, i2)
	}
}

// ---------------------------------------------------------------------------
// PROOF 3 — the REAL concurrent wire window, sink-side.
// ---------------------------------------------------------------------------

// TestAdversarial2_ConcurrentForwardConvergesAtReceiver reproduces the actual
// race (not a synthetic permutation): two goroutines Observe the same key with
// strictly-advancing incarnations, so the relay genuinely MAY receive them
// newest-first (the prior pass confirmed this window is observable). We then feed
// the relay's ACTUAL captured order into a fresh monotonic receiver and assert it
// converges to the highest incarnation. This is the end-to-end sink-side proof
// that the relay-after-unlock window cannot corrupt a downstream cell.
func TestAdversarial2_ConcurrentForwardConvergesAtReceiver(t *testing.T) {
	const iterations = 500
	for it := 0; it < iterations; it++ {
		rel := &captureRelay{}
		a := NewCrossCellSuspicion("cell-a", rel)
		// Seed so both racing observations are strictly-advancing accepts.
		_, _ = a.Observe(context.Background(), SuspicionEvent{CellID: "cell-a", MemberID: "m1", State: swim.StateAlive, Incarnation: 1})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = a.Observe(context.Background(), SuspicionEvent{CellID: "cell-a", MemberID: "m1", State: swim.StateSuspect, Incarnation: 2})
		}()
		go func() {
			defer wg.Done()
			_, _ = a.Observe(context.Background(), SuspicionEvent{CellID: "cell-a", MemberID: "m1", State: swim.StateDead, Incarnation: 3})
		}()
		wg.Wait()

		// Sender table always converges to the highest incarnation (lock-protected).
		sa, ia, _ := a.State("cell-a", "m1")
		if sa != swim.StateDead || ia != 3 {
			t.Fatalf("iter %d: sender table did not converge: (%v,%d)", it, sa, ia)
		}

		// Deliver the relay's ACTUAL (possibly reordered) forward stream to a fresh
		// monotonic receiver. Whatever the wire order, the sink must end at Dead@3.
		forwards := rel.snapshot()
		sb, ib, known := applyToFreshReceiver(t, "cell-a", "m1", forwards)
		if !known {
			t.Fatalf("iter %d: receiver never recorded member; forwards=%+v", it, forwards)
		}
		if sb != swim.StateDead || ib != 3 {
			t.Fatalf("iter %d: SINK DIVERGENCE under real concurrent reorder: receiver=(%v,%d) want (Dead,3); "+
				"wire order=%+v", it, sb, ib, forwards)
		}
	}
}

// ---------------------------------------------------------------------------
// PROOF 4 — the load-bearing precondition (why the contract is "monotonic").
// ---------------------------------------------------------------------------

// lwwReceiver is a deliberately NON-monotonic (last-writer-wins) receiver: it
// overwrites unconditionally on every event. It models what a downstream that
// "trusts arrival order" would do.
type lwwReceiver struct {
	state swim.MemberState
	inc   uint32
}

func (r *lwwReceiver) apply(ev SuspicionEvent) { r.state, r.inc = ev.State, ev.Incarnation }

// TestAdversarial2_NonMonotonicReceiverWouldDiverge pins the EXACT precondition
// that makes the reorder benign: receiver monotonicity. Fed the SAME reordered
// forward stream, a non-monotonic last-writer-wins receiver DOES regress to a
// stale state, while the real monotonic CrossCellSuspicion receiver does not.
//
// This is the mutation-style witness for the DOCUMENTED CONTRACT: it proves the
// benignity is NOT free — it is purchased entirely by the receiver's monotonic
// merge. If a future downstream drops that merge, the relay-after-unlock reorder
// becomes a real lost-update. (No production code is changed; this asserts the
// contract boundary.)
func TestAdversarial2_NonMonotonicReceiverWouldDiverge(t *testing.T) {
	// Reordered stream: newest (Dead@5) arrives BEFORE stale (Alive@1).
	reordered := []SuspicionEvent{
		{CellID: "cell-a", MemberID: "m", State: swim.StateDead, Incarnation: 5},
		{CellID: "cell-a", MemberID: "m", State: swim.StateAlive, Incarnation: 1},
	}

	// Monotonic receiver (production path) — must hold Dead@5.
	sM, iM, _ := applyToFreshReceiver(t, "cell-a", "m", reordered)
	if sM != swim.StateDead || iM != 5 {
		t.Fatalf("monotonic receiver regressed under reorder: (%v,%d), want (Dead,5)", sM, iM)
	}

	// Non-monotonic receiver — DOES regress to the stale Alive@1. If this ever
	// stopped regressing, the test's premise (that monotonicity is load-bearing)
	// would be wrong and must be revisited.
	lww := &lwwReceiver{}
	for _, ev := range reordered {
		lww.apply(ev)
	}
	if lww.state == swim.StateDead && lww.inc == 5 {
		t.Fatalf("non-monotonic receiver did NOT regress — premise broken; the reorder's "+
			"benignity cannot be attributed solely to receiver monotonicity")
	}
	if lww.state != swim.StateAlive || lww.inc != 1 {
		t.Fatalf("non-monotonic receiver unexpected final state (%v,%d), want (Alive,1)", lww.state, lww.inc)
	}
}
