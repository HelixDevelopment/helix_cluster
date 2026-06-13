package raft

import (
	"fmt"
	"net"
	"time"

	hraft "github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

// This file COMPOSES the two real-production primitives that already exist in
// this package — but were, until now, only available SEPARATELY — into a SINGLE
// node that is BOTH networked AND durable:
//
//   - the REAL loopback TCP NetworkTransport from tcp.go (newTCPNode /
//     tcpStreamLayer), over which Raft RPCs travel on genuine kernel sockets; and
//   - the REAL on-disk BoltStore (log+stable) + FileSnapshotStore from store.go
//     (the openPersistentStores helper), so committed state survives a restart.
//
// Neither primitive is duplicated here: the TCP listener/StreamLayer wiring is
// reused via openTCPTransport (extracted from newTCPNode), and the BoltDB +
// FileSnapshotStore wiring is reused via openPersistentStores (extracted from
// newPersistentNode). A persistent+networked node therefore carries BOTH a
// non-nil netTransport (TCP) AND a non-nil closeStore/dataDir (BoltDB) on the
// existing Node struct — these are orthogonal fields, so there is no clash with
// node.go's "exactly one TRANSPORT (trans XOR netTransport)" invariant: such a
// node uses netTransport (TCP) and leaves trans (in-mem) nil, exactly like a
// TCP-only node, while additionally setting the storage/closeStore fields a
// persistent node sets.
//
// This is the production gap I5: a real cluster node that talks to peers over
// real TCP AND recovers its persisted log from disk after a process restart.

// openTCPTransport binds a fresh loopback ephemeral TCP listener and wraps it in
// a hashicorp/raft NetworkTransport, returning the transport plus the concrete
// bound *net.TCPAddr. It is the transport half of newTCPNode, factored out so the
// persistent+networked builder reuses the identical real-TCP wiring rather than
// duplicating it.
//
// It is a thin convenience wrapper over openTCPTransportAt with the loopback
// ephemeral bind address "127.0.0.1:0": the OS assigns a free port. The shared
// listener/StreamLayer/NetworkTransport wiring lives in openTCPTransportAt so the
// ephemeral (in-process test cluster) and fixed-address (multi-process daemon)
// paths cannot drift.
func openTCPTransport(id string, base *hraft.Config) (*hraft.NetworkTransport, *net.TCPAddr, error) {
	return openTCPTransportAt(id, "127.0.0.1:0", base)
}

// openTCPTransportAt binds a TCP listener at the CALLER-SPECIFIED bindAddr and
// wraps it in a hashicorp/raft NetworkTransport, returning the transport plus the
// concrete bound *net.TCPAddr. This is the single shared real-TCP wiring used by
// BOTH the ephemeral path (openTCPTransport, "127.0.0.1:0") and the multi-process
// daemon path, which passes a fixed host:port (e.g. "127.0.0.1:7001") so peers
// across separate OS processes can be configured ahead of time by known address.
//
// bindAddr is any address net.ResolveTCPAddr accepts: "host:port" for a fixed
// port, ":0"/"127.0.0.1:0" for an OS-assigned ephemeral port. The returned
// *net.TCPAddr is the ACTUAL bound address (with the concrete port), so a fixed
// bind echoes its port and an ephemeral bind reveals the chosen one.
func openTCPTransportAt(id, bindAddr string, base *hraft.Config) (*hraft.NetworkTransport, *net.TCPAddr, error) {
	if bindAddr == "" {
		return nil, nil, fmt.Errorf("raft: empty bind address for %s", id)
	}
	resolved, err := net.ResolveTCPAddr("tcp", bindAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("raft: resolve bind addr %q for %s: %w", bindAddr, id, err)
	}
	listener, err := net.ListenTCP("tcp", resolved)
	if err != nil {
		return nil, nil, fmt.Errorf("raft: listen tcp %q for %s: %w", bindAddr, id, err)
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
	return hraft.NewNetworkTransportWithConfig(transCfg), tcpAddr, nil
}

// persistentTCPConfig returns the tuned hraft.Config for a persistent+networked
// node. Timers match the TCP-only node's (real socket round-trips are slower than
// in-process channels), and on-disk recovery + replication add startup cost — so
// the generous-but-bounded TCP timers are exactly what this combined path wants.
func persistentTCPConfig(id string, base *hraft.Config) *hraft.Config {
	conf := hraft.DefaultConfig()
	conf.LocalID = hraft.ServerID(id)
	conf.HeartbeatTimeout = 200 * time.Millisecond
	conf.ElectionTimeout = 200 * time.Millisecond
	conf.LeaderLeaseTimeout = 200 * time.Millisecond
	conf.CommitTimeout = 50 * time.Millisecond
	conf.LogLevel = "ERROR"
	applyBaseLogging(conf, base)
	return conf
}

// newPersistentNetworkNode builds a single Raft node backed by BOTH a REAL TCP
// NetworkTransport (fresh loopback ephemeral port) AND a REAL on-disk BoltStore
// (log+stable) + FileSnapshotStore rooted at dataDir. It returns the nodeConfig
// wiring, the open *BoltStore (so the caller can wire Node.closeStore), the bound
// *net.TCPAddr, and whether the data dir already held raft state (so the caller
// can skip re-bootstrap on reopen — identical reopen semantics to store.go).
//
// It is the ephemeral-port convenience over newPersistentNetworkNodeAt (bind
// "127.0.0.1:0"); the in-process test cluster builder uses it. The combined
// store+transport assembly lives once in newPersistentNetworkNodeAt so the
// ephemeral and fixed-address (multi-process daemon) paths cannot drift.
func newPersistentNetworkNode(id, dataDir string, base *hraft.Config) (nc *nodeConfig, boltStore *raftboltdb.BoltStore, tcpAddr *net.TCPAddr, alreadyInit bool, err error) {
	return newPersistentNetworkNodeAt(id, dataDir, "127.0.0.1:0", base)
}

// newPersistentNetworkNodeAt builds a single Raft node backed by BOTH a REAL TCP
// NetworkTransport bound at the CALLER-SPECIFIED bindAddr AND a REAL on-disk
// BoltStore (log+stable) + FileSnapshotStore rooted at dataDir. It returns the
// nodeConfig wiring, the open *BoltStore, the ACTUAL bound *net.TCPAddr, and
// whether the data dir already held raft state (reopen detection identical to
// store.go's openPersistentStores).
//
// The bindAddr makes this the multi-process building block: a daemon process can
// bind a FIXED host:port so its peers — running in SEPARATE OS processes — are
// configured by known address ahead of time. (newPersistentNetworkNode passes
// "127.0.0.1:0" to keep the ephemeral in-process path.)
//
// On ANY error after a resource is opened, it closes what it opened (bolt store
// and/or TCP listener) so nothing leaks on the failure path.
func newPersistentNetworkNodeAt(id, dataDir, bindAddr string, base *hraft.Config) (nc *nodeConfig, boltStore *raftboltdb.BoltStore, tcpAddr *net.TCPAddr, alreadyInit bool, err error) {
	if id == "" {
		return nil, nil, nil, false, fmt.Errorf("raft: persistent network node needs a non-empty id")
	}
	if dataDir == "" {
		return nil, nil, nil, false, fmt.Errorf("raft: persistent network node needs a non-empty data dir")
	}

	// REAL on-disk stores (reused from store.go via openPersistentStores).
	store, snaps, init, err := openPersistentStores(dataDir, base)
	if err != nil {
		return nil, nil, nil, false, err
	}

	// REAL TCP transport bound at the requested address (shared tcp.go wiring via
	// openTCPTransportAt — NOT duplicated).
	trans, addr, err := openTCPTransportAt(id, bindAddr, base)
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, false, err
	}

	return &nodeConfig{
		id:           id,
		logStore:     store, // *BoltStore implements raft.LogStore
		stable:       store, // ...and raft.StableStore (same bbolt file)
		snaps:        snaps, // on-disk FileSnapshotStore
		addr:         hraft.ServerAddress(addr.String()),
		fsm:          NewKVFSM(),
		conf:         persistentTCPConfig(id, base),
		netTransport: trans, // REAL TCP transport (trans/in-mem field stays nil)
		dataDirPath:  dataDir,
	}, store, addr, init, nil
}

// PersistentNetworkNode is a single Raft node that is BOTH networked (real TCP
// transport) AND durable (on-disk BoltStore + FileSnapshotStore). It is created
// by NewPersistentNetworkNode and bundles the live *Node with its bound TCP
// address, so a caller (and the cluster builder) can interconnect peers by real
// address and assert on the real port + on-disk files.
type PersistentNetworkNode struct {
	// Node is the live raft node (TCP transport + BoltDB stores).
	Node *Node
	// TCPAddr is the node's real bound loopback TCP address (non-zero port).
	TCPAddr *net.TCPAddr
	// boltStore is the open on-disk log+stable store. It is exposed (read-only)
	// via LogStore so a test can inspect the PERSISTED log directly — proving
	// on-disk recovery in isolation — without opening a second BoltDB handle on
	// the same file (bbolt is single-writer; a second open would block).
	boltStore *raftboltdb.BoltStore
}

// LogStore returns the node's on-disk raft LogStore for read-only inspection of
// the persisted log (e.g. asserting recovered entries after a restart). The
// returned store is owned by the node and closed by Node.Shutdown — callers must
// not close it.
func (p *PersistentNetworkNode) LogStore() hraft.LogStore { return p.boltStore }

// NewPersistentNetworkNode creates and starts a single Raft Node backed by BOTH
// a real TCP transport and a real on-disk BoltStore+FileSnapshotStore at dataDir.
//
// First call on a fresh dataDir: it bootstraps the given cluster configuration
// (servers) into the durable stores. Subsequent calls on the SAME dataDir (after
// the previous Node was Shutdown, which closed the bolt file AND the TCP
// listener): it RE-OPENS the durable stores and does NOT re-bootstrap — raft
// replays the persisted log / restores the latest snapshot into a fresh KVFSM,
// recovering all previously-committed entries from disk — while binding a NEW TCP
// listener (the old ephemeral port is gone after Shutdown).
//
// servers is the bootstrap membership used ONLY on first init; it is ignored on
// reopen (the durable configuration already on disk wins). For a single-node
// node, pass nil/empty and a one-server config keyed by this node's bound address
// is synthesized. For a multi-node cluster the cluster builder passes the full
// membership.
//
// The caller MUST call Node.Shutdown when done: it stops raft, closes the TCP
// transport (releasing the listener), and closes the BoltDB (flushing files so
// the dataDir can be reopened) — in that order.
func NewPersistentNetworkNode(id, dataDir string, base *hraft.Config, servers []hraft.Server) (*PersistentNetworkNode, error) {
	return NewPersistentNetworkNodeAt(id, dataDir, "127.0.0.1:0", base, servers)
}

// NewPersistentNetworkNodeAt is the fixed-bind-address variant of
// NewPersistentNetworkNode: it creates and starts a single Raft Node backed by
// BOTH a real TCP transport bound at the CALLER-SPECIFIED bindAddr AND a real
// on-disk BoltStore+FileSnapshotStore at dataDir.
//
// This is the multi-PROCESS building block. Because the transport binds a known
// fixed address (e.g. "127.0.0.1:7001") rather than an ephemeral one, the FULL
// initial cluster membership (servers — every node's id=addr, INCLUDING self) can
// be configured ahead of time across SEPARATE OS processes. Each daemon process
// calls this with the SAME servers list and its own id/bindAddr/dataDir.
//
// First boot (fresh dataDir): bootstraps the given servers membership into the
// durable stores. Reopen (same dataDir, after a prior Shutdown OR an abrupt
// process kill): re-opens the durable stores and does NOT re-bootstrap — raft
// recovers the persisted log/snapshot from disk and rejoins the live cluster —
// using the SAME fixed bindAddr so peers reconnect by the address they already
// know. Reopen detection is identical to store.go (openPersistentStores).
//
// servers is ignored on reopen (the durable on-disk configuration wins); if empty
// on first boot a single-server config keyed by this node's bound address is
// synthesized. The caller MUST call Node.Shutdown when done (stops raft, closes
// the TCP transport, then closes the BoltDB — in that order).
func NewPersistentNetworkNodeAt(id, dataDir, bindAddr string, base *hraft.Config, servers []hraft.Server) (*PersistentNetworkNode, error) {
	nc, boltStore, tcpAddr, alreadyInit, err := newPersistentNetworkNodeAt(id, dataDir, bindAddr, base)
	if err != nil {
		return nil, err
	}

	// Bootstrap ONLY on first initialization of this data dir. On reopen the
	// durable stores already hold the configuration + log; re-bootstrapping would
	// error or clobber recovered state.
	if !alreadyInit {
		bootSrv := servers
		if len(bootSrv) == 0 {
			bootSrv = []hraft.Server{{
				Suffrage: hraft.Voter,
				ID:       hraft.ServerID(nc.id),
				Address:  nc.addr,
			}}
		}
		bootstrapCfg := hraft.Configuration{Servers: bootSrv}
		if err := hraft.BootstrapCluster(nc.conf, nc.logStore, nc.stable, nc.snaps, nc.netTransport, cloneConfiguration(bootstrapCfg)); err != nil {
			_ = nc.netTransport.Close()
			_ = boltStore.Close()
			return nil, fmt.Errorf("raft: bootstrap persistent network node %s: %w", nc.id, err)
		}
	}

	r, err := hraft.NewRaft(nc.conf, nc.fsm, nc.logStore, nc.stable, nc.snaps, nc.netTransport)
	if err != nil {
		_ = nc.netTransport.Close()
		_ = boltStore.Close()
		return nil, fmt.Errorf("raft: start persistent network node %s: %w", nc.id, err)
	}

	node := &Node{
		id:           nc.id,
		raft:         r,
		fsm:          nc.fsm,
		netTransport: nc.netTransport,
		dataDir:      dataDir,
		closeStore:   boltStore.Close,
	}
	return &PersistentNetworkNode{Node: node, TCPAddr: tcpAddr, boltStore: boltStore}, nil
}

// NewPersistentNetworkCluster constructs an N-node Raft cluster whose nodes are
// EACH backed by BOTH a real loopback TCP transport AND a real on-disk
// BoltStore+FileSnapshotStore. dataDirs[i] is the durable directory for ids[i]
// (they must be the same length, with unique non-empty ids and distinct dirs).
//
// It is the persistent+networked analogue of NewNetworkCluster: it binds each
// node's real TCP listener first (so the full membership of real addresses is
// known), then bootstraps that membership into every node's DURABLE stores and
// starts them. The returned *Cluster exposes the same WaitForLeader /
// WaitForNewLeader / Survivors / Shutdown helpers, so existing cluster test logic
// works unchanged against a genuinely networked AND durable cluster.
//
// On partial failure it closes every resource it already opened (bolt stores and
// TCP listeners) so nothing leaks.
func NewPersistentNetworkCluster(base *hraft.Config, ids []string, dataDirs []string) (*Cluster, error) {
	if len(ids) < 1 {
		return nil, fmt.Errorf("raft: cluster needs at least 1 node")
	}
	if len(ids) != len(dataDirs) {
		return nil, fmt.Errorf("raft: ids/dataDirs length mismatch (%d != %d)", len(ids), len(dataDirs))
	}

	cfgs := make([]*nodeConfig, 0, len(ids))
	bolts := make([]*raftboltdb.BoltStore, 0, len(ids))
	servers := make([]hraft.Server, 0, len(ids))
	seenID := make(map[string]struct{}, len(ids))
	seenDir := make(map[string]struct{}, len(ids))

	// cleanupOpened closes every store + transport opened so far (used on any
	// open-phase failure before nodes are started).
	cleanupOpened := func() {
		for _, nc := range cfgs {
			if nc.netTransport != nil {
				_ = nc.netTransport.Close()
			}
		}
		for _, b := range bolts {
			_ = b.Close()
		}
	}

	for i, id := range ids {
		if id == "" {
			cleanupOpened()
			return nil, fmt.Errorf("raft: empty node id")
		}
		if _, dup := seenID[id]; dup {
			cleanupOpened()
			return nil, fmt.Errorf("raft: duplicate node id %q", id)
		}
		seenID[id] = struct{}{}
		dir := dataDirs[i]
		if dir == "" {
			cleanupOpened()
			return nil, fmt.Errorf("raft: empty data dir for node %q", id)
		}
		if _, dup := seenDir[dir]; dup {
			cleanupOpened()
			return nil, fmt.Errorf("raft: duplicate data dir %q", dir)
		}
		seenDir[dir] = struct{}{}

		nc, boltStore, _, _, err := newPersistentNetworkNode(id, dir, base)
		if err != nil {
			cleanupOpened()
			return nil, err
		}
		cfgs = append(cfgs, nc)
		bolts = append(bolts, boltStore)
		servers = append(servers, hraft.Server{
			Suffrage: hraft.Voter,
			ID:       hraft.ServerID(nc.id),
			Address:  nc.addr, // the REAL bound TCP address (127.0.0.1:<ephemeral>)
		})
	}

	bootstrapCfg := hraft.Configuration{Servers: servers}
	nodes, err := wrapPersistentNetworkNodes(cfgs, bolts, bootstrapCfg)
	if err != nil {
		return nil, err
	}
	return &Cluster{nodes: nodes}, nil
}

// wrapPersistentNetworkNodes bootstraps + starts (NewRaft) each already-opened
// persistent+networked nodeConfig, returning the live *Node slice. It bootstraps
// the shared membership into every node's DURABLE stores on first init (a fresh
// cluster), mirroring NewNetworkCluster's wrap phase but additionally wiring each
// Node's closeStore to its BoltDB so Shutdown flushes the on-disk files.
//
// On ANY error it leaves NOTHING running: it Shutdown()s every Node already
// started (which stops raft, closes that node's TCP transport, and closes its
// bolt store) and closes the transport + bolt store of every cfg NOT yet wrapped,
// so no listener socket, raft goroutine, or bolt handle is leaked.
func wrapPersistentNetworkNodes(cfgs []*nodeConfig, bolts []*raftboltdb.BoltStore, bootstrapCfg hraft.Configuration) ([]*Node, error) {
	nodes := make([]*Node, 0, len(cfgs))

	cleanupPartial := func(failedFrom int) {
		for _, n := range nodes {
			_ = n.Shutdown()
		}
		for j := failedFrom; j < len(cfgs); j++ {
			if cfgs[j].netTransport != nil {
				_ = cfgs[j].netTransport.Close()
			}
			if bolts[j] != nil {
				_ = bolts[j].Close()
			}
		}
	}

	for i, nc := range cfgs {
		// Bootstrap the shared membership into this node's durable stores. On a
		// fresh cluster the stores are empty so BootstrapCluster succeeds; if a dir
		// already held state (reopen), BootstrapCluster returns ErrCantBootstrap and
		// we proceed straight to NewRaft (recovery from disk).
		if err := hraft.BootstrapCluster(nc.conf, nc.logStore, nc.stable, nc.snaps, nc.netTransport, cloneConfiguration(bootstrapCfg)); err != nil && err != hraft.ErrCantBootstrap {
			cleanupPartial(i)
			return nil, fmt.Errorf("raft: bootstrap persistent network node %s: %w", nc.id, err)
		}
		r, err := hraft.NewRaft(nc.conf, nc.fsm, nc.logStore, nc.stable, nc.snaps, nc.netTransport)
		if err != nil {
			cleanupPartial(i)
			return nil, fmt.Errorf("raft: start persistent network node %s: %w", nc.id, err)
		}
		nodes = append(nodes, &Node{
			id:           nc.id,
			raft:         r,
			fsm:          nc.fsm,
			netTransport: nc.netTransport,
			dataDir:      nc.dataDir(),
			closeStore:   bolts[i].Close,
		})
	}

	return nodes, nil
}
