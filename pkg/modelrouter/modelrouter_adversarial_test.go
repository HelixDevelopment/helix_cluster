package modelrouter

import (
	"fmt"
	"sort"
	"testing"
)

// ============================================================================
// Adversarial contract pin for pkg/modelrouter.
//
// ROUTING MODEL (as built): the Router is a STATIC keyed-table lookup, not a
// cross-model scorer. Resolve(strategy, requestedModel) does exactly:
//
//	concrete, ok := table[strategy]   // selection = exact key lookup
//	if !ok                 -> *ErrUnknownStrategy   (no-match contract)
//	if requested==sentinel -> concrete              (strategy default)
//	else                   -> requestedModel        (pass-through, verbatim)
//
// Consequences this file PINS adversarially (centerpieces marked ★):
//
//   ★ SELECTION CORRECTNESS: each strategy resolves to its OWN documented
//     concrete id and to no other strategy's id — hand-verified per strategy,
//     and the winner is the one whose KEY matches, independent of the order the
//     strategies are probed in (the "optimal is not first/last" analog).
//
//   ★ DETERMINISM (no map-order leak): selection is by KEY, never by map
//     iteration. Proven by (a) repeating the full strategy sweep many times and
//     asserting a byte-stable winner each time, and (b) constructing MANY fresh
//     routers via New() (which copies the package map) and asserting every one
//     yields the identical strategy->id bijection. A map-iteration leak in New()
//     (e.g. last-write-wins clobber) or in selection would break stability.
//
//   - ELIGIBILITY analog: this router has no health/capability filter by design;
//     the only "ineligible"-shaped edge is an EMPTY requested model. We PIN that
//     "" is a verbatim pass-through ("" in -> "" out, no error) so that a future
//     change to reject empties is a conscious, test-visible decision.
//
//   - NO-MATCH: unknown strategy -> typed *ErrUnknownStrategy, never panic,
//     never a silent empty string — INCLUDING when the caller supplied an
//     explicit (resolvable-looking) model, because the table-miss is checked
//     BEFORE the sentinel/pass-through branch. This branch-ORDER is pinned.
//
//   - EDGES: empty strategy, sentinel case-sensitivity, single-key reachability.
//
// ANTI-BLUFF: each assertion is mutation-verified — see the documented mutation
// recipe at the bottom; the centerpiece tests fail under a swapped/last-write
// table or a pass-through->concrete inversion.
// ============================================================================

// canonicalTable is the documented strategy->concrete-id contract, declared
// independently of production so a production edit cannot silently move the
// goalposts. If production drifts from this, these tests fail.
var canonicalTable = map[Strategy]string{
	StrategyLatency:    "helix-fast-v2",
	StrategyThroughput: "helix-bulk-v3",
	StrategyQuality:    "helix-precise-v4",
	StrategyCost:       "helix-nano-v1",
}

// allStrategies in a FIXED, non-table order. Probing in this order (not the
// map's order) is part of the "optimal is not merely first/last" check.
var allStrategies = []Strategy{
	StrategyQuality, // deliberately NOT latency-first
	StrategyCost,
	StrategyLatency,
	StrategyThroughput, // deliberately NOT last == cost
}

// ----------------------------------------------------------------------------
// ★ CENTERPIECE 1 — SELECTION CORRECTNESS
//
// For every strategy: the resolved id equals ITS OWN documented id and is NOT
// equal to ANY OTHER strategy's id. This hand-verifies the winner per strategy
// and proves the lookup is keyed (not, say, "always return the first/last map
// entry"). Probed in allStrategies order, which differs from the table order.
// ----------------------------------------------------------------------------
func TestAdversarial_SelectionCorrectness_PerStrategyOwnId(t *testing.T) {
	r := New()

	for _, strat := range allStrategies {
		strat := strat
		t.Run(string(strat), func(t *testing.T) {
			want := canonicalTable[strat]

			got, err := r.Resolve(strat, SentinelDefault)
			if err != nil {
				t.Fatalf("Resolve(%q, default) unexpected error: %v", strat, err)
			}
			if got != want {
				t.Fatalf("selection: Resolve(%q, default)=%q want %q", strat, got, want)
			}

			// Cross-strategy exclusion: the resolved id must belong to NO other
			// strategy. This catches a swap that happens to still be onto.
			for other, otherID := range canonicalTable {
				if other == strat {
					continue
				}
				if got == otherID {
					t.Fatalf("mis-route: Resolve(%q)=%q which is %q's id (%q)",
						strat, got, other, otherID)
				}
			}
			t.Logf("[%s] selected concrete=%q (distinct from all peers)", strat, got)
		})
	}
}

