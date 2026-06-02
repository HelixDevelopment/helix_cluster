package constraints

import (
	"errors"
	"testing"
)

const runUUID = "HXC-1402-constraints-7f3a91c2-run"

// TestColocationPositive_SameNode proves two positively colocated resources
// land on the SAME node, driven by colocationBonus.
//
// web prefers n1 (+5) and is placed first -> n1. cache prefers n2 (+5); the
// only thing that can overcome cache's own +5 pull toward n2 is the
// colocationBonus on web's node n1. So the bonus must beat the Prefer score.
//
// Mutation: neutralize `score += colocationBonus` and cache follows its Prefer
// to n2 while web stays on n1 -> split -> Solve now returns ErrUnsatisfiable
// (the post-placement colocation check fires) and the err==nil path here fails.
func TestColocationPositive_SameNode(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "web"}, {ID: "cache"}},
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	cs := ConstraintSet{
		// Pull each resource toward a DIFFERENT preferred node so that, absent
		// colocation, they would split. Colocation must override that.
		Locations: []Location{
			{Resource: "web", Node: "n1", Affinity: Prefer, Score: 5},
			{Resource: "cache", Node: "n2", Affinity: Prefer, Score: 5},
		},
		Colocations: []Colocation{{First: "web", Second: "cache", Together: true}},
	}
	res, err := e.Solve(cs)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	t.Logf("[%s] before(prefs): web=n1 cache=n2 ; after(placement): web=%s cache=%s",
		runUUID, res.Placement["web"], res.Placement["cache"])
	if res.Placement["web"] != res.Placement["cache"] {
		t.Fatalf("[%s] positive colocation failed: web=%s cache=%s (want same node)",
			runUUID, res.Placement["web"], res.Placement["cache"])
	}
}

// TestColocationPositive_BonusIsDecisive isolates the colocationBonus addition
// (`score += colocationBonus`) as the sole deciding factor pulling a resource
// onto its partner, WITHOUT relying on a veto and WITHOUT relying on the
// post-placement unsatisfiability check.
//
// Construction: web is Required onto n2 and placed first (insertion order
// web-before-cache) -> web=n2. cache is admissible on EVERY node {n1,n2,n3}
// (no Required/Forbidden for it) and carries a Prefer +5 on n3. With the bonus
// active, cache's node n2 scores colocationBonus (>> 5) and wins -> cache=n2,
// co-located, and Solve returns nil. With the bonus neutralized, cache's best
// node is its Prefer target n3 (+5) -> cache=n3, which is admissible, so it is
// PLACED there (no veto stops it); the pair is then split and the
// post-placement check returns ErrUnsatisfiable. Either way, neutralizing the
// bonus makes this test fail (err != nil OR cache != n2).
//
// Mutation: neutralize ONLY `score += colocationBonus` -> cache follows its
// Prefer to n3, Solve returns ErrUnsatisfiable, and the err-check below FAILS.
// This proves the bonus addition is load-bearing and independently tested.
func TestColocationPositive_BonusIsDecisive(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "web"}, {ID: "cache"}},
		[]Node{{ID: "n1"}, {ID: "n2"}, {ID: "n3"}},
	)
	cs := ConstraintSet{
		Locations: []Location{
			// web pinned to n2 (NOT the first node) and placed first.
			{Resource: "web", Node: "n2", Affinity: Required},
			// cache is free on every node but is pulled toward n3 by Prefer +5;
			// only the bonus on web's node n2 can overcome that pull.
			{Resource: "cache", Node: "n3", Affinity: Prefer, Score: 5},
		},
		Colocations: []Colocation{{First: "web", Second: "cache", Together: true}},
	}
	res, err := e.Solve(cs)
	if err != nil {
		t.Fatalf("[%s] colocation bonus not decisive (got error, partner pull lost to Prefer): %v", runUUID, err)
	}
	t.Logf("[%s] before(web required n2 placed first; cache prefers n3 +5; bonus must beat the +5 pull) ; after: web=%s cache=%s",
		runUUID, res.Placement["web"], res.Placement["cache"])
	if res.Placement["web"] != "n2" {
		t.Fatalf("[%s] precondition broken: web=%s (want n2)", runUUID, res.Placement["web"])
	}
	if res.Placement["cache"] != "n2" {
		t.Fatalf("[%s] colocation bonus not decisive: cache=%s (want n2 = web's node, not its Prefer n3)",
			runUUID, res.Placement["cache"])
	}
}

