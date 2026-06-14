package rescorer

// Adversarial NaN/Inf + total-order probe for pkg/rescorer.
//
// rescorer re-ranks providers by a float-derived score (score(price) = -price;
// cheapest => top). Its package doc PROMISES the ranking is "deterministic" and
// that "lower price => better score ... cheapest is top". The comparator in
// rerank is:
//
//	if sa != sb { return sa > sb }
//	return idx[a] < idx[b]   // insertion-order tie-break
//
// This guard checks `sa != sb` and `sa > sb`. NEITHER survives a NaN: for a
// NaN-priced provider, score(NaN) = -NaN = NaN, and EVERY comparison against
// NaN is false. So less(a,b)==false AND less(b,a)==false for the NaN element vs
// anything — the less-func is NOT a strict weak ordering. sort.SliceStable then
// produces an order that DEPENDS on the input (insertion) order: the same set of
// providers with the same prices ranks DIFFERENTLY depending only on the order
// the caller happened to add them, and the poisoned NaN provider can land at the
// TOP — mis-routing all traffic to the broken provider. That violates both the
// "deterministic" and the "cheapest is top" contract.
//
// These tests assert SINK-SIDE: the actual published ranking / Top(), never just
// "no panic". They FAIL on unfixed prod (NaN is admitted into the sort).
//
// COORDINATOR NOTE: TestNaNPoison_* and the comparator test below are written to
// PASS once the minimal fix lands (sink NaN scores to the bottom deterministically
// — e.g. treat a NaN/Inf score as worst, ranked after all finite scores, ties
// among them by insertion order). They are NOT gated behind any env var.

import (
	"math"
	"testing"
)

// permute3 returns all 6 permutations of a 3-element string slice.
func permute3(s [3]string) [][3]string {
	return [][3]string{
		{s[0], s[1], s[2]},
		{s[0], s[2], s[1]},
		{s[1], s[0], s[2]},
		{s[1], s[2], s[0]},
		{s[2], s[0], s[1]},
		{s[2], s[1], s[0]},
	}
}

// buildRanked constructs a fresh ReScorer, sets the three providers' prices in
// the given insertion order, ticks once to establish the ranking, and returns
// the published ranking + top.
func buildRanked(order [3]string, prices map[string]float64) ([]string, string) {
	r := New(60, 0, 0)
	for _, id := range order {
		r.SetPrice(id, prices[id])
	}
	r.Tick(0)
	return r.Ranking(), r.Top()
}

// TestNaNPoison_GoodProviderTopIsDeterministic proves the SINK is wrong on
// unfixed prod: a single NaN-priced provider makes the cheapest KNOWN-GOOD
// provider's rank depend on insertion order, and lets the NaN provider seize the
// top. The contract says cheapest (pGood, price 10) is ALWAYS top regardless of
// how providers were inserted.
//
// On unfixed prod: pGood is top in only some insertion orders (NaN breaks the
// total order) -> FAIL. With the fix (NaN sunk to bottom): pGood is top in ALL
// 6 -> PASS.
func TestNaNPoison_GoodProviderTopIsDeterministic(t *testing.T) {
	id := runUUID(t)
	prices := map[string]float64{
		"pGood": 10, // cheapest finite -> MUST be top
		"pB":    20,
		"pNaN":  math.NaN(), // poison
	}

	goodTop := 0
	nanTop := 0
	for _, ord := range permute3([3]string{"pGood", "pB", "pNaN"}) {
		ranking, top := buildRanked(ord, prices)
		t.Logf("run=%s insert=%v -> ranking=%v top=%s", id, ord, ranking, top)
		if top == "pGood" {
			goodTop++
		}
		if top == "pNaN" {
			nanTop++
		}
	}

	if nanTop != 0 {
		t.Fatalf("run=%s NaN-priced provider seized top in %d/6 insertion orders; "+
			"a NaN score must never outrank a finite (cheaper) one", id, nanTop)
	}
	if goodTop != 6 {
		t.Fatalf("run=%s cheapest finite pGood was top in only %d/6 insertion orders; "+
			"contract requires deterministic 'cheapest is top' independent of insertion "+
			"order, but NaN poisons the total order", id, goodTop)
	}
}