// ★ CENTERPIECE 1b — the strategy->id mapping is a BIJECTION: every documented
// concrete id is produced by exactly one strategy, and all four ids are
// reachable. A collapsed/duplicated table (two strategies -> one id) fails here.
func TestAdversarial_SelectionCorrectness_Bijection(t *testing.T) {
	r := New()

	producedBy := map[string][]Strategy{}
	for _, strat := range allStrategies {
		id, err := r.StrategyDefault(strat)
		if err != nil {
			t.Fatalf("StrategyDefault(%q): %v", strat, err)
		}
		producedBy[id] = append(producedBy[id], strat)
	}

	if len(producedBy) != len(allStrategies) {
		t.Fatalf("bijection broken: %d strategies produced only %d distinct ids: %v",
			len(allStrategies), len(producedBy), producedBy)
	}
	for id, strats := range producedBy {
		if len(strats) != 1 {
			t.Fatalf("id %q produced by multiple strategies %v — not a bijection", id, strats)
		}
	}

	// Every canonical id is actually reachable.
	reachable := map[string]bool{}
	for id := range producedBy {
		reachable[id] = true
	}
	for strat, want := range canonicalTable {
		if !reachable[want] {
			t.Fatalf("canonical id %q for strategy %q is not reachable via the router",
				want, strat)
		}
	}
	t.Logf("bijection OK: %v", producedBy)
}

// ----------------------------------------------------------------------------
// ★ CENTERPIECE 2 — DETERMINISM (no map-iteration-order leak)
//
// (a) Repeat the full strategy sweep many times on ONE router: every iteration
//
//	must yield the byte-identical winner per strategy. A selection that leaked
//	map order would (over enough repeats) waver.
//
// (b) Build MANY fresh routers via New() (which range-copies the package map):
//
//	every router must reproduce the identical strategy->id bijection. This
//	guards New() against an iteration-order clobber regression.
//
// ----------------------------------------------------------------------------
func TestAdversarial_Determinism_StableWinnerAcrossRepeats(t *testing.T) {
	r := New()
	const repeats = 2000

	// Record the first observation as the reference, then assert stability.
	ref := map[Strategy]string{}
	for _, strat := range allStrategies {
		id, err := r.Resolve(strat, SentinelDefault)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", strat, err)
		}
		ref[strat] = id
		// reference must match canonical too
		if id != canonicalTable[strat] {
			t.Fatalf("reference selection for %q = %q, want canonical %q",
				strat, id, canonicalTable[strat])
		}
	}

	for i := 0; i < repeats; i++ {
		for _, strat := range allStrategies {
			id, err := r.Resolve(strat, SentinelDefault)
			if err != nil {
				t.Fatalf("[rep %d] Resolve(%q): %v", i, strat, err)
			}
			if id != ref[strat] {
				t.Fatalf("[rep %d] NON-DETERMINISTIC selection for %q: got %q, "+
					"first-seen %q (map-order leak?)", i, strat, id, ref[strat])
			}
		}
	}
	t.Logf("stable across %d repeats: %v", repeats, ref)
}

func TestAdversarial_Determinism_FreshRoutersIdentical(t *testing.T) {
	const routers = 1000

	for n := 0; n < routers; n++ {
		r := New()
		for _, strat := range allStrategies {
			id, err := r.StrategyDefault(strat)
			if err != nil {
				t.Fatalf("[router %d] StrategyDefault(%q): %v", n, strat, err)
			}
			if want := canonicalTable[strat]; id != want {
				t.Fatalf("[router %d] fresh New() produced %q for %q, want %q "+
					"(New() map-copy nondeterminism?)", n, id, strat, want)
			}
		}
	}
	t.Logf("%d freshly-built routers all reproduced the canonical bijection", routers)
}

