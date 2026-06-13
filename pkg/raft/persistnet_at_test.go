package raft

import (
	"fmt"
	"net"
	"testing"
	"time"

	hraft "github.com/hashicorp/raft"
)

// freeLoopbackPort binds a loopback TCP listener on an OS-assigned ephemeral
// port, reads the port back, closes the listener, and returns the now-free port.
// This is the standard "pick a free port" pattern: it lets the test choose a
// FIXED bind address for each node up front (the multi-process daemon cannot use
// ephemeral binds, since peers must be configured by known address ahead of
// time). There is an inherent race window between close and re-bind, but the
// window is tiny on loopback and the test reuses the port immediately.
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("freeLoopbackPort: listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("freeLoopbackPort: close: %v", err)
	}
	return port
}

// TestPersistentNetworkAt_ThreeNodeFixedAddrElectReplicate is the in-process
// unit test for the FIXED-bind-address primitive (NewPersistentNetworkNodeAt)
// that the multi-process daemon composes. Unlike NewPersistentNetworkCluster
// (which binds each node to an ephemeral 127.0.0.1:0 port discovered after the
// fact), here each node is bound to a CALLER-CHOSEN fixed 127.0.0.1:<port> and
// every node is handed the SAME explicit servers list up front — exactly how 3
// separate OS processes would be wired. It proves, in one process and under
// -race, that the fixed-address path:
//
//	(1) binds all 3 nodes at the requested fixed ports (echoed back on TCPAddr);
//	(2) elects exactly ONE leader over real TCP, agreed by all 3;
//	(3) replicates a committed entry to ALL 3 durable FSMs;
//	(4) keeps each node's raft.db on disk (durability) and uses 3 DISTINCT ports.
func TestPersistentNetworkAt_ThreeNodeFixedAddrElectReplicate(t *testing.T) {
	logf := func(format string, args ...any) {
		t.Logf("[raft-persistnet-at] "+format, args...)
	}

	const (
		electionTimeout = 30 * time.Second
		applyTimeout    = 10 * time.Second
		replTimeout     = 20 * time.Second
	)

	ids := []string{"at-a", "at-b", "at-c"}
	dirs := []string{t.TempDir(), t.TempDir(), t.TempDir()}

	// Pick 3 free loopback ports up front and build the FIXED-address membership
	// shared by every node — the cross-process wiring, done in-process here.
	ports := []int{freeLoopbackPort(t), freeLoopbackPort(t), freeLoopbackPort(t)}
	binds := make([]string, 3)
	servers := make([]hraft.Server, 3)
	for i := range ids {
		binds[i] = fmt.Sprintf("127.0.0.1:%d", ports[i])
		servers[i] = hraft.Server{
			Suffrage: hraft.Voter,
			ID:       hraft.ServerID(ids[i]),
			Address:  hraft.ServerAddress(binds[i]),
		}
	}
	logf("FIXED-ADDR membership: %s=%s %s=%s %s=%s",
		ids[0], binds[0], ids[1], binds[1], ids[2], binds[2])

	pnodes := make([]*PersistentNetworkNode, 0, 3)
	nodes := make([]*Node, 0, 3)
	t.Cleanup(func() {
		for _, pn := range pnodes {
			_ = pn.Node.Shutdown()
		}
	})

	for i := range ids {
		pn, err := NewPersistentNetworkNodeAt(ids[i], dirs[i], binds[i], nil, servers)
		if err != nil {
			t.Fatalf("NewPersistentNetworkNodeAt(%s, %s): %v", ids[i], binds[i], err)
		}
		pnodes = append(pnodes, pn)
		nodes = append(nodes, pn.Node)

		// (1) The node bound the EXACT fixed port we requested.
		if pn.TCPAddr.Port != ports[i] {
			t.Fatalf("node %s bound port %d, wanted fixed %d", ids[i], pn.TCPAddr.Port, ports[i])
		}
		if !pn.TCPAddr.IP.IsLoopback() {
			t.Fatalf("node %s bound non-loopback IP %s", ids[i], pn.TCPAddr.IP)
		}
	}
	logf("FIXED-ADDR BIND PROOF: all 3 nodes bound their requested fixed ports %v", ports)

	// (4-distinct) 3 distinct non-zero ports.
	seen := make(map[int]string, 3)
	for i := range nodes {
		if other, dup := seen[ports[i]]; dup {
			t.Fatalf("ports collide: %s and %s both on %d", ids[i], other, ports[i])
		}
		seen[ports[i]] = ids[i]
	}

	cluster := &Cluster{nodes: nodes}

	// (2) Exactly one leader, agreed by all 3 — over real TCP.
	leader, err := cluster.WaitForLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no leader elected over fixed-addr TCP+disk: %v", err)
	}
	if err := waitForAllAgreeLeader(nodes, leader.ID(), 10*time.Second); err != nil {
		t.Fatalf("nodes did not agree on single leader %s: %v", leader.ID(), err)
	}
	logf("SINGLE LEADER over fixed-addr TCP, agreed by all: %s", leader.ID())

	// (3) Replicate a committed entry to all 3 durable FSMs.
	entries := map[string]string{"at/key": "at-value", "at/n": "3"}
	for k, v := range entries {
		if err := leader.Apply(Command{Op: CommandSet, Key: k, Value: v}, applyTimeout); err != nil {
			t.Fatalf("leader %s apply {%s=%s}: %v", leader.ID(), k, v, err)
		}
	}
	if err := waitForReplication(nodes, entries, replTimeout); err != nil {
		t.Fatalf("replication to all 3 fixed-addr durable FSMs failed: %v", err)
	}
	logf("REPLICATION CONFIRMED on all 3 fixed-addr durable FSMs")

	// (4-disk) Every node's raft.db is on disk, non-empty (durability).
	for i := range nodes {
		assertBoltOnDisk(t, dirs[i], ids[i])
	}
	logf("ON-DISK PROOF: all 3 fixed-addr nodes have a non-empty raft.db")
}
