package raftleader

import (
	"fmt"
	"time"

	hraft "github.com/hashicorp/raft"

	"github.com/HelixDevelopment/helix_cluster/pkg/raft"
)

// ElectionCluster is a set of RaftElection nodes backed by ONE real, in-memory
// Raft consensus group (assembled via pkg/raft.NewInmemCluster). It exists so
// the integration test — and local development — can exercise genuine leader
// election, leadership-change observation, and failover without any external
// network, etcd, or disk dependency.
type ElectionCluster struct {
	raftCluster *raft.Cluster
	elections   []*RaftElection
	byID        map[string]*RaftElection
}

// NewInmemElectionCluster builds an N-node Raft consensus group over in-memory
// transports/stores and wraps each node in a RaftElection. The nodes immediately
// begin a real election; use WaitForLeader to learn the winner.
//
// ids must be unique and non-empty.
func NewInmemElectionCluster(base *hraft.Config, ids ...string) (*ElectionCluster, error) {
	rc, err := raft.NewInmemCluster(base, ids...)
	if err != nil {
		return nil, err
	}
	elections := make([]*RaftElection, 0, len(rc.Nodes()))
	byID := make(map[string]*RaftElection, len(rc.Nodes()))
	for _, n := range rc.Nodes() {
		e := New(n)
		elections = append(elections, e)
		byID[e.ID()] = e
	}
	return &ElectionCluster{raftCluster: rc, elections: elections, byID: byID}, nil
}

// Elections returns the cluster's RaftElection nodes.
func (c *ElectionCluster) Elections() []*RaftElection { return c.elections }

// Get returns the RaftElection with the given id, or nil.
func (c *ElectionCluster) Get(id string) *RaftElection { return c.byID[id] }

// Leader returns the RaftElection that is currently the Raft leader, or nil if
// none is presently elected. It resolves leadership THROUGH the RaftElection
// adapter's own IsLeader()/LeaderID() (not the underlying raft.Node directly) so
// that the integration test genuinely exercises the adapted surface — an
// anti-bluff mutation of RaftElection.IsLeader must change this result.
func (c *ElectionCluster) Leader() *RaftElection {
	for _, e := range c.elections {
		if e.IsLeader() && e.LeaderID() == e.ID() {
			return e
		}
	}
	return nil
}

// Followers returns every RaftElection that is not currently the leader.
func (c *ElectionCluster) Followers() []*RaftElection {
	out := make([]*RaftElection, 0, len(c.elections))
	for _, e := range c.elections {
		if !e.IsLeader() {
			out = append(out, e)
		}
	}
	return out
}

// WaitForLeader polls (through the adapter's Leader resolution) until a leader
// is elected or the deadline expires, returning the leader's RaftElection.
// Outcome-based (it asserts on observed state), not a fixed sleep.
func (c *ElectionCluster) WaitForLeader(timeout time.Duration) (*RaftElection, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if l := c.Leader(); l != nil {
			return l, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("raftleader: no leader elected within %s", timeout)
}

// WaitForNewLeader polls until a leader different from excludeID is elected, or
// the deadline expires. Used after killing a leader to assert a NEW leader
// emerges among the survivors. Resolution goes through the adapter so an
// anti-bluff mutation that makes every node "leader" breaks the differs-check.
func (c *ElectionCluster) WaitForNewLeader(excludeID string, timeout time.Duration) (*RaftElection, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if l := c.Leader(); l != nil && l.ID() != excludeID {
			return l, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("raftleader: no new leader (excluding %s) elected within %s", excludeID, timeout)
}

// CountLeaders returns how many nodes currently consider themselves the leader.
// In a healthy Raft group this is at most 1; the integration test asserts on it
// to prove "exactly one leader".
func (c *ElectionCluster) CountLeaders() int {
	count := 0
	for _, e := range c.elections {
		if e.IsLeader() && e.LeaderID() == e.ID() {
			count++
		}
	}
	return count
}

// WaitForSingleLeader polls until exactly one node reports leadership (guarding
// against a transient window where zero or, briefly, more than one node claims
// it), returning the unique leader.
func (c *ElectionCluster) WaitForSingleLeader(timeout time.Duration) (*RaftElection, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.CountLeaders() == 1 {
			if l := c.Leader(); l != nil {
				return l, nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("raftleader: did not converge to exactly one leader within %s (saw %d)", timeout, c.CountLeaders())
}

// KillLeader shuts down the current leader's underlying Raft node and stops its
// RaftElection watcher, simulating a leader failure. It returns the id of the
// killed leader. The survivors must then re-elect a NEW leader (proven by the
// test).
func (c *ElectionCluster) KillLeader() (string, error) {
	l := c.Leader()
	if l == nil {
		return "", fmt.Errorf("raftleader: no leader to kill")
	}
	id := l.ID()
	l.Close()
	if err := l.Node().Shutdown(); err != nil {
		return id, err
	}
	return id, nil
}

// Survivors returns every RaftElection except the one with excludeID.
func (c *ElectionCluster) Survivors(excludeID string) []*RaftElection {
	out := make([]*RaftElection, 0, len(c.elections))
	for _, e := range c.elections {
		if e.ID() != excludeID {
			out = append(out, e)
		}
	}
	return out
}

// Close stops every RaftElection watcher and shuts down the underlying Raft
// cluster. Safe to call once at the end of a test (e.g. via t.Cleanup); it
// tolerates nodes already killed by KillLeader.
func (c *ElectionCluster) Close() error {
	for _, e := range c.elections {
		e.Close()
	}
	return c.raftCluster.Shutdown()
}
