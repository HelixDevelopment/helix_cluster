package carbonsched

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// oracleSelect is an INDEPENDENT reimplementation of the documented selection
// rule, written from the prose in Place's doc comment (lines 43-48 of
// carbonsched.go) rather than by reusing Place's loop:
//
//  1. eligible := { r : r.LatencyMS <= job.MaxLatencyMS }
//  2. if eligible is empty -> no choice (ErrNoEligibleRegion)
//  3. else pick argmin over eligible of (CarbonIntensity, Name) lexicographically
//     i.e. lowest CarbonIntensity; ties broken by lexicographically smallest Name.
//
// It deliberately uses a sort over a filtered copy (a structurally different
// algorithm from Place's single-pass scan) so that a bug in Place's scan cannot
// hide behind an identically-buggy oracle. Returns the chosen Name and whether
// any region was eligible.
func oracleSelect(job Job, regions []Region) (name string, ok bool) {
	eligible := make([]Region, 0, len(regions))
	for _, r := range regions {
		if r.LatencyMS <= job.MaxLatencyMS {
			eligible = append(eligible, r)
		}
	}
	if len(eligible) == 0 {
		return "", false
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].CarbonIntensity != eligible[j].CarbonIntensity {
			return eligible[i].CarbonIntensity < eligible[j].CarbonIntensity
		}
		return eligible[i].Name < eligible[j].Name
	})
	return eligible[0].Name, true
}

// expectedMetering recomputes the documented energy/emission for the WINNER the
// oracle chose, independently of Place, so the test asserts the full Placement
// (region + KWh + GramsCO2), not just the region name. This bites a mutation
// that picks the right region but meters against the wrong region's intensity.
func expectedMetering(job Job, regions []Region, winner string) (kWh, grams float64) {
	kWh = job.PowerWatts / 1000 * job.DurationHours
	for _, r := range regions {
		if r.Name == winner {
			return kWh, kWh * r.CarbonIntensity
		}
	}
	return kWh, 0 // unreachable for a valid winner
}

// TestPlace_OracleArgmin_Exhaustive is the GAP-FILLING adversarial guard. Across
// MANY randomized region configurations (clear winners, near-ties, exact ties,
// many nodes, varied eligibility), it asserts Place's chosen region is EXACTLY
// the independent oracle's argmin, and that the full metered Placement matches a
// hand-recomputed expectation.
//
// Anti-bluff reasoning — why each assertion BITES:
//   - "got.Region == oracleName": this is argmin agreement against a structurally
//     different (sort-based) oracle. If Place's comparison is flipped to prefer
//     HIGHER carbon (the "pick a dirtier node" bug the task warns about), the
//     dirtiest eligible node is chosen while the oracle still returns the cleanest
//     -> mismatch -> FAIL on the very first config with a clear winner.
//   - Distinct carbon values per config (deduped below) make the argmin UNIQUE,
//     so a correct selector has exactly one acceptable answer — there is no
//     "either is fine" slack for a wrong pick to slip through.
//   - Full-Placement check (KWh + GramsCO2 against expectedMetering): bites a
//     mutation that returns the right Name but meters the wrong intensity.
func TestPlace_OracleArgmin_Exhaustive(t *testing.T) {
	const runUUID = "f0e1d2c3-b4a5-4968-8c7b-1480000aRGMN"

	rng := rand.New(rand.NewSource(0x6361726201)) // deterministic: "carb" + 01

	const configs = 400
	for c := 0; c < configs; c++ {
		nRegions := 1 + rng.Intn(8) // 1..8 candidate regions
		regions := makeDistinctCarbonRegions(rng, nRegions)

		// Pick a MaxLatency that sometimes excludes some/all regions, so the
		// argmin is taken over a varying eligible subset.
		maxLat := float64(rng.Intn(260)) // 0..259
		job := Job{
			ID:            fmt.Sprintf("j-%d", c),
			PowerWatts:    float64(100 + rng.Intn(900)),
			DurationHours: float64(1 + rng.Intn(10)),
			MaxLatencyMS:  maxLat,
		}

		wantName, wantOK := oracleSelect(job, regions)

		got, err := Place(job, regions)

		if !wantOK {
			if !errors.Is(err, ErrNoEligibleRegion) {
				t.Fatalf("[%s] cfg#%d: oracle says no eligible region (maxLat=%v) but Place err=%v, placement=%+v\nregions=%+v",
					runUUID, c, maxLat, err, got, regions)
			}
			if got != (Placement{}) {
				t.Fatalf("[%s] cfg#%d: expected zero Placement on no-eligible, got %+v", runUUID, c, got)
			}
			continue
		}

		if err != nil {
			t.Fatalf("[%s] cfg#%d: oracle picked %q but Place errored: %v\nregions=%+v",
				runUUID, c, wantName, err, regions)
		}
		if got.Region != wantName {
			t.Fatalf("[%s] cfg#%d: ARGMIN MISMATCH — Place chose %q, oracle argmin=%q (maxLat=%v)\nregions=%+v",
				runUUID, c, got.Region, wantName, maxLat, regions)
		}

		wantKWh, wantGrams := expectedMetering(job, regions, wantName)
		if !floatEq(got.KWh, wantKWh) {
			t.Fatalf("[%s] cfg#%d: KWh=%v want %v (region %q)", runUUID, c, got.KWh, wantKWh, wantName)
		}
		if !floatEq(got.GramsCO2, wantGrams) {
			t.Fatalf("[%s] cfg#%d: GramsCO2=%v want %v (region %q)", runUUID, c, got.GramsCO2, wantGrams, wantName)
		}
		if got.JobID != job.ID {
			t.Fatalf("[%s] cfg#%d: JobID=%q want %q", runUUID, c, got.JobID, job.ID)
		}
	}
	t.Logf("[%s] proved Place == independent sort-based oracle argmin over %d randomized configs "+
		"(1..8 regions, distinct carbon, varying eligibility), full Placement verified", runUUID, configs)
}

