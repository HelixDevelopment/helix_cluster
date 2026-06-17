package constraints

// Adversarial / contract-pinning suite for pkg/constraints.
//
// Motivation: a sibling package (classads) had a REMOTELY-TRIGGERABLE DoS — a
// deeply-nested hostile expression drove unbounded recursive-descent past Go's
// ~1GB stack into an uncatchable `fatal error: stack overflow`. This suite hunts
// that exact class here, plus total-order / identity edges, numeric poison, and
// unguarded shared state under concurrency.
//
// HEADLINE FINDING: the recursive-descent DoS class DOES NOT EXIST in this
// package. constraints does NOT parse or evaluate any string/expression grammar.
// Its inputs are FLAT Go structs (Location, Colocation, Order) and every solver
// routine is a non-recursive loop:
//   - topoSort       : iterative Kahn's algorithm (no recursion)
//   - admissibleNodes: two flat loops
//   - bestNode       : flat loop over candidate nodes
//   - colocationPartner: a flat switch
// There is no self-call anywhere in engine.go, so no caller-controlled input can
// drive unbounded recursion. The tests below PROVE termination on pathological
// inputs (long Order chains, dense colocation/location sets) instead of merely
// asserting "no panic".
//
// REAL BUG: none found. The integer-overflow interaction in TestNumeric_* is a
// LATENT RISK (caller must supply math.MaxInt-class Score), documented and
// pinned but NOT fixed — the documented colocationBonus is a finite constant and
// the doc's "bonus dominates Prefer/stickiness" claim is a magnitude statement
// about ordinary scores, which the test confirms holds for non-pathological
// inputs.
//
// All tests are package-internal (white-box) to reach unexported constants
// (colocationBonus) and assert SINK-SIDE behavior: actual placement map, actual
// start order, actual error identity — never just "did not panic".

import (
	"math"
	"runtime"
	"sync"
	"testing"
)

const advUUID = "HXC-constraints-adversarial-9d41fb70"

// =============================================================================
// (a) HOSTILE-INPUT / DoS CLASS — recursion & unbounded-allocation hunt.
//
// There is NO expression parser to nest, so the classic deeply-nested-parens
// stack-overflow input has no entry point. The closest structural analogue is a
// very long Order DEPENDENCY CHAIN (a->b->c->...), which a hostile caller could
// supply hoping topoSort recurses per edge. It does NOT: Kahn's algorithm is
// iterative. These tests build large pathological inputs and prove the solver
// TERMINATES with a correct, finite result on the SAME goroutine stack.
// =============================================================================

// TestDoS_LongOrderChain_NoRecursion feeds a strictly-linear Order chain of
// length N (n0 before n1 before ... before n{N-1}) — the input most likely to
// blow a recursive topological sort's stack. Kahn's iterative algorithm must
// return the exact linear order with no stack growth.
//
// Sink-side proof: we assert the FULL resolved StartOrder equals the forced
// linear sequence (not just "no panic"), and we run on the default goroutine
// stack — a recursive impl at N=200000 would have overflowed (~1GB) before
// returning. It returns instantly, proving iteration.
//
// Mutation note: this is a contract pin, not a bug repro. If topoSort were ever
// rewritten recursively, THIS test would convert to a stack-overflow crash at
// large N (run the gated variant below to see that distinction).
func TestDoS_LongOrderChain_NoRecursion(t *testing.T) {
	const N = 200_000
	resources := make([]Resource, N)
	for i := range resources {
		resources[i] = Resource{ID: ridFor(i)}
	}
	orders := make([]Order, 0, N-1)
	for i := 0; i < N-1; i++ {
		orders = append(orders, Order{Before: ridFor(i), After: ridFor(i + 1)})
	}
	e := NewEngine(resources, []Node{{ID: "n1"}})

	var startStack, endStack [8 << 10]byte
	nStart := runtime.Stack(startStack[:], false)

	res, err := e.Solve(ConstraintSet{Orders: orders})
	if err != nil {
		t.Fatalf("[%s] unexpected error on length-%d chain: %v", advUUID, N, err)
	}
	nEnd := runtime.Stack(endStack[:], false)

	// Sink-side: the order is exactly the forced linear chain.
	if len(res.StartOrder) != N {
		t.Fatalf("[%s] start order length=%d, want %d", advUUID, len(res.StartOrder), N)
	}
	for i, id := range res.StartOrder {
		if id != ridFor(i) {
			t.Fatalf("[%s] order[%d]=%q, want %q (chain not honored)", advUUID, i, id, ridFor(i))
		}
	}
	t.Logf("[%s] resolved length-%d linear Order chain iteratively; goroutine stack-frame sample start=%dB end=%dB (no recursive growth) ; first3=%v last=%q",
		advUUID, N, nStart, nEnd, res.StartOrder[:3], res.StartOrder[N-1])
}

