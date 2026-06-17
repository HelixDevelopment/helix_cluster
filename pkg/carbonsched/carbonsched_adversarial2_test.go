package carbonsched

import (
	"errors"
	"math"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// ADVERSARIAL SUITE 2 — DEEPER second pass (the NaN-CarbonIntensity vector is
// already fixed; these hunt what the first pass MISSED).
//
// Contract under attack (carbonsched.go Place doc, lines 46-56):
//
//	"Only regions whose LatencyMS <= job.MaxLatencyMS are considered eligible.
//	 Among the eligible regions, the one with the LOWEST CarbonIntensity is
//	 chosen; ties are broken deterministically by the lexicographically smallest
//	 Name. If no region is eligible, ErrNoEligibleRegion is returned."
//
// The first pass guarded NaN in CarbonIntensity. It did NOT guard the SYMMETRIC
// hole in the *latency* filter: the eligibility test is `r.LatencyMS >
// job.MaxLatencyMS { continue }`. Because EVERY comparison against NaN is false,
// `NaN > Max` is false, so a region with a NaN ("unknown"/sensor-error) latency
// is NOT skipped — it is admitted as eligible. But the documented eligibility
// predicate is `LatencyMS <= MaxLatencyMS`, which is FALSE for a NaN latency
// (NaN <= Max is false). So the implementation ADMITS a region the contract says
// is NOT eligible. If that unknown-latency region also has the lowest carbon, it
// is selected as "greenest" and its intensity is metered into the GramsCO2 sink —
// a region whose latency we literally do not know wins the placement.
//
// This is the same CLASS of defect as the fixed NaN-CarbonIntensity poison
// (a NaN sensor reading silently corrupting selection), just on the latency axis.
// These reproducers assert SINK-SIDE (the chosen Region.Name + emitted GramsCO2),
// not merely "no panic".
// ─────────────────────────────────────────────────────────────────────────────

// TestAdversarial2_NaNLatencyMustNotBeEligible is the load-bearing teeth for the
// latency-filter NaN hole. A region with NaN latency and the LOWEST carbon sits
// next to a genuinely eligible region. The documented predicate `LatencyMS <=
// MaxLatencyMS` is FALSE for NaN, so the NaN-latency region MUST be filtered out
// and the eligible "Known" region chosen.
//
// Mutation falsifiability: on the unfixed comparator (`NaN > Max` == false), the
// NaN-latency region is admitted, wins on carbon (10 < 100), and Place returns
// "UnknownLat" metering 10 gCO2/kWh — a region of unknown latency selected as
// greenest. The assertion below FAILS in that case (chose UnknownLat, want Known).
func TestAdversarial2_NaNLatencyMustNotBeEligible(t *testing.T) {
	const runUUID = "decade02-1480-4lat-9101-nanlateligbl"

	regions := []Region{
		{Name: "UnknownLat", CarbonIntensity: 10, LatencyMS: math.NaN()}, // unknown latency, tempting carbon
		{Name: "Known", CarbonIntensity: 100, LatencyMS: 20},             // the only truly eligible region
	}
	job := Job{ID: "j-nanlat", PowerWatts: 1000, DurationHours: 1, MaxLatencyMS: 50}

	got, err := Place(job, regions)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v (a genuinely eligible region exists)", runUUID, err)
	}

	// SINK ASSERTION 1: chosen region must be the latency-eligible "Known".
	if got.Region != "Known" {
		t.Fatalf("[%s] NaN-LATENCY POISON: Place chose %q, but its LatencyMS is NaN — "+
			"the documented eligibility predicate (LatencyMS <= MaxLatencyMS) is FALSE for "+
			"NaN, so it must be filtered out and \"Known\" chosen. chosen=%+v",
			runUUID, got.Region, got)
	}
	// SINK ASSERTION 2: metered emission must be Known's finite value, not the
	// unknown-latency region's. KWh = 1000/1000*1 = 1.0 ; Known = 1.0*100 = 100.
	if !floatEq(got.KWh, 1.0) || !floatEq(got.GramsCO2, 100.0) {
		t.Fatalf("[%s] metering=(KWh %v, GramsCO2 %v), want (1.0, 100.0)",
			runUUID, got.KWh, got.GramsCO2)
	}
	t.Logf("[%s] proved NaN latency is not eligible: chose Known@100g (not UnknownLat@10g)", runUUID)
}

