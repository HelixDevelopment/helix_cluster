package swim

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// manualClock — deterministic injectable clock (test double)
// ============================================================================

func TestManualClock_AdvanceDrivesTimers(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))

	fired := make(chan struct{}, 1)
	stop := clk.AfterFunc(100*time.Millisecond, func() {
		fired <- struct{}{}
	})
	defer stop()

	// Before advancing, nothing should fire.
	select {
	case <-fired:
		t.Fatal("timer fired before clock advanced")
	case <-time.After(20 * time.Millisecond):
	}

	// Advance short of deadline: still nothing.
	clk.Advance(50 * time.Millisecond)
	select {
	case <-fired:
		t.Fatal("timer fired before deadline reached")
	case <-time.After(20 * time.Millisecond):
	}

	// Advance past deadline: must fire.
	clk.Advance(60 * time.Millisecond)
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("timer did not fire after clock advanced past deadline")
	}
}

func TestManualClock_AdvanceDrivesTimers_Mutation(t *testing.T) {
	// Mutation: Advance does not move time forward => timer must NOT fire.
	clk := NewManualClock(time.Unix(0, 0))
	fired := make(chan struct{}, 1)
	stop := clk.AfterFunc(100*time.Millisecond, func() { fired <- struct{}{} })
	defer stop()

	// Advancing strictly less than the deadline must never fire.
	clk.Advance(99 * time.Millisecond)
	select {
	case <-fired:
		t.Fatal("timer fired before deadline — clock advanced too far / mutation")
	case <-time.After(50 * time.Millisecond):
		// correct: not yet fired
	}
}

func TestManualClock_StopPreventsFire(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	fired := make(chan struct{}, 1)
	stop := clk.AfterFunc(10*time.Millisecond, func() { fired <- struct{}{} })

	require.True(t, stop(), "stop should report it cancelled a pending timer")
	clk.Advance(time.Second)

	select {
	case <-fired:
		t.Fatal("stopped timer still fired")
	case <-time.After(50 * time.Millisecond):
	}
}

// ============================================================================
// SuspicionManager — incarnation refutation
// ============================================================================

// recordingProber records direct/indirect probe attempts and lets the test
// decide whether each probe succeeds. Deterministic, no real network.
type recordingProber struct {
	mu             sync.Mutex
	directTargets  []string
	indirectCalls  []indirectCall
	directReply    map[string]bool // memberID -> direct ping succeeds
	indirectReply  map[string]bool // memberID -> indirect ping succeeds
}

type indirectCall struct {
	target string
	relays []string
}

func newRecordingProber() *recordingProber {
	return &recordingProber{
		directReply:   map[string]bool{},
		indirectReply: map[string]bool{},
	}
}

func (rp *recordingProber) DirectProbe(target *Member) bool {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.directTargets = append(rp.directTargets, target.ID)
	return rp.directReply[target.ID]
}

func (rp *recordingProber) IndirectProbe(target *Member, relays []*Member) bool {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	ids := make([]string, 0, len(relays))
	for _, r := range relays {
		ids = append(ids, r.ID)
	}
	rp.indirectCalls = append(rp.indirectCalls, indirectCall{target: target.ID, relays: ids})
	return rp.indirectReply[target.ID]
}

func (rp *recordingProber) directCount() int {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return len(rp.directTargets)
}

func (rp *recordingProber) indirectCount() int {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return len(rp.indirectCalls)
}

func (rp *recordingProber) lastIndirect() (indirectCall, bool) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	if len(rp.indirectCalls) == 0 {
		return indirectCall{}, false
	}
	return rp.indirectCalls[len(rp.indirectCalls)-1], true
}

func newTestMember(id string, inc uint32) *Member {
	m := &Member{ID: id, Address: "127.0.0.1:0", State: StateAlive, Incarnation: inc, Metadata: map[string]string{}}
	m.Touch()
	return m
}

