package costtracker

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"testing"
)

// runUUID returns a random hex id so each test run is traceable in logs.
func runUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

const eps = 1e-9

// monthAlloc mirrors the package's allocation for test-side independent math.
type monthAlloc struct {
	rate  float64
	hours float64
	spot  bool
}

// derive computes the expected totals/blended/reduction INDEPENDENTLY of the
// package, straight from inputs and the formula in the spec. Used as the oracle.
func derive(allocs []monthAlloc, baseline float64) (totalCost, totalHours, blended, pctRed float64) {
	for _, a := range allocs {
		totalCost += a.rate * a.hours
		totalHours += a.hours
	}
	if totalHours > 0 {
		blended = totalCost / totalHours
	}
	baseTotal := baseline * totalHours
	if baseTotal != 0 {
		pctRed = (baseTotal - totalCost) / baseTotal
	}
	return
}

// Clause 1: a simulated month; blended == totalCost/totalHours and reduction ==
// hand-derived value. The allocations have DIFFERING durations so a naive
// average-of-rates implementation produces a different blended number.
//
// Mutation: blended computed as simple mean of rates (ignoring hours weighting)
// fails because durations differ; wrong baseline sign fails the reduction check.
func TestReport_MonthBlendedAndReduction(t *testing.T) {
	uuid := runUUID(t)

	// Mix of on-demand and spot allocations with deliberately different hours.
	month := []monthAlloc{
		{rate: 3.00, hours: 100, spot: false}, // on-demand, long
		{rate: 0.90, hours: 400, spot: true},  // spot, longer (drags blended down)
		{rate: 2.40, hours: 50, spot: false},  // on-demand, short
		{rate: 1.10, hours: 250, spot: true},  // spot, medium
	}
	const baseline = 3.20 // AWS on-demand baseline $/GPU-hr

	tr := New()
	for _, a := range month {
		tr.Record(a.rate, a.hours, a.spot)
	}

	wantCost, wantHours, wantBlended, wantRed := derive(month, baseline)
	t.Logf("run=%s BEFORE: want cost=%.6f hours=%.6f blended=%.6f reduction=%.6f",
		uuid, wantCost, wantHours, wantBlended, wantRed)

	got := tr.Report(baseline)
	t.Logf("run=%s AFTER: got cost=%.6f hours=%.6f blended=%.6f reduction=%.6f",
		uuid, got.TotalCost, got.TotalHours, got.BlendedPerHour, got.PctReductionVsBaseline)

	if math.Abs(got.TotalCost-wantCost) > eps {
		t.Fatalf("TotalCost = %.9f, want %.9f", got.TotalCost, wantCost)
	}
	if math.Abs(got.TotalHours-wantHours) > eps {
		t.Fatalf("TotalHours = %.9f, want %.9f", got.TotalHours, wantHours)
	}
	if math.Abs(got.BlendedPerHour-wantBlended) > eps {
		t.Fatalf("BlendedPerHour = %.9f, want %.9f", got.BlendedPerHour, wantBlended)
	}
	// Independent check that blended is exactly cost/hours per spec.
	if math.Abs(got.BlendedPerHour-(got.TotalCost/got.TotalHours)) > eps {
		t.Fatalf("BlendedPerHour %.9f != TotalCost/TotalHours %.9f",
			got.BlendedPerHour, got.TotalCost/got.TotalHours)
	}
	if math.Abs(got.PctReductionVsBaseline-wantRed) > eps {
		t.Fatalf("PctReductionVsBaseline = %.9f, want %.9f", got.PctReductionVsBaseline, wantRed)
	}

	// Guard against the naive-average mutation: with these inputs the simple
	// mean of rates differs measurably from the hours-weighted blended. If they
	// were equal the mutation test would be vacuous, so assert they diverge.
	var rateSum float64
	for _, a := range month {
		rateSum += a.rate
	}
	naiveMean := rateSum / float64(len(month))
	if math.Abs(naiveMean-got.BlendedPerHour) < 1e-3 {
		t.Fatalf("test setup weak: naive mean %.6f ~= blended %.6f; durations must differ enough",
			naiveMean, got.BlendedPerHour)
	}
}

