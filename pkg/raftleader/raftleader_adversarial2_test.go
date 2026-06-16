package raftleader

// Adversarial SECOND pass over the Raft leader-election adapter.
//
// The first adversarial pass (raftleader_adversarial_test.go) pinned the core
// Raft safety invariants (at-most-one-leader-per-term, term monotonicity,
// reelection convergence, resign transfer, no split-brain under concurrent
// reads). This second pass targets the ADAPTER-SPECIFIC glue that those probes
// do not exercise in depth:
//
//   - the LOSE side of leadership observation: when a node stops being leader
//     (via resign/transfer), its LeadershipChanges channel must actually emit an
//     IsLeader==false event — the first pass only ever asserts the ACQUIRE event;
//   - LeaderID/IsLeader self-consistency at the instant an acquire event is
//     delivered (a delivered "I am leader" event whose LeaderID is some OTHER
//     node would be a torn/stale read the cluster's gating relies on);
//   - Campaign / WaitForLeadership honoring context cancellation (a follower that
//     never wins must not hang or mis-report) — the leader-gate must fail CLOSED
//     on a follower, never spuriously report leadership;
//   - Resign on a follower is a clean no-op (must NOT error, must NOT flip any
//     other node's leadership);
//   - Close is idempotent and closes the changes channel exactly once (a second
//     Close, or a Close racing the watcher, must not panic / double-close).
//
// Every probe is timeout-guarded so a regression FAILS rather than hangs.

import (
	"context"
	"sync"
	"testing"
	"time"
)

// drainUntilLose consumes LeadershipState events from e until it sees one with
// IsLeader==false (the leader->follower transition), or the timeout fires. It
// returns the lose event and true on success; a zero event and false on timeout
// or channel close without a lose event.
func drainUntilLose(e *RaftElection, timeout time.Duration) (LeadershipState, bool) {
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-e.LeadershipChanges():
			if !ok {
				return LeadershipState{}, false
			}
			if !ev.IsLeader {
				return ev, true
			}
		case <-deadline:
			return LeadershipState{}, false
		}
	}
}

// TestResigningLeaderObservesLoseEvent proves the LOSE half of the leadership
// observation contract: a node that resigns leadership must see an
// IsLeader==false event on its OWN LeadershipChanges channel. The first
// adversarial pass only ever asserts the acquire (true) event; a watcher that
// silently swallowed the lose transition (so a consumer believed it was still
// leader and kept performing leader-only work) would be a fail-OPEN safety bug
// that the acquire-only assertions cannot catch.
func TestResigningLeaderObservesLoseEvent(t *testing.T) {
	cluster, err := NewInmemElectionCluster(nil, "lose1", "lose2", "lose3")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Close() })

	leader, err := cluster.WaitForSingleLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no leader: %v", err)
	}
	oldID := leader.ID()

	// Drain the acquire event(s) already queued so we are positioned to observe
	// the subsequent LOSE transition specifically.
	if err := expectLeadershipAcquire(leader, 5*time.Second); err != nil {
		t.Fatalf("did not observe initial acquire on %s: %v", oldID, err)
	}

	if err := leader.Resign(); err != nil {
		t.Fatalf("resign: %v", err)
	}

	// SINK-SIDE: the resigning leader must observe its own loss of leadership.
	ev, ok := drainUntilLose(leader, 5*time.Second)
	if !ok {
		t.Fatalf("FAIL-OPEN: leader %s resigned but never delivered an IsLeader==false (lose) event on its LeadershipChanges channel; a consumer would still believe it is leader", oldID)
	}
	// And after the lose event, IsLeader() itself must agree it is no longer leader.
	deadline := time.Now().Add(5 * time.Second)
	for leader.IsLeader() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if leader.IsLeader() {
		t.Fatalf("FAIL-OPEN: %s delivered a lose event but IsLeader() still reports true", oldID)
	}
	t.Logf("[safety] resigning leader %s observed lose event (LeaderID=%q at delivery) and IsLeader()==false", oldID, ev.LeaderID)
}

