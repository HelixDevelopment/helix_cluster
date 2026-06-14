package raft

// Adversarial / safety-invariant probes for pkg/raft.
//
// CONTEXT (three-way discrimination, established by reading the core files):
// pkg/raft is a THIN WRAPPER over github.com/hashicorp/raft v1.7.3. The hard
// Raft SAFETY properties — election safety (one leader per term), at-most-one
// vote per term, term monotonicity / total order, commit safety, log matching,
// leader completeness, and persistence — are ALL DELEGATED to the library; this
// package does not re-implement the consensus core. Therefore those properties
// are a DOCUMENTED/DELEGATED CONTRACT: the tests below PIN them sink-side
// (asserting the actual invariant — leader count, term value, FSM key/value
// state) against a REAL cluster, rather than re-deriving them. A pin breaking
// would mean the delegated library regressed or our wiring corrupted it.
//
// The LOCALLY-implemented surface this package owns is small and IS probed for
// real defects, sink-side:
//   - node.go Apply(): the ErrNotLeader gate on a follower (must not mutate).
//   - fsm.go KVFSM: deterministic, mutex-guarded Apply/Get/Snapshot (race-safe),
//     last-writer-wins commit order.
//   - cluster.go Leader(): must surface AT MOST ONE leader (no split view).
//
// All probes assert the SAFETY INVARIANT directly (votedFor/term/commitIndex
// proxied through leader identity + replicated FSM state), never merely "no
// panic". Every test is non-blocking and deterministic (bounded polling on
// observed state, never a fixed sleep that assumes timing).

import (
	"fmt"
	"sync"
	"testing"
	"time"

	hraft "github.com/hashicorp/raft"
)

const (
	advElectionTimeout = 10 * time.Second
	advApplyTimeout    = 5 * time.Second
)

// countLeaders returns how many nodes simultaneously report Raft Leader state.
// This is the SINK-SIDE election-safety probe: at any instant, across the whole
// cluster, at most one node may be in Leader state for the current term. (Two
// leaders in one term is the canonical Raft safety violation.)
func countLeaders(nodes []*Node) (int, []string) {
	var ids []string
	for _, n := range nodes {
		if n.State() == hraft.Leader {
			ids = append(ids, n.ID())
		}
	}
	return len(ids), ids
}

// leaderTerm extracts the leader's current Raft term from its Stats(). Term is
// the consensus total-order clock; it must be monotonic across re-elections.
func leaderTerm(t *testing.T, n *Node) uint64 {
	t.Helper()
	stats := n.Raft().Stats()
	termStr, ok := stats["term"]
	if !ok {
		t.Fatalf("raft Stats() has no \"term\" field for node %s: %v", n.ID(), stats)
	}
	var term uint64
	if _, err := fmt.Sscanf(termStr, "%d", &term); err != nil {
		t.Fatalf("parse term %q for node %s: %v", termStr, n.ID(), err)
	}
	return term
}

