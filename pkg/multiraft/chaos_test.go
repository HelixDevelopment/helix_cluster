//go:build chaos

package multiraft

import (
	"context"
	"fmt"
	"testing"

	pb "go.etcd.io/raft/v3/raftpb"
)

// chaos_test.go exercises pkg/multiraft under fault injection (HXC-937,
// Constitution §11.4.85). These are REAL etcd-raft groups driven over the real
// InProcTransport; the faults injected here (leader kill, message-dropping
// partition) are observed for their REAL effect on consensus, and every test
// asserts the sink-side consistency/availability invariant — not a happy path.
//
// Build-tagged `chaos` so it never runs in the default `go test ./...` pass;
// run with: go test -tags chaos ./pkg/multiraft/...

// chaosSettle drives a Ready loop then a bounded number of ticks+Ready loops
// until consensus quiesces. Ticks are needed because re-election after a leader
// kill is timeout-driven (electionTick), not Run-driven. (The plain `settle`
// helper in multiraft_test.go is reused for tick-only drives; this variant adds
// the leading Run() so a just-issued proposal replicates promptly.)
func chaosSettle(m *MultiRaftManager, ticks int) {
	m.Run()
	settle(m, ticks)
}

// electLeader ticks until SOME live node of the shard is leader, returning it.
// Returns 0 if no leader emerged within the budget — the caller asserts on that.
func electLeader(m *MultiRaftManager, shard ShardID, maxTicks int) uint64 {
	for i := 0; i < maxTicks; i++ {
		if l := m.LeaderID(shard); l != 0 {
			return l
		}
		m.Tick()
		m.Run()
	}
	return m.LeaderID(shard)
}

// proposeUntilCommitted proposes data to the shard leader and drives consensus
// until at least one live node has committed `want` total entries, or the tick
// budget is exhausted. Returns the committed count actually observed on the node
// with the most entries (sink-side evidence).
func maxCommittedAcrossLive(m *MultiRaftManager, shard ShardID, live []uint64) int {
	best := 0
	for _, id := range live {
		c, err := m.CommittedCount(shard, id)
		if err == nil && c > best {
			best = c
		}
	}
	return best
}

// TestChaosLeaderLossReElects kills the current leader of a 3-node shard and
// asserts the REAL invariants: a NEW, different leader is elected from the
// surviving quorum, and a write proposed AFTER the kill actually commits on the
// new regime (proving the group is live again, not merely that LeaderID changed).
func TestChaosLeaderLossReElects(t *testing.T) {
	tr := NewInProcTransport()
	m := NewMultiRaftManager(tr)
	const shard = ShardID("chaos-leader-loss")
	peers := []uint64{1, 2, 3}
	if err := m.CreateShard(shard, peers); err != nil {
		t.Fatalf("CreateShard: %v", err)
	}

	leader := electLeader(m, shard, 100)
	if leader == 0 {
		t.Fatal("no initial leader elected within budget")
	}

	// Commit one entry under the original leader so we have a known log prefix.
	if err := m.Propose(context.Background(), shard, []byte("pre-kill")); err != nil {
		t.Fatalf("pre-kill propose: %v", err)
	}
	chaosSettle(m, 5)

	// FAULT: kill the leader. StopNode unregisters its inbox so the remaining
	// quorum must re-elect.
	if err := m.StopNode(shard, leader); err != nil {
		t.Fatalf("StopNode(leader=%d): %v", leader, err)
	}

	// SINK-SIDE INVARIANT 1: a new, DIFFERENT leader is elected from survivors.
	newLeader := uint64(0)
	for i := 0; i < 200; i++ {
		m.Tick()
		m.Run()
		l := m.LeaderID(shard)
		if l != 0 && l != leader {
			newLeader = l
			break
		}
	}
	if newLeader == 0 {
		t.Fatalf("no new leader elected after killing leader %d within budget", leader)
	}
	if newLeader == leader {
		t.Fatalf("dead node %d reported as leader — re-election did not occur", leader)
	}

	// SINK-SIDE INVARIANT 2: the group is genuinely live again — a NEW write
	// proposed post-failure actually commits. This is the test that would catch
	// an "unwired" re-election where LeaderID flips but no progress is possible.
	if err := m.Propose(context.Background(), shard, []byte("post-kill")); err != nil {
		t.Fatalf("post-kill propose to new leader %d: %v", newLeader, err)
	}
	chaosSettle(m, 10)

	live := []uint64{}
	for _, p := range peers {
		if p != leader {
			live = append(live, p)
		}
	}
	committed := maxCommittedAcrossLive(m, shard, live)
	if committed < 2 {
		t.Fatalf("expected >=2 committed entries (pre+post kill) on a surviving node, got %d", committed)
	}

	// SINK-SIDE INVARIANT 3: log consistency — every live node that has applied
	// both entries agrees on the payload order (no divergence across the regime
	// change). Compare each survivor's committed prefix to the leader's.
	leaderLog, err := m.CommittedEntries(shard, newLeader)
	if err != nil {
		t.Fatalf("CommittedEntries(newLeader=%d): %v", newLeader, err)
	}
	for _, id := range live {
		log, err := m.CommittedEntries(shard, id)
		if err != nil {
			t.Fatalf("CommittedEntries(%d): %v", id, err)
		}
		n := len(log)
		if n > len(leaderLog) {
			n = len(leaderLog)
		}
		for i := 0; i < n; i++ {
			if string(log[i]) != string(leaderLog[i]) {
				t.Fatalf("log divergence at index %d: node %d=%q leader %d=%q",
					i, id, log[i], newLeader, leaderLog[i])
			}
		}
	}
}

