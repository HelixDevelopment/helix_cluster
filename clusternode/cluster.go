package clusternode

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/discovery/p2p"
	"github.com/HelixDevelopment/helix_cluster/edge/gateway"
	"github.com/HelixDevelopment/helix_cluster/pkg/raftleader"

	"github.com/libp2p/go-libp2p/core/peer"
)

// NodeCluster is an in-process cluster of NodeAgents that share ONE real raft
// consensus group. It is the integration harness the demo and the end-to-end
// test drive: it owns the shared raftleader.ElectionCluster, constructs one
// NodeAgent per raft seat, and provides the bring-up helpers (discovery
// bootstrap, leader wait, cross-node routing) an end user would expect from a
// "bring up a 3-node cluster" entry point.
type NodeCluster struct {
	election *raftleader.ElectionCluster
	agents   []*NodeAgent
	byID     map[string]*NodeAgent

	// mu guards routes. routes caches, per (source agent, destination id), the
	// single loopback observer endpoint that the source's gateway routes the
	// destination's envelopes to. It is populated lazily on first Route and
	// reused on every subsequent Route to the same destination, so repeated
	// Route calls do NOT keep appending fresh conns under the same id (each of
	// which would otherwise receive a copy of the envelope into a bounded inbox
	// that is never drained). Reusing one conn per pair keeps the inbox balanced
	// — every Route drains exactly the one envelope it sent — and makes Route
	// safely repeatable.
	mu     sync.Mutex
	routes map[string]map[string]*gateway.LoopbackConn
}

// NewNodeCluster builds an N-node in-process cluster. It creates ONE shared raft
// election group across the given ids (raftleader.NewInmemElectionCluster), then
// wraps each raft seat in a NodeAgent. The agents are constructed but NOT yet
// started — call Start to bring the discovery hosts and gateways online and to
// form the discovery overlay.
//
// ids must be unique and non-empty (the raft layer enforces this).
func NewNodeCluster(ids ...string) (*NodeCluster, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("clusternode: cluster needs at least one node id")
	}

	ec, err := raftleader.NewInmemElectionCluster(nil, ids...)
	if err != nil {
		return nil, fmt.Errorf("clusternode: build raft election group: %w", err)
	}

	agents := make([]*NodeAgent, 0, len(ids))
	byID := make(map[string]*NodeAgent, len(ids))
	for _, id := range ids {
		seat := ec.Get(id)
		if seat == nil {
			_ = ec.Close()
			return nil, fmt.Errorf("clusternode: raft group has no seat for id %q", id)
		}
		agent, err := NewNodeAgent(Config{
			ID:       id,
			Election: seat,
			Logger:   p2p.NopLogger{},
		})
		if err != nil {
			_ = ec.Close()
			return nil, err
		}
		agents = append(agents, agent)
		byID[id] = agent
	}

	return &NodeCluster{
		election: ec,
		agents:   agents,
		byID:     byID,
		routes:   make(map[string]map[string]*gateway.LoopbackConn),
	}, nil
}

// Agents returns the cluster's NodeAgents in id order.
func (c *NodeCluster) Agents() []*NodeAgent { return c.agents }

// Get returns the agent with the given id, or nil.
func (c *NodeCluster) Get(id string) *NodeAgent { return c.byID[id] }

// Election exposes the shared raft election group (e.g. for CountLeaders /
// WaitForSingleLeader).
func (c *NodeCluster) Election() *raftleader.ElectionCluster { return c.election }