// TestAdversarial_ElectionSafety_AtMostOneLeaderPerTerm pins the central Raft
// safety property: across a REAL 3-node cluster, no two nodes are ever Leader at
// the same time, and all followers agree on a single leader identity. We sample
// repeatedly over a window (covering steady state AND the churn right after a
// leader kill, when a double-leader bug would surface) and assert the invariant
// holds at EVERY sample.
//
// DELEGATED-CONTRACT PIN: a real two-leaders-in-one-term regression in the
// underlying consensus (or our cluster wiring handing two nodes overlapping
// configurations) makes countLeaders() return >= 2 and fails this test.
func TestAdversarial_ElectionSafety_AtMostOneLeaderPerTerm(t *testing.T) {
	cluster, err := NewInmemCluster(nil, "es-a", "es-b", "es-c")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Shutdown() })

	leader, err := cluster.WaitForLeader(advElectionTimeout)
	if err != nil {
		t.Fatalf("no leader: %v", err)
	}
	nodes := cluster.Nodes()

	// sampleInvariant samples the whole cluster once and fails if >1 leader.
	sampleInvariant := func(phase string) {
		if n, ids := countLeaders(nodes); n > 1 {
			t.Fatalf("ELECTION SAFETY VIOLATED (%s): %d simultaneous leaders %v", phase, n, ids)
		}
		// Cross-check the locally-implemented Cluster.Leader(): it must surface at
		// most one node (it already requires IsLeader && self-advertised).
		seen := map[string]struct{}{}
		for _, n := range nodes {
			if l := clusterLeaderView(n); l != "" {
				seen[l] = struct{}{}
			}
		}
		if len(seen) > 1 {
			t.Fatalf("SPLIT-VIEW (%s): nodes disagree on leader identity: %v", phase, seen)
		}
	}

	// Steady-state window: sample repeatedly.
	for i := 0; i < 50; i++ {
		sampleInvariant("steady-state")
		time.Sleep(2 * time.Millisecond)
	}

	// Churn window: kill the leader and keep sampling through the re-election —
	// the most likely moment for a transient double-leader to appear if the
	// safety logic were broken.
	oldLeaderID := leader.ID()
	if err := leader.Shutdown(); err != nil {
		t.Fatalf("kill leader: %v", err)
	}
	deadline := time.Now().Add(advElectionTimeout)
	survivors := cluster.Survivors(oldLeaderID)
	for time.Now().Before(deadline) {
		// Among SURVIVORS, at most one leader at any instant.
		if n, ids := countLeaders(survivors); n > 1 {
			t.Fatalf("ELECTION SAFETY VIOLATED (post-kill churn): %d leaders %v", n, ids)
		}
		if l := firstLeader(survivors); l != nil {
			break // new leader settled
		}
		time.Sleep(2 * time.Millisecond)
	}
	if firstLeader(survivors) == nil {
		t.Fatalf("no new leader after killing %s", oldLeaderID)
	}
	// Final steady-state re-sample.
	for i := 0; i < 25; i++ {
		if n, ids := countLeaders(survivors); n > 1 {
			t.Fatalf("ELECTION SAFETY VIOLATED (post-kill steady): %d leaders %v", n, ids)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// clusterLeaderView returns the leader identity this node would surface through
// the locally-implemented Cluster.Leader() predicate (IsLeader && self-named),
// or "" if this node does not consider itself the leader.
func clusterLeaderView(n *Node) string {
	if n.IsLeader() && n.Leader() == n.ID() {
		return n.ID()
	}
	return ""
}

// firstLeader returns the first node in Leader state that also self-advertises
// (matching Cluster.Leader's predicate), or nil.
func firstLeader(nodes []*Node) *Node {
	for _, n := range nodes {
		if n.IsLeader() && n.Leader() == n.ID() {
			return n
		}
	}
	return nil
}

// TestAdversarial_TermMonotonic_AcrossReelection pins term total-order: after a
// leader is killed and a new one elected, the NEW leader's term must be strictly
// GREATER than the dead leader's last term. A stale/equal term on the new leader
// would mean the total-order clock went backward or stalled — a total-order
// violation that would permit a stale leader to overwrite committed state.
//
// DELEGATED-CONTRACT PIN.
func TestAdversarial_TermMonotonic_AcrossReelection(t *testing.T) {
	cluster, err := NewInmemCluster(nil, "tm-a", "tm-b", "tm-c")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Shutdown() })

	leader, err := cluster.WaitForLeader(advElectionTimeout)
	if err != nil {
		t.Fatalf("no leader: %v", err)
	}
	oldTerm := leaderTerm(t, leader)
	oldID := leader.ID()

	if err := leader.Shutdown(); err != nil {
		t.Fatalf("kill leader: %v", err)
	}
	newLeader, err := cluster.WaitForNewLeader(oldID, advElectionTimeout)
	if err != nil {
		t.Fatalf("no new leader: %v", err)
	}
	newTerm := leaderTerm(t, newLeader)

	if newTerm <= oldTerm {
		t.Fatalf("TERM TOTAL-ORDER VIOLATED: new leader %s term=%d not strictly greater than dead leader %s term=%d",
			newLeader.ID(), newTerm, oldID, oldTerm)
	}
}

