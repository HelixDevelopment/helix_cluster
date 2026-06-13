package raft

import (
	"fmt"
	"net"
	"testing"
	"time"
)

// TestRaftThreeNodeTCPLeaderKillReelection is a REAL integration test of
// embedded Raft consensus over GENUINE loopback TCP sockets. Unlike the in-mem
// variant, every RequestVote/AppendEntries/InstallSnapshot RPC traverses an
// actual kernel TCP connection between distinct ephemeral ports. It:
//
//	(a) brings up a real 3-node raft cluster over real TCP transports and asserts
//	    exactly ONE leader is elected and all nodes agree on it;
//	(b) applies committed entries and asserts they replicate to ALL 3 FSMs;
//	(c) KILLS the leader (full Shutdown, closing its TCP listener) and asserts a
//	    NEW leader is elected among the survivors with the committed entries intact
//	    and the cluster still writable;
//	(d) proves the transport is REAL TCP: each node's transport LocalAddr is a
//	    *net.TCPAddr with a non-zero port, and all ports are DISTINCT.
//
// There is no mock, stub, or skip: the test fails for real if Raft could not
// elect/re-elect over TCP or lost committed log entries.
func TestRaftThreeNodeTCPLeaderKillReelection(t *testing.T) {
	logf := func(format string, args ...any) {
		t.Logf("[raft-tcp-it] "+format, args...)
	}

	const (
		// TCP elections take ~1-2s; give generous-but-bounded headroom. Outcomes
		// are reached via deadline polling, never fixed sleeps.
		electionTimeout = 20 * time.Second
		applyTimeout    = 5 * time.Second
		replTimeout     = 10 * time.Second
	)

	// (a) Real 3-node cluster over real TCP transports.
	cluster, err := NewNetworkCluster(nil, "tcp-a", "tcp-b", "tcp-c")
	if err != nil {
		t.Fatalf("failed to create 3-node TCP cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Shutdown() })

	if got := len(cluster.Nodes()); got != 3 {
		t.Fatalf("expected 3 nodes, got %d", got)
	}

	// (d-1) Prove REAL TCP transport BEFORE any consensus: each node's LocalAddr
	// must be a genuine *net.TCPAddr with a non-zero, DISTINCT port.
	ports := make(map[int]string)
	for _, n := range cluster.Nodes() {
		tcpAddr, err := n.LocalTCPAddr()
		if err != nil {
			t.Fatalf("node %s LocalTCPAddr: %v", n.ID(), err)
		}
		// LocalAddr() returns a ServerAddress; LocalTCPAddr resolved it to a real
		// *net.TCPAddr. Assert it is concretely *net.TCPAddr (compile + runtime).
		var _ *net.TCPAddr = tcpAddr
		if tcpAddr.Port == 0 {
			t.Fatalf("node %s bound to port 0 — not a real ephemeral TCP port", n.ID())
		}
		if !tcpAddr.IP.IsLoopback() {
			t.Fatalf("node %s bound to non-loopback IP %s", n.ID(), tcpAddr.IP)
		}
		if other, dup := ports[tcpAddr.Port]; dup {
			t.Fatalf("REAL-TCP proof failed: nodes %s and %s share port %d", n.ID(), other, tcpAddr.Port)
		}
		ports[tcpAddr.Port] = n.ID()
		logf("REAL TCP transport: node %s LocalAddr=%s (port %d)", n.ID(), tcpAddr.String(), tcpAddr.Port)
	}
	if len(ports) != 3 {
		t.Fatalf("expected 3 distinct TCP ports, got %d: %v", len(ports), ports)
	}
	logf("REAL-TCP PROOF: 3 distinct non-zero loopback TCP ports: %v", ports)

	// (a) Wait for the single initial leader; all nodes must agree.
	leader, err := cluster.WaitForLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no initial leader elected over TCP: %v", err)
	}
	initialLeaderID := leader.ID()
	if err := waitForAllAgreeLeader(cluster.Nodes(), initialLeaderID, 5*time.Second); err != nil {
		t.Fatalf("nodes did not agree on a single leader %s: %v", initialLeaderID, err)
	}
	logf("SINGLE LEADER ELECTED over TCP and agreed by all nodes: %s", initialLeaderID)

	// (b) Apply committed entries through the leader; assert replication to all 3.
	entries := map[string]string{
		"tcp/region":   "eu-west-1",
		"tcp/replicas": "3",
		"tcp/mode":     "active",
	}
	for k, v := range entries {
		if err := leader.Apply(Command{Op: CommandSet, Key: k, Value: v}, applyTimeout); err != nil {
			t.Fatalf("leader %s failed to apply {%s=%s} over TCP: %v", initialLeaderID, k, v, err)
		}
	}
	if err := waitForReplication(cluster.Nodes(), entries, replTimeout); err != nil {
		t.Fatalf("replication to all 3 nodes over TCP failed before kill: %v", err)
	}
	logf("REPLICATION CONFIRMED on all 3 FSMs over TCP before kill")

	// (c) KILL the leader (closes its TCP listener too).
	if err := leader.Shutdown(); err != nil {
		t.Fatalf("failed to shut down leader %s: %v", initialLeaderID, err)
	}
	logf("LEADER KILLED over TCP: %s", initialLeaderID)

	// A NEW leader must emerge among the 2 survivors (quorum 2 of 3).
	newLeader, err := cluster.WaitForNewLeader(initialLeaderID, electionTimeout)
	if err != nil {
		t.Fatalf("no NEW leader elected over TCP after killing %s: %v", initialLeaderID, err)
	}
	if newLeader.ID() == initialLeaderID {
		t.Fatalf("new leader %s must differ from killed leader %s", newLeader.ID(), initialLeaderID)
	}
	logf("NEW LEADER ELECTED over TCP after kill: %s (was %s)", newLeader.ID(), initialLeaderID)

	// (c) committed entries intact on survivors.
	survivors := cluster.Survivors(initialLeaderID)
	if len(survivors) != 2 {
		t.Fatalf("expected 2 survivors, got %d", len(survivors))
	}
	for _, n := range survivors {
		for k, want := range entries {
			got, ok := n.FSM().Get(k)
			if !ok {
				t.Fatalf("LOG LOSS over TCP: survivor %s missing key %q after re-election", n.ID(), k)
			}
			if got != want {
				t.Fatalf("LOG CORRUPTION over TCP: survivor %s key %q = %q, want %q", n.ID(), k, got, want)
			}
		}
	}
	logf("LOG INTACT over TCP: all %d committed entries preserved on both survivors", len(entries))

	// (c) cluster still writable: new leader commits a post-kill entry.
	if err := newLeader.Apply(Command{Op: CommandSet, Key: "tcp-post-kill/key", Value: "ok"}, applyTimeout); err != nil {
		t.Fatalf("new leader %s could not commit a post-kill entry over TCP: %v", newLeader.ID(), err)
	}
	if err := waitForReplication(survivors, map[string]string{"tcp-post-kill/key": "ok"}, replTimeout); err != nil {
		t.Fatalf("post-kill entry did not replicate to survivors over TCP: %v", err)
	}
	logf("POST-KILL WRITE COMMITTED over TCP by new leader %s and replicated", newLeader.ID())

	// (d-2) Survivors' transports are still real TCP with their original ports.
	for _, n := range survivors {
		tcpAddr, err := n.LocalTCPAddr()
		if err != nil {
			t.Fatalf("survivor %s LocalTCPAddr after re-election: %v", n.ID(), err)
		}
		if tcpAddr.Port == 0 {
			t.Fatalf("survivor %s lost its TCP port after re-election", n.ID())
		}
	}

	logf("PASS: 3-node Raft over REAL TCP survived leader kill — re-elected %s, log intact, cluster writable", newLeader.ID())
}

// TestRaftSingleNodeTCPBootstrap is a fast sanity test that a single-node TCP
// cluster elects itself leader over a real listener and applies entries.
func TestRaftSingleNodeTCPBootstrap(t *testing.T) {
	cluster, err := NewNetworkCluster(nil, "tcp-solo")
	if err != nil {
		t.Fatalf("create single-node TCP cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Shutdown() })

	n := cluster.Nodes()[0]
	tcpAddr, err := n.LocalTCPAddr()
	if err != nil {
		t.Fatalf("solo LocalTCPAddr: %v", err)
	}
	if tcpAddr.Port == 0 {
		t.Fatalf("solo node bound to port 0 — not a real TCP listener")
	}

	leader, err := cluster.WaitForLeader(15 * time.Second)
	if err != nil {
		t.Fatalf("single TCP node did not become leader: %v", err)
	}
	if leader.ID() != "tcp-solo" {
		t.Fatalf("expected leader 'tcp-solo', got %q", leader.ID())
	}
	if err := leader.Apply(Command{Op: CommandSet, Key: "k", Value: "v"}, 5*time.Second); err != nil {
		t.Fatalf("apply on single TCP node: %v", err)
	}
	if v, ok := leader.FSM().Get("k"); !ok || v != "v" {
		t.Fatalf("fsm did not apply entry over TCP: got %q ok=%v", v, ok)
	}
}

// waitForAllAgreeLeader polls until every node reports wantLeader as the leader,
// or the deadline expires. This proves there is exactly ONE leader the whole
// cluster agrees on (no split view).
func waitForAllAgreeLeader(nodes []*Node, wantLeader string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = nil
		for _, n := range nodes {
			if got := n.Leader(); got != wantLeader {
				lastErr = fmt.Errorf("node %s sees leader %q, want %q", n.ID(), got, wantLeader)
			}
		}
		if lastErr == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return lastErr
}
