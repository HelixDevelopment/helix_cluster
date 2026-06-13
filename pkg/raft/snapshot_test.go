package raft

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	hraft "github.com/hashicorp/raft"
)

// TestSnapshot_CompactionForcesInstallSnapshotToLateJoiner is the REAL runtime
// realization of specs/RaftSnapshot.tla (HXC-979): hashicorp/raft FSM
// snapshotting + log compaction (truncation past the snapshot point) +
// InstallSnapshot to a node whose log has fallen BEHIND the leader's snapshot.
//
// There is NO bluff here. The proof is built so the ONLY way the late joiner
// could possibly end up holding the earliest committed key is via an
// InstallSnapshot RPC, because the leader has physically TRUNCATED that key out
// of its replicated log. Concretely it proves, with sink-side evidence:
//
//	(a) a 3-node in-mem Raft cluster elects one leader, agreed by all;
//	(b) applying 40 committed keys, with the leader tuned for AGGRESSIVE
//	    compaction (SnapshotThreshold=8, TrailingLogs=4, short SnapshotInterval)
//	    plus a forced raft.Snapshot(), causes the leader to take a real FSM
//	    snapshot AND truncate its log — proven by LogStore.FirstIndex() advancing
//	    from 1 to well past 1, and Stats()["last_snapshot_index"] > 0. The
//	    earliest keys NO LONGER EXIST in any live log entry — only in the snapshot;
//	(c) a FRESH 4th node (empty state, empty log) is AddVoter'd in. Because the
//	    leader's log no longer holds the early entries (FirstIndex advanced past
//	    them), a plain AppendEntries CANNOT bring the joiner current — raft MUST
//	    ship it an InstallSnapshot;
//	(d) the joiner's FSM ends up holding EVERY committed key, INCLUDING key #1
//	    (index below the leader's snapshot point, absent from every live log).
//	    The only transport that could have delivered key #1 is InstallSnapshot —
//	    so this assertion IS the InstallSnapshot proof, not mere log replay.
//
// Maps to the TLA+ model: COMMIT (apply entries) → COMPACT (snapshot + truncate,
// leaderSnap advances, log <= snap discarded) → INSTALL (a follower with
// fLogEnd < leaderSnap adopts the snapshot and materialises the covered state).
// The TLA+ safety property NoFollowerGap / FollowerAgrees is exactly what
// assertion (d) checks at runtime: no committed entry below the snapshot is lost.
func TestSnapshot_CompactionForcesInstallSnapshotToLateJoiner(t *testing.T) {
	logf := func(format string, args ...any) {
		t.Logf("[raft-snapshot-it] "+format, args...)
	}

	const (
		electionTimeout = 15 * time.Second
		applyTimeout    = 10 * time.Second
		replTimeout     = 15 * time.Second
		catchUpTimeout  = 20 * time.Second
		numKeys         = 40
	)

	// Tune EVERY node for aggressive compaction. The crux is the leader, but the
	// in-mem cluster shares one base across all nodes; tuning all is fine and makes
	// the scenario robust regardless of which node is elected leader.
	//
	//   SnapshotThreshold=8  → only 8 uncompacted logs are needed before raft is
	//                          willing to snapshot (default is 8192).
	//   TrailingLogs=4       → after a snapshot, KEEP only 4 trailing log entries;
	//                          everything older is TRUNCATED. This is the compaction.
	//   SnapshotInterval     → check for a needed snapshot frequently.
	base := &hraft.Config{
		SnapshotThreshold: 8,
		TrailingLogs:      4,
		SnapshotInterval:  50 * time.Millisecond,
	}

	// Build the 3-node in-mem cluster MANUALLY (mirroring NewInmemCluster) so the
	// test retains a handle to each node's package-internal LogStore. hashicorp/raft
	// does not expose a node's LogStore after NewRaft, and the leader's LogStore is
	// precisely what proves real truncation (FirstIndex) and that an early key is
	// gone from the live log — so we keep the references here rather than widening
	// the production Node surface.
	ids := []string{"snap-a", "snap-b", "snap-c"}
	cluster, logStoreByID := buildInmemClusterWithLogStores(t, base, ids)
	t.Cleanup(func() { _ = cluster.Shutdown() })

	leader, err := cluster.WaitForLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no leader elected: %v", err)
	}
	if err := waitForAllAgreeLeader(cluster.Nodes(), leader.ID(), electionTimeout); err != nil {
		t.Fatalf("nodes did not agree on a single leader %s: %v", leader.ID(), err)
	}
	logf("SINGLE LEADER ELECTED and agreed by all: %s", leader.ID())

	// (a) Record the leader log's FirstIndex BEFORE applying. A freshly
	// bootstrapped log starts at 1 (the bootstrap configuration entry).
	leaderLog := logStoreByID[leader.ID()]
	if leaderLog == nil {
		t.Fatalf("no retained LogStore for leader %s", leader.ID())
	}
	firstBefore, err := leaderLog.FirstIndex()
	if err != nil {
		t.Fatalf("leader FirstIndex (before): %v", err)
	}
	logf("BEFORE apply: leader %s log FirstIndex=%d", leader.ID(), firstBefore)

	// (b) Apply many committed entries. key #1 ("snap/k/0001") is the EARLIEST —
	// it is the one we will prove the joiner can only have obtained via snapshot.
	want := make(map[string]string, numKeys)
	for i := 1; i <= numKeys; i++ {
		k := fmt.Sprintf("snap/k/%04d", i)
		v := fmt.Sprintf("v-%04d", i)
		want[k] = v
		if err := leader.Apply(Command{Op: CommandSet, Key: k, Value: v}, applyTimeout); err != nil {
			t.Fatalf("leader %s failed to apply %s: %v", leader.ID(), k, err)
		}
	}
	firstKey := fmt.Sprintf("snap/k/%04d", 1)
	firstVal := want[firstKey]
	logf("APPLIED %d committed keys (earliest=%q)", numKeys, firstKey)

	// All current cluster members must hold every key before we go further.
	if err := waitForReplication(cluster.Nodes(), want, replTimeout); err != nil {
		t.Fatalf("replication to all 3 nodes failed: %v", err)
	}
	logf("REPLICATION CONFIRMED on all 3 FSMs (%d keys)", numKeys)

	// (b) FORCE a snapshot now (in addition to the auto-snapshot the low threshold
	// already triggers), then WAIT for the leader's log to be truncated past index
	// 1. raft.Snapshot() captures the FSM and, governed by TrailingLogs=4, truncates
	// the log; FirstIndex advancing is the sink-side evidence of real compaction.
	if f := leader.Raft().Snapshot(); f.Error() != nil {
		// A concurrent auto-snapshot can race a forced one ("nothing new to
		// snapshot"); that's fine — the auto-snapshot still compacts. We only fail
		// if compaction does not actually happen, asserted by the poll below.
		logf("note: forced Snapshot() returned %v (an auto-snapshot likely already ran)", f.Error())
	}

	firstAfter, lastSnapIdx := waitForCompaction(t, leader, leaderLog, firstBefore, catchUpTimeout)
	logf("COMPACTION PROVEN: leader %s log FirstIndex advanced %d -> %d; last_snapshot_index=%d",
		leader.ID(), firstBefore, firstAfter, lastSnapIdx)

	// ANTI-BLUFF TEETH #1: the log must have genuinely truncated. If FirstIndex is
	// still 1 the early entries are still replayable and the test proves nothing
	// about InstallSnapshot — so require real truncation.
	if firstAfter <= 1 {
		t.Fatalf("ANTI-BLUFF FAILED: leader log FirstIndex=%d did not advance past 1 — no compaction happened, InstallSnapshot cannot be proven", firstAfter)
	}
	if lastSnapIdx == 0 {
		t.Fatalf("ANTI-BLUFF FAILED: leader last_snapshot_index=0 — no snapshot was taken")
	}

	// ANTI-BLUFF TEETH #2: prove key #1 is GONE from the leader's live log. We scan
	// every entry the leader still holds (FirstIndex..LastIndex) and assert key #1
	// is NOT among them. After this, the ONLY place key #1 exists on the leader is
	// inside the snapshot — so any node that ends up holding key #1 and was not in
	// the cluster when key #1 was logged MUST have received it via InstallSnapshot.
	if liveHasFirstKey, atIdx := liveLogContainsKey(t, leaderLog, firstKey); liveHasFirstKey {
		t.Fatalf("ANTI-BLUFF FAILED: earliest key %q is STILL in the leader's live log at index %d — it was not compacted away, so a joiner could catch up by plain AppendEntries; InstallSnapshot is not proven", firstKey, atIdx)
	}
	logf("ANTI-BLUFF CONFIRMED: earliest key %q is ABSENT from the leader's live log (FirstIndex=%d) — it exists ONLY in the snapshot now", firstKey, firstAfter)

	// (c) Build a FRESH 4th node: empty FSM, empty in-mem log, NOT bootstrapped.
	// It knows nothing — the leader must bring it current. Because the early log is
	// gone from the leader, AppendEntries alone cannot do it; raft must InstallSnapshot.
	joinerID := "snap-joiner"
	joinerCfg, err := newInmemNode(joinerID, base)
	if err != nil {
		t.Fatalf("build joiner node config: %v", err)
	}
	// Fully connect the joiner's transport with every existing node BOTH ways so
	// the leader can dial it (to push the snapshot) and it can reply.
	for _, n := range cluster.Nodes() {
		joinerCfg.transport.Connect(n.trans.LocalAddr(), n.trans)
		n.trans.Connect(joinerCfg.transport.LocalAddr(), joinerCfg.transport)
	}
	joinerRaft, err := hraft.NewRaft(joinerCfg.conf, joinerCfg.fsm, joinerCfg.logStore, joinerCfg.stable, joinerCfg.snaps, joinerCfg.transport)
	if err != nil {
		t.Fatalf("start joiner raft: %v", err)
	}
	joiner := &Node{id: joinerID, raft: joinerRaft, fsm: joinerCfg.fsm, trans: joinerCfg.transport}
	t.Cleanup(func() { _ = joiner.Shutdown() })

	// Sanity: the joiner starts with an EMPTY FSM (it has caught up to nothing yet).
	if got := joiner.FSM().Len(); got != 0 {
		t.Fatalf("joiner FSM should start empty, has %d keys", got)
	}
	if _, ok := joiner.FSM().Get(firstKey); ok {
		t.Fatalf("joiner FSM should NOT yet hold %q before being added", firstKey)
	}
	logf("JOINER %s built fresh (empty FSM, empty log, not bootstrapped) at %s", joinerID, joinerCfg.addr)

	// (c) Add the joiner as a voter via the leader. This drives raft's replication
	// to it; with the early log compacted away, raft falls back to InstallSnapshot.
	if f := leader.Raft().AddVoter(hraft.ServerID(joinerID), joinerCfg.addr, 0, applyTimeout); f.Error() != nil {
		t.Fatalf("leader failed to AddVoter joiner %s: %v", joinerID, f.Error())
	}
	logf("ADDED joiner %s as voter — raft must now bring it current", joinerID)

	// (d) PROOF: the joiner's FSM must hold EVERY committed key, including key #1
	// which is absent from every live log. Wait for full catch-up.
	if err := waitForReplication([]*Node{joiner}, want, catchUpTimeout); err != nil {
		t.Fatalf("INSTALL-SNAPSHOT FAILED: joiner %s did not catch up to all %d keys: %v", joinerID, numKeys, err)
	}

	// The load-bearing assertion: key #1 (below the snapshot point, compacted out of
	// every live log) is present in the joiner's FSM. The joiner was NOT a cluster
	// member when key #1 was logged, and the entry no longer exists in any log to
	// replay — so it can ONLY have arrived inside an InstallSnapshot. This is the
	// runtime witness of the TLA+ Install action materialising covered state.
	got, ok := joiner.FSM().Get(firstKey)
	if !ok || got != firstVal {
		t.Fatalf("INSTALL-SNAPSHOT PROOF FAILED: joiner %s FSM[%q]=(%q,%v), want (%q,true) — the compacted earliest key did not arrive via snapshot", joinerID, firstKey, got, ok, firstVal)
	}
	logf("INSTALL-SNAPSHOT PROVEN: joiner %s holds compacted-away earliest key %q=%q — it could ONLY have arrived via InstallSnapshot (the entry exists in NO live log)", joinerID, firstKey, got)

	// SINK-SIDE EVIDENCE on the joiner: its Stats report a non-zero
	// last_snapshot_index, i.e. it RESTORED from a received snapshot rather than
	// replaying a full log from index 1.
	joinerSnapIdx := statUint(t, joiner, "last_snapshot_index")
	if joinerSnapIdx == 0 {
		t.Fatalf("INSTALL-SNAPSHOT PROOF FAILED: joiner %s last_snapshot_index=0 — it did not restore from a snapshot", joinerID)
	}
	logf("SINK-SIDE EVIDENCE: joiner %s last_snapshot_index=%d (restored from an installed snapshot)", joinerID, joinerSnapIdx)

	// Final teeth: the joiner holds ALL keys, and its FSM size matches.
	if got := joiner.FSM().Len(); got != numKeys {
		t.Fatalf("joiner FSM has %d keys, want %d", got, numKeys)
	}
	logf("PASS: log compaction truncated the leader's log; a late joiner recovered the full committed state — including the compacted-away earliest key — via InstallSnapshot")
}

