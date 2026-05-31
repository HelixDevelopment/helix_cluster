package trust

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestNewNodeStartsBelowFloor proves a never-seen volunteer is distrusted by
// default: its initial reputation is the configured initial value and it sits
// below the trust floor.
//
// Mutation: set InitialReputation default to 0.60 (>= floor). Trusted() returns
// true and this assertion fails.
func TestNewNodeStartsBelowFloor(t *testing.T) {
	s := NewDefaultScorer()
	if got := s.Score("fresh"); !approx(got, DefaultInitialReputation) {
		t.Fatalf("initial score = %v, want %v", got, DefaultInitialReputation)
	}
	if s.Trusted("fresh") {
		t.Errorf("fresh node must not be trusted (score %v < floor %v)", s.Score("fresh"), s.TrustFloor())
	}
}

// TestValidStreakRaisesScoreExact pins the exact reputation after a known streak
// so the additive recovery + streak bonus math is verified, not just "it went
// up". With defaults:
//
//	start 0.10
//	+1: 0.10 + 0.10 + 0.02*1 = 0.22
//	+2: 0.22 + 0.10 + 0.02*2 = 0.36
//	+3: 0.36 + 0.10 + 0.02*3 = 0.52
//	+4: 0.52 + 0.10 + 0.02*4 = 0.70
//	+5: 0.70 + 0.10 + 0.02*5 = 0.90
//
// Mutation: drop the StreakBonus term (gain := RecoveryStep only). The score
// after 5 valids becomes 0.60, not 0.90, and the exact assertion fails. Also,
// a scorer that ignores valid outcomes (no-op reward) leaves it at 0.10.
func TestValidStreakRaisesScoreExact(t *testing.T) {
	s := NewDefaultScorer()
	var last float64
	for i := 0; i < 5; i++ {
		last = s.RecordResult("worker", OutcomeValid)
	}
	if !approx(last, 0.90) {
		t.Fatalf("score after 5-valid streak = %v, want 0.90", last)
	}
	if s.Streak("worker") != 5 {
		t.Errorf("streak = %d, want 5", s.Streak("worker"))
	}
	if s.Credit("worker") != 5 {
		t.Errorf("credit = %d, want 5", s.Credit("worker"))
	}
	if !s.Trusted("worker") {
		t.Errorf("node with 0.90 reputation must be trusted")
	}
}

// TestBadResultDropsScoreSharply is the anti-bluff core: a long validated streak
// builds a high score, then a SINGLE bad result must drop it sharply. We assert
// the exact before and after.
//
// After 5 valids: 0.90 (see above). One OutcomeInvalid:
//
//	0.90*PenaltyFactor(0.25) - PenaltyFloorDrop(0.05) = 0.225 - 0.05 = 0.175
//
// So the score must fall from 0.90 to 0.175 and the node drops below the floor.
//
// Mutation: make RecordResult ignore non-valid outcomes (the "never penalizes"
// scorer named in the task). The after-score stays 0.90 and BOTH the sharp-drop
// assertion and the below-floor assertion fail. Equally, removing the
// PenaltyFloorDrop changes 0.175 -> 0.225 and the exact assertion fails.
func TestBadResultDropsScoreSharply(t *testing.T) {
	s := NewDefaultScorer()
	for i := 0; i < 5; i++ {
		s.RecordResult("worker", OutcomeValid)
	}
	before := s.Score("worker")
	if !approx(before, 0.90) {
		t.Fatalf("precondition before = %v, want 0.90", before)
	}

	after := s.RecordResult("worker", OutcomeInvalid)
	if !approx(after, 0.175) {
		t.Fatalf("score after single bad result = %v, want 0.175", after)
	}
	if !(after < before*0.5) {
		t.Errorf("bad result must cut score by more than half: before %v after %v", before, after)
	}
	if s.Trusted("worker") {
		t.Errorf("node must drop below trust floor after bad result (score %v, floor %v)", after, s.TrustFloor())
	}
	if s.Streak("worker") != 0 {
		t.Errorf("streak must reset to 0 after bad result, got %d", s.Streak("worker"))
	}
}

// TestTimeoutAndAbandonAlsoPenalize proves the penalty path is not invalid-only:
// timeouts and abandonments erode trust identically.
//
// Mutation: in Outcome.good() return `o == OutcomeValid || o == OutcomeTimeout`.
// The timeout branch then rewards instead of penalizing and this fails.
func TestTimeoutAndAbandonAlsoPenalize(t *testing.T) {
	for _, oc := range []Outcome{OutcomeTimeout, OutcomeAbandoned} {
		s := NewDefaultScorer()
		for i := 0; i < 5; i++ {
			s.RecordResult("w", OutcomeValid)
		}
		before := s.Score("w")
		after := s.RecordResult("w", oc)
		if !(after < before) {
			t.Errorf("outcome %v must reduce score: before %v after %v", oc, before, after)
		}
		if !approx(after, 0.175) {
			t.Errorf("outcome %v after = %v, want 0.175", oc, after)
		}
	}
}

