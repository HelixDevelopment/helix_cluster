package constraints

// Adversarial pass 2 for pkg/constraints — score-overflow SELECTION/SATISFIABILITY probes.
//
// Prior pass classified "near-MaxInt score+bonus int overflow" as a LATENT RISK
// that only produced a CLEAN ErrUnsatisfiable. This pass probed the overflow's
// downstream effect on the actual SELECTION decision and the returned placement,
// and found a strictly worse, previously-unpinned failure mode:
//
//   REAL DEFECT (sink-side correctness inversion): with a plain `score += ...`
//   accumulation, a near-MaxInt score sum WRAPS to a large NEGATIVE value. That
//   does NOT always surface as a typed error — in the no-colocation case it
//   SILENTLY returns err=nil with the WRONG node: a node the caller STRONGLY
//   preferred (highest intended score) LOSES to a node it weakly preferred,
//   directly inverting the documented contract "Pick the highest score". A
//   silent wrong placement is worse than the clean ErrUnsatisfiable the prior
//   pass pinned, because a caller cannot detect it.
//
// Reachability: the wrap requires a node's score SUM to approach math.MaxInt
// (a near-MaxInt caller Score, or a homegrown-INFINITY sentinel summed with the
// +colocationBonus / +Stickiness terms). It is input-domain-dependent, not
// always-on for ordinary Pacemaker-magnitude scores — but the failure when it
// triggers is a silent misadmit, not a recoverable error, so it is hardened
// rather than merely pinned.
//
// FIX (engine.go): saturatingAdd clamps every score accumulation to the int
// range instead of wrapping. For any non-overflowing sum it is byte-identical to
// `+` (proven by the rest of the suite staying green); at the MaxInt boundary it
// keeps "highest score wins" true across the entire int domain.
//
// Every test below asserts SINK-SIDE: the actual chosen node and/or the actual
// error identity — never just "did not panic". Each is mutation-designed: revert
// saturatingAdd to a plain `a + b` and the marked tests FAIL.

import (
	"math"
	"testing"
)

const adv2UUID = "HXC-constraints-adversarial2-3e7c11a9"

// ---------------------------------------------------------------------------
// (1) The score helper in isolation. saturatingAdd must clamp, not wrap.
// MUTATION TARGET: `return a + b` instead of clamping fails the MaxInt cases.
// ---------------------------------------------------------------------------

func TestNumeric_SaturatingAdd_ClampsNoWrap(t *testing.T) {
	cases := []struct {
		name string
		a, b int
		want int
	}{
		{"ordinary", 5, 7, 12},
		{"ordinary-neg", -5, 2, -3},
		{"zero", 0, math.MaxInt, math.MaxInt},
		{"pos-overflow", math.MaxInt, 1, math.MaxInt},
		{"pos-overflow-bonus", math.MaxInt, colocationBonus, math.MaxInt},
		{"pos-overflow-half", math.MaxInt/2 + 100, math.MaxInt/2 + 100, math.MaxInt},
		{"neg-overflow", math.MinInt, -1, math.MinInt},
		{"neg-overflow-veto", math.MinInt, vetoScore, math.MinInt},
		{"boundary-exact-max", math.MaxInt - 3, 3, math.MaxInt},
		{"boundary-exact-min", math.MinInt + 3, -3, math.MinInt},
	}
	for _, tc := range cases {
		if got := saturatingAdd(tc.a, tc.b); got != tc.want {
			t.Fatalf("[%s] saturatingAdd(%d,%d)=%d, want %d (wrap not clamped) [%s]",
				adv2UUID, tc.a, tc.b, got, tc.want, tc.name)
		}
		// Commutativity sanity (the helper is order-independent on these inputs).
		if got := saturatingAdd(tc.b, tc.a); got != tc.want {
			t.Fatalf("[%s] saturatingAdd not commutative on (%d,%d): %d vs %d [%s]",
				adv2UUID, tc.a, tc.b, got, tc.want, tc.name)
		}
	}
	t.Logf("[%s] saturatingAdd clamps at both int bounds; identical to + on non-overflowing sums", adv2UUID)
}

