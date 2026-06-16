package swim

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// swim_adversarial_test.go — adversarial SINK-SIDE probes for the SWIM membership
// state machine, incarnation total-order/overflow, concurrency safety on the
// shared member table, and hostile/self-referential gossip acceptance.
//
// Each test asserts the RESOLVED member state (the sink), conservation, or a
// refutation verdict — never merely "no panic". Probes that encode a CONTRACT
// pin the documented behaviour so they mutation-bite. Probes that encode a found
// REAL BUG are named *_BUG_* and explain the violated property; they FAIL on
// unfixed prod (not env-gated) so the defect is surfaced honestly.

// ---------------------------------------------------------------------------
// (a) INCARNATION TOTAL-ORDER at EQUAL incarnation.
//
// CONTRACT (member.go UpdateState): at equal incarnation, a state transition is
// accepted iff the new state has precedence >= current (Alive=0 < Suspect=1 <
// Dead=2 < Left=3). A lower-precedence state at equal incarnation is rejected.
// This gives a deterministic total order so two conflicting updates resolve the
// same way regardless of arrival order. Pin it both directions.
// ---------------------------------------------------------------------------

func TestAdversarial_EqualIncarnation_TotalOrder_Sink(t *testing.T) {
	// Higher-precedence state wins at equal incarnation (Suspect overrides Alive).
	m := &Member{ID: "n", State: StateAlive, Incarnation: 7}
	if !m.UpdateState(StateSuspect, 7) {
		t.Fatalf("Suspect@7 must override Alive@7 (higher precedence at equal incarnation)")
	}
	if got := m.State; got != StateSuspect {
		t.Fatalf("sink: want StateSuspect, got %v", got)
	}

	// Lower-precedence state must NOT win at equal incarnation (Alive cannot
	// override Suspect without a higher incarnation — the SWIM refutation rule).
	if m.UpdateState(StateAlive, 7) {
		t.Fatalf("Alive@7 must NOT override Suspect@7 (lower precedence at equal incarnation)")
	}
	if got := m.State; got != StateSuspect {
		t.Fatalf("sink after rejected downgrade: want StateSuspect, got %v", got)
	}

	// Order-independence: applying the two conflicting updates in the opposite
	// order yields the SAME winner (Suspect), proving a deterministic total order.
	m2 := &Member{ID: "n", State: StateAlive, Incarnation: 7}
	m2.UpdateState(StateAlive, 7)   // no-op-ish (already alive at 7)
	m2.UpdateState(StateSuspect, 7) // Suspect wins
	if m2.State != StateSuspect {
		t.Fatalf("order-independence violated: want StateSuspect, got %v", m2.State)
	}

	// Dead overrides Suspect at equal incarnation; Alive cannot resurrect at equal.
	md := &Member{ID: "n", State: StateSuspect, Incarnation: 3}
	if !md.UpdateState(StateDead, 3) {
		t.Fatalf("Dead@3 must override Suspect@3")
	}
	if md.UpdateState(StateAlive, 3) {
		t.Fatalf("Alive@3 must NOT resurrect Dead@3 (equal incarnation) — would be a dead-node resurrection")
	}
	if md.State != StateDead {
		t.Fatalf("sink: dead node resurrected at equal incarnation: %v", md.State)
	}
}

// Stale-incarnation rejection: a strictly lower incarnation never wins, even for
// a higher-precedence state. Pins the `incarnation < m.Incarnation` guard.
func TestAdversarial_StaleIncarnation_Rejected_Sink(t *testing.T) {
	m := &Member{ID: "n", State: StateAlive, Incarnation: 100}
	// A Dead claim at a LOWER incarnation (stale/hostile) must be refuted, not applied.
	if m.UpdateState(StateDead, 50) {
		t.Fatalf("Dead@50 must be rejected against Alive@100 (stale incarnation)")
	}
	if m.State != StateAlive {
		t.Fatalf("sink: stale Dead@50 wrongly killed Alive@100: %v", m.State)
	}
	// A higher incarnation Alive (legit refutation) does win.
	if !m.UpdateState(StateAlive, 101) {
		t.Fatalf("Alive@101 must win over Alive@100")
	}
}

