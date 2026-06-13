package raft

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	hraft "github.com/hashicorp/raft"
)

// TestPersistentNetwork_ThreeNodeRestartRecoverRejoin is the REAL integration
// test for production gap I5: a Raft cluster whose nodes are EACH backed by BOTH
// a genuine loopback TCP transport (RPCs over real kernel sockets) AND a real
// on-disk BoltStore+FileSnapshotStore (committed state survives a process
// restart). It proves networked + durable TOGETHER:
//
//	(a) bring up a real 3-node cluster over real TCP with real BoltDB stores;
//	    elect exactly ONE leader (all 3 agree); apply committed entries; assert
//	    they replicate to ALL 3 FSMs;
//	(b) pick a FOLLOWER, SHUTDOWN it (closing its bolt file AND its TCP listener),
//	    then RESTART it from its SAME data dir on a NEW TCP listener, re-add it to
//	    the cluster, and assert it RECOVERS its persisted state from disk AND
//	    catches up to the CURRENT log (its FSM holds every committed entry,
//	    including ones committed WHILE it was down);
//	(c) REAL-TCP proof: every node's transport LocalAddr is a *net.TCPAddr with a
//	    distinct non-zero port; on-disk proof: every node's raft.db exists on disk.
//
// There is no mock, stub, or skip: it fails for real if Raft could not
// elect/replicate over TCP, or if the restarted follower did not recover its
// persisted log from disk and rejoin.
func TestPersistentNetwork_ThreeNodeRestartRecoverRejoin(t *testing.T) {
	logf := func(format string, args ...any) {
		t.Logf("[raft-persistnet-it] "+format, args...)
	}

	const (
		// TCP + disk is slower than in-process/in-mem; give generous-but-bounded
		// headroom. Every outcome is reached via deadline polling, never a fixed
		// sleep.
		electionTimeout = 30 * time.Second
		applyTimeout    = 10 * time.Second
		replTimeout     = 20 * time.Second
		restartTimeout  = 30 * time.Second
	)

	ids := []string{"pn-a", "pn-b", "pn-c"}
	// One durable directory per node (t.TempDir gives a fresh dir per call).
	dirs := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	dirByID := map[string]string{ids[0]: dirs[0], ids[1]: dirs[1], ids[2]: dirs[2]}

	// (a) Real 3-node cluster over real TCP transports + real BoltDB stores.
	cluster, err := NewPersistentNetworkCluster(nil, ids, dirs)
	if err != nil {
		t.Fatalf("failed to create persistent+networked 3-node cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Shutdown() })

	if got := len(cluster.Nodes()); got != 3 {
		t.Fatalf("expected 3 nodes, got %d", got)
	}

	// (c-1) REAL-TCP proof BEFORE consensus: distinct non-zero loopback ports.
	ports := make(map[int]string)
	for _, n := range cluster.Nodes() {
		tcpAddr, err := n.LocalTCPAddr()
		if err != nil {
			t.Fatalf("node %s LocalTCPAddr: %v", n.ID(), err)
		}
		var _ *net.TCPAddr = tcpAddr
		if tcpAddr.Port == 0 {
			t.Fatalf("node %s bound to port 0 — not a real ephemeral TCP port", n.ID())
		}
		if !tcpAddr.IP.IsLoopback() {
			t.Fatalf("node %s bound to non-loopback IP %s", n.ID(), tcpAddr.IP)
		}
		if other, dup := ports[tcpAddr.Port]; dup {
			t.Fatalf("REAL-TCP proof failed: nodes %s and %s share port %d", n.ID(), other, tcpAddr.Port)
		}
		ports[tcpAddr.Port] = n.ID()
	}
	if len(ports) != 3 {
		t.Fatalf("expected 3 distinct TCP ports, got %d: %v", len(ports), ports)
	}
	logf("REAL-TCP PROOF: 3 distinct non-zero loopback TCP ports: %v", ports)

	// (c-2) On-disk proof: every node's raft.db exists with non-zero size.
	for _, n := range cluster.Nodes() {
		assertBoltOnDisk(t, n.DataDir(), n.ID())
	}
	logf("ON-DISK PROOF: all 3 nodes have a non-empty raft.db on disk")

	// (a) Single leader, agreed by all.
	leader, err := cluster.WaitForLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no initial leader elected over TCP+disk: %v", err)
	}
	initialLeaderID := leader.ID()
	if err := waitForAllAgreeLeader(cluster.Nodes(), initialLeaderID, 10*time.Second); err != nil {
		t.Fatalf("nodes did not agree on a single leader %s: %v", initialLeaderID, err)
	}
	logf("SINGLE LEADER ELECTED over TCP+disk and agreed by all: %s", initialLeaderID)

	// (a) Apply committed entries; assert replication to all 3 FSMs.
	entries := map[string]string{
		"pn/region":   "eu-central-1",
		"pn/replicas": "3",
		"pn/mode":     "durable",
	}
	for k, v := range entries {
		if err := leader.Apply(Command{Op: CommandSet, Key: k, Value: v}, applyTimeout); err != nil {
			t.Fatalf("leader %s failed to apply {%s=%s}: %v", initialLeaderID, k, v, err)
		}
	}
	if err := waitForReplication(cluster.Nodes(), entries, replTimeout); err != nil {
		t.Fatalf("replication to all 3 nodes over TCP+disk failed: %v", err)
	}
	logf("REPLICATION CONFIRMED on all 3 durable FSMs over TCP")

	// (b) Pick a FOLLOWER to restart (any node that is not the leader).
	var follower *Node
	for _, n := range cluster.Nodes() {
		if n.ID() != initialLeaderID {
			follower = n
			break
		}
	}
	if follower == nil {
		t.Fatalf("could not find a follower to restart")
	}
	followerID := follower.ID()
	followerDir := dirByID[followerID]
	if followerDir != follower.DataDir() {
		t.Fatalf("follower %s data dir mismatch: map=%q node=%q", followerID, followerDir, follower.DataDir())
	}
	logf("FOLLOWER chosen for restart: %s (dataDir=%s)", followerID, followerDir)

	// (b) SHUTDOWN the follower (closes its bolt file AND its TCP listener) and
	// REMOVE it from the cluster configuration via the leader, so the leader will
	// re-add it as a fresh voter at its NEW address on restart.
	if err := follower.Shutdown(); err != nil {
		t.Fatalf("shutdown follower %s: %v", followerID, err)
	}
	if f := leader.Raft().RemoveServer(hraft.ServerID(followerID), 0, applyTimeout); f.Error() != nil {
		t.Fatalf("leader failed to remove follower %s from config: %v", followerID, f.Error())
	}
	logf("FOLLOWER %s SHUT DOWN (bolt+TCP closed) and removed from cluster config", followerID)

	// (b) Commit MORE entries WHILE the follower is down, so recovery must include
	// catching up to log entries it never saw before its restart.
	downEntries := map[string]string{
		"pn/while-down/x": "100",
		"pn/while-down/y": "200",
	}
	for k, v := range downEntries {
		if err := leader.Apply(Command{Op: CommandSet, Key: k, Value: v}, applyTimeout); err != nil {
			t.Fatalf("leader %s failed to apply while-down entry {%s=%s}: %v", initialLeaderID, k, v, err)
		}
	}
	logf("COMMITTED %d MORE entries while %s was down", len(downEntries), followerID)

	// (b) RESTART the follower from its SAME data dir on a NEW TCP listener. Pass
	// nil servers: on reopen the durable on-disk configuration is used and no
	// re-bootstrap occurs — raft replays the persisted log into a fresh FSM.
	restarted, err := NewPersistentNetworkNode(followerID, followerDir, nil, nil)
	if err != nil {
		t.Fatalf("restart follower %s from same data dir: %v", followerID, err)
	}
	t.Cleanup(func() { _ = restarted.Node.Shutdown() })
	logf("FOLLOWER %s RESTARTED from same data dir on NEW TCP addr %s", followerID, restarted.TCPAddr.String())

	// The restarted node binds a DIFFERENT port than before (old listener closed).
	if _, dup := ports[restarted.TCPAddr.Port]; dup {
		// Extremely unlikely (the OS reused the freed ephemeral port); not an error
		// per se, just note it. The rejoin proof below is what matters.
		logf("note: restarted %s reused a prior ephemeral port %d", followerID, restarted.TCPAddr.Port)
	}

	// (b) DISK-RECOVERY ISOLATION (the anti-bluff anchor): BEFORE re-adding the
	// node to the cluster, prove the pre-restart entries SURVIVED ON DISK in this
	// fresh process. The leader has not replicated anything to this process yet (it
	// is not in the configuration; its prior address was removed), so the ONLY
	// possible source for these log entries is the reopened BoltStore.
	//
	// We assert on the PERSISTED LOG rather than the FSM because hashicorp/raft
	// does not persist the commit index: a standalone follower cannot reach quorum,
	// so it will not advance its commit index and will not yet apply its log to the
	// FSM (that happens after rejoin, when the leader re-establishes the commit
	// index). But the durable LOG itself is recovered from disk immediately — its
	// last index must be >= the number of entries committed before restart.
	//
	// This is what the anti-bluff mutation targets: restart from a FRESH empty dir
	// and this assertion FAILS (the empty store's LastIndex is the bootstrap-only
	// baseline, with none of the pre-restart user entries).
	logStore := restarted.LogStore()
	recoveredLast, lerr := logStore.LastIndex()
	if lerr != nil {
		t.Fatalf("read recovered LastIndex for %s: %v", followerID, lerr)
	}
	persistedSeed := persistedUserEntries(t, logStore)
	for k, v := range entries {
		got, ok := persistedSeed[k]
		if !ok || got != v {
			t.Fatalf("DISK-RECOVERY FAILED: pre-restart entry %q=%q NOT found in %s's persisted on-disk log after restart (got %q ok=%v) — it can ONLY have come from disk at this point", k, v, followerID, got, ok)
		}
	}
	logf("DISK-RECOVERY PROVEN: %s's persisted log on disk (LastIndex=%d) contains all %d pre-restart entries BEFORE any rejoin/network catch-up", followerID, recoveredLast, len(entries))

	// (b) RE-ADD the restarted follower to the cluster at its NEW address via the
	// leader. After this it must catch up to the CURRENT log over TCP.
	addr := hraft.ServerAddress(restarted.TCPAddr.String())
	if f := leader.Raft().AddVoter(hraft.ServerID(followerID), addr, 0, applyTimeout); f.Error() != nil {
		t.Fatalf("leader failed to re-add restarted follower %s at %s: %v", followerID, addr, f.Error())
	}
	logf("RE-ADDED restarted follower %s to cluster at %s", followerID, addr)

	// (b) PROOF: the restarted follower's FSM must hold EVERY committed entry —
	// the pre-restart seed (recovered FROM DISK, since this lifetime never
	// re-applied it) AND the while-down entries (caught up over TCP after rejoin).
	want := make(map[string]string, len(entries)+len(downEntries))
	for k, v := range entries {
		want[k] = v
	}
	for k, v := range downEntries {
		want[k] = v
	}
	if err := waitForReplication([]*Node{restarted.Node}, want, restartTimeout); err != nil {
		t.Fatalf("RECOVER+REJOIN FAILED: restarted follower %s did not recover persisted state AND catch up: %v", followerID, err)
	}
	logf("RECOVER+REJOIN PROVEN: %s recovered %d pre-restart entries from disk AND caught up to %d while-down entries (%d total)",
		followerID, len(entries), len(downEntries), len(want))

	// (c-2 again) On-disk proof persists for the restarted node too.
	assertBoltOnDisk(t, restarted.Node.DataDir(), followerID)

	// Cluster is still writable end-to-end with the rejoined node back in quorum.
	if err := leader.Apply(Command{Op: CommandSet, Key: "pn/post-rejoin", Value: "ok"}, applyTimeout); err != nil {
		t.Fatalf("leader could not commit a post-rejoin entry: %v", err)
	}
	if err := waitForReplication([]*Node{restarted.Node}, map[string]string{"pn/post-rejoin": "ok"}, replTimeout); err != nil {
		t.Fatalf("post-rejoin entry did not replicate to restarted follower: %v", err)
	}
	logf("PASS: persistent+networked 3-node cluster — follower restarted from disk over real TCP, recovered persisted state, caught up, and rejoined quorum")
}

// TestPersistentNetwork_SingleNodeBootstrap is a fast sanity test that a single
// persistent+networked node elects itself over a real TCP listener, persists to
// disk, and recovers its committed entries after a restart from the same dir.
func TestPersistentNetwork_SingleNodeBootstrap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const id = "pn-solo"

	pn1, err := NewPersistentNetworkNode(id, dir, nil, nil)
	if err != nil {
		t.Fatalf("create single persistent+networked node: %v", err)
	}
	if pn1.TCPAddr.Port == 0 {
		t.Fatalf("solo node bound to port 0 — not a real TCP listener")
	}
	if err := pn1.Node.WaitForLeader(15 * time.Second); err != nil {
		t.Fatalf("single node did not become leader: %v", err)
	}
	if err := pn1.Node.Apply(Command{Op: CommandSet, Key: "k", Value: "v"}, 5*time.Second); err != nil {
		t.Fatalf("apply on single persistent+networked node: %v", err)
	}
	assertBoltOnDisk(t, dir, id)
	if err := pn1.Node.Shutdown(); err != nil {
		t.Fatalf("shutdown solo node: %v", err)
	}

	// Reopen from the same dir: must recover the committed entry from disk.
	pn2, err := NewPersistentNetworkNode(id, dir, nil, nil)
	if err != nil {
		t.Fatalf("reopen single persistent+networked node: %v", err)
	}
	t.Cleanup(func() { _ = pn2.Node.Shutdown() })
	if err := pn2.Node.WaitForLeader(15 * time.Second); err != nil {
		t.Fatalf("reopened node did not become leader: %v", err)
	}
	if v, ok := pn2.Node.FSM().Get("k"); !ok || v != "v" {
		t.Fatalf("RECOVERY FAILED: reopened FSM[k]=(%q,%v), want (v,true)", v, ok)
	}
}

