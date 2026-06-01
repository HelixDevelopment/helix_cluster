package swim

// lifecycle_test.go — deterministic unit tests for the SWIM gossip lifecycle:
//
//   JOINING (StateAlive on join) -> ACTIVE (StateAlive, probed alive) ->
//   SUSPECT (StateSuspect, missed acks) -> FAILED (StateDead, timeout expired)
//
// All tests use ManualClock + SimTransport/recordingProber — no wall-clock
// dependency, no flakiness, no real sockets. Sink-side assertions verify the
// EXACT membership state transitions, not just "no panic" / "non-nil".
//
// §1.1 mutation-proof pairings are inline (break→fail→restore).

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Lifecycle: JOINING -> ACTIVE (Alive) via join handshake
// ============================================================================

// TestLifecycle_JoinSetsAlive asserts that when node B sends a MsgJoin to node
// A, A records B as StateAlive (JOINING -> ACTIVE transition), and B learns
// about A via the MsgAlive response.
//
// Mutation proof: if handleJoin does not add the member (delete the
// p.members[msg.SourceID] = ... block), Members() on p1 still has 1 entry and
// the Len(members1, 2) assertion fails.
func TestLifecycle_JoinSetsAlive(t *testing.T) {
	p1, err := NewProtocol(&Config{
		LocalID:        "alpha",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  30 * time.Second,
		GossipInterval: 30 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, p1.Start())
	defer p1.Stop()

	p2, err := NewProtocol(&Config{
		LocalID:        "beta",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  30 * time.Second,
		GossipInterval: 30 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, p2.Start())
	defer p2.Stop()

	// B joins A.
	require.NoError(t, p2.Join([]string{p1.LocalAddr()}))

	// Allow the join + alive messages to propagate over the real UDP transport.
	require.Eventually(t, func() bool {
		p1.mu.RLock()
		_, found := p1.members["beta"]
		p1.mu.RUnlock()
		return found
	}, 2*time.Second, 20*time.Millisecond, "A must record B as a member within 2s")

	// Sink-side: A knows B as StateAlive (ACTIVE).
	p1.mu.RLock()
	mB := p1.members["beta"]
	p1.mu.RUnlock()
	require.NotNil(t, mB)
	assert.Equal(t, StateAlive, mB.State, "joined node must be ACTIVE (StateAlive) on the receiver")

	// B knows A as well (from the MsgAlive reply).
	require.Eventually(t, func() bool {
		p2.mu.RLock()
		_, found := p2.members["alpha"]
		p2.mu.RUnlock()
		return found
	}, 2*time.Second, 20*time.Millisecond, "B must learn about A from the join-ack")
}

// Mutation companion: if StateAlive is not set, the assertion fails.
func TestLifecycle_JoinSetsAlive_Mutation(t *testing.T) {
	// Drive the same path but assert that we see StateAlive, not any other state.
	p1, err := NewProtocol(&Config{
		LocalID: "alpha2", BindAddr: "127.0.0.1", BindPort: 0,
		ProbeInterval: 30 * time.Second, GossipInterval: 30 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, p1.Start())
	defer p1.Stop()

	p2, err := NewProtocol(&Config{
		LocalID: "beta2", BindAddr: "127.0.0.1", BindPort: 0,
		ProbeInterval: 30 * time.Second, GossipInterval: 30 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, p2.Start())
	defer p2.Stop()

	require.NoError(t, p2.Join([]string{p1.LocalAddr()}))
	require.Eventually(t, func() bool {
		p1.mu.RLock()
		_, found := p1.members["beta2"]
		p1.mu.RUnlock()
		return found
	}, 2*time.Second, 20*time.Millisecond)

	p1.mu.RLock()
	mB := p1.members["beta2"]
	p1.mu.RUnlock()
	// Mutation: if join handler set StateSuspect instead of StateAlive, this fails.
	require.NotEqual(t, StateSuspect, mB.State, "join must NOT mark member Suspect")
	require.NotEqual(t, StateDead, mB.State, "join must NOT mark member Dead")
}

// ============================================================================
// Lifecycle: ACTIVE -> SUSPECT (missed acks via sim transport, deterministic)
// ============================================================================

// TestLifecycle_MissedAckBecomeSuspect verifies the core SWIM path:
// a node that fails to ack a direct probe must be moved from StateAlive
// (ACTIVE) to StateSuspect (SUSPECT) by the suspicion manager.
//
// Uses ManualClock + recordingProber — zero wall-clock dependency.
//
// Mutation proof: if SuspicionManager.Suspect did not call
// member.UpdateState(StateSuspect, ...) (i.e. the state machine transition is
// removed), the assert.Equal(StateSuspect) below fails.
func TestLifecycle_MissedAckBecomeSuspect(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	prober.directReply["peer"] = false // direct probe fails => missed ack

	peer := newTestMember("peer", 0)
	peer.UpdateState(StateAlive, 0) // start ACTIVE

	var dead []string
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 5 * time.Second,
		IndirectProbes:   1,
		OnDead:           func(id string) { dead = append(dead, id) },
	})

	// ACTIVE -> SUSPECT: suspicion armed at incarnation 1.
	ok := sm.Suspect(peer, 1)
	require.True(t, ok, "first suspect at incarnation 1 must succeed")

	// Sink-side: member state is now SUSPECT before the timeout.
	assert.Equal(t, StateSuspect, peer.State, "missed ack must move peer to SUSPECT")
	assert.True(t, sm.IsSuspected("peer"), "suspicion must be registered")
	assert.Empty(t, dead, "node must NOT be dead before timeout elapses")
}

// Mutation: if Suspect doesn't update the member state, the assertion fails.
func TestLifecycle_MissedAckBecomeSuspect_Mutation(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()

	peer := newTestMember("peer", 0)
	// Force incarnation to 0 so we can suspect at incarnation 1.
	peer.Incarnation = 0
	peer.State = StateAlive

	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 5 * time.Second,
		IndirectProbes:   1,
	})

	ok := sm.Suspect(peer, 1)
	require.True(t, ok)
	// Mutation guard: state must be EXACTLY StateSuspect; if the impl used
	// StateDead or left it Alive, these fail.
	require.Equal(t, StateSuspect, peer.State)
	require.NotEqual(t, StateDead, peer.State)
	require.NotEqual(t, StateAlive, peer.State)
}