// A higher incarnation refutes an existing suspicion: Suspect -> Alive.
func TestSuspicionManager_HigherIncarnationRefutes(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	target := newTestMember("node-2", 1)

	var deadCalled bool
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:          clk,
		Prober:         prober,
		SuspicionTimeout: 1 * time.Second,
		IndirectProbes: 2,
		OnDead:         func(string) { deadCalled = true },
	})

	// Suspect at incarnation 1.
	require.True(t, sm.Suspect(target, 1))
	assert.Equal(t, StateSuspect, target.State)
	require.True(t, sm.IsSuspected("node-2"))

	// Refute with a strictly higher incarnation: must override Suspect with Alive.
	refuted := sm.Refute("node-2", 2)
	require.True(t, refuted, "higher incarnation must refute")
	assert.Equal(t, StateAlive, target.State, "member must return to Alive after refutation")
	assert.Equal(t, uint32(2), target.Incarnation, "incarnation must be bumped to refuting value")
	require.False(t, sm.IsSuspected("node-2"), "suspicion must be cleared after refutation")

	// Even if the suspicion timeout fully elapses now, the node must NOT die.
	clk.Advance(2 * time.Second)
	assert.False(t, deadCalled, "refuted node must never be declared Dead")
	assert.NotEqual(t, StateDead, target.State)
}

// Mutation: a NON-higher incarnation must NOT refute an active suspicion.
func TestSuspicionManager_HigherIncarnationRefutes_Mutation(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	target := newTestMember("node-2", 5)

	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 1 * time.Second,
		IndirectProbes:   2,
	})

	require.True(t, sm.Suspect(target, 5))

	// Equal incarnation must NOT refute a suspicion (Alive < Suspect priority).
	require.False(t, sm.Refute("node-2", 5), "equal incarnation must not refute")
	assert.Equal(t, StateSuspect, target.State)
	require.True(t, sm.IsSuspected("node-2"))

	// Lower incarnation must NOT refute.
	require.False(t, sm.Refute("node-2", 4), "lower incarnation must not refute")
	assert.Equal(t, StateSuspect, target.State)
	require.True(t, sm.IsSuspected("node-2"))
}

// ============================================================================
// SuspicionManager — suspicion -> dead with indirect probes
// ============================================================================

// Before the suspicion timeout elapses, the node must NOT be marked Dead.
func TestSuspicionManager_NotDeadBeforeTimeout(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	target := newTestMember("node-2", 1)

	var deadIDs []string
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 1 * time.Second,
		IndirectProbes:   3,
		OnDead:           func(id string) { deadIDs = append(deadIDs, id) },
	})

	require.True(t, sm.Suspect(target, 1))

	// Advance to just shy of the timeout.
	clk.Advance(999 * time.Millisecond)
	assert.Empty(t, deadIDs, "node must NOT be Dead before suspicion timeout")
	assert.NotEqual(t, StateDead, target.State)
	require.True(t, sm.IsSuspected("node-2"))
}

// Mutation: prove the boundary — crossing the timeout DOES kill (so the
// "not dead before timeout" assertion above is meaningful, not vacuous).
func TestSuspicionManager_DeadAtTimeout_Mutation(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	prober.indirectReply["node-2"] = false // indirect probes fail too
	target := newTestMember("node-2", 1)

	deadCh := make(chan string, 1)
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 1 * time.Second,
		IndirectProbes:   3,
		Relays:           []*Member{newTestMember("r1", 1), newTestMember("r2", 1)},
		OnDead:           func(id string) { deadCh <- id },
	})

	require.True(t, sm.Suspect(target, 1))
	clk.Advance(1 * time.Second)

	select {
	case id := <-deadCh:
		assert.Equal(t, "node-2", id)
	case <-time.After(time.Second):
		t.Fatal("node was not declared Dead at suspicion timeout")
	}
	assert.Equal(t, StateDead, target.State)
}

// Indirect probes MUST be attempted before declaring Dead, and if an indirect
// probe succeeds the node is refuted (not killed).
func TestSuspicionManager_IndirectProbeRefutesBeforeDead(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	prober.indirectReply["node-2"] = true // an indirect relay reaches the target
	target := newTestMember("node-2", 1)
	relays := []*Member{newTestMember("r1", 1), newTestMember("r2", 1), newTestMember("r3", 1)}

	var deadCalled bool
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 1 * time.Second,
		IndirectProbes:   2,
		Relays:           relays,
		OnDead:           func(string) { deadCalled = true },
	})

	require.True(t, sm.Suspect(target, 1))

	// Fire the timeout: the manager must attempt indirect probes first.
	clk.Advance(1 * time.Second)

	require.GreaterOrEqual(t, prober.indirectCount(), 1, "indirect probes MUST be attempted before declaring Dead")
	ic, ok := prober.lastIndirect()
	require.True(t, ok)
	assert.Equal(t, "node-2", ic.target)
	assert.LessOrEqual(t, len(ic.relays), 2, "must use at most IndirectProbes relays")
	assert.NotEmpty(t, ic.relays, "must select relays for indirect probe")

	// Indirect probe reached the node -> refuted, not dead.
	assert.False(t, deadCalled, "successful indirect probe must prevent Dead")
	assert.NotEqual(t, StateDead, target.State)
	require.False(t, sm.IsSuspected("node-2"), "successful indirect probe clears suspicion")
}

