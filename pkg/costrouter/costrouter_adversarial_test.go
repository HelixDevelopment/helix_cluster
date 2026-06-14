package costrouter

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// oracleMin recomputes the expected winner directly from the inputs for the
// given workload type, using the SAME documented contract: minimize the
// objective (latency for Inference, cost for Training), break ties by ascending
// (lexicographic) provider ID. It is deliberately written differently from the
// production hot loop — it first finds the minimum objective value, then selects
// the smallest ID among all providers that attain it — so an order-dependent or
// inverted production bug cannot be masked by a structurally identical oracle.
func oracleMin(t WorkloadType, ps []Provider) Provider {
	field := func(p Provider) float64 {
		if t == Inference {
			return p.LatencyMs
		}
		return p.CostPerHour
	}
	minVal := field(ps[0])
	for _, p := range ps[1:] {
		if field(p) < minVal {
			minVal = field(p)
		}
	}
	winner := ""
	var win Provider
	for _, p := range ps {
		if field(p) == minVal {
			if winner == "" || p.ID < winner {
				winner, win = p.ID, p
			}
		}
	}
	return win
}

// permutations returns every ordering of the input slice. Used to prove the
// selection is fully order-independent (a missing/weak deterministic tie-break
// would leak input order — the same nondeterminism class as Go map ranging).
func permutations(ps []Provider) [][]Provider {
	var out [][]Provider
	var rec func(prefix, rest []Provider)
	rec = func(prefix, rest []Provider) {
		if len(rest) == 0 {
			cp := make([]Provider, len(prefix))
			copy(cp, prefix)
			out = append(out, cp)
			return
		}
		for i := range rest {
			next := make([]Provider, 0, len(rest)-1)
			next = append(next, rest[:i]...)
			next = append(next, rest[i+1:]...)
			rec(append(prefix, rest[i]), next)
		}
	}
	rec(nil, ps)
	return out
}

// TestCostOptimality_CheapestNotFirstNotLast is the cost-optimality centerpiece.
//
// Over many hand-built tables — including ones where the optimal candidate is
// NEITHER first NOR last in the slice — Route must return the minimum-objective
// candidate, hand-verified against an independent oracle. This bites an inverted
// min/max or a >=/<= flip: the wrong (non-optimal) provider would be returned.
func TestCostOptimality_CheapestNotFirstNotLast(t *testing.T) {
	id := runUUID(t)

	cases := []struct {
		name     string
		wl       WorkloadType
		table    []Provider
		wantID   string // hand-computed winner
		optIndex int    // position of the optimum in the slice (for the "not first/last" assertion)
	}{
		{
			name: "training-cheapest-in-middle",
			wl:   Training,
			table: []Provider{
				{ID: "a", CostPerHour: 7.0, LatencyMs: 10},
				{ID: "b", CostPerHour: 2.0, LatencyMs: 99}, // cheapest, middle
				{ID: "c", CostPerHour: 5.0, LatencyMs: 20},
			},
			wantID:   "b",
			optIndex: 1,
		},
		{
			name: "inference-fastest-in-middle",
			wl:   Inference,
			table: []Provider{
				{ID: "a", CostPerHour: 1.0, LatencyMs: 80},
				{ID: "b", CostPerHour: 9.0, LatencyMs: 5}, // fastest, middle
				{ID: "c", CostPerHour: 2.0, LatencyMs: 60},
			},
			wantID:   "b",
			optIndex: 1,
		},
		{
			name: "training-cheapest-deep-middle-of-five",
			wl:   Training,
			table: []Provider{
				{ID: "p0", CostPerHour: 8.0, LatencyMs: 11},
				{ID: "p1", CostPerHour: 6.0, LatencyMs: 12},
				{ID: "p2", CostPerHour: 0.5, LatencyMs: 13}, // cheapest, deep middle
				{ID: "p3", CostPerHour: 6.0, LatencyMs: 14},
				{ID: "p4", CostPerHour: 8.0, LatencyMs: 15},
			},
			wantID:   "p2",
			optIndex: 2,
		},
		{
			name: "training-zero-cost-wins",
			wl:   Training,
			table: []Provider{
				{ID: "x", CostPerHour: 3.0, LatencyMs: 10},
				{ID: "free", CostPerHour: 0.0, LatencyMs: 50}, // zero cost
				{ID: "y", CostPerHour: 1.0, LatencyMs: 5},
			},
			wantID:   "free",
			optIndex: 1,
		},
		{
			name: "training-negative-cost-wins", // credit/rebate edge
			wl:   Training,
			table: []Provider{
				{ID: "x", CostPerHour: 1.0, LatencyMs: 10},
				{ID: "credit", CostPerHour: -2.5, LatencyMs: 99}, // negative cost
				{ID: "y", CostPerHour: 0.0, LatencyMs: 5},
			},
			wantID:   "credit",
			optIndex: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Sanity: the declared optimum is neither first nor last where claimed.
			if tc.optIndex == 0 || tc.optIndex == len(tc.table)-1 {
				t.Fatalf("run=%s case %s: optimum at boundary index %d defeats the purpose", id, tc.name, tc.optIndex)
			}
			want := oracleMin(tc.wl, tc.table)
			if want.ID != tc.wantID {
				t.Fatalf("run=%s case %s: oracle/hand mismatch oracle=%s hand=%s", id, tc.name, want.ID, tc.wantID)
			}
			t.Logf("run=%s case=%s BEFORE table=%+v want=%s", id, tc.name, tc.table, tc.wantID)
			got, err := Route(tc.wl, tc.table)
			if err != nil {
				t.Fatalf("run=%s case %s: unexpected err %v", id, tc.name, err)
			}
			t.Logf("run=%s case=%s AFTER got=%s", id, tc.name, got.ID)
			if got.ID != tc.wantID {
				t.Fatalf("run=%s case %s: got %s, want optimal %s (non-optimal selection)", id, tc.name, got.ID, tc.wantID)
			}
		})
	}
}