// ---------------------------------------------------------------------------
// (a') INCARNATION OVERFLOW (uint32 wrap). SWIM incarnations are monotonic per
// node; a node bumps its own incarnation on every refutation. uint32 wrap at
// MaxUint32 -> 0 means a freshly-wrapped legit update (newest) compares as
// LOWER than a stale pre-wrap message, so the stale message wins / a dead node
// can resurrect. This pins the documented uint32 comparison so that if a future
// change claims to add wrap-safe total order, this test forces the discussion.
//
// We assert the CURRENT (documented uint32) behaviour at the sink and flag the
// wrap hazard as a LATENT RISK rather than a refutable bug, because the wire
// type is uint32 everywhere (Message.Incarnation) and reaching 2^32 self-bumps
// is not reachable in practice. The pin bites any change to the comparison.
// ---------------------------------------------------------------------------

func TestAdversarial_IncarnationOverflow_Wrap_DocumentedUint32(t *testing.T) {
	const maxU32 = ^uint32(0) // 4294967295

	// Node legitimately reached the max incarnation and is Alive.
	m := &Member{ID: "n", State: StateAlive, Incarnation: maxU32}

	// A stale Dead message from before still carries a "lower" number; correctly
	// rejected by the `<` guard. Sink: still Alive.
	if m.UpdateState(StateDead, maxU32-1) {
		t.Fatalf("Dead@max-1 must be rejected against Alive@max")
	}
	if m.State != StateAlive {
		t.Fatalf("sink: stale Dead killed max-incarnation node: %v", m.State)
	}

	// The node now refutes/advances. With a uint32 wire type the only "next"
	// value is 0 (wrap). Under the documented comparison, 0 < max, so this newest
	// update is REJECTED and a stale max-incarnation Dead would win — the wrap
	// hazard. We pin the documented uint32 behaviour at the sink: the wrapped
	// update does NOT take effect.
	one := uint32(1)
	wrapped := maxU32 + one // runtime wrap == 0 (compile-time maxU32+1 would overflow the const)
	if wrapped != 0 {
		t.Fatalf("sanity: uint32 wrap expected 0, got %d", wrapped)
	}
	applied := m.UpdateState(StateSuspect, wrapped) // Suspect@0 vs Alive@max
	if applied {
		t.Fatalf("documented uint32 behaviour: a wrapped (0) incarnation must NOT override max; got applied=true — comparison changed")
	}
	if m.State != StateAlive {
		t.Fatalf("sink: wrapped update unexpectedly changed state to %v", m.State)
	}
	// LATENT RISK documented in the final report: at the uint32 ceiling the newest
	// update loses to stale pre-wrap messages. Not reachable in practice (2^32
	// self-increments) and the wire type is uint32 throughout.
}

// ---------------------------------------------------------------------------
// (b) UNGUARDED SHARED STATE — concurrent gossip apply / add / query on the
// member map. Drive handleAlive (which inserts/updates p.members under p.mu),
// Members()/HealthyMembers() (read under p.mu), and per-member UpdateState
// concurrently. Run with -race. Sink: conservation — every joined member is
// present exactly once, no lost insert, no torn map.
// ---------------------------------------------------------------------------

