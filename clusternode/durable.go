package clusternode

// This file adds a DURABLE-CONSENSUS capability to the clusternode layer without
// disturbing the existing in-memory leader-election path.
//
// The plain NodeAgent/NodeCluster compose a SHARED in-memory raftleader election
// group (cluster.go / nodeagent.go): great for leader election, but its state
// lives only in memory and does not survive a process restart. This file gives
// each NodeAgent the ability to additionally own ONE real durable raft node from
// pkg/raft — a node backed by BOTH a real loopback TCP transport AND a real
// on-disk BoltDB store (raft.NewPersistentNetworkNodeAt). Through that node the
// agent exposes a small cluster-wide replicated key/value surface — Put/Get —
// implemented on the SAME raft.KVFSM the consensus layer already replicates (no
// parallel non-raft store is invented).
//
// The decisive property this enables, exercised end-to-end by the integration
// test, is that a value written through the agent API survives a FULL cluster
// shutdown and is recovered from on-disk BoltDB when the agents are re-created
// from the SAME data dirs — durable consensus proven AT the clusternode layer,
// through clusternode's own API rather than a direct pkg/raft call.
//
// # Why fixed bind addresses
//
// A persistent raft node persists its cluster CONFIGURATION (the set of peer
// addresses) to disk alongside its log. For a full-cluster shutdown+restart to
// recover purely from disk, each node must rebind the SAME address its persisted
// configuration already references — otherwise the recovered peers point at dead
// ports. So the durable cluster reserves three fixed loopback ports up front,
// bootstraps the full membership across those fixed addresses, and rebinds the
// identical address per node on restart (raft.NewPersistentNetworkNodeAt with a
// fixed bindAddr). This is the multi-process-faithful path: the same mechanism a
// real deployment uses to reconnect daemons by known address after a reboot.

import (
	"fmt"
	"net"
	"time"

	hraft "github.com/hashicorp/raft"

	"github.com/HelixDevelopment/helix_cluster/discovery/p2p"
	"github.com/HelixDevelopment/helix_cluster/edge/gateway"
	"github.com/HelixDevelopment/helix_cluster/pkg/raft"
	"github.com/HelixDevelopment/helix_cluster/pkg/raftleader"
)

// durableSeat is a NodeAgent's durable-consensus state: the live persistent+
// networked raft node, the on-disk data dir backing it, and the fixed loopback
// TCP address it binds (stable across restarts so the persisted configuration
// stays valid). It is owned by the agent and torn down in Stop.
type durableSeat struct {
	node    *raft.PersistentNetworkNode
	dataDir string
	bind    string
}

// DurableConfig configures the durable-consensus backing of a single NodeAgent.
// All three fields are required when an agent is to be durable.
type DurableConfig struct {
	// DataDir is the on-disk directory backing this agent's raft node (its
	// BoltDB log+stable store and FileSnapshotStore). It MUST be reused verbatim
	// across a restart for state to be recovered from disk.
	DataDir string

	// Bind is the fixed loopback TCP address this agent's raft transport binds
	// (e.g. "127.0.0.1:7501"). It MUST be reused verbatim across a restart so the
	// peer addresses in the persisted raft configuration remain reachable.
	Bind string

	// Servers is the full initial cluster membership (every agent's id=bind,
	// including self), used ONLY on first boot to bootstrap the durable stores.
	// It is ignored on reopen (the durable on-disk configuration wins).
	Servers []hraft.Server
}

// newDurableAgent constructs a NodeAgent whose durable-consensus seat is a real
// persistent+networked raft node created from dc. The agent's non-durable
// subsystems (discovery host + messaging gateway) are NOT started here — call
// Start as usual; the raft node, by contrast, is live the moment it is created
// (it immediately participates in election/replication), mirroring pkg/raft.
//
// election is the agent's seat in the SHARED in-memory raftleader group, kept so
// the existing IsLeader()/Election() surface continues to work unchanged. The
// durable surface (Put/Get/IsDurableLeader) is independent of it.
func newDurableAgent(id string, election *raftleader.RaftElection, dc DurableConfig, logger p2p.Logger) (*NodeAgent, error) {
	if dc.DataDir == "" {
		return nil, fmt.Errorf("clusternode: durable agent %q requires a DataDir", id)
	}
	if dc.Bind == "" {
		return nil, fmt.Errorf("clusternode: durable agent %q requires a Bind address", id)
	}

	agent, err := NewNodeAgent(Config{
		ID:       id,
		Election: election,
		Logger:   logger,
	})
	if err != nil {
		return nil, err
	}

	// Create the REAL persistent+networked raft node bound at the FIXED address.
	// On first boot (fresh data dir) it bootstraps dc.Servers; on reopen (same
	// data dir after a prior Shutdown) it recovers the persisted log/snapshot from
	// disk and does NOT re-bootstrap.
	pn, err := raft.NewPersistentNetworkNodeAt(id, dc.DataDir, dc.Bind, nil, dc.Servers)
	if err != nil {
		return nil, fmt.Errorf("clusternode: durable agent %q create raft node: %w", id, err)
	}

	agent.durable = &durableSeat{
		node:    pn,
		dataDir: dc.DataDir,
		bind:    dc.Bind,
	}
	return agent, nil
}