// TestAdversarial2_NaNLatencyOnlyRegion_NoEligible proves that when the ONLY
// region has a NaN latency, Place returns ErrNoEligibleRegion — not a corrupt
// placement of an unknown-latency region. With nothing eligible, the contract
// says ErrNoEligibleRegion + zero Placement.
//
// Mutation falsifiability: on unfixed prod the NaN-latency region is admitted and
// returned as the placement (no error, GramsCO2 = 1.0*42 = 42), so the
// errors.Is(...ErrNoEligibleRegion) assertion FAILS.
func TestAdversarial2_NaNLatencyOnlyRegion_NoEligible(t *testing.T) {
	const runUUID = "decade02-1480-4lat-9102-nanlatonly02"

	regions := []Region{
		{Name: "UnknownLat", CarbonIntensity: 42, LatencyMS: math.NaN()},
	}
	job := Job{ID: "j-nanlat-only", PowerWatts: 1000, DurationHours: 1, MaxLatencyMS: 50}

	got, err := Place(job, regions)
	if !errors.Is(err, ErrNoEligibleRegion) {
		t.Fatalf("[%s] NaN-LATENCY POISON: a single unknown-latency region must yield "+
			"ErrNoEligibleRegion (NaN <= Max is false), got err=%v placement=%+v",
			runUUID, err, got)
	}
	if got != (Placement{}) {
		t.Fatalf("[%s] expected zero Placement on no-eligible, got %+v", runUUID, got)
	}
	t.Logf("[%s] proved sole NaN-latency region -> ErrNoEligibleRegion, zero Placement", runUUID)
}

// TestAdversarial2_NaNLatencyOrderIndependent is the total-order teeth on the
// latency axis: the SAME region set must select the SAME winner regardless of
// whether the NaN-latency region is scanned first or last. On unfixed prod the
// NaN-latency region is eligible in BOTH orderings (so it actually selects
// consistently-wrong here) — but combined with the carbon comparator the broader
// risk is order sensitivity. This test pins that the eligible "Known" is chosen
// in both orderings once the NaN-latency hole is closed.
func TestAdversarial2_NaNLatencyOrderIndependent(t *testing.T) {
	const runUUID = "decade02-1480-4lat-9103-nanlatorder3"

	unknown := Region{Name: "UnknownLat", CarbonIntensity: 5, LatencyMS: math.NaN()}
	known := Region{Name: "Known", CarbonIntensity: 250, LatencyMS: 30}
	job := Job{ID: "j-nanlat-ord", PowerWatts: 500, DurationHours: 2, MaxLatencyMS: 100}

	gotFirst, errF := Place(job, []Region{unknown, known}) // NaN-latency first
	gotLast, errL := Place(job, []Region{known, unknown})  // NaN-latency last
	if errF != nil || errL != nil {
		t.Fatalf("[%s] unexpected errors: first=%v last=%v", runUUID, errF, errL)
	}
	if gotFirst.Region != gotLast.Region {
		t.Fatalf("[%s] ORDER-DEPENDENT under NaN latency: [Unknown,Known]->%q but "+
			"[Known,Unknown]->%q", runUUID, gotFirst.Region, gotLast.Region)
	}
	if gotFirst.Region != "Known" {
		t.Fatalf("[%s] both orderings chose %q but the only latency-eligible region is "+
			"\"Known\" (UnknownLat has NaN latency)", runUUID, gotFirst.Region)
	}
	t.Logf("[%s] proved NaN-latency order-independence: both orderings chose Known", runUUID)
}

// ─────────────────────────────────────────────────────────────────────────────
// DOCUMENTED-CONTRACT PINS (these PASS on current prod; they lock in behavior so
// a future refactor that breaks them is caught).
// ─────────────────────────────────────────────────────────────────────────────

// TestAdversarial2_LatencyBoundaryInclusive_Pin pins the EXACT latency boundary:
// the doc says eligibility is `LatencyMS <= MaxLatencyMS` (INCLUSIVE). A region
// whose latency exactly equals the budget MUST be eligible; one a hair above MUST
// be excluded. This catches an off-by-one flip of `>` to `>=` (which would wrongly
// reject the exact-boundary region).
func TestAdversarial2_LatencyBoundaryInclusive_Pin(t *testing.T) {
	const runUUID = "decade02-1480-4bnd-9104-boundary0004"

	job := Job{ID: "j-bnd", PowerWatts: 1000, DurationHours: 1, MaxLatencyMS: 100}

	// EXACTLY at the budget -> eligible (inclusive).
	gotEq, errEq := Place(job, []Region{{Name: "Exact", CarbonIntensity: 77, LatencyMS: 100}})
	if errEq != nil {
		t.Fatalf("[%s] boundary==: latency exactly == budget must be ELIGIBLE (<=), got err=%v", runUUID, errEq)
	}
	if gotEq.Region != "Exact" {
		t.Fatalf("[%s] boundary==: chose %q want Exact", runUUID, gotEq.Region)
	}

	// One ulp above the budget -> excluded.
	just := math.Nextafter(100, math.Inf(1)) // smallest float64 > 100
	gotOver, errOver := Place(job, []Region{{Name: "JustOver", CarbonIntensity: 1, LatencyMS: just}})
	if !errors.Is(errOver, ErrNoEligibleRegion) {
		t.Fatalf("[%s] boundary+ulp: latency just above budget (%v) must be EXCLUDED, got err=%v placement=%+v",
			runUUID, just, errOver, gotOver)
	}
	t.Logf("[%s] pinned inclusive latency boundary: ==budget eligible, +1ulp excluded", runUUID)
}

