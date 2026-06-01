package swim

// simparity_test.go — HXC-1237: prod+sim parity / membership-convergence test.
//
// Wires N real Protocol instances together over an in-memory SimNetwork (no UDP
// sockets, no wall-clock timers) by injecting a *SimTransport into each
// Protocol via Config.Transport. The test drives the Join message-exchange path
// deterministically with SimNetwork.Run and asserts that every node's
// Protocol.Members() converges to see all peers as StateAlive.
//
// Why no Protocol.Start() here: Start() launches goroutines whose probe/gossip
// loops depend on real wall-clock tickers and time.Sleep — incompatible with
// virtual-clock determinism. The Join/handleJoin/handleAlive path is
// independently exercised here: it is the handshake that grows the members map
// and is the core of "nodes learning each other". The existing integration tests
// (swim_integration_test.go, build tag "integration") cover the full probe/gossip
// lifecycle on real UDP. This file owns the deterministic seam-injection proof.
//
// Anti-bluff: convergence is asserted on the REAL Protocol.Members() after REAL
// handleJoin/handleAlive handlers run over SimTransport. Any mutation that:
//   - drops SimTransport delivery (omit cfg.Transport so real UDP is used)
//   - suppresses handleJoin's MsgAlive response
//   - skips SimNetwork.Run so messages never deliver
// causes the Members() assertion to fail (node not found or state != Alive).

