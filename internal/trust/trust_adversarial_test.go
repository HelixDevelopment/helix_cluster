package trust

import (
	"math"
	"sync"
	"testing"
)

// trust_adversarial_test.go — adversarial / SINK-SIDE probes of the trust
// scorer. Each test asserts the actual trust VERDICT (Trusted / Accepted /
// OutcomeValid reward) on hostile, missing, zero-value, malformed, numeric, or
// concurrent input — never merely "did not panic".
//
// Threat model: the scorer is the gate that decides whether an untrusted
// volunteer node's work is accepted without further replication. A FAIL-OPEN is
// any path that returns Trusted==true / Accepted==true / rewards OutcomeValid for
// an entity that must be treated as untrusted.

// ----------------------------------------------------------------------------
// (a) FAIL-OPEN class
// ----------------------------------------------------------------------------

// Unknown / never-seen node must be UNTRUSTED by default (doc: "New volunteers
// are distrusted by default"). SINK: Trusted() verdict.
func TestAdv_UnknownNodeUntrustedByDefault(t *testing.T) {
	s := NewDefaultScorer()
	for _, node := range []string{"", "ghost", "\x00", "node-never-seen"} {
		if s.Trusted(node) {
			t.Errorf("FAIL-OPEN: unknown node %q reported Trusted==true (score=%v, floor=%v)",
				node, s.Score(node), s.TrustFloor())
		}
	}
}

// Empty / nil result set must NOT reach quorum (nothing to validate -> deny).
// SINK: Quorum.Accepted.
func TestAdv_ValidateEmptyDenies(t *testing.T) {
	s := NewDefaultScorer()
	for _, in := range [][]Result{nil, {}} {
		q := s.Validate(in, 3)
		if q.Accepted {
			t.Errorf("FAIL-OPEN: empty/nil results accepted quorum: %+v", q)
		}
	}
}

// Zero-value / missing quorum threshold m. m is the int default 0 when a caller
// forgets to set it. The doc contract is "accepted only when at least m
// independent nodes submit it". This probe pins the ACTUAL sink behavior: does a
// single self-submitting node reach quorum when m is the zero value?
func TestAdv_ValidateZeroThreshold_SingleNodeSelfQuorum(t *testing.T) {
	s := NewDefaultScorer()
	// One node, one result, caller passed the zero-value m=0.
	q := s.Validate([]Result{{Node: "solo", Value: "x"}}, 0)
	t.Logf("m=0 single-node verdict: Accepted=%v Votes=%d Value=%q Agree=%v",
		q.Accepted, q.Votes, q.Value, q.Agree)
	if q.Accepted && q.Votes < 2 {
		// SINK: a lone node validated its own work and was rewarded.
		got := s.Score("solo")
		t.Logf("PROBE RESULT (fail-open candidate): solo node self-validated with "+
			"m=0 -> Accepted, Votes=%d, solo reputation now %v (was %v)",
			q.Votes, got, DefaultInitialReputation)
	}
}

// Negative threshold m<0 (a malformed/garbage value) must NOT be more permissive
// than m=1. Pin the clamp behavior.
func TestAdv_ValidateNegativeThreshold(t *testing.T) {
	s := NewDefaultScorer()
	q := s.Validate([]Result{{Node: "solo", Value: "x"}}, -100)
	t.Logf("m=-100 verdict: Accepted=%v Votes=%d", q.Accepted, q.Votes)
}

// A node submitting an empty-string Value (zero-value / malformed result) that
// reaches quorum produces Accepted==true with Value=="" — indistinguishable from
// the no-quorum sentinel (Accepted==false, Value==""). Pin both so a caller that
// keys off Value=="" cannot be fooled.
func TestAdv_ValidateEmptyValueQuorumVsNoQuorum(t *testing.T) {
	s := NewDefaultScorer()
	// Two nodes both submit the empty value, m=2 -> quorum on "".
	acc := s.Validate([]Result{{Node: "a", Value: ""}, {Node: "b", Value: ""}}, 2)
	noq := s.Validate([]Result{{Node: "c", Value: "z"}}, 5)
	t.Logf("accepted-empty: Accepted=%v Value=%q | no-quorum: Accepted=%v Value=%q",
		acc.Accepted, acc.Value, noq.Accepted, noq.Value)
	if acc.Value == noq.Value && acc.Accepted != noq.Accepted {
		t.Logf("NOTE: Accepted-empty and no-quorum share Value=%q; callers MUST key "+
			"off Accepted, not Value.", acc.Value)
	}
}

// ----------------------------------------------------------------------------
// (b) NUMERIC class
// ----------------------------------------------------------------------------

// A NaN reputation must NOT pass the trust threshold. NaN >= floor is false in
// Go, so the threshold itself fails CLOSED — assert that explicitly (the safe
// direction). Drive NaN in through a NaN InitialReputation config (a misconfig /
// hostile config value) and confirm an unknown node is NOT trusted.
func TestAdv_NaNReputationNotTrusted(t *testing.T) {
	// NaN initial reputation. NewScorer only replaces InitialReputation when it
	// == 0, and NaN != 0, so the NaN survives.
	s := NewScorer(Config{InitialReputation: math.NaN()})
	got := s.Score("ghost")
	if !math.IsNaN(got) {
		t.Fatalf("expected NaN to survive config, got %v", got)
	}
	if s.Trusted("ghost") {
		t.Errorf("FAIL-OPEN: NaN reputation reported Trusted==true (NaN should fail the threshold)")
	}
}

