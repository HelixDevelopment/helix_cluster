package costsched

import (
	"errors"
	"math"
	"testing"
)

const runUUID = "costsched-run-3f1c8a92-7b6e-4d52-9a01-cc1499hxc"

// minCostID derives, from the provided eligible candidates, the ID with the
// lowest CostPerHour (lexicographic tie-break) and that minimum cost. Tests use
// this to derive expectations from input rather than hardcoding them.
func minCostID(cands []Candidate) (string, float64) {
	bestID := ""
	bestCost := math.Inf(1)
	for _, c := range cands {
		if c.CostPerHour < bestCost || (c.CostPerHour == bestCost && c.ID < bestID) {
			bestCost = c.CostPerHour
			bestID = c.ID
		}
	}
	return bestID, bestCost
}

// TestSelectChoosesCheapestAmongAllEligible proves closure clause 1: when every
// candidate meets the SLA, Select returns the CHEAPEST one and records its cost.
//
// Mutation: replacing min with max in Select (or "<" with ">" in the sort) makes
// this fail because the expected ID/cost are derived as the minimum from input.
func TestSelectChoosesCheapestAmongAllEligible(t *testing.T) {
	sla := SLA{MinVRAMGiB: 8, MaxLatencyMs: 100, RequiredModel: "llama3"}
	cands := []Candidate{
		{ID: "gpu-a", CostPerHour: 3.50, VRAMGiB: 24, MaxLatencyMs: 40, ModelsSupported: map[string]bool{"llama3": true}},
		{ID: "gpu-b", CostPerHour: 1.25, VRAMGiB: 16, MaxLatencyMs: 50, ModelsSupported: map[string]bool{"llama3": true}},
		{ID: "gpu-c", CostPerHour: 2.75, VRAMGiB: 32, MaxLatencyMs: 30, ModelsSupported: map[string]bool{"llama3": true}},
	}
	// Sanity: every candidate must be eligible for this clause to be meaningful.
	for _, c := range cands {
		if !c.Meets(sla) {
			t.Fatalf("[%s] precondition violated: %s does not meet SLA", runUUID, c.ID)
		}
	}

	wantID, wantCost := minCostID(cands)
	t.Logf("[%s] before: %d eligible candidates, derived min ID=%s cost=%.4f", runUUID, len(cands), wantID, wantCost)

	s := NewCostAwareScheduler()
	got, err := s.Select(cands, sla)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	t.Logf("[%s] after: Select chose ID=%s cost=%.4f", runUUID, got.ID, got.CostPerHour)

	if got.ID != wantID {
		t.Fatalf("[%s] chosen ID = %s, want cheapest %s", runUUID, got.ID, wantID)
	}
	if got.CostPerHour != wantCost {
		t.Fatalf("[%s] chosen cost = %.4f, want min %.4f", runUUID, got.CostPerHour, wantCost)
	}
	// Independently confirm it is the strict minimum over input cost.
	for _, c := range cands {
		if c.CostPerHour < got.CostPerHour {
			t.Fatalf("[%s] %s (cost %.4f) is cheaper than chosen %s (cost %.4f)", runUUID, c.ID, c.CostPerHour, got.ID, got.CostPerHour)
		}
	}
	// Recorded selection cost must equal the chosen cost.
	recorded, ok := s.LastSelectedCost()
	if !ok {
		t.Fatalf("[%s] LastSelectedCost not recorded after successful Select", runUUID)
	}
	if recorded != got.CostPerHour {
		t.Fatalf("[%s] recorded cost = %.4f, want chosen %.4f", runUUID, recorded, got.CostPerHour)
	}
	t.Logf("[%s] recorded selection cost=%.4f matches chosen", runUUID, recorded)
}

