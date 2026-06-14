package costsched

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
)

// runUUIDAdv is the forensic anchor for the adversarial suite (distinct from the
// baseline suite's runUUID so the two are independently traceable in logs).
const runUUIDAdv = "costsched-adv-7d2e4b10-9c3a-4f88-b6e2-aa20cc77hxc"

// oracleArgmin is an INDEPENDENT re-implementation of the documented selection
// rule ("lowest CostPerHour, lexicographic tie-break on ID") over a set of
// candidates that are ALL assumed eligible. It does NOT call into the package
// under test (no sort.Slice, no Select) — it is a hand-rolled linear scan, so a
// bug copied from the production comparator cannot hide here.
//
// Returns the winning ID and the minimum cost. Panics on empty input (callers
// guarantee non-empty).
func oracleArgmin(cands []Candidate) (string, float64) {
	if len(cands) == 0 {
		panic("oracleArgmin: empty input")
	}
	bestID := cands[0].ID
	bestCost := cands[0].CostPerHour
	for _, c := range cands[1:] {
		switch {
		case c.CostPerHour < bestCost:
			bestCost = c.CostPerHour
			bestID = c.ID
		case c.CostPerHour == bestCost && c.ID < bestID:
			// Strict lexicographic tie-break: smaller ID wins on equal cost.
			bestID = c.ID
		}
	}
	return bestID, bestCost
}

// loose is an SLA that every candidate built by these tests satisfies, so the
// SLA filter is a no-op and Select reduces to the pure cost+tie-break argmin
// that the oracle computes. (SLA filtering is already proven by the baseline
// suite; this suite isolates the selection objective.)
var loose = SLA{MinVRAMGiB: 0, MaxLatencyMs: math.MaxInt32, RequiredModel: ""}

// assertAllEligible fails loudly if any candidate does NOT meet `loose`, which
// would silently weaken every downstream assertion (a candidate filtered out
// would make the oracle and Select disagree for a legitimate reason).
func assertAllEligible(t *testing.T, cands []Candidate, sla SLA) {
	t.Helper()
	for _, c := range cands {
		if !c.Meets(sla) {
			t.Fatalf("[%s] precondition violated: candidate %q does not meet the loose SLA", runUUIDAdv, c.ID)
		}
	}
}

