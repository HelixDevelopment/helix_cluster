package raft

import (
	"fmt"
	"net"
	"time"

	hraft "github.com/hashicorp/raft"
)

// This file adds a REAL TCP-networked Raft transport ALONGSIDE the in-memory
// transport in node.go/cluster.go. Where NewInmemCluster wires nodes together
// with hashicorp/raft's InmemTransport (an in-process channel mux), the helpers
// here stand up a genuine hashicorp/raft NetworkTransport over an actual
// loopback TCP listener (127.0.0.1:0, ephemeral port) per node. Raft RPCs
// (RequestVote/AppendEntries/InstallSnapshot) travel over real kernel TCP
// sockets, so a leader is elected over the network — the prerequisite for any
// multi-process distributed cluster daemon.
//
// Only the TRANSPORT is real TCP here; the log/stable/snapshot stores stay
// in-memory (as in the in-mem cluster) because that is orthogonal to proving
// network consensus and keeps the test self-contained. The FSM is the same
// KVFSM used everywhere else.

// tcpStreamLayer adapts a *net.TCPListener into a hashicorp/raft StreamLayer
// (net.Listener + Dial). We own the listener directly — rather than letting
// NewTCPTransport create it internally — so we can capture and expose the real
// bound *net.TCPAddr (with its non-zero ephemeral port) for both advertisement
// and test assertions proving the transport is genuinely TCP-backed.
type tcpStreamLayer struct {
	listener *net.TCPListener
}

// Accept waits for and returns the next inbound connection. Implements net.Listener.
func (t *tcpStreamLayer) Accept() (net.Conn, error) { return t.listener.Accept() }

// Close closes the underlying TCP listener. Implements net.Listener.
func (t *tcpStreamLayer) Close() error { return t.listener.Close() }

// Addr returns the listener's bound address (a real *net.TCPAddr). Implements net.Listener.
func (t *tcpStreamLayer) Addr() net.Addr { return t.listener.Addr() }

// Dial opens an outbound TCP connection to a peer's advertised address. This is
// what carries Raft RPCs to other nodes over real sockets.
func (t *tcpStreamLayer) Dial(address hraft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", string(address), timeout)
}

// newTCPNode builds a single Raft node whose transport is a REAL TCP
// NetworkTransport bound to a fresh loopback ephemeral port. It returns the
// nodeConfig wiring (in-mem stores + the TCP transport) plus the concrete
// *net.TCPAddr the listener bound to, so the caller can interconnect peers by
// real address and assert on the real port.
func newTCPNode(id string, base *hraft.Config) (*nodeConfig, *net.TCPAddr, error) {
	// Bind a real loopback listener on an OS-assigned ephemeral port. The kernel
	// guarantees the resulting *net.TCPAddr has a unique, non-zero port.
	resolved, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("raft: resolve loopback addr for %s: %w", id, err)
	}
	listener, err := net.ListenTCP("tcp", resolved)
	if err != nil {
		return nil, nil, fmt.Errorf("raft: listen tcp for %s: %w", id, err)
	}
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, nil, fmt.Errorf("raft: listener for %s did not yield *net.TCPAddr (got %T)", id, listener.Addr())
	}

	stream := &tcpStreamLayer{listener: listener}

	transCfg := &hraft.NetworkTransportConfig{
		Stream:  stream,
		MaxPool: 3,
		Timeout: 5 * time.Second,
	}
	if base != nil && base.Logger != nil {
		transCfg.Logger = base.Logger
	}
	trans := hraft.NewNetworkTransportWithConfig(transCfg)

	conf := hraft.DefaultConfig()
	conf.LocalID = hraft.ServerID(id)
	// TCP elections are slower than in-process channel ones: a real socket
	// round-trip plus dial cost. Use timers generous enough to avoid flapping
	// over loopback but still bounded so a real failure surfaces fast. The test
	// polls for outcomes rather than sleeping a fixed duration.
	conf.HeartbeatTimeout = 200 * time.Millisecond
	conf.ElectionTimeout = 200 * time.Millisecond
	conf.LeaderLeaseTimeout = 200 * time.Millisecond
	conf.CommitTimeout = 50 * time.Millisecond
	conf.LogLevel = "ERROR"
	if base != nil {
		if base.LogOutput != nil {
			conf.LogOutput = base.LogOutput
		}
		if base.Logger != nil {
			conf.Logger = base.Logger
		}
		if base.LogLevel != "" {
			conf.LogLevel = base.LogLevel
		}
	}

	return &nodeConfig{
		id:       id,
		logStore: hraft.NewInmemStore(),
		stable:   hraft.NewInmemStore(),
		snaps:    hraft.NewInmemSnapshotStore(),
		// transport (the *InmemTransport field) is intentionally left nil for TCP
		// nodes; the real transport is the netTransport below.
		addr:         hraft.ServerAddress(tcpAddr.String()),
		fsm:          NewKVFSM(),
		conf:         conf,
		netTransport: trans,
	}, tcpAddr, nil
}