// TestChaosLeaderChurnConverges repeatedly kills whoever is leader of a 5-node
// shard (churn), recreating shards is not possible so we drain the quorum down
// to its minimum and assert that as long as a majority survives, the group keeps
// electing a leader and committing — and once the quorum is lost, it correctly
// REFUSES to elect a phantom leader (no split-brain / false availability).
func TestChaosLeaderChurnConverges(t *testing.T) {
	tr := NewInProcTransport()
	m := NewMultiRaftManager(tr)
	const shard = ShardID("chaos-churn")
	peers := []uint64{1, 2, 3, 4, 5}
	if err := m.CreateShard(shard, peers); err != nil {
		t.Fatalf("CreateShard: %v", err)
	}

	alive := map[uint64]bool{1: true, 2: true, 3: true, 4: true, 5: true}
	liveSlice := func() []uint64 {
		out := []uint64{}
		for id, ok := range alive {
			if ok {
				out = append(out, id)
			}
		}
		return out
	}
	quorum := 3 // majority of 5

	if electLeader(m, shard, 100) == 0 {
		t.Fatal("no initial leader")
	}

	wrote := 0
	// Kill leaders one at a time. With 5 nodes we can lose 2 and still hold a
	// quorum (3). The 3rd kill breaks quorum.
	for round := 0; round < 3; round++ {
		leader := electLeader(m, shard, 200)
		liveCount := len(liveSlice())

		if liveCount >= quorum {
			// Quorum present: a leader MUST exist and a write MUST commit.
			if leader == 0 {
				t.Fatalf("round %d: quorum present (%d live) but no leader", round, liveCount)
			}
			payload := []byte(fmt.Sprintf("churn-%d", round))
			if err := m.Propose(context.Background(), shard, payload); err != nil {
				t.Fatalf("round %d: propose with quorum present: %v", round, err)
			}
			chaosSettle(m, 8)
			wrote++
			before := maxCommittedAcrossLive(m, shard, liveSlice())
			if before < wrote {
				t.Fatalf("round %d: quorum present but committed=%d < expected %d (group not live)",
					round, before, wrote)
			}
		}

		// FAULT: kill the current leader (or any live node if mid-election).
		victim := leader
		if victim == 0 {
			victim = liveSlice()[0]
		}
		if err := m.StopNode(shard, victim); err != nil {
			t.Fatalf("round %d: StopNode(%d): %v", round, victim, err)
		}
		alive[victim] = false
	}

	// After 3 kills, only 2 nodes remain — quorum (3) is LOST. SINK-SIDE
	// INVARIANT: the group MUST NOT be able to commit a new write (no phantom
	// leader serving writes without a majority). Drive hard and assert the write
	// does NOT commit.
	chaosSettle(m, 30)
	committedBefore := maxCommittedAcrossLive(m, shard, liveSlice())
	// Propose may return ErrNoLeader (correct) or succeed-to-local-log but never
	// commit. Either way the committed count must NOT advance.
	_ = m.Propose(context.Background(), shard, []byte("must-not-commit"))
	chaosSettle(m, 30)
	committedAfter := maxCommittedAcrossLive(m, shard, liveSlice())
	if committedAfter > committedBefore {
		t.Fatalf("split-brain: committed advanced from %d to %d with only %d/%d nodes (no quorum)",
			committedBefore, committedAfter, len(liveSlice()), len(peers))
	}
}

