package main

import (
	"errors"
	"testing"
	"time"
)

// testIDs is the canonical 3-node set used by the integration tests.
var testIDs = []string{"kv-1", "kv-2", "kv-3"}

const (
	electTimeout = 5 * time.Second
	replTimeout  = 5 * time.Second
)

// newTestService brings up a 3-node replicated KV service and registers cleanup.
func newTestService(t *testing.T) *KVService {
	t.Helper()
	svc, err := NewKVService(electTimeout, testIDs...)
	if err != nil {
		t.Fatalf("NewKVService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// TestReplicationAcrossAllNodes proves the core building-block composition:
// Put via the leader, then assert every key is readable with the correct value
// from ALL THREE nodes' replicated FSMs. A Get reads the per-node FSM, so this is
// genuine replication evidence — not a shared map.
func TestReplicationAcrossAllNodes(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	leader := svc.LeaderID()
	if leader == "" {
		t.Fatal("expected a leader to be elected")
	}

	kv := map[string]string{
		"alpha":     "1",
		"beta":      "two",
		"gamma/sub": "3.0",
	}
	for k, v := range kv {
		if err := svc.Put(k, v); err != nil {
			t.Fatalf("Put(%q,%q): %v", k, v, err)
		}
	}

	// Sink-side assertion: every key must be readable with the correct value on
	// every node's own FSM. WaitReplicated polls the real sink state.
	for k, v := range kv {
		if err := svc.WaitReplicated(k, v, testIDs, replTimeout); err != nil {
			t.Fatalf("replication of %q: %v", k, err)
		}
		for _, id := range testIDs {
			got, ok, err := svc.Get(id, k)
			if err != nil {
				t.Fatalf("Get(%s,%q): %v", id, k, err)
			}
			if !ok || got != v {
				t.Fatalf("node %s: key %q = %q (ok=%v), want %q on all nodes", id, k, got, ok, v)
			}
		}
	}
}

// TestFollowerWriteRejected proves that an un-routed write proposed directly on a
// FOLLOWER is rejected with ErrNotLeader (leader-gated write path). The service's
// routed Put forwards to the leader; PutVia(follower,…) is the raw path the test
// uses to observe the rejection. We state explicitly: Put() forwards to the
// leader; PutVia(follower) does NOT and is therefore rejected.
func TestFollowerWriteRejected(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	leader := svc.LeaderID()
	if leader == "" {
		t.Fatal("expected a leader to be elected")
	}

	// Find a follower.
	var follower string
	for _, id := range svc.NodeIDs() {
		if id != leader {
			follower = id
			break
		}
	}
	if follower == "" {
		t.Fatal("expected at least one follower in a 3-node cluster")
	}

	err := svc.PutVia(follower, "k", "v")
	if err == nil {
		t.Fatalf("expected follower write to %s to be rejected, got nil", follower)
	}
	if !errors.Is(err, ErrNotLeader) {
		t.Fatalf("expected ErrNotLeader from follower write, got: %v", err)
	}

	// And the rejected key must NOT appear on any node's FSM.
	for _, id := range svc.NodeIDs() {
		if _, ok, _ := svc.Get(id, "k"); ok {
			t.Fatalf("rejected key should not be present on node %s", id)
		}
	}
}

// TestLeaderFailover proves real failover: pre-failover keys survive on the
// remaining nodes, a NEW leader is elected after the old one is killed, and a key
// Put via the new leader replicates to the survivors. This is leader election +
// log replication + durable commit composed into a usable service.
func TestLeaderFailover(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	original := svc.LeaderID()
	if original == "" {
		t.Fatal("expected a leader to be elected")
	}

	// Write keys that must survive the failover.
	pre := map[string]string{
		"durable/a": "A",
		"durable/b": "B",
	}
	for k, v := range pre {
		if err := svc.Put(k, v); err != nil {
			t.Fatalf("pre-failover Put(%q): %v", k, err)
		}
	}
	if err := svc.WaitReplicated("durable/a", "A", testIDs, replTimeout); err != nil {
		t.Fatalf("pre-failover replication: %v", err)
	}

	// Kill the leader; a new leader must be elected by the survivors.
	killed, err := svc.KillLeader()
	if err != nil {
		t.Fatalf("KillLeader: %v", err)
	}
	if killed != original {
		t.Fatalf("killed %q but original leader was %q", killed, original)
	}

	newLeader, err := svc.WaitNewLeader(killed, electTimeout)
	if err != nil {
		t.Fatalf("WaitNewLeader: %v", err)
	}
	if newLeader == killed || newLeader == "" {
		t.Fatalf("expected a new distinct leader, got %q (killed %q)", newLeader, killed)
	}

	survivors := svc.SurvivorIDs(killed)
	if len(survivors) != 2 {
		t.Fatalf("expected 2 survivors, got %d (%v)", len(survivors), survivors)
	}

	// Pre-failover keys must still be readable on every survivor.
	for k, v := range pre {
		if err := svc.WaitReplicated(k, v, survivors, replTimeout); err != nil {
			t.Fatalf("post-failover durability of %q: %v", k, err)
		}
		for _, id := range survivors {
			got, ok, err := svc.Get(id, k)
			if err != nil {
				t.Fatalf("Get(%s,%q): %v", id, k, err)
			}
			if !ok || got != v {
				t.Fatalf("survivor %s lost durable key %q: got %q (ok=%v), want %q", id, k, got, ok, v)
			}
		}
	}

	// A new write via the NEW leader must replicate to the survivors.
	if err := svc.Put("post/key", "post-value"); err != nil {
		t.Fatalf("post-failover Put: %v", err)
	}
	if err := svc.WaitReplicated("post/key", "post-value", survivors, replTimeout); err != nil {
		t.Fatalf("post-failover replication: %v", err)
	}
	for _, id := range survivors {
		got, ok, err := svc.Get(id, "post/key")
		if err != nil {
			t.Fatalf("Get(%s,post/key): %v", id, err)
		}
		if !ok || got != "post-value" {
			t.Fatalf("survivor %s missing post-failover key: got %q (ok=%v)", id, got, ok)
		}
	}
}

// TestRoutedPutAlwaysFindsLeader documents the forwarding behaviour: the routed
// Put() succeeds regardless of which node currently leads, because it resolves
// the leader internally. (We do NOT implement follower->leader RPC forwarding;
// Put() routes by selecting the leader node. PutVia is the un-routed path.)
func TestRoutedPutAlwaysFindsLeader(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	if err := svc.Put("routed", "ok"); err != nil {
		t.Fatalf("routed Put should succeed via the leader: %v", err)
	}
	if err := svc.WaitReplicated("routed", "ok", testIDs, replTimeout); err != nil {
		t.Fatalf("routed Put replication: %v", err)
	}
}