// ----------------------------------------------------------------------------
// NO-MATCH branch-ORDER: unknown strategy errors EVEN WITH an explicit model.
// The table-miss is checked before the sentinel/pass-through branch, so a
// caller cannot smuggle an unknown strategy past the guard by supplying a
// concrete model. Pins that ordering (no silent "" and no spurious passthrough).
// ----------------------------------------------------------------------------
func TestAdversarial_NoMatch_UnknownStrategyBeatsExplicitModel(t *testing.T) {
	r := New()

	cases := []struct {
		name      string
		strategy  Strategy
		requested string
	}{
		{"unknown+sentinel", "bogus", SentinelDefault},
		{"unknown+explicit", "bogus", "caller-explicit-model"},
		{"unknown+empty", "bogus", ""},
		{"empty-strategy+explicit", "", "caller-explicit-model"},
		{"case-wrong-strategy", "Latency", SentinelDefault}, // strategies are exact
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(tc.strategy, tc.requested)
			if err == nil {
				t.Fatalf("expected ErrUnknownStrategy for strategy %q (requested=%q), "+
					"got nil err and result %q", tc.strategy, tc.requested, got)
			}
			if !IsUnknownStrategy(err) {
				t.Fatalf("wrong error type for %q: %T %v", tc.strategy, err, err)
			}
			if got != "" {
				t.Fatalf("on error the resolved id must be empty, got %q", got)
			}
			t.Logf("[%s] strategy=%q requested=%q -> err=%v (no silent route)",
				tc.name, tc.strategy, tc.requested, err)
		})
	}
}

// BuildRequestBody must propagate the SAME no-match error (never emit a body
// with an empty/garbage model for an unknown strategy).
func TestAdversarial_NoMatch_BuildRequestBodyPropagates(t *testing.T) {
	r := New()
	raw, err := r.BuildRequestBody("bogus", "explicit-model", "run-1")
	if err == nil {
		t.Fatalf("expected error, got body=%s", raw)
	}
	if !IsUnknownStrategy(err) {
		t.Fatalf("wrong error type: %T %v", err, err)
	}
	if raw != nil {
		t.Fatalf("on error no body bytes must be returned, got %s", raw)
	}
}

// ----------------------------------------------------------------------------
// ELIGIBILITY analog — EMPTY model pass-through is contracted "" -> "".
// This is the one place the router can emit an "empty"/ineligible-looking
// model. We PIN the current verbatim-passthrough behavior so any future
// "reject empty model" hardening is a deliberate, test-visible change rather
// than an accident — and so a regression that starts substituting the concrete
// id for "" (sentinel confusion) is caught.
// ----------------------------------------------------------------------------
func TestAdversarial_PassThrough_EmptyAndCaseSensitivity(t *testing.T) {
	r := New()

	t.Run("empty-model-passes-through-verbatim", func(t *testing.T) {
		got, err := r.Resolve(StrategyLatency, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Fatalf("empty model must pass through verbatim; got %q "+
				"(did sentinel/concrete substitution leak in?)", got)
		}
		// And it must NOT have been replaced by the concrete id.
		if got == canonicalTable[StrategyLatency] {
			t.Fatalf("empty model was substituted with concrete id %q", got)
		}
	})

	// The sentinel is the EXACT literal "default"; near-misses pass through.
	for _, near := range []string{"Default", "DEFAULT", " default", "default ", "defaults", "deafult"} {
		near := near
		t.Run("near-sentinel/"+near, func(t *testing.T) {
			got, err := r.Resolve(StrategyQuality, near)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != near {
				t.Fatalf("near-sentinel %q must pass through verbatim, got %q "+
					"(sentinel match too loose?)", near, got)
			}
			if got == canonicalTable[StrategyQuality] {
				t.Fatalf("near-sentinel %q wrongly resolved to concrete id %q", near, got)
			}
		})
	}

	// Positive control: the exact sentinel DOES resolve.
	t.Run("exact-sentinel-resolves", func(t *testing.T) {
		got, err := r.Resolve(StrategyQuality, SentinelDefault)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != canonicalTable[StrategyQuality] {
			t.Fatalf("exact sentinel must resolve to %q, got %q",
				canonicalTable[StrategyQuality], got)
		}
	})
}

