package swim

// runtime_failure_detection_test.go — REAL runtime integration test for the
// SWIM membership/failure-detection protocol. This is the runtime pairing of the
// SWIM TLA+ spec: it stands up N genuine in-process Protocol instances on real
// loopback UDP sockets, Start()s them (so the REAL probe / ping / ping-req /
// gossip / suspicion / failure-detector goroutines run), joins them into one
// cluster, and proves two load-bearing properties:
//
//  1. SAFETY-OF-DETECTION (headline): when ONE member genuinely stops answering
//     pings (its UDP transport is closed — a realistic hard crash), the OTHER
//     members DETECT the failure THROUGH the protocol (probe miss -> suspect ->
//     suspicion-timeout -> dead) and every survivor converges to "failed member
//     is Dead AND every other survivor is still Alive."
//
//  2. NO-FALSE-POSITIVE (anti-bluff): under NORMAL operation (no failures), over
//     many protocol periods, NO healthy member is EVER marked Suspect or Dead by
//     any other member.
//
// Why both are load-bearing (anti-bluff reasoning):
//
//   - Property (1) alone is NOT sufficient. A degenerate "detector" that simply
//     marks every peer Dead after a fixed delay would PASS property (1) — it
//     would happily declare the crashed node dead. The CLAUDE-1 mandate calls a
//     test that passes on a non-functional feature a PASS-bluff. Property (2) is
//     the negative that bites such a bluff: a mark-everything-dead detector marks
//     LIVE peers dead too, so the no-false-positive phase fails. Only a detector
//     that distinguishes live from crashed peers passes BOTH.
//
//   - The failure in (1) is injected by CLOSING THE TRANSPORT, never by calling a
//     markDead / UpdateState(StateDead) API on the survivors' view. The survivors
//     must reach StateDead on their own through real ping timeouts and the real
//     suspicion timer. If detection were faked by directly poking membership, the
//     no-false-positive phase (which never touches the transport) would have no
//     way to ever produce a Dead state — yet the headline still requires a real
//     Dead. The two phases together force the detection to be protocol-driven.
//
// Runs in the DEFAULT test set (no build tag): the existing
// swim_integration_test.go full-lifecycle UDP test is gated behind
// `//go:build integration` and does NOT run under `go test ./pkg/swim/`. This
// file delivers that runtime end-to-end proof in the hermetic default suite, on
// the same real-UDP-over-loopback substrate the rest of the package's default
// tests already use (NewTransport("127.0.0.1", 0), Protocol.Start()).

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// rtFastConfig builds a Protocol Config with fast-but-realistic timers suitable
// for an in-process multi-member cluster. ProbeInterval/ProbeTimeout are small
// so the test runs quickly, but they remain real durations (not zero) so the
// real probe loop, ack wait, and suspicion timer all execute genuinely.
//
// SuspicionMult=3 with ProbeInterval=100ms => suspicion timeout = 300ms. The
// failure detector's timeout (NewFailureDetector) is SuspicionMult*ProbeInterval
// = 300ms, so a crashed peer goes Suspect on the first missed probe and Dead
// ~300ms later. Detection deadlines below are far larger than this to absorb
// gossip propagation and CI scheduling jitter without ever weakening the
// assertion.
func rtFastConfig(id string) *Config {
	return &Config{
		LocalID:        id,
		BindAddr:       "127.0.0.1",
		BindPort:       0, // OS assigns a free loopback port
		ProbeInterval:  100 * time.Millisecond,
		ProbeTimeout:   60 * time.Millisecond,
		SuspicionMult:  3,
		GossipCount:    3,
		GossipInterval: 50 * time.Millisecond,
	}
}