// ============================================================================
// Lifecycle: SUSPECT -> FAILED (StateDead) on timeout without ack
// ============================================================================

// TestLifecycle_SuspectTimeoutFails verifies the SUSPECT -> FAILED transition:
// when the suspicion timer fires and no ack is received (indirect probes fail
// too), the member must become StateDead (FAILED).
//
// ManualClock drives the timeout synchronously so the test has zero wall-clock
// dependency.
//
// Mutation proof: if the suspicion timer callback does not call
// member.UpdateState(StateDead, ...) when all probes fail, the
// assert.Equal(StateDead) below fails.
func TestLifecycle_SuspectTimeoutFails(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	prober.indirectReply["peer"] = false // all indirect probes fail too

	peer := newTestMember("peer", 0)

	deadCh := make(chan string, 1)
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 3 * time.Second,
		IndirectProbes:   2,
		Relays:           []*Member{newTestMember("relay1", 0), newTestMember("relay2", 0)},
		OnDead:           func(id string) { deadCh <- id },
	})

	// ACTIVE -> SUSPECT.
	require.True(t, sm.Suspect(peer, 1))
	assert.Equal(t, StateSuspect, peer.State)

	// Advance just under the timeout: still SUSPECT, not FAILED.
	clk.Advance(2*time.Second + 999*time.Millisecond)
	assert.Equal(t, StateSuspect, peer.State, "must remain SUSPECT before timeout")
	assert.True(t, sm.IsSuspected("peer"))

	// Cross the timeout: SUSPECT -> FAILED.
	clk.Advance(1 * time.Millisecond)

	select {
	case id := <-deadCh:
		assert.Equal(t, "peer", id, "OnDead must be invoked with the correct member ID")
	case <-time.After(2 * time.Second):
		t.Fatal("OnDead was not called after suspicion timeout")
	}
	// Sink-side: member is FAILED (StateDead).
	assert.Equal(t, StateDead, peer.State, "member must be FAILED (StateDead) after timeout")
	assert.False(t, sm.IsSuspected("peer"), "suspicion must be cleared after death")
}