// TestSelectExhaustiveMatchesOracle is the load-bearing invariant: across MANY
// randomized configurations (varying node counts, clean winners, near-ties,
// exact ties, and float-precision edges), the candidate Select returns MUST
// equal the independently computed argmin (ID) AND its CostPerHour MUST equal
// the oracle minimum AND LastSelectedCost MUST record exactly that minimum.
//
// ANTI-BLUFF — why this bites:
//   - "chosen.ID == oracle ID" bites a wrong selection: flip the production
//     comparator's "<" to ">" (most expensive wins) and the chosen ID diverges
//     from the oracle's minimum on every config with a unique cheapest node.
//   - "chosen.CostPerHour == oracle min" + "LastSelectedCost == oracle min"
//     bite a selection that returns the right node but mis-records the sink-side
//     cost, and double-bite a max-selection (recorded cost would be the max).
//   - Exact-tie configs + the order-independence check below bite a dropped
//     lex tie-break: sort.Slice is NOT stable, so without the ID tie-break the
//     winner among equal-cost nodes becomes input-order-dependent and the
//     shuffled-vs-sorted assertion fails.
//
// The oracle is a separate linear scan, so it cannot share a bug with the
// sort-based production path.
func TestSelectExhaustiveMatchesOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(0x05ca1ab1e)) // fixed seed -> reproducible

	// Cost "buckets" deliberately include collisions so many configs contain
	// exact ties that exercise the lexicographic tie-break, plus values that are
	// equal-after-arithmetic to probe float behavior under the chosen rule.
	costBuckets := []float64{
		0.10, 0.10, // exact dup -> tie
		0.25,
		1.0 / 3.0,     // 0.3333...
		0.1 + 0.2,     // 0.30000000000000004 (classic FP), distinct from 0.3
		0.3,           // exactly 0.3
		0.5, 0.5, 0.5, // 3-way tie bucket
		1.25, 2.75, 3.50, // clean spread
		2.7499999999999996, // a hair below 2.75 -> strict min, near-tie
	}

	const iterations = 2000
	for iter := 0; iter < iterations; iter++ {
		n := 1 + rng.Intn(12) // 1..12 nodes
		cands := make([]Candidate, n)
		for i := 0; i < n; i++ {
			// IDs are random non-unique 4-char strings so ties on cost can ALSO
			// land on distinct IDs, forcing a real lex comparison; we guarantee
			// uniqueness of (ID) below to keep the documented rule well-defined.
			cands[i] = Candidate{
				ID:           fmt.Sprintf("n-%05d-%c", rng.Intn(1000), 'a'+rune(rng.Intn(26))),
				CostPerHour:  costBuckets[rng.Intn(len(costBuckets))],
				VRAMGiB:      64,
				MaxLatencyMs: 1,
			}
		}
		dedupeIDs(cands) // documented rule keys ties on ID; require unique IDs

		assertAllEligible(t, cands, loose)
		wantID, wantCost := oracleArgmin(cands)

		s := NewCostAwareScheduler()
		got, err := s.Select(cands, loose)
		if err != nil {
			t.Fatalf("[%s] iter %d: unexpected error %v (n=%d)", runUUIDAdv, iter, err, n)
		}

		if got.ID != wantID {
			t.Fatalf("[%s] iter %d: chosen ID=%q cost=%.17g, oracle argmin ID=%q cost=%.17g; cands=%s",
				runUUIDAdv, iter, got.ID, got.CostPerHour, wantID, wantCost, dump(cands))
		}
		if got.CostPerHour != wantCost {
			t.Fatalf("[%s] iter %d: chosen cost=%.17g != oracle min=%.17g (ID=%q); cands=%s",
				runUUIDAdv, iter, got.CostPerHour, wantCost, wantID, dump(cands))
		}
		// Independent brute-force floor: NO candidate may be strictly cheaper.
		for _, c := range cands {
			if c.CostPerHour < got.CostPerHour {
				t.Fatalf("[%s] iter %d: %q (cost=%.17g) is strictly cheaper than chosen %q (cost=%.17g)",
					runUUIDAdv, iter, c.ID, c.CostPerHour, got.ID, got.CostPerHour)
			}
		}
		// Independent lex floor: among min-cost candidates, none has a smaller ID.
		for _, c := range cands {
			if c.CostPerHour == got.CostPerHour && c.ID < got.ID {
				t.Fatalf("[%s] iter %d: %q ties chosen cost %.17g but has a smaller ID than chosen %q",
					runUUIDAdv, iter, c.ID, got.CostPerHour, got.ID)
			}
		}
		// Sink-side: recorded cost must equal the committed minimum.
		rec, ok := s.LastSelectedCost()
		if !ok {
			t.Fatalf("[%s] iter %d: LastSelectedCost not recorded after success", runUUIDAdv, iter)
		}
		if rec != wantCost {
			t.Fatalf("[%s] iter %d: LastSelectedCost=%.17g != oracle min=%.17g", runUUIDAdv, iter, rec, wantCost)
		}
	}
	t.Logf("[%s] %d randomized configs: Select == oracle argmin (cost+lex), cost & sink-side recorded min, brute-force floors held",
		runUUIDAdv, iterations)
}