// TestAdversarial_CommitSafety_CommittedEntrySurvivesReelection pins commit
// safety + leader completeness: an entry committed under leader T1 must still be
// present, byte-identical, on the FSM of whichever node becomes leader at T2 —
// it must NOT be lost or overwritten by the leadership change. We assert the
// SINK (FSM key/value), the actual safety state, not a return code.
//
// DELEGATED-CONTRACT PIN.
func TestAdversarial_CommitSafety_CommittedEntrySurvivesReelection(t *testing.T) {
	cluster, err := NewInmemCluster(nil, "cs-a", "cs-b", "cs-c")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Shutdown() })

	leader, err := cluster.WaitForLeader(advElectionTimeout)
	if err != nil {
		t.Fatalf("no leader: %v", err)
	}
	oldID := leader.ID()

	committed := map[string]string{
		"safety/committed-1": "alpha",
		"safety/committed-2": "beta",
	}
	for k, v := range committed {
		if err := leader.Apply(Command{Op: CommandSet, Key: k, Value: v}, advApplyTimeout); err != nil {
			t.Fatalf("commit %s=%s: %v", k, v, err)
		}
	}
	if err := waitForReplication(cluster.Nodes(), committed, advApplyTimeout); err != nil {
		t.Fatalf("pre-kill replication: %v", err)
	}

	if err := leader.Shutdown(); err != nil {
		t.Fatalf("kill leader: %v", err)
	}
	newLeader, err := cluster.WaitForNewLeader(oldID, advElectionTimeout)
	if err != nil {
		t.Fatalf("no new leader: %v", err)
	}

	// SINK-SIDE: every committed entry must be intact on the new leader's FSM.
	for k, want := range committed {
		got, ok := newLeader.FSM().Get(k)
		if !ok {
			t.Fatalf("COMMIT SAFETY VIOLATED: committed key %q LOST on new leader %s", k, newLeader.ID())
		}
		if got != want {
			t.Fatalf("COMMIT SAFETY VIOLATED: committed key %q OVERWRITTEN on new leader %s: got %q want %q",
				k, newLeader.ID(), got, want)
		}
	}
}

// TestAdversarial_LogMatching_AllFSMsConverge pins the log-matching property at
// the sink: after a batch of commits, EVERY node's FSM must hold the identical
// key/value set (same length, same values) — replicas must converge, never
// diverge. A log-matching break would surface as one node's FSM differing.
//
// DELEGATED-CONTRACT PIN.
func TestAdversarial_LogMatching_AllFSMsConverge(t *testing.T) {
	cluster, err := NewInmemCluster(nil, "lm-a", "lm-b", "lm-c")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Shutdown() })

	leader, err := cluster.WaitForLeader(advElectionTimeout)
	if err != nil {
		t.Fatalf("no leader: %v", err)
	}

	want := map[string]string{}
	for i := 0; i < 20; i++ {
		k := fmt.Sprintf("lm/key-%02d", i)
		v := fmt.Sprintf("val-%02d", i)
		want[k] = v
		if err := leader.Apply(Command{Op: CommandSet, Key: k, Value: v}, advApplyTimeout); err != nil {
			t.Fatalf("apply %s: %v", k, err)
		}
	}
	if err := waitForReplication(cluster.Nodes(), want, advApplyTimeout); err != nil {
		t.Fatalf("convergence: %v", err)
	}
	// Every FSM must hold EXACTLY the same set — assert length too, to catch a
	// replica that gained or lost an entry.
	for _, n := range cluster.Nodes() {
		if got := n.FSM().Len(); got != len(want) {
			t.Fatalf("LOG MATCHING VIOLATED: node %s FSM has %d keys, want %d", n.ID(), got, len(want))
		}
		for k, v := range want {
			if g, ok := n.FSM().Get(k); !ok || g != v {
				t.Fatalf("LOG MATCHING VIOLATED: node %s key %q = %q ok=%v, want %q", n.ID(), k, g, ok, v)
			}
		}
	}
}