// Start brings every agent's per-node subsystems online and forms the discovery
// overlay: the FIRST agent is the anchor (no bootstrap peers); every other agent
// is started with the anchor's AddrInfo as its sole bootstrap peer, then the
// whole set runs DHT bootstrap so the Kademlia routing tables fill. After Start
// returns the discovery hosts are connected and the DHT is seeded; callers use
// DiscoverPeers / the agents' Discovery() to exercise registry-free discovery.
func (c *NodeCluster) Start(ctx context.Context) error {
	if len(c.agents) == 0 {
		return fmt.Errorf("clusternode: no agents to start")
	}

	// Track which agents this call actually brought online so that ANY error
	// path below tears the started subset back down before returning — Start
	// must leave nothing running (no leaked libp2p hosts / gateway goroutines)
	// when it reports failure. NodeAgent.Start already cleans up after itself on
	// its own internal failure; this guards the cross-agent bring-up sequence.
	var started []*NodeAgent
	ok := false
	defer func() {
		if ok {
			return
		}
		// Roll back in reverse start order. NodeAgent.Stop is idempotent and
		// leaves the shared raft group (owned by the NodeCluster) untouched.
		for i := len(started) - 1; i >= 0; i-- {
			_ = started[i].Stop()
		}
	}()

	// 1. Start the anchor first so we can hand its AddrInfo to the joiners.
	anchor := c.agents[0]
	if err := anchor.Start(ctx); err != nil {
		return err
	}
	started = append(started, anchor)
	anchorInfo := anchor.AddrInfo()

	// 2. Start every joiner with the anchor as its bootstrap peer.
	for _, a := range c.agents[1:] {
		a.bootstrap = []peer.AddrInfo{anchorInfo}
		if err := a.Start(ctx); err != nil {
			return err
		}
		started = append(started, a)
	}

	// 3. Seed the DHT on every node. Joiners dial the anchor (their bootstrap
	//    peer); the anchor bootstraps too so it learns the joiners as they dial
	//    in, filling every routing table for the registry-free Kademlia walk.
	for _, a := range c.agents {
		if err := a.Discovery().Bootstrap(ctx); err != nil {
			return fmt.Errorf("clusternode: bootstrap discovery for %q: %w", a.ID(), err)
		}
	}

	ok = true
	return nil
}

// WaitForDiscovery blocks until every agent's DHT routing table holds at least
// one peer (so the overlay has genuinely formed), or the timeout elapses. This
// is outcome-based polling, not a fixed sleep.
func (c *NodeCluster) WaitForDiscovery(ctx context.Context, timeout time.Duration) error {
	for _, a := range c.agents {
		if err := a.Discovery().WaitForRoutingTable(ctx, 1, timeout); err != nil {
			return fmt.Errorf("clusternode: discovery overlay for %q: %w", a.ID(), err)
		}
	}
	return nil
}

// DiscoverPeer resolves target's discovery addresses from finder's point of view
// PURELY through the Kademlia DHT (finder.FindPeer). The caller must not have
// pre-fed finder with target's address; a successful, non-empty result proves
// the discovery layer located the peer registry-free. Returns the resolved
// AddrInfo.
func (c *NodeCluster) DiscoverPeer(ctx context.Context, finderID, targetID string) (peer.AddrInfo, error) {
	finder := c.byID[finderID]
	target := c.byID[targetID]
	if finder == nil || target == nil {
		return peer.AddrInfo{}, fmt.Errorf("clusternode: discover %q->%q: unknown agent", finderID, targetID)
	}
	resolved, err := finder.Discovery().FindPeer(ctx, target.Discovery().ID())
	if err != nil {
		return peer.AddrInfo{}, fmt.Errorf("clusternode: %q could not discover %q via DHT: %w", finderID, targetID, err)
	}
	return resolved, nil
}

// WaitForLeader blocks until the shared raft group converges to exactly one
// leader, returning that agent. It resolves leadership through the raftleader
// adapter (so an anti-bluff mutation of IsLeader is caught) and is outcome-based.
func (c *NodeCluster) WaitForLeader(timeout time.Duration) (*NodeAgent, error) {
	seat, err := c.election.WaitForSingleLeader(timeout)
	if err != nil {
		return nil, fmt.Errorf("clusternode: %w", err)
	}
	agent := c.byID[seat.ID()]
	if agent == nil {
		return nil, fmt.Errorf("clusternode: leader seat %q has no agent", seat.ID())
	}
	return agent, nil
}

