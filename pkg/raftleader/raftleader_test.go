package raftleader

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/raft"
)

const (
	electionTimeout = 10 * time.Second
	applyTimeout    = 5 * time.Second
)

// TestRaftElectionThreeNodeKillLeaderReelection is the REAL 3-node integration
// test mandated by CLAUDE-1 (no mocks for a real-operation feature). It proves
// that RaftElection is backed by genuine embedded Raft, not a stub:
//
//  1. brings up 3 RaftElection nodes over pkg/raft's in-mem cluster,
//  2. asserts EXACTLY ONE leader is elected; leader.IsLeader()==true and every
//     follower's IsLeader()==false,
//  3. observes a real leadership-change event on the leader's LeadershipChanges
//     channel (sourced from hashicorp/raft's LeaderCh),
//  4. commits a value through the leader BEFORE the kill,
//  5. KILLS the leader and asserts a NEW leader is elected among the survivors
//     (different node id),
//  6. asserts the value committed before the kill is still readable AFTER on the
//     survivors (leadership AND replicated state survive the failover).
func TestRaftElectionThreeNodeKillLeaderReelection(t *testing.T) {
	logf := func(format string, args ...any) { t.Logf("[raftleader-it] "+format, args...) }

	cluster, err := NewInmemElectionCluster(nil, "node-a", "node-b", "node-c")
	if err != nil {
		t.Fatalf("create 3-node election cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Close() })

	if got := len(cluster.Elections()); got != 3 {
		t.Fatalf("expected 3 election nodes, got %d", got)
	}

	// 1+2. Exactly one leader is elected.
	leader, err := cluster.WaitForSingleLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no single leader elected: %v", err)
	}
	initialLeaderID := leader.ID()
	if n := cluster.CountLeaders(); n != 1 {
		t.Fatalf("expected EXACTLY ONE leader, got %d", n)
	}
	if !leader.IsLeader() {
		t.Fatalf("elected leader %s reports IsLeader()=false", initialLeaderID)
	}
	if leader.LeaderID() != initialLeaderID {
		t.Fatalf("leader %s LeaderID()=%q, want self", initialLeaderID, leader.LeaderID())
	}

	// Count, across ALL nodes, how many report IsLeader()==true. This drives the
	// assertion directly off RaftElection.IsLeader (not LeaderID), so an
	// anti-bluff mutation of IsLeader -> always true makes this != 1 and FAILS.
	isLeaderTrue := 0
	for _, e := range cluster.Elections() {
		if e.IsLeader() {
			isLeaderTrue++
		}
	}
	if isLeaderTrue != 1 {
		t.Fatalf("EXACTLY ONE node must report IsLeader()=true, got %d", isLeaderTrue)
	}
	logf("EXACTLY ONE LEADER ELECTED: %s (1/%d nodes report IsLeader=true)",
		initialLeaderID, len(cluster.Elections()))

	// There must be exactly 2 followers, each reporting IsLeader()==false and
	// agreeing on the leader id. The count assertion also fails under the bluff
	// (Followers() returns only !IsLeader nodes, so a mutant yields 0 followers).
	followers := cluster.Followers()
	if len(followers) != 2 {
		t.Fatalf("expected EXACTLY 2 followers, got %d (a mutant IsLeader=true yields 0)", len(followers))
	}
	for _, f := range followers {
		if f.IsLeader() {
			t.Fatalf("follower %s incorrectly reports IsLeader()=true", f.ID())
		}
		if f.LeaderID() != initialLeaderID {
			t.Fatalf("follower %s sees leader %q, want %q", f.ID(), f.LeaderID(), initialLeaderID)
		}
	}
	logf("FOLLOWERS CONFIRMED: %d followers all report IsLeader()=false and agree leader=%s",
		len(followers), initialLeaderID)

	// 3. Observe a real leadership-acquire event on the leader's channel.
	if err := expectLeadershipAcquire(leader, 5*time.Second); err != nil {
		t.Fatalf("did not observe leadership-acquire event for %s: %v", initialLeaderID, err)
	}
	logf("LEADERSHIP-CHANGE EVENT OBSERVED: acquire on %s", initialLeaderID)

	// 2b. Non-leaders reject writes with ErrNotLeader.
	for _, f := range cluster.Followers() {
		err := f.Apply(raft.Command{Op: raft.CommandSet, Key: "denied", Value: "x"}, applyTimeout)
		if !errors.Is(err, ErrNotLeader) {
			t.Fatalf("follower %s Apply: want ErrNotLeader, got %v", f.ID(), err)
		}
	}
	logf("WRITE-GATING CONFIRMED: all followers rejected Apply with ErrNotLeader")

	// 4. Commit a value through the leader BEFORE the kill.
	const survivKey, survivVal = "cluster/leader-epoch", "pre-kill-value"
	if err := leader.Apply(raft.Command{Op: raft.CommandSet, Key: survivKey, Value: survivVal}, applyTimeout); err != nil {
		t.Fatalf("leader %s failed to commit pre-kill value: %v", initialLeaderID, err)
	}
	if err := waitForReplication(cluster.Survivors(""), map[string]string{survivKey: survivVal}, 5*time.Second); err != nil {
		t.Fatalf("pre-kill value did not replicate to all nodes: %v", err)
	}
	logf("PRE-KILL VALUE COMMITTED & REPLICATED: %s=%s", survivKey, survivVal)

	// 5. KILL the leader.
	killedID, err := cluster.KillLeader()
	if err != nil {
		t.Fatalf("kill leader: %v", err)
	}
	if killedID != initialLeaderID {
		t.Fatalf("killed %s but initial leader was %s", killedID, initialLeaderID)
	}
	logf("LEADER KILLED: %s", killedID)

	// 5b. A NEW leader must be elected among the survivors, with a DIFFERENT id.
	newLeader, err := cluster.WaitForNewLeader(killedID, electionTimeout)
	if err != nil {
		t.Fatalf("no NEW leader after killing %s: %v", killedID, err)
	}
	if newLeader.ID() == killedID {
		t.Fatalf("new leader %s must differ from killed %s", newLeader.ID(), killedID)
	}
	if !newLeader.IsLeader() {
		t.Fatalf("new leader %s reports IsLeader()=false", newLeader.ID())
	}
	logf("NEW LEADER ELECTED after kill: %s (was %s)", newLeader.ID(), killedID)

	// 6. The pre-kill value must still be readable on the survivors (replicated
	// state survived the leadership change).
	survivors := cluster.Survivors(killedID)
	if len(survivors) != 2 {
		t.Fatalf("expected 2 survivors, got %d", len(survivors))
	}
	for _, s := range survivors {
		got, ok := s.Node().FSM().Get(survivKey)
		if !ok {
			t.Fatalf("STATE LOSS: survivor %s missing %q after re-election", s.ID(), survivKey)
		}
		if got != survivVal {
			t.Fatalf("STATE CORRUPTION: survivor %s %q=%q, want %q", s.ID(), survivKey, got, survivVal)
		}
	}
	logf("STATE SURVIVED FAILOVER: %s=%s intact on both survivors after re-election", survivKey, survivVal)

	// 6b. The new leader can still commit (cluster functional, not just alive).
	if err := newLeader.Apply(raft.Command{Op: raft.CommandSet, Key: "post-kill", Value: "ok"}, applyTimeout); err != nil {
		t.Fatalf("new leader %s could not commit post-kill: %v", newLeader.ID(), err)
	}
	if err := waitForReplication(survivors, map[string]string{"post-kill": "ok"}, 5*time.Second); err != nil {
		t.Fatalf("post-kill write did not replicate: %v", err)
	}
	logf("PASS: real-Raft leader election survived leader kill — re-elected %s, state intact, writable",
		newLeader.ID())
}

// TestRaftElectionResignTransfersLeadership proves Resign performs a REAL Raft
// leadership transfer (not a local flag flip): after a leader resigns, a
// different node becomes leader while the cluster stays healthy.
func TestRaftElectionResignTransfersLeadership(t *testing.T) {
	cluster, err := NewInmemElectionCluster(nil, "r1", "r2", "r3")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Close() })

	leader, err := cluster.WaitForSingleLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no leader: %v", err)
	}
	oldID := leader.ID()
	if err := leader.Resign(); err != nil {
		t.Fatalf("resign: %v", err)
	}
	newLeader, err := cluster.WaitForNewLeader(oldID, electionTimeout)
	if err != nil {
		t.Fatalf("no new leader after resign of %s: %v", oldID, err)
	}
	if newLeader.ID() == oldID {
		t.Fatalf("leadership did not transfer away from %s", oldID)
	}
	t.Logf("[raftleader-it] RESIGN TRANSFERRED leadership %s -> %s", oldID, newLeader.ID())
}