// TestDeterministicTieBreak_AllOrderings is the deterministic-tie-break
// centerpiece. For a table with an exact-cost / exact-latency tie, Route must
// return the SAME smallest-ID winner regardless of input ordering. We enumerate
// EVERY permutation of the slice and also shuffle large random tables many
// times. If the production tie-break were dropped (so first-seen / arbitrary
// order decides), at least one ordering would return a different winner and this
// fails — pinning the determinism contract the package documents.
func TestDeterministicTieBreak_AllOrderings(t *testing.T) {
	id := runUUID(t)

	// Five providers ALL tied on cost (Training) and a separate tie on latency
	// (Inference), with IDs intentionally NOT in slice order.
	base := []Provider{
		{ID: "n", CostPerHour: 4.0, LatencyMs: 30.0},
		{ID: "a", CostPerHour: 4.0, LatencyMs: 30.0}, // smallest ID -> must always win both
		{ID: "z", CostPerHour: 4.0, LatencyMs: 30.0},
		{ID: "c", CostPerHour: 4.0, LatencyMs: 30.0},
		{ID: "m", CostPerHour: 4.0, LatencyMs: 30.0},
	}
	const wantID = "a"

	perms := permutations(base)
	t.Logf("run=%s enumerating %d permutations; want winner=%s for both objectives", id, len(perms), wantID)

	for _, wl := range []WorkloadType{Inference, Training} {
		for i, perm := range perms {
			got, err := Route(wl, perm)
			if err != nil {
				t.Fatalf("run=%s wl=%s perm#%d err: %v", id, wl, i, err)
			}
			if got.ID != wantID {
				t.Fatalf("run=%s wl=%s perm#%d order=%s -> got %s, want deterministic %s (tie-break leaked input order)",
					id, wl, i, idsOf(perm), got.ID, wantID)
			}
		}
	}

	// Larger random table with a guaranteed tie cluster at the optimum, shuffled
	// many times: the smallest-ID member of the optimal cluster must always win.
	rng := rand.New(rand.NewSource(0xC0FFEE))
	big := make([]Provider, 0, 24)
	// Optimal cluster: cost 1.0, several members; smallest ID "opt-00".
	for k := 0; k < 6; k++ {
		big = append(big, Provider{ID: fmt.Sprintf("opt-%02d", k), CostPerHour: 1.0, LatencyMs: 7.0})
	}
	// Distractors strictly worse on cost AND latency.
	for k := 0; k < 18; k++ {
		big = append(big, Provider{ID: fmt.Sprintf("d-%02d", k), CostPerHour: 2.0 + float64(k), LatencyMs: 20.0 + float64(k)})
	}
	const wantBigCost = "opt-00" // smallest ID at min cost
	const wantBigLat = "opt-00"  // also smallest ID at min latency
	for iter := 0; iter < 200; iter++ {
		rng.Shuffle(len(big), func(i, j int) { big[i], big[j] = big[j], big[i] })
		gc, err := Route(Training, big)
		if err != nil || gc.ID != wantBigCost {
			t.Fatalf("run=%s iter=%d Training got %s/%v, want %s", id, iter, gc.ID, err, wantBigCost)
		}
		gl, err := Route(Inference, big)
		if err != nil || gl.ID != wantBigLat {
			t.Fatalf("run=%s iter=%d Inference got %s/%v, want %s", id, iter, gl.ID, err, wantBigLat)
		}
	}
	t.Logf("run=%s determinism: all permutations + 200 shuffles stable on smallest-ID winner", id)
}

