package carbonsched

import (
	"math"
	"sync"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// ADVERSARIAL SUITE — NaN carbon-intensity poison + tie-order + concurrency.
//
// Contract under attack (carbonsched.go lines 43-53, Place doc):
//   - Among latency-eligible regions, pick the one with the LOWEST CarbonIntensity.
//   - Ties broken deterministically by lexicographically smallest Name.
//   - Implies a TOTAL ORDER and INPUT-ORDER-INDEPENDENT selection.
//
// Place's comparator (lines 61-65):
//
//	if best == nil ||
//	    r.CarbonIntensity < best.CarbonIntensity ||
//	    (r.CarbonIntensity == best.CarbonIntensity && r.Name < best.Name) {
//	    best = r
//	}
//
// NaN poison: every comparison against NaN (`<`, `==`) is false. So a region
// whose CarbonIntensity is NaN:
//   - if it is the FIRST eligible region scanned, becomes `best` and is NEVER
//     displaced (cleanCI < NaN == false, cleanCI == NaN == false) → a NaN
//     ("unknown"/poison) region is selected as the "greenest";
//   - if it appears AFTER a real best, is never selected.
//
// ⇒ selection becomes INPUT-ORDER-DEPENDENT and NON-DETERMINISTIC, violating the
// documented total order + order-independence. A `< 0`-only sanity guard would
// likewise miss NaN. These reproducers FAIL on unfixed prod and assert SINK-SIDE
// (the actual chosen Region.Name / GramsCO2), not "no panic".
//
// NOT gated behind any env var (per coordinator protocol): the coordinator
// applies the minimal fix so they pass by default.
// ─────────────────────────────────────────────────────────────────────────────

// TestAdversarial_NaNPoisonsGreenestSelection_FirstPosition proves the SINK-SIDE
// poison: a NaN-intensity region placed FIRST is wrongly chosen as the greenest,
// beating a genuinely clean (50 gCO2) region that the contract says must win.
//
// Why it bites: the documented rule is "lowest CarbonIntensity". A NaN is not a
// real intensity; the only meaningful argmin here is "Clean" at 50 g. Prod's
// comparator latches the first-seen NaN and never lets Clean displace it, so the
// SELECTED region is "Poison" and the metered GramsCO2 is NaN — a corrupt sink.
func TestAdversarial_NaNPoisonsGreenestSelection_FirstPosition(t *testing.T) {
	const runUUID = "decade00-1480-4nan-9001-poisonfirst01"

	regions := []Region{
		{Name: "Poison", CarbonIntensity: math.NaN(), LatencyMS: 10}, // first-scanned, eligible
		{Name: "Clean", CarbonIntensity: 50, LatencyMS: 20},          // the true argmin
	}
	job := Job{ID: "j-nan", PowerWatts: 500, DurationHours: 4, MaxLatencyMS: 100}

	got, err := Place(job, regions)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}

	// SINK ASSERTION 1: the chosen region must be the real-valued minimum "Clean".
	if got.Region != "Clean" {
		t.Fatalf("[%s] NaN POISON: Place chose %q as greenest, but the only real "+
			"argmin is \"Clean\" (50 g). A NaN intensity must never out-rank a real "+
			"low-carbon region. chosen=%+v", runUUID, got.Region, got)
	}

	// SINK ASSERTION 2: emitted GramsCO2 must be the finite hand-calc value, not
	// NaN. KWh = 500/1000*4 = 2.0 ; Clean GramsCO2 = 2.0*50 = 100.
	if math.IsNaN(got.GramsCO2) {
		t.Fatalf("[%s] NaN POISON: GramsCO2 is NaN — a poison region's intensity "+
			"leaked into the metered emission sink: %+v", runUUID, got)
	}
	if !floatEq(got.KWh, 2.0) || !floatEq(got.GramsCO2, 100.0) {
		t.Fatalf("[%s] metering=(KWh %v, GramsCO2 %v), want (2.0, 100.0)",
			runUUID, got.KWh, got.GramsCO2)
	}
	t.Logf("[%s] proved NaN-first does not poison greenest selection: chose Clean@50g, GramsCO2=%vg",
		runUUID, got.GramsCO2)
}

// TestAdversarial_NaNSelectionOrderDependent is the TOTAL-ORDER teeth: the SAME
// region set yields a DIFFERENT chosen region depending solely on whether the
// NaN region is scanned first or last. The documented contract is order-
// independent, so both orderings MUST select the same real-valued argmin.
//
// Why it bites: on unfixed prod, [Poison(NaN), Clean] selects "Poison" (NaN
// latched first) while [Clean, Poison(NaN)] selects "Clean" (NaN can't displace
// a real best). Different winner from a mere permutation ⇒ total order broken.
func TestAdversarial_NaNSelectionOrderDependent(t *testing.T) {
	const runUUID = "decade00-1480-4nan-9002-orderdepend02"

	clean := Region{Name: "Clean", CarbonIntensity: 50, LatencyMS: 20}
	poison := Region{Name: "Poison", CarbonIntensity: math.NaN(), LatencyMS: 10}
	job := Job{ID: "j-nan-ord", PowerWatts: 1000, DurationHours: 1, MaxLatencyMS: 100}

	gotFirst, errF := Place(job, []Region{poison, clean}) // NaN scanned first
	gotLast, errL := Place(job, []Region{clean, poison})  // NaN scanned last
	if errF != nil || errL != nil {
		t.Fatalf("[%s] unexpected errors: first=%v last=%v", runUUID, errF, errL)
	}

	if gotFirst.Region != gotLast.Region {
		t.Fatalf("[%s] ORDER-DEPENDENT under NaN: [Poison,Clean]->%q but "+
			"[Clean,Poison]->%q. A permutation changed the winner; the documented "+
			"selection is input-order-independent.", runUUID, gotFirst.Region, gotLast.Region)
	}
	// And both must be the real argmin, "Clean".
	if gotFirst.Region != "Clean" {
		t.Fatalf("[%s] both orderings agreed on %q but the real argmin is \"Clean\"",
			runUUID, gotFirst.Region)
	}
	t.Logf("[%s] proved order-independent NaN handling: both orderings chose Clean", runUUID)
}

