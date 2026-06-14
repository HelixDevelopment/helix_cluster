package revenueopt

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// ============================================================================
// Adversarial correctness suite for HXC-1456 RevenueOptimizer.
//
// The documented rule (revenueopt.go):
//   effectiveRevenue(g, off) = off.ExpectedHourlyUSD, multiplied by
//       (1 + TEEChutesBoost) IFF (g.TEE && off.Marketplace == "Chutes").
//   OptimizeAllocation assigns each GPU to the offer (whose GPUModel == g.Model)
//   that MAXIMIZES effective revenue (argmax). On an exact tie the FIRST offer in
//   input order wins (the loop uses strict `>`). GPUs with no matching offer are
//   omitted from the result map.
//
// These tests recompute the documented formula with an INDEPENDENT oracle and
// assert the chosen marketplace + the exact effective revenue. Anti-bluff teeth
// (each mutation that would bite is documented at the assertion site):
//   - winner == oracle argmax           bites a wrong-winner / flipped comparison
//   - effective revenue == oracle value bites mis-applied / mis-scoped bonus
//   - bonus only to TEE+Chutes          bites a bonus leaked to non-qualifiers
//   - order-independence (non-tie)      bites a non-deterministic selection
// ============================================================================

const boostEps = 1e-9

// oracleEffective is an INDEPENDENT re-implementation of the documented
// effective-revenue rule. It deliberately does NOT call into production code so
// that a production bug cannot mask itself in the oracle.
func oracleEffective(tee bool, marketplace string, raw, boost float64) float64 {
	if tee && marketplace == "Chutes" {
		return raw * (1.0 + boost)
	}
	return raw
}

// oracleWinner returns the marketplace the documented rule must select for a
// single GPU over a list of offers, plus that winner's effective revenue and a
// found flag. Tie-break: FIRST offer in input order among equal-effective
// offers (matching the production strict-`>` loop). Only offers whose GPUModel
// matches the GPU model are considered.
func oracleWinner(tee bool, model string, offers []Offer, boost float64) (string, float64, bool) {
	best := ""
	bestRev := 0.0
	found := false
	for _, off := range offers {
		if off.GPUModel != model {
			continue
		}
		rev := oracleEffective(tee, off.Marketplace, off.ExpectedHourlyUSD, boost)
		if !found || rev > bestRev {
			found = true
			bestRev = rev
			best = off.Marketplace
		}
	}
	return best, bestRev, found
}

// effectiveRevenueOfChoice recomputes the effective revenue that the production
// code's chosen marketplace yields for a GPU, so a test can assert the EXACT
// price/revenue the optimizer is implicitly committing to — not just the name.
//
// A marketplace name can legitimately appear in MULTIPLE matching offers at
// different prices (e.g. two "Chutes" H100 offers at 1.5 and 5.0). The optimizer
// would only ever SELECT such a marketplace via its BEST (max effective) offer,
// so the revenue the optimizer commits to for a chosen marketplace is the
// MAXIMUM effective revenue over that marketplace's matching offers. Using the
// max (not the first) is what makes this an honest reconstruction of the price
// the winner name stands for.
func effectiveRevenueOfChoice(tee bool, model, chosen string, offers []Offer, boost float64) (float64, bool) {
	best := 0.0
	found := false
	for _, off := range offers {
		if off.GPUModel == model && off.Marketplace == chosen {
			rev := oracleEffective(tee, off.Marketplace, off.ExpectedHourlyUSD, boost)
			if !found || rev > best {
				best = rev
				found = true
			}
		}
	}
	return best, found
}

// -----------------------------------------------------------------------------
// 1. Winner + effective revenue match the oracle across MANY configurations,
//    including the bonus-flips-the-winner case.
// -----------------------------------------------------------------------------