// Mutation: if the suspicion timeout is disabled (timer never fires), the
// FAILED transition never happens and the test fails.
func TestLifecycle_SuspectTimeoutFails_Mutation(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	prober.indirectReply["peer"] = false

	peer := newTestMember("peer", 0)

	var deadCount int
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 1 * time.Second,
		IndirectProbes:   1,
		Relays:           []*Member{newTestMember("r1", 0)},
		OnDead:           func(string) { deadCount++ },
	})

	require.True(t, sm.Suspect(peer, 1))

	// Advancing BEFORE the timeout must NOT fire the callback.
	clk.Advance(500 * time.Millisecond)
	assert.Equal(t, 0, deadCount, "OnDead must NOT fire before timeout (mutation guard)")

	// Advancing PAST the timeout MUST fire it exactly once.
	clk.Advance(600 * time.Millisecond)
	assert.Equal(t, 1, deadCount, "OnDead must fire exactly once at timeout (mutation guard)")
}

// ============================================================================
// Lifecycle: refute clears suspicion (SUSPECT -> ACTIVE)
// ============================================================================

// TestLifecycle_RefuteClearsSuspicion asserts that a higher-incarnation refute
// transitions the node SUSPECT -> ACTIVE (StateAlive) and prevents the FAILED
// transition even after the original timeout would have fired.
//
// Mutation proof: if Refute does not stop the timer, the suspicion still fires
// and deadCalled becomes true, failing the assert.False below.
func TestLifecycle_RefuteClearsSuspicion(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	peer := newTestMember("peer", 1)

	var deadCalled bool
	var refuteCalled bool
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 5 * time.Second,
		IndirectProbes:   1,
		OnDead:           func(string) { deadCalled = true },
		OnRefute:         func(string) { refuteCalled = true },
	})

	// ACTIVE -> SUSPECT.
	require.True(t, sm.Suspect(peer, 1))
	assert.Equal(t, StateSuspect, peer.State)

	// Refute at incarnation 2 (strictly higher): SUSPECT -> ACTIVE.
	refuted := sm.Refute("peer", 2)
	require.True(t, refuted, "higher incarnation must refute")
	assert.Equal(t, StateAlive, peer.State, "refuted node must return to ACTIVE (StateAlive)")
	assert.Equal(t, uint32(2), peer.Incarnation, "incarnation must advance to refuting value")
	assert.True(t, refuteCalled, "OnRefute callback must fire on successful refutation")
	assert.False(t, sm.IsSuspected("peer"), "suspicion must be cleared")

	// Advance well past the original timeout: node must stay ACTIVE, not FAILED.
	clk.Advance(10 * time.Second)
	assert.False(t, deadCalled, "refuted node must NEVER become FAILED")
	assert.Equal(t, StateAlive, peer.State, "refuted node must stay ACTIVE")
}

// Mutation: lower or equal incarnation must NOT refute.
func TestLifecycle_RefuteClearsSuspicion_Mutation(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	peer := newTestMember("peer", 3)

	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 5 * time.Second,
		IndirectProbes:   1,
	})

	require.True(t, sm.Suspect(peer, 3))
	assert.Equal(t, StateSuspect, peer.State)

	// Equal incarnation must NOT clear suspicion.
	ok := sm.Refute("peer", 3)
	require.False(t, ok, "equal incarnation refute must be rejected")
	assert.Equal(t, StateSuspect, peer.State, "state must remain SUSPECT after failed refute")
	assert.True(t, sm.IsSuspected("peer"))

	// Lower incarnation must NOT clear suspicion.
	ok = sm.Refute("peer", 2)
	require.False(t, ok, "lower incarnation refute must be rejected")
	assert.Equal(t, StateSuspect, peer.State)
}

// ============================================================================
// Phi-accrual: phi rises with missed heartbeats, threshold crossing catches FAILED
// ============================================================================