func newTestProto(t *testing.T, id, addr string) *Protocol {
	t.Helper()
	netw := NewSimNetwork(1, PerfectLink)
	tr, err := netw.NewTransport(addr)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	p, err := NewProtocol(&Config{
		LocalID:        id,
		Transport:      tr,
		ProbeInterval:  time.Hour, // keep background loops idle/deterministic
		ProbeTimeout:   time.Millisecond,
		GossipInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewProtocol: %v", err)
	}
	// Note: we do NOT call Start(); these probes drive handlers directly so the
	// background loops never run and the test is deterministic and cannot hang.
	return p
}

func TestAdversarial_ConcurrentMemberTable_Conservation_Race(t *testing.T) {
	p := newTestProto(t, "self", "127.0.0.1:18001")

	const writers = 24
	const perWriter = 40
	var wg sync.WaitGroup

	// Concurrent inserters via the real handler path handleAlive (locks p.mu).
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			ua, _ := net.ResolveUDPAddr("udp", "127.0.0.1:1")
			for i := 0; i < perWriter; i++ {
				id := fmt.Sprintf("node-%d-%d", w, i)
				p.handleAlive(&Message{Type: MsgAlive, SourceID: id, Incarnation: 1}, ua)
			}
		}(w)
	}
	// Concurrent readers racing the writers.
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = p.Members()
				_ = p.HealthyMembers()
			}
		}()
	}
	wg.Wait()

	// Sink: conservation. self + writers*perWriter unique joined members present.
	got := len(p.Members())
	want := 1 + writers*perWriter
	if got != want {
		t.Fatalf("conservation violated: want %d members, got %d (lost update / torn insert)", want, got)
	}
	// Every expected id present exactly once.
	seen := make(map[string]int)
	for _, m := range p.Members() {
		seen[m.ID]++
	}
	for w := 0; w < writers; w++ {
		for i := 0; i < perWriter; i++ {
			id := fmt.Sprintf("node-%d-%d", w, i)
			if seen[id] != 1 {
				t.Fatalf("member %s present %d times (want 1)", id, seen[id])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// (c) SUSPICION -> DEAD timeout boundary (off-by-one) + refutation exactly at
// the boundary. Uses the injectable ManualClock + SuspicionManager so it is
// fully deterministic and cannot hang.
// ---------------------------------------------------------------------------

func TestAdversarial_SuspicionTimeoutBoundary_Sink(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	var deadID string
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		SuspicionTimeout: 100 * time.Millisecond,
		IndirectProbes:   0, // no prober -> die on timeout
		OnDead:           func(id string) { deadID = id },
	})

	m := &Member{ID: "victim", State: StateAlive, Incarnation: 5}
	if !sm.Suspect(m, 5) {
		t.Fatalf("Suspect must arm")
	}

	// Just BEFORE the boundary: must NOT be dead.
	clk.Advance(99 * time.Millisecond)
	if m.State == StateDead || deadID != "" {
		t.Fatalf("premature death at t=99ms (timeout=100ms): off-by-one early")
	}
	if !sm.IsSuspected("victim") {
		t.Fatalf("victim should still be suspected at t=99ms")
	}

	// Exactly AT the boundary (t=100ms): the death timer fires.
	clk.Advance(1 * time.Millisecond)
	if m.State != StateDead || deadID != "victim" {
		t.Fatalf("victim not declared Dead exactly at timeout boundary t=100ms (off-by-one late): state=%v deadID=%q", m.State, deadID)
	}
}

func TestAdversarial_RefutationAtBoundary_Sink(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	var dead, refuted string
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		SuspicionTimeout: 100 * time.Millisecond,
		IndirectProbes:   0,
		OnDead:           func(id string) { dead = id },
		OnRefute:         func(id string) { refuted = id },
	})
	m := &Member{ID: "victim", State: StateAlive, Incarnation: 5}
	sm.Suspect(m, 5)

	clk.Advance(99 * time.Millisecond)
	// Refute just before the boundary with a strictly higher incarnation: must win.
	if !sm.Refute("victim", 6) {
		t.Fatalf("refutation with higher incarnation must succeed before timeout")
	}
	if refuted != "victim" || m.State != StateAlive {
		t.Fatalf("sink: refuted member must be Alive: state=%v refuted=%q", m.State, refuted)
	}
	// Crossing the boundary now must NOT resurrect a death: the timer was cancelled.
	clk.Advance(10 * time.Millisecond)
	if dead == "victim" || m.State == StateDead {
		t.Fatalf("sink: refuted victim was still killed after boundary (cancelled timer fired): state=%v dead=%q", m.State, dead)
	}

	// Equal-incarnation refutation must FAIL (mirrors UpdateState total order).
	m2 := &Member{ID: "v2", State: StateAlive, Incarnation: 5}
	sm.Suspect(m2, 5)
	if sm.Refute("v2", 5) {
		t.Fatalf("equal-incarnation refutation must be rejected")
	}
}

// ---------------------------------------------------------------------------
// (d) HOSTILE / SELF-REFERENTIAL gossip.
//
// (d1) A Dead claim about a node N at a LOWER incarnation than N already holds
// must be refuted (rejected), never applied — otherwise a stale/hostile gossip
// kills a live node. Asserted at the sink via handleDead.
//
// (d2) SELF-Dead: a node receiving MsgDead targeting ITSELF. Canonical SWIM
// requires a node to refute a death rumour about itself (bump incarnation +
// broadcast Alive), exactly as handleSuspect(self) does. We probe the sink:
// the local member's resolved State after handleDead(self).
// ---------------------------------------------------------------------------

