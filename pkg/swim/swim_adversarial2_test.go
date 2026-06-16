package swim

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// swim_adversarial2_test.go — SECOND adversarial pass over the SWIM membership
// state machine. The first pass (swim_adversarial_test.go) pinned the
// incarnation total-order, the saturating-bump, the self-Dead refute, and a
// concurrency-conservation probe. This pass targets OTHER load-bearing paths:
//
//   - the merge/precedence rule as a genuine TOTAL ORDER across ALL four states
//     (Alive<Suspect<Dead<Left), including higher-incarnation resurrection and
//     order-independence on randomized application;
//   - Dead as an ABSORBING state against stale Alive/Suspect, and lawful
//     higher-incarnation resurrection;
//   - the FailureDetector refutation contract: whether a STALE refutation (one
//     whose incarnation does NOT beat the live suspicion) is allowed to cancel a
//     legitimate death timer (handleAlive calls fd.Refute UNCONDITIONALLY even
//     when UpdateState rejected the stale Alive);
//   - concurrent gossip apply + membership read under -race (separate single
//     package run), asserting a deterministic resolved sink.
//
// Sink-side discipline: every probe asserts the RESOLVED member State / the
// detector's suspicion bookkeeping / conservation — never merely "no panic".
// Probes encoding a documented CONTRACT pin it so it mutation-bites. A probe
// encoding a REAL found bug is named *_BUG_* and FAILS on unfixed prod.

// ---------------------------------------------------------------------------
// (A) TOTAL-ORDER LATTICE over all four states at equal incarnation.
//
// UpdateState's equal-incarnation rule accepts iff new.State >= cur.State by the
// numeric precedence Alive(0)<Suspect(1)<Dead(2)<Left(3). For a relation to be a
// usable conflict resolver it must be a TOTAL ORDER: for every ordered pair the
// higher-precedence state wins and the lower loses, deterministically. Exhaustively
// pin every (from,to) pair at equal incarnation. This is broader than the first
// pass (which spot-checked a few pairs).
// ---------------------------------------------------------------------------

func TestAdversarial2_EqualIncarnation_FullLattice_Sink(t *testing.T) {
	states := []MemberState{StateAlive, StateSuspect, StateDead, StateLeft}
	for _, from := range states {
		for _, to := range states {
			m := &Member{ID: "n", State: from, Incarnation: 9}
			applied := m.UpdateState(to, 9)
			wantApplied := to >= from
			if applied != wantApplied {
				t.Fatalf("equal-inc (%v->%v): applied=%v want %v (precedence broken)", from, to, applied, wantApplied)
			}
			// Sink: resolved state is the higher-precedence of the two.
			wantState := from
			if to >= from {
				wantState = to
			}
			if m.State != wantState {
				t.Fatalf("equal-inc (%v->%v) sink: state=%v want %v", from, to, m.State, wantState)
			}
		}
	}
}

// Higher-incarnation ALWAYS wins regardless of state precedence — including a
// lawful resurrection (Dead -> Alive at a strictly higher incarnation). Pin the
// whole cross product at inc+1 so any change to the `<`/`==` split bites.
func TestAdversarial2_HigherIncarnation_AlwaysWins_Sink(t *testing.T) {
	states := []MemberState{StateAlive, StateSuspect, StateDead, StateLeft}
	for _, from := range states {
		for _, to := range states {
			m := &Member{ID: "n", State: from, Incarnation: 5}
			if !m.UpdateState(to, 6) {
				t.Fatalf("higher-inc (%v@5 -> %v@6) must ALWAYS be applied", from, to)
			}
			if m.State != to || m.Incarnation != 6 {
				t.Fatalf("higher-inc sink (%v@5 -> %v@6): got state=%v inc=%d", from, to, m.State, m.Incarnation)
			}
		}
	}
}

// Order-independence on RANDOMIZED application: shuffling a fixed multiset of
// (state,incarnation) updates must converge to the SAME resolved (state,inc),
// proving the merge is a deterministic join (a real total order, not arrival
// dependent). The lattice's join of the set is its max by (incarnation, state).
func TestAdversarial2_MergeConvergence_OrderIndependent_Sink(t *testing.T) {
	type upd struct {
		s MemberState
		i uint32
	}
	updates := []upd{
		{StateSuspect, 3}, {StateAlive, 5}, {StateDead, 5},
		{StateAlive, 4}, {StateLeft, 2}, {StateSuspect, 5},
		{StateAlive, 1}, {StateDead, 4},
	}
	// Expected join: max incarnation is 5; among inc==5 updates the max-precedence
	// state is Dead(2) (Alive,Dead,Suspect all at 5 -> Dead wins). So sink = Dead@5.
	const wantState = StateDead
	const wantInc = uint32(5)

	// Try several permutations (rotations + reversal) — all must converge.
	perms := [][]upd{}
	for shift := 0; shift < len(updates); shift++ {
		p := make([]upd, 0, len(updates))
		p = append(p, updates[shift:]...)
		p = append(p, updates[:shift]...)
		perms = append(perms, p)
	}
	// reversed
	rev := make([]upd, len(updates))
	for i := range updates {
		rev[i] = updates[len(updates)-1-i]
	}
	perms = append(perms, rev)

	for pi, perm := range perms {
		m := &Member{ID: "n", State: StateAlive, Incarnation: 0}
		for _, u := range perm {
			m.UpdateState(u.s, u.i)
		}
		if m.State != wantState || m.Incarnation != wantInc {
			t.Fatalf("perm %d converged to (state=%v inc=%d), want (Dead,5) — merge not order-independent", pi, m.State, m.Incarnation)
		}
	}
}