// dropTransport wraps a RaftTransport and, while partitioned, drops every raft
// message whose destination is in the partitioned set — a REAL network-partition
// fault (messages are lost, not buffered), exactly how InProcTransport models a
// downed peer. Healing clears the drop set so delivery resumes.
type dropTransport struct {
	inner       RaftTransport
	mu          chan struct{} // simple guard (1-buffered) to avoid importing sync here
	partitioned map[uint64]bool
	dropped     int
}

func newDropTransport(inner RaftTransport) *dropTransport {
	d := &dropTransport{inner: inner, mu: make(chan struct{}, 1), partitioned: map[uint64]bool{}}
	d.mu <- struct{}{}
	return d
}

func (d *dropTransport) lock()   { <-d.mu }
func (d *dropTransport) unlock() { d.mu <- struct{}{} }

func (d *dropTransport) partition(ids ...uint64) {
	d.lock()
	for _, id := range ids {
		d.partitioned[id] = true
	}
	d.unlock()
}

func (d *dropTransport) heal() {
	d.lock()
	d.partitioned = map[uint64]bool{}
	d.unlock()
}

func (d *dropTransport) Dropped() int {
	d.lock()
	defer d.unlock()
	return d.dropped
}

func (d *dropTransport) Send(shard ShardID, msg pb.Message) error {
	d.lock()
	drop := d.partitioned[msg.To] || d.partitioned[msg.From]
	if drop {
		d.dropped++
	}
	d.unlock()
	if drop {
		return nil // message lost on the partitioned link
	}
	return d.inner.Send(shard, msg)
}

func (d *dropTransport) RegisterPeer(shard ShardID, nodeID uint64, inbox MessageHandler) {
	d.inner.RegisterPeer(shard, nodeID, inbox)
}
func (d *dropTransport) UnregisterPeer(shard ShardID, nodeID uint64) {
	d.inner.UnregisterPeer(shard, nodeID)
}
func (d *dropTransport) SendRead(shard ShardID, leaseholderID uint64) ([]byte, error) {
	return d.inner.SendRead(shard, leaseholderID)
}

var _ RaftTransport = (*dropTransport)(nil)

