// Command raftkv-demo is a small, runnable distributed key/value service built on
// top of the proven embedded-Raft building blocks (pkg/raft + pkg/raftleader).
// It proves those blocks compose into a usable replicated service: a 3-node
// in-memory Raft cluster exposing Put (leader-routed, replicated through the Raft
// log) and Get (served from any node's replicated FSM).
//
// This is NOT a mock (CLAUDE-1): every Put goes through real Raft consensus —
// committed by a majority and applied to every replica's FSM before it returns —
// and every Get reads the replicated FSM, so a value is only visible on a node
// once Raft has actually replicated it there. Leader failover is real too: when
// the leader is killed, the survivors run a genuine election and previously-Put
// keys remain readable because they were already committed to the surviving FSMs.
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/raft"
	"github.com/HelixDevelopment/helix_cluster/pkg/raftleader"
)

// ErrNotLeader is re-exported so callers/tests of the KV service can detect a
// write that was rejected because it targeted a follower. It is the same sentinel
// the underlying Raft layer uses, so errors.Is works across the whole stack.
var ErrNotLeader = raftleader.ErrNotLeader

// applyTimeout bounds how long a single replicated Put may take to commit. It is
// an upper bound, not a fixed sleep: Apply returns as soon as a majority commits.
const applyTimeout = 2 * time.Second

// KVService is a 3-(or N-)node replicated key/value store. It owns a real
// in-memory Raft consensus group (one node per replica) and routes writes to the
// leader while serving reads from any replica's replicated FSM.
//
// Write path  (Put): caller -> leader's Raft.Apply -> replicated to majority ->
//                     applied to EVERY replica's KVFSM in identical order.
// Read path   (Get): caller -> chosen replica's KVFSM.Get (already-replicated).
//
// Because writes traverse the Raft log and reads come from the FSM, the service
// is the genuine composition of the leader-election + log-replication primitives,
// not a local map with a leader flag.
type KVService struct {
	cluster *raftleader.ElectionCluster
}

// NewKVService brings up an n-node replicated KV service over an in-memory Raft
// consensus group and blocks (up to electTimeout) until a leader is elected, so
// the returned service is immediately usable. Call Close to tear it down.
func NewKVService(electTimeout time.Duration, ids ...string) (*KVService, error) {
	if len(ids) == 0 {
		return nil, errors.New("raftkv: need at least one node id")
	}
	cluster, err := raftleader.NewInmemElectionCluster(nil, ids...)
	if err != nil {
		return nil, fmt.Errorf("raftkv: build cluster: %w", err)
	}
	svc := &KVService{cluster: cluster}
	if _, err := cluster.WaitForSingleLeader(electTimeout); err != nil {
		_ = cluster.Close()
		return nil, fmt.Errorf("raftkv: cluster did not elect a leader: %w", err)
	}
	return svc, nil
}

// Cluster exposes the underlying election cluster (read-only / advanced use by
// the demo and integration test: leader id, kill-the-leader, per-node FSM).
func (s *KVService) Cluster() *raftleader.ElectionCluster { return s.cluster }

// LeaderID returns the id of the current Raft leader as observed through the
// election adapter, or "" if no leader is presently known.
func (s *KVService) LeaderID() string {
	if l := s.cluster.Leader(); l != nil {
		return l.ID()
	}
	return ""
}

// Put writes key=val into the replicated store. It is leader-routed: the command
// is proposed on whichever node is currently the leader, committed by a majority
// of the Raft group, and applied to EVERY replica's FSM before Put returns. If no
// leader is currently elected it returns ErrNotLeader.
//
// This is the "forwarding" behaviour: Put always finds and uses the leader, so a
// caller need not know which node leads. To exercise the raw follower-rejection
// path (a write proposed directly on a follower), use PutVia with a follower id.
func (s *KVService) Put(key, val string) error {
	leader := s.cluster.Leader()
	if leader == nil {
		return fmt.Errorf("raftkv: put %q: %w", key, ErrNotLeader)
	}
	if err := leader.Apply(raft.Command{Op: raft.CommandSet, Key: key, Value: val}, applyTimeout); err != nil {
		return fmt.Errorf("raftkv: put %q via leader %s: %w", key, leader.ID(), err)
	}
	return nil
}

// PutVia proposes a write on the specific node nodeID, WITHOUT routing to the
// leader. On the leader it behaves like Put; on a follower it returns ErrNotLeader
// (the Raft layer refuses to apply a write on a non-leader). The integration test
// uses this to prove that an un-routed write to a follower is rejected.
func (s *KVService) PutVia(nodeID, key, val string) error {
	node := s.cluster.Get(nodeID)
	if node == nil {
		return fmt.Errorf("raftkv: put %q: unknown node %q", key, nodeID)
	}
	if err := node.Apply(raft.Command{Op: raft.CommandSet, Key: key, Value: val}, applyTimeout); err != nil {
		return fmt.Errorf("raftkv: put %q via %s: %w", key, nodeID, err)
	}
	return nil
}

// Get reads key from the replicated FSM of node nodeID. It returns the value and
// whether the key exists on THAT replica. Reading per-node (not from a shared
// map) is what lets the test prove replication: a key Put via the leader must be
// readable with the correct value from every node's own FSM.
func (s *KVService) Get(nodeID, key string) (string, bool, error) {
	node := s.cluster.Get(nodeID)
	if node == nil {
		return "", false, fmt.Errorf("raftkv: get %q: unknown node %q", key, nodeID)
	}
	v, ok := node.Node().FSM().Get(key)
	return v, ok, nil
}

// NodeIDs returns the ids of all replicas (including any that have been killed —
// callers that only want live nodes should consult the cluster's survivors).
func (s *KVService) NodeIDs() []string {
	elections := s.cluster.Elections()
	ids := make([]string, 0, len(elections))
	for _, e := range elections {
		ids = append(ids, e.ID())
	}
	return ids
}

// WaitReplicated polls until key resolves to want on EVERY listed node's FSM, or
// the deadline expires. It is outcome-based (asserts on observed replicated
// state) rather than a fixed sleep: replication latency varies, so the test waits
// for the actual sink-side condition. Returns nil once all nodes agree.
func (s *KVService) WaitReplicated(key, want string, nodeIDs []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		all := true
		for _, id := range nodeIDs {
			v, ok, err := s.Get(id, key)
			if err != nil {
				return err
			}
			if !ok || v != want {
				all = false
				break
			}
		}
		if all {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("raftkv: key %q did not replicate to %v as %q within %s", key, nodeIDs, want, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// KillLeader removes the current leader from the cluster (simulating a crash) and
// returns its id. The survivors must then elect a NEW leader, which the caller
// can await with WaitNewLeader.
func (s *KVService) KillLeader() (string, error) {
	return s.cluster.KillLeader()
}

// WaitNewLeader blocks until a leader other than excludeID is elected among the
// survivors, returning its id. Used after KillLeader to prove real failover.
func (s *KVService) WaitNewLeader(excludeID string, timeout time.Duration) (string, error) {
	l, err := s.cluster.WaitForNewLeader(excludeID, timeout)
	if err != nil {
		return "", err
	}
	return l.ID(), nil
}

// SurvivorIDs returns the ids of every node except excludeID — i.e. the nodes
// still alive after KillLeader(excludeID).
func (s *KVService) SurvivorIDs(excludeID string) []string {
	survivors := s.cluster.Survivors(excludeID)
	ids := make([]string, 0, len(survivors))
	for _, e := range survivors {
		ids = append(ids, e.ID())
	}
	return ids
}

// Close tears down the service and its Raft cluster.
func (s *KVService) Close() error { return s.cluster.Close() }