// TestAdversarial_NaNComparatorStrictWeakOrdering feeds NaN through the SAME
// comparator shape Place uses (the documented (CarbonIntensity, Name) order) and
// asserts the chosen winner is invariant across ALL permutations of a region set
// containing a NaN. A strict-weak-ordering violation (NaN makes `<` non-total)
// manifests as a permutation-dependent winner.
func TestAdversarial_NaNComparatorStrictWeakOrdering(t *testing.T) {
	const runUUID = "decade00-1480-4nan-9003-strictweak003"

	base := []Region{
		{Name: "Aaa", CarbonIntensity: 120, LatencyMS: 30},
		{Name: "Bbb", CarbonIntensity: math.NaN(), LatencyMS: 30},
		{Name: "Ccc", CarbonIntensity: 40, LatencyMS: 30}, // real argmin
	}
	job := Job{ID: "j-nan-swo", PowerWatts: 200, DurationHours: 2, MaxLatencyMS: 100}

	// All 6 permutations of 3 elements.
	perms := [][]int{
		{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0},
	}
	var winners = map[string]int{}
	for _, p := range perms {
		regs := []Region{base[p[0]], base[p[1]], base[p[2]]}
		got, err := Place(job, regs)
		if err != nil {
			t.Fatalf("[%s] perm %v: unexpected error %v", runUUID, p, err)
		}
		winners[got.Region]++
	}
	if len(winners) != 1 {
		t.Fatalf("[%s] STRICT-WEAK-ORDERING VIOLATION: NaN made the (CarbonIntensity,Name) "+
			"comparator non-total — winner varies across permutations: %v (want a single "+
			"stable winner \"Ccc\"@40g)", runUUID, winners)
	}
	if _, ok := winners["Ccc"]; !ok {
		t.Fatalf("[%s] stable winner is %v, want the real argmin \"Ccc\"@40g", runUUID, winners)
	}
	t.Logf("[%s] proved NaN-robust comparator: all 6 perms chose Ccc@40g", runUUID)
}

// TestAdversarial_PosInfNeverWinsGreenest pins that +Inf carbon (a maximally
// dirty grid) never out-ranks a real region. +Inf compares normally so a correct
// `<` already rejects it; this guards against any future NaN-fix that overcorrects
// and accidentally treats +Inf as "clean".
func TestAdversarial_PosInfNeverWinsGreenest(t *testing.T) {
	const runUUID = "decade00-1480-4inf-9004-posinflast04"

	job := Job{ID: "j-inf", PowerWatts: 500, DurationHours: 2, MaxLatencyMS: 100}
	for _, order := range [][]Region{
		{{Name: "Inf", CarbonIntensity: math.Inf(1), LatencyMS: 10}, {Name: "Real", CarbonIntensity: 300, LatencyMS: 20}},
		{{Name: "Real", CarbonIntensity: 300, LatencyMS: 20}, {Name: "Inf", CarbonIntensity: math.Inf(1), LatencyMS: 10}},
	} {
		got, err := Place(job, order)
		if err != nil {
			t.Fatalf("[%s] unexpected error: %v", runUUID, err)
		}
		if got.Region != "Real" {
			t.Fatalf("[%s] +Inf out-ranked a real region: chose %q want \"Real\"", runUUID, got.Region)
		}
		if math.IsInf(got.GramsCO2, 0) {
			t.Fatalf("[%s] +Inf intensity leaked into GramsCO2 sink: %+v", runUUID, got)
		}
	}
	t.Logf("[%s] proved +Inf never wins greenest, both orderings chose Real", runUUID)
}

// TestAdversarial_ConcurrentPlace_RaceFree runs Place concurrently over a shared,
// read-only region slice to surface any unguarded shared state under -race.
// Place is documented as a pure selection; concurrent reads must be race-free and
// every goroutine must agree on the deterministic winner.
func TestAdversarial_ConcurrentPlace_RaceFree(t *testing.T) {
	const runUUID = "decade00-1480-4race-905-concurrent05"

	regions := []Region{
		{Name: "Gamma", CarbonIntensity: 200, LatencyMS: 10},
		{Name: "Alpha", CarbonIntensity: 75, LatencyMS: 20}, // argmin
		{Name: "Beta", CarbonIntensity: 75, LatencyMS: 30},  // tie on carbon, larger name
		{Name: "Delta", CarbonIntensity: 500, LatencyMS: 40},
	}
	job := Job{ID: "j-race", PowerWatts: 800, DurationHours: 2, MaxLatencyMS: 100}

	const goroutines = 64
	var wg sync.WaitGroup
	results := make([]string, goroutines)
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(idx int) {
			defer wg.Done()
			got, err := Place(job, regions)
			if err != nil {
				results[idx] = "ERR:" + err.Error()
				return
			}
			results[idx] = got.Region
		}(g)
	}
	wg.Wait()

	for i, r := range results {
		if r != "Alpha" {
			t.Fatalf("[%s] goroutine %d got %q, want deterministic winner \"Alpha\" "+
				"(75g, name<\"Beta\")", runUUID, i, r)
		}
	}
	t.Logf("[%s] proved concurrent Place is race-free and deterministic: all %d agreed on Alpha",
		runUUID, goroutines)
}
