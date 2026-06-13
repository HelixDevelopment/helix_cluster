package raft

import (
	"testing"
	"time"

	hraft "github.com/hashicorp/raft"
)

// TestPreVote_PartitionRejoinDoesNotDisruptLeader is the REAL runtime realization
// of specs/RaftPreVote.tla (HXC-986): hashicorp/raft's PreVote (pre-election)
// feature, ENABLED BY DEFAULT in v1.7.3 (Config.PreVoteDisabled defaults to
// false), prevents a partitioned-then-reconnected follower from DISRUPTING a
// stable leader.
//
// The mechanism it proves (no bluff):
//
//	A follower that is network-isolated from the rest of the cluster stops
//	hearing the leader's heartbeats and its election timer fires. WITHOUT
//	pre-vote, on every timeout it transitions to Candidate and INCREMENTS its own
//	currentTerm before soliciting votes. Over a long isolation it inflates its
//	term far beyond the cluster's. When it RECONNECTS, its RequestVote (or even a
//	heartbeat reply) carries that higher term; raft's rule "if RPC term > my term,
//	step down and adopt it" forces the SITTING LEADER to revert to follower and a
//	fresh election to run — a real, observable disruption.
//
//	WITH pre-vote (the default), the isolated follower first runs a PRE-election:
//	it asks peers "would you vote for me?" WITHOUT bumping its term. While
//	isolated it has no peers to answer, so the pre-election never succeeds and it
//	NEVER inflates currentTerm. On reconnect it carries the SAME (old) term it had
//	before the partition, learns the current leader/term from the first heartbeat,
//	and rejoins quietly. The leader is NOT deposed and the cluster term does NOT
//	jump on its account.
//
// Maps to the TLA+ model: an isolated node that, under PreVote, may take the
// PreVote action (which cannot raise its term without a quorum granting it) vs.
// under no-PreVote takes the Timeout/IncrementTerm action freely. The safety
// property "a partitioned node rejoining cannot, by itself, force the existing
// leader to step down / cannot inflate the committed term" is exactly what the
// PreVote-ON assertions below check at runtime.
//
// ANTI-BLUFF: the SAME scenario is run with PreVoteDisabled=true and asserted to
// produce the OPPOSITE — the reconnecting node's inflated term DOES disrupt the
// cluster (term jumps well past the original, the isolated node having driven it).
// The comparison is what makes the PreVote-ON assertion load-bearing: if PreVote
// were not actually doing its job, the ON run would inflate just like the OFF run
// and the differential assertion would fail.
func TestPreVote_PartitionRejoinDoesNotDisruptLeader(t *testing.T) {
	logf := func(format string, args ...any) {
		t.Logf("[raft-prevote-it] "+format, args...)
	}

	const (
		electionTimeout = 15 * time.Second
		settleTimeout   = 15 * time.Second
		// Isolation window: long enough that the isolated follower's 100ms election
		// timer fires MANY times (so, without prevote, it inflates its term by a lot,
		// and with prevote it stays put). We don't rely on the exact duration for the
		// assertion — we poll the isolated node's term to PROVE what actually happened.
		isolationWindow = 3 * time.Second
	)

	// Run the identical partition/rejoin scenario twice: once with PreVote ON
	// (default) and once with PreVote explicitly OFF. Return the observed terms so
	// the two runs can be compared.
	onResult := runPartitionRejoin(t, logf, "ON", false, electionTimeout, isolationWindow, settleTimeout)
	offResult := runPartitionRejoin(t, logf, "OFF", true, electionTimeout, isolationWindow, settleTimeout)

	// ---- PreVote-ON assertions: NO DISRUPTION ---------------------------------

	// (1) The ORIGINAL leader is STILL the leader after the partitioned follower
	// rejoins. PreVote stopped the rejoiner from deposing it.
	if onResult.leaderAfter != onResult.leaderBefore {
		t.Fatalf("PREVOTE-ON DISRUPTION: original leader %q was replaced by %q after the partitioned follower rejoined — PreVote did not protect the leader",
			onResult.leaderBefore, onResult.leaderAfter)
	}
	logf("PREVOTE-ON: original leader %q retained leadership across the partition/rejoin (no disruption)", onResult.leaderBefore)

	// (2) The isolated follower did NOT inflate its term while isolated. With
	// PreVote it runs pre-elections that cannot succeed (no peers reachable), so it
	// never bumps currentTerm. We allow it to be EQUAL to the leader's term but it
	// must NOT have run away above it.
	if onResult.isolatedTermDuring > onResult.clusterTermBefore {
		t.Fatalf("PREVOTE-ON FAILED: isolated follower term climbed to %d while isolated (cluster term was %d) — PreVote did NOT prevent term inflation",
			onResult.isolatedTermDuring, onResult.clusterTermBefore)
	}
	logf("PREVOTE-ON: isolated follower term stayed at %d during isolation (cluster term %d) — no inflation",
		onResult.isolatedTermDuring, onResult.clusterTermBefore)

	// (3) The cluster's term after rejoin did not jump on the rejoiner's account.
	// A benign +0/+1 is tolerated (raft can have a single in-flight bump), but the
	// runaway inflation the OFF run shows must NOT appear here.
	onJump := int64(onResult.clusterTermAfter) - int64(onResult.clusterTermBefore)
	if onJump > 1 {
		t.Fatalf("PREVOTE-ON FAILED: cluster term jumped by %d (%d -> %d) after rejoin — the partitioned node disrupted the cluster",
			onJump, onResult.clusterTermBefore, onResult.clusterTermAfter)
	}
	logf("PREVOTE-ON: cluster term %d -> %d (jump=%d) across partition/rejoin",
		onResult.clusterTermBefore, onResult.clusterTermAfter, onJump)

	// ---- ANTI-BLUFF: PreVote-OFF must show the OPPOSITE -----------------------

	// The OFF run's isolated follower MUST have inflated its term while isolated
	// (each fired election timer +1). This proves the OFF scenario actually
	// exercises the disruptive path — otherwise the comparison would be vacuous.
	if offResult.isolatedTermDuring <= offResult.clusterTermBefore {
		t.Fatalf("ANTI-BLUFF SETUP FAILED: with PreVote OFF the isolated follower term did NOT inflate (during=%d, before=%d) — the no-prevote disruptive path was not exercised, so the comparison proves nothing",
			offResult.isolatedTermDuring, offResult.clusterTermBefore)
	}
	logf("PREVOTE-OFF: isolated follower term INFLATED %d -> %d while isolated (no prevote, every timeout bumped the term)",
		offResult.clusterTermBefore, offResult.isolatedTermDuring)

	// The OFF run's CLUSTER term after rejoin must have jumped to absorb the
	// rejoiner's inflated term (the sitting leader steps down, a new election runs
	// at the higher term). This is the disruption.
	offJump := int64(offResult.clusterTermAfter) - int64(offResult.clusterTermBefore)
	if offJump <= 1 {
		t.Fatalf("ANTI-BLUFF FAILED: with PreVote OFF the cluster term did NOT jump on rejoin (%d -> %d, jump=%d) — expected the inflated rejoiner to force a higher-term election; the disruption was not reproduced",
			offResult.clusterTermBefore, offResult.clusterTermAfter, offJump)
	}
	logf("PREVOTE-OFF: cluster term JUMPED %d -> %d (jump=%d) after the inflated follower rejoined — DISRUPTION reproduced",
		offResult.clusterTermBefore, offResult.clusterTermAfter, offJump)

	// ---- The load-bearing DIFFERENTIAL assertion ------------------------------
	//
	// This is what makes the PreVote-ON result meaningful and the whole test
	// non-vacuous: under identical partition timing and identical 100ms timers, the
	// no-prevote cluster's term jump is STRICTLY GREATER than the prevote cluster's.
	// The ONLY independent variable between the two runs is PreVoteDisabled, so a
	// strictly larger jump in the OFF run is attributable to PreVote and nothing
	// else. If PreVote were a no-op, the two jumps would be comparable and this
	// assertion would FAIL — i.e. the test has teeth.
	if offJump <= onJump {
		t.Fatalf("DIFFERENTIAL FAILED: PreVote-OFF term jump (%d) was not strictly greater than PreVote-ON term jump (%d) — PreVote did not measurably reduce disruption; the ON assertions are not load-bearing",
			offJump, onJump)
	}
	logf("DIFFERENTIAL PROVEN: PreVote-OFF cluster term jump (%d) >> PreVote-ON jump (%d). The isolated follower's inflated term disrupted the no-prevote cluster but NOT the prevote cluster — the only difference between the runs is PreVoteDisabled.",
		offJump, onJump)

	logf("PASS: hashicorp/raft PreVote (default-on in v1.7.3) prevents a partitioned/reconnected follower from disrupting a stable leader; disabling it reproduces the disruption (runtime pairing of specs/RaftPreVote.tla, HXC-986)")
}