// TestChaosPartitionHealConverges partitions a single follower off a 3-node
// shard (its messages are dropped), commits writes on the majority side, then
// HEALS the partition and asserts the REAL convergence invariant: the previously
// isolated node catches up and its committed log converges to the leader's. This
// is the network-partition + reconciliation-after-heal scenario.
func TestChaosPartitionHealConverges(t *testing.T) {
	base := NewInProcTransport()
	dt := newDropTransport(base)
	m := NewMultiRaftManager(dt)
	const shard = ShardID("chaos-partition")
	peers := []uint64{1, 2, 3}
	if err := m.CreateShard(shard, peers); err != nil {
		t.Fatalf("CreateShard: %v", err)
	}

	leader := electLeader(m, shard, 100)
	if leader == 0 {
		t.Fatal("no initial leader")
	}

	// Choose a follower to isolate (anything that is not the leader).
	isolated := uint64(0)
	for _, p := range peers {
		if p != leader {
			isolated = p
			break
		}
	}

	// FAULT: partition the follower. The majority (leader + other follower) keeps
	// quorum and must keep committing.
	dt.partition(isolated)

	const writes = 5
	for i := 0; i < writes; i++ {
		if err := m.Propose(context.Background(), shard, []byte(fmt.Sprintf("w%d", i))); err != nil {
			t.Fatalf("propose %d during partition: %v", i, err)
		}
		chaosSettle(m, 4)
	}
	if dt.Dropped() == 0 {
		t.Fatal("partition injected no dropped messages — the fault was not actually exercised")
	}

	// SINK-SIDE during partition: majority committed all writes; isolated node
	// LAGS (it could not receive the appends). This proves the partition had a
	// real effect — if the isolated node were already caught up, the test would
	// not be observing the failure mode (CLAUDE-1 PASS-bluff guard).
	majorityCommitted, err := m.CommittedCount(shard, leader)
	if err != nil {
		t.Fatalf("CommittedCount(leader): %v", err)
	}
	if majorityCommitted < writes {
		t.Fatalf("majority did not commit during partition: got %d want >=%d", majorityCommitted, writes)
	}
	isolatedDuring, err := m.CommittedCount(shard, isolated)
	if err != nil {
		t.Fatalf("CommittedCount(isolated): %v", err)
	}
	if isolatedDuring >= majorityCommitted {
		t.Fatalf("isolated node %d was not actually isolated: committed %d >= majority %d",
			isolated, isolatedDuring, majorityCommitted)
	}

	// HEAL the partition. The previously isolated follower kept bumping its own
	// term while its MsgVotes were dropped, so on heal it may force a fresh
	// election. We therefore drive until the isolated node's committed log
	// CONVERGES to the majority's, re-resolving the current leader each cycle
	// (the leader id is NOT assumed stable across the heal). This is a bounded
	// wait on the real reconciliation, not a fixed tick budget that can flake.
	dt.heal()
	const healTicks = 400
	converged := false
	for i := 0; i < healTicks; i++ {
		m.Tick()
		m.Run()
		isoC, _ := m.CommittedCount(shard, isolated)
		if isoC >= majorityCommitted {
			converged = true
			break
		}
	}
	if !converged {
		isoC, _ := m.CommittedCount(shard, isolated)
		t.Fatalf("post-heal: isolated node %d did not converge: committed %d, want >= %d",
			isolated, isoC, majorityCommitted)
	}

	// SINK-SIDE INVARIANT: after heal, the previously isolated node's committed
	// log matches the CURRENT leader's prefix — identical payload order, no
	// divergence. Resolve the current leader (it may differ from the pre-fault
	// leader after the heal-triggered election).
	curLeader := m.LeaderID(shard)
	if curLeader == 0 {
		t.Fatal("post-heal: no leader after convergence")
	}
	leaderLog, err := m.CommittedEntries(shard, curLeader)
	if err != nil {
		t.Fatalf("CommittedEntries(curLeader=%d): %v", curLeader, err)
	}
	isoLog, err := m.CommittedEntries(shard, isolated)
	if err != nil {
		t.Fatalf("CommittedEntries(isolated): %v", err)
	}
	if len(isoLog) < majorityCommitted {
		t.Fatalf("post-heal divergence: isolated node committed %d entries, want >= %d (did not converge)",
			len(isoLog), majorityCommitted)
	}
	n := len(leaderLog)
	if len(isoLog) < n {
		n = len(isoLog)
	}
	for i := 0; i < n; i++ {
		if string(isoLog[i]) != string(leaderLog[i]) {
			t.Fatalf("post-heal log mismatch at %d: isolated=%q leader=%q", i, isoLog[i], leaderLog[i])
		}
	}
}