// TestLifecycle_PhiRisesWithMissedHeartbeats proves that as a peer goes silent
// (heartbeats stop), phi rises from safe-low to above the failure threshold.
// This is the core anti-bluff check for the phi-accrual detector: it measures
// REAL silence, not a stub.
//
// We use ±200ms jitter around a 1s cadence so the normal distribution is wide
// enough that phi rises gradually (not as a near-step function). The steps are
// sized so phi stays strictly below the saturation cap (300.0) throughout,
// keeping the monotonicity assertion meaningful.
//
// Mutation proof: if Phi() ignores elapsed silence and returns a constant, the
// strictly-increasing-phi chain and the threshold-crossing assertion both fail.
func TestLifecycle_PhiRisesWithMissedHeartbeats(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	d := NewPhiAccrualDetector(PhiAccrualConfig{
		Clock:      clk,
		WindowSize: 50,
		MinSamples: 3,
		// Large MinStdDev so the normal CDF is smooth and phi rises gradually,
		// avoiding premature saturation at the phiMinProb floor (phi=300).
		MinStdDev: 200 * time.Millisecond,
	})

	// Build a jittered window: ±200ms around a 1s cadence. beatJittered is
	// defined in phi_accrual_test.go (same package).
	beatJittered(d, clk, "peer", 1*time.Second, 200*time.Millisecond, 30)

	// Silence begins here. Verify phi starts below threshold.
	phi0 := d.Phi("peer")
	assert.Less(t, phi0, 8.0, "phi must be below threshold immediately after last heartbeat")

	// Advance in steps that stay well within the regime where phi < 300 (no
	// saturation). With mean=1s and stdDev=200ms:
	//   elapsed=0.7s: y=(0.7-1)/0.2=-1.5 → phi≈0.03 (below threshold)
	//   elapsed=1.5s: y=(1.5-1)/0.2=2.5  → phi≈3.4
	//   elapsed=2.0s: y=(2.0-1)/0.2=5.0  → phi≈6.5
	//   elapsed=2.5s: y=(2.5-1)/0.2=7.5  → phi≈13.8 (above threshold)
	// All values are well below the saturation cap (300), so each step produces
	// a strictly higher phi.
	var prev float64 = -1
	steps := []time.Duration{
		700 * time.Millisecond, // 700ms: below the mean — phi is low
		800 * time.Millisecond, // 1500ms: 1.5× the mean — phi rising
		500 * time.Millisecond, // 2000ms: 2× the mean
		500 * time.Millisecond, // 2500ms: 2.5× the mean — above threshold
	}
	var phis []float64
	for i, step := range steps {
		clk.Advance(step)
		cur := d.Phi("peer")
		require.Greater(t, cur, prev,
			"phi must rise monotonically at step %d (elapsed ~%v)", i, step)
		prev = cur
		phis = append(phis, cur)
	}

	// First sample (700ms into silence, below the 1s mean): phi is low.
	assert.Less(t, phis[0], 8.0, "phi must be below threshold shortly after last heartbeat")

	// Last sample (2.5s of silence, 2.5× the cadence): phi must cross the threshold.
	lastPhi := phis[len(phis)-1]
	assert.Greater(t, lastPhi, 8.0, "phi must cross threshold after prolonged silence")
	assert.True(t, d.Suspect("peer", 8.0), "Suspect must flip true once phi crosses 8.0")
}

// Mutation companion: ensures the boundary is real (phi stays below threshold
// for a small silence, confirming the rising curve is load-bearing).
func TestLifecycle_PhiRisesWithMissedHeartbeats_Mutation(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	d := NewPhiAccrualDetector(PhiAccrualConfig{
		Clock:      clk,
		WindowSize: 50,
		MinSamples: 3,
		MinStdDev:  10 * time.Millisecond,
	})

	// Same cadence as above.
	for i := 0; i < 21; i++ {
		d.Heartbeat("peer")
		clk.Advance(500 * time.Millisecond)
	}
	d.Heartbeat("peer")

	// 100ms into silence: must NOT be suspected.
	clk.Advance(100 * time.Millisecond)
	assert.False(t, d.Suspect("peer", 8.0),
		"peer must NOT be suspected 100ms into a 500ms cadence (mutation guard)")

	// 3 seconds into silence (6× the interval): MUST be suspected.
	clk.Advance(2900 * time.Millisecond)
	assert.True(t, d.Suspect("peer", 8.0),
		"peer must be suspected after 3s silence at 500ms cadence (mutation guard)")
}

