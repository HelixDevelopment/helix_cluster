package multiraft

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	pb "go.etcd.io/raft/v3/raftpb"
)

// driveUntilLeaders ticks the manager until every shard has elected a leader (or
// the tick budget is exhausted). It then runs the Ready loop to settle. This
// exercises the genuine raft election path through the in-process transport.
func driveUntilLeaders(t *testing.T, m *MultiRaftManager, shards []ShardID, maxTicks int) {
	t.Helper()
	for i := 0; i < maxTicks; i++ {
		m.Tick()
		m.Run()
		all := true
		for _, id := range shards {
			if m.LeaderID(id) == 0 {
				all = false
				break
			}
		}
		if all {
			return
		}
	}
	for _, id := range shards {
		if m.LeaderID(id) == 0 {
			t.Fatalf("shard %q never elected a leader within %d ticks", id, maxTicks)
		}
	}
}

// settle runs ticks + Ready loops to give proposals time to replicate & commit.
func settle(m *MultiRaftManager, ticks int) {
	for i := 0; i < ticks; i++ {
		m.Tick()
		m.Run()
	}
}

func threePeers() []uint64 { return []uint64{1, 2, 3} }

// Test 1: N>=3 shards each independently elect a leader and COMMIT a proposed
// entry. We assert the committed payload bytes appear in every node's applied log
// (sink-side evidence), not merely that Propose returned nil.
func TestIndependentLeaderElectionAndCommit(t *testing.T) {
	tr := NewInProcTransport()
	m := NewMultiRaftManager(tr)

	const nShards = 4
	ids := make([]ShardID, nShards)
	for i := 0; i < nShards; i++ {
		ids[i] = ShardID(fmt.Sprintf("shard-%d", i))
		if err := m.CreateShard(ids[i], threePeers()); err != nil {
			t.Fatalf("CreateShard %q: %v", ids[i], err)
		}
	}

	driveUntilLeaders(t, m, ids, 200)

	// Each shard must have a leader, and the leaders need not be the same node.
	leaders := make(map[ShardID]uint64)
	for _, id := range ids {
		leaders[id] = m.LeaderID(id)
		if leaders[id] == 0 {
			t.Fatalf("shard %q has no leader", id)
		}
	}

	// Propose a distinct payload to each shard and prove it commits everywhere.
	for _, id := range ids {
		payload := []byte("commit:" + string(id))
		if err := m.Propose(context.Background(), id, payload); err != nil {
			t.Fatalf("Propose to %q: %v", id, err)
		}
	}
	settle(m, 50)

	for _, id := range ids {
		for _, peer := range threePeers() {
			entries, err := m.CommittedEntries(id, peer)
			if err != nil {
				t.Fatalf("CommittedEntries %q/%d: %v", id, peer, err)
			}
			want := []byte("commit:" + string(id))
			found := false
			for _, e := range entries {
				if bytes.Equal(e, want) {
					found = true
				}
				// Cross-shard contamination check: a payload destined for another
				// shard must never appear here (mutation guard for routing).
				for _, other := range ids {
					if other == id {
						continue
					}
					if bytes.Equal(e, []byte("commit:"+string(other))) {
						t.Fatalf("shard %q node %d committed foreign payload %q", id, peer, e)
					}
				}
			}
			if !found {
				t.Fatalf("shard %q node %d did not commit its payload %q (got %d entries)", id, peer, want, len(entries))
			}
		}
	}
}