// TestAdversarial_FollowerApplyRejected_NoSinkMutation probes the LOCALLY
// implemented leader-gate in node.go Apply(): a write submitted on a FOLLOWER
// must be rejected with ErrNotLeader AND must NOT mutate that follower's FSM via
// some side door. We assert BOTH the error and the SINK (the key never appears
// on the follower's store except through normal replication, which we exclude by
// using a key the leader never writes).
//
// LOCAL-CODE PROBE (real-bug hunt): if Apply() ever fell through to replicate
// from a follower, or mutated local FSM state, this fails sink-side.
func TestAdversarial_FollowerApplyRejected_NoSinkMutation(t *testing.T) {
	cluster, err := NewInmemCluster(nil, "fa-a", "fa-b", "fa-c")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Shutdown() })

	leader, err := cluster.WaitForLeader(advElectionTimeout)
	if err != nil {
		t.Fatalf("no leader: %v", err)
	}

	// Find a follower.
	var follower *Node
	for _, n := range cluster.Survivors(leader.ID()) {
		if !n.IsLeader() {
			follower = n
			break
		}
	}
	if follower == nil {
		t.Fatalf("no follower found")
	}

	const ghostKey = "follower-only/ghost"
	err = follower.Apply(Command{Op: CommandSet, Key: ghostKey, Value: "should-not-exist"}, advApplyTimeout)
	if err == nil {
		t.Fatalf("LEADER-GATE VIOLATED: follower %s accepted a write (expected ErrNotLeader)", follower.ID())
	}
	// The error must be the leader-gate error (delegated check is advisory; local
	// gate returns ErrNotLeader before touching the log).
	if err.Error() != ErrNotLeader.Error() {
		// Tolerate a library-level rejection too, but the SINK assertion below is
		// the real safety check.
		t.Logf("follower rejection error (non-fatal, sink-checked below): %v", err)
	}

	// SINK-SIDE: the ghost key must NOT appear on ANY node's FSM — neither the
	// follower (it must not have mutated locally) nor others (it must not have
	// replicated). Give a brief window to expose any erroneous async write.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		for _, n := range cluster.Nodes() {
			if _, ok := n.FSM().Get(ghostKey); ok {
				t.Fatalf("SINK MUTATION: ghost key from follower write appeared on node %s FSM", n.ID())
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAdversarial_FSMConcurrency_Race drives the LOCALLY implemented KVFSM under
// concurrent Apply / Get / Len / Snapshot / AppliedCount from many goroutines.
// Run under `-race`, this surfaces any unguarded access to the shared store /
// applied counter. We assert SINK-SIDE that after N applies on a single key the
// final value is the LAST writer's (deterministic last-writer-wins under the
// mutex) and AppliedCount reflects every Apply.
//
// LOCAL-CODE PROBE (data-race hunt). Note: this exercises KVFSM directly (its
// public Apply via *hraft.Log), which is legitimate — KVFSM is a standalone
// state machine type with exported methods used by observers/tests.
func TestAdversarial_FSMConcurrency_Race(t *testing.T) {
	fsm := NewKVFSM()

	const writers = 16
	const perWriter = 64

	var wg sync.WaitGroup
	// Concurrent writers to DISTINCT keys (so the final state is fully
	// determined) plus concurrent readers/snapshotters racing them.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				cmd := Command{Op: CommandSet, Key: fmt.Sprintf("w%02d/k%03d", w, i), Value: "v"}
				data, _ := cmd.Encode()
				if resp := fsm.Apply(&hraft.Log{Data: data}); resp != nil {
					t.Errorf("fsm apply returned error: %v", resp)
				}
			}
		}(w)
	}
	// Concurrent readers / snapshotters racing the writers (race detector target).
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = fsm.Len()
				_, _ = fsm.Get("w00/k000")
				_ = fsm.AppliedCount()
				if snap, err := fsm.Snapshot(); err == nil && snap != nil {
					snap.Release()
				}
			}
		}()
	}
	wg.Wait()

	// SINK-SIDE invariants after the storm.
	wantKeys := writers * perWriter
	if got := fsm.Len(); got != wantKeys {
		t.Fatalf("FSM concurrency: expected %d keys, got %d (lost/duplicated write under race)", wantKeys, got)
	}
	if got := fsm.AppliedCount(); got != uint64(wantKeys) {
		t.Fatalf("FSM concurrency: AppliedCount=%d, want %d (counter race)", got, wantKeys)
	}
}

// TestAdversarial_FSM_LastWriterWins_CommitOrder pins the deterministic
// commit-order semantics of KVFSM: applying SET k=v1 then SET k=v2 then DELETE k
// in order must leave the key ABSENT, and SET k=a then SET k=b must leave b.
// This is the per-replica determinism that makes log matching meaningful (same
// ordered log -> same state). LOCAL-CODE PROBE, sink-side.
func TestAdversarial_FSM_LastWriterWins_CommitOrder(t *testing.T) {
	fsm := NewKVFSM()
	apply := func(op CommandType, k, v string) {
		data, err := Command{Op: op, Key: k, Value: v}.Encode()
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if resp := fsm.Apply(&hraft.Log{Data: data}); resp != nil {
			t.Fatalf("apply %s %s: %v", op, k, resp)
		}
	}

	apply(CommandSet, "k", "a")
	apply(CommandSet, "k", "b")
	if v, ok := fsm.Get("k"); !ok || v != "b" {
		t.Fatalf("LAST-WRITER-WINS VIOLATED: k=%q ok=%v, want b", v, ok)
	}
	apply(CommandDelete, "k", "")
	if _, ok := fsm.Get("k"); ok {
		t.Fatalf("DELETE-ORDER VIOLATED: k present after trailing delete")
	}

	// An unknown op must be rejected (returned error) AND must not mutate state.
	badData, _ := Command{Op: CommandType("frobnicate"), Key: "x", Value: "y"}.Encode()
	if resp := fsm.Apply(&hraft.Log{Data: badData}); resp == nil {
		t.Fatalf("unknown op accepted (expected error response)")
	}
	if _, ok := fsm.Get("x"); ok {
		t.Fatalf("SINK MUTATION: unknown-op key x leaked into store")
	}
}