// ---------------------------------------------------------------------------
// (2) HEADLINE: silent WRONG-NODE pick from overflow, NO colocation involved.
// The caller expresses "strongly prefer n1" with two big Prefer scores that SUM
// past MaxInt. Correct contract: highest score wins -> n1. With plain += the sum
// wraps negative and n2 (weakly preferred, score 5) silently wins with err=nil.
//
// This is the load-bearing repro: it returns a placement (not an error), so it
// catches the silent misadmit the prior clean-error pin missed.
//
// MUTATION: revert saturatingAdd -> `a + b`. b lands on n2, this test FAILS.
// ---------------------------------------------------------------------------

func TestNumeric_OverflowSilentWrongPick_NoColocation(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "b"}},
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	res, err := e.Solve(ConstraintSet{
		Locations: []Location{
			// Two big Prefers on n1 whose SUM exceeds MaxInt -> would wrap negative.
			{Resource: "b", Node: "n1", Affinity: Prefer, Score: math.MaxInt/2 + 100},
			{Resource: "b", Node: "n1", Affinity: Prefer, Score: math.MaxInt/2 + 100},
			// n2 is only weakly preferred.
			{Resource: "b", Node: "n2", Affinity: Prefer, Score: 5},
		},
	})
	if err != nil {
		t.Fatalf("[%s] unexpected error placing a single resource: %v", adv2UUID, err)
	}
	if res.Placement["b"] != "n1" {
		t.Fatalf("[%s] OVERFLOW SILENT WRONG-PICK: b=%q, want n1 "+
			"(caller strongly preferred n1; a wrapped negative sum let weakly-preferred n2 win)",
			adv2UUID, res.Placement["b"])
	}
	t.Logf("[%s] saturating score keeps 'highest score wins': b on n1 despite a MaxInt-overflowing Prefer sum", adv2UUID)
}

// ---------------------------------------------------------------------------
// (3) Overflow via Stickiness + Prefer accumulation flips a re-score (still no
// colocation). b currently sits on n1 with huge stickiness AND a huge Prefer for
// n1; together they exceed MaxInt. The resource should STAY on n1; a wrap lets
// n2 steal it.
//
// MUTATION: plain `a + b` -> b migrates to n2, this test FAILS.
// ---------------------------------------------------------------------------

func TestNumeric_OverflowStickinessFlipsRescore(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "b", Current: "n1", Stickiness: math.MaxInt/2 + 50}},
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	res, err := e.Solve(ConstraintSet{
		Locations: []Location{
			{Resource: "b", Node: "n1", Affinity: Prefer, Score: math.MaxInt/2 + 50},
			{Resource: "b", Node: "n2", Affinity: Prefer, Score: 5},
		},
	})
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", adv2UUID, err)
	}
	if res.Placement["b"] != "n1" {
		t.Fatalf("[%s] OVERFLOW STICKINESS FLIP: b=%q, want n1 "+
			"(huge stickiness+Prefer on its current node must keep it there; wrap let n2 win)",
			adv2UUID, res.Placement["b"])
	}
	t.Logf("[%s] saturating sum keeps b pinned to its sticky current node n1 under near-MaxInt magnitudes", adv2UUID)
}

// ---------------------------------------------------------------------------
// (4) Overflow that WOULD split a satisfiable Together pair into a FALSE
// ErrUnsatisfiable (the prior pass's pinned route) is now resolved correctly.
// b wants its partner a's node n1 with a MaxInt Prefer AND is Together with a;
// the +colocationBonus on n1 used to push the already-MaxInt sum into a negative
// wrap, dropping n1 below n2 and splitting the pair. It must now co-locate.
//
// MUTATION: plain `a + b` -> Solve returns ErrUnsatisfiable, this test FAILS.
// ---------------------------------------------------------------------------