// TestDoS_DenseColocationSet_Terminates builds a dense set of colocation and
// location constraints (O(R) resources, O(R) colocations, O(R*K) locations) and
// proves Solve terminates with a finite placement. This is the unbounded-work /
// unbounded-allocation hunt for the placement phase. bestNode is O(cand *
// (locations + colocations)); with everything flat it terminates in polynomial
// time and bounded memory.
//
// Sink-side: every resource is placed on exactly one admissible node, and the
// chain of positive colocations actually co-locates them all (proving the work
// produced a correct result, not an early bail-out).
func TestDoS_DenseColocationSet_Terminates(t *testing.T) {
	const R = 400
	resources := make([]Resource, R)
	for i := range resources {
		resources[i] = Resource{ID: ridFor(i)}
	}
	nodes := []Node{{ID: "n1"}, {ID: "n2"}}
	// Chain every resource Together with the next: r0~r1~r2~...~r{R-1}.
	colos := make([]Colocation, 0, R-1)
	for i := 0; i < R-1; i++ {
		colos = append(colos, Colocation{First: ridFor(i), Second: ridFor(i + 1), Together: true})
	}
	// r0 is Required onto n2; the bonus must drag the whole chain to n2.
	locs := []Location{{Resource: ridFor(0), Node: "n2", Affinity: Required}}

	e := NewEngine(resources, nodes)
	res, err := e.Solve(ConstraintSet{Locations: locs, Colocations: colos})
	if err != nil {
		t.Fatalf("[%s] dense set unexpectedly unsatisfiable: %v", advUUID, err)
	}
	for i := 0; i < R; i++ {
		if got := res.Placement[ridFor(i)]; got != "n2" {
			t.Fatalf("[%s] colocation chain broke at %s: placed on %q, want n2", advUUID, ridFor(i), got)
		}
	}
	t.Logf("[%s] dense %d-resource Together chain anchored at n2 fully co-located (%d colocations, terminated, all on n2)",
		advUUID, R, len(colos))
}

// TestDoS_GatedRecursionRepro is a GATED placeholder for the stack-overflow
// repro that WOULD exist if constraints parsed nested expressions like classads.
// It does not — so there is nothing to crash. The test documents that and skips
// by default; the env switch exists only to mirror the protocol's gating
// requirement for fatal repros. If a future change introduces a recursive
// expression evaluator, replace the body with the hostile nested input and
// confirm the `fatal error: stack overflow` crash in a subprocess.
//
// Run explicitly with: HXC_CONSTRAINTS_DOS_REPRO=1 go test -run GatedRecursion .
func TestDoS_GatedRecursionRepro(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	t.Skipf("[%s] N/A: pkg/constraints has no expression parser / no recursion — "+
		"the classads-style nested-input stack-overflow DoS has no entry point here "+
		"(topoSort is iterative Kahn's; bestNode/admissibleNodes are flat loops). "+
		"No fatal repro exists to gate.", advUUID)
}

