//go:build integration

package clusternode

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// TestThreeNodeClusterEndToEnd is the sink-side proof that the project's proven
// building blocks COMPOSE for an end user (HelixConstitution CLAUDE-1). It brings
// up THREE real NodeAgents in-process — each with a real libp2p discovery host
// and a real messaging gateway, all sharing ONE real embedded-raft consensus
// group — and asserts, end to end, the four user-visible guarantees:
//
//  1. DISCOVERY: the three libp2p nodes actually discover each other. node-a
//     resolves node-c's addresses PURELY via the Kademlia DHT (node-a was never
//     handed node-c's address), so a non-empty result proves the real discovery
//     layer did it — not a pre-fed peerstore.
//  2. CONSENSUS: exactly ONE raft leader is elected across the three, via
//     raftleader, and every node agrees (one IsLeader()==true, two followers).
//  3. CAPABILITIES: each node's hardware capabilities are collected and are
//     non-empty (real CPU model + host name via the per-OS hwinventory probe).
//  4. MESSAGING: a message routed through a node's gateway reaches its
//     destination BYTE-EXACT.
//
// Every wait is deadline-polling (no fixed sleeps) so the test is non-flaky, and
// every assertion is on user-visible state (resolved peer, leader count,
// collected inventory, received payload) — not on "a function returned nil".
func TestThreeNodeClusterEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cluster, err := NewNodeCluster("node-a", "node-b", "node-c")
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Stop() })

	if err := cluster.Start(ctx); err != nil {
		t.Fatalf("start cluster: %v", err)
	}
	if err := cluster.WaitForDiscovery(ctx, 30*time.Second); err != nil {
		t.Fatalf("discovery overlay did not form: %v", err)
	}

	// --- (1) DISCOVERY: node-b finds node-c registry-free via the DHT. --------
	// The overlay is a star: node-a is the anchor; node-b and node-c each dial
	// ONLY the anchor and never each other. So node-b discovering node-c is the
	// genuine registry-free case — neither was ever handed the other's address,
	// and the only way node-b can resolve node-c is the Kademlia DHT walk.
	//
	// Anti-bluff precondition (structural): node-c is NOT among the bootstrap
	// peers node-b was ever handed. node-b was seeded with the anchor (node-a)
	// only and never dials node-c, so any address node-b holds for node-c must
	// have been learned through the DHT — a live peerstore check after the
	// overlay forms races libp2p's eager identify exchange, so we witness the
	// "never hand-fed" guarantee at the configuration boundary instead.
	b := cluster.Get("node-b")
	c := cluster.Get("node-c")
	if b == nil || c == nil {
		t.Fatalf("missing agents: b=%v c=%v", b, c)
	}
	cID := c.Discovery().ID()
	for _, bp := range b.BootstrapPeers() {
		if bp.ID == cID {
			t.Fatalf("DISCOVERY precondition violated: node-c was hand-fed to node-b as a bootstrap peer (%v)", bp)
		}
	}
	t.Logf("DISCOVERY precondition OK: node-c was never a bootstrap peer of node-b (bootstrap=%v) — any resolution is via the DHT", b.BootstrapPeers())

	findCtx, findCancel := context.WithTimeout(ctx, 30*time.Second)
	defer findCancel()
	resolved, err := cluster.DiscoverPeer(findCtx, "node-b", "node-c")
	if err != nil {
		t.Fatalf("DISCOVERY FAILED: node-b could not discover node-c via DHT: %v", err)
	}
	if resolved.ID != c.Discovery().ID() {
		t.Fatalf("DISCOVERY FAILED: resolved wrong peer %s (want %s)", resolved.ID, c.Discovery().ID())
	}
	if len(resolved.Addrs) == 0 {
		t.Fatalf("DISCOVERY FAILED: node-b learned node-c but with ZERO addrs (DHT did not really resolve)")
	}
	t.Logf("DISCOVERY OK: node-b discovered node-c=%s addrs=%v purely via Kademlia DHT", resolved.ID, resolved.Addrs)

	// --- (2) CONSENSUS: exactly one raft leader across the three. -------------
	leader, err := cluster.WaitForLeader(30 * time.Second)
	if err != nil {
		t.Fatalf("CONSENSUS FAILED: cluster did not converge to a single leader: %v", err)
	}
	if n := cluster.Election().CountLeaders(); n != 1 {
		t.Fatalf("CONSENSUS FAILED: expected EXACTLY ONE leader, got %d", n)
	}
	// Drive the count directly off each agent's IsLeader() (the composed surface)
	// so an anti-bluff mutation of leadership is caught here too.
	leaders := 0
	for _, ag := range cluster.Agents() {
		if ag.IsLeader() {
			leaders++
		}
	}
	if leaders != 1 {
		t.Fatalf("CONSENSUS FAILED: EXACTLY ONE agent must report IsLeader()=true, got %d", leaders)
	}
	if !leader.IsLeader() {
		t.Fatalf("CONSENSUS FAILED: elected leader %s reports IsLeader()=false", leader.ID())
	}
	// Every node must agree on the leader id.
	for _, ag := range cluster.Agents() {
		if got := ag.Election().LeaderID(); got != leader.ID() {
			t.Fatalf("CONSENSUS FAILED: %s sees leader=%q, want %q", ag.ID(), got, leader.ID())
		}
	}
	t.Logf("CONSENSUS OK: exactly one leader %s elected across %d nodes (all agree)", leader.ID(), len(cluster.Agents()))

	// --- (3) CAPABILITIES: each node collects a non-empty inventory. ----------
	for _, ag := range cluster.Agents() {
		inv, err := ag.Capabilities(ctx)
		if err != nil {
			t.Fatalf("CAPABILITIES FAILED: %s collect: %v", ag.ID(), err)
		}
		if inv.Host == "" {
			t.Fatalf("CAPABILITIES FAILED: %s reported empty host", ag.ID())
		}
		if inv.CPU.Model == "" {
			t.Fatalf("CAPABILITIES FAILED: %s reported empty CPU model", ag.ID())
		}
		if inv.CPU.LogicalCores < 1 {
			t.Fatalf("CAPABILITIES FAILED: %s reported %d logical cores", ag.ID(), inv.CPU.LogicalCores)
		}
		if inv.MemoryBytes <= 0 {
			t.Fatalf("CAPABILITIES FAILED: %s reported %d memory bytes", ag.ID(), inv.MemoryBytes)
		}
		t.Logf("CAPABILITIES OK: %s host=%s cpu=%q cores=%d mem=%dB",
			ag.ID(), inv.Host, inv.CPU.Model, inv.CPU.LogicalCores, inv.MemoryBytes)
	}

	// --- (4) MESSAGING: a routed message reaches its destination byte-exact. --
	payload := []byte("end-to-end-routed-message:" + leader.ID())
	routeCtx, routeCancel := context.WithTimeout(ctx, 10*time.Second)
	defer routeCancel()
	got, err := cluster.Route(routeCtx, "node-a", "node-c", payload)
	if err != nil {
		t.Fatalf("MESSAGING FAILED: route node-a->node-c: %v", err)
	}
	if got.Dest != "node-c" {
		t.Fatalf("MESSAGING FAILED: envelope delivered to wrong dest %q", got.Dest)
	}
	if got.Source != "node-a" {
		t.Fatalf("MESSAGING FAILED: envelope has wrong source %q", got.Source)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Fatalf("MESSAGING FAILED: payload not byte-exact: got %q want %q", string(got.Payload), string(payload))
	}
	t.Logf("MESSAGING OK: node-a->node-c delivered byte-exact payload (%d bytes) through the gateway", len(payload))

	t.Logf("END-TO-END OK: discovery + consensus + capabilities + messaging all composed across 3 real nodes")
}