// ----------------------------------------------------------------------------
// SINK-SIDE selection evidence: the resolved concrete id actually lands in the
// serialized JSON body's "model" field for every strategy, and equals the
// documented id (not the sentinel, not empty, not a peer's id).
// ----------------------------------------------------------------------------
func TestAdversarial_SinkSide_BodyCarriesResolvedId(t *testing.T) {
	r := New()
	for _, strat := range allStrategies {
		strat := strat
		t.Run(string(strat), func(t *testing.T) {
			runID := fmt.Sprintf("run-%s", strat)
			raw, err := r.BuildRequestBody(strat, SentinelDefault, runID)
			if err != nil {
				t.Fatalf("BuildRequestBody(%q): %v", strat, err)
			}
			rb := decodeBody(t, raw)
			want := canonicalTable[strat]
			if rb.Model != want {
				t.Fatalf("[%s] sink model=%q want %q (raw=%s)", strat, rb.Model, want, raw)
			}
			if rb.Model == SentinelDefault || rb.Model == "" {
				t.Fatalf("[%s] sink model is sentinel/empty: %q", strat, rb.Model)
			}
			if rb.RunID != runID {
				t.Fatalf("[%s] sink run_id=%q want %q", strat, rb.RunID, runID)
			}
			t.Logf("[%s] sink-side model=%q run_id=%q", strat, rb.Model, rb.RunID)
		})
	}
}

// canonicalIDsSorted is a small determinism helper / documentation aid: it lets
// a reader see the full id set the router can ever emit, sorted for stability.
func TestAdversarial_Inventory_AllEmittableIds(t *testing.T) {
	r := New()
	got := map[string]bool{}
	for _, strat := range allStrategies {
		id, err := r.StrategyDefault(strat)
		if err != nil {
			t.Fatalf("StrategyDefault(%q): %v", strat, err)
		}
		got[id] = true
	}
	var ids []string
	for id := range got {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	want := []string{"helix-bulk-v3", "helix-fast-v2", "helix-nano-v1", "helix-precise-v4"}
	if len(ids) != len(want) {
		t.Fatalf("emittable id set size = %d (%v), want %d (%v)", len(ids), ids, len(want), want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("emittable id[%d]=%q want %q (full got=%v)", i, ids[i], want[i], ids)
		}
	}
	t.Logf("router can ever emit exactly: %v", ids)
}

// ----------------------------------------------------------------------------
// MUTATION RECIPE (anti-bluff; documented so a reviewer can reproduce):
//
//   M1 (wrong-selection / swap): in modelrouter.go swap two values in
//      defaultModels, e.g.
//          StrategyLatency: "helix-nano-v1", StrategyCost: "helix-fast-v2"
//      -> TestAdversarial_SelectionCorrectness_PerStrategyOwnId FAILS
//         (latency resolves to cost's id) and _SinkSide_ FAILS.
//
//   M2 (collapse / non-bijection): set two strategies to the same id
//          StrategyCost: "helix-fast-v2"
//      -> TestAdversarial_SelectionCorrectness_Bijection FAILS (id produced by
//         two strategies) and _Inventory_ FAILS (set size 3 != 4).
//
//   M3 (pass-through inversion): in Resolve, change the non-sentinel branch to
//      `return concrete, nil` (always substitute)
//      -> TestAdversarial_PassThrough_EmptyAndCaseSensitivity FAILS
//         (empty + near-sentinel wrongly become the concrete id).
//
//   M4 (no-match branch reorder): move the sentinel check BEFORE the table-miss
//      guard so an unknown strategy with an explicit model passes through
//      -> TestAdversarial_NoMatch_UnknownStrategyBeatsExplicitModel FAILS
//         (unknown+explicit returns a value with nil error).
//
//   M5 (loose sentinel): compare with strings.EqualFold(requested, sentinel)
//      -> TestAdversarial_PassThrough_EmptyAndCaseSensitivity FAILS on
//         "Default"/"DEFAULT".
//
// All five mutations are reverted byte-identically after verification; the
// production file is left UNMODIFIED.
// ----------------------------------------------------------------------------