// makeDistinctCarbonRegions builds n regions with pairwise-DISTINCT carbon
// intensities, guaranteeing the carbon argmin is unique (no tie slack). Names
// and latencies are randomized. Used by the exhaustive argmin test.
func makeDistinctCarbonRegions(rng *rand.Rand, n int) []Region {
	used := make(map[float64]bool, n)
	regions := make([]Region, 0, n)
	for i := 0; i < n; i++ {
		var ci float64
		for {
			ci = float64(rng.Intn(1000)) // 0..999 gCO2/kWh
			if !used[ci] {
				used[ci] = true
				break
			}
		}
		regions = append(regions, Region{
			Name:            fmt.Sprintf("r%02d-%c", i, rune('A'+rng.Intn(26))),
			CarbonIntensity: ci,
			LatencyMS:       float64(rng.Intn(260)),
		})
	}
	return regions
}

// TestPlace_OrderIndependent_Shuffle proves selection is INPUT-ORDER-INDEPENDENT,
// including the documented Name tiebreak under exact carbon ties. For each of
// many configs it computes Place's choice on a canonical ordering, then shuffles
// the input many times and asserts the SAME region is chosen every time.
//
// Anti-bluff reasoning — why this BITES:
//   - A non-deterministic or order-dependent selector (e.g. "keep the first
//     minimum seen" WITHOUT the `r.Name < best.Name` tiebreak, or a `<=` that
//     lets the LAST equal-carbon region win) would, under a permutation that
//     moves a tied region, return a DIFFERENT Name -> the cross-permutation
//     equality assertion FAILS.
//   - Configs here intentionally include exact carbon ties (duplicated carbon
//     values), the only place where tiebreak order-sensitivity can manifest.
func TestPlace_OrderIndependent_Shuffle(t *testing.T) {
	const runUUID = "ab12cd34-ef56-4780-9a1b-1480SHUFFLE0"

	rng := rand.New(rand.NewSource(0x73687566)) // "shuf"

	const configs = 120
	const perms = 24

	for c := 0; c < configs; c++ {
		n := 2 + rng.Intn(7) // 2..8 regions
		regions := makeTieProneRegions(rng, n)
		job := Job{
			ID:            fmt.Sprintf("shuf-%d", c),
			PowerWatts:    500,
			DurationHours: 3,
			MaxLatencyMS:  float64(rng.Intn(260)),
		}

		// Reference choice from the canonical (unshuffled) input.
		refPlacement, refErr := Place(job, append([]Region(nil), regions...))
		// Cross-check the reference against the oracle too.
		oracleName, oracleOK := oracleSelect(job, regions)
		if oracleOK {
			if refErr != nil {
				t.Fatalf("[%s] cfg#%d: oracle eligible (%q) but Place errored: %v", runUUID, c, oracleName, refErr)
			}
			if refPlacement.Region != oracleName {
				t.Fatalf("[%s] cfg#%d: canonical Place=%q != oracle=%q\nregions=%+v",
					runUUID, c, refPlacement.Region, oracleName, regions)
			}
		} else if !errors.Is(refErr, ErrNoEligibleRegion) {
			t.Fatalf("[%s] cfg#%d: oracle no-eligible but Place err=%v", runUUID, c, refErr)
		}

		for p := 0; p < perms; p++ {
			shuffled := append([]Region(nil), regions...)
			rng.Shuffle(len(shuffled), func(i, j int) {
				shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
			})
			got, err := Place(job, shuffled)
			if !oracleOK {
				if !errors.Is(err, ErrNoEligibleRegion) {
					t.Fatalf("[%s] cfg#%d perm#%d: want ErrNoEligibleRegion, got err=%v placement=%+v",
						runUUID, c, p, err, got)
				}
				continue
			}
			if err != nil {
				t.Fatalf("[%s] cfg#%d perm#%d: unexpected error %v", runUUID, c, p, err)
			}
			if got.Region != refPlacement.Region {
				t.Fatalf("[%s] cfg#%d perm#%d: ORDER-DEPENDENT — permuted choice %q != canonical %q\n"+
					"canonical regions=%+v\nshuffled=%+v",
					runUUID, c, p, got.Region, refPlacement.Region, regions, shuffled)
			}
			if !floatEq(got.GramsCO2, refPlacement.GramsCO2) {
				t.Fatalf("[%s] cfg#%d perm#%d: GramsCO2 changed under permutation: %v vs %v",
					runUUID, c, p, got.GramsCO2, refPlacement.GramsCO2)
			}
		}
	}
	t.Logf("[%s] proved order-independence: %d configs x %d permutations each, "+
		"choice stable under shuffle incl. exact-carbon Name tiebreak", runUUID, configs, perms)
}