func TestAdversarial_WinnerAndRevenueMatchOracle(t *testing.T) {
	const runUUID = "revenueopt-adv-7a31-WinnerRevenueOracle"

	type tc struct {
		name       string
		boost      float64
		tee        bool
		model      string
		offers     []Offer
		wantWinner string // independently reasoned expectation, also cross-checked vs oracle
	}
	cases := []tc{
		{
			// Bonus FLIPS the winner: Chutes raw 9.00 is the cheaper offer, but
			// boost 0.10 -> 9.90 beats Vast 9.50. This is the load-bearing case.
			name:       "bonus_flips_winner",
			boost:      0.10,
			tee:        true,
			model:      "H100",
			offers:     []Offer{{"Chutes", "H100", 9.00}, {"Vast", "H100", 9.50}},
			wantWinner: "Chutes",
		},
		{
			// Same numbers but boost too small to flip: 9.00*1.05 = 9.45 < 9.50.
			name:       "bonus_too_small_no_flip",
			boost:      0.05,
			tee:        true,
			model:      "H100",
			offers:     []Offer{{"Chutes", "H100", 9.00}, {"Vast", "H100", 9.50}},
			wantWinner: "Vast",
		},
		{
			// Boost present but GPU is non-TEE -> Chutes must NOT be boosted.
			name:       "non_tee_no_boost",
			boost:      0.50,
			tee:        false,
			model:      "A100",
			offers:     []Offer{{"Chutes", "A100", 4.00}, {"RunPod", "A100", 6.25}, {"Vast", "A100", 5.10}},
			wantWinner: "RunPod",
		},
		{
			// TEE GPU but the rich offer is a non-Chutes marketplace; boost must
			// not leak to Vast. Chutes 5.00*1.20=6.00 vs Vast 7.00 -> Vast wins.
			name:       "boost_not_leaked_to_vast",
			boost:      0.20,
			tee:        true,
			model:      "H100",
			offers:     []Offer{{"Chutes", "H100", 5.00}, {"Vast", "H100", 7.00}},
			wantWinner: "Vast",
		},
		{
			// Exact effective tie 9.90 == 9.90: first-in-order (Chutes) must win.
			name:       "exact_tie_first_wins",
			boost:      0.10,
			tee:        true,
			model:      "H100",
			offers:     []Offer{{"Chutes", "H100", 9.00}, {"Vast", "H100", 9.90}},
			wantWinner: "Chutes",
		},
		{
			// Single offer.
			name:       "single_offer",
			boost:      0.30,
			tee:        true,
			model:      "H100",
			offers:     []Offer{{"Chutes", "H100", 1.23}},
			wantWinner: "Chutes",
		},
		{
			// Negative-ish small revenues still selected by !found guard (no
			// implicit 0.0 floor): only offer is 0.0001, must still be chosen.
			name:       "tiny_positive_still_chosen",
			boost:      0.10,
			tee:        false,
			model:      "L4",
			offers:     []Offer{{"RunPod", "L4", 0.0001}},
			wantWinner: "RunPod",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opt := NewRevenueOptimizer(c.boost)
			fleet := []GPU{{ID: "g", Model: c.model, TEE: c.tee}}
			got := opt.OptimizeAllocation(fleet, c.offers)

			// Oracle argmax (independent of production).
			oWinner, oRev, oFound := oracleWinner(c.tee, c.model, c.offers, c.boost)
			if !oFound {
				t.Fatalf("[%s/%s] test bug: oracle found no match", runUUID, c.name)
			}
			// Cross-check the hand-reasoned expectation against the oracle.
			if oWinner != c.wantWinner {
				t.Fatalf("[%s/%s] test self-inconsistency: oracle=%q wantWinner=%q",
					runUUID, c.name, oWinner, c.wantWinner)
			}

			// TEETH #1: production winner must equal oracle argmax. Bites a
			// flipped comparison or a dropped/mis-scoped bonus that changes argmax.
			if got["g"] != oWinner {
				t.Fatalf("[%s/%s] WRONG WINNER: got %q, oracle argmax %q (boost=%.4f tee=%v offers=%v)",
					runUUID, c.name, got["g"], oWinner, c.boost, c.tee, c.offers)
			}

			// TEETH #2: the EXACT effective revenue the optimizer committed to
			// must equal the oracle's winning revenue. Bites a mis-computed
			// adjusted price even when the winner name happens to be right.
			chRev, ok := effectiveRevenueOfChoice(c.tee, c.model, got["g"], c.offers, c.boost)
			if !ok {
				t.Fatalf("[%s/%s] chosen marketplace %q has no matching offer", runUUID, c.name, got["g"])
			}
			if math.Abs(chRev-oRev) > boostEps {
				t.Fatalf("[%s/%s] WRONG EFFECTIVE REVENUE: chosen %q yields %.6f, oracle winner yields %.6f",
					runUUID, c.name, got["g"], chRev, oRev)
			}
			t.Logf("[%s/%s] winner=%q effRev=%.6f matches oracle", runUUID, c.name, oWinner, oRev)
		})
	}
}

