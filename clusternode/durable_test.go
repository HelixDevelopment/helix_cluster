package clusternode

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestThreeNodeDurableConsensusRestartRecovery is the decisive end-user proof
// (HelixConstitution CLAUDE-1, NO BLUFF) that the clusternode layer can run on
// DURABLE consensus — the persistent+networked raft from pkg/raft — and survive a
// FULL cluster restart with replicated state intact, exercised exclusively
// through clusternode's OWN agent API (Put/Get/WaitForDurableLeader), never a
// direct pkg/raft call that bypasses the agent layer.
//
// It proves, in order:
//
//  1. BRING-UP: three NodeAgents, each backed by a real persistent+networked raft
//     node (own on-disk BoltDB data dir + own fixed loopback TCP bind), elect a
//     single durable leader over REAL TCP.
//  2. REAL-TCP / ON-DISK witnesses: every agent's durable node binds a distinct
//     non-zero loopback TCP port, and every data dir holds a non-empty raft.db.
//  3. REPLICATED WRITE THROUGH THE AGENT: a value Put through the LEADER agent's
//     API replicates across the raft group and becomes readable via Get on a
//     FOLLOWER agent — cross-node replication through the clusternode layer.
//  4. FULL-RESTART-FROM-DISK SURVIVAL (the anti-bluff core): the WHOLE agent
//     cluster is cleanly Stopped (every raft node Shutdown, every BoltDB flushed
//     and closed), then RE-CREATED from the SAME data dirs + SAME binds. After a
//     fresh durable leader is elected, the previously-written value is STILL
//     readable through the agent Get API. Because everything was shut down between
//     the write and this read, the value can ONLY have come from on-disk BoltDB.
//  5. NEGATIVE CONTROL: a fourth durable agent built on a FRESH EMPTY data dir
//     does NOT magically have the value — proving step 4's survival is real disk
//     recovery, not an ambient/default that any node would report.
//
// Every wait is deadline-polling (no fixed sleeps). The multi-node persistent
// path is heavy; it is run WITHOUT -race here (the pkg/raft unit tests carry the
// -race coverage of the same primitives), per the task's gate guidance.
func TestThreeNodeDurableConsensusRestartRecovery(t *testing.T) {
	logf := func(format string, args ...any) {
		t.Logf("[clusternode-durable-it] "+format, args...)
	}

	const (
		electionTimeout = 30 * time.Second
		applyTimeout    = 10 * time.Second
		replTimeout     = 20 * time.Second
	)

	ids := []string{"dn-a", "dn-b", "dn-c"}
	// One durable directory per agent, persisting across the restart (t.TempDir is
	// cleaned up by the test framework at the very end, AFTER the restart read).
	dirs := []string{t.TempDir(), t.TempDir(), t.TempDir()}

	const (
		key = "cluster/region"
		val = "eu-central-1"
	)

	// ---- Phase 1: build the durable cluster, write through the agent layer ----

	c1, err := NewNodeClusterWithDurableConsensus(ids, dirs)
	if err != nil {
		t.Fatalf("build durable cluster: %v", err)
	}
	// Record the fixed binds so the restart rebinds the SAME addresses (the
	// persisted raft configuration references them).
	binds := c1.DurableBinds()
	if len(binds) != 3 {
		t.Fatalf("expected 3 durable binds, got %d: %v", len(binds), binds)
	}

	// REAL-TCP witness: every agent's durable node has a distinct non-zero
	// loopback TCP port. This is load-bearing: it proves the consensus travels
	// over genuine kernel sockets, not an in-process shim.
	ports := make(map[int]string)
	for _, a := range c1.Agents() {
		addr, err := a.DurableTCPAddr()
		if err != nil {
			t.Fatalf("agent %s DurableTCPAddr: %v", a.ID(), err)
		}
		if addr.Port == 0 {
			t.Fatalf("agent %s durable node bound to port 0 — not a real TCP listener", a.ID())
		}
		if !addr.IP.IsLoopback() {
			t.Fatalf("agent %s durable node bound to non-loopback IP %s", a.ID(), addr.IP)
		}
		if other, dup := ports[addr.Port]; dup {
			t.Fatalf("REAL-TCP proof failed: agents %s and %s share durable port %d", a.ID(), other, addr.Port)
		}
		ports[addr.Port] = a.ID()
	}
	logf("REAL-TCP PROOF: 3 distinct non-zero loopback durable ports: %v", ports)

	// ON-DISK witness: every agent's data dir holds a non-empty raft.db. This is
	// load-bearing: it proves the consensus state is on real durable storage.
	for _, a := range c1.Agents() {
		assertRaftDBOnDisk(t, a.DurableDataDir(), a.ID())
	}
	logf("ON-DISK PROOF (pre-write): all 3 agents have a non-empty raft.db on disk")

	// Single durable leader, observed by all agents through the agent API.
	leader1, err := c1.WaitForDurableLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no durable leader elected over TCP+disk: %v", err)
	}
	logf("DURABLE LEADER ELECTED over TCP+disk: %s", leader1.ID())

	// Replicated WRITE through the LEADER agent's own API. This is load-bearing:
	// the write enters the raft log via NodeAgent.Put (Apply on the leader), not a
	// direct pkg/raft call — proving the replicated-state operation is exposed AT
	// the clusternode layer.
	if err := leader1.Put(key, val, applyTimeout); err != nil {
		t.Fatalf("leader agent %s Put(%s=%s): %v", leader1.ID(), key, val, err)
	}
	logf("WROTE through leader agent API: Put(%s=%s)", key, val)

	// A write issued to a FOLLOWER agent must be rejected as not-leader — proving
	// Put is a real leader-only replicated op, not a local map write.
	var follower1 *NodeAgent
	for _, a := range c1.Agents() {
		if a.ID() != leader1.ID() {
			follower1 = a
			break
		}
	}
	if follower1 == nil {
		t.Fatalf("could not find a follower agent")
	}
	if err := follower1.Put("should/fail", "x", 2*time.Second); err == nil {
		t.Fatalf("ANTI-BLUFF FAILED: Put on FOLLOWER agent %s unexpectedly succeeded — it is not a real leader-only raft write", follower1.ID())
	}
	logf("LEADER-ONLY PROOF: Put on follower agent %s correctly rejected", follower1.ID())

	// Cross-node replication THROUGH THE AGENT: the FOLLOWER agent's Get must
	// observe the leader's write once replication completes. This is load-bearing:
	// it proves the value crossed nodes through the raft group and is readable via
	// the agent layer on a different node than wrote it.
	if err := waitForAgentGet(follower1, key, val, replTimeout); err != nil {
		t.Fatalf("cross-node replication to follower agent %s failed: %v", follower1.ID(), err)
	}
	logf("CROSS-NODE REPLICATION PROVEN: follower agent %s Get(%s)==%q", follower1.ID(), key, val)

	// ---- Phase 2: FULL cluster shutdown ----------------------------------------
	// Stop tears down EVERY agent's durable raft node (Shutdown stops raft, closes
	// the TCP listener, then flushes+closes the BoltDB) and the shared election
	// group. After this nothing about the written value lives in memory anywhere —
	// the only place it survives is each node's on-disk raft.db.
	if err := c1.Stop(); err != nil {
		t.Fatalf("full durable cluster Stop: %v", err)
	}
	logf("FULL CLUSTER SHUTDOWN complete — all raft nodes Shutdown, all BoltDB flushed+closed")

	// On-disk proof persists after shutdown: the raft.db files are still there and
	// non-empty (they outlived the process-equivalent shutdown).
	for i, dir := range dirs {
		assertRaftDBOnDisk(t, dir, ids[i])
	}
	logf("ON-DISK PROOF (post-shutdown): all 3 raft.db files persist non-empty on disk")

	// ---- Phase 3: RE-CREATE from the SAME data dirs + binds; recover from disk --
	// RecreateDurableCluster reopens each data dir at the SAME fixed bind. On
	// reopen no bootstrap occurs: each raft node replays its persisted log/snapshot
	// from BoltDB. The recovered value can ONLY have come from disk.
	c2, err := RecreateDurableCluster(ids, dirs, binds)
	if err != nil {
		t.Fatalf("recreate durable cluster from same data dirs: %v", err)
	}
	t.Cleanup(func() { _ = c2.Stop() })

	leader2, err := c2.WaitForDurableLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no durable leader after restart-from-disk: %v", err)
	}
	logf("DURABLE LEADER RE-ELECTED after restart-from-disk: %s", leader2.ID())

	// THE DECISIVE ASSERTION: the value written BEFORE the full shutdown is STILL
	// readable through the agent Get API on every re-created agent. Because the
	// whole cluster was down between the write and now, this value was recovered
	// from on-disk BoltDB — it could not have come from memory or the network.
	for _, a := range c2.Agents() {
		if err := waitForAgentGet(a, key, val, replTimeout); err != nil {
			t.Fatalf("RESTART-FROM-DISK FAILED: agent %s did not recover %s=%q from disk: %v", a.ID(), key, val, err)
		}
	}
	logf("RESTART-FROM-DISK PROVEN: %s=%q survived a FULL cluster shutdown and is readable through the agent API on all 3 re-created agents", key, val)

	// raft.db still present + non-empty for the re-created agents.
	for _, a := range c2.Agents() {
		assertRaftDBOnDisk(t, a.DurableDataDir(), a.ID())
	}

	// ---- Phase 4: NEGATIVE CONTROL — a fresh-empty-dir agent lacks the value ----
	// Build a standalone durable agent on a brand-new EMPTY data dir. It elects
	// itself (single-node) but its FSM must NOT contain the key: proving the
	// survival above is real disk recovery, not an ambient default any node has.
	freshDir := t.TempDir()
	fresh, err := NewNodeClusterWithDurableConsensus([]string{"dn-fresh"}, []string{freshDir})
	if err != nil {
		t.Fatalf("build fresh-empty-dir durable agent: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Stop() })
	if _, err := fresh.WaitForDurableLeader(electionTimeout); err != nil {
		t.Fatalf("fresh-empty-dir agent did not elect: %v", err)
	}
	if v, ok, _ := fresh.Agents()[0].Get(key); ok {
		t.Fatalf("ANTI-BLUFF FAILED: fresh-empty-dir agent magically has %s=%q (ok=%v) — the survival proof is not real disk recovery", key, v, ok)
	}
	logf("NEGATIVE CONTROL PROVEN: fresh-empty-dir agent does NOT have %s — survival is genuine on-disk recovery", key)

	logf("PASS: clusternode runs on DURABLE persistent+networked raft — replicated write through the agent survived a FULL cluster restart from on-disk BoltDB")
}

// waitForAgentGet polls a NodeAgent's durable Get API until key==want or the
// timeout elapses. Outcome-based (asserts on the replicated/recovered value), not
// a fixed sleep.
func waitForAgentGet(a *NodeAgent, key, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		v, ok, err := a.Get(key)
		if err != nil {
			return err
		}
		if ok && v == want {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	v, ok, _ := a.Get(key)
	return fmt.Errorf("agent %s Get(%s)=(%q,%v), want (%q,true) within %s", a.ID(), key, v, ok, want, timeout)
}

// assertRaftDBOnDisk fails the test unless dataDir/raft.db exists and is
// non-empty — the on-disk durability witness.
func assertRaftDBOnDisk(t *testing.T, dataDir, id string) {
	t.Helper()
	if dataDir == "" {
		t.Fatalf("agent %s has no durable data dir", id)
	}
	dbPath := filepath.Join(dataDir, "raft.db")
	fi, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("agent %s raft.db not on disk at %s: %v", id, dbPath, err)
	}
	if fi.Size() == 0 {
		t.Fatalf("agent %s raft.db at %s is empty (size 0) — no durable state", id, dbPath)
	}
}