// TestAdversarial2_InputSliceNotMutated_Pin proves Place does not mutate the
// caller's region slice (it takes `r := &regions[i]` internally). Aliasing /
// in-place mutation of a shared candidate set would corrupt subsequent calls.
func TestAdversarial2_InputSliceNotMutated_Pin(t *testing.T) {
	const runUUID = "decade02-1480-4ali-9105-noaliasing05"

	regions := []Region{
		{Name: "A", CarbonIntensity: 300, LatencyMS: 10},
		{Name: "B", CarbonIntensity: 50, LatencyMS: 20},
		{Name: "C", CarbonIntensity: 900, LatencyMS: 30},
	}
	snapshot := make([]Region, len(regions))
	copy(snapshot, regions)

	job := Job{ID: "j-alias", PowerWatts: 400, DurationHours: 2, MaxLatencyMS: 100}
	if _, err := Place(job, regions); err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	for i := range regions {
		if regions[i] != snapshot[i] {
			t.Fatalf("[%s] INPUT MUTATED: regions[%d] = %+v, was %+v", runUUID, i, regions[i], snapshot[i])
		}
	}
	t.Logf("[%s] pinned input-slice isolation: caller regions byte-identical after Place", runUUID)
}

// ─────────────────────────────────────────────────────────────────────────────
// LATENT-RISK NOTE (PIN of CURRENT behavior — NOT asserting it is wrong):
//
// A NEGATIVE CarbonIntensity (a physically-impossible / sensor-error grid value)
// is NOT guarded by the NaN guard and currently WINS as "greenest", emitting a
// NEGATIVE GramsCO2 into the metering sink. The documented contract only promises
// "lowest CarbonIntensity", and a negative value IS the lowest, so this is the
// literal documented behavior — but it is a latent footgun: a single bad sensor
// reading of -1 gCO2/kWh would out-rank every real clean region and emit negative
// emissions. This pin records the CURRENT contract behavior so a future hardening
// (reject negatives like NaN) is a deliberate, test-visible decision.
// ─────────────────────────────────────────────────────────────────────────────

// TestAdversarial2_NegativeCarbon_CurrentContractPin documents (does not condemn)
// that a negative carbon intensity is selected as the minimum and produces a
// negative GramsCO2 — the literal "lowest CarbonIntensity" contract. If a future
// change rejects negatives, this pin will fail and force an explicit doc update.
func TestAdversarial2_NegativeCarbon_CurrentContractPin(t *testing.T) {
	const runUUID = "decade02-1480-4neg-9106-negcarbon006"

	regions := []Region{
		{Name: "BadSensor", CarbonIntensity: -500, LatencyMS: 10}, // impossible value
		{Name: "Real", CarbonIntensity: 50, LatencyMS: 20},
	}
	job := Job{ID: "j-neg", PowerWatts: 1000, DurationHours: 1, MaxLatencyMS: 100}

	got, err := Place(job, regions)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	// CURRENT contract: lowest CarbonIntensity wins -> the negative one.
	if got.Region != "BadSensor" {
		t.Fatalf("[%s] documented-contract drift: negative carbon is the numeric minimum "+
			"so it currently wins; got %q. If this changed by design, update the Place doc "+
			"and this pin together.", runUUID, got.Region)
	}
	if got.GramsCO2 >= 0 {
		t.Fatalf("[%s] expected negative GramsCO2 from negative intensity, got %v", runUUID, got.GramsCO2)
	}
	t.Logf("[%s] PINNED current contract (LATENT RISK): negative carbon -500 wins, "+
		"GramsCO2=%v < 0. Documented as lowest-intensity; flagged for future hardening.",
		runUUID, got.GramsCO2)
}