// rtMemberState returns p's current view of member id's state, or
// MemberState(-1) if p does not know that member. State is read under both the
// membership lock and the member's own lock to match the package's locking
// discipline (avoids racing State mutators that hold only m.mu).
func rtMemberState(p *Protocol, id string) MemberState {
	p.mu.RLock()
	m, ok := p.members[id]
	p.mu.RUnlock()
	if !ok {
		return MemberState(-1)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.State
}

// rtAliveView returns the sorted set of member IDs that p currently sees as
// StateAlive (including p itself).
func rtAliveView(p *Protocol) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	ids := make([]string, 0, len(p.members))
	for id, m := range p.members {
		m.mu.RLock()
		alive := m.State == StateAlive
		m.mu.RUnlock()
		if alive {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// rtHasAll reports whether haystack contains every needle.
func rtHasAll(haystack []string, needles ...string) bool {
	set := make(map[string]struct{}, len(haystack))
	for _, s := range haystack {
		set[s] = struct{}{}
	}
	for _, n := range needles {
		if _, ok := set[n]; !ok {
			return false
		}
	}
	return true
}

// rtPollUntil polls cond every interval until it returns true or the deadline
// elapses. Returns true if cond was satisfied. This is the non-flaky primitive:
// the test never sleeps a fixed amount and then asserts (which would race the
// timing-based protocol); it waits for the CONDITION up to a bounded deadline.
func rtPollUntil(deadline time.Duration, interval time.Duration, cond func() bool) bool {
	end := time.Now().Add(deadline)
	for {
		if cond() {
			return true
		}
		if time.Now().After(end) {
			return cond() // one final check at the boundary
		}
		time.Sleep(interval)
	}
}

// rtStartCluster creates and starts n real-UDP Protocol nodes named "n0".."n(n-1)"
// and joins them into a single cluster via node 0 as the seed (each later node
// also joins the previous one to accelerate gossip convergence). It returns the
// nodes and a stop func that tears down every still-running node. The caller is
// responsible for any per-node early shutdown (e.g. crashing one node).
func rtStartCluster(t *testing.T, n int) ([]*Protocol, func()) {
	t.Helper()
	nodes := make([]*Protocol, 0, n)
	for i := 0; i < n; i++ {
		p, err := NewProtocol(rtFastConfig(fmt.Sprintf("n%d", i)))
		if err != nil {
			t.Fatalf("NewProtocol(n%d): %v", i, err)
		}
		if err := p.Start(); err != nil {
			t.Fatalf("Start(n%d): %v", i, err)
		}
		nodes = append(nodes, p)
	}

	// Join the nodes into a single cluster with a full-mesh MsgJoin exchange:
	// every node sends a MsgJoin to every other node. This implementation's
	// Join/handleJoin/handleAlive handshake is what grows the membership map
	// (handleJoin records the joiner; the MsgAlive reply teaches the joiner about
	// the receiver) — gossip only disseminates subsequent CHANGE events, not the
	// initial membership roster, so a partial (seed-only) join does NOT make every
	// node learn every peer. A full-mesh join is the realistic way to seed an
	// all-to-all cluster on this transport and mirrors the package's own
	// SimNetwork parity test (driveFullMeshJoin).
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			if err := nodes[i].Join([]string{nodes[j].LocalAddr()}); err != nil {
				t.Fatalf("n%d.Join(n%d): %v", i, j, err)
			}
		}
	}

	stop := func() {
		for _, p := range nodes {
			// Stop is idempotent + safe after an early transport.Close().
			_ = p.Stop()
		}
	}
	return nodes, stop
}

// allIDs returns the IDs n0..n(count-1).
func allIDs(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("n%d", i)
	}
	return ids
}

