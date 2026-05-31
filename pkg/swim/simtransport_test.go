package swim

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Deterministic-Simulation Transport (DST) tests.
//
// These wire N SWIM endpoints together over an in-memory, deterministic,
// virtual-clock SimNetwork (NO real sockets, NO real time) and run a full
// membership scenario (join, suspect, fail, recover) to a reproducible
// conclusion. The seeded RNG + logical event queue make the entire schedule a
// pure function of the seed.
// ============================================================================

// simNode is a minimal SWIM endpoint built on a SimTransport. It replicates the
// production Ping/Ack/PingReq handler logic and drives suspicion through a
// SuspicionManager on a ManualClock, so the DST exercises the same membership
// state machine end users depend on — not a stand-in.
type simNode struct {
	id        string
	addr      string
	transport *SimTransport
	members   map[string]*Member // peers (incl. self)
	clock     *ManualClock
	sm        *SuspicionManager
	events    *[]membershipEvent // shared, ordered transition log
}

type membershipEvent struct {
	observer string
	subject  string
	state    MemberState
}

func newSimNode(t *testing.T, n *SimNetwork, id, addr string, clk *ManualClock, log *[]membershipEvent) *simNode {
	t.Helper()
	st, err := n.NewTransport(addr)
	require.NoError(t, err)

	sn := &simNode{
		id:        id,
		addr:      st.LocalAddr().String(),
		transport: st,
		members:   make(map[string]*Member),
		clock:     clk,
		events:    log,
	}

	// Suspicion manager wired to this node's transport via the seam prober and
	// the deterministic ManualClock. Indirect probe uses 1 relay.
	prober := newTransportProber(st, id, 0)
	// Drive the prober's internal wait off the SimNetwork: when the prober
	// "waits", advance the network so the in-flight Ping/Ack actually deliver.
	prober.wait = func(time.Duration) { n.Run(0) }

	sn.sm = NewSuspicionManager(SuspicionConfig{
		Clock:            clk,
		Prober:           prober,
		SuspicionTimeout: 4 * time.Second,
		IndirectProbes:   1,
		OnDead: func(memberID string) {
			*log = append(*log, membershipEvent{observer: id, subject: memberID, state: StateDead})
		},
		OnRefute: func(memberID string) {
			*log = append(*log, membershipEvent{observer: id, subject: memberID, state: StateAlive})
		},
	})

	// Ping -> Ack (mirrors Protocol.handlePing incl. indirect-probe origin ack).
	st.RegisterHandler(MsgPing, func(msg *Message, from net.Addr) {
		ack := &Message{Type: MsgAck, SourceID: id, TargetID: msg.SourceID}
		if a, err := net.ResolveUDPAddr("udp", from.String()); err == nil {
			st.Send(ack, *a)
		}
		if len(msg.Payload) > 0 {
			if origin, err := net.ResolveUDPAddr("udp", string(msg.Payload)); err == nil {
				st.Send(ack, *origin)
			}
		}
	})

	// PingReq -> forward Ping to target carrying the origin address (relay path).
	st.RegisterHandler(MsgPingReq, func(msg *Message, from net.Addr) {
		taddr, err := net.ResolveUDPAddr("udp", string(msg.Payload))
		if err != nil {
			return
		}
		ping := &Message{Type: MsgPing, SourceID: id, TargetID: msg.TargetID, Payload: []byte(from.String())}
		st.Send(ping, *taddr)
	})

	// Ack -> Touch the acked member (advances lastSeen; clears suspicion locally).
	st.RegisterHandler(MsgAck, func(msg *Message, from net.Addr) {
		if m, ok := sn.members[msg.SourceID]; ok {
			m.Touch()
			m.ClearSuspect()
		}
	})

	return sn
}

// addPeer registers a peer member known to this node.
func (sn *simNode) addPeer(id, addr string) *Member {
	m := &Member{ID: id, Address: addr, State: StateAlive, Metadata: map[string]string{}}
	m.Touch()
	sn.members[id] = m
	return m
}