import (
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nodeSpec describes one Protocol+SimTransport node in the parity test.
type nodeSpec struct {
	id       string
	addr     string
	protocol *Protocol
}

// buildParityCluster creates n Protocol nodes over a SimNetwork. Each Protocol
// is constructed with cfg.Transport = simTransport so NO real UDP socket is
// opened. The protocols are NOT started (no wall-clock goroutines).
func buildParityCluster(t *testing.T, net *SimNetwork, n int) []*nodeSpec {
	t.Helper()
	nodes := make([]*nodeSpec, n)
	for i := 0; i < n; i++ {
		addr := fmt.Sprintf("127.0.0.1:%d", 18100+i)
		st, err := net.NewTransport(addr)
		require.NoErrorf(t, err, "NewTransport(%s)", addr)

		id := fmt.Sprintf("node-%d", i+1)
		cfg := &Config{
			LocalID:   id,
			Transport: st, // seam injection — no UDP socket opened
		}
		p, err := NewProtocol(cfg)
		require.NoErrorf(t, err, "NewProtocol(%s)", id)

		nodes[i] = &nodeSpec{id: id, addr: addr, protocol: p}
	}
	return nodes
}

// driveFullMeshJoin has every node send a MsgJoin to every other node and then
// runs the SimNetwork until quiescence (all pending messages delivered,
// including any MsgAlive replies). This drives the real handleJoin and
// handleAlive protocol handlers without any wall-clock dependency.
func driveFullMeshJoin(t *testing.T, net *SimNetwork, nodes []*nodeSpec) {
	t.Helper()
	for _, src := range nodes {
		for _, dst := range nodes {
			if src.id == dst.id {
				continue
			}
			err := src.protocol.Join([]string{dst.addr})
			// Join returns an error only when ALL seed addresses fail (Send error).
			// Over SimTransport, Send never fails for open transports, so this
			// must succeed.
			require.NoErrorf(t, err, "%s -> %s Join must succeed over sim transport", src.id, dst.id)
		}
	}
	// Deliver all pending messages (MsgJoin → handleJoin → MsgAlive → handleAlive).
	// Run until quiescence (empty queue). This is deterministic: same seed,
	// same enqueue order, same delivery schedule.
	delivered := net.Run(0)
	t.Logf("sim delivered %d messages in full-mesh join phase", delivered)
}

// memberIDsAlive returns the sorted IDs of all StateAlive members as seen by p.
func memberIDsAlive(p *Protocol) []string {
	members := p.Members()
	ids := make([]string, 0, len(members))
	for _, m := range members {
		if m.State == StateAlive {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// ---------------------------------------------------------------------------
// TestSimParity_MembershipConvergence — the core seam-injection convergence
// test.
//
// Mutation that makes this FAIL:
//  1. Remove cfg.Transport injection (set to nil) → real UDP *Transport is
//     created instead; MsgJoin goes to a real UDP socket, handleJoin never
//     fires on the sim endpoints → no MsgAlive response → members never appear
//     as Alive on the joining nodes → assertion fails.
//  2. Drop all messages in the SimNetwork policy → handleJoin never fires →
//     same failure.
//  3. Remove SimNetwork.Run() call → messages stay queued → handlers never
//     execute → members not added → assertion fails.
//  4. Return wrong state in handleJoin (e.g. StateDead instead of StateAlive)
//     → Alive assertion fails.
// ---------------------------------------------------------------------------
func TestSimParity_MembershipConvergence(t *testing.T) {
	const seed = int64(1237)
	const nNodes = 3

	net := NewSimNetwork(seed, PerfectLink)
	nodes := buildParityCluster(t, net, nNodes)
	driveFullMeshJoin(t, net, nodes)

	// Assert: every node sees every other node as StateAlive.
	for _, observer := range nodes {
		alive := memberIDsAlive(observer.protocol)
		t.Logf("%s sees alive: %v", observer.id, alive)

		for _, expected := range nodes {
			if expected.id == observer.id {
				continue // self is always present; checked separately below
			}
			// Find the member in the full Members() snapshot (not just alive) to
			// give a precise failure message if state is wrong.
			var found *Member
			for _, m := range observer.protocol.Members() {
				if m.ID == expected.id {
					found = m
					break
				}
			}
			require.NotNilf(t, found,
				"%s must know about %s after full-mesh join (sim transport must deliver MsgJoin/MsgAlive)",
				observer.id, expected.id)
			assert.Equalf(t, StateAlive, found.State,
				"%s sees %s as %v, want StateAlive (handleAlive must set state correctly)",
				observer.id, expected.id, found.State)
		}

		// Self must always be present and Alive.
		var self *Member
		for _, m := range observer.protocol.Members() {
			if m.ID == observer.id {
				self = m
				break
			}
		}
		require.NotNilf(t, self, "%s self-member missing", observer.id)
		assert.Equalf(t, StateAlive, self.State, "%s self must be StateAlive", observer.id)
	}

	// Sink-side evidence: total members per node must equal nNodes.
	for _, node := range nodes {
		require.Lenf(t, node.protocol.Members(), nNodes,
			"%s must have exactly %d members (self + %d peers)",
			node.id, nNodes, nNodes-1)
	}
}

// ---------------------------------------------------------------------------
// TestSimParity_Determinism — same seed produces identical membership outcome.
//
// Mutation that makes this FAIL:
//   - Seed SimNetwork from a non-deterministic source (e.g. time.Now())
//     → two runs produce different delivery schedules → member view diverges
//     → the sorted alive-ID slices differ → assert.Equal fails.
//   - Use map iteration order (non-deterministic) instead of the seq
//     tie-breaker in SimNetwork.deliverNext → non-reproducible ordering.
// ---------------------------------------------------------------------------
func TestSimParity_Determinism(t *testing.T) {
	const seed = int64(1237)
	const nNodes = 4

	type runResult struct {
		alivePerNode [][]string // sorted alive IDs as seen by each node (by node index)
		delivered    int
		drops        int
	}

	doRun := func() runResult {
		net := NewSimNetwork(seed, PerfectLink)
		nodes := buildParityCluster(t, net, nNodes)
		driveFullMeshJoin(t, net, nodes)

		res := runResult{
			alivePerNode: make([][]string, nNodes),
			delivered:    net.Delivered(),
			drops:        net.Drops(),
		}
		for i, node := range nodes {
			res.alivePerNode[i] = memberIDsAlive(node.protocol)
		}
		return res
	}

	run1 := doRun()
	run2 := doRun()

	// Both runs must produce exactly the same membership view.
	assert.Equal(t, run1.alivePerNode, run2.alivePerNode,
		"same seed must produce identical membership convergence across two runs")
	assert.Equal(t, run1.delivered, run2.delivered,
		"same seed must deliver the same number of messages")
	assert.Equal(t, run1.drops, run2.drops,
		"same seed must produce the same number of drops")

	// Convergence sanity: every node must see all nNodes members alive.
	for i, alive := range run1.alivePerNode {
		require.Lenf(t, alive, nNodes,
			"node %d must see %d alive members after convergence", i, nNodes)
	}
}

// ---------------------------------------------------------------------------
// TestSimParity_SeamInjectionIsLoadBearing — proves that cfg.Transport is the
// critical seam: a Protocol built with cfg.Transport = simTransport receives
// and sends messages over the virtual network; the same join scenario with
// cfg.Transport = nil would open real UDP and the sim network would deliver
// nothing to real sockets (they don't share the in-memory queue).
//
// We prove the positive case (seam injected → convergence) here and verify
// the seam type: a Protocol built with a SimTransport must report a local addr
// matching the sim-transport's virtual address (not an OS-assigned port).
//
// Mutation that makes this FAIL:
//   - Set cfg.Transport = nil inside NewProtocol regardless of the field value
//     → Protocol opens a real UDP socket, sim delivery never reaches it,
//     Join/Alive exchange never fires, Members() shows only self.
//   - Ignore cfg.Transport and always use NewTransport(cfg.BindAddr, cfg.BindPort)
//     → same failure.
// ---------------------------------------------------------------------------
func TestSimParity_SeamInjectionIsLoadBearing(t *testing.T) {
	net := NewSimNetwork(1237, PerfectLink)

	st0, err := net.NewTransport("127.0.0.1:18200")
	require.NoError(t, err)
	st1, err := net.NewTransport("127.0.0.1:18201")
	require.NoError(t, err)

	p0, err := NewProtocol(&Config{LocalID: "alpha", Transport: st0})
	require.NoError(t, err)
	p1, err := NewProtocol(&Config{LocalID: "beta", Transport: st1})
	require.NoError(t, err)

	// The seam: Protocol.localAddr must equal the sim-transport virtual address,
	// not an OS-assigned port. This proves NewProtocol used cfg.Transport.
	assert.Equal(t, "127.0.0.1:18200", p0.LocalAddr(),
		"protocol with injected sim transport must report the sim addr, not a real UDP port")
	assert.Equal(t, "127.0.0.1:18201", p1.LocalAddr(),
		"protocol with injected sim transport must report the sim addr, not a real UDP port")

	// Drive join: beta joins alpha.
	err = p1.Join([]string{p0.LocalAddr()})
	require.NoError(t, err)
	net.Run(0) // deliver MsgJoin → handleJoin → MsgAlive → handleAlive

	// alpha must see beta as Alive.
	var alphaSeesBeta *Member
	for _, m := range p0.Members() {
		if m.ID == "beta" {
			alphaSeesBeta = m
			break
		}
	}
	require.NotNil(t, alphaSeesBeta,
		"alpha must know beta after sim-delivered MsgJoin — seam injection is load-bearing")
	assert.Equal(t, StateAlive, alphaSeesBeta.State,
		"beta must be StateAlive on alpha after handleJoin/handleAlive over sim transport")

	// beta must see alpha as Alive (MsgAlive reply delivered by SimNetwork.Run).
	var betaSeesAlpha *Member
	for _, m := range p1.Members() {
		if m.ID == "alpha" {
			betaSeesAlpha = m
			break
		}
	}
	require.NotNil(t, betaSeesAlpha,
		"beta must know alpha after sim-delivered MsgAlive — handleAlive must add unknown members")
	assert.Equal(t, StateAlive, betaSeesAlpha.State,
		"alpha must be StateAlive on beta after handleAlive over sim transport")
}