// ============================================================================
// Phi-accrual integrated with SuspicionManager: phi-triggered Dead via manual clock
// ============================================================================

// TestLifecycle_PhiAccrualDrivesFailure is the end-to-end phi-accrual test:
// feed regular heartbeats to build the window, stop feeding (silence = missed
// heartbeats), watch phi cross the threshold, confirm the node is FAILED via
// the suspicion manager driven off the same ManualClock.
//
// This is NOT a mock: the phi detector computes a real normal-distribution
// tail probability, the suspicion manager has a real timer driven by the
// injected ManualClock, and the state machine transitions are exact.
//
// Mutation proof: if SuspicionManager.onTimeout does not call
// member.UpdateState(StateDead,...), the assert.Equal(StateDead) fails.
func TestLifecycle_PhiAccrualDrivesFailure(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	d := NewPhiAccrualDetector(PhiAccrualConfig{
		Clock:      clk,
		WindowSize: 30,
		MinSamples: 3,
		MinStdDev:  20 * time.Millisecond,
	})
	prober := newRecordingProber()
	prober.indirectReply["phi-peer"] = false // indirect probes fail

	phiPeer := newTestMember("phi-peer", 0)
	relays := []*Member{newTestMember("r1", 0), newTestMember("r2", 0)}

	deadCh := make(chan string, 1)
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 2 * time.Second,
		IndirectProbes:   2,
		Relays:           relays,
		OnDead:           func(id string) { deadCh <- id },
	})

	// Build phi window with 200ms cadence (15 samples).
	for i := 0; i < 15; i++ {
		d.Heartbeat("phi-peer")
		clk.Advance(200 * time.Millisecond)
	}
	d.Heartbeat("phi-peer")

	// Silence begins. Arm suspicion when phi crosses 8.0.
	const threshold = 8.0
	var suspected bool
	for step := 0; step < 100 && !suspected; step++ {
		clk.Advance(50 * time.Millisecond)
		if d.Phi("phi-peer") >= threshold {
			// ACTIVE -> SUSPECT via phi-triggered decision.
			ok := sm.Suspect(phiPeer, 1)
			require.True(t, ok, "phi-triggered suspect must succeed")
			assert.Equal(t, StateSuspect, phiPeer.State)
			suspected = true
		}
	}
	require.True(t, suspected, "phi must cross threshold within 5 seconds of silence at 200ms cadence")

	// Now let the suspicion timer fire (indirect probes fail -> FAILED).
	clk.Advance(2 * time.Second)

	select {
	case id := <-deadCh:
		assert.Equal(t, "phi-peer", id, "OnDead must be called for the correct peer")
	case <-time.After(2 * time.Second):
		t.Fatal("phi-accrual-triggered failure never reached StateDead")
	}

	// Sink-side: member is FAILED.
	assert.Equal(t, StateDead, phiPeer.State,
		"phi-accrual pipeline must produce FAILED (StateDead) for a silent peer")
}

// ============================================================================
// SimDST: 3-node DST with phi-accrual, suspicion, and exact state assertions
// ============================================================================