// probe performs one direct probe round against target: send a Ping, run the
// network (delivering the Ping and any resulting Ack), and report whether the
// target acked (its lastSeen advanced).
func (sn *simNode) probe(n *SimNetwork, target *Member) bool {
	before := target.LastSeen()
	taddr, err := net.ResolveUDPAddr("udp", target.Address)
	if err != nil {
		return false
	}
	sn.transport.Send(&Message{Type: MsgPing, SourceID: sn.id, TargetID: target.ID}, *taddr)
	n.Run(0)
	return target.LastSeen().After(before)
}

// failNode marks a target endpoint silent: it stops answering pings. We model
// this by removing its Ping handler so probes get no Ack.
func goSilent(target *simNode) {
	target.transport.RegisterHandler(MsgPing, func(*Message, net.Addr) {})
	target.transport.RegisterHandler(MsgPingReq, func(*Message, net.Addr) {})
}

// dropDirectToPolicy drops only direct Pings whose destination is victimAddr
// AND that did NOT arrive via a relay (empty Payload). Relay-forwarded Pings
// carry the origin address in Payload and are allowed through, modelling a
// one-way partition that an indirect probe can route around.
func dropDirectToPolicy(victimAddr string) DeliveryPolicy {
	return func(_ *SimNetwork, _, to string, msg *Message) (time.Duration, bool) {
		if to == victimAddr && msg.Type == MsgPing && len(msg.Payload) == 0 {
			return 0, true // drop direct probe to the victim
		}
		return 0, false
	}
}

// ---------------------------------------------------------------------------
// Test: a node that stops responding is marked Suspect then Dead after the
// configured suspicion timeout. Asserts the EXACT state transitions sink-side.
//
// Mutation that makes this FAIL: in SimTransport.Send, ignore the policy/closed
// check and always deliver (e.g. delete the `if drop { n.drops++; return }`
// branch in SimNetwork.enqueue) — then the silenced node still gets probed in a
// way that acks, lastSeen advances, suspicion is refuted, and it never reaches
// StateDead, failing the StateDead assertion below.
// ---------------------------------------------------------------------------
func TestSimDST_SuspectThenDead(t *testing.T) {
	var log []membershipEvent
	clk := NewManualClock(time.Unix(0, 0))
	netw := NewSimNetwork(42, PerfectLink)

	a := newSimNode(t, netw, "A", "127.0.0.1:7001", clk, &log)
	b := newSimNode(t, netw, "B", "127.0.0.1:7002", clk, &log)

	// A knows B.
	mb := a.addPeer("B", b.addr)

	// Sanity: while B answers, a probe acks and B stays alive.
	require.True(t, a.probe(netw, mb), "healthy probe must ack")
	assert.Equal(t, StateAlive, mb.State)

	// B goes silent (stops answering pings/ping-reqs).
	goSilent(b)

	// A probes B: no ack. After the failed probe round, A suspects B.
	require.False(t, a.probe(netw, mb), "silent node must not ack")
	require.True(t, a.sm.Suspect(mb, 1), "suspect must arm")
	assert.Equal(t, StateSuspect, mb.State, "node must be Suspect after failed probe")
	assert.True(t, a.sm.IsSuspected("B"))

	// Advance the virtual clock past the suspicion timeout. There are no relays,
	// so the indirect probe is skipped and B is confirmed Dead.
	clk.Advance(4 * time.Second)

	assert.Equal(t, StateDead, mb.State, "node must be Dead after suspicion timeout")
	assert.False(t, a.sm.IsSuspected("B"))

	// Sink-side: the ordered event log must record Suspect(not via log)->Dead.
	// OnDead fired exactly once for B.
	require.Len(t, log, 1)
	assert.Equal(t, membershipEvent{observer: "A", subject: "B", state: StateDead}, log[0])
}