// TestColocationNegative_DifferentNode proves anti-affine resources land on
// DIFFERENT nodes.
//
// Mutation: if the anti-affinity veto (`pnode == nid` in the else branch) is
// removed, both resources fall onto their shared preferred node and this fails.
func TestColocationNegative_DifferentNode(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "primary"}, {ID: "replica"}},
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	cs := ConstraintSet{
		// Both prefer n1; anti-affinity must push replica to n2.
		Locations: []Location{
			{Resource: "primary", Node: "n1", Affinity: Prefer, Score: 9},
			{Resource: "replica", Node: "n1", Affinity: Prefer, Score: 9},
		},
		Colocations: []Colocation{{First: "primary", Second: "replica", Together: false}},
	}
	res, err := e.Solve(cs)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	t.Logf("[%s] before(both prefer n1) ; after: primary=%s replica=%s",
		runUUID, res.Placement["primary"], res.Placement["replica"])
	if res.Placement["primary"] == res.Placement["replica"] {
		t.Fatalf("[%s] anti-affinity failed: both on %s (want different)",
			runUUID, res.Placement["primary"])
	}
}

// TestOrder_Sequence proves ordered resources appear A before B before C.
//
// Mutation: if topoSort stops respecting indegree (e.g. emits insertion order),
// the C-before-A edge below is violated and the index check fails.
func TestOrder_Sequence(t *testing.T) {
	// Insertion order deliberately reversed (c,b,a) so a naive pass-through
	// would emit c,b,a and fail the required a<b<c ordering.
	e := NewEngine(
		[]Resource{{ID: "c"}, {ID: "b"}, {ID: "a"}},
		[]Node{{ID: "n1"}},
	)
	cs := ConstraintSet{
		Orders: []Order{
			{Before: "a", After: "b"},
			{Before: "b", After: "c"},
		},
	}
	res, err := e.Solve(cs)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	pos := map[string]int{}
	for i, id := range res.StartOrder {
		pos[id] = i
	}
	t.Logf("[%s] before(insertion c,b,a) ; after(start order): %v", runUUID, res.StartOrder)
	if !(pos["a"] < pos["b"] && pos["b"] < pos["c"]) {
		t.Fatalf("[%s] order violated: %v (want a<b<c)", runUUID, res.StartOrder)
	}
}

// TestOrder_Cycle proves a cyclic order set returns ErrOrderCycle.
//
// Mutation: if topoSort returns insertion order on stall instead of an error,
// errors.Is below fails.
func TestOrder_Cycle(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "x"}, {ID: "y"}},
		[]Node{{ID: "n1"}},
	)
	cs := ConstraintSet{
		Orders: []Order{
			{Before: "x", After: "y"},
			{Before: "y", After: "x"},
		},
	}
	_, err := e.Solve(cs)
	t.Logf("[%s] before(cycle x->y->x) ; after(err): %v", runUUID, err)
	if !errors.Is(err, ErrOrderCycle) {
		t.Fatalf("[%s] want ErrOrderCycle, got %v", runUUID, err)
	}
}

// TestStickiness_StaysPut proves a placed resource STAYS on its current node
// even when another node is slightly more attractive, when stickiness exceeds
// the attraction gap.
//
// Mutation: dropping the stickiness bonus (the `score += stick[rid]` line)
// lets the more-attractive node win and the resource MOVES — failing this.
func TestStickiness_StaysPut(t *testing.T) {
	// Resource currently on n1. n2 is preferred by +3. Stickiness +5 > 3 wins.
	e := NewEngine(
		[]Resource{{ID: "db", Current: "n1", Stickiness: 5}},
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	cs := ConstraintSet{
		Locations: []Location{
			{Resource: "db", Node: "n2", Affinity: Prefer, Score: 3},
		},
	}
	res, err := e.Solve(cs)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	t.Logf("[%s] before(db on n1, n2 pref +3, sticky +5) ; after: db=%s", runUUID, res.Placement["db"])
	if res.Placement["db"] != "n1" {
		t.Fatalf("[%s] stickiness failed: db=%s (want n1, should stay put)", runUUID, res.Placement["db"])
	}
}

// TestStickiness_Zero_Moves proves the OTHER branch: with stickiness 0 the same
// resource MOVES to the more attractive node. This pairs with StaysPut to prove
// the bonus is load-bearing, not incidental.
//
// Mutation: if Prefer scores were ignored, the resource would stay on n1 and
// this fails; conversely it confirms StaysPut isn't passing by accident.
func TestStickiness_Zero_Moves(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "db", Current: "n1", Stickiness: 0}},
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	cs := ConstraintSet{
		Locations: []Location{
			{Resource: "db", Node: "n2", Affinity: Prefer, Score: 3},
		},
	}
	res, err := e.Solve(cs)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	t.Logf("[%s] before(db on n1, n2 pref +3, sticky 0) ; after: db=%s", runUUID, res.Placement["db"])
	if res.Placement["db"] != "n2" {
		t.Fatalf("[%s] expected move to n2, got db=%s", runUUID, res.Placement["db"])
	}
}