// TestRaftElectionSingleNodeCampaign is a fast wiring sanity check: a single-node
// cluster elects itself, Campaign observes it, and Apply commits.
func TestRaftElectionSingleNodeCampaign(t *testing.T) {
	cluster, err := NewInmemElectionCluster(nil, "solo")
	if err != nil {
		t.Fatalf("create solo cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Close() })

	solo := cluster.Get("solo")
	ctx, cancel := context.WithTimeout(context.Background(), electionTimeout)
	defer cancel()
	id, err := solo.Campaign(ctx)
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	if id != "solo" {
		t.Fatalf("campaign observed leader %q, want solo", id)
	}
	if !solo.IsLeader() {
		t.Fatalf("solo node did not become leader")
	}
	if err := solo.Apply(raft.Command{Op: raft.CommandSet, Key: "k", Value: "v"}, applyTimeout); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if v, ok := solo.Node().FSM().Get("k"); !ok || v != "v" {
		t.Fatalf("fsm missing committed value: got %q ok=%v", v, ok)
	}
}

// expectLeadershipAcquire waits for an IsLeader==true event on the election's
// LeadershipChanges channel within timeout.
func expectLeadershipAcquire(e *RaftElection, timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-e.LeadershipChanges():
			if !ok {
				return errors.New("leadership channel closed before acquire event")
			}
			if ev.IsLeader {
				return nil
			}
		case <-deadline:
			return errors.New("timed out waiting for leadership-acquire event")
		}
	}
}

// waitForReplication polls until every node's FSM reflects all want entries.
func waitForReplication(nodes []*RaftElection, want map[string]string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = nil
		for _, n := range nodes {
			for k, v := range want {
				got, ok := n.Node().FSM().Get(k)
				if !ok || got != v {
					lastErr = errNotReplicated(n.ID(), k)
				}
			}
		}
		if lastErr == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return lastErr
}

func errNotReplicated(node, key string) error {
	return errors.New("node " + node + " key " + key + " not yet replicated")
}