// makeTieProneRegions builds regions where carbon intensities are drawn from a
// SMALL pool (0..3 distinct values), so exact ties are common — exercising the
// Name tiebreak's order-independence. All Names are unique.
func makeTieProneRegions(rng *rand.Rand, n int) []Region {
	carbonPool := []float64{50, 50, 100, 100, 200}
	regions := make([]Region, 0, n)
	for i := 0; i < n; i++ {
		regions = append(regions, Region{
			Name:            fmt.Sprintf("node-%02d", rng.Intn(1000)+i*1000), // unique via +i*1000
			CarbonIntensity: carbonPool[rng.Intn(len(carbonPool))],
			LatencyMS:       float64(rng.Intn(260)),
		})
	}
	return regions
}

// TestPlace_AllEqualCarbon_NameWins proves that when EVERY eligible region shares
// the same carbon intensity, the lexicographically smallest Name is chosen, and
// that this holds regardless of input order. Direct, non-random teeth for the
// documented tiebreak across more than two equal nodes.
func TestPlace_AllEqualCarbon_NameWins(t *testing.T) {
	const runUUID = "cc00dd11-2233-4455-a677-1480ALLEQUAL"

	// Same carbon (250), all eligible (latency <= 100). Smallest name "Aabb".
	base := []Region{
		{Name: "Mmm", CarbonIntensity: 250, LatencyMS: 10},
		{Name: "Zzz", CarbonIntensity: 250, LatencyMS: 20},
		{Name: "Aabb", CarbonIntensity: 250, LatencyMS: 90}, // wins on name, NOT on latency
		{Name: "Aac", CarbonIntensity: 250, LatencyMS: 5},
	}
	job := Job{ID: "j-alleq", PowerWatts: 400, DurationHours: 5, MaxLatencyMS: 100}

	orders := [][]int{
		{0, 1, 2, 3},
		{3, 2, 1, 0},
		{2, 0, 3, 1},
		{1, 3, 0, 2},
	}
	for oi, order := range orders {
		regions := make([]Region, len(order))
		for k, idx := range order {
			regions[k] = base[idx]
		}
		got, err := Place(job, regions)
		if err != nil {
			t.Fatalf("[%s] order#%d: unexpected error %v", runUUID, oi, err)
		}
		if got.Region != "Aabb" {
			t.Fatalf("[%s] order#%d: all-equal carbon should pick smallest name %q, chose %q (order=%v)",
				runUUID, oi, "Aabb", got.Region, order)
		}
		// KWh = 400/1000 * 5 = 2.0 ; GramsCO2 = 2.0 * 250 = 500
		if !floatEq(got.KWh, 2.0) || !floatEq(got.GramsCO2, 500.0) {
			t.Fatalf("[%s] order#%d: metering=(%v,%v) want (2.0,500.0)", runUUID, oi, got.KWh, got.GramsCO2)
		}
	}
	t.Logf("[%s] proved all-equal-carbon tiebreak: 4 regions @250g, picked smallest name "+
		"Aabb across 4 input orderings (not lowest-latency Aac@5ms)", runUUID)
}

