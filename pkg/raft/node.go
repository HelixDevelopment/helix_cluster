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
	// dataDir is the on-disk directory backing a PERSISTENT node (see store.go);
	// empty for the in-mem and TCP nodes whose stores live only in memory.
	dataDir string
	// closeStore releases the on-disk BoltDB handle for a persistent node so the
	// files are flushed and can be re-opened by a fresh Node. It is nil for
	// in-mem/TCP nodes. Shutdown invokes it after stopping raft.
	closeStore func() error
}

// DataDir returns the on-disk directory backing this node's persistent stores,
// or "" if the node is in-mem/TCP backed (no disk persistence).
func (n *Node) DataDir() string { return n.dataDir }

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

// LeaderAddr returns the network ADDRESS of the cluster leader as currently known
// by this node, or "" if no leader is known yet. For a TCP-backed node this is
// the leader's real host:port — what a follower's admin endpoint reports so a
// client can redirect its write to the leader process.
func (n *Node) LeaderAddr() string {
	addr, _ := n.raft.LeaderWithID()
	return string(addr)
}

// LastIndex returns the last index in this node's stable storage (last log entry
// or last snapshot). It is a sink-side catch-up signal: a freshly restarted node
// rejoining the cluster reports a LastIndex that climbs to match the leader's as
// it replays its on-disk log and receives any entries it missed.
func (n *Node) LastIndex() uint64 {
	return n.raft.LastIndex()
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
	// For PERSISTENT (BoltDB-backed) nodes, close the on-disk store LAST so its
	// files are flushed and the file handle is released — the prerequisite for a
	// fresh Node to re-open the SAME data dir and recover committed state.
	if n.closeStore != nil {
		if err := n.closeStore(); err != nil {
			return fmt.Errorf("raft: close bolt store for node %s: %w", n.id, err)
		}
	}
	return nil
}

// applyBaseLogging copies the logging-related fields (LogOutput, Logger,
// LogLevel) from an optional base *hraft.Config onto conf, but ONLY for fields
// the caller actually set on base. A nil base, or an unset field, leaves conf's
// existing value untouched. This is the single shared merge used by both the
// in-mem (newInmemNode) and TCP (newTCPNode) builders so the two cannot drift.
//
// It deliberately touches ONLY logging fields — it does not alter timers, IDs,
// or any other behavior — so the in-mem and TCP election semantics are unchanged.
func applyBaseLogging(conf, base *hraft.Config) {
	if base == nil {
		return
	}
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

// applySnapshotTuning copies the snapshot/log-compaction tuning fields
// (SnapshotInterval, SnapshotThreshold, TrailingLogs) from an optional base
// *hraft.Config onto conf, but ONLY for fields the caller actually set to a
// non-zero value. A nil base, or an unset (zero) field, leaves conf's existing
// default untouched — so EXISTING callers that pass nil (or a base with only
// logging fields set) get byte-for-byte the same configuration as before this
// helper existed; their election/replication semantics are unchanged.
//
// It exists so a TEST can drive hashicorp/raft's real automatic snapshotting +
// log compaction (low SnapshotThreshold + low TrailingLogs + short
// SnapshotInterval cause applying many entries to trigger an FSM snapshot and
// truncate the log past the snapshot point). It deliberately touches ONLY these
// three snapshot fields — no timers, ids, or other behavior — so it cannot
// affect any production path that does not opt in by setting them.
func applySnapshotTuning(conf, base *hraft.Config) {
	if base == nil {
		return
	}
	if base.SnapshotInterval != 0 {
		conf.SnapshotInterval = base.SnapshotInterval
	}
	if base.SnapshotThreshold != 0 {
		conf.SnapshotThreshold = base.SnapshotThreshold
	}
	if base.TrailingLogs != 0 {
		conf.TrailingLogs = base.TrailingLogs
	}
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
	// dataDirPath is the on-disk directory backing a PERSISTENT node's BoltStore
	// (see store.go / persistnet.go); empty for nodes whose stores live in memory.
	// It is carried onto the live Node.dataDir when the node is wrapped.
	dataDirPath string
}

// dataDir returns the persistent data directory for this node config, or "" if
// the node's stores are in-memory. Used by the cluster wrap phase to populate the
// live Node's dataDir for a persistent+networked node.
func (c *nodeConfig) dataDir() string { return c.dataDirPath }

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
	applyBaseLogging(conf, base)
	// Snapshot/log-compaction tuning is opt-in via base (zero fields are ignored),
	// so this is a no-op for every existing caller that passes nil or logging-only
	// base config; defaults are preserved. The snapshot integration test uses it to
	// force real auto-snapshot + log truncation.
	applySnapshotTuning(conf, base)

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