// persistedUserEntries scans a recovered raft LogStore end-to-end and decodes
// every CommandSet log entry into a key->value map. It reads ONLY what is durably
// on disk (the bolt log), so it proves which committed user entries survived a
// restart independent of any FSM apply / network catch-up. Non-command log
// entries (configuration/no-op) are skipped.
func persistedUserEntries(t *testing.T, store hraft.LogStore) map[string]string {
	t.Helper()
	out := make(map[string]string)
	first, err := store.FirstIndex()
	if err != nil {
		t.Fatalf("log store FirstIndex: %v", err)
	}
	last, err := store.LastIndex()
	if err != nil {
		t.Fatalf("log store LastIndex: %v", err)
	}
	if first == 0 || last == 0 {
		return out
	}
	for i := first; i <= last; i++ {
		var lg hraft.Log
		if err := store.GetLog(i, &lg); err != nil {
			t.Fatalf("log store GetLog(%d): %v", i, err)
		}
		if lg.Type != hraft.LogCommand {
			continue
		}
		var cmd Command
		if err := json.Unmarshal(lg.Data, &cmd); err != nil {
			// Not a KV command entry; skip.
			continue
		}
		switch cmd.Op {
		case CommandSet:
			out[cmd.Key] = cmd.Value
		case CommandDelete:
			delete(out, cmd.Key)
		}
	}
	return out
}

// assertBoltOnDisk fails the test unless dataDir/raft.db exists with non-zero size.
func assertBoltOnDisk(t *testing.T, dataDir, id string) {
	t.Helper()
	if dataDir == "" {
		t.Fatalf("node %s reported empty dataDir — not a persistent node", id)
	}
	boltPath := filepath.Join(dataDir, raftBoltFile)
	fi, err := os.Stat(boltPath)
	if err != nil {
		t.Fatalf("on-disk proof failed: stat %q for node %s: %v", boltPath, id, err)
	}
	if fi.Size() == 0 {
		t.Fatalf("on-disk proof failed: %q for node %s is empty", boltPath, id)
	}
}
