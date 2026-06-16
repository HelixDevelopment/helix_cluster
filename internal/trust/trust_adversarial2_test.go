package trust

import (
	"math"
	"testing"
)

// trust_adversarial2_test.go — SECOND adversarial pass over the trust scorer.
//
// The first pass (trust_adversarial_test.go) established the threat model: the
// scorer is the gate deciding whether an untrusted volunteer node's work is
// accepted without further replication, and a FAIL-OPEN is any path returning
// Trusted==true / Accepted==true / an OutcomeValid reward for an entity that
// must be treated as untrusted.
//
// This pass goes DEEPER and converts several behaviours the first pass only
// *logged* (t.Logf, no assertion) into HARD sink-side assertions that pin the
// security-relevant contract, plus probes the first pass missed:
//
//   - m=0 self-quorum: a forgotten/zero threshold must NEVER let a lone node's
//     self-submitted result become Trusted (documented contract: it is accepted
//     and lightly rewarded, but stays far below the floor — pin BOTH facts).
//   - the documented "agreer even among other submissions" hedging rule is pinned
//     so any future change is a conscious decision, AND we assert the hard
//     security invariant it must never cross: hedging must never push the hedger
//     to Trusted off a single work unit.
//   - NaN must fail CLOSED on every path (reward, penalty, threshold, Validate).
//   - the trust verdict is strictly monotone in reputation and the floor is a
//     `>=` boundary (a node exactly at the floor is trusted; one epsilon below is
//     not) — an inverted/`>`-vs-`>=` comparison would flip these.

// ----------------------------------------------------------------------------
// (a) m=0 / threshold-clamp FAIL-OPEN class — HARD assertions
// ----------------------------------------------------------------------------

// A lone node submitting a single self-result with the zero-value threshold
// (m=0, the int default a caller gets when it forgets to set m) is accepted by
// the documented clamp (m<1 -> m=1). That is the documented contract — but the
// load-bearing SECURITY invariant is that self-validation must NOT make the node
// Trusted. Pin both: Accepted==true (contract) AND Trusted==false (gate holds).
//
// Mutation that this catches: if the clamp were removed so m=0 meant "0 agreeing
// nodes required", an EMPTY result set would reach quorum, or a self-result would
// jump a node over the floor — either flips an assertion here.
func TestAdv2_ZeroThresholdSelfQuorumNeverTrusted(t *testing.T) {
	s := NewDefaultScorer()
	q := s.Validate([]Result{{Node: "solo", Value: "x"}}, 0)

	if !q.Accepted {
		t.Fatalf("documented clamp contract changed: m=0 single result Accepted=%v (was true)", q.Accepted)
	}
	if q.Votes != 1 {
		t.Errorf("m=0 self-quorum votes = %d, want 1 (lone node)", q.Votes)
	}
	// THE GATE: a node that only validated its own work must remain untrusted.
	if s.Trusted("solo") {
		t.Errorf("FAIL-OPEN: lone self-validating node (m=0) reported Trusted==true (score=%v, floor=%v)",
			s.Score("solo"), s.TrustFloor())
	}
	if got := s.Score("solo"); got >= s.TrustFloor() {
		t.Errorf("FAIL-OPEN: self-validation lifted score to %v >= floor %v on ONE work unit", got, s.TrustFloor())
	}
}

// Empty/nil results with the zero-value threshold m=0 must STILL deny: after the
// clamp m=1, but there are zero votes, so winVotes(0) < m(1) -> not accepted, and
// no node may be created/rewarded. The first pass only checked m=3; pin m=0.
func TestAdv2_ZeroThresholdEmptyStillDenies(t *testing.T) {
	s := NewDefaultScorer()
	for _, in := range [][]Result{nil, {}} {
		q := s.Validate(in, 0)
		if q.Accepted {
			t.Errorf("FAIL-OPEN: empty results with m=0 accepted quorum: %+v", q)
		}
		if q.Votes != 0 {
			t.Errorf("empty results votes = %d, want 0", q.Votes)
		}
	}
}

// ----------------------------------------------------------------------------
// (b) Hedging / trust-laundering class — pin documented rule + hard invariant
// ----------------------------------------------------------------------------

