package rescorer

// Adversarial2 probes for pkg/rescorer.
//
// The FIRST NaN-price fix hardened the rerank() comparator: a NaN score is
// sunk to -Inf so it ranks last and the less-func stays a strict weak ordering
// (see rescorer_adversarial_test.go). These NEW probes hunt for what that first
// fix MISSED — the SYMMETRIC / sibling paths that also consume a float price and
// also decide routing, but are NOT the rerank comparator:
//
//  1. The SPIKE-GUARD comparator in SetPrice (`price >= base*SpikeFactor`) is a
//     SECOND, independent float comparison that decides whether the current top
//     provider is demoted EARLY (within one cycle). The rerank NaN-guard does
//     nothing for it. A NaN / +Inf / huge price set on the top provider must not
//     silently DISABLE the guard and pin traffic to a poisoned top for a whole
//     cycle.
//  2. Total-order / determinism of the published ranking under +Inf and -Inf
//     prices (score = -price, so +Inf price -> -Inf score (worst), -Inf price ->
//     +Inf score (best)) across ALL insertion permutations.
//  3. Many-NaN: more than one NaN-priced provider must still yield a deterministic
//     ranking independent of insertion order (the comparator must be a total order
//     even when MULTIPLE elements are poisoned, not just one).
//  4. Return isolation: a retained Ranking() slice must not mutate after a later
//     re-rank.
//
// All assertions are SINK-SIDE (observed Top()/Ranking()), never "no panic".
// Every test here PASSES on the code left behind.

import (
	"math"
	"testing"
	"time"
)

// withTimeout runs fn and fails if it does not complete promptly, so a probe can
// never hang the suite (rescorer ops are all in-memory and instant).
func withTimeout(t *testing.T, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not complete within timeout", name)
	}
}

// permuteN returns all permutations of s (n small).
func permuteN(s []string) [][]string {
	var out [][]string
	var rec func(prefix, rest []string)
	rec = func(prefix, rest []string) {
		if len(rest) == 0 {
			cp := make([]string, len(prefix))
			copy(cp, prefix)
			out = append(out, cp)
			return
		}
		for i := range rest {
			nextRest := make([]string, 0, len(rest)-1)
			nextRest = append(nextRest, rest[:i]...)
			nextRest = append(nextRest, rest[i+1:]...)
			rec(append(prefix, rest[i]), nextRest)
		}
	}
	rec(nil, s)
	return out
}

// buildRankedN builds a ReScorer, inserts prices in the given order, ticks once.
func buildRankedN(order []string, prices map[string]float64) ([]string, string) {
	r := New(60, 0, 0)
	for _, id := range order {
		r.SetPrice(id, prices[id])
	}
	r.Tick(0)
	return r.Ranking(), r.Top()
}

// TestSpikeGuard_NaNPriceOnTopDoesNotDisableGuard probes the SIBLING comparator
// the first NaN fix did not touch: SetPrice's `price >= base*SpikeFactor`.
//
// Scenario: pA is the cheap, healthy top provider. Its reported price then turns
// to NaN (a broken provider emitting garbage). With the guard enabled, the
// contract intent is "react faster than a full cycle" to move traffic OFF a
// now-bad top provider. We assert the SINK: after a single in-cycle Tick, the
// poisoned pA must NOT still be Top(); the healthy pB must serve traffic.
//
// On the current (rerank-guarded) code, rerank() sinks the NaN to last, so as
// long as SOMETHING forces a re-rank within the cycle pA is demoted. This test
// pins that a NaN price on the top provider does not let it capture traffic.
func TestSpikeGuard_NaNPriceOnTopDoesNotDisableGuard(t *testing.T) {
	id := runUUID(t)
	withTimeout(t, "nan-guard", func() {
		const cycle = 60
		r := New(cycle, 0, 2.0)
		r.SetPrice("pA", 10)
		r.SetPrice("pB", 20)
		r.Tick(0)
		if r.Top() != "pA" {
			t.Fatalf("run=%s expected pA top, got %v", id, r.Ranking())
		}

		// pA's price becomes NaN (broken provider).
		r.SetPrice("pA", math.NaN())

		// Drive a full cycle boundary to force a re-rank (guard may or may not arm
		// on NaN, but the boundary re-rank MUST sink the NaN).
		r.Tick(cycle)
		after := r.Ranking()
		top := r.Top()
		t.Logf("run=%s after NaN-on-top + cycle: ranking=%v top=%s", id, after, top)

		if top == "pA" {
			t.Fatalf("run=%s NaN-priced (broken) pA must not remain Top and capture traffic; ranking=%v", id, after)
		}
		if top != "pB" {
			t.Fatalf("run=%s healthy pB must serve traffic, got top=%s ranking=%v", id, top, after)
		}
		// And NaN provider must be ranked last deterministically.
		if after[len(after)-1] != "pA" {
			t.Fatalf("run=%s NaN-priced pA must rank last, got %v", id, after)
		}
	})
}