// TestRuntime_SWIMDetectsCrashAndNoFalsePositive is the headline runtime proof.
//
//	Phase A: stand up a 5-node cluster on real UDP and wait for FULL convergence
//	         (every node sees all 5 as Alive).
//	Phase B: NO-FALSE-POSITIVE — run normally for many protocol periods and assert
//	         no survivor ever marks any healthy peer Suspect or Dead.
//	Phase C: crash ONE node by closing its transport (it stops answering pings);
//	         poll the survivors until each independently reaches "crashed node is
//	         Dead AND all other survivors still Alive" via the real protocol.
func TestRuntime_SWIMDetectsCrashAndNoFalsePositive(t *testing.T) {
	const n = 5
	nodes, stop := rtStartCluster(t, n)
	defer stop()

	ids := allIDs(n)

	// ---- Phase A: full convergence (every node sees all n alive) ----
	converged := rtPollUntil(15*time.Second, 50*time.Millisecond, func() bool {
		for _, p := range nodes {
			if !rtHasAll(rtAliveView(p), ids...) {
				return false
			}
		}
		return true
	})
	if !converged {
		for _, p := range nodes {
			t.Logf("convergence: %s alive-view = %v", p.LocalID(), rtAliveView(p))
		}
		t.Fatalf("cluster did not converge to all %d-alive within 15s", n)
	}
	for _, p := range nodes {
		t.Logf("converged: %s sees alive=%v", p.LocalID(), rtAliveView(p))
	}

	// ---- Phase B: NO-FALSE-POSITIVE under normal operation ----
	//
	// Run for many protocol periods (ProbeInterval=100ms => ~20 periods over 2s)
	// and assert that NO node ever marks any other healthy node Suspect or Dead.
	// This is the anti-bluff negative: a detector that marks peers dead without
	// evidence (or one with a too-aggressive suspicion timeout that mis-fires on
	// healthy peers) trips here. Healthy peers MUST stay Alive because real acks
	// keep refuting any transient suspicion within the suspicion window.
	const noFPPeriods = 20
	falsePositiveSeen := ""
	for period := 0; period < noFPPeriods; period++ {
		for _, observer := range nodes {
			for _, id := range ids {
				if id == observer.LocalID() {
					continue
				}
				st := rtMemberState(observer, id)
				if st == StateSuspect || st == StateDead {
					falsePositiveSeen = fmt.Sprintf(
						"period %d: %s falsely marked healthy %s as %v",
						period, observer.LocalID(), id, st)
				}
			}
		}
		if falsePositiveSeen != "" {
			break
		}
		// One protocol period.
		time.Sleep(100 * time.Millisecond)
	}
	if falsePositiveSeen != "" {
		t.Fatalf("FALSE POSITIVE during normal operation: %s", falsePositiveSeen)
	}
	t.Logf("no-false-positive: over %d protocol periods, no healthy member was ever marked suspect/dead", noFPPeriods)

	// ---- Phase C: crash ONE node and require protocol-driven detection ----
	//
	// Crash n4 by closing ONLY its transport. This is NOT a markDead API call on
	// the survivors — n4 simply stops answering pings, exactly like a host crash.
	// The survivors must DETECT this through real probe misses + the suspicion
	// timer. We do NOT Stop() n4 (Stop would gossip a graceful MsgLeave); we want
	// a hard, silent crash so failure DETECTION is exercised, not leave handling.
	crashed := nodes[n-1]
	crashedID := crashed.LocalID()
	survivors := nodes[:n-1]
	survivorIDs := allIDs(n - 1) // n0..n3

	if err := crashed.transport.Close(); err != nil {
		t.Fatalf("closing %s transport: %v", crashedID, err)
	}
	t.Logf("crashed %s (transport closed) — survivors must detect via protocol", crashedID)

	crashAt := time.Now()
	detected := rtPollUntil(20*time.Second, 50*time.Millisecond, func() bool {
		for _, s := range survivors {
			// Survivor must see the crashed node as Dead...
			if rtMemberState(s, crashedID) != StateDead {
				return false
			}
			// ...and must still see every OTHER survivor as Alive.
			for _, sid := range survivorIDs {
				if sid == s.LocalID() {
					continue
				}
				if rtMemberState(s, sid) != StateAlive {
					return false
				}
			}
		}
		return true
	})

	// Report per-survivor detection latency in protocol periods.
	period := 100 * time.Millisecond
	for _, s := range survivors {
		t.Logf("post-crash view of %s: sees %s=%v ; other survivors: %s",
			s.LocalID(), crashedID, rtMemberState(s, crashedID), rtSurvivorStates(s, survivorIDs))
	}
	if !detected {
		t.Fatalf("survivors did not converge to '%s Dead + others Alive' within 20s", crashedID)
	}
	elapsed := time.Since(crashAt)
	t.Logf("DETECTED: all %d survivors marked %s Dead (and kept each other Alive) after %v (~%d protocol periods)",
		len(survivors), crashedID, elapsed.Round(time.Millisecond), int(elapsed/period))

	// Final hard assertions (sink-side, per survivor) — explicit so a failure
	// names the exact offending node/state.
	for _, s := range survivors {
		if got := rtMemberState(s, crashedID); got != StateDead {
			t.Fatalf("%s sees crashed %s as %v, want StateDead", s.LocalID(), crashedID, got)
		}
		for _, sid := range survivorIDs {
			if sid == s.LocalID() {
				continue
			}
			if got := rtMemberState(s, sid); got != StateAlive {
				t.Fatalf("%s sees live survivor %s as %v, want StateAlive (false positive)", s.LocalID(), sid, got)
			}
		}
	}
}