// Mutation: prove indirect probes are actually attempted — if the manager
// skipped them, a reachable node would be wrongly declared Dead.
func TestSuspicionManager_IndirectProbeAttempted_Mutation(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	prober.indirectReply["node-2"] = true // node IS reachable indirectly
	target := newTestMember("node-2", 1)
	relays := []*Member{newTestMember("r1", 1), newTestMember("r2", 1)}

	var deadCalled bool
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 500 * time.Millisecond,
		IndirectProbes:   2,
		Relays:           relays,
		OnDead:           func(string) { deadCalled = true },
	})

	require.True(t, sm.Suspect(target, 1))
	clk.Advance(500 * time.Millisecond)

	// If indirect probing were skipped (mutation), deadCalled would be true.
	require.Equal(t, 1, prober.indirectCount(), "exactly one indirect probe round must run")
	require.False(t, deadCalled, "reachable node must not die when indirect probe succeeds")
}

// When direct + indirect both fail and the timeout elapses, node becomes Dead.
func TestSuspicionManager_DeadWhenAllProbesFail(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	prober.indirectReply["node-2"] = false
	target := newTestMember("node-2", 1)
	relays := []*Member{newTestMember("r1", 1), newTestMember("r2", 1)}

	deadCh := make(chan string, 1)
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 1 * time.Second,
		IndirectProbes:   2,
		Relays:           relays,
		OnDead:           func(id string) { deadCh <- id },
	})

	require.True(t, sm.Suspect(target, 1))
	clk.Advance(1 * time.Second)

	require.GreaterOrEqual(t, prober.indirectCount(), 1, "indirect probes attempted before death")
	select {
	case id := <-deadCh:
		assert.Equal(t, "node-2", id)
	case <-time.After(time.Second):
		t.Fatal("node not declared Dead though all probes failed and timeout elapsed")
	}
	assert.Equal(t, StateDead, target.State)
}

// Refutation that arrives after the timeout fired (race) must be a no-op once
// dead; but a refutation arriving before death stops the timer.
func TestSuspicionManager_RefuteStopsTimeout(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	target := newTestMember("node-2", 1)

	var deadCalled bool
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 1 * time.Second,
		IndirectProbes:   2,
		OnDead:           func(string) { deadCalled = true },
	})

	require.True(t, sm.Suspect(target, 1))
	clk.Advance(500 * time.Millisecond)
	require.True(t, sm.Refute("node-2", 2)) // higher incarnation refutes mid-suspicion

	clk.Advance(10 * time.Second) // long past original deadline
	assert.False(t, deadCalled, "refuted-then-timeout must not kill")
	assert.Equal(t, StateAlive, target.State)
	require.Equal(t, 0, prober.indirectCount(), "no indirect probe after refute stops the timer")
}

// Re-suspecting at an equal or higher incarnation must not reset an already
// running suspicion into a no-op; lower incarnation suspicion is rejected.
func TestSuspicionManager_SuspectRejectsLowerIncarnation(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	prober := newRecordingProber()
	target := newTestMember("node-2", 5)

	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 1 * time.Second,
		IndirectProbes:   2,
	})

	// Suspicion at a stale incarnation must be rejected (UpdateState semantics).
	require.False(t, sm.Suspect(target, 4), "stale-incarnation suspect must be rejected")
	assert.Equal(t, StateAlive, target.State)
	require.False(t, sm.IsSuspected("node-2"))
}

// ============================================================================
// Integration: hardened suspicion over the REAL UDP transport (no mocks).
// Proves end-user-visible behaviour: a live node's indirect probe succeeds and
// prevents Dead; an unreachable node is declared Dead only after the timeout +
// failed indirect probes.
// ============================================================================