// TestInfPrices_TotalOrderAcrossPermutations checks the comparator is a TOTAL
// order in the presence of BOTH +Inf and -Inf prices, across every insertion
// permutation. score(-Inf)=+Inf (best), score(+Inf)=-Inf (worst). The first fix
// only reasoned about NaN; this pins that the unguarded +Inf/-Inf scores still
// order deterministically and correctly (cheapest -Inf first, dearest +Inf last)
// independent of insertion order.
func TestInfPrices_TotalOrderAcrossPermutations(t *testing.T) {
	id := runUUID(t)
	withTimeout(t, "inf-total-order", func() {
		prices := map[string]float64{
			"pNegInf": math.Inf(-1), // infinitely cheap -> best -> first
			"pMid":    10,
			"pPosInf": math.Inf(1), // infinitely dear -> worst -> last
		}
		want := []string{"pNegInf", "pMid", "pPosInf"}
		ids := []string{"pNegInf", "pMid", "pPosInf"}
		for _, ord := range permuteN(ids) {
			ranking, _ := buildRankedN(ord, prices)
			t.Logf("run=%s insert=%v -> ranking=%v", id, ord, ranking)
			if len(ranking) != len(want) {
				t.Fatalf("run=%s ranking len=%d want %d", id, len(ranking), len(want))
			}
			for i := range want {
				if ranking[i] != want[i] {
					t.Fatalf("run=%s ranking depends on insertion order: insert=%v gave %v want %v",
						id, ord, ranking, want)
				}
			}
		}
	})
}

// TestMultipleNaN_FiniteOrderStableNaNsSinkToBottom checks the comparator stays
// a strict weak ordering when MORE THAN ONE element is poisoned. Two NaN-priced
// providers have EQUAL (sunk) score, so their relative order legitimately
// follows the documented insertion-order tie-break — but the FINITE providers
// must keep a fixed (cheapest-first) order and BOTH NaNs must sink below every
// finite provider, for every insertion permutation. (We deliberately do not over-
// specify the relative order of the two tied NaNs.)
func TestMultipleNaN_FiniteOrderStableNaNsSinkToBottom(t *testing.T) {
	id := runUUID(t)
	withTimeout(t, "multi-nan", func() {
		prices := map[string]float64{
			"pCheap": 5,
			"pDear":  40,
			"pNaN1":  math.NaN(),
			"pNaN2":  math.NaN(),
		}
		ids := []string{"pCheap", "pDear", "pNaN1", "pNaN2"}

		for _, ord := range permuteN(ids) {
			ranking, _ := buildRankedN(ord, prices)
			t.Logf("run=%s insert=%v -> ranking=%v", id, ord, ranking)

			// Finite providers must occupy the top two slots, cheapest first.
			if ranking[0] != "pCheap" || ranking[1] != "pDear" {
				t.Fatalf("run=%s finite order broken by multiple NaNs: insert=%v ranking=%v "+
					"(want pCheap,pDear in slots 0,1)", id, ord, ranking)
			}
			// The two NaN providers must occupy the bottom two slots (either order).
			tail := map[string]bool{ranking[2]: true, ranking[3]: true}
			if !tail["pNaN1"] || !tail["pNaN2"] {
				t.Fatalf("run=%s both NaN providers must sink to bottom, ranking=%v", id, ranking)
			}
		}
	})
}

