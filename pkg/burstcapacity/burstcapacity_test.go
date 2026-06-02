package burstcapacity

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// runUUID returns a random run identifier for log correlation.
func runUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// Clause 1: utilization ABOVE threshold with demand exceeding local capacity
// MUST create a burst allocation sized to the EXCESS (demand-localCapacity) on
// the chosen remote provider.
//
// Mutation: if the implementation estimates the FULL demand (demand) instead of
// the EXCESS (demand-localCapacity), EstimatedUnits would be 130 not 30 and
// this test fails. The expected value 30 is DERIVED from inputs (130-100).
func TestActivateBurst_AboveThreshold_SizesToExcess(t *testing.T) {
	uuid := runUUID(t)
	e := Estimator{Threshold: 0.90}

	localCapacity := 100.0
	demand := 130.0
	localUtil := 0.95 // strictly above 0.90

	providers := []Provider{
		{ID: "remote-a", CostPerUnit: 2.0, Capacity: 200},
	}

	// before: no spillover allocation exists.
	t.Logf("run=%s before: util=%.2f localCap=%.1f demand=%.1f spillover=none",
		uuid, localUtil, localCapacity, demand)

	alloc, activated := e.ActivateBurst(localUtil, localCapacity, demand, providers)

	if !activated {
		t.Fatalf("run=%s expected burst activated above threshold with excess demand", uuid)
	}

	// Excess is DERIVED: demand-localCapacity = 130-100 = 30.
	wantExcess := demand - localCapacity
	if alloc.EstimatedUnits != wantExcess {
		t.Fatalf("run=%s estimated units = %.1f, want EXCESS %.1f (not full demand %.1f)",
			uuid, alloc.EstimatedUnits, wantExcess, demand)
	}
	if alloc.EstimatedUnits == demand {
		t.Fatalf("run=%s estimated full demand %.1f instead of excess %.1f", uuid, demand, wantExcess)
	}
	if alloc.ProviderID != "remote-a" {
		t.Fatalf("run=%s provider = %q, want remote-a", uuid, alloc.ProviderID)
	}

	// after: a burst allocation now exists sized to the excess.
	t.Logf("run=%s after: burst created provider=%s units=%.1f cost=%.2f",
		uuid, alloc.ProviderID, alloc.EstimatedUnits, alloc.CostPerUnit)
}

// Clause 2a: BELOW threshold -> no burst, even if demand exceeds local capacity.
//
// Mutation: if activation ignored the threshold (always activates), this fails
// because util 0.50 <= 0.90 must yield no burst.
func TestActivateBurst_BelowThreshold_NoBurst(t *testing.T) {
	uuid := runUUID(t)
	e := Estimator{Threshold: 0.90}

	localCapacity := 100.0
	demand := 130.0 // exceeds capacity, but util is low
	localUtil := 0.50

	providers := []Provider{{ID: "remote-a", CostPerUnit: 1.0, Capacity: 500}}

	t.Logf("run=%s before: util=%.2f threshold=%.2f demand=%.1f", uuid, localUtil, e.Threshold, demand)

	alloc, activated := e.ActivateBurst(localUtil, localCapacity, demand, providers)

	if activated {
		t.Fatalf("run=%s expected NO burst below threshold, got provider=%q units=%.1f",
			uuid, alloc.ProviderID, alloc.EstimatedUnits)
	}
	if alloc != (Allocation{}) {
		t.Fatalf("run=%s expected zero Allocation, got %+v", uuid, alloc)
	}

	t.Logf("run=%s after: no burst activated (correct)", uuid)
}