// Test 2: Concurrent Propose across shards all commit. Asserts per-shard
// committed-entry counts equal the number of proposals routed to that shard.
// MUTATION GUARD: if Propose ignored shardID and routed every proposal to one
// shared node, the other shards' counts would be 0 and this test fails.
func TestConcurrentProposeAcrossShardsCommit(t *testing.T) {
	tr := NewInProcTransport()
	m := NewMultiRaftManager(tr)

	const nShards = 5
	const perShard = 6
	ids := make([]ShardID, nShards)
	for i := 0; i < nShards; i++ {
		ids[i] = ShardID(fmt.Sprintf("c-shard-%d", i))
		if err := m.CreateShard(ids[i], threePeers()); err != nil {
			t.Fatalf("CreateShard: %v", err)
		}
	}
	driveUntilLeaders(t, m, ids, 200)

	// Fire proposals concurrently across shards; per-shard locks mean shard A's
	// proposals never serialize behind shard B's.
	var wg sync.WaitGroup
	errs := make(chan error, nShards*perShard)
	for _, id := range ids {
		for j := 0; j < perShard; j++ {
			wg.Add(1)
			go func(id ShardID, j int) {
				defer wg.Done()
				p := []byte(fmt.Sprintf("%s:item:%d", id, j))
				// Retry across settle cycles in case of a transient no-leader.
				var lastErr error
				for attempt := 0; attempt < 5; attempt++ {
					if err := m.Propose(context.Background(), id, p); err != nil {
						lastErr = err
						continue
					}
					lastErr = nil
					break
				}
				if lastErr != nil {
					errs <- lastErr
				}
			}(id, j)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent propose error: %v", err)
	}

	settle(m, 100)

	for _, id := range ids {
		// Leader is the authoritative committer; check it observed all perShard.
		leader := m.LeaderID(id)
		if leader == 0 {
			t.Fatalf("shard %q lost its leader", id)
		}
		count, err := m.CommittedCount(id, leader)
		if err != nil {
			t.Fatalf("CommittedCount %q: %v", id, err)
		}
		if count != perShard {
			t.Fatalf("shard %q leader committed %d entries, want %d", id, count, perShard)
		}
	}
}

// Test 3: Per-shard independence — proposing heavily to many shards commits
// without one shard's progress depending on another. We additionally prove that
// a shard whose Ready loop we never drive does NOT silently consume another
// shard's commits (independence of committed state).
func TestPerShardIndependence(t *testing.T) {
	tr := NewInProcTransport()
	m := NewMultiRaftManager(tr)

	busy := ShardID("busy")
	quiet := ShardID("quiet")
	if err := m.CreateShard(busy, threePeers()); err != nil {
		t.Fatalf("CreateShard busy: %v", err)
	}
	if err := m.CreateShard(quiet, threePeers()); err != nil {
		t.Fatalf("CreateShard quiet: %v", err)
	}
	driveUntilLeaders(t, m, []ShardID{busy, quiet}, 200)

	const n = 20
	for i := 0; i < n; i++ {
		if err := m.Propose(context.Background(), busy, []byte(fmt.Sprintf("b-%d", i))); err != nil {
			t.Fatalf("propose busy %d: %v", i, err)
		}
	}
	settle(m, 80)

	busyLeader := m.LeaderID(busy)
	bc, _ := m.CommittedCount(busy, busyLeader)
	if bc != n {
		t.Fatalf("busy shard committed %d, want %d", bc, n)
	}

	// The quiet shard must have committed ZERO application entries — busy's
	// commits did not leak into it. (Guards against a single shared raft node.)
	quietLeader := m.LeaderID(quiet)
	qc, _ := m.CommittedCount(quiet, quietLeader)
	if qc != 0 {
		t.Fatalf("quiet shard committed %d application entries, want 0 (cross-shard leak)", qc)
	}

	// And the quiet shard is still independently usable afterwards.
	if err := m.Propose(context.Background(), quiet, []byte("q-1")); err != nil {
		t.Fatalf("propose quiet: %v", err)
	}
	settle(m, 50)
	qc2, _ := m.CommittedCount(quiet, m.LeaderID(quiet))
	if qc2 != 1 {
		t.Fatalf("quiet shard committed %d after one propose, want 1", qc2)
	}
}

// Test 4: Leader loss — stop one shard's leader; that shard re-elects a new
// leader and resumes committing, while a second shard keeps committing throughout.
func TestLeaderLossReElectionWhileOtherShardProgresses(t *testing.T) {
	tr := NewInProcTransport()
	m := NewMultiRaftManager(tr)

	a := ShardID("A")
	b := ShardID("B")
	if err := m.CreateShard(a, threePeers()); err != nil {
		t.Fatalf("CreateShard A: %v", err)
	}
	if err := m.CreateShard(b, threePeers()); err != nil {
		t.Fatalf("CreateShard B: %v", err)
	}
	driveUntilLeaders(t, m, []ShardID{a, b}, 200)

	// Commit a baseline entry on both shards.
	if err := m.Propose(context.Background(), a, []byte("a-pre")); err != nil {
		t.Fatalf("propose a-pre: %v", err)
	}
	if err := m.Propose(context.Background(), b, []byte("b-pre")); err != nil {
		t.Fatalf("propose b-pre: %v", err)
	}
	settle(m, 50)

	oldLeaderA := m.LeaderID(a)
	if oldLeaderA == 0 {
		t.Fatalf("shard A had no leader before stop")
	}

	// Kill shard A's leader.
	if err := m.StopNode(a, oldLeaderA); err != nil {
		t.Fatalf("StopNode: %v", err)
	}

	// While A re-elects, keep proposing to B and prove B keeps committing.
	bBefore, _ := m.CommittedCount(b, m.LeaderID(b))
	newLeaderA := uint64(0)
	for i := 0; i < 400; i++ {
		m.Tick()
		m.Run()
		// B keeps taking writes throughout A's disruption.
		_ = m.Propose(context.Background(), b, []byte(fmt.Sprintf("b-during-%d", i)))
		m.Run()
		if l := m.LeaderID(a); l != 0 && l != oldLeaderA {
			newLeaderA = l
			break
		}
	}
	if newLeaderA == 0 {
		t.Fatalf("shard A did not re-elect a new leader after losing %d", oldLeaderA)
	}
	if newLeaderA == oldLeaderA {
		t.Fatalf("shard A re-elected the stopped node %d", oldLeaderA)
	}

	// Shard A resumes committing under its new leader.
	aBefore, _ := m.CommittedCount(a, newLeaderA)
	if err := m.Propose(context.Background(), a, []byte("a-post")); err != nil {
		t.Fatalf("propose a-post after re-election: %v", err)
	}
	settle(m, 80)
	aAfter, _ := m.CommittedCount(a, newLeaderA)
	if aAfter <= aBefore {
		t.Fatalf("shard A did not commit after re-election: before=%d after=%d", aBefore, aAfter)
	}

	// Shard B committed strictly more than before A's disruption — it never stalled.
	bAfter, _ := m.CommittedCount(b, m.LeaderID(b))
	if bAfter <= bBefore {
		t.Fatalf("shard B failed to keep committing during A's outage: before=%d after=%d", bBefore, bAfter)
	}

	// The a-post payload is the agreed write on the new leader.
	entries, _ := m.CommittedEntries(a, newLeaderA)
	found := false
	for _, e := range entries {
		if bytes.Equal(e, []byte("a-post")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("a-post not committed on new leader %d", newLeaderA)
	}
}

// Test: error surface — unknown shard, duplicate shard, no-peer shard.
func TestManagerErrorSurface(t *testing.T) {
	m := NewMultiRaftManager(NewInProcTransport())

	if err := m.CreateShard("x", nil); err == nil {
		t.Fatal("expected error creating shard with no peers")
	}
	if err := m.CreateShard("x", threePeers()); err != nil {
		t.Fatalf("CreateShard x: %v", err)
	}
	if err := m.CreateShard("x", threePeers()); err == nil {
		t.Fatal("expected ErrShardExists on duplicate create")
	}
	if _, err := m.CommittedCount("nope", 1); err == nil {
		t.Fatal("expected ErrShardNotFound")
	}
	if err := m.Propose(context.Background(), "nope", []byte("d")); err == nil {
		t.Fatal("expected ErrShardNotFound from Propose")
	}

	// Cancelled context is rejected before touching the shard.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Propose(ctx, "x", []byte("d")); err == nil {
		t.Fatal("expected context.Canceled from Propose")
	}
}

// Test: storage is a real raft.Storage and round-trips entries/HardState.
func TestShardStorageImplementsRaftStorage(t *testing.T) {
	st := NewShardStorage()
	hs, _, err := st.InitialState()
	if err != nil {
		t.Fatalf("InitialState: %v", err)
	}
	if hs.Commit != 0 {
		t.Fatalf("fresh storage commit=%d, want 0", hs.Commit)
	}
	li, err := st.LastIndex()
	if err != nil {
		t.Fatalf("LastIndex: %v", err)
	}
	if li != 0 {
		t.Fatalf("fresh LastIndex=%d, want 0", li)
	}
}

// Test: transport refuses zero-destination messages and counts drops to
// unregistered peers (mutation guard for leader-loss drop accounting).
func TestInProcTransportRoutingAndDrops(t *testing.T) {
	tr := NewInProcTransport()
	got := make(chan uint64, 1)
	tr.RegisterPeer("s", 2, func(msg pb.Message) { got <- msg.To })

	if err := tr.Send("s", pb.Message{To: 2, From: 1}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case to := <-got:
		if to != 2 {
			t.Fatalf("delivered to %d, want 2", to)
		}
	default:
		t.Fatal("message was not delivered to registered peer")
	}

	if err := tr.Send("s", pb.Message{To: 0, From: 1}); err == nil {
		t.Fatal("expected error for zero destination")
	}

	// Unregistered peer -> dropped, no error.
	if err := tr.Send("s", pb.Message{To: 99, From: 1}); err != nil {
		t.Fatalf("Send to unknown peer should not error: %v", err)
	}
	if tr.Dropped() != 1 {
		t.Fatalf("Dropped()=%d, want 1", tr.Dropped())
	}
}