// TestSelectOrderIndependent proves the choice does NOT depend on input order:
// for each config, every PERMUTATION (and many random shuffles for larger sets)
// must yield the identical chosen ID and cost.
//
// ANTI-BLUFF — why this bites: sort.Slice is NOT a stable sort. If the
// production comparator's ID tie-break were removed (return false on equal
// cost), the winner among equal-cost nodes would be whichever the unstable sort
// happens to surface — which varies with input order. This test feeds the SAME
// multiset in many orders and demands ONE answer; a dropped tie-break makes the
// answers diverge. The baseline suite's 2-element fixed-order tie test does NOT
// catch this (it never reorders), so this closes that real gap.
func TestSelectOrderIndependent(t *testing.T) {
	// Configs deliberately dominated by ties so the tie-break is the only thing
	// distinguishing the winner.
	configs := [][]Candidate{
		// All equal cost -> lex-min must win regardless of order.
		{
			{ID: "delta", CostPerHour: 1.0, VRAMGiB: 64, MaxLatencyMs: 1},
			{ID: "alpha", CostPerHour: 1.0, VRAMGiB: 64, MaxLatencyMs: 1},
			{ID: "charlie", CostPerHour: 1.0, VRAMGiB: 64, MaxLatencyMs: 1},
			{ID: "bravo", CostPerHour: 1.0, VRAMGiB: 64, MaxLatencyMs: 1},
		},
		// Two-way min tie among more-expensive distractors.
		{
			{ID: "zulu", CostPerHour: 9.0, VRAMGiB: 64, MaxLatencyMs: 1},
			{ID: "mike", CostPerHour: 2.0, VRAMGiB: 64, MaxLatencyMs: 1},
			{ID: "kilo", CostPerHour: 2.0, VRAMGiB: 64, MaxLatencyMs: 1},
			{ID: "yankee", CostPerHour: 5.0, VRAMGiB: 64, MaxLatencyMs: 1},
		},
		// Unique cheapest among ties at a higher price (tie-break must NOT steal).
		{
			{ID: "aaa", CostPerHour: 4.0, VRAMGiB: 64, MaxLatencyMs: 1},
			{ID: "bbb", CostPerHour: 4.0, VRAMGiB: 64, MaxLatencyMs: 1},
			{ID: "zzz", CostPerHour: 0.99, VRAMGiB: 64, MaxLatencyMs: 1},
		},
	}

	rng := rand.New(rand.NewSource(0xfeedface))
	for ci, base := range configs {
		assertAllEligible(t, base, loose)
		wantID, wantCost := oracleArgmin(base)

		// Exhaustive permutations for small sets; random shuffles otherwise.
		perms := permutations(base)
		const extraShuffles = 200
		for k := 0; k < extraShuffles; k++ {
			c := append([]Candidate(nil), base...)
			rng.Shuffle(len(c), func(i, j int) { c[i], c[j] = c[j], c[i] })
			perms = append(perms, c)
		}

		for pi, perm := range perms {
			s := NewCostAwareScheduler()
			got, err := s.Select(perm, loose)
			if err != nil {
				t.Fatalf("[%s] config %d perm %d: unexpected error %v", runUUIDAdv, ci, pi, err)
			}
			if got.ID != wantID || got.CostPerHour != wantCost {
				t.Fatalf("[%s] config %d perm %d: order changed the answer: got ID=%q cost=%.17g, want ID=%q cost=%.17g; order=%s",
					runUUIDAdv, ci, pi, got.ID, got.CostPerHour, wantID, wantCost, dump(perm))
			}
		}
		t.Logf("[%s] config %d: %d orderings all chose ID=%q cost=%.4f (order-independent)",
			runUUIDAdv, ci, len(perms), wantID, wantCost)
	}
}

// TestSelectAllEqualCostManyNodes hammers the all-equal case at scale: 50 nodes
// at identical cost, shuffled — the lexicographically smallest ID must win every
// time. Bites a dropped tie-break especially hard at scale (an unstable sort is
// far more likely to surface a non-min element among 50 than among 2).
func TestSelectAllEqualCostManyNodes(t *testing.T) {
	const n = 50
	base := make([]Candidate, n)
	for i := 0; i < n; i++ {
		base[i] = Candidate{ID: fmt.Sprintf("node-%02d", i), CostPerHour: 7.77, VRAMGiB: 64, MaxLatencyMs: 1}
	}
	wantID, wantCost := oracleArgmin(base) // "node-00", 7.77

	rng := rand.New(rand.NewSource(0xabad1dea))
	for trial := 0; trial < 500; trial++ {
		c := append([]Candidate(nil), base...)
		rng.Shuffle(len(c), func(i, j int) { c[i], c[j] = c[j], c[i] })
		s := NewCostAwareScheduler()
		got, err := s.Select(c, loose)
		if err != nil {
			t.Fatalf("[%s] trial %d: unexpected error %v", runUUIDAdv, trial, err)
		}
		if got.ID != wantID || got.CostPerHour != wantCost {
			t.Fatalf("[%s] trial %d: all-equal cost: got ID=%q cost=%.4f, want lex-min ID=%q cost=%.4f",
				runUUIDAdv, trial, got.ID, got.CostPerHour, wantID, wantCost)
		}
	}
	t.Logf("[%s] 500 shuffles of %d equal-cost nodes -> lex-min %q always won", runUUIDAdv, n, wantID)
}