// rtSurvivorStates renders an observer's view of a set of survivor IDs for logs.
func rtSurvivorStates(observer *Protocol, ids []string) string {
	out := ""
	for _, id := range ids {
		if id == observer.LocalID() {
			continue
		}
		out += fmt.Sprintf(" %s=%v", id, rtMemberState(observer, id))
	}
	return out
}

// TestRuntime_SWIMNoFalsePositiveSteadyState is a focused, standalone version of
// the anti-bluff negative: a healthy cluster, left entirely alone, must NEVER
// produce a Suspect or Dead verdict about any member over a sustained run of
// protocol periods. Kept separate from the headline test so the no-false-positive
// guarantee can be exercised (and rerun) in isolation.
//
// Why this is the load-bearing negative: SWIM failure detection is only
// meaningful if it does not cry wolf. A detector whose suspicion timeout is too
// short, whose ack-refute path is broken, or which marks peers dead without
// evidence, would mis-declare a perfectly healthy peer Suspect/Dead here. The
// headline crash test cannot catch that class of bug on its own — it WANTS a
// Dead verdict — so this steady-state phase is what proves the verdicts are
// earned, not reflexive.
func TestRuntime_SWIMNoFalsePositiveSteadyState(t *testing.T) {
	const n = 4
	nodes, stop := rtStartCluster(t, n)
	defer stop()

	ids := allIDs(n)

	// Converge first.
	if !rtPollUntil(15*time.Second, 50*time.Millisecond, func() bool {
		for _, p := range nodes {
			if !rtHasAll(rtAliveView(p), ids...) {
				return false
			}
		}
		return true
	}) {
		for _, p := range nodes {
			t.Logf("%s alive-view = %v", p.LocalID(), rtAliveView(p))
		}
		t.Fatalf("cluster did not converge to all %d-alive within 15s", n)
	}
	t.Logf("converged %d-node cluster; entering steady-state observation", n)

	// Observe for a sustained number of protocol periods. With ProbeInterval=100ms
	// this is ~3s of real probing/gossip — many full probe rounds per node.
	const periods = 30
	for period := 0; period < periods; period++ {
		for _, observer := range nodes {
			for _, id := range ids {
				if id == observer.LocalID() {
					continue
				}
				switch st := rtMemberState(observer, id); st {
				case StateSuspect:
					t.Fatalf("period %d: %s falsely SUSPECTED healthy %s", period, observer.LocalID(), id)
				case StateDead:
					t.Fatalf("period %d: %s falsely declared healthy %s DEAD", period, observer.LocalID(), id)
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("no false positives over %d protocol periods (~%v) across %d nodes",
		periods, time.Duration(periods)*100*time.Millisecond, n)
}
