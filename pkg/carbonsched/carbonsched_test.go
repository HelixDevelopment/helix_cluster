package carbonsched

import (
	"errors"
	"math"
	"testing"
)

const floatTol = 1e-9

func floatEq(a, b float64) bool { return math.Abs(a-b) <= floatTol }

// TestPlace_PrefersLowCarbon is the LOAD-BEARING GUARD for the carbon-intensity
// preference. Two regions are both latency-eligible. The lower-latency region
// ("Fast") has HIGH carbon; the higher-latency-but-still-eligible region
// ("Clean") has much LOWER carbon. The scheduler MUST pick "Clean".
//
// Mutation falsifiability:
//   - "pick first region"  -> would pick "Fast" (listed first) -> region flips, FAIL.
//   - "pick lowest latency" -> would pick "Fast" (10 < 80 ms)  -> region flips, FAIL.
//   - ignore carbon entirely -> chosen region + GramsCO2 diverge from hand-calc, FAIL.
func TestPlace_PrefersLowCarbon(t *testing.T) {
	const runUUID = "b1f0c4d2-7a3e-4e91-9c2a-0480c0de1480"

	regions := []Region{
		{Name: "Fast", CarbonIntensity: 800, LatencyMS: 10}, // first + lowest latency, dirty grid
		{Name: "Clean", CarbonIntensity: 50, LatencyMS: 80}, // slower but still eligible, clean grid
	}
	job := Job{ID: "j-carbon", PowerWatts: 500, DurationHours: 4, MaxLatencyMS: 100}

	// Hand calc: KWh = 500/1000 * 4 = 2.0
	//            Clean GramsCO2 = 2.0 * 50  = 100
	//            (Fast  GramsCO2 = 2.0 * 800 = 1600 -- what a broken mutation would emit)
	const wantKWh = 2.0
	const wantGrams = 100.0
	const mutantGrams = 1600.0

	got, err := Place(job, regions)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	if got.Region != "Clean" {
		t.Fatalf("[%s] carbon signal ignored: chose %q, want %q", runUUID, got.Region, "Clean")
	}
	if !floatEq(got.KWh, wantKWh) {
		t.Fatalf("[%s] KWh = %v, want %v", runUUID, got.KWh, wantKWh)
	}
	if !floatEq(got.GramsCO2, wantGrams) {
		t.Fatalf("[%s] GramsCO2 = %v, want %v", runUUID, got.GramsCO2, wantGrams)
	}
	if floatEq(got.GramsCO2, mutantGrams) {
		t.Fatalf("[%s] GramsCO2 matches dirty-grid mutant value %v", runUUID, mutantGrams)
	}
	t.Logf("[%s] proved low-carbon preference: candidates Fast(800g,10ms)+Clean(50g,80ms) "+
		"-> chose Clean; before(naive Fast)=%vg CO2 -> after(carbon-aware Clean)=%vg CO2 (delta -%vg)",
		runUUID, mutantGrams, got.GramsCO2, mutantGrams-got.GramsCO2)
}

// TestPlace_LatencyFiltersOut proves a zero-carbon region exceeding MaxLatencyMS
// is NOT chosen, even though its carbon is the lowest possible.
func TestPlace_LatencyFiltersOut(t *testing.T) {
	const runUUID = "5e2a9b71-0c44-4d8f-a6b3-1480deadbeef"

	regions := []Region{
		{Name: "ZeroCarbonFar", CarbonIntensity: 0, LatencyMS: 250}, // tempting but too far
		{Name: "EligibleNear", CarbonIntensity: 300, LatencyMS: 40}, // only eligible option
	}
	job := Job{ID: "j-latency", PowerWatts: 1000, DurationHours: 1, MaxLatencyMS: 100}

	// Hand calc: KWh = 1000/1000 * 1 = 1.0; GramsCO2 = 1.0 * 300 = 300
	const wantKWh = 1.0
	const wantGrams = 300.0

	got, err := Place(job, regions)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	if got.Region != "EligibleNear" {
		t.Fatalf("[%s] latency filter failed: chose %q, want %q", runUUID, got.Region, "EligibleNear")
	}
	if got.Region == "ZeroCarbonFar" {
		t.Fatalf("[%s] picked latency-ineligible zero-carbon region", runUUID)
	}
	if !floatEq(got.KWh, wantKWh) || !floatEq(got.GramsCO2, wantGrams) {
		t.Fatalf("[%s] metering = (KWh %v, GramsCO2 %v), want (%v, %v)",
			runUUID, got.KWh, got.GramsCO2, wantKWh, wantGrams)
	}
	t.Logf("[%s] proved latency filtering: before(would pick 0g/250ms ZeroCarbonFar) "+
		"-> after(filtered out, picked EligibleNear 300g/40ms) GramsCO2=%vg", runUUID, got.GramsCO2)
}

// TestPlace_NoEligible proves ErrNoEligibleRegion when every region exceeds the
// job's latency budget.
func TestPlace_NoEligible(t *testing.T) {
	const runUUID = "9c7d3f10-aa21-4b55-8e0c-148000000000"

	regions := []Region{
		{Name: "Far1", CarbonIntensity: 10, LatencyMS: 200},
		{Name: "Far2", CarbonIntensity: 20, LatencyMS: 500},
	}
	job := Job{ID: "j-none", PowerWatts: 100, DurationHours: 2, MaxLatencyMS: 50}

	got, err := Place(job, regions)
	if !errors.Is(err, ErrNoEligibleRegion) {
		t.Fatalf("[%s] err = %v, want ErrNoEligibleRegion", runUUID, err)
	}
	if got != (Placement{}) {
		t.Fatalf("[%s] expected zero Placement on error, got %+v", runUUID, got)
	}
	t.Logf("[%s] proved no-eligible path: before(2 regions @200/500ms vs budget 50ms) "+
		"-> after(0 eligible) -> ErrNoEligibleRegion", runUUID)
}

// TestPlace_CarbonTieBreakByName proves the deterministic Name tiebreak when two
// eligible regions share the lowest carbon intensity.
func TestPlace_CarbonTieBreakByName(t *testing.T) {
	const runUUID = "2f48a0c9-6b1d-4e7a-9f33-1480cafe1480"

	regions := []Region{
		{Name: "Zulu", CarbonIntensity: 100, LatencyMS: 20},
		{Name: "Alpha", CarbonIntensity: 100, LatencyMS: 30}, // same carbon, smaller name -> wins
	}
	job := Job{ID: "j-tie", PowerWatts: 250, DurationHours: 8, MaxLatencyMS: 50}

	// Hand calc: KWh = 250/1000 * 8 = 2.0; GramsCO2 = 2.0 * 100 = 200
	got, err := Place(job, regions)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	if got.Region != "Alpha" {
		t.Fatalf("[%s] tiebreak failed: chose %q, want %q", runUUID, got.Region, "Alpha")
	}
	if !floatEq(got.KWh, 2.0) || !floatEq(got.GramsCO2, 200.0) {
		t.Fatalf("[%s] metering = (KWh %v, GramsCO2 %v), want (2.0, 200.0)",
			runUUID, got.KWh, got.GramsCO2)
	}
	t.Logf("[%s] proved deterministic tiebreak: Zulu==Alpha @100g -> chose Alpha (name<); GramsCO2=%vg",
		runUUID, got.GramsCO2)
}