// ---------------------------------------------------------------------------
// (B) DEAD is ABSORBING against stale, but lawfully resurrectable by a strictly
// higher incarnation. Drive the REAL handler paths (handleDead/handleAlive) so
// the absorbing property is asserted end-to-end through the protocol, not just
// on a bare Member.
// ---------------------------------------------------------------------------

func TestAdversarial2_DeadAbsorbsStale_ButResurrectsOnHigherInc_Sink(t *testing.T) {
	p := newTestProto(t, "self", "127.0.0.1:18051")
	ua, _ := net.ResolveUDPAddr("udp", "127.0.0.1:9")

	// Peer becomes Alive@10 then Dead@10 (Dead>Alive at equal inc).
	p.handleAlive(&Message{Type: MsgAlive, SourceID: "peer", Incarnation: 10}, ua)
	p.handleDead(&Message{Type: MsgDead, SourceID: "other", TargetID: "peer", Incarnation: 10}, ua)
	assertState(t, p, "peer", StateDead)

	// Stale Alive@10 (equal inc) must NOT resurrect — Dead absorbs it.
	p.handleAlive(&Message{Type: MsgAlive, SourceID: "peer", Incarnation: 10}, ua)
	assertState(t, p, "peer", StateDead)

	// Stale Alive@9 (lower inc) must NOT resurrect.
	p.handleAlive(&Message{Type: MsgAlive, SourceID: "peer", Incarnation: 9}, ua)
	assertState(t, p, "peer", StateDead)

	// Lawful resurrection: Alive@11 (strictly higher) DOES resurrect (a rejoining
	// node bumps its incarnation past the death rumour).
	p.handleAlive(&Message{Type: MsgAlive, SourceID: "peer", Incarnation: 11}, ua)
	assertState(t, p, "peer", StateAlive)
}

// ---------------------------------------------------------------------------
// (C) FAILURE-DETECTOR REFUTATION CONTRACT — the load-bearing bug.
//
// In handleAlive the protocol calls p.fd.Refute(source) UNCONDITIONALLY, even
// when the accompanying m.UpdateState(StateAlive, inc) was REJECTED because the
// Alive was STALE (lower-or-equal incarnation than the live Suspect). fd.Refute
// takes NO incarnation and cancels the armed death timer regardless. So a stale
// (or replayed) Alive gossip can CANCEL a legitimate suspicion's death timer:
// the suspected node then never transitions to Dead even though it is genuinely
// gone — a failure-detector liveness defect (a dead node is kept "suspect
// forever", never Confirmed). Canonical SWIM only honours a refutation carrying
// an incarnation strictly higher than the suspicion.
//
// We drive the REAL handlers: handleSuspect arms fd at inc=5; a STALE
// handleAlive at inc=3 (which UpdateState rejects, leaving the member Suspect)
// must NOT clear the suspicion. SINK: fd.IsSuspected(peer) stays true after the
// stale Alive.
// ---------------------------------------------------------------------------