// TestSelectSingleNode covers the n==1 edge: the sole candidate is chosen and
// its cost recorded.
func TestSelectSingleNode(t *testing.T) {
	only := Candidate{ID: "solo", CostPerHour: 1.23, VRAMGiB: 64, MaxLatencyMs: 1}
	s := NewCostAwareScheduler()
	got, err := s.Select([]Candidate{only}, loose)
	if err != nil {
		t.Fatalf("[%s] single node: unexpected error %v", runUUIDAdv, err)
	}
	if got.ID != only.ID || got.CostPerHour != only.CostPerHour {
		t.Fatalf("[%s] single node: got ID=%q cost=%.4f, want %q/%.4f", runUUIDAdv, got.ID, got.CostPerHour, only.ID, only.CostPerHour)
	}
	if rec, ok := s.LastSelectedCost(); !ok || rec != only.CostPerHour {
		t.Fatalf("[%s] single node: LastSelectedCost=%.4f ok=%v, want %.4f", runUUIDAdv, rec, ok, only.CostPerHour)
	}
}

// TestSelectEmptyReturnsErr documents the empty-input behavior: ErrNoEligible
// (the empty-eligible path), no panic, no recorded selection.
func TestSelectEmptyReturnsErr(t *testing.T) {
	s := NewCostAwareScheduler()
	got, err := s.Select(nil, loose)
	if err != ErrNoEligible {
		t.Fatalf("[%s] empty input: err=%v want ErrNoEligible (got ID=%q)", runUUIDAdv, err, got.ID)
	}
	if _, ok := s.LastSelectedCost(); ok {
		t.Fatalf("[%s] empty input: LastSelectedCost recorded despite no selection", runUUIDAdv)
	}
}

// --- test helpers (no production-code dependency) ---

// dedupeIDs rewrites IDs so they are unique while PRESERVING relative
// lexicographic order of the original IDs, so the documented "lex tie-break on
// ID" remains well-defined for randomized multisets. It assigns IDs by sorted
// position with a zero-padded prefix, guaranteeing global uniqueness and a total
// order independent of slice order.
func dedupeIDs(cands []Candidate) {
	type idx struct {
		pos int
		id  string
	}
	order := make([]idx, len(cands))
	for i := range cands {
		order[i] = idx{pos: i, id: cands[i].ID}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].id != order[j].id {
			return order[i].id < order[j].id
		}
		return order[i].pos < order[j].pos
	})
	for rank, o := range order {
		cands[o.pos].ID = fmt.Sprintf("id-%04d", rank)
	}
}

// permutations returns all orderings of cands (used only for small slices).
func permutations(cands []Candidate) [][]Candidate {
	var out [][]Candidate
	var rec func(prefix, rest []Candidate)
	rec = func(prefix, rest []Candidate) {
		if len(rest) == 0 {
			cp := append([]Candidate(nil), prefix...)
			out = append(out, cp)
			return
		}
		for i := range rest {
			next := append([]Candidate(nil), prefix...)
			next = append(next, rest[i])
			remaining := append([]Candidate(nil), rest[:i]...)
			remaining = append(remaining, rest[i+1:]...)
			rec(next, remaining)
		}
	}
	rec(nil, cands)
	return out
}

func dump(cands []Candidate) string {
	s := "["
	for i, c := range cands {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%s@%.17g", c.ID, c.CostPerHour)
	}
	return s + "]"
}
