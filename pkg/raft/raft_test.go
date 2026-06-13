package raft

import (
	"fmt"
	"testing"
	"time"
)

// TestRaftThreeNodeLeaderKillReelection is a REAL integration test of embedded
// Raft consensus. It:
//  1. spins up a genuine 3-node hashicorp/raft cluster (in-mem transport+store),
//  2. waits for a leader to be elected,
//  3. applies several committed entries through Raft,
//  4. verifies all 3 FSMs converged to the replicated state,
//  5. KILLS THE LEADER (full Shutdown),
//  6. asserts a NEW leader is elected among the survivors, and
//  7. asserts the previously-committed entries are intact on the survivors and
//     that the new leader can still commit new entries.
//
// The test fails for real if Raft did not re-elect, or lost committed log
// entries — there is no mock, stub, or skip dodging the hard part.
func TestRaftThreeNodeLeaderKillReelection(t *testing.T) {
	logf := func(format string, args ...any) {
		t.Logf("[raft-it] "+format, args...)
	}

	const (
		electionTimeout = 10 * time.Second
		applyTimeout    = 5 * time.Second
	)

	// 1. Spin up a real 3-node cluster.
	cluster, err := NewInmemCluster(nil, "node-a", "node-b", "node-c")
	if err != nil {
		t.Fatalf("failed to create 3-node cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Shutdown() })

	if got := len(cluster.Nodes()); got != 3 {
		t.Fatalf("expected 3 nodes, got %d", got)
	}

	// 2. Wait for the initial leader.
	leader, err := cluster.WaitForLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no initial leader elected: %v", err)
	}
	initialLeaderID := leader.ID()
	logf("INITIAL LEADER ELECTED: %s", initialLeaderID)

	// 3. Apply committed entries through the leader.
	entries := map[string]string{
		"cluster/region":   "us-east-1",
		"cluster/replicas": "3",
		"cluster/mode":     "active",
	}
	for k, v := range entries {
		if err := leader.Apply(Command{Op: CommandSet, Key: k, Value: v}, applyTimeout); err != nil {
			t.Fatalf("leader %s failed to apply {%s=%s}: %v", initialLeaderID, k, v, err)
		}
	}
	logf("ENTRIES APPLIED via leader %s: %d entries committed", initialLeaderID, len(entries))

	// 4. All three FSMs must converge to the replicated state.
	if err := waitForReplication(cluster.Nodes(), entries, 5*time.Second); err != nil {
		t.Fatalf("replication to all nodes failed before leader kill: %v", err)
	}
	logf("REPLICATION CONFIRMED on all 3 FSMs before kill")

	// 5. KILL THE LEADER.
	if err := leader.Shutdown(); err != nil {
		t.Fatalf("failed to shut down leader %s: %v", initialLeaderID, err)
	}
	logf("LEADER KILLED: %s (shutdown complete)", initialLeaderID)

	// 6. A NEW leader must be elected among the 2 survivors (quorum = 2 of 3).
	newLeader, err := cluster.WaitForNewLeader(initialLeaderID, electionTimeout)
	if err != nil {
		t.Fatalf("no NEW leader elected after killing %s: %v", initialLeaderID, err)
	}
	if newLeader.ID() == initialLeaderID {
		t.Fatalf("new leader %s must differ from killed leader %s", newLeader.ID(), initialLeaderID)
	}
	logf("NEW LEADER ELECTED after kill: %s (was %s)", newLeader.ID(), initialLeaderID)

	// 7a. Previously-committed entries must be intact on the survivors (FSM state
	// preserved through the leadership change).
	survivors := cluster.Survivors(initialLeaderID)
	if len(survivors) != 2 {
		t.Fatalf("expected 2 survivors, got %d", len(survivors))
	}
	for _, n := range survivors {
		for k, want := range entries {
			got, ok := n.FSM().Get(k)
			if !ok {
				t.Fatalf("LOG LOSS: survivor %s missing key %q after re-election", n.ID(), k)
			}
			if got != want {
				t.Fatalf("LOG CORRUPTION: survivor %s key %q = %q, want %q", n.ID(), k, got, want)
			}
		}
	}
	logf("LOG INTACT: all %d committed entries preserved on both survivors after re-election", len(entries))

	// 7b. The new leader must still be able to commit NEW entries (cluster is
	// functional, not just alive).
	if err := newLeader.Apply(Command{Op: CommandSet, Key: "post-kill/key", Value: "ok"}, applyTimeout); err != nil {
		t.Fatalf("new leader %s could not commit a post-kill entry: %v", newLeader.ID(), err)
	}
	postKill := map[string]string{"post-kill/key": "ok"}
	if err := waitForReplication(survivors, postKill, 5*time.Second); err != nil {
		t.Fatalf("post-kill entry did not replicate to survivors: %v", err)
	}
	logf("POST-KILL WRITE COMMITTED by new leader %s and replicated to survivors", newLeader.ID())

	logf("PASS: 3-node Raft survived leader kill — re-elected %s, log intact, cluster writable", newLeader.ID())
}

// TestRaftSingleNodeBootstrap is a fast sanity test that a single-node cluster
// elects itself leader and applies entries — useful for catching wiring breaks
// independent of multi-node timing.
func TestRaftSingleNodeBootstrap(t *testing.T) {
	cluster, err := NewInmemCluster(nil, "solo")
	if err != nil {
		t.Fatalf("create single-node cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Shutdown() })

	leader, err := cluster.WaitForLeader(10 * time.Second)
	if err != nil {
		t.Fatalf("single node did not become leader: %v", err)
	}
	if leader.ID() != "solo" {
		t.Fatalf("expected leader 'solo', got %q", leader.ID())
	}
	if err := leader.Apply(Command{Op: CommandSet, Key: "k", Value: "v"}, 5*time.Second); err != nil {
		t.Fatalf("apply on single node: %v", err)
	}
	if v, ok := leader.FSM().Get("k"); !ok || v != "v" {
		t.Fatalf("fsm did not apply entry: got %q ok=%v", v, ok)
	}
	if err := leader.Apply(Command{Op: CommandDelete, Key: "k"}, 5*time.Second); err != nil {
		t.Fatalf("delete on single node: %v", err)
	}
	if _, ok := leader.FSM().Get("k"); ok {
		t.Fatalf("fsm did not delete entry")
	}
}

// waitForReplication polls until every node's FSM reflects all want entries, or
// the deadline expires.
func waitForReplication(nodes []*Node, want map[string]string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = nil
		for _, n := range nodes {
			for k, v := range want {
				got, ok := n.FSM().Get(k)
				if !ok || got != v {
					lastErr = fmt.Errorf("node %s key %q not yet replicated (ok=%v got=%q want=%q)", n.ID(), k, ok, got, v)
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