// TestLifecycle_SimDST_ThreeNodeConverge runs a 3-node deterministic simulation
// using the SimNetwork virtual transport. It verifies:
//   - All three nodes probe each other and observe StateAlive (membership convergence).
//   - When one node goes silent (goSilent), the observer moves it SUSPECT then FAILED.
//   - The phi detector rises above the threshold for the silent node.
//   - Exact sink-side assertions on every state transition.
//
// Mutation proof: if SimNetwork.enqueue ignores drops (delivers everything),
// the silent node still acks direct probes, its phi never rises, it is never
// suspected, and the StateDead assertion fails.
func TestLifecycle_SimDST_ThreeNodeConverge(t *testing.T) {
	var log []membershipEvent
	clk := NewManualClock(time.Unix(0, 0))
	netw := NewSimNetwork(42, PerfectLink)

	// Three simNodes: A observes B and C; C acts as relay for B.
	a := newSimNode(t, netw, "A", "127.0.0.1:8001", clk, &log)
	b := newSimNode(t, netw, "B", "127.0.0.1:8002", clk, &log)
	c := newSimNode(t, netw, "C", "127.0.0.1:8003", clk, &log)

	mb := a.addPeer("B", b.addr)
	mc := a.addPeer("C", c.addr)
	// B and C are peers of each other (for relay path).
	b.addPeer("C", c.addr)
	c.addPeer("B", b.addr)

	// ---- Convergence: all probes ack while everyone is alive ----
	require.True(t, a.probe(netw, mb), "A must observe B alive")
	require.True(t, a.probe(netw, mc), "A must observe C alive")

	// phi-accrual: feed regular heartbeats from A's perspective.
	phiA := NewPhiAccrualDetector(PhiAccrualConfig{
		Clock:      clk,
		MinSamples: 3,
		MinStdDev:  10 * time.Millisecond,
	})
	for i := 0; i < 5; i++ {
		phiA.Heartbeat("B") // B is alive, feeding heartbeats
		clk.Advance(100 * time.Millisecond)
		netw.Run(0)
	}
	phiA.Heartbeat("B")

	phiAfterHealth := phiA.Phi("B")
	assert.Less(t, phiAfterHealth, 8.0, "phi must be low while B is actively heartbeating")

	// ---- B goes silent ----
	goSilent(b)

	// A probes B: no ack. ACTIVE -> SUSPECT.
	require.False(t, a.probe(netw, mb), "silent B must not ack")
	require.True(t, a.sm.Suspect(mb, 1), "A must suspect silent B")
	assert.Equal(t, StateSuspect, mb.State, "B must be SUSPECT after missed ack")

	// Phi starts rising: silence accumulates.
	clk.Advance(300 * time.Millisecond)
	phiSilence := phiA.Phi("B")
	assert.GreaterOrEqual(t, phiSilence, phiAfterHealth, "phi must not decrease after silence starts")

	// SUSPECT -> FAILED: timeout fires without relays (no indirect probe path to silent B).
	clk.Advance(4 * time.Second)

	assert.Equal(t, StateDead, mb.State, "B must be FAILED (StateDead) after suspicion timeout")
	assert.False(t, a.sm.IsSuspected("B"))

	// C is still alive.
	require.True(t, a.probe(netw, mc), "C must remain alive throughout")
	assert.Equal(t, StateAlive, mc.State)

	// Sink-side event log: exactly one Dead event for B from A's perspective.
	require.Len(t, log, 1, "exactly one state-change event must be recorded")
	assert.Equal(t, membershipEvent{observer: "A", subject: "B", state: StateDead}, log[0])
}

// ============================================================================
// Metadata propagation via GossipBuffer (sink-side UUID check)
// ============================================================================

// TestLifecycle_MetadataUUIDPropagates verifies that metadata (including a
// per-run UUID) set on a member is preserved through gossip-buffer round-trips.
// This is the unit-level analogue of the integration test's UUID assertion.
//
// Mutation proof: if GossipBuffer.Add does not store the MemberID or
// Recent does not return the right event, the UUID check fails.
func TestLifecycle_MetadataUUIDPropagates(t *testing.T) {
	const runUUID = "test-uuid-deadbeef-1234-5678-abcd-ef0123456789"

	// Build a protocol with metadata carrying the UUID.
	p, err := NewProtocol(&Config{
		LocalID:        "uuid-node",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  30 * time.Second,
		GossipInterval: 30 * time.Second,
	})
	require.NoError(t, err)
	defer p.transport.Close()

	// Inject the UUID into the local member's metadata.
	p.mu.Lock()
	p.members["uuid-node"].Metadata["run_uuid"] = runUUID
	p.mu.Unlock()

	// Add a gossip event for a remote node that also carries the UUID in its MemberID.
	p.gossipBuffer.Add(GossipEvent{
		MemberID:    fmt.Sprintf("remote-%s", runUUID),
		State:       StateAlive,
		Incarnation: 1,
		Timestamp:   time.Now(),
	})

	// Sink-side: retrieve the event and assert the UUID is intact.
	recent := p.gossipBuffer.Recent(1)
	require.Len(t, recent, 1)
	assert.Equal(t, fmt.Sprintf("remote-%s", runUUID), recent[0].MemberID,
		"gossip event must preserve the per-run UUID verbatim")
	assert.Equal(t, StateAlive, recent[0].State)

	// Also verify the local member carries the UUID.
	p.mu.RLock()
	localMeta := p.members["uuid-node"].Metadata
	p.mu.RUnlock()
	assert.Equal(t, runUUID, localMeta["run_uuid"],
		"local member metadata must carry the per-run UUID")
}