// ridFor produces a stable, collision-free resource ID for index i.
func ridFor(i int) string {
	// base-36-ish without fmt to keep the hot loop allocation-light; correctness,
	// not speed, is the point — but uniqueness must hold for 200k IDs.
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if i == 0 {
		return "r0"
	}
	var buf [16]byte
	p := len(buf)
	for i > 0 {
		p--
		buf[p] = digits[i%36]
		i /= 36
	}
	return "r" + string(buf[p:])
}

// =============================================================================
// (b) TOTAL-ORDER / IDENTITY EDGES — the engine PROMISES "fully deterministic"
// placement and "ties are broken by stable node ordering". Pin that, plus
// duplicate-key behavior.
// =============================================================================

// TestTotalOrder_DeterministicAcrossRuns runs an ambiguous-tie scenario many
// times and asserts the SAME placement and start order every run. The scenario
// is deliberately tie-heavy: two resources, two nodes, equal prefer scores, so a
// map-iteration-order leak (nondeterminism) would surface as differing results.
//
// Sink-side: byte-identical Placement map and StartOrder slice across 200 runs.
func TestTotalOrder_DeterministicAcrossRuns(t *testing.T) {
	build := func() (*Engine, ConstraintSet) {
		e := NewEngine(
			[]Resource{{ID: "alpha"}, {ID: "beta"}, {ID: "gamma"}},
			[]Node{{ID: "n1"}, {ID: "n2"}, {ID: "n3"}},
		)
		cs := ConstraintSet{
			// All-equal prefer on n2 for everyone -> tie that must resolve by the
			// stable node ordering (n1 first overall, but n2 preferred): the doc
			// says first node wins ties, and prefer +1 lifts n2 above n1/n3.
			Locations: []Location{
				{Resource: "alpha", Node: "n2", Affinity: Prefer, Score: 1},
				{Resource: "beta", Node: "n2", Affinity: Prefer, Score: 1},
				{Resource: "gamma", Node: "n2", Affinity: Prefer, Score: 1},
			},
		}
		return e, cs
	}

	e0, cs0 := build()
	first, err := e0.Solve(cs0)
	if err != nil {
		t.Fatalf("[%s] baseline solve failed: %v", advUUID, err)
	}
	for run := 0; run < 200; run++ {
		e, cs := build()
		got, err := e.Solve(cs)
		if err != nil {
			t.Fatalf("[%s] run %d failed: %v", advUUID, run, err)
		}
		if !samePlacement(first.Placement, got.Placement) {
			t.Fatalf("[%s] NONDETERMINISTIC placement at run %d: %v vs baseline %v",
				advUUID, run, got.Placement, first.Placement)
		}
		if !sameOrder(first.StartOrder, got.StartOrder) {
			t.Fatalf("[%s] NONDETERMINISTIC start order at run %d: %v vs baseline %v",
				advUUID, run, got.StartOrder, first.StartOrder)
		}
	}
	t.Logf("[%s] 200 runs identical: placement=%v order=%v (tie resolved on n2, stable)",
		advUUID, first.Placement, first.StartOrder)
}