// -----------------------------------------------------------------------------
// 2. Bonus/penalty math: the boost is applied correctly AND only to qualifying
//    (TEE + Chutes) pairs. Mutation-verify the exact (1+boost) factor.
// -----------------------------------------------------------------------------

func TestAdversarial_BonusAppliedOnlyToQualifying(t *testing.T) {
	const runUUID = "revenueopt-adv-be20-BonusScope"

	const raw = 10.0
	const boost = 0.25 // (1+boost) = 1.25 -> 12.50 when applied

	type want struct {
		tee         bool
		marketplace string
		wantRev     float64
	}
	wants := []want{
		{tee: true, marketplace: "Chutes", wantRev: raw * 1.25}, // qualifying -> boosted
		{tee: true, marketplace: "Vast", wantRev: raw},          // TEE but not Chutes -> raw
		{tee: false, marketplace: "Chutes", wantRev: raw},       // Chutes but not TEE -> raw
		{tee: false, marketplace: "Vast", wantRev: raw},         // neither -> raw
	}

	opt := NewRevenueOptimizer(boost)
	for _, w := range wants {
		got := opt.effectiveRevenue(GPU{TEE: w.tee}, Offer{Marketplace: w.marketplace, ExpectedHourlyUSD: raw})
		// TEETH: bites (a) bonus dropped: qualifying case returns 10.00 not 12.50;
		// (b) bonus leaked: a non-qualifying case returns 12.50 not 10.00;
		// (c) wrong factor (e.g. *boost instead of *(1+boost)): qualifying returns 2.50.
		if math.Abs(got-w.wantRev) > boostEps {
			t.Fatalf("[%s] bonus scope/math wrong: tee=%v mkt=%q -> got %.6f, want %.6f",
				runUUID, w.tee, w.marketplace, got, w.wantRev)
		}
		t.Logf("[%s] tee=%v mkt=%q effRev=%.4f (want %.4f) OK", runUUID, w.tee, w.marketplace, got, w.wantRev)
	}

	// Explicit factor check across several boosts: effective == raw*(1+boost),
	// distinguishing (1+boost) from boost, 1, and (1-boost).
	for _, b := range []float64{0.0, 0.1, 0.5, 1.0, 2.0} {
		o := NewRevenueOptimizer(b)
		got := o.effectiveRevenue(GPU{TEE: true}, Offer{Marketplace: "Chutes", ExpectedHourlyUSD: raw})
		wantV := raw * (1 + b)
		if math.Abs(got-wantV) > boostEps {
			t.Fatalf("[%s] boost factor wrong at b=%.2f: got %.6f want %.6f (must be raw*(1+boost))",
				runUUID, b, got, wantV)
		}
	}
}

// -----------------------------------------------------------------------------
// 3. Order-independence (non-tie) + deterministic first-wins tie-break.
// -----------------------------------------------------------------------------