// Clause 2: a spot-heavy (cheaper) month yields a LARGER % reduction than an
// on-demand-heavy month against the SAME baseline.
//
// Mutation: wrong baseline sign inverts which month "wins", failing this
// strictly-greater comparison.
func TestReport_SpotHeavyBeatsOnDemandHeavy(t *testing.T) {
	uuid := runUUID(t)
	const baseline = 3.00

	spotHeavy := []monthAlloc{
		{rate: 0.80, hours: 500, spot: true},
		{rate: 0.95, hours: 300, spot: true},
		{rate: 2.90, hours: 50, spot: false},
	}
	onDemandHeavy := []monthAlloc{
		{rate: 2.90, hours: 500, spot: false},
		{rate: 2.80, hours: 300, spot: false},
		{rate: 0.85, hours: 50, spot: true},
	}

	build := func(m []monthAlloc) Report {
		tr := New()
		for _, a := range m {
			tr.Record(a.rate, a.hours, a.spot)
		}
		return tr.Report(baseline)
	}

	spotR := build(spotHeavy)
	odR := build(onDemandHeavy)

	t.Logf("run=%s BEFORE: baseline=%.4f", uuid, baseline)
	t.Logf("run=%s AFTER: spotHeavy reduction=%.6f (cost=%.4f), onDemandHeavy reduction=%.6f (cost=%.4f)",
		uuid, spotR.PctReductionVsBaseline, spotR.TotalCost,
		odR.PctReductionVsBaseline, odR.TotalCost)

	if !(spotR.PctReductionVsBaseline > odR.PctReductionVsBaseline) {
		t.Fatalf("spot-heavy reduction %.6f should exceed on-demand-heavy %.6f",
			spotR.PctReductionVsBaseline, odR.PctReductionVsBaseline)
	}
	// Sanity: spot-heavy should beat baseline (positive reduction); on-demand
	// heavy here is near/below baseline so its reduction is smaller.
	if spotR.PctReductionVsBaseline <= 0 {
		t.Fatalf("spot-heavy reduction should be positive, got %.6f", spotR.PctReductionVsBaseline)
	}
}

// Clause 3: zero allocations -> a well-defined empty report, no divide-by-zero.
//
// Mutation: removing the totalHours>0 / baselineTotal!=0 guards would produce
// NaN/Inf, which this test rejects.
func TestReport_EmptyIsWellDefined(t *testing.T) {
	uuid := runUUID(t)
	tr := New()

	t.Logf("run=%s BEFORE: empty tracker, baseline=3.00", uuid)
	got := tr.Report(3.00)
	t.Logf("run=%s AFTER: cost=%.6f hours=%.6f blended=%.6f reduction=%.6f",
		uuid, got.TotalCost, got.TotalHours, got.BlendedPerHour, got.PctReductionVsBaseline)

	if got.TotalCost != 0 || got.TotalHours != 0 {
		t.Fatalf("empty totals: cost=%.6f hours=%.6f, want 0/0", got.TotalCost, got.TotalHours)
	}
	if got.BlendedPerHour != 0 {
		t.Fatalf("empty blended = %.6f, want 0", got.BlendedPerHour)
	}
	if got.PctReductionVsBaseline != 0 {
		t.Fatalf("empty reduction = %.6f, want 0", got.PctReductionVsBaseline)
	}
	if math.IsNaN(got.BlendedPerHour) || math.IsInf(got.BlendedPerHour, 0) {
		t.Fatalf("empty blended is NaN/Inf: %v", got.BlendedPerHour)
	}
	if math.IsNaN(got.PctReductionVsBaseline) || math.IsInf(got.PctReductionVsBaseline, 0) {
		t.Fatalf("empty reduction is NaN/Inf: %v", got.PctReductionVsBaseline)
	}
}

// Extra guard: zero baseline rate must not divide-by-zero even with real
// allocations recorded.
func TestReport_ZeroBaselineNoDivByZero(t *testing.T) {
	uuid := runUUID(t)
	tr := New()
	tr.Record(1.5, 10, true)
	tr.Record(2.0, 5, false)

	got := tr.Report(0)
	t.Logf("run=%s zero-baseline: cost=%.4f hours=%.4f blended=%.4f reduction=%.4f",
		uuid, got.TotalCost, got.TotalHours, got.BlendedPerHour, got.PctReductionVsBaseline)

	wantCost := 1.5*10 + 2.0*5
	if math.Abs(got.TotalCost-wantCost) > eps {
		t.Fatalf("TotalCost = %.6f, want %.6f", got.TotalCost, wantCost)
	}
	if got.PctReductionVsBaseline != 0 {
		t.Fatalf("zero-baseline reduction = %.6f, want 0 (guard)", got.PctReductionVsBaseline)
	}
	if math.IsNaN(got.PctReductionVsBaseline) || math.IsInf(got.PctReductionVsBaseline, 0) {
		t.Fatalf("zero-baseline reduction is NaN/Inf: %v", got.PctReductionVsBaseline)
	}
}