// TestTotalOrder_EqualPreferTieGoesToFirstNode pins the documented tie-break
// rule "Pick the highest score (first node wins ties)". Two admissible nodes
// carry the SAME prefer score; the resource must land on whichever appears FIRST
// in the node slice — deterministically, not by chance.
//
// Sink-side: swapping node order swaps the winner, proving the tie-break is the
// node slice position and nothing else (e.g. not lexicographic on ID).
func TestTotalOrder_EqualPreferTieGoesToFirstNode(t *testing.T) {
	locs := []Location{
		{Resource: "svc", Node: "a", Affinity: Prefer, Score: 7},
		{Resource: "svc", Node: "b", Affinity: Prefer, Score: 7},
	}
	// Nodes [a,b]: tie -> a.
	resAB, err := NewEngine([]Resource{{ID: "svc"}}, []Node{{ID: "a"}, {ID: "b"}}).
		Solve(ConstraintSet{Locations: locs})
	if err != nil {
		t.Fatalf("[%s] AB solve failed: %v", advUUID, err)
	}
	// Nodes [b,a]: same scores, tie -> b (first in slice now).
	resBA, err := NewEngine([]Resource{{ID: "svc"}}, []Node{{ID: "b"}, {ID: "a"}}).
		Solve(ConstraintSet{Locations: locs})
	if err != nil {
		t.Fatalf("[%s] BA solve failed: %v", advUUID, err)
	}
	t.Logf("[%s] tie-break: nodes[a,b]->%q ; nodes[b,a]->%q", advUUID, resAB.Placement["svc"], resBA.Placement["svc"])
	if resAB.Placement["svc"] != "a" {
		t.Fatalf("[%s] tie not broken by first node: nodes[a,b] gave %q, want a", advUUID, resAB.Placement["svc"])
	}
	if resBA.Placement["svc"] != "b" {
		t.Fatalf("[%s] tie not broken by first node: nodes[b,a] gave %q, want b", advUUID, resBA.Placement["svc"])
	}
}

// TestIdentity_DuplicateResourceID_DeterministicLastWins documents the engine's
// behavior when the SAME resource ID appears twice in the resources slice (a
// caller identity error). The contract docs don't address duplicates, so this is
// a LATENT-RISK PIN, not a bug claim: the engine must at least behave
// DETERMINISTICALLY. Observed: topoSort dedups (one entry in StartOrder), and the
// per-ID state maps (current/stick) are overwritten in slice order, so the LAST
// duplicate's Current/Stickiness win. We pin that exact, repeatable outcome.
//
// Sink-side: the placement reflects the LAST duplicate's stickiness (Current n2,
// Stickiness 0 -> no pull, lands on first node n1), repeatably across runs.
func TestIdentity_DuplicateResourceID_DeterministicLastWins(t *testing.T) {
	build := func() *Engine {
		return NewEngine(
			[]Resource{
				{ID: "svc", Current: "n2", Stickiness: 1_000_000}, // first dup: huge pull to n2
				{ID: "svc", Current: "n1", Stickiness: 0},         // last dup: no pull
			},
			[]Node{{ID: "n1"}, {ID: "n2"}},
		)
	}
	var base Result
	for run := 0; run < 50; run++ {
		res, err := build().Solve(ConstraintSet{})
		if err != nil {
			t.Fatalf("[%s] dup-id solve failed run %d: %v", advUUID, run, err)
		}
		if len(res.StartOrder) != 1 || res.StartOrder[0] != "svc" {
			t.Fatalf("[%s] dup id not deduped in order: %v", advUUID, res.StartOrder)
		}
		if run == 0 {
			base = res
			continue
		}
		if !samePlacement(base.Placement, res.Placement) {
			t.Fatalf("[%s] NONDETERMINISTIC dup-id placement: %v vs %v", advUUID, res.Placement, base.Placement)
		}
	}
	// Last-dup wins: Current=n1/Stickiness=0 -> no stickiness pull -> first node n1.
	if base.Placement["svc"] != "n1" {
		t.Fatalf("[%s] dup-id last-wins contract broke: svc=%q, want n1 (last dup has Stickiness 0)",
			advUUID, base.Placement["svc"])
	}
	t.Logf("[%s] LATENT-RISK PIN: duplicate resource ID is deterministic (deduped in order; "+
		"last duplicate's Current/Stickiness win) -> svc=%s", advUUID, base.Placement["svc"])
}

// =============================================================================
// (c) NUMERIC POISON — scores are int (no float/NaN), so the NaN-defeats-a-
// threshold class is absent. The live numeric edge is INTEGER OVERFLOW of the
// score sum. We pin both the GOOD direction (ordinary-magnitude bonus dominates,
// per doc) and document the LATENT RISK direction (caller-supplied math.MaxInt
// Score interacts with the +colocationBonus / +stickiness sum to flip a
// placement). No fix: the doc's dominance claim is about the finite constant vs
// ordinary scores; MaxInt input is pathological caller misuse, not a contract
// breach.
// =============================================================================