// prevoteRun captures the term/leadership observations of one partition/rejoin run.
type prevoteRun struct {
	leaderBefore string // leader id before the partition
	leaderAfter  string // leader id after the rejoin settled

	clusterTermBefore uint64 // leader's term before the partition
	clusterTermAfter  uint64 // max term across the cluster after rejoin settled

	isolatedTermDuring uint64 // isolated follower's OWN term observed during isolation
}

// runPartitionRejoin stands up a fresh 3-node in-mem cluster (PreVote on or off
// per disablePreVote), elects a stable leader, isolates ONE follower by severing
// its in-mem transport routes in BOTH directions, lets it sit isolated, then heals
// the partition and waits for the cluster to settle — capturing terms throughout.
//
// The partition is forced by InmemTransport.Disconnect (the real in-process
// equivalent of a network partition): the isolated node's transport drops its
// routes to the two survivors, and each survivor drops its route to the isolated
// node. With no route either way the isolated node receives no heartbeats (its
// election timer fires) and its own RPCs reach no one (so, with prevote, its
// pre-election cannot gather votes). Heal = re-Connect all four routes.
func runPartitionRejoin(t *testing.T, logf func(string, ...any), label string, disablePreVote bool,
	electionTimeout, isolationWindow, settleTimeout time.Duration) prevoteRun {
	t.Helper()

	base := &hraft.Config{PreVoteDisabled: disablePreVote}
	ids := []string{"pv-" + label + "-a", "pv-" + label + "-b", "pv-" + label + "-c"}
	cluster, err := NewInmemCluster(base, ids...)
	if err != nil {
		t.Fatalf("[%s] build cluster: %v", label, err)
	}
	t.Cleanup(func() { _ = cluster.Shutdown() })

	leader, err := cluster.WaitForLeader(electionTimeout)
	if err != nil {
		t.Fatalf("[%s] no leader elected: %v", label, err)
	}
	if err := waitForAllAgreeLeader(cluster.Nodes(), leader.ID(), electionTimeout); err != nil {
		t.Fatalf("[%s] nodes did not agree on leader %s: %v", label, leader.ID(), err)
	}

	var run prevoteRun
	run.leaderBefore = leader.ID()
	run.clusterTermBefore = statUint(t, leader, "term")
	logf("[%s] STABLE: leader=%s clusterTerm(before)=%d", label, run.leaderBefore, run.clusterTermBefore)

	// Pick a FOLLOWER to isolate (never the leader — we are testing that an isolated
	// follower cannot depose the leader).
	var isolated *Node
	survivors := make([]*Node, 0, 2)
	for _, n := range cluster.Nodes() {
		if n.ID() == leader.ID() {
			survivors = append(survivors, n) // leader stays among the connected majority
			continue
		}
		if isolated == nil {
			isolated = n
		} else {
			survivors = append(survivors, n)
		}
	}
	if isolated == nil || len(survivors) != 2 {
		t.Fatalf("[%s] failed to choose an isolated follower (isolated=%v, survivors=%d)", label, isolated, len(survivors))
	}

	// PARTITION: sever both directions between the isolated follower and the two
	// survivors (one of which is the leader). After this the isolated node is alone.
	for _, s := range survivors {
		isolated.Transport().Disconnect(s.Transport().LocalAddr())
		s.Transport().Disconnect(isolated.Transport().LocalAddr())
	}
	logf("[%s] PARTITIONED follower %s (severed both directions from %s and %s)",
		label, isolated.ID(), survivors[0].ID(), survivors[1].ID())

	// While isolated, POLL the isolated node's OWN term and track the maximum it
	// reaches. Without prevote it climbs on every timeout; with prevote it does not
	// move. We capture the max rather than a single read so a slow scheduler can't
	// miss the inflation in the OFF run.
	isolatedMaxTerm := run.clusterTermBefore
	deadline := time.Now().Add(isolationWindow)
	for time.Now().Before(deadline) {
		if tm := statUint(t, isolated, "term"); tm > isolatedMaxTerm {
			isolatedMaxTerm = tm
		}
		time.Sleep(25 * time.Millisecond)
	}
	run.isolatedTermDuring = isolatedMaxTerm
	logf("[%s] DURING isolation: isolated follower %s term reached %d (cluster term before was %d)",
		label, isolated.ID(), run.isolatedTermDuring, run.clusterTermBefore)

	// During the partition the leader must NOT have changed for the surviving
	// majority (2 of 3 with prevote OR no-prevote keeps quorum). Confirm the
	// majority still has the original leader before we heal — establishes the
	// baseline that any post-rejoin disruption is attributable to the rejoiner.
	if l := cluster.Leader(); l == nil || l.ID() != run.leaderBefore {
		// Not fatal for the OFF run (the surviving majority always keeps its leader
		// regardless of prevote because the isolated node can't reach them) — but log
		// loudly if it ever differs.
		got := "<none>"
		if l != nil {
			got = l.ID()
		}
		logf("[%s] NOTE: during isolation the majority leader is %s (expected %s)", label, got, run.leaderBefore)
	}

	// HEAL: reconnect all four routes. Now the (possibly term-inflated) follower
	// rejoins and we observe whether it disrupts the leader.
	for _, s := range survivors {
		isolated.Transport().Connect(s.Transport().LocalAddr(), s.Transport())
		s.Transport().Connect(isolated.Transport().LocalAddr(), isolated.Transport())
	}
	logf("[%s] HEALED partition for follower %s — reconnected all routes", label, isolated.ID())

	// Settle: wait until SOME leader exists, then read the maximum term across the
	// cluster. We poll for a leader (not a fixed sleep) so the post-rejoin state is
	// observed deterministically.
	if _, err := cluster.WaitForLeader(settleTimeout); err != nil {
		t.Fatalf("[%s] no leader after healing partition: %v", label, err)
	}

	if disablePreVote {
		// OFF (disruptive) run: the rejoining follower carries an inflated term that
		// forces a higher-term election; the cluster legitimately churns (the inflated
		// node can even transiently win) before re-converging. We do NOT demand
		// unanimous agreement inside a fixed window here — requiring clean re-agreement
		// of an inherently DISRUPTED cluster would be the wrong (and flaky) assertion.
		// The disruption SIGNAL is the term, which has already jumped to absorb the
		// inflated rejoiner. We give it a brief moment to let terms propagate, then
		// record the cluster's max term — that elevated term IS the proof of
		// disruption, independent of which node currently holds leadership.
		_ = waitForClusterTermAtLeast(t, cluster.Nodes(), run.clusterTermBefore+2, settleTimeout)
		run.leaderAfter = currentLeaderID(cluster)
	} else {
		// ON (non-disruptive) run: PreVote must have kept the cluster stable, so the
		// stronger property MUST hold — every node re-agrees on a single leader, and it
		// is the SAME original leader. This is the assertion that would FAIL if PreVote
		// did not protect the leader.
		if err := waitForAllAgreeLeader(cluster.Nodes(), run.leaderBefore, settleTimeout); err != nil {
			t.Fatalf("[%s] nodes did not re-agree on the ORIGINAL leader %q after rejoin (PreVote should have prevented any disruption): %v", label, run.leaderBefore, err)
		}
		run.leaderAfter = run.leaderBefore
	}

	run.clusterTermAfter = maxClusterTerm(t, cluster.Nodes())
	logf("[%s] SETTLED: leader=%s clusterTerm(after)=%d", label, run.leaderAfter, run.clusterTermAfter)

	return run
}

// waitForClusterTermAtLeast polls until the maximum "term" across all nodes
// reaches want, or the deadline expires. It returns true if the threshold was
// reached. Used in the disruptive OFF run to let the inflated rejoiner's higher
// term propagate before we read it, deterministically (poll, not fixed sleep).
func waitForClusterTermAtLeast(t *testing.T, nodes []*Node, want uint64, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if maxClusterTerm(t, nodes) >= want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// currentLeaderID returns the cluster's current leader id, or "<none>" if there
// is momentarily no leader (tolerated in the disruptive OFF run).
func currentLeaderID(c *Cluster) string {
	if l := c.Leader(); l != nil {
		return l.ID()
	}
	return "<none>"
}

// maxClusterTerm returns the highest "term" reported across all nodes. After a
// term-changing event raft converges every node to the same term, but during the
// brief settle window nodes may report transiently different terms; taking the max
// gives the cluster's authoritative current term.
func maxClusterTerm(t *testing.T, nodes []*Node) uint64 {
	t.Helper()
	var maxTerm uint64
	for _, n := range nodes {
		if tm := statUint(t, n, "term"); tm > maxTerm {
			maxTerm = tm
		}
	}
	return maxTerm
}