// Mutation guard companion: prove that if dropped messages were actually
// delivered, the node would be wrongly refuted and NOT die. We construct the
// same silent-node scenario but force the SuspicionManager's indirect probe to
// see an ack by NOT silencing B, and assert it does NOT die — locking in that
// "Dead" is contingent on real non-delivery.
func TestSimDST_DropIsLoadBearing_Mutation(t *testing.T) {
	var log []membershipEvent
	clk := NewManualClock(time.Unix(0, 0))
	netw := NewSimNetwork(42, PerfectLink)

	a := newSimNode(t, netw, "A", "127.0.0.1:7001", clk, &log)
	b := newSimNode(t, netw, "B", "127.0.0.1:7002", clk, &log)
	c := newSimNode(t, netw, "C", "127.0.0.1:7003", clk, &log)
	mb := a.addPeer("B", b.addr)
	mc := a.addPeer("C", c.addr)

	// Suspect B but provide C as a relay. B is NOT silent, so the timeout's
	// indirect probe reaches B via C and B is refuted, never Dead.
	require.True(t, a.sm.SuspectWithRelays(mb, 1, []*Member{mc}))
	clk.Advance(4 * time.Second)

	assert.NotEqual(t, StateDead, mb.State, "reachable node must be refuted, not killed")
	// Sink-side: a refute event (Alive), no Dead event for B.
	for _, e := range log {
		assert.NotEqual(t, StateDead, e.state, "no node should be declared Dead when reachable")
	}
}

// ---------------------------------------------------------------------------
// Test: indirect-probe via a relay recovers a node behind a one-way drop.
//
// Mutation that makes this FAIL: make SimTransport deliver dropped messages
// (drop the `if to == victimAddr ... return 0,true` short-circuit in
// dropDirectToPolicy, or have SimNetwork.enqueue ignore the policy's drop=true).
// Then either the direct probe would already ack (no suspicion) OR — and this is
// the assertion the task names — a SimTransport that delivers dropped messages
// fails the "node behind drop -> Dead/Suspect" expectation. Here we assert the
// relay path is REQUIRED: with the one-way drop and NO relay the node dies;
// with a relay it recovers.
// ---------------------------------------------------------------------------
func TestSimDST_IndirectProbeRecoversBehindDrop(t *testing.T) {
	var log []membershipEvent
	clk := NewManualClock(time.Unix(0, 0))

	// One-way drop: direct Pings to B (victim) are lost; relay-forwarded Pings
	// (non-empty Payload) get through.
	netw := NewSimNetwork(7, dropDirectToPolicy("127.0.0.1:7002"))

	a := newSimNode(t, netw, "A", "127.0.0.1:7001", clk, &log)
	b := newSimNode(t, netw, "B", "127.0.0.1:7002", clk, &log)
	c := newSimNode(t, netw, "C", "127.0.0.1:7003", clk, &log)

	mb := a.addPeer("B", b.addr)
	mc := a.addPeer("C", c.addr)

	// Direct probe to B is dropped one-way: no ack.
	require.False(t, a.probe(netw, mb), "direct probe must be dropped one-way")
	require.Equal(t, 1, netw.Drops(), "exactly one direct ping dropped")

	// Suspect B, providing C as a relay. On timeout, the indirect probe routes
	// A->C->B->A around the one-way drop, B acks, and the suspicion is refuted.
	require.True(t, a.sm.SuspectWithRelays(mb, 1, []*Member{mc}))
	assert.Equal(t, StateSuspect, mb.State)

	clk.Advance(4 * time.Second)

	assert.NotEqual(t, StateDead, mb.State, "indirect probe via relay must recover B")
	assert.Equal(t, StateAlive, mb.State, "B refuted back to Alive via relay")

	// Sink-side: exactly one refute (Alive) event for B, no Dead.
	require.Len(t, log, 1)
	assert.Equal(t, membershipEvent{observer: "A", subject: "B", state: StateAlive}, log[0])
}

// Mutation companion for the relay test: WITHOUT a relay, the same one-way drop
// kills B. This proves the relay is load-bearing and that the drop is real.
func TestSimDST_NoRelayBehindDropDies(t *testing.T) {
	var log []membershipEvent
	clk := NewManualClock(time.Unix(0, 0))
	netw := NewSimNetwork(7, dropDirectToPolicy("127.0.0.1:7002"))

	a := newSimNode(t, netw, "A", "127.0.0.1:7001", clk, &log)
	_ = newSimNode(t, netw, "B", "127.0.0.1:7002", clk, &log)
	mb := a.addPeer("B", "127.0.0.1:7002")

	require.False(t, a.probe(netw, mb))
	require.True(t, a.sm.Suspect(mb, 1)) // no relays configured
	clk.Advance(4 * time.Second)

	assert.Equal(t, StateDead, mb.State, "no relay + one-way drop => Dead")
	require.Len(t, log, 1)
	assert.Equal(t, StateDead, log[0].state)
}

