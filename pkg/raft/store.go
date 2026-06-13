package raft

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	hraft "github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

// This file adds a REAL on-disk (BoltDB) storage variant of a Raft Node
// ALONGSIDE the in-memory one in node.go/cluster.go and the TCP one in tcp.go.
//
// Where newInmemNode / newTCPNode use hashicorp/raft's NewInmemStore +
// NewInmemSnapshotStore (so a node loses ALL committed state on restart), the
// persistent node here uses:
//
//   - github.com/hashicorp/raft-boltdb/v2 BoltStore — a single bbolt file that
//     implements BOTH raft.LogStore and raft.StableStore, durably persisting the
//     replicated log and Raft's persistent fields (currentTerm, votedFor, the
//     committed configuration) to disk; and
//   - raft.NewFileSnapshotStore — FSM snapshots persisted as files on disk.
//
// Because storage is durable, a node that is SHUT DOWN (which closes the bolt
// file) can be RE-OPENED from the same data directory: hashicorp/raft replays
// the persisted log (and/or restores the latest snapshot) into a freshly built
// KVFSM, recovering all previously-committed key/value entries WITHOUT them
// being re-applied by a caller. This is the storage-durability prerequisite for
// a real, restartable distributed node (Roadmap §2.1 / production gap I4).
//
// The transport is orthogonal to storage durability: the recovery property is
// about STORAGE, so a single-node persistent cluster uses the in-mem transport
// (a single-node raft is trivially leader and commits immediately, which is
// exactly what a disk-persistence test needs). The same BoltStore could be
// paired with the TCP transport from tcp.go for a multi-process deployment.

// boltLogStoreFile / boltStableStoreFile / snapshotsDir are the fixed on-disk
// layout inside a node's data directory. Using a single bbolt file for both the
// log and stable stores is the standard raft-boltdb arrangement (one BoltStore
// satisfies both interfaces).
const (
	// raftBoltFile is the bbolt database holding the log + stable stores.
	raftBoltFile = "raft.db"
	// snapshotsDir holds FileSnapshotStore's snapshot files.
	snapshotsDir = "snapshots"
)

// snapshotRetain is how many snapshots FileSnapshotStore keeps. Must be >= 1.
const snapshotRetain = 2

// newPersistentNode builds a single Raft node backed by a REAL on-disk BoltStore
// (logs+stable) under dataDir plus a real FileSnapshotStore, using the in-mem
// transport (sufficient and correct for a single-node durability test — see the
// file header). The returned nodeConfig carries the open *BoltStore so the
// caller can wire a closeStore hook onto the Node; the caller owns closing it
// (via Node.Shutdown) so the files flush and can be reopened.
//
// The same dataDir may be reused across process lifetimes: the FIRST call
// bootstraps the single-server configuration into the fresh stores; a LATER call
// against an already-bootstrapped dataDir must NOT bootstrap again (raft refuses
// to bootstrap over existing state). newPersistentNode reports whether the
// stores were already initialized so the caller can skip bootstrap on reopen.
func newPersistentNode(id, dataDir string, base *hraft.Config) (nc *nodeConfig, boltStore *raftboltdb.BoltStore, alreadyInit bool, err error) {
	if id == "" {
		return nil, nil, false, fmt.Errorf("raft: persistent node needs a non-empty id")
	}
	if dataDir == "" {
		return nil, nil, false, fmt.Errorf("raft: persistent node needs a non-empty data dir")
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, nil, false, fmt.Errorf("raft: create data dir %q: %w", dataDir, err)
	}
	snapDir := filepath.Join(dataDir, snapshotsDir)
	if err := os.MkdirAll(snapDir, 0o750); err != nil {
		return nil, nil, false, fmt.Errorf("raft: create snapshot dir %q: %w", snapDir, err)
	}

	boltPath := filepath.Join(dataDir, raftBoltFile)
	// Detect whether this data dir already holds raft state: if the bolt file
	// exists and is non-empty, the node has run before and must be RECOVERED
	// (reopened) rather than re-bootstrapped.
	if fi, statErr := os.Stat(boltPath); statErr == nil && fi.Size() > 0 {
		alreadyInit = true
	}

	// REAL on-disk store: one bbolt file implementing LogStore + StableStore.
	store, err := raftboltdb.NewBoltStore(boltPath)
	if err != nil {
		return nil, nil, false, fmt.Errorf("raft: open bolt store %q: %w", boltPath, err)
	}

	// As a second, independent signal of prior state, consult the persisted log's
	// LastIndex. A freshly created (or bootstrapped-only) store reports 0; a store
	// that has committed user entries reports > 0. We OR this with the file-size
	// heuristic so reopen is detected robustly.
	if last, lerr := store.LastIndex(); lerr == nil && last > 0 {
		alreadyInit = true
	}

	// REAL on-disk snapshot store.
	var snapOut io.Writer = os.Stderr
	if base != nil && base.LogOutput != nil {
		snapOut = base.LogOutput
	}
	snaps, err := hraft.NewFileSnapshotStore(snapDir, snapshotRetain, snapOut)
	if err != nil {
		_ = store.Close()
		return nil, nil, false, fmt.Errorf("raft: open file snapshot store %q: %w", snapDir, err)
	}

	// In-mem transport: single-node raft is trivially leader; storage durability
	// (the property under test) is independent of transport.
	addr, trans := hraft.NewInmemTransport(hraft.ServerAddress(id))

	conf := hraft.DefaultConfig()
	conf.LocalID = hraft.ServerID(id)
	conf.HeartbeatTimeout = 100 * time.Millisecond
	conf.ElectionTimeout = 100 * time.Millisecond
	conf.LeaderLeaseTimeout = 100 * time.Millisecond
	conf.CommitTimeout = 20 * time.Millisecond
	conf.LogLevel = "ERROR"
	applyBaseLogging(conf, base)

	return &nodeConfig{
		id:        id,
		logStore:  store, // *BoltStore implements raft.LogStore
		stable:    store, // ...and raft.StableStore (same file)
		snaps:     snaps,
		transport: trans,
		addr:      addr,
		fsm:       NewKVFSM(),
		conf:      conf,
	}, store, alreadyInit, nil
}