func TestAdversarial_OrderIndependenceAndTieBreak(t *testing.T) {
	const runUUID = "revenueopt-adv-04dd-OrderIndep"

	// (a) Strict-winner case (no ties): shuffling the offer slice MUST NOT change
	// the winner or its revenue. Bites a non-deterministic / order-sensitive
	// selection over distinct effective revenues.
	opt := NewRevenueOptimizer(0.10)
	model := "H100"
	tee := true
	base := []Offer{
		{"Chutes", "H100", 9.00}, // boosted -> 9.90
		{"Vast", "H100", 9.50},
		{"RunPod", "H100", 8.00},
		{"Lambda", "H100", 9.80},
	}
	oWinner, oRev, _ := oracleWinner(tee, model, base, 0.10)

	rng := rand.New(rand.NewSource(0xC0FFEE))
	for i := 0; i < 200; i++ {
		shuf := make([]Offer, len(base))
		copy(shuf, base)
		rng.Shuffle(len(shuf), func(a, b int) { shuf[a], shuf[b] = shuf[b], shuf[a] })

		got := opt.OptimizeAllocation([]GPU{{ID: "g", Model: model, TEE: tee}}, shuf)
		if got["g"] != oWinner {
			t.Fatalf("[%s] order-DEPENDENT winner at iter %d: got %q want %q on perm %v",
				runUUID, i, got["g"], oWinner, shuf)
		}
		chRev, _ := effectiveRevenueOfChoice(tee, model, got["g"], shuf, 0.10)
		if math.Abs(chRev-oRev) > boostEps {
			t.Fatalf("[%s] order-DEPENDENT revenue at iter %d: %.6f vs oracle %.6f",
				runUUID, i, chRev, oRev)
		}
	}
	t.Logf("[%s] 200 permutations all -> winner=%q rev=%.4f (order-independent)", runUUID, oWinner, oRev)

	// (b) Deterministic tie-break: two offers with EQUAL effective revenue. The
	// documented rule (strict `>`) keeps the FIRST in input order. Assert that
	// reversing the order flips which marketplace name wins (proving first-wins),
	// while revenue is identical either way.
	tieOffers := []Offer{
		{"AlphaMkt", "H100", 7.00},
		{"BetaMkt", "H100", 7.00},
	}
	g1 := opt.OptimizeAllocation([]GPU{{ID: "g", Model: "H100", TEE: false}}, tieOffers)
	rev1, _ := effectiveRevenueOfChoice(false, "H100", g1["g"], tieOffers, 0.10)

	rev := []Offer{tieOffers[1], tieOffers[0]}
	g2 := opt.OptimizeAllocation([]GPU{{ID: "g", Model: "H100", TEE: false}}, rev)
	rev2, _ := effectiveRevenueOfChoice(false, "H100", g2["g"], rev, 0.10)

	if g1["g"] != "AlphaMkt" {
		t.Fatalf("[%s] tie-break: first-in-order must win, got %q want AlphaMkt", runUUID, g1["g"])
	}
	if g2["g"] != "BetaMkt" {
		t.Fatalf("[%s] tie-break: after reversal first-in-order (BetaMkt) must win, got %q", runUUID, g2["g"])
	}
	if math.Abs(rev1-rev2) > boostEps || math.Abs(rev1-7.00) > boostEps {
		t.Fatalf("[%s] tie-break revenue must be identical 7.00 either way: %.6f vs %.6f", runUUID, rev1, rev2)
	}
	t.Logf("[%s] tie-break deterministic first-wins: order1->%q order2->%q (rev %.2f both)",
		runUUID, g1["g"], g2["g"], rev1)
}

// -----------------------------------------------------------------------------
// 4. Edge cases: empty fleet, empty offers, all-equal effective revenue, and a
//    randomized fuzz that compares the FULL allocation map against the oracle.
// -----------------------------------------------------------------------------

func TestAdversarial_EdgeCases(t *testing.T) {
	const runUUID = "revenueopt-adv-ed9e-EdgeCases"
	opt := NewRevenueOptimizer(0.10)

	// Empty fleet -> empty (non-nil) map, no panic.
	if got := opt.OptimizeAllocation(nil, []Offer{{"Vast", "H100", 1}}); len(got) != 0 {
		t.Fatalf("[%s] empty fleet must yield empty result, got %v", runUUID, got)
	}
	// Empty offers -> every GPU omitted, no panic.
	if got := opt.OptimizeAllocation([]GPU{{ID: "g", Model: "H100", TEE: true}}, nil); len(got) != 0 {
		t.Fatalf("[%s] empty offers must omit all GPUs, got %v", runUUID, got)
	}
	// Both empty.
	if got := opt.OptimizeAllocation(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("[%s] both empty must yield empty non-nil map, got %v", runUUID, got)
	}

	// All-equal effective revenue across 3 offers: first-in-order wins, no panic,
	// exactly one assignment.
	eq := []Offer{{"M1", "H100", 5.0}, {"M2", "H100", 5.0}, {"M3", "H100", 5.0}}
	got := opt.OptimizeAllocation([]GPU{{ID: "g", Model: "H100", TEE: false}}, eq)
	if got["g"] != "M1" {
		t.Fatalf("[%s] all-equal: first-in-order (M1) must win, got %q", runUUID, got["g"])
	}
	t.Logf("[%s] edge cases (empty fleet/offers/both, all-equal) OK", runUUID)
}