// TestEdges_EmptySingleAndError pins the boundary behavior: empty table errors
// (no panic), single-candidate routes to that candidate, and nil is handled.
func TestEdges_EmptySingleAndError(t *testing.T) {
	id := runUUID(t)

	for _, wl := range []WorkloadType{Inference, Training} {
		if _, err := Route(wl, nil); !errors.Is(err, ErrNoProviders) {
			t.Fatalf("run=%s wl=%s nil table: got %v, want ErrNoProviders", id, wl, err)
		}
		if _, err := Route(wl, []Provider{}); !errors.Is(err, ErrNoProviders) {
			t.Fatalf("run=%s wl=%s empty table: got %v, want ErrNoProviders", id, wl, err)
		}
		single := []Provider{{ID: "solo", CostPerHour: 42.0, LatencyMs: 1.0}}
		got, err := Route(wl, single)
		if err != nil {
			t.Fatalf("run=%s wl=%s single: unexpected err %v", id, wl, err)
		}
		if got.ID != "solo" {
			t.Fatalf("run=%s wl=%s single: got %s, want solo", id, wl, got.ID)
		}
	}
	t.Logf("run=%s edges: empty->ErrNoProviders, single->that provider, no panic", id)
}

// TestProperty_RouteMatchesOracle is a randomized property test: across many
// random tables (random costs/latencies, random duplicate IDs avoided), Route's
// winner must equal the independent oracle for BOTH objectives, and the winner
// must genuinely attain the global minimum objective value. This broadly guards
// against non-optimal selection on shapes not covered by the table cases.
func TestProperty_RouteMatchesOracle(t *testing.T) {
	id := runUUID(t)
	rng := rand.New(rand.NewSource(0xBADF00D))

	const trials = 2000
	for tr := 0; tr < trials; tr++ {
		n := 1 + rng.Intn(8)
		ps := make([]Provider, 0, n)
		seen := map[string]bool{}
		for len(ps) < n {
			pid := fmt.Sprintf("p%03d", rng.Intn(1000))
			if seen[pid] {
				continue
			}
			seen[pid] = true
			// Quantized values so exact ties occur frequently (small value set).
			ps = append(ps, Provider{
				ID:          pid,
				CostPerHour: float64(rng.Intn(5)),      // 0..4, many ties
				LatencyMs:   float64(rng.Intn(5)) * 10, // 0..40, many ties
			})
		}

		for _, wl := range []WorkloadType{Inference, Training} {
			want := oracleMin(wl, ps)
			got, err := Route(wl, ps)
			if err != nil {
				t.Fatalf("run=%s trial=%d wl=%s unexpected err %v table=%+v", id, tr, wl, err, ps)
			}
			if got.ID != want.ID {
				t.Fatalf("run=%s trial=%d wl=%s got %s want %s table=%+v", id, tr, wl, got.ID, want.ID, ps)
			}
			// Independent invariant: winner attains the global minimum objective.
			minVal := objectiveField(wl, ps[0])
			for _, p := range ps[1:] {
				if v := objectiveField(wl, p); v < minVal {
					minVal = v
				}
			}
			if objectiveField(wl, got) != minVal {
				t.Fatalf("run=%s trial=%d wl=%s winner %s does not attain min objective %.1f table=%+v",
					id, tr, wl, got.ID, minVal, ps)
			}
			// And it is the smallest ID among all minimum-objective providers.
			var minIDs []string
			for _, p := range ps {
				if objectiveField(wl, p) == minVal {
					minIDs = append(minIDs, p.ID)
				}
			}
			sort.Strings(minIDs)
			if got.ID != minIDs[0] {
				t.Fatalf("run=%s trial=%d wl=%s winner %s not smallest-ID among optima %v", id, tr, wl, got.ID, minIDs)
			}
		}
	}
	t.Logf("run=%s property: %d random tables x2 objectives all match oracle + invariants", id, trials)
}

// objectiveField mirrors the documented objective for test-side invariants.
func objectiveField(t WorkloadType, p Provider) float64 {
	if t == Inference {
		return p.LatencyMs
	}
	return p.CostPerHour
}

// idsOf renders provider IDs in slice order for failure diagnostics.
func idsOf(ps []Provider) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return fmt.Sprint(out)
}