// Route delivers a dispatch envelope from the source agent to the destination
// agent and returns the envelope the destination actually received on its inbox,
// so the caller can assert byte-exact delivery.
//
// Each agent owns its own gateway (as three real machines would), so to route
// across agents in-process we register the destination agent's own endpoint as
// an observer on the SOURCE agent's gateway under the destination id, then route.
// The gateway's routing core is transport-agnostic and unicast for dispatch, so
// the envelope is delivered to exactly that endpoint and to no other — proving
// real gateway routing, not a direct channel hand-off.
func (c *NodeCluster) Route(ctx context.Context, srcID, dstID string, payload []byte) (gateway.Envelope, error) {
	src := c.byID[srcID]
	dst := c.byID[dstID]
	if src == nil || dst == nil {
		return gateway.Envelope{}, fmt.Errorf("clusternode: route %q->%q: unknown agent", srcID, dstID)
	}

	// Register the destination's endpoint as an observer on the source's gateway
	// so the source gateway has a route to dstID — but do it ONCE per (src,dst)
	// and reuse it on every subsequent Route. Connecting afresh each call would
	// append another conn under dstID; the routing table delivers a dispatch to
	// ALL conns for an id, so repeated Route calls would fan the envelope to
	// every stale conn whose bounded inbox is never drained, and eventually fill
	// them. Caching one conn per pair keeps the inbox balanced (this Route drains
	// exactly the one envelope it sent) and makes Route safely repeatable.
	dstConn, err := c.routeConn(src, dstID)
	if err != nil {
		return gateway.Envelope{}, fmt.Errorf("clusternode: wire route %q->%q: %w", srcID, dstID, err)
	}

	env := gateway.Envelope{
		ID:       fmt.Sprintf("%s->%s-%d", srcID, dstID, time.Now().UnixNano()),
		Kind:     gateway.KindDispatch,
		Source:   srcID,
		Dest:     dstID,
		Payload:  payload,
		IssuedAt: time.Now(),
	}
	if err := src.Send(env); err != nil {
		return gateway.Envelope{}, fmt.Errorf("clusternode: route %q->%q: %w", srcID, dstID, err)
	}

	select {
	case got, ok := <-dstConn.Inbox:
		if !ok {
			return gateway.Envelope{}, fmt.Errorf("clusternode: route %q->%q: destination inbox closed", srcID, dstID)
		}
		return got, nil
	case <-ctx.Done():
		return gateway.Envelope{}, fmt.Errorf("clusternode: route %q->%q: %w", srcID, dstID, ctx.Err())
	}
}

// routeConn returns the single loopback observer endpoint for dstID on src's
// gateway, creating and registering it on first use and reusing it thereafter.
// One conn per (src,dst) pair is what makes Route safely repeatable: the gateway
// delivers a dispatch to every conn registered under dstID, so a fresh conn per
// call would leave stale, never-drained inboxes behind. The conns are owned by
// src's loopback transport and are torn down when the agent (and thus its
// gateway) is stopped in Stop.
func (c *NodeCluster) routeConn(src *NodeAgent, dstID string) (*gateway.LoopbackConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	bySrc := c.routes[src.ID()]
	if bySrc == nil {
		bySrc = make(map[string]*gateway.LoopbackConn)
		c.routes[src.ID()] = bySrc
	}
	if conn := bySrc[dstID]; conn != nil {
		return conn, nil
	}
	conn, err := src.loop.Connect(dstID)
	if err != nil {
		return nil, err
	}
	bySrc[dstID] = conn
	return conn, nil
}

// Stop tears down every agent (discovery + gateway) and shuts down the shared
// raft group. Safe to call once at the end of a run.
func (c *NodeCluster) Stop() error {
	var firstErr error
	for _, a := range c.agents {
		if err := a.Stop(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := c.election.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