func TestAdversarial_HostileLowerIncarnationDead_Refuted_Sink(t *testing.T) {
	p := newTestProto(t, "self", "127.0.0.1:18001")
	ua, _ := net.ResolveUDPAddr("udp", "127.0.0.1:9")

	// Introduce a live peer at a high incarnation.
	p.handleAlive(&Message{Type: MsgAlive, SourceID: "peer", Incarnation: 100}, ua)

	// Hostile: claim peer Dead at a LOWER incarnation. Must be rejected.
	p.handleDead(&Message{Type: MsgDead, SourceID: "self", TargetID: "peer", Incarnation: 40}, ua)

	p.mu.RLock()
	st := p.members["peer"].State
	p.mu.RUnlock()
	if st == StateDead {
		t.Fatalf("sink: hostile Dead@40 wrongly killed peer who is Alive@100 (state=%v)", st)
	}
	if st != StateAlive {
		t.Fatalf("sink: peer should remain Alive after stale Dead, got %v", st)
	}
}

// TestAdversarial_SelfDead_MustRefute_BUG probes whether a node accepts a death
// rumour about ITSELF. In canonical SWIM a node MUST refute (it knows it is
// alive); handleSuspect implements this self-refutation, but handleDead does not.
//
// PROPERTY UNDER TEST: after receiving MsgDead(target=self), the local node must
// NOT mark itself Dead — it must remain Alive (ideally after bumping its
// incarnation). This test asserts the SINK: the local member's State.
//
// NOTE: this is written to PASS on correct behaviour and FAIL on the current
// implementation if the self-Dead path marks self Dead. It is NOT env-gated.
func TestAdversarial_SelfDead_NotRefuted_KNOWNBUG_FailsOnProd(t *testing.T) {
	p := newTestProto(t, "self", "127.0.0.1:18001")
	ua, _ := net.ResolveUDPAddr("udp", "127.0.0.1:9")

	selfIncBefore := p.Incarnation()

	// Hostile/buggy rumour: self is Dead at an incarnation >= self's own.
	// Use a high incarnation so the *member* state machine would accept it absent
	// a self-refute guard. This is exactly the rumour SWIM says a node must
	// refute about itself.
	p.handleDead(&Message{
		Type:        MsgDead,
		SourceID:    "other",
		TargetID:    "self",
		Incarnation: selfIncBefore + 5,
	}, ua)

	p.mu.RLock()
	selfState := p.members["self"].State
	p.mu.RUnlock()

	if selfState == StateDead {
		t.Fatalf("SWIM violation: node accepted a death rumour about ITSELF (state=%v). "+
			"Canonical SWIM requires self-refutation (bump incarnation + broadcast Alive), "+
			"as handleSuspect(self) does; handleDead lacks the self-target guard.", selfState)
	}
}

// HARDENED (HXC-1905): bumpIncarnation saturates at MaxUint32 instead of wrapping
// to 0, so the Protocol can never PRODUCE a wrapped incarnation that a stale
// pre-wrap message would beat (the resurrection hazard). Regression guard:
// reverting bumpIncarnation to a raw `inc+1` makes MaxUint32 bump to 0 and this
// FAILS.
func TestAdversarial_BumpIncarnation_SaturatesAtMaxUint32(t *testing.T) {
	const maxU32 = ^uint32(0)
	if got := bumpIncarnation(maxU32); got != maxU32 {
		t.Fatalf("bumpIncarnation(MaxUint32) must saturate at MaxUint32, not wrap; got %d", got)
	}
	if got := bumpIncarnation(maxU32 - 1); got != maxU32 {
		t.Fatalf("bumpIncarnation(max-1) must reach MaxUint32; got %d", got)
	}
	if got := bumpIncarnation(0); got != 1 {
		t.Fatalf("bumpIncarnation(0) must be 1 (normal increment); got %d", got)
	}
	if got := bumpIncarnation(41); got != 42 {
		t.Fatalf("bumpIncarnation(41) must be 42; got %d", got)
	}
}