// ---------------------------------------------------------------------------
// Determinism: the SAME seed reproduces the IDENTICAL sequence of membership
// events and the identical delivery schedule.
//
// Mutation that makes this FAIL: seed the network from a non-deterministic
// source (e.g. NewSimNetwork uses time.Now().UnixNano() instead of `seed`), or
// make deliverNext order non-deterministically (drop the `seq` tie-breaker /
// use a map iteration). Then two same-seed runs diverge and DeepEqual fails.
// ---------------------------------------------------------------------------
func TestSimDST_SameSeedReproducesSchedule(t *testing.T) {
	run := func(seed int64) ([]membershipEvent, []string) {
		var log []membershipEvent
		clk := NewManualClock(time.Unix(0, 0))
		netw := NewSimNetwork(seed, randomDelayPolicy())

		a := newSimNode(t, netw, "A", "127.0.0.1:7001", clk, &log)
		b := newSimNode(t, netw, "B", "127.0.0.1:7002", clk, &log)
		c := newSimNode(t, netw, "C", "127.0.0.1:7003", clk, &log)
		_ = c

		mb := a.addPeer("B", b.addr)
		mc := a.addPeer("C", c.addr)

		// A few probe rounds with random delays, then fail B and let it die.
		var trace []string
		for round := 0; round < 5; round++ {
			ok := a.probe(netw, mb)
			trace = append(trace, fmt.Sprintf("r%d:B=%v@%d", round, ok, netw.Now().UnixNano()))
			okc := a.probe(netw, mc)
			trace = append(trace, fmt.Sprintf("r%d:C=%v@%d", round, okc, netw.Now().UnixNano()))
		}
		goSilent(b)
		a.probe(netw, mb)
		require.True(t, a.sm.SuspectWithRelays(mb, 1, []*Member{mc}))
		clk.Advance(4 * time.Second)
		trace = append(trace, fmt.Sprintf("final:B=%v drops=%d delivered=%d", mb.State, netw.Drops(), netw.Delivered()))
		return log, trace
	}

	log1, trace1 := run(123456)
	log2, trace2 := run(123456)

	assert.Equal(t, log1, log2, "same seed must reproduce identical membership event log")
	assert.Equal(t, trace1, trace2, "same seed must reproduce identical delivery schedule/trace")

	// And a DIFFERENT seed must (with high probability) produce a different
	// schedule — proving the schedule actually depends on the seeded RNG and is
	// not constant. (randomDelayPolicy varies delays from the seeded source.)
	_, trace3 := run(987654)
	assert.NotEqual(t, trace1, trace3, "different seed should change the schedule")
}

// randomDelayPolicy pulls a per-message delay (0..3 logical ms) from the
// network's seeded RNG. Deterministic given the seed; varies across seeds.
func randomDelayPolicy() DeliveryPolicy {
	return func(n *SimNetwork, _, _ string, _ *Message) (time.Duration, bool) {
		return time.Duration(n.Rand().Intn(4)) * time.Millisecond, false
	}
}

// ---------------------------------------------------------------------------
// Seam-level tests for SimTransport / SimNetwork primitives.
// ---------------------------------------------------------------------------

// Mutation that makes this FAIL: remove the drop branch in SimNetwork.enqueue
// (always enqueue) — then Drops() stays 0 and Delivered() is 1, failing both
// assertions.
func TestSimNetwork_PolicyDropsAreReal(t *testing.T) {
	netw := NewSimNetwork(1, func(_ *SimNetwork, _, to string, _ *Message) (time.Duration, bool) {
		return 0, true // drop everything
	})
	src, err := netw.NewTransport("127.0.0.1:9001")
	require.NoError(t, err)
	dst, err := netw.NewTransport("127.0.0.1:9002")
	require.NoError(t, err)

	got := 0
	dst.RegisterHandler(MsgPing, func(*Message, net.Addr) { got++ })

	daddr := dst.LocalAddr().(*net.UDPAddr)
	require.NoError(t, src.Send(&Message{Type: MsgPing, SourceID: "x"}, *daddr))
	netw.Run(0)

	assert.Equal(t, 0, got, "dropped message must not reach handler")
	assert.Equal(t, 1, netw.Drops())
	assert.Equal(t, 0, netw.Delivered())
}