// NewPersistentNode creates a single-node Raft Node backed by a REAL on-disk
// BoltStore (log+stable) and FileSnapshotStore rooted at dataDir, then starts it.
//
// First call on a fresh dataDir: the single-server configuration is bootstrapped
// into the durable stores and the node elects itself leader. Subsequent calls on
// the SAME dataDir (after the previous Node was Shutdown, which closed the bolt
// file): the node is RE-OPENED — raft replays the persisted log / restores the
// latest snapshot into a new KVFSM, recovering all previously committed entries
// WITHOUT a caller re-applying them — then resumes as the single-node leader.
//
// The caller MUST call Node.Shutdown when done with the node; Shutdown stops raft
// and closes the BoltDB so its files flush and the dataDir can be reopened.
//
// Callers can poll the returned node's IsLeader (or build a one-node *Cluster via
// WaitForLeader using a wrapper) to await leadership; for a single node election
// is near-instant.
func NewPersistentNode(id, dataDir string, base *hraft.Config) (*Node, error) {
	nc, boltStore, alreadyInit, err := newPersistentNode(id, dataDir, base)
	if err != nil {
		return nil, err
	}

	// Bootstrap ONLY on first initialization of this data dir. On reopen the
	// durable stores already contain the configuration + log, and re-bootstrapping
	// would either error or clobber recovered state.
	if !alreadyInit {
		bootstrapCfg := hraft.Configuration{Servers: []hraft.Server{{
			Suffrage: hraft.Voter,
			ID:       hraft.ServerID(nc.id),
			Address:  nc.addr,
		}}}
		if err := hraft.BootstrapCluster(nc.conf, nc.logStore, nc.stable, nc.snaps, nc.transport, bootstrapCfg); err != nil {
			_ = boltStore.Close()
			return nil, fmt.Errorf("raft: bootstrap persistent node %s: %w", nc.id, err)
		}
	}

	r, err := hraft.NewRaft(nc.conf, nc.fsm, nc.logStore, nc.stable, nc.snaps, nc.transport)
	if err != nil {
		_ = boltStore.Close()
		return nil, fmt.Errorf("raft: start persistent node %s: %w", nc.id, err)
	}

	return &Node{
		id:         nc.id,
		raft:       r,
		fsm:        nc.fsm,
		trans:      nc.transport,
		dataDir:    dataDir,
		closeStore: boltStore.Close,
	}, nil
}

// WaitForLeader polls until this node becomes the Raft leader or the deadline
// expires. It is the single-node analogue of Cluster.WaitForLeader, asserting on
// observed state rather than sleeping a fixed duration.
//
// On a REOPENED persistent node, becoming leader is necessary but not sufficient
// for the FSM to reflect all previously-committed entries: hashicorp/raft replays
// the persisted log into the FSM ASYNCHRONOUSLY once the node has a commit index.
// WaitForLeader therefore also issues a Raft Barrier after leadership is observed,
// blocking until every persisted-and-committed log entry has been applied to the
// FSM — so on return the recovered state is fully visible via FSM().Get.
func (n *Node) WaitForLeader(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if n.IsLeader() && n.Leader() == n.ID() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				remaining = time.Second
			}
			// Barrier resolves only after all preceding (replayed) log entries
			// have been applied to the FSM — i.e. recovery from disk is complete.
			if err := n.raft.Barrier(remaining).Error(); err != nil {
				return fmt.Errorf("raft: barrier on node %s after election: %w", n.id, err)
			}
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("raft: persistent node %s did not become leader within %s", n.id, timeout)
}