// NewNetworkCluster constructs an N-node Raft cluster whose nodes communicate
// over REAL loopback TCP transports, bootstraps it with an N-server
// configuration keyed by each node's real bound TCP address, and returns the
// started cluster. Each node dials the others' actual TCP ports for Raft RPCs.
//
// It mirrors NewInmemCluster's contract: the returned *Cluster exposes the same
// WaitForLeader / WaitForNewLeader / Survivors / Shutdown helpers, so existing
// test logic works unchanged against a genuinely networked cluster.
//
// ids must be unique and non-empty.
func NewNetworkCluster(base *hraft.Config, ids ...string) (*Cluster, error) {
	if len(ids) < 1 {
		return nil, fmt.Errorf("raft: cluster needs at least 1 node")
	}

	cfgs := make([]*nodeConfig, 0, len(ids))
	servers := make([]hraft.Server, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, fmt.Errorf("raft: empty node id")
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("raft: duplicate node id %q", id)
		}
		seen[id] = struct{}{}

		nc, _, err := newTCPNode(id, base)
		if err != nil {
			// Best-effort cleanup of already-opened transports on partial failure.
			for _, done := range cfgs {
				if done.netTransport != nil {
					_ = done.netTransport.Close()
				}
			}
			return nil, err
		}
		cfgs = append(cfgs, nc)
		servers = append(servers, hraft.Server{
			Suffrage: hraft.Voter,
			ID:       hraft.ServerID(nc.id),
			Address:  nc.addr, // the REAL bound TCP address (127.0.0.1:<ephemeral>)
		})
	}

	// Unlike the in-mem cluster, there is nothing to "connect" — peers reach each
	// other by dialing the real TCP addresses recorded in the bootstrap config.
	bootstrapCfg := hraft.Configuration{Servers: servers}

	nodes := make([]*Node, 0, len(cfgs))
	for _, nc := range cfgs {
		if err := hraft.BootstrapCluster(nc.conf, nc.logStore, nc.stable, nc.snaps, nc.netTransport, cloneConfiguration(bootstrapCfg)); err != nil {
			return nil, fmt.Errorf("raft: bootstrap node %s: %w", nc.id, err)
		}
		r, err := hraft.NewRaft(nc.conf, nc.fsm, nc.logStore, nc.stable, nc.snaps, nc.netTransport)
		if err != nil {
			return nil, fmt.Errorf("raft: start node %s: %w", nc.id, err)
		}
		nodes = append(nodes, &Node{id: nc.id, raft: r, fsm: nc.fsm, netTransport: nc.netTransport})
	}

	return &Cluster{nodes: nodes}, nil
}

// NetTransport returns the node's real TCP NetworkTransport, or nil for in-mem
// nodes. The TCP integration test uses this to assert each node's LocalAddr is a
// genuine *net.TCPAddr with a distinct non-zero port — proof the transport is
// truly network-backed and not an in-process shim.
func (n *Node) NetTransport() *hraft.NetworkTransport { return n.netTransport }

// LocalTCPAddr resolves the node's transport LocalAddr to a concrete
// *net.TCPAddr. It returns an error for in-mem nodes (whose addresses are opaque
// ids, not TCP endpoints) or if the address fails to resolve.
func (n *Node) LocalTCPAddr() (*net.TCPAddr, error) {
	if n.netTransport == nil {
		return nil, fmt.Errorf("raft: node %s has no TCP transport (in-mem node)", n.id)
	}
	addr := string(n.netTransport.LocalAddr())
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("raft: resolve local tcp addr %q for node %s: %w", addr, n.id, err)
	}
	return tcpAddr, nil
}
