package raft

import (
	"fmt"
	"time"

	hraft "github.com/hashicorp/raft"
)

// Cluster is a set of in-memory Raft nodes wired together for testing and local
// development. It bootstraps a single Raft consensus group across N nodes whose
// transports are fully connected, so they can elect a leader and replicate logs
// without any external network, etcd, or disk dependency.
type Cluster struct {
	nodes []*Node
}

// Nodes returns the cluster's nodes.
func (c *Cluster) Nodes() []*Node { return c.nodes }

// NewInmemCluster constructs an N-node Raft cluster over in-memory transports
// and stores, bootstraps it with an N-server configuration, and returns the
// started nodes. The nodes immediately begin an election; callers should poll
// WaitForLeader to learn the elected leader.
//
// ids must be unique and non-empty. This is the helper the kill-the-leader
// integration test uses to assemble a real 3-node cluster.
func NewInmemCluster(base *hraft.Config, ids ...string) (*Cluster, error) {
	if len(ids) < 1 {
		return nil, fmt.Errorf("raft: cluster needs at least 1 node")
	}

	// Build per-node wiring first so we can interconnect transports.
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

		nc, err := newInmemNode(id, base)
		if err != nil {
			return nil, err
		}
		cfgs = append(cfgs, nc)
		servers = append(servers, hraft.Server{
			Suffrage: hraft.Voter,
			ID:       hraft.ServerID(nc.id),
			Address:  nc.addr,
		})
	}

	// Fully connect every transport to every other so peers can reach each other.
	for i := range cfgs {
		for j := range cfgs {
			if i == j {
				continue
			}
			cfgs[i].transport.Connect(cfgs[j].addr, cfgs[j].transport)
		}
	}

	bootstrapCfg := hraft.Configuration{Servers: servers}

	nodes := make([]*Node, 0, len(cfgs))
	for _, nc := range cfgs {
		// Bootstrap the shared configuration into each node's stores. Bootstrap
		// is idempotent per node; every node is given the identical N-server
		// configuration so they agree on membership.
		if err := hraft.BootstrapCluster(nc.conf, nc.logStore, nc.stable, nc.snaps, nc.transport, cloneConfiguration(bootstrapCfg)); err != nil {
			return nil, fmt.Errorf("raft: bootstrap node %s: %w", nc.id, err)
		}
		r, err := hraft.NewRaft(nc.conf, nc.fsm, nc.logStore, nc.stable, nc.snaps, nc.transport)
		if err != nil {
			return nil, fmt.Errorf("raft: start node %s: %w", nc.id, err)
		}
		nodes = append(nodes, &Node{id: nc.id, raft: r, fsm: nc.fsm, trans: nc.transport})
	}

	return &Cluster{nodes: nodes}, nil
}

// cloneConfiguration returns a deep copy of a configuration's server slice so
// each node bootstraps from its own copy.
func cloneConfiguration(c hraft.Configuration) hraft.Configuration {
	servers := make([]hraft.Server, len(c.Servers))
	copy(servers, c.Servers)
	return hraft.Configuration{Servers: servers}
}

// Leader returns the current leader node, or nil if none is currently elected.
// A node is considered the leader only if it reports Leader state AND advertises
// itself as the leader (guards against transient split views).
func (c *Cluster) Leader() *Node {
	for _, n := range c.nodes {
		if n.IsLeader() && n.Leader() == n.ID() {
			return n
		}
	}
	return nil
}

// WaitForLeader polls until a single leader is elected or the deadline expires.
// It returns the leader node or an error on timeout. This is deterministic in
// outcome (it asserts on observed state) rather than relying on a fixed sleep.
func (c *Cluster) WaitForLeader(timeout time.Duration) (*Node, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if l := c.Leader(); l != nil {
			return l, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("raft: no leader elected within %s", timeout)
}

// WaitForNewLeader polls until a leader different from excludeID is elected, or
// the deadline expires. Used after killing the previous leader to assert that a
// NEW leader emerges among the survivors.
func (c *Cluster) WaitForNewLeader(excludeID string, timeout time.Duration) (*Node, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if l := c.Leader(); l != nil && l.ID() != excludeID {
			return l, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("raft: no new leader (excluding %s) elected within %s", excludeID, timeout)
}

// Survivors returns all nodes except the one with the given id, skipping nil.
func (c *Cluster) Survivors(excludeID string) []*Node {
	out := make([]*Node, 0, len(c.nodes))
	for _, n := range c.nodes {
		if n.ID() != excludeID {
			out = append(out, n)
		}
	}
	return out
}

// Shutdown stops every node in the cluster.
func (c *Cluster) Shutdown() error {
	var firstErr error
	for _, n := range c.nodes {
		if n.State() == hraft.Shutdown {
			continue
		}
		if err := n.Shutdown(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