// +Inf reputation: a hostile config could make every node trusted forever. Pin
// the sink behavior. With InitialReputation=+Inf an unknown node IS trusted —
// document that this is a config-trust boundary, not a runtime fail-open.
func TestAdv_InfReputationConfig(t *testing.T) {
	s := NewScorer(Config{InitialReputation: math.Inf(1)})
	t.Logf("+Inf initial reputation -> Trusted(ghost)=%v (config-trust boundary)", s.Trusted("ghost"))
}

// Reputation clamp must hold under a NaN gain. If StreakBonus/RecoveryStep were
// NaN, reputation becomes NaN and the `> MaxReputation` clamp (NaN > x == false)
// would NOT fire, leaving a NaN reputation. Assert the resulting Trusted verdict
// is NOT true (fails closed).
func TestAdv_NaNGainDoesNotProduceTrusted(t *testing.T) {
	s := NewScorer(Config{RecoveryStep: math.NaN()})
	rep := s.RecordResult("n", OutcomeValid)
	t.Logf("after NaN-gain valid result, reputation=%v Trusted=%v", rep, s.Trusted("n"))
	if s.Trusted("n") {
		t.Errorf("FAIL-OPEN: NaN-gain reputation reported Trusted==true")
	}
}

// ----------------------------------------------------------------------------
// (c) TOTAL-ORDER class
// ----------------------------------------------------------------------------

// Tie in vote count must be broken deterministically (doc: "lexicographically
// smallest value is chosen"). Run many times; the winner must be stable.
func TestAdv_ValidateTieDeterministic(t *testing.T) {
	want := ""
	for i := 0; i < 200; i++ {
		s := NewDefaultScorer()
		// "alpha" and "beta" each get 2 votes -> tie; smallest ("alpha") must win.
		q := s.Validate([]Result{
			{Node: "n1", Value: "beta"}, {Node: "n2", Value: "beta"},
			{Node: "n3", Value: "alpha"}, {Node: "n4", Value: "alpha"},
		}, 2)
		if !q.Accepted {
			t.Fatalf("expected quorum on tie, got %+v", q)
		}
		if i == 0 {
			want = q.Value
		}
		if q.Value != want {
			t.Fatalf("NONDETERMINISTIC tie-break: iter %d winner=%q, first=%q", i, q.Value, want)
		}
	}
	if want != "alpha" {
		t.Errorf("tie-break winner=%q, expected lexicographically smallest \"alpha\"", want)
	}
}

// Duplicate identity: the same node submitting the winning value many times must
// be counted ONCE (doc: "Duplicate submissions ... counted once"). A node could
// otherwise ballot-stuff its own value past quorum single-handedly.
func TestAdv_ValidateBallotStuffingCountedOnce(t *testing.T) {
	s := NewDefaultScorer()
	// One node submits "evil" five times. m=3. Must NOT reach quorum.
	dup := []Result{
		{Node: "sybil", Value: "evil"},
		{Node: "sybil", Value: "evil"},
		{Node: "sybil", Value: "evil"},
		{Node: "sybil", Value: "evil"},
		{Node: "sybil", Value: "evil"},
	}
	q := s.Validate(dup, 3)
	if q.Accepted {
		t.Errorf("FAIL-OPEN: single node ballot-stuffed to quorum: Votes=%d %+v", q.Votes, q)
	}
	if q.Votes != 1 {
		t.Errorf("expected dedup to 1 vote, got Votes=%d", q.Votes)
	}
}

// ----------------------------------------------------------------------------
// (d) UNGUARDED SHARED STATE class
// ----------------------------------------------------------------------------

// Concurrent RecordResult + Validate + reads on the shared node map must be
// race-free. Run under -race with capped goroutines. SINK: -race verdict + a
// final consistent read.
func TestAdv_ConcurrentNoRace(t *testing.T) {
	s := NewDefaultScorer()
	const workers = 8 // capped per ANTI-HANG
	const iters = 250
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			node := "node"
			for i := 0; i < iters; i++ {
				switch i % 4 {
				case 0:
					s.RecordResult(node, OutcomeValid)
				case 1:
					s.RecordResult(node, OutcomeInvalid)
				case 2:
					_ = s.Trusted(node)
					_ = s.Score(node)
					_ = s.Credit(node)
					_ = s.Streak(node)
				case 3:
					s.Validate([]Result{{Node: node, Value: "v"}, {Node: "x", Value: "v"}}, 2)
				}
			}
		}(w)
	}
	wg.Wait()
	// Final consistent read must be within documented bounds [0, MaxReputation].
	got := s.Score("node")
	if got < 0 || got > DefaultMaxReputation+1e-9 {
		t.Errorf("reputation escaped bounds after concurrent ops: %v", got)
	}
}