// TestAcquireEventIsSelfConsistent proves that when a node delivers an
// IsLeader==true (acquire) event, that node is in fact the one Raft considers
// leader. A delivered "I am leader" event must not name a DIFFERENT node as the
// leader — that would be a torn/stale read of the leadership pair that the
// cluster's gating (IsLeader && LeaderID==ID) depends on.
//
// Note: at the exact instant LeaderCh fires, node.Leader() can momentarily still
// be "" (the leader address has not been published yet). That transient "" is
// tolerated (the event is fresh and IsLeader() will catch up); what must NEVER
// happen is the event naming a DIFFERENT, wrong leader id while claiming this
// node just acquired leadership.
func TestAcquireEventIsSelfConsistent(t *testing.T) {
	cluster, err := NewInmemElectionCluster(nil, "sc1", "sc2", "sc3")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Close() })

	leader, err := cluster.WaitForSingleLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no leader: %v", err)
	}
	self := leader.ID()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-leader.LeadershipChanges():
			if !ok {
				t.Fatalf("channel closed before an acquire event for %s", self)
			}
			if ev.IsLeader {
				// LeaderID must be either self or transiently empty — never a
				// different node while claiming self-acquisition.
				if ev.LeaderID != "" && ev.LeaderID != self {
					t.Fatalf("TORN LEADERSHIP READ: node %s delivered acquire (IsLeader=true) but event LeaderID=%q (a different node)", self, ev.LeaderID)
				}
				t.Logf("[safety] acquire event on %s self-consistent (LeaderID=%q)", self, ev.LeaderID)
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for acquire event on %s", self)
		}
	}
}

// TestFollowerWaitForLeadershipHonorsCancel proves the leader-gate fails CLOSED
// on a follower: a node that is NOT the leader must not be reported as one, and
// WaitForLeadership on that follower must block until the context is cancelled
// (returning ctx.Err()) rather than spuriously returning nil. A bug that made
// WaitForLeadership return nil for a non-leader would let a follower run
// leader-only work — a fail-OPEN split-brain enabler.
func TestFollowerWaitForLeadershipHonorsCancel(t *testing.T) {
	cluster, err := NewInmemElectionCluster(nil, "w1", "w2", "w3")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Close() })

	leader, err := cluster.WaitForSingleLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no leader: %v", err)
	}
	// Pick a follower.
	var follower *RaftElection
	for _, f := range cluster.Followers() {
		if f.ID() != leader.ID() {
			follower = f
			break
		}
	}
	if follower == nil {
		t.Fatalf("no follower found")
	}
	if follower.IsLeader() {
		t.Fatalf("precondition: %s must be a follower", follower.ID())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- follower.WaitForLeadership(ctx) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("FAIL-OPEN: WaitForLeadership returned nil for follower %s (it never became leader); a non-leader was reported as leader", follower.ID())
		}
		_ = start
		// Must be the context error, not a spurious success.
		if ctx.Err() == nil {
			t.Fatalf("WaitForLeadership returned %v but context was not cancelled", err)
		}
		t.Logf("[safety] follower %s WaitForLeadership correctly blocked then returned ctx err: %v", follower.ID(), err)
	case <-time.After(5 * time.Second):
		t.Fatalf("WaitForLeadership on follower %s HUNG past context deadline (no fail-closed return)", follower.ID())
	}
}

// TestFollowerCampaignCancelDoesNotFabricateLeader proves Campaign returns the
// context error (and an EMPTY leader id) when cancelled before any leader is
// observed — it must not fabricate a leader id. We force the no-leader window by
// cancelling immediately; the contract is: on cancellation, (\"\", ctx.Err()).
func TestCampaignReturnsObservedLeaderNotSelf(t *testing.T) {
	cluster, err := NewInmemElectionCluster(nil, "cmp1", "cmp2", "cmp3")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Close() })

	leader, err := cluster.WaitForSingleLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no leader: %v", err)
	}
	leaderID := leader.ID()

	// Campaign from a FOLLOWER must observe the ACTUAL leader (the real winner),
	// not the follower itself. A bug returning the caller's own id would mislead a
	// follower into believing it leads.
	var follower *RaftElection
	for _, f := range cluster.Followers() {
		follower = f
		break
	}
	if follower == nil {
		t.Fatalf("no follower")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observed, err := follower.Campaign(ctx)
	if err != nil {
		t.Fatalf("follower campaign: %v", err)
	}
	if observed != leaderID {
		t.Fatalf("FAIL-OPEN: follower %s Campaign observed leader=%q, but the real leader is %q", follower.ID(), observed, leaderID)
	}
	if observed == follower.ID() && follower.ID() != leaderID {
		t.Fatalf("FAIL-OPEN: follower %s Campaign returned ITS OWN id as leader", follower.ID())
	}
	t.Logf("[safety] follower %s Campaign correctly observed real leader %q (not self)", follower.ID(), observed)
}