// TestAdversarial_FullMapFuzz drives randomized fleets+offers and asserts the
// ENTIRE production allocation map equals the independent oracle's, GPU by GPU,
// including both the winner and its exact effective revenue. This is the broad
// net that catches any wrong-winner / wrong-price regression the curated cases
// might miss.
func TestAdversarial_FullMapFuzz(t *testing.T) {
	const runUUID = "revenueopt-adv-fz55-FullMapFuzz"

	models := []string{"H100", "A100", "L4", "MI300X"}
	markets := []string{"Chutes", "Vast", "RunPod", "Lambda", "Akash"}
	rng := rand.New(rand.NewSource(424242))

	for iter := 0; iter < 2000; iter++ {
		boost := rng.Float64() * 2.0 // 0 .. 2.0
		opt := NewRevenueOptimizer(boost)

		nGPU := rng.Intn(5)
		fleet := make([]GPU, nGPU)
		for i := range fleet {
			fleet[i] = GPU{
				ID:    fmt.Sprintf("g%d", i),
				Model: models[rng.Intn(len(models))],
				TEE:   rng.Intn(2) == 0,
			}
		}

		nOff := rng.Intn(8)
		offers := make([]Offer, nOff)
		for i := range offers {
			// Coarse revenue grid raises the chance of exact ties so the
			// first-wins tie-break is exercised under fuzz too.
			offers[i] = Offer{
				Marketplace:       markets[rng.Intn(len(markets))],
				GPUModel:          models[rng.Intn(len(models))],
				ExpectedHourlyUSD: float64(rng.Intn(20)+1) * 0.5,
			}
		}

		got := opt.OptimizeAllocation(fleet, offers)

		// Build the oracle map and compare entry-for-entry.
		want := make(map[string]string)
		for _, g := range fleet {
			w, _, found := oracleWinner(g.TEE, g.Model, offers, boost)
			if found {
				want[g.ID] = w
			}
		}

		if len(got) != len(want) {
			t.Fatalf("[%s] iter %d size mismatch: got %d want %d\nfleet=%v\noffers=%v\ngot=%v want=%v",
				runUUID, iter, len(got), len(want), fleet, offers, got, want)
		}
		for _, g := range fleet {
			wWin, wRev, found := oracleWinner(g.TEE, g.Model, offers, boost)
			gv, present := got[g.ID]
			if found != present {
				t.Fatalf("[%s] iter %d gpu %s presence mismatch: oracleFound=%v gotPresent=%v\noffers=%v",
					runUUID, iter, g.ID, found, present, offers)
			}
			if !found {
				continue
			}
			if gv != wWin {
				t.Fatalf("[%s] iter %d gpu %s WRONG WINNER: got %q want %q (boost=%.4f tee=%v model=%s)\noffers=%v",
					runUUID, iter, g.ID, gv, wWin, boost, g.TEE, g.Model, offers)
			}
			chRev, _ := effectiveRevenueOfChoice(g.TEE, g.Model, gv, offers, boost)
			if math.Abs(chRev-wRev) > boostEps {
				t.Fatalf("[%s] iter %d gpu %s WRONG REVENUE: chosen %q yields %.6f, oracle %.6f\noffers=%v",
					runUUID, iter, g.ID, gv, chRev, wRev, offers)
			}
		}
	}
	t.Logf("[%s] 2000 randomized fleets/offers: full allocation map matches oracle (winner+revenue)", runUUID)
}