// DOCUMENTED CONTRACT pin: scorer.go lines 323-325 state a node that submitted
// the winning value "even among other submissions" counts as an agreer. So a
// node that hedges (submits the correct value AND a wrong value) is rewarded, not
// penalized. This test pins that exact classification so any future change is a
// deliberate, reviewed decision — AND asserts the security ceiling: one hedged
// work unit must not make the hedger Trusted.
func TestAdv2_HedgerClassifiedAsAgreer_PinnedButNotTrusted(t *testing.T) {
	s := NewDefaultScorer()
	scoreBefore := s.Score("hed")
	q := s.Validate([]Result{
		{Node: "h1", Value: "correct"},
		{Node: "h2", Value: "correct"},
		{Node: "hed", Value: "correct"}, // hedge: also submits the winner
		{Node: "hed", Value: "wrong"},   //        plus a known-wrong value
	}, 2)

	if !q.Accepted || q.Value != "correct" {
		t.Fatalf("precondition: quorum on \"correct\" expected, got Accepted=%v Value=%q", q.Accepted, q.Value)
	}
	// Pin documented classification: hedger is an agreer, not a dissenter.
	foundAgree := false
	for _, n := range q.Agree {
		if n == "hed" {
			foundAgree = true
		}
	}
	for _, n := range q.Dissent {
		if n == "hed" {
			t.Errorf("classification changed: hedger now in Dissent (was documented agreer) %v", q.Dissent)
		}
	}
	if !foundAgree {
		t.Errorf("documented contract changed: hedger no longer in Agree=%v", q.Agree)
	}
	// HARD security invariant regardless of classification: a single work unit of
	// hedging must not vault the hedger over the trust floor.
	if s.Trusted("hed") {
		t.Errorf("FAIL-OPEN: hedger became Trusted off ONE work unit (score=%v, floor=%v, before=%v)",
			s.Score("hed"), s.TrustFloor(), scoreBefore)
	}
}

// A node that submits ONLY non-winning values (a pure dissenter spreading several
// wrong answers) must be penalized once, never rewarded. Pin sink: its score
// strictly drops and it is classified as a dissenter exactly once.
func TestAdv2_PureMultiWrongDissenterPenalizedOnce(t *testing.T) {
	s := NewDefaultScorer()
	// Build the dissenter a reputation to lose.
	for i := 0; i < 5; i++ {
		s.RecordResult("liar", OutcomeValid)
	}
	before := s.Score("liar") // 0.90

	q := s.Validate([]Result{
		{Node: "a", Value: "truth"},
		{Node: "b", Value: "truth"},
		{Node: "liar", Value: "w1"},
		{Node: "liar", Value: "w2"},
	}, 2)
	if !q.Accepted {
		t.Fatalf("precondition: quorum on truth expected")
	}
	dissentCount := 0
	for _, n := range q.Dissent {
		if n == "liar" {
			dissentCount++
		}
	}
	if dissentCount != 1 {
		t.Errorf("multi-wrong node must be classified as dissenter exactly once, got %d (Dissent=%v)", dissentCount, q.Dissent)
	}
	after := s.Score("liar")
	if !(after < before) {
		t.Errorf("multi-wrong dissenter trust must DECREASE: before %v after %v", before, after)
	}
	// Penalized exactly once (single multiplicative+floor drop), not per wrong value.
	if math.Abs(after-(before*DefaultPenaltyFactor-DefaultPenaltyFloorDrop)) >= 1e-9 {
		t.Errorf("dissenter penalized more/less than once: after=%v want single-penalty=%v",
			after, before*DefaultPenaltyFactor-DefaultPenaltyFloorDrop)
	}
}

// ----------------------------------------------------------------------------
// (c) NaN fail-closed on EVERY path
// ----------------------------------------------------------------------------