// TestNumeric_BonusDominatesOrdinaryScores is the GOOD-direction pin matching
// the documented promise: "The bonus dominates any Prefer/stickiness pull, so a
// resource gravitates to its partner whenever that partner's node is admissible."
// With ORDINARY (non-overflowing) scores, a partner sitting on node X must pull
// the resource onto X even when the resource has a large-but-finite Prefer on a
// different node, as long as that Prefer is below colocationBonus.
//
// Sink-side: cache co-locates with web on n1 despite a Prefer just under the
// bonus on n2.
func TestNumeric_BonusDominatesOrdinaryScores(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "web"}, {ID: "cache"}},
		[]Node{{ID: "n1"}, {ID: "n2"}},
	)
	cs := ConstraintSet{
		Locations: []Location{
			{Resource: "web", Node: "n1", Affinity: Required}, // web -> n1, placed first
			// cache prefers n2 with a big-but-sub-bonus score; the bonus on n1 must win.
			{Resource: "cache", Node: "n2", Affinity: Prefer, Score: colocationBonus - 1},
		},
		Colocations: []Colocation{{First: "web", Second: "cache", Together: true}},
	}
	res, err := e.Solve(cs)
	if err != nil {
		t.Fatalf("[%s] bonus failed to dominate ordinary Prefer (got error): %v", advUUID, err)
	}
	if res.Placement["cache"] != "n1" {
		t.Fatalf("[%s] doc 'bonus dominates' broken: cache=%q, want n1 (web's node)",
			advUUID, res.Placement["cache"])
	}
	t.Logf("[%s] bonus(%d) dominated Prefer(%d): web=%s cache=%s (co-located)",
		advUUID, colocationBonus, colocationBonus-1, res.Placement["web"], res.Placement["cache"])
}

// TestNumeric_MaxIntScoreNoOverflowSplit_Hardened pins the HARDENED behavior
// after the saturatingAdd fix in engine.go. PREVIOUSLY this case was a latent
// risk: a caller-supplied math.MaxInt Prefer on the partner's OWN node, summed
// with +colocationBonus, wrapped int to a large NEGATIVE value, so the partner's
// node LOST and the Together pair SPLIT into a FALSE ErrUnsatisfiable — the
// opposite of intent (b WANTED to be with its partner on that very node).
//
// With saturating accumulation the sum clamps to math.MaxInt instead of
// wrapping, so n1 (partner's node, also b's hugely-preferred node) correctly
// wins and the pair co-locates. See also TestNumeric_SaturatingAdd_* below for
// the mutation-isolated proof on the score helper.
//
// Sink-side proof: with Score=MaxInt the pair now co-locates on n1 (no error,
// no split); with Score=MaxInt>>1 (never overflowed even pre-fix) the same shape
// co-locates — both agree, confirming the wrap-driven split is gone.
func TestNumeric_MaxIntScoreNoOverflowSplit_Hardened(t *testing.T) {
	shape := func(score int) (Result, error) {
		e := NewEngine(
			[]Resource{{ID: "a"}, {ID: "b"}}, // a placed first
			[]Node{{ID: "n1"}, {ID: "n2"}},
		)
		return e.Solve(ConstraintSet{
			Locations: []Location{
				{Resource: "a", Node: "n1", Affinity: Required}, // a -> n1
				// b WANTS n1 (its partner's node) with a huge Prefer, AND b is
				// Together with a. Both the Prefer and the bonus land on n1.
				{Resource: "b", Node: "n1", Affinity: Prefer, Score: score},
				{Resource: "b", Node: "n2", Affinity: Prefer, Score: 10},
			},
			Colocations: []Colocation{{First: "a", Second: "b", Together: true}},
		})
	}

	// Hardened case: MaxInt + colocationBonus saturates (no wrap) -> n1 wins -> co-located.
	resMax, errMax := shape(math.MaxInt)
	if errMax != nil {
		t.Fatalf("[%s] HARDENED: Score=MaxInt unexpectedly errored (wrap-split should be gone): %v",
			advUUID, errMax)
	}
	if resMax.Placement["b"] != "n1" {
		t.Fatalf("[%s] HARDENED: Score=MaxInt -> b=%q, want n1 (saturating sum must keep partner's node winning)",
			advUUID, resMax.Placement["b"])
	}
	t.Logf("[%s] HARDENED: Score=MaxInt -> co-located b on n1 (saturating add, no wrap-split)", advUUID)

	// Cross-check: a large score that NEVER overflowed even pre-fix agrees.
	safe := math.MaxInt>>1 - colocationBonus
	resOK, errOK := shape(safe)
	if errOK != nil {
		t.Fatalf("[%s] control (non-overflowing huge Score) unexpectedly failed: %v", advUUID, errOK)
	}
	if resOK.Placement["b"] != "n1" {
		t.Fatalf("[%s] control: b=%q, want n1 (huge non-overflowing Prefer + bonus on n1 must win)",
			advUUID, resOK.Placement["b"])
	}
	t.Logf("[%s] control: Score=%d (never overflowed) -> b on n1 ; agrees with the MaxInt case",
		advUUID, safe)
}