// Mutation that makes this FAIL: in deliverNext, stop advancing n.now to
// env.deliverAt (delete the `if env.deliverAt > n.now { n.now = env.deliverAt }`)
// — then Now() stays at 0 and the deadline-ordering assertion below breaks.
func TestSimNetwork_DelayOrdersDelivery(t *testing.T) {
	netw := NewSimNetwork(1, func(_ *SimNetwork, _, _ string, m *Message) (time.Duration, bool) {
		// Payload byte encodes the intended delay in ms.
		return time.Duration(m.Payload[0]) * time.Millisecond, false
	})
	src, _ := netw.NewTransport("127.0.0.1:9101")
	dst, _ := netw.NewTransport("127.0.0.1:9102")

	var order []byte
	dst.RegisterHandler(MsgPing, func(m *Message, _ net.Addr) {
		order = append(order, m.Payload[0])
	})
	daddr := dst.LocalAddr().(*net.UDPAddr)

	// Enqueue out of delay order: 5ms, 1ms, 3ms. Delivery must be 1,3,5.
	for _, d := range []byte{5, 1, 3} {
		require.NoError(t, src.Send(&Message{Type: MsgPing, Payload: []byte{d}}, *daddr))
	}
	netw.Run(0)

	assert.Equal(t, []byte{1, 3, 5}, order, "messages must deliver in delay order")
	assert.Equal(t, int64(5*time.Millisecond), netw.Now().UnixNano(), "clock advances to last delivery")
}

// Mutation that makes this FAIL: have SimTransport.Send share the caller's
// Payload slice instead of copying it — then mutating the slice after Send
// would change the delivered bytes, failing the assertion.
func TestSimTransport_SendSnapshotsPayload(t *testing.T) {
	netw := NewSimNetwork(1, PerfectLink)
	src, _ := netw.NewTransport("127.0.0.1:9201")
	dst, _ := netw.NewTransport("127.0.0.1:9202")

	var got []byte
	dst.RegisterHandler(MsgPing, func(m *Message, _ net.Addr) { got = m.Payload })
	daddr := dst.LocalAddr().(*net.UDPAddr)

	payload := []byte("hello")
	require.NoError(t, src.Send(&Message{Type: MsgPing, Payload: payload}, *daddr))
	payload[0] = 'J' // mutate AFTER send; must not affect delivered copy
	netw.Run(0)

	assert.Equal(t, "hello", string(got), "delivered payload must be a snapshot")
}

// Mutation that makes this FAIL: make Listen return immediately (nil) instead of
// blocking on ctx — then the goroutine would exit before cancel and the test's
// "still blocked" assertion (no early return) breaks.
func TestSimTransport_ListenBlocksUntilCancel(t *testing.T) {
	netw := NewSimNetwork(1, PerfectLink)
	st, _ := netw.NewTransport("127.0.0.1:9301")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- st.Listen(ctx) }()

	select {
	case <-done:
		t.Fatal("Listen returned before context cancel")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Listen did not return after cancel")
	}
}

// Confirms the real *Transport and *SimTransport are interchangeable through the
// seam (compile + runtime). Mutation: change SimTransport.LocalAddr to return
// nil and this fails resolving back to a UDP addr.
func TestMessageTransport_SeamInterchangeable(t *testing.T) {
	var mt MessageTransport
	netw := NewSimNetwork(1, PerfectLink)
	st, err := netw.NewTransport("127.0.0.1:9401")
	require.NoError(t, err)
	mt = st
	_, ok := mt.LocalAddr().(*net.UDPAddr)
	require.True(t, ok, "sim LocalAddr must be a *net.UDPAddr for handler round-trips")
	require.NoError(t, mt.Close())
	// Send after close must fail (closed check is real).
	err = mt.Send(&Message{Type: MsgPing}, net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9402})
	require.Error(t, err)
}