// A real, listening relay reaches a real, listening target on our behalf, so an
// indirect probe at timeout refutes the suspicion instead of killing it.
func TestHardenedSuspicion_RealTransport_IndirectProbeKeepsNodeAlive(t *testing.T) {
	// Local protocol that owns the suspicion manager + prober.
	local, err := NewProtocol(&Config{
		LocalID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		ProbeInterval: 5 * time.Second, GossipInterval: 5 * time.Second,
		ProbeTimeout: 150 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NoError(t, local.Start())
	defer local.Stop()

	// Real target node that answers pings.
	target, err := NewProtocol(&Config{
		LocalID: "target", BindAddr: "127.0.0.1", BindPort: 0,
		ProbeInterval: 5 * time.Second, GossipInterval: 5 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, target.Start())
	defer target.Stop()

	// Real relay node that forwards ping-req to the target.
	relay, err := NewProtocol(&Config{
		LocalID: "relay", BindAddr: "127.0.0.1", BindPort: 0,
		ProbeInterval: 5 * time.Second, GossipInterval: 5 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, relay.Start())
	defer relay.Stop()

	targetMember := &Member{ID: "target", Address: target.LocalAddr(), State: StateAlive, Metadata: map[string]string{}}
	targetMember.Touch()
	relayMember := &Member{ID: "relay", Address: relay.LocalAddr(), State: StateAlive, Metadata: map[string]string{}}
	relayMember.Touch()

	// Register members so local's handleAck can update targetMember's lastSeen.
	local.mu.Lock()
	local.members["target"] = targetMember
	local.members["relay"] = relayMember
	local.mu.Unlock()

	// Sanity: the opt-in protocol accessor builds a usable manager (real prober).
	require.NotNil(t, local.NewHardenedSuspicion(nil, nil, nil))

	clk := NewManualClock(time.Unix(0, 0))
	var deadCalled bool
	// Construct directly so the suspicion timeout is driven by the manual clock.
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           newTransportProber(local.transport, "local", 200*time.Millisecond),
		SuspicionTimeout: 1 * time.Second,
		IndirectProbes:   2,
		Relays:           []*Member{relayMember},
		OnDead:           func(string) { deadCalled = true },
	})

	require.True(t, sm.Suspect(targetMember, 1))
	require.True(t, sm.IsSuspected("target"))

	// Fire the timeout: the prober sends a real ping-req to the relay, which
	// forwards a real ping to the target, which acks local -> targetMember
	// lastSeen advances -> indirect probe reports success -> refuted.
	clk.Advance(1 * time.Second)

	assert.False(t, deadCalled, "live node reachable via relay must NOT be declared Dead")
	assert.NotEqual(t, StateDead, snapshotState(targetMember))
	require.False(t, sm.IsSuspected("target"), "successful indirect probe clears suspicion")
}

// snapshotState reads a member's State under its lock (race-safe for tests).
func snapshotState(m *Member) MemberState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.State
}

// With no reachable target and no relays able to reach it, the hardened
// suspicion declares the node Dead after the timeout.
func TestHardenedSuspicion_RealTransport_UnreachableNodeDies(t *testing.T) {
	local, err := NewProtocol(&Config{
		LocalID: "local", BindAddr: "127.0.0.1", BindPort: 0,
		ProbeInterval: 5 * time.Second, GossipInterval: 5 * time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, local.Start())
	defer local.Stop()

	// A dead target address: bind then immediately close so nothing answers.
	deadConn, err := NewTransport("127.0.0.1", 0)
	require.NoError(t, err)
	deadAddr := deadConn.LocalAddr().String()
	deadConn.Close()

	targetMember := &Member{ID: "ghost", Address: deadAddr, State: StateAlive, Metadata: map[string]string{}}
	targetMember.Touch()
	local.mu.Lock()
	local.members["ghost"] = targetMember
	local.mu.Unlock()

	clk := NewManualClock(time.Unix(0, 0))
	deadCh := make(chan string, 1)
	sm := NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           newTransportProber(local.transport, "local", 100*time.Millisecond),
		SuspicionTimeout: 1 * time.Second,
		IndirectProbes:   2,
		Relays:           []*Member{}, // no relays reachable
		OnDead:           func(id string) { deadCh <- id },
	})

	require.True(t, sm.Suspect(targetMember, 1))
	clk.Advance(1 * time.Second)

	select {
	case id := <-deadCh:
		assert.Equal(t, "ghost", id)
	case <-time.After(2 * time.Second):
		t.Fatal("unreachable node was not declared Dead after timeout")
	}
	assert.Equal(t, StateDead, snapshotState(targetMember))
}