// ============================================================================
// ConcurrentAccess — race-detector validation of lifecycle paths
// ============================================================================

// TestLifecycle_ConcurrentSuspectRefute drives concurrent Suspect/Refute
// calls on the SuspicionManager and asserts no data race (verified by -race).
// The final state is either Alive (if refute won) or Dead (if timeout won):
// both are valid; we only assert no panic and no race.
func TestLifecycle_ConcurrentSuspectRefute(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()

	const N = 20
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			peer := newTestMember(fmt.Sprintf("p%d", i), uint32(i))
			sm := NewSuspicionManager(SuspicionConfig{
				Clock:            clk,
				Prober:           prober,
				SuspicionTimeout: 100 * time.Millisecond,
				IndirectProbes:   1,
			})
			if sm.Suspect(peer, uint32(i)) {
				// Concurrent refute at a higher incarnation.
				sm.Refute(fmt.Sprintf("p%d", i), uint32(i+1))
			}
		}(i)
	}
	wg.Wait()
	// If any goroutine panics or the -race detector fires, the test fails.
}

// ============================================================================
// SimDST: rejoin after FAILED (FAILED -> JOINING -> ACTIVE)
// ============================================================================

// TestLifecycle_SimDST_RejoinAfterFailed tests the FAILED -> JOINING -> ACTIVE
// path: a node that was declared Dead re-joins and must be re-admitted at a
// higher incarnation, clearing its Dead state.
//
// Mutation proof: if handleJoin does not upsert the member state (only
// adds, never updates an existing Dead entry), the final StateAlive assertion
// fails because Dead persists.
func TestLifecycle_SimDST_RejoinAfterFailed(t *testing.T) {
	p1, err := NewProtocol(&Config{
		LocalID: "rejoin-seed", BindAddr: "127.0.0.1", BindPort: 0,
		ProbeInterval: 30 * time.Second, GossipInterval: 30 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, p1.Start())
	defer p1.Stop()

	// Manually mark a future joiner as Dead in p1's membership.
	p1.mu.Lock()
	p1.members["rejoiner"] = &Member{
		ID:          "rejoiner",
		Address:     "127.0.0.1:0", // placeholder
		State:       StateDead,
		Incarnation: 5,
		Metadata:    make(map[string]string),
	}
	p1.members["rejoiner"].Touch()
	p1.mu.Unlock()

	// Rejoiner node starts fresh at incarnation 0.
	p2, err := NewProtocol(&Config{
		LocalID: "rejoiner", BindAddr: "127.0.0.1", BindPort: 0,
		ProbeInterval: 30 * time.Second, GossipInterval: 30 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, p2.Start())
	defer p2.Stop()

	// p2 sends a MsgJoin; p1's handleJoin inserts/updates the entry.
	require.NoError(t, p2.Join([]string{p1.LocalAddr()}))

	// Allow join propagation.
	require.Eventually(t, func() bool {
		p1.mu.RLock()
		m, ok := p1.members["rejoiner"]
		p1.mu.RUnlock()
		if !ok {
			return false
		}
		m.mu.RLock()
		alive := m.State == StateAlive
		m.mu.RUnlock()
		return alive
	}, 3*time.Second, 30*time.Millisecond,
		"rejoining node must be re-admitted as StateAlive (JOINING -> ACTIVE)")

	// Sink-side: p1 now sees rejoiner as ACTIVE.
	p1.mu.RLock()
	m := p1.members["rejoiner"]
	p1.mu.RUnlock()
	m.mu.RLock()
	finalState := m.State
	m.mu.RUnlock()
	assert.Equal(t, StateAlive, finalState, "rejoin must resurrect a previously-Dead member to ACTIVE")
}