// Clause 2b: above threshold but demand WITHIN local capacity -> no burst.
//
// Mutation: if activation didn't require positive excess (e.g. activated on
// threshold alone), this fails because demand 80 <= capacity 100 means no
// spillover.
func TestActivateBurst_DemandWithinCapacity_NoBurst(t *testing.T) {
	uuid := runUUID(t)
	e := Estimator{Threshold: 0.90}

	localCapacity := 100.0
	demand := 80.0 // within capacity
	localUtil := 0.97

	providers := []Provider{{ID: "remote-a", CostPerUnit: 1.0, Capacity: 500}}

	t.Logf("run=%s before: util=%.2f demand=%.1f localCap=%.1f", uuid, localUtil, demand, localCapacity)

	alloc, activated := e.ActivateBurst(localUtil, localCapacity, demand, providers)

	if activated {
		t.Fatalf("run=%s expected NO burst when demand within capacity, got %+v", uuid, alloc)
	}

	t.Logf("run=%s after: no burst (demand fits locally)", uuid)
}

// Clause 3: the cheapest SUFFICIENT remote provider is chosen.
//
// Mutation: if selection picked the first/any provider instead of the cheapest
// sufficient one, it would pick remote-expensive or remote-tiny. The cheapest
// overall (remote-cheap-tiny, cost 0.5) is INSUFFICIENT (cap 10 < excess 50),
// so it must be skipped; remote-cheap-big (cost 1.0) is the cheapest sufficient.
func TestActivateBurst_ChoosesCheapestSufficient(t *testing.T) {
	uuid := runUUID(t)
	e := Estimator{Threshold: 0.90}

	localCapacity := 100.0
	demand := 150.0 // excess = 50
	localUtil := 0.99

	providers := []Provider{
		{ID: "remote-expensive", CostPerUnit: 5.0, Capacity: 1000}, // sufficient but dear
		{ID: "remote-cheap-tiny", CostPerUnit: 0.5, Capacity: 10},  // cheapest but INSUFFICIENT
		{ID: "remote-cheap-big", CostPerUnit: 1.0, Capacity: 80},   // cheapest SUFFICIENT
		{ID: "remote-mid", CostPerUnit: 3.0, Capacity: 200},        // sufficient, pricier
	}

	wantExcess := demand - localCapacity // 50, derived

	t.Logf("run=%s before: util=%.2f demand=%.1f excess=%.1f providers=%d",
		uuid, localUtil, demand, wantExcess, len(providers))

	alloc, activated := e.ActivateBurst(localUtil, localCapacity, demand, providers)

	if !activated {
		t.Fatalf("run=%s expected burst activated", uuid)
	}
	if alloc.ProviderID != "remote-cheap-big" {
		t.Fatalf("run=%s chose %q (cost %.1f), want remote-cheap-big (cheapest sufficient)",
			uuid, alloc.ProviderID, alloc.CostPerUnit)
	}
	if alloc.CostPerUnit != 1.0 {
		t.Fatalf("run=%s cost = %.1f, want 1.0", uuid, alloc.CostPerUnit)
	}
	if alloc.EstimatedUnits != wantExcess {
		t.Fatalf("run=%s units = %.1f, want %.1f", uuid, alloc.EstimatedUnits, wantExcess)
	}

	t.Logf("run=%s after: chose provider=%s cost=%.2f units=%.1f",
		uuid, alloc.ProviderID, alloc.CostPerUnit, alloc.EstimatedUnits)
}

// Clause 3 (edge): when NO provider has sufficient capacity, no burst is
// activated even above threshold with positive excess.
//
// Mutation: if selection ignored capacity sufficiency, it would activate on the
// too-small provider; this asserts it does not.
func TestActivateBurst_NoSufficientProvider_NoBurst(t *testing.T) {
	uuid := runUUID(t)
	e := Estimator{Threshold: 0.90}

	localCapacity := 100.0
	demand := 200.0 // excess = 100
	localUtil := 0.99

	providers := []Provider{
		{ID: "remote-small-1", CostPerUnit: 0.5, Capacity: 30},
		{ID: "remote-small-2", CostPerUnit: 0.4, Capacity: 50},
	}

	t.Logf("run=%s before: excess=%.1f maxProviderCap=50", uuid, demand-localCapacity)

	alloc, activated := e.ActivateBurst(localUtil, localCapacity, demand, providers)

	if activated {
		t.Fatalf("run=%s expected NO burst (no sufficient provider), got %+v", uuid, alloc)
	}

	t.Logf("run=%s after: no burst (no provider can absorb excess)", uuid)
}