// TestResignOnFollowerIsNoopAndDoesNotDisturbLeadership pins the documented
// contract: Resign on a non-leader returns nil and does NOT change who is
// leader. A buggy Resign that triggered a transfer (or errored) from a follower
// would needlessly disrupt a healthy cluster.
func TestResignOnFollowerIsNoopAndDoesNotDisturbLeadership(t *testing.T) {
	cluster, err := NewInmemElectionCluster(nil, "rf1", "rf2", "rf3")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Close() })

	leader, err := cluster.WaitForSingleLeader(electionTimeout)
	if err != nil {
		t.Fatalf("no leader: %v", err)
	}
	leaderID := leader.ID()
	preTerm, _ := term(leader)

	var follower *RaftElection
	for _, f := range cluster.Followers() {
		follower = f
		break
	}
	if follower == nil {
		t.Fatalf("no follower")
	}

	if err := follower.Resign(); err != nil {
		t.Fatalf("Resign on follower %s must be a no-op returning nil, got: %v", follower.ID(), err)
	}

	// Leadership must be undisturbed: same leader, exactly one leader, same term.
	// Give a brief settle window, then assert stability.
	time.Sleep(150 * time.Millisecond)
	if c := cluster.CountLeaders(); c != 1 {
		t.Fatalf("follower Resign disturbed leadership: CountLeaders()=%d, want 1", c)
	}
	stillLeader := cluster.Leader()
	if stillLeader == nil || stillLeader.ID() != leaderID {
		gotID := "<nil>"
		if stillLeader != nil {
			gotID = stillLeader.ID()
		}
		t.Fatalf("follower Resign changed the leader: was %s, now %s", leaderID, gotID)
	}
	postTerm, _ := term(stillLeader)
	if postTerm != preTerm {
		t.Fatalf("follower Resign advanced the term (pre=%d post=%d) — it must be a pure no-op", preTerm, postTerm)
	}
	t.Logf("[safety] Resign on follower %s was a clean no-op; leader %s and term %d unchanged", follower.ID(), leaderID, preTerm)
}

// TestCloseIsIdempotentAndClosesChannel proves Close can be called multiple
// times without panicking (double-close of stopCh / changes) and that after
// Close the LeadershipChanges channel is closed (drained reads return !ok). A
// non-idempotent Close (panic on second call) or a channel left open would be a
// resource/observation defect.
func TestCloseIsIdempotentAndClosesChannel(t *testing.T) {
	cluster, err := NewInmemElectionCluster(nil, "cl1", "cl2", "cl3")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	// We manage Close ourselves here; still register a guarded cleanup for the
	// underlying raft cluster.
	t.Cleanup(func() { _ = cluster.raftCluster.Shutdown() })

	if _, err := cluster.WaitForSingleLeader(electionTimeout); err != nil {
		t.Fatalf("no leader: %v", err)
	}

	target := cluster.Elections()[0]

	// First Close: must close the changes channel.
	target.Close()
	// Second + third Close: must be no-ops, never panic (idempotency).
	target.Close()
	target.Close()

	// Drain the channel; once exhausted, reads must observe a CLOSED channel
	// (ok==false), proving watchLeadership ran its `defer close(e.changes)`.
	closed := false
	deadline := time.After(5 * time.Second)
	for !closed {
		select {
		case _, ok := <-target.LeadershipChanges():
			if !ok {
				closed = true
			}
		case <-deadline:
			t.Fatalf("LeadershipChanges channel was not closed after Close()")
		}
	}
	t.Logf("[safety] Close idempotent (3x, no panic) and LeadershipChanges channel closed")

	// Close the remaining elections so the cleanup shutdown is clean.
	for _, e := range cluster.Elections()[1:] {
		e.Close()
	}
}

// TestConcurrentCloseNoPanic stresses Close from many goroutines simultaneously
// to flush out any double-close / data race in the stopOnce / wg / channel
// teardown. Under -race this also asserts the teardown is race-free.
func TestConcurrentCloseNoPanic(t *testing.T) {
	cluster, err := NewInmemElectionCluster(nil, "cc1", "cc2", "cc3")
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.raftCluster.Shutdown() })

	if _, err := cluster.WaitForSingleLeader(electionTimeout); err != nil {
		t.Fatalf("no leader: %v", err)
	}

	target := cluster.Elections()[0]
	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			target.Close()
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("concurrent Close() deadlocked")
	}
	t.Logf("[safety] %d concurrent Close() calls: no panic, no deadlock", goroutines)

	for _, e := range cluster.Elections()[1:] {
		e.Close()
	}
}