func TestNumeric_OverflowFalseUnsatisfiable_NowCoLocates(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "a"}, {ID: "b"}}, // a placed first onto n1
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	res, err := e.Solve(ConstraintSet{
		Locations: []Location{
			{Resource: "a", Node: "n1", Affinity: Required},
			{Resource: "b", Node: "n1", Affinity: Prefer, Score: math.MaxInt},
			{Resource: "b", Node: "n2", Affinity: Prefer, Score: 10},
		},
		Colocations: []Colocation{{First: "a", Second: "b", Together: true}},
	})
	if err != nil {
		t.Fatalf("[%s] FALSE UNSATISFIABLE: satisfiable Together pair errored: %v "+
			"(MaxInt Prefer + colocationBonus on n1 wrapped negative and split the pair)", adv2UUID, err)
	}
	if res.Placement["a"] != "n1" || res.Placement["b"] != "n1" {
		t.Fatalf("[%s] Together pair not co-located on n1: a=%q b=%q",
			adv2UUID, res.Placement["a"], res.Placement["b"])
	}
	t.Logf("[%s] saturating sum co-locates a,b on n1 at Score=MaxInt (no wrap-driven false-unsat split)", adv2UUID)
}

// ---------------------------------------------------------------------------
// (5) GENUINELY-unsatisfiable sets must STILL report ErrUnsatisfiable — the fix
// must not turn the saturating clamp into a fail-open that admits a forbidden
// placement. Anti-affinity on the only shared admissible node, plus a MaxInt
// Prefer, must NOT let b sneak onto a's node.
// ---------------------------------------------------------------------------

func TestNumeric_SaturatingDoesNotFailOpen_AntiAffinity(t *testing.T) {
	// a -> n1 (Required). b is anti-affine with a and Required onto n1 too, with a
	// MaxInt Prefer. b's only admissible node is n1, but anti-affinity vetoes n1
	// (a is there). No admissible node remains -> must be ErrUnsatisfiable, never a
	// fail-open co-placement of two anti-affine resources.
	e := NewEngine(
		[]Resource{{ID: "a"}, {ID: "b"}},
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	res, err := e.Solve(ConstraintSet{
		Locations: []Location{
			{Resource: "a", Node: "n1", Affinity: Required},
			{Resource: "b", Node: "n1", Affinity: Required},
			{Resource: "b", Node: "n1", Affinity: Prefer, Score: math.MaxInt},
		},
		Colocations: []Colocation{{First: "a", Second: "b", Together: false}}, // anti-affinity
	})
	if err == nil {
		t.Fatalf("[%s] FAIL-OPEN: anti-affine b forced onto a's only node n1 returned no error: %v",
			adv2UUID, res.Placement)
	}
	if res.Placement != nil {
		t.Fatalf("[%s] expected nil placement on unsatisfiable anti-affinity, got %v", adv2UUID, res.Placement)
	}
	t.Logf("[%s] saturating clamp is NOT a fail-open: genuinely-unsatisfiable anti-affinity still ErrUnsatisfiable", adv2UUID)
}

// ---------------------------------------------------------------------------
// (6) MaxInt veto interaction: anti-affinity veto must still win regardless of a
// MaxInt Prefer on the vetoed node. b is vetoed off n1 (a there) and must land on
// n2 even though it has a MaxInt Prefer for n1. (Veto is a `continue`, not a
// score term, so this guards that the saturating change didn't accidentally
// route the veto through scoring.)
// ---------------------------------------------------------------------------

func TestNumeric_VetoBeatsMaxIntPrefer(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "a"}, {ID: "b"}},
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	res, err := e.Solve(ConstraintSet{
		Locations: []Location{
			{Resource: "a", Node: "n1", Affinity: Required},
			{Resource: "b", Node: "n1", Affinity: Prefer, Score: math.MaxInt},
		},
		Colocations: []Colocation{{First: "a", Second: "b", Together: false}},
	})
	if err != nil {
		t.Fatalf("[%s] unexpected error (b can still go to n2): %v", adv2UUID, err)
	}
	if res.Placement["b"] != "n2" {
		t.Fatalf("[%s] VETO BYPASS: b=%q, want n2 (anti-affinity veto must beat a MaxInt Prefer on n1)",
			adv2UUID, res.Placement["b"])
	}
	t.Logf("[%s] anti-affinity veto on n1 still routes b to n2 despite a MaxInt Prefer for n1", adv2UUID)
}