// TestPlace_SingleNode proves the degenerate single-region cases: one eligible
// region is chosen and metered; one ineligible region yields ErrNoEligibleRegion.
func TestPlace_SingleNode(t *testing.T) {
	const runUUID = "11aa22bb-33cc-4dd5-9e6f-1480SINGLE00"

	job := Job{ID: "j-1", PowerWatts: 600, DurationHours: 2, MaxLatencyMS: 100}

	// Eligible single node.
	got, err := Place(job, []Region{{Name: "Solo", CarbonIntensity: 123, LatencyMS: 99}})
	if err != nil {
		t.Fatalf("[%s] single eligible: unexpected error %v", runUUID, err)
	}
	if got.Region != "Solo" {
		t.Fatalf("[%s] single eligible: chose %q want Solo", runUUID, got.Region)
	}
	// KWh = 600/1000 * 2 = 1.2 ; GramsCO2 = 1.2 * 123 = 147.6
	if !floatEq(got.KWh, 1.2) || !floatEq(got.GramsCO2, 147.6) {
		t.Fatalf("[%s] single eligible: metering=(%v,%v) want (1.2,147.6)", runUUID, got.KWh, got.GramsCO2)
	}

	// Ineligible single node -> error.
	got2, err2 := Place(job, []Region{{Name: "SoloFar", CarbonIntensity: 0, LatencyMS: 101}})
	if !errors.Is(err2, ErrNoEligibleRegion) {
		t.Fatalf("[%s] single ineligible: err=%v want ErrNoEligibleRegion", runUUID, err2)
	}
	if got2 != (Placement{}) {
		t.Fatalf("[%s] single ineligible: expected zero Placement, got %+v", runUUID, got2)
	}
	t.Logf("[%s] proved single-node: eligible Solo chosen+metered; ineligible SoloFar -> ErrNoEligibleRegion", runUUID)
}

// TestPlace_EmptyAndNil proves the empty / nil region slice yields
// ErrNoEligibleRegion with a zero Placement and NO panic (documented behavior:
// "If no region is eligible, ErrNoEligibleRegion is returned").
func TestPlace_EmptyAndNil(t *testing.T) {
	const runUUID = "ee99ff88-7766-4554-8332-1480EMPTYNIL"

	job := Job{ID: "j-empty", PowerWatts: 100, DurationHours: 1, MaxLatencyMS: 100}

	for _, tc := range []struct {
		label   string
		regions []Region
	}{
		{"nil", nil},
		{"empty", []Region{}},
	} {
		got, err := Place(job, tc.regions)
		if !errors.Is(err, ErrNoEligibleRegion) {
			t.Fatalf("[%s] %s: err=%v want ErrNoEligibleRegion", runUUID, tc.label, err)
		}
		if got != (Placement{}) {
			t.Fatalf("[%s] %s: expected zero Placement, got %+v", runUUID, tc.label, got)
		}
	}
	t.Logf("[%s] proved empty/nil input -> ErrNoEligibleRegion, zero Placement, no panic", runUUID)
}