// TestLocation_HardForbid_NeverPlaced proves a resource is never placed on a
// forbidden node even when that node is otherwise the only "preferred" pull.
//
// Mutation: if admissibleNodes stopped filtering Forbidden, the resource could
// land on n1 and this fails.
func TestLocation_HardForbid_NeverPlaced(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "svc"}},
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	cs := ConstraintSet{
		Locations: []Location{
			// n1 is forbidden yet also carries a huge Prefer score to tempt the
			// scorer; the hard filter must remove n1 before scoring.
			{Resource: "svc", Node: "n1", Affinity: Forbidden},
			{Resource: "svc", Node: "n1", Affinity: Prefer, Score: 1000},
		},
	}
	res, err := e.Solve(cs)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	t.Logf("[%s] before(n1 forbidden+pref1000) ; after: svc=%s", runUUID, res.Placement["svc"])
	if res.Placement["svc"] == "n1" {
		t.Fatalf("[%s] forbidden node was used: svc=n1", runUUID)
	}
	if res.Placement["svc"] != "n2" {
		t.Fatalf("[%s] expected svc on n2, got %s", runUUID, res.Placement["svc"])
	}
}

// TestLocation_AllForbidden_Unsatisfiable proves an over-constrained set (every
// node forbidden) returns ErrUnsatisfiable and no placement.
//
// Mutation: if admissibleNodes returned a non-empty allowed list on empty,
// errors.Is below fails.
func TestLocation_AllForbidden_Unsatisfiable(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "svc"}},
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	cs := ConstraintSet{
		Locations: []Location{
			{Resource: "svc", Node: "n1", Affinity: Forbidden},
			{Resource: "svc", Node: "n2", Affinity: Forbidden},
		},
	}
	res, err := e.Solve(cs)
	t.Logf("[%s] before(all nodes forbidden) ; after(err): %v placement=%v", runUUID, err, res.Placement)
	if !errors.Is(err, ErrUnsatisfiable) {
		t.Fatalf("[%s] want ErrUnsatisfiable, got %v", runUUID, err)
	}
	if res.Placement != nil {
		t.Fatalf("[%s] expected no placement on unsatisfiable, got %v", runUUID, res.Placement)
	}
}

// TestColocationPositive_SplitDetected_Unsatisfiable proves the post-placement
// colocation consistency check (`placement[First] != placement[Second]` ->
// ErrUnsatisfiable) is itself the cause of the error — NOT the admissibleNodes
// empty-set path.
//
// Construction: a is Required onto n2 and placed first (insertion order
// a-before-b) -> a=n2. b is Forbidden ONLY on n2, so b's admissible set is
// {n1} — a NON-EMPTY set, so admissibleNodes returns successfully and cannot be
// the source of the error. a~b are Together; the bonus tries to pull b to n2 but
// n2 is not admissible for b, so b is PLACED on n1 (its only candidate, no veto
// blocks it). The pair is now split (a=n2, b=n1) and ONLY the post-placement
// check turns that into ErrUnsatisfiable.
//
// Mutation: remove ONLY the post-placement split check (the `if
// placement[co.First] != placement[co.Second]` loop in Solve) and b is returned
// happily on n1 with NO error -> this test FAILS (got nil). admissibleNodes is
// untouched here (b's set is non-empty), so this exercises a DISTINCT code path
// from TestLocation_AllForbidden_Unsatisfiable.
func TestColocationPositive_SplitDetected_Unsatisfiable(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "a"}, {ID: "b"}}, // a placed first (insertion order)
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	cs := ConstraintSet{
		Locations: []Location{
			{Resource: "a", Node: "n2", Affinity: Required},  // a pinned to n2
			{Resource: "b", Node: "n2", Affinity: Forbidden}, // b cannot be on n2 (a's node)
			// b is NOT forbidden on n1: admissible[b] = {n1}, a non-empty set,
			// so admissibleNodes does NOT error. Unsatisfiability arises ONLY
			// because b lands on n1, split from a on n2, and the post-placement
			// colocation check rejects the split.
		},
		Colocations: []Colocation{{First: "a", Second: "b", Together: true}},
	}
	res, err := e.Solve(cs)
	t.Logf("[%s] before(a=n2 required placed first; b admissible only on n1; a~b together => forced split) ; after(err): %v placement=%v",
		runUUID, err, res.Placement)
	if !errors.Is(err, ErrUnsatisfiable) {
		t.Fatalf("[%s] want ErrUnsatisfiable from post-placement split check, got %v", runUUID, err)
	}
	if res.Placement != nil {
		t.Fatalf("[%s] expected no placement on unsatisfiable, got %v", runUUID, res.Placement)
	}
}