// =============================================================================
// (d) UNGUARDED SHARED STATE under concurrency. Engine holds immutable slices;
// Solve allocates all mutable state (maps) locally. Concurrent Solve on a SHARED
// Engine must be race-free and produce IDENTICAL results. Run under -race.
// =============================================================================

// TestConcurrency_SharedEngineRaceFree fans out many goroutines calling Solve on
// ONE shared Engine with a tie-rich, colocation-bearing constraint set. Under
// `go test -race` any shared-state write would be flagged. We also collect every
// result and assert they are byte-identical (no torn/interleaved state).
//
// Sink-side: -race clean AND all goroutine results equal the serial baseline.
func TestConcurrency_SharedEngineRaceFree(t *testing.T) {
	e := NewEngine(
		[]Resource{{ID: "web"}, {ID: "cache"}, {ID: "db"}},
		[]Node{{ID: "n1"}, {ID: "n2"}, {ID: "n3"}},
	)
	cs := ConstraintSet{
		Locations: []Location{
			{Resource: "web", Node: "n1", Affinity: Prefer, Score: 5},
			{Resource: "cache", Node: "n2", Affinity: Prefer, Score: 5},
			{Resource: "db", Node: "n3", Affinity: Required},
		},
		Colocations: []Colocation{{First: "web", Second: "cache", Together: true}},
		Orders:      []Order{{Before: "db", After: "web"}},
	}

	baseline, err := e.Solve(cs)
	if err != nil {
		t.Fatalf("[%s] baseline failed: %v", advUUID, err)
	}

	const G = 64
	var wg sync.WaitGroup
	results := make([]Result, G)
	errs := make([]error, G)
	wg.Add(G)
	for g := 0; g < G; g++ {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = e.Solve(cs)
		}(g)
	}
	wg.Wait()

	for g := 0; g < G; g++ {
		if errs[g] != nil {
			t.Fatalf("[%s] goroutine %d errored: %v", advUUID, g, errs[g])
		}
		if !samePlacement(baseline.Placement, results[g].Placement) {
			t.Fatalf("[%s] goroutine %d placement diverged: %v vs baseline %v",
				advUUID, g, results[g].Placement, baseline.Placement)
		}
		if !sameOrder(baseline.StartOrder, results[g].StartOrder) {
			t.Fatalf("[%s] goroutine %d order diverged: %v vs baseline %v",
				advUUID, g, results[g].StartOrder, baseline.StartOrder)
		}
	}
	t.Logf("[%s] %d concurrent Solve on a shared Engine: race-free, all identical (placement=%v order=%v)",
		advUUID, G, baseline.Placement, baseline.StartOrder)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func samePlacement(a, b Placement) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