func TestAdversarial2_StaleAliveCancelsDeathTimer_BUG_FailsOnProd(t *testing.T) {
	p := newTestProto(t, "self", "127.0.0.1:18052")
	ua, _ := net.ResolveUDPAddr("udp", "127.0.0.1:9")

	// Peer known Alive@5.
	p.handleAlive(&Message{Type: MsgAlive, SourceID: "peer", Incarnation: 5}, ua)
	assertState(t, p, "peer", StateAlive)

	// Peer suspected at inc 5 (real path: arms fd death timer + sets Suspect).
	p.handleSuspect(&Message{Type: MsgSuspect, SourceID: "self", TargetID: "peer", Incarnation: 5}, ua)
	assertState(t, p, "peer", StateSuspect)
	if !p.fd.IsSuspected("peer") {
		t.Fatalf("precondition: peer must be suspected after handleSuspect")
	}

	// STALE Alive@3: UpdateState rejects it (3 < 5), so the member CORRECTLY stays
	// Suspect. The detector must likewise NOT treat this as a valid refutation.
	p.handleAlive(&Message{Type: MsgAlive, SourceID: "peer", Incarnation: 3}, ua)

	// Member state is correctly still Suspect (UpdateState rejected the stale Alive)...
	assertState(t, p, "peer", StateSuspect)

	// ...but the SINK that bites: a stale Alive must NOT have cleared the death
	// timer. If the suspicion was cancelled, the genuinely-failing peer will never
	// be Confirmed Dead — the failure detector was defeated by a replayed packet.
	if !p.fd.IsSuspected("peer") {
		t.Fatalf("BUG: a STALE Alive@3 cancelled the death timer of a Suspect@5 " +
			"(handleAlive calls fd.Refute unconditionally, ignoring incarnation). " +
			"The peer can no longer be Confirmed Dead — failure detector defeated.")
	}
}

// Companion: a LEGITIMATE refutation (Alive at a STRICTLY HIGHER incarnation than
// the suspicion) MUST clear the suspicion and return the member to Alive. This
// pins the correct direction so the fix cannot over-correct into never refuting.
func TestAdversarial2_HigherIncAliveRefutesSuspect_Sink(t *testing.T) {
	p := newTestProto(t, "self", "127.0.0.1:18053")
	ua, _ := net.ResolveUDPAddr("udp", "127.0.0.1:9")

	p.handleAlive(&Message{Type: MsgAlive, SourceID: "peer", Incarnation: 5}, ua)
	p.handleSuspect(&Message{Type: MsgSuspect, SourceID: "self", TargetID: "peer", Incarnation: 5}, ua)
	if !p.fd.IsSuspected("peer") {
		t.Fatalf("precondition: peer suspected")
	}

	// Legit refutation: peer bumps its incarnation to 6 and broadcasts Alive.
	p.handleAlive(&Message{Type: MsgAlive, SourceID: "peer", Incarnation: 6}, ua)

	assertState(t, p, "peer", StateAlive)
	if p.fd.IsSuspected("peer") {
		t.Fatalf("a legitimate higher-incarnation Alive@6 must clear the Suspect@5 death timer")
	}
}

// ---------------------------------------------------------------------------
// (D) CONCURRENCY: concurrent state-merges on a SHARED member plus concurrent
// reads, then assert the deterministic resolved sink. Run the package with
// -race separately. A single shared member is hammered by many writers applying
// a fixed multiset of updates in random order; the resolved (state,inc) must be
// the lattice join independent of the racing schedule.
// ---------------------------------------------------------------------------

func TestAdversarial2_ConcurrentMerge_DeterministicJoin_Race(t *testing.T) {
	m := &Member{ID: "n", State: StateAlive, Incarnation: 0}

	// Fixed multiset whose join is Dead@7 (max inc 7; at 7 the max-precedence is Dead).
	type upd struct {
		s MemberState
		i uint32
	}
	set := []upd{
		{StateAlive, 7}, {StateSuspect, 7}, {StateDead, 7},
		{StateSuspect, 6}, {StateDead, 5}, {StateLeft, 3},
		{StateAlive, 4}, {StateAlive, 2},
	}

	const goroutines = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			<-start
			// Apply the set in a rotated order per-goroutine to vary interleavings.
			for k := 0; k < len(set); k++ {
				u := set[(k+seed)%len(set)]
				m.UpdateState(u.s, u.i)
			}
		}(g)
	}
	// Concurrent readers (exercise the RLock paths via IsHealthy/LastSeen).
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 200; i++ {
				_ = m.IsHealthy()
				_ = m.LastSeen()
			}
		}()
	}
	close(start)
	wg.Wait()

	// Sink: regardless of the racing schedule, the join is Dead@7. Because Dead
	// absorbs at inc 7 and 7 is the max incarnation, no later update can lower it.
	if m.State != StateDead || m.Incarnation != 7 {
		t.Fatalf("concurrent merge did not converge to the lattice join: got state=%v inc=%d want (Dead,7)", m.State, m.Incarnation)
	}
}

// assertState reads a member's resolved State under the protocol lock.
func assertState(t *testing.T, p *Protocol, id string, want MemberState) {
	t.Helper()
	p.mu.RLock()
	m, ok := p.members[id]
	p.mu.RUnlock()
	if !ok {
		t.Fatalf("member %q absent", id)
	}
	m.mu.RLock()
	st := m.State
	m.mu.RUnlock()
	if st != want {
		t.Fatalf("member %q sink state=%v want %v", id, st, want)
	}
}

var _ = fmt.Sprintf
var _ = time.Now