// TestSLAFilterExcludesCheapButIneligible proves closure clause 2: a cheaper
// candidate that fails the SLA (insufficient VRAM) is excluded, and a more
// expensive SLA-meeting candidate is chosen instead. It demonstrates the
// before/after when the SLA tightens.
//
// Mutation: removing the SLA filter in Select (selecting purely by min cost)
// makes this fail because the cheap-but-ineligible candidate would be chosen.
func TestSLAFilterExcludesCheapButIneligible(t *testing.T) {
	// "cheap-small" is the cheapest overall but has too little VRAM.
	cheapIneligible := Candidate{ID: "cheap-small", CostPerHour: 0.50, VRAMGiB: 4, MaxLatencyMs: 20, ModelsSupported: map[string]bool{"llama3": true}}
	expensiveEligible := Candidate{ID: "big-pricey", CostPerHour: 4.00, VRAMGiB: 48, MaxLatencyMs: 20, ModelsSupported: map[string]bool{"llama3": true}}
	cands := []Candidate{cheapIneligible, expensiveEligible}

	// BEFORE: loose SLA — both eligible, so the cheap one wins.
	looseSLA := SLA{MinVRAMGiB: 4, MaxLatencyMs: 100, RequiredModel: "llama3"}
	sBefore := NewCostAwareScheduler()
	gotBefore, err := sBefore.Select(cands, looseSLA)
	if err != nil {
		t.Fatalf("[%s] before: unexpected error: %v", runUUID, err)
	}
	t.Logf("[%s] before (loose SLA MinVRAM=%d): chose ID=%s cost=%.2f", runUUID, looseSLA.MinVRAMGiB, gotBefore.ID, gotBefore.CostPerHour)
	if gotBefore.ID != cheapIneligible.ID {
		t.Fatalf("[%s] before: with loose SLA expected cheap %s, got %s", runUUID, cheapIneligible.ID, gotBefore.ID)
	}

	// AFTER: tighten SLA so the cheap candidate no longer qualifies (VRAM).
	tightSLA := SLA{MinVRAMGiB: 16, MaxLatencyMs: 100, RequiredModel: "llama3"}
	if cheapIneligible.Meets(tightSLA) {
		t.Fatalf("[%s] precondition violated: cheap candidate unexpectedly meets tight SLA", runUUID)
	}
	if !expensiveEligible.Meets(tightSLA) {
		t.Fatalf("[%s] precondition violated: expensive candidate must meet tight SLA", runUUID)
	}

	sAfter := NewCostAwareScheduler()
	gotAfter, err := sAfter.Select(cands, tightSLA)
	if err != nil {
		t.Fatalf("[%s] after: unexpected error: %v", runUUID, err)
	}
	t.Logf("[%s] after (tight SLA MinVRAM=%d): chose ID=%s cost=%.2f", runUUID, tightSLA.MinVRAMGiB, gotAfter.ID, gotAfter.CostPerHour)

	if gotAfter.ID == cheapIneligible.ID {
		t.Fatalf("[%s] after: cheap-but-ineligible %s was wrongly selected (SLA filter not applied)", runUUID, cheapIneligible.ID)
	}
	if gotAfter.ID != expensiveEligible.ID {
		t.Fatalf("[%s] after: expected SLA-meeting %s, got %s", runUUID, expensiveEligible.ID, gotAfter.ID)
	}
	if gotAfter.CostPerHour <= cheapIneligible.CostPerHour {
		t.Fatalf("[%s] after: chosen cost %.2f should exceed the cheap ineligible %.2f, proving cost was not the only criterion", runUUID, gotAfter.CostPerHour, cheapIneligible.CostPerHour)
	}
	recorded, ok := sAfter.LastSelectedCost()
	if !ok || recorded != expensiveEligible.CostPerHour {
		t.Fatalf("[%s] after: recorded cost = %.2f (ok=%v), want %.2f", runUUID, recorded, ok, expensiveEligible.CostPerHour)
	}
}

// TestSelectNoneEligibleReturnsErr proves closure clause 3: when no candidate
// meets the SLA, Select returns ErrNoEligible and records no selection.
//
// Mutation: weakening Meets to return true (or skipping the empty-eligible
// check) makes this fail because a candidate would be returned without error.
func TestSelectNoneEligibleReturnsErr(t *testing.T) {
	sla := SLA{MinVRAMGiB: 80, MaxLatencyMs: 10, RequiredModel: "gpt-huge"}
	cands := []Candidate{
		{ID: "gpu-a", CostPerHour: 1.00, VRAMGiB: 16, MaxLatencyMs: 50, ModelsSupported: map[string]bool{"llama3": true}},
		{ID: "gpu-b", CostPerHour: 2.00, VRAMGiB: 24, MaxLatencyMs: 30, ModelsSupported: map[string]bool{"mistral": true}},
	}
	eligibleCount := 0
	for _, c := range cands {
		if c.Meets(sla) {
			eligibleCount++
		}
	}
	t.Logf("[%s] before: %d candidates, %d meet the SLA", runUUID, len(cands), eligibleCount)

	s := NewCostAwareScheduler()
	got, err := s.Select(cands, sla)
	if !errors.Is(err, ErrNoEligible) {
		t.Fatalf("[%s] after: err = %v, want ErrNoEligible (got candidate ID=%q)", runUUID, err, got.ID)
	}
	t.Logf("[%s] after: Select returned ErrNoEligible as required", runUUID)

	if _, ok := s.LastSelectedCost(); ok {
		t.Fatalf("[%s] after: LastSelectedCost recorded despite no eligible candidate", runUUID)
	}
}

// TestSelectDeterministicTieBreak guards the tie-break path so the cheapest
// selection is reproducible when two eligible candidates share the min cost.
//
// Mutation: dropping the ID tie-break (return false) makes the result depend on
// input order; this asserts the lexicographically smallest ID always wins.
func TestSelectDeterministicTieBreak(t *testing.T) {
	sla := SLA{MinVRAMGiB: 8, MaxLatencyMs: 100}
	cands := []Candidate{
		{ID: "zeta", CostPerHour: 1.00, VRAMGiB: 16, MaxLatencyMs: 10},
		{ID: "alpha", CostPerHour: 1.00, VRAMGiB: 16, MaxLatencyMs: 10},
	}
	s := NewCostAwareScheduler()
	got, err := s.Select(cands, sla)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	t.Logf("[%s] tie at cost %.2f -> chose ID=%s", runUUID, got.CostPerHour, got.ID)
	if got.ID != "alpha" {
		t.Fatalf("[%s] tie-break: want lexicographically smallest 'alpha', got %s", runUUID, got.ID)
	}
}