// TestNaNPoison_RankingIsDeterministicAcrossInsertionOrder asserts the FULL
// published ranking is identical regardless of insertion order. The doc says the
// ranking is deterministic; insertion order is only a TIE-break, and pGood(10),
// pB(20) are not tied, so the finite providers' relative order must be fixed and
// the NaN provider must sink to a fixed position (last).
//
// On unfixed prod the rankings differ across permutations -> FAIL. With the fix
// every permutation yields the same ranking -> PASS.
func TestNaNPoison_RankingIsDeterministicAcrossInsertionOrder(t *testing.T) {
	id := runUUID(t)
	prices := map[string]float64{
		"pGood": 10,
		"pB":    20,
		"pNaN":  math.NaN(),
	}

	want := []string{"pGood", "pB", "pNaN"} // cheapest, next, then poisoned-last
	for _, ord := range permute3([3]string{"pGood", "pB", "pNaN"}) {
		ranking, _ := buildRanked(ord, prices)
		t.Logf("run=%s insert=%v -> ranking=%v", id, ord, ranking)
		if len(ranking) != len(want) {
			t.Fatalf("run=%s ranking len=%d want %d (%v)", id, len(ranking), len(want), ranking)
		}
		for i := range want {
			if ranking[i] != want[i] {
				t.Fatalf("run=%s ranking depends on insertion order: insert=%v gave %v, "+
					"want deterministic %v (NaN must sink to last, finite order fixed)",
					id, ord, ranking, want)
			}
		}
	}
}

// TestNaNPoison_TopProviderInfDoesNotPoison is a focused single-instance sink
// assertion: a +Inf-priced provider is the WORST (most expensive) and must rank
// LAST, never displacing the cheapest finite provider from the top. score(+Inf)
// = -Inf which DOES order correctly under `>`, so this should already pass; it
// pins that contract and contrasts with the NaN case so a fix that over-sinks
// +Inf-as-cheapest is caught.
func TestInfPriceRanksWorstNotBest(t *testing.T) {
	id := runUUID(t)
	prices := map[string]float64{
		"pGood": 10,
		"pB":    20,
		"pInf":  math.Inf(1), // most expensive -> worst -> last
	}
	ranking, top := buildRanked([3]string{"pGood", "pB", "pInf"}, prices)
	t.Logf("run=%s +Inf-price ranking=%v top=%s", id, ranking, top)
	if top != "pGood" {
		t.Fatalf("run=%s cheapest pGood must be top with a +Inf-priced sibling, got top=%s ranking=%v",
			id, top, ranking)
	}
	if ranking[len(ranking)-1] != "pInf" {
		t.Fatalf("run=%s most-expensive +Inf pInf must rank last, got %v", id, ranking)
	}
}

// TestComparatorIsStrictWeakOrdering_WithNaN directly probes the rerank
// comparator's adherence to a strict weak ordering in the presence of a NaN
// score. For a valid less-func, for any a,b at most one of less(a,b),less(b,a)
// is true, and if both are false they are "equivalent" (must be mutually
// substitutable). We feed three providers where two are finite-equal in score
// is NOT needed; the NaN element is the trap: on unfixed prod, less(nan, x) and
// less(x, nan) are BOTH false for finite x even though x has a strictly better
// (finite) score than the NaN — so NaN is falsely "equivalent" to BOTH a good
// and a bad finite provider, which are NOT equivalent to each other
// (intransitivity). We surface this via the observable consequence: insertion
// order changes the NaN element's final rank.
//
// On unfixed prod -> FAIL (NaN rank varies). With fix -> PASS (NaN always last).
func TestComparatorIsStrictWeakOrdering_WithNaN(t *testing.T) {
	id := runUUID(t)
	prices := map[string]float64{
		"pA":   5,
		"pZ":   50,
		"pNaN": math.NaN(),
	}
	var firstNaNPos = -1
	for _, ord := range permute3([3]string{"pA", "pZ", "pNaN"}) {
		ranking, _ := buildRanked(ord, prices)
		pos := indexOf(ranking, "pNaN")
		t.Logf("run=%s insert=%v -> ranking=%v (pNaN@%d)", id, ord, ranking, pos)
		if firstNaNPos == -1 {
			firstNaNPos = pos
			continue
		}
		if pos != firstNaNPos {
			t.Fatalf("run=%s pNaN's rank position is nondeterministic across insertion "+
				"orders (%d vs %d); the rerank comparator is not a strict weak ordering "+
				"when a NaN score is present", id, pos, firstNaNPos)
		}
	}
	// And it must be LAST (worst) deterministically.
	if firstNaNPos != 2 {
		t.Fatalf("run=%s NaN-priced provider must rank LAST (pos 2), got pos %d", id, firstNaNPos)
	}
}
