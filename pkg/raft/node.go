package raft

import (
	"errors"
	"fmt"
	"time"

	hraft "github.com/hashicorp/raft"
)

// ErrNotLeader is returned when a write is attempted on a non-leader node.
// It mirrors pkg/leader.ErrNotLeader so callers can treat coordination errors
// uniformly across the deterministic-election and Raft-consensus backends.
var ErrNotLeader = errors.New("this node is not the raft leader")

// Node wraps a single hashicorp/raft instance together with its replicated
// KVFSM, exposing a small coordination surface (Apply / Leader / IsLeader /
// Shutdown) that intentionally resembles pkg/leader's Election so the two are
// interchangeable from a caller's point of view.
type Node struct {
	id    string
	raft  *hraft.Raft
	fsm   *KVFSM
	trans *hraft.InmemTransport
	// netTransport is set instead of trans for nodes built on a REAL TCP
	// NetworkTransport (see tcp.go). Exactly one of trans/netTransport is non-nil.
	netTransport *hraft.NetworkTransport
}

// ID returns the node's server ID.
func (n *Node) ID() string { return n.id }

// FSM returns the underlying replicated state machine (read-only use intended).
func (n *Node) FSM() *KVFSM { return n.fsm }

// Raft exposes the underlying hashicorp/raft instance for advanced operations
// (configuration changes, stats). Most callers should not need this.
func (n *Node) Raft() *hraft.Raft { return n.raft }

// Transport returns the node's in-memory transport. Used by the test cluster
// builder to wire peers together; not needed for real network transports.
func (n *Node) Transport() *hraft.InmemTransport { return n.trans }

// IsLeader reports whether this node is currently the Raft leader.
func (n *Node) IsLeader() bool {
	return n.raft.State() == hraft.Leader
}

// State returns the node's current Raft state (Leader/Follower/Candidate/Shutdown).
func (n *Node) State() hraft.RaftState {
	return n.raft.State()
}

// Leader returns the ServerID of the cluster leader as currently known by this
// node, or "" if no leader is known yet.
func (n *Node) Leader() string {
	_, id := n.raft.LeaderWithID()
	return string(id)
}

// Apply proposes a Command to the Raft log. It only succeeds on the leader; the
// command is replicated to a majority and applied to every FSM before the
// returned future resolves. On a follower it returns ErrNotLeader.
func (n *Node) Apply(cmd Command, timeout time.Duration) error {
	if n.raft.State() != hraft.Leader {
		return ErrNotLeader
	}
	data, err := cmd.Encode()
	if err != nil {
		return fmt.Errorf("raft: encode command: %w", err)
	}
	f := n.raft.Apply(data, timeout)
	if err := f.Error(); err != nil {
		return fmt.Errorf("raft: apply: %w", err)
	}
	if resp := f.Response(); resp != nil {
		if rerr, ok := resp.(error); ok && rerr != nil {
			return fmt.Errorf("raft: fsm rejected command: %w", rerr)
		}
	}
	return nil
}

// Shutdown stops the Raft node and blocks until it has fully stopped. After
// Shutdown the node no longer participates in the cluster (this is what the
// kill-the-leader test uses to remove the leader).
func (n *Node) Shutdown() error {
	if err := n.raft.Shutdown().Error(); err != nil {
		return fmt.Errorf("raft: shutdown node %s: %w", n.id, err)
	}
	// For TCP-backed nodes, also close the underlying NetworkTransport so its
	// listener socket is released. After this the node is fully off the network,
	// which is exactly what the kill-the-leader test relies on.
	if n.netTransport != nil {
		if err := n.netTransport.Close(); err != nil {
			return fmt.Errorf("raft: close tcp transport for node %s: %w", n.id, err)
		}
	}
	return nil
}

// nodeConfig holds the wiring for a single in-memory Raft node.
type nodeConfig struct {
	id        string
	logStore  hraft.LogStore
	stable    hraft.StableStore
	snaps     hraft.SnapshotStore
	transport *hraft.InmemTransport
	addr      hraft.ServerAddress
	fsm       *KVFSM
	conf      *hraft.Config
	// netTransport is the REAL TCP transport for TCP-backed nodes (see tcp.go);
	// nil for in-mem nodes, which use the transport field above instead.
	netTransport *hraft.NetworkTransport
}

// newInmemNode builds a single Raft node backed entirely by in-memory stores and
// an in-memory transport. Real production deployments would swap these for
// BoltDB stores and a TCP transport, but the FSM and Node logic are identical.
func newInmemNode(id string, base *hraft.Config) (*nodeConfig, error) {
	fsm := NewKVFSM()
	addr, trans := hraft.NewInmemTransport(hraft.ServerAddress(id))

	conf := hraft.DefaultConfig()
	conf.LocalID = hraft.ServerID(id)
	// Tighten timers so elections complete quickly in tests while staying within
	// hashicorp/raft's validation constraints (HeartbeatTimeout/ElectionTimeout
	// must be >= 5ms; CommitTimeout reasonable). These are bounded, not fixed
	// sleeps — the test polls for outcomes rather than assuming a duration.
	conf.HeartbeatTimeout = 100 * time.Millisecond
	conf.ElectionTimeout = 100 * time.Millisecond
	conf.LeaderLeaseTimeout = 100 * time.Millisecond
	conf.CommitTimeout = 20 * time.Millisecond
	// Silence library logs by default; the test can override via base.
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
		id:        id,
		logStore:  hraft.NewInmemStore(),
		stable:    hraft.NewInmemStore(),
		snaps:     hraft.NewInmemSnapshotStore(),
		transport: trans,
		addr:      addr,
		fsm:       fsm,
		conf:      conf,
	}, nil
}