// NaN must fail closed through the PENALTY path too (the first pass only proved
// the reward/threshold paths). A NaN reputation hit by OutcomeInvalid stays NaN
// (NaN*x-y == NaN, and `NaN < 0` is false so the zero-clamp doesn't fire), and
// NaN >= floor is false, so Trusted stays false. Pin it.
func TestAdv2_NaNPenaltyPathStaysUntrusted(t *testing.T) {
	s := NewScorer(Config{InitialReputation: math.NaN()})
	rep := s.RecordResult("n", OutcomeInvalid)
	if !math.IsNaN(rep) {
		t.Logf("NaN did not survive penalty path (rep=%v) — still acceptable if Trusted stays false", rep)
	}
	if s.Trusted("n") {
		t.Errorf("FAIL-OPEN: NaN reputation through penalty path reported Trusted==true")
	}
}

// A NaN trust FLOOR (hostile/misconfigured config) must not make every node
// trusted: `anything >= NaN` is false in Go, so Trusted fails CLOSED for all
// nodes. Pin this — an inverted comparison (`<=`) would flip it open.
func TestAdv2_NaNFloorFailsClosed(t *testing.T) {
	s := NewScorer(Config{TrustFloor: math.NaN()})
	// A normally-trusted high-reputation node must NOT be trusted against a NaN floor.
	for i := 0; i < 10; i++ {
		s.RecordResult("hi", OutcomeValid)
	}
	if s.Trusted("hi") {
		t.Errorf("FAIL-OPEN: NaN trust floor reported Trusted==true (score=%v)", s.Score("hi"))
	}
}

// ----------------------------------------------------------------------------
// (d) Trust-verdict ordering: total, monotone, `>=` boundary
// ----------------------------------------------------------------------------

// The trust verdict must be a correct threshold on reputation: a node exactly AT
// the floor is Trusted (>=), and a node one epsilon BELOW is not. A `>` instead
// of `>=`, or an inverted comparison, flips one of these. Drive the floor to an
// exact value and set reputation precisely via config so the boundary is exact.
func TestAdv2_TrustFloorIsInclusiveBoundary(t *testing.T) {
	// InitialReputation exactly equals the floor -> unknown node sits AT floor.
	floor := 0.50
	s := NewScorer(Config{InitialReputation: floor, TrustFloor: floor})
	if !s.Trusted("at") {
		t.Errorf("node exactly AT floor (%v) must be Trusted (>= boundary)", floor)
	}

	// A node just below the floor must NOT be trusted.
	below := math.Nextafter(floor, 0) // largest float < floor
	s2 := NewScorer(Config{InitialReputation: below, TrustFloor: floor})
	if s2.Trusted("below") {
		t.Errorf("FAIL-OPEN: node below floor (%v < %v) reported Trusted==true", below, floor)
	}
}

// Trust is monotone in reputation: building a streak only ever moves a node from
// untrusted toward trusted, never the reverse, and once a bad result drops it
// below the floor the verdict flips back to untrusted. Pin the directionality so
// an inverted Trusted() (returning `score < floor`) is caught.
func TestAdv2_TrustVerdictMonotoneWithReputation(t *testing.T) {
	s := NewDefaultScorer()
	if s.Trusted("m") {
		t.Fatalf("fresh node must start untrusted")
	}
	// Climb to trusted.
	var crossed bool
	for i := 0; i < 6; i++ {
		s.RecordResult("m", OutcomeValid)
		if s.Trusted("m") {
			crossed = true
			break
		}
	}
	if !crossed {
		t.Fatalf("a sustained valid streak must eventually cross the floor (score=%v)", s.Score("m"))
	}
	if s.Score("m") < s.TrustFloor() {
		t.Errorf("inverted verdict: Trusted==true but score %v < floor %v", s.Score("m"), s.TrustFloor())
	}
	// One bad result must flip it back to untrusted (sharp drop below floor).
	s.RecordResult("m", OutcomeInvalid)
	if s.Trusted("m") && s.Score("m") < s.TrustFloor() {
		t.Errorf("inverted verdict after drop: Trusted==true but score %v < floor %v", s.Score("m"), s.TrustFloor())
	}
	if s.Score("m") >= s.TrustFloor() {
		t.Errorf("expected a single bad result to drop a freshly-trusted node below the floor, score=%v", s.Score("m"))
	}
}