// TestRecoveryIsAsymmetric proves a node cannot undo a bad result with a single
// good one: after the drop to 0.175, ONE valid result is not enough to clear the
// floor; it takes several. This pins the asymmetry the spec demands.
//
// After bad: 0.175, streak reset to 0.
//
//	+1: 0.175 + 0.10 + 0.02*1 = 0.295  (still < 0.50 floor)
//	+2: 0.295 + 0.10 + 0.02*2 = 0.435  (still < floor)
//	+3: 0.435 + 0.10 + 0.02*3 = 0.595  (>= floor)
//
// So recovery to trusted takes 3 consecutive valids, not 1.
//
// Mutation: change the penalty path to additive (-0.10) instead of the
// multiplicative+floor drop, OR raise RecoveryStep to 0.40. Either makes a
// single good result restore trust and the "not trusted after one" assertions
// fail.
func TestRecoveryIsAsymmetric(t *testing.T) {
	s := NewDefaultScorer()
	for i := 0; i < 5; i++ {
		s.RecordResult("w", OutcomeValid)
	}
	s.RecordResult("w", OutcomeInvalid) // -> 0.175

	r1 := s.RecordResult("w", OutcomeValid)
	if !approx(r1, 0.295) {
		t.Fatalf("after +1 recovery = %v, want 0.295", r1)
	}
	if s.Trusted("w") {
		t.Errorf("one good result must NOT restore trust (score %v)", r1)
	}

	r2 := s.RecordResult("w", OutcomeValid)
	if s.Trusted("w") {
		t.Errorf("two good results must NOT restore trust (score %v)", r2)
	}

	r3 := s.RecordResult("w", OutcomeValid)
	if !approx(r3, 0.595) {
		t.Fatalf("after +3 recovery = %v, want 0.595", r3)
	}
	if !s.Trusted("w") {
		t.Errorf("three good results should restore trust (score %v >= floor %v)", r3, s.TrustFloor())
	}
}

// TestValidateQuorumMajorityWinsAndPenalizesDissenter is the replication core:
// 3 nodes, 2 agree on "0xCAFE" and 1 dissents with "0xBEEF". With m=2 quorum is
// reached, the majority value is returned, the dissenter is flagged, and its
// trust DECREASES (sink-side), while agreers' trust increases.
//
// Mutation: make Validate skip the `s.RecordResult(n, OutcomeInvalid)` loop over
// dissenters (the "never penalizes dissenter" bug). The dissenter's score then
// stays at its pre-validate value and the strict-decrease assertion fails.
func TestValidateQuorumMajorityWinsAndPenalizesDissenter(t *testing.T) {
	s := NewDefaultScorer()
	// Give the dissenter a real reputation to lose so the drop is observable.
	for i := 0; i < 5; i++ {
		s.RecordResult("c", OutcomeValid)
	}
	dissenterBefore := s.Score("c") // 0.90
	agreerBefore := s.Score("a")    // initial 0.10 (unseen)

	q := s.Validate([]Result{
		{Node: "a", Value: "0xCAFE"},
		{Node: "b", Value: "0xCAFE"},
		{Node: "c", Value: "0xBEEF"},
	}, 2)

	if !q.Accepted {
		t.Fatalf("quorum of 2/3 must be accepted")
	}
	if q.Value != "0xCAFE" {
		t.Errorf("agreed value = %q, want 0xCAFE", q.Value)
	}
	if q.Votes != 2 {
		t.Errorf("votes = %d, want 2", q.Votes)
	}
	if len(q.Dissent) != 1 || q.Dissent[0] != "c" {
		t.Errorf("dissent = %v, want [c]", q.Dissent)
	}

	if got := s.Score("c"); !(got < dissenterBefore) {
		t.Errorf("dissenter trust must DECREASE: before %v after %v", dissenterBefore, got)
	}
	if !approx(s.Score("c"), 0.175) {
		t.Errorf("dissenter after = %v, want 0.175", s.Score("c"))
	}
	if got := s.Score("a"); !(got > agreerBefore) {
		t.Errorf("agreer trust must INCREASE: before %v after %v", agreerBefore, got)
	}
}

