package raft

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPersistentNode_RecoversCommittedStateFromDisk is the REAL durability test
// for the on-disk (BoltDB) store variant. It proves the storage-durability
// property production needs: a node that commits entries, is shut down, and is
// re-opened from the SAME data directory RECOVERS its committed key/value state
// from disk (via raft log replay/restore into a fresh FSM) WITHOUT a caller
// re-applying those entries.
//
// Flow:
//
//	a. Create a persistent single-node raft Node (BoltStore + FileSnapshotStore)
//	   in a t.TempDir(); wait until leader; Apply several committed entries; read
//	   them back from the FSM.
//	b. SHUTDOWN the node (which CLOSES the BoltDB so files flush/release).
//	c. Create a NEW Node from the SAME tempdir; wait until leader; assert the
//	   previously-committed entries are RECOVERED from disk (readable from the
//	   rebuilt FSM) WITHOUT re-applying them. Assert the BoltDB file exists on
//	   disk with non-zero size.
//	d. Apply a NEW entry post-recovery and confirm it persists across a SECOND
//	   restart too.
func TestPersistentNode_RecoversCommittedStateFromDisk(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	const nodeID = "persistent-1"

	// Entries committed before the first restart. The recovery assertion checks
	// these come back from disk WITHOUT being re-applied by the test.
	seed := map[string]string{
		"alpha":   "1",
		"bravo":   "2",
		"charlie": "3",
	}

	// --- (a) first lifetime: bootstrap, become leader, commit entries ---------
	n1, err := NewPersistentNode(nodeID, dataDir, nil)
	if err != nil {
		t.Fatalf("create persistent node (first lifetime): %v", err)
	}
	if err := n1.WaitForLeader(5 * time.Second); err != nil {
		t.Fatalf("first lifetime did not become leader: %v", err)
	}

	for k, v := range seed {
		if err := n1.Apply(Command{Op: CommandSet, Key: k, Value: v}, 2*time.Second); err != nil {
			t.Fatalf("apply %q=%q: %v", k, v, err)
		}
	}
	// Sink-side read-back from the live FSM proves the entries committed.
	for k, want := range seed {
		if got, ok := n1.FSM().Get(k); !ok || got != want {
			t.Fatalf("first lifetime FSM[%q] = (%q,%v), want (%q,true)", k, got, ok, want)
		}
	}
	appliedBeforeRestart := n1.FSM().AppliedCount()
	if appliedBeforeRestart < uint64(len(seed)) {
		t.Fatalf("first lifetime applied count = %d, want >= %d", appliedBeforeRestart, len(seed))
	}

	// --- (b) shutdown + CLOSE BoltDB so files are flushed/released ------------
	if err := n1.Shutdown(); err != nil {
		t.Fatalf("shutdown first lifetime: %v", err)
	}

	// On-disk-file proof: the bolt DB file exists and is non-empty AFTER close.
	boltPath := filepath.Join(dataDir, raftBoltFile)
	fi, err := os.Stat(boltPath)
	if err != nil {
		t.Fatalf("stat bolt file %q after shutdown: %v", boltPath, err)
	}
	if fi.Size() == 0 {
		t.Fatalf("bolt file %q is empty after shutdown; nothing was persisted to disk", boltPath)
	}
	t.Logf("on-disk proof: %s exists, size=%d bytes after shutdown", boltPath, fi.Size())

	// --- (c) second lifetime: REOPEN same dir, recover from disk --------------
	n2, err := NewPersistentNode(nodeID, dataDir, nil)
	if err != nil {
		t.Fatalf("reopen persistent node (second lifetime): %v", err)
	}
	t.Cleanup(func() { _ = n2.Shutdown() })

	if err := n2.WaitForLeader(5 * time.Second); err != nil {
		t.Fatalf("second lifetime did not become leader: %v", err)
	}

	// The recovered FSM must contain the previously-committed entries — and the
	// test never re-applied them in this lifetime, so they can ONLY have come
	// from disk via raft log replay / snapshot restore.
	for k, want := range seed {
		got, ok := n2.FSM().Get(k)
		if !ok || got != want {
			t.Fatalf("RECOVERY FAILED: FSM[%q] = (%q,%v), want (%q,true) — committed entry was NOT recovered from disk", k, got, ok, want)
		}
	}
	if n := n2.FSM().Len(); n != len(seed) {
		t.Fatalf("recovered FSM has %d keys, want %d", n, len(seed))
	}
	t.Logf("recovered %d committed entries from disk after restart without re-applying them", len(seed))

	// --- (d) apply a NEW entry post-recovery and confirm it also persists -----
	if err := n2.Apply(Command{Op: CommandSet, Key: "delta", Value: "4"}, 2*time.Second); err != nil {
		t.Fatalf("apply post-recovery entry: %v", err)
	}
	if got, ok := n2.FSM().Get("delta"); !ok || got != "4" {
		t.Fatalf("post-recovery FSM[delta] = (%q,%v), want (4,true)", got, ok)
	}
	if err := n2.Shutdown(); err != nil {
		t.Fatalf("shutdown second lifetime: %v", err)
	}

	// Third lifetime confirms the post-recovery entry ALSO survived a restart,
	// alongside the original seed entries.
	n3, err := NewPersistentNode(nodeID, dataDir, nil)
	if err != nil {
		t.Fatalf("reopen persistent node (third lifetime): %v", err)
	}
	t.Cleanup(func() { _ = n3.Shutdown() })
	if err := n3.WaitForLeader(5 * time.Second); err != nil {
		t.Fatalf("third lifetime did not become leader: %v", err)
	}
	want := map[string]string{"alpha": "1", "bravo": "2", "charlie": "3", "delta": "4"}
	for k, w := range want {
		if got, ok := n3.FSM().Get(k); !ok || got != w {
			t.Fatalf("third lifetime FSM[%q] = (%q,%v), want (%q,true)", k, got, ok, w)
		}
	}
}

// TestPersistentNode_FreshDirIsEmpty is the control for the recovery test: a
// BRAND-NEW data dir (no prior state) yields a leader whose FSM is empty. This
// is exactly the state the anti-bluff mutation (point reopen at a fresh dir, or
// swap BoltDB for an in-mem store) would leave the "recovered" node in — so this
// test documents what recovery must AVOID.
func TestPersistentNode_FreshDirIsEmpty(t *testing.T) {
	t.Parallel()

	n, err := NewPersistentNode("fresh-1", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create persistent node: %v", err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	if err := n.WaitForLeader(5 * time.Second); err != nil {
		t.Fatalf("did not become leader: %v", err)
	}
	if got := n.FSM().Len(); got != 0 {
		t.Fatalf("fresh node FSM has %d keys, want 0", got)
	}
}