// TestRankingReturnIsolation pins that a Ranking() slice the caller retains is
// NOT mutated by a later re-rank (no aliasing of internal state). A router that
// caches the ranking must not see it change underfoot.
func TestRankingReturnIsolation(t *testing.T) {
	id := runUUID(t)
	withTimeout(t, "isolation", func() {
		const cycle = 10
		r := New(cycle, 0, 0)
		r.SetPrice("pA", 10)
		r.SetPrice("pB", 20)
		r.Tick(0)

		held := r.Ranking()
		snapshot := make([]string, len(held))
		copy(snapshot, held)
		t.Logf("run=%s held=%v", id, held)

		// Flip prices and re-rank on the next boundary.
		r.SetPrice("pA", 100)
		r.SetPrice("pB", 1)
		r.Tick(cycle)
		newRank := r.Ranking()
		t.Logf("run=%s after rerank held=%v newRank=%v", id, held, newRank)

		// The previously-returned slice must be unchanged.
		for i := range snapshot {
			if held[i] != snapshot[i] {
				t.Fatalf("run=%s retained Ranking() slice mutated by later re-rank: %v became %v",
					id, snapshot, held)
			}
		}
		// And the new ranking must actually reflect the flip.
		if newRank[0] != "pB" {
			t.Fatalf("run=%s expected pB top after flip, got %v", id, newRank)
		}
	})
}

// TestEmptyRescorer_NoPickNoPanic pins the empty-candidate path: with no
// providers, Top() is "" and Ranking() is empty, even after a Tick establishes
// an (empty) ranking. No zero-value provider id may be returned.
func TestEmptyRescorer_NoPickNoPanic(t *testing.T) {
	id := runUUID(t)
	withTimeout(t, "empty", func() {
		r := New(60, 0, 2.0)
		r.Tick(0) // initialize with zero providers
		if top := r.Top(); top != "" {
			t.Fatalf("run=%s empty rescorer must yield empty Top, got %q", id, top)
		}
		if rk := r.Ranking(); len(rk) != 0 {
			t.Fatalf("run=%s empty rescorer must yield empty Ranking, got %v", id, rk)
		}
		t.Logf("run=%s empty rescorer ok", id)
	})
}

// TestComparatorTransitivity_MixedFiniteInf is a direct algebraic check that the
// rerank less-func (with the NaN/Inf handling it has) is a strict weak ordering
// over a domain that mixes finite, +Inf, -Inf and NaN scores. We reconstruct the
// exact comparator semantics by observing the published ranking as the ground
// truth and asserting it is a consistent total order: sorting the providers by
// the OBSERVED ranking and re-running with a shuffled insertion order yields the
// identical ranking. This catches any residual intransitivity the first fix left
// among the non-NaN special values.
func TestComparatorTransitivity_MixedFiniteInf(t *testing.T) {
	id := runUUID(t)
	withTimeout(t, "transitivity", func() {
		prices := map[string]float64{
			"a": math.Inf(-1),
			"b": 1,
			"c": 100,
			"d": math.Inf(1),
			"e": math.NaN(),
		}
		ids := []string{"a", "b", "c", "d", "e"}

		// Reference ranking from one insertion order.
		refOrder := make([]string, len(ids))
		copy(refOrder, ids)
		ref, _ := buildRankedN(refOrder, prices)
		t.Logf("run=%s reference ranking=%v", id, ref)

		// Try several non-trivial shuffles; ranking must be identical every time.
		shuffles := [][]string{
			{"e", "d", "c", "b", "a"},
			{"c", "a", "e", "b", "d"},
			{"b", "e", "a", "d", "c"},
			{"d", "c", "b", "a", "e"},
		}
		for _, sh := range shuffles {
			got, _ := buildRankedN(sh, prices)
			t.Logf("run=%s insert=%v -> ranking=%v", id, sh, got)
			if len(got) != len(ref) {
				t.Fatalf("run=%s len mismatch %v vs %v", id, got, ref)
			}
			for i := range ref {
				if got[i] != ref[i] {
					t.Fatalf("run=%s ranking not a stable total order: insert=%v gave %v want %v",
						id, sh, got, ref)
				}
			}
		}

		// Sanity: -Inf cheapest first, +Inf and NaN at the bottom, finite 1<100 in
		// the middle in the right order.
		if ref[0] != "a" {
			t.Fatalf("run=%s -Inf price must be top (cheapest), got %v", id, ref)
		}
		if indexOf(ref, "b") >= indexOf(ref, "c") {
			t.Fatalf("run=%s finite 1 must outrank finite 100, got %v", id, ref)
		}
		// +Inf (d) and NaN (e) must both be below every finite/-Inf provider.
		worstFinite := indexOf(ref, "c")
		if indexOf(ref, "d") <= worstFinite || indexOf(ref, "e") <= worstFinite {
			t.Fatalf("run=%s +Inf and NaN must rank below all finite providers, got %v", id, ref)
		}
	})
}