// IsDurable reports whether this agent is backed by a durable-consensus raft node.
func (a *NodeAgent) IsDurable() bool { return a.durable != nil }

// DurableDataDir returns the on-disk data dir backing this agent's durable raft
// node, or "" if the agent is not durable. It is the directory whose raft.db the
// on-disk persistence proof inspects.
func (a *NodeAgent) DurableDataDir() string {
	if a.durable == nil {
		return ""
	}
	return a.durable.dataDir
}

// DurableTCPAddr returns the agent's durable raft node's bound loopback TCP
// address, or an error if the agent is not durable. It is the REAL-TCP witness:
// a concrete *net.TCPAddr with a non-zero port.
func (a *NodeAgent) DurableTCPAddr() (*net.TCPAddr, error) {
	if a.durable == nil {
		return nil, fmt.Errorf("clusternode: agent %q is not durable", a.id)
	}
	return a.durable.node.TCPAddr, nil
}

// IsDurableLeader reports whether this agent's durable raft node currently holds
// raft leadership. It is distinct from IsLeader() (the in-memory raftleader
// group); a durable write must go through whichever agent is the durable leader.
func (a *NodeAgent) IsDurableLeader() bool {
	return a.durable != nil && a.durable.node.Node.IsLeader()
}

// WaitForDurableLeader blocks until this agent's durable raft node observes a
// single elected leader (which may be itself or a peer), or the timeout elapses.
// On return, a Put issued to the durable leader is guaranteed to find an elected
// leader. It is outcome-based polling, not a fixed sleep.
func (a *NodeAgent) WaitForDurableLeader(timeout time.Duration) error {
	if a.durable == nil {
		return fmt.Errorf("clusternode: agent %q is not durable", a.id)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if a.durable.node.Node.Leader() != "" {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("clusternode: durable agent %q saw no leader within %s", a.id, timeout)
}

// Put writes a cluster-wide replicated key/value pair through this agent's
// durable raft node. It MUST be issued to the durable LEADER: it proposes a
// CommandSet to the raft log, which is replicated to a majority and applied to
// every node's KVFSM before returning. On a follower it returns the raft
// not-leader error (raft.ErrNotLeader), exactly as a real leader-only write
// surface should — the caller redirects to the durable leader.
//
// This is the replicated-state operation exposed AT the clusternode layer: it
// reuses raft.KVFSM (no parallel store), so a value Put through one agent becomes
// readable via Get on every agent once replication completes.
func (a *NodeAgent) Put(key, value string, timeout time.Duration) error {
	if a.durable == nil {
		return fmt.Errorf("clusternode: agent %q is not durable (no Put surface)", a.id)
	}
	return a.durable.node.Node.Apply(raft.Command{Op: raft.CommandSet, Key: key, Value: value}, timeout)
}

// Get reads a key from this agent's durable raft node's replicated KVFSM and
// reports whether it exists. Any agent can serve a Get from its LOCAL replica;
// after a Put has replicated, a follower's Get returns the same value the leader
// wrote. After a restart, the value Get returns came from the on-disk BoltDB the
// node recovered on reopen.
func (a *NodeAgent) Get(key string) (string, bool, error) {
	if a.durable == nil {
		return "", false, fmt.Errorf("clusternode: agent %q is not durable (no Get surface)", a.id)
	}
	v, ok := a.durable.node.Node.FSM().Get(key)
	return v, ok, nil
}

// shutdownDurable stops the agent's durable raft node (stops raft, closes the TCP
// listener, then closes the BoltDB so its files flush and the data dir can be
// reopened). It is invoked from Stop and is safe to call when not durable.
func (a *NodeAgent) shutdownDurable() error {
	if a.durable == nil {
		return nil
	}
	return a.durable.node.Node.Shutdown()
}

// --- Durable NodeCluster ----------------------------------------------------

// reserveLoopbackPorts reserves n free loopback TCP ports by binding then
// immediately closing a listener on each, returning the chosen "127.0.0.1:port"
// addresses. There is an inherent (small) race between releasing the port and
// raft rebinding it, but on loopback with immediate reuse it is reliable for an
// in-process test; the fixed addresses are what let a full-cluster restart
// recover from disk by known address.
func reserveLoopbackPorts(n int) ([]string, error) {
	addrs := make([]string, 0, n)
	listeners := make([]*net.TCPListener, 0, n)
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	for i := 0; i < n; i++ {
		l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			return nil, fmt.Errorf("clusternode: reserve loopback port %d: %w", i, err)
		}
		listeners = append(listeners, l)
		addrs = append(addrs, l.Addr().String())
	}
	return addrs, nil
}

// NewNodeClusterWithDurableConsensus builds an N-node in-process cluster whose
// agents are EACH backed by a real durable persistent+networked raft node (own
// data dir, own fixed loopback TCP bind) — in addition to sharing the in-memory
// raftleader election group a plain NodeCluster uses. dataDirs[i] is the durable
// directory for ids[i]; they must be the same length, ids unique & non-empty,
// dirs distinct.
//
// On FIRST construction (fresh data dirs) every node bootstraps the shared
// membership across the reserved fixed addresses. To RESTART a previously-built
// durable cluster from the SAME data dirs, call this again with the SAME ids,
// SAME data dirs and the SAME binds (pass them via the returned cluster's
// DurableBinds(), or supply your own) — see RecreateDurableCluster.
//
// The returned NodeCluster's agents expose Put/Get/IsDurableLeader/
// WaitForDurableLeader. The non-durable subsystems are NOT started yet — call
// Start to bring discovery + gateways online, exactly as for a plain cluster.
func NewNodeClusterWithDurableConsensus(ids []string, dataDirs []string) (*NodeCluster, error) {
	binds, err := reserveLoopbackPorts(len(ids))
	if err != nil {
		return nil, err
	}
	return RecreateDurableCluster(ids, dataDirs, binds)
}

// RecreateDurableCluster builds (or REBUILDS) a durable NodeCluster with EXPLICIT
// fixed binds. It is the constructor a restart uses: pass the SAME ids, SAME
// dataDirs and SAME binds a prior durable cluster used, and each agent's raft
// node reopens its data dir and recovers committed state from on-disk BoltDB.
//
// ids/dataDirs/binds must all be the same non-zero length, ids unique &
// non-empty, dataDirs & binds distinct. On any partial failure it shuts down
// every durable node it already created so nothing leaks.
func RecreateDurableCluster(ids []string, dataDirs []string, binds []string) (*NodeCluster, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("clusternode: durable cluster needs at least one node id")
	}
	if len(ids) != len(dataDirs) || len(ids) != len(binds) {
		return nil, fmt.Errorf("clusternode: durable cluster ids/dataDirs/binds length mismatch (%d/%d/%d)", len(ids), len(dataDirs), len(binds))
	}

	// Shared in-memory election group across the ids, exactly as the plain cluster
	// builds — so the existing IsLeader()/Election() surface is unchanged.
	ec, err := raftleader.NewInmemElectionCluster(nil, ids...)
	if err != nil {
		return nil, fmt.Errorf("clusternode: build raft election group: %w", err)
	}

	// Full durable membership across the fixed binds (used only on first boot;
	// ignored on reopen, where the durable on-disk configuration wins).
	servers := make([]hraft.Server, 0, len(ids))
	for i, id := range ids {
		servers = append(servers, hraft.Server{
			Suffrage: hraft.Voter,
			ID:       hraft.ServerID(id),
			Address:  hraft.ServerAddress(binds[i]),
		})
	}

	agents := make([]*NodeAgent, 0, len(ids))
	byID := make(map[string]*NodeAgent, len(ids))

	// On any failure mid-build, tear down the durable nodes already created.
	ok := false
	defer func() {
		if ok {
			return
		}
		for _, a := range agents {
			_ = a.shutdownDurable()
		}
		_ = ec.Close()
	}()

	for i, id := range ids {
		seat := ec.Get(id)
		if seat == nil {
			return nil, fmt.Errorf("clusternode: raft group has no seat for id %q", id)
		}
		agent, err := newDurableAgent(id, seat, DurableConfig{
			DataDir: dataDirs[i],
			Bind:    binds[i],
			Servers: servers,
		}, p2p.NopLogger{})
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
		byID[id] = agent
	}

	ok = true
	return &NodeCluster{
		election:    ec,
		agents:      agents,
		byID:        byID,
		routes:      make(map[string]map[string]*gateway.LoopbackConn),
		durableBind: binds,
	}, nil
}

// WaitForDurableLeader blocks until the durable raft group converges to a leader
// observed by every agent, returning the agent that is the durable leader. It is
// outcome-based polling on the durable nodes' raft state.
func (c *NodeCluster) WaitForDurableLeader(timeout time.Duration) (*NodeAgent, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var leader *NodeAgent
		allSee := true
		for _, a := range c.agents {
			if a.durable == nil {
				return nil, fmt.Errorf("clusternode: agent %q is not durable", a.ID())
			}
			ld := a.durable.node.Node.Leader()
			if ld == "" {
				allSee = false
				break
			}
			if a.IsDurableLeader() {
				leader = a
			}
		}
		if allSee && leader != nil {
			return leader, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("clusternode: durable cluster saw no agreed leader within %s", timeout)
}

// DurableBinds returns the fixed loopback TCP binds this durable cluster's agents
// use, in id order. Hand them (with the same ids + data dirs) to
// RecreateDurableCluster to restart the cluster from disk on the same addresses.
func (c *NodeCluster) DurableBinds() []string {
	out := make([]string, len(c.durableBind))
	copy(out, c.durableBind)
	return out
}