// waitForCompaction polls until the leader's log FirstIndex has advanced past
// firstBefore (real truncation) AND its last_snapshot_index is non-zero, or the
// deadline expires. It returns the observed FirstIndex and last_snapshot_index.
// Polling (not a fixed sleep) is what makes the snapshot/compaction outcome
// deterministic in the assertion sense.
func waitForCompaction(t *testing.T, leader *Node, leaderLog hraft.LogStore, firstBefore uint64, timeout time.Duration) (firstAfter, lastSnapIdx uint64) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		fi, err := leaderLog.FirstIndex()
		if err != nil {
			t.Fatalf("leader FirstIndex (poll): %v", err)
		}
		snapIdx := statUint(t, leader, "last_snapshot_index")
		if fi > firstBefore && fi > 1 && snapIdx > 0 {
			return fi, snapIdx
		}
		// Nudge another snapshot in case the first raced an auto-snapshot.
		_ = leader.Raft().Snapshot().Error()
		time.Sleep(50 * time.Millisecond)
	}
	fi, _ := leaderLog.FirstIndex()
	t.Fatalf("compaction did not occur within %s: leader FirstIndex=%d (was %d), last_snapshot_index=%d", timeout, fi, firstBefore, statUint(t, leader, "last_snapshot_index"))
	return 0, 0
}