// TestValidateNoQuorumLeavesTrustUntouched proves that when no value reaches m
// agreeing nodes (3 distinct answers, m=2) nothing is accepted and NO trust is
// adjusted — we cannot attribute blame without a majority.
//
// Mutation: change the `winVotes < m` guard to `winVotes < 1` (accept any
// plurality). Then a 1-vote winner would be accepted, RecordResult fires, and
// the "scores unchanged" assertions fail.
func TestValidateNoQuorumLeavesTrustUntouched(t *testing.T) {
	s := NewDefaultScorer()
	a, b, c := s.Score("a"), s.Score("b"), s.Score("c")

	q := s.Validate([]Result{
		{Node: "a", Value: "X"},
		{Node: "b", Value: "Y"},
		{Node: "c", Value: "Z"},
	}, 2)

	if q.Accepted {
		t.Fatalf("no value has 2 votes; quorum must NOT be accepted")
	}
	if q.Value != "" {
		t.Errorf("rejected quorum value = %q, want empty", q.Value)
	}
	if s.Score("a") != a || s.Score("b") != b || s.Score("c") != c {
		t.Errorf("no-quorum validation must not alter trust: a %v b %v c %v", s.Score("a"), s.Score("b"), s.Score("c"))
	}
}

// TestValidateTieBrokenDeterministically proves a vote-count tie resolves to the
// lexicographically smallest value, so replication is reproducible across runs.
// "AAA" and "BBB" each get 2 votes; m=2; "AAA" must win.
//
// Mutation: change the winner loop comparison from `n > winVotes` to
// `n >= winVotes`. Then the last-iterated value ("BBB", since values are sorted
// ascending) overwrites the tie and wins, flipping the result to "BBB".
func TestValidateTieBrokenDeterministically(t *testing.T) {
	s := NewDefaultScorer()
	q := s.Validate([]Result{
		{Node: "n1", Value: "BBB"},
		{Node: "n2", Value: "BBB"},
		{Node: "n3", Value: "AAA"},
		{Node: "n4", Value: "AAA"},
	}, 2)
	if !q.Accepted {
		t.Fatalf("2-vote tie meets m=2 quorum")
	}
	if q.Value != "AAA" {
		t.Errorf("tie winner = %q, want AAA (lexicographically smallest)", q.Value)
	}
}

// TestValidateDedupSameNode proves repeated submissions from one node for the
// same value are counted once, so a node cannot manufacture quorum by spamming.
// Node "a" submits "V" three times, node "b" submits "W" once; m=2. "V" has only
// 1 distinct node -> no quorum.
//
// Mutation: count raw submissions instead of distinct nodes (drop the per-node
// dedup `seen` guard). "V" would tally 3 votes, falsely reach quorum, and this
// fails.
func TestValidateDedupSameNode(t *testing.T) {
	s := NewDefaultScorer()
	q := s.Validate([]Result{
		{Node: "a", Value: "V"},
		{Node: "a", Value: "V"},
		{Node: "a", Value: "V"},
		{Node: "b", Value: "W"},
	}, 2)
	if q.Accepted {
		t.Errorf("single node cannot self-quorum; accepted=%v value=%q votes=%d", q.Accepted, q.Value, q.Votes)
	}
}

// TestReputationCappedAtMax proves a long valid streak cannot push reputation
// past the configured cap, so a single node never becomes blindly trusted.
//
// Mutation: remove the `if st.reputation > MaxReputation` clamp. After 100
// valids the score blows past 0.99 and this fails.
func TestReputationCappedAtMax(t *testing.T) {
	s := NewDefaultScorer()
	var last float64
	for i := 0; i < 100; i++ {
		last = s.RecordResult("grinder", OutcomeValid)
	}
	if last > DefaultMaxReputation+1e-9 {
		t.Errorf("reputation %v exceeded cap %v", last, DefaultMaxReputation)
	}
	if !approx(last, DefaultMaxReputation) {
		t.Errorf("sustained streak should pin to cap %v, got %v", DefaultMaxReputation, last)
	}
}

// TestReputationFloorAtZero proves repeated bad results cannot drive reputation
// negative.
//
// Mutation: remove the `if st.reputation < 0` clamp in the penalty path. After
// many bad results from a low base the score goes negative and this fails.
func TestReputationFloorAtZero(t *testing.T) {
	s := NewDefaultScorer()
	var last float64
	for i := 0; i < 20; i++ {
		last = s.RecordResult("badnode", OutcomeInvalid)
	}
	if last < 0 {
		t.Errorf("reputation must not go negative, got %v", last)
	}
}