// liveLogContainsKey scans every entry the log store STILL holds
// (FirstIndex..LastIndex) and reports whether any of them is a CommandSet for
// key. After compaction this is how we prove an early key is GONE from the live
// log (so the only remaining copy is in the snapshot).
func liveLogContainsKey(t *testing.T, store hraft.LogStore, key string) (bool, uint64) {
	t.Helper()
	first, err := store.FirstIndex()
	if err != nil {
		t.Fatalf("FirstIndex: %v", err)
	}
	last, err := store.LastIndex()
	if err != nil {
		t.Fatalf("LastIndex: %v", err)
	}
	if first == 0 || last == 0 {
		return false, 0
	}
	for i := first; i <= last; i++ {
		var lg hraft.Log
		if err := store.GetLog(i, &lg); err != nil {
			// A concurrent snapshot may truncate under us; a now-missing index just
			// means it was compacted — not present in the live log.
			continue
		}
		if lg.Type != hraft.LogCommand {
			continue
		}
		var cmd Command
		if err := json.Unmarshal(lg.Data, &cmd); err != nil {
			continue
		}
		if cmd.Op == CommandSet && cmd.Key == key {
			return true, i
		}
	}
	return false, 0
}

// buildInmemClusterWithLogStores assembles an N-node in-mem Raft cluster exactly
// like NewInmemCluster (same bootstrap + transport interconnect), but ALSO returns
// each node's LogStore keyed by id. The test needs the leader's LogStore to read
// FirstIndex (truncation evidence) and to scan the live log — neither is reachable
// through the public Node API for in-mem nodes. Keeping the construction in the
// test (rather than exposing the store on Node) keeps the production surface minimal.
func buildInmemClusterWithLogStores(t *testing.T, base *hraft.Config, ids []string) (*Cluster, map[string]hraft.LogStore) {
	t.Helper()

	cfgs := make([]*nodeConfig, 0, len(ids))
	servers := make([]hraft.Server, 0, len(ids))
	logStores := make(map[string]hraft.LogStore, len(ids))
	for _, id := range ids {
		nc, err := newInmemNode(id, base)
		if err != nil {
			t.Fatalf("newInmemNode(%s): %v", id, err)
		}
		cfgs = append(cfgs, nc)
		logStores[id] = nc.logStore
		servers = append(servers, hraft.Server{
			Suffrage: hraft.Voter,
			ID:       hraft.ServerID(nc.id),
			Address:  nc.addr,
		})
	}

	// Fully connect every transport to every other (identical to NewInmemCluster).
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
		if err := hraft.BootstrapCluster(nc.conf, nc.logStore, nc.stable, nc.snaps, nc.transport, cloneConfiguration(bootstrapCfg)); err != nil {
			t.Fatalf("bootstrap node %s: %v", nc.id, err)
		}
		r, err := hraft.NewRaft(nc.conf, nc.fsm, nc.logStore, nc.stable, nc.snaps, nc.transport)
		if err != nil {
			t.Fatalf("start node %s: %v", nc.id, err)
		}
		nodes = append(nodes, &Node{id: nc.id, raft: r, fsm: nc.fsm, trans: nc.transport})
	}

	return &Cluster{nodes: nodes}, logStores
}

// statUint reads a numeric value from raft.Stats() by key, returning 0 if absent
// or unparsable. Stats are hashicorp/raft's own sink-side counters.
func statUint(t *testing.T, n *Node, key string) uint64 {
	t.Helper()
	s := n.Raft().Stats()
	raw, ok := s[key]
	if !ok {
		return 0
	}
	var out uint64
	if _, err := fmt.Sscanf(raw, "%d", &out); err != nil {
		return 0
	}
	return out
}
