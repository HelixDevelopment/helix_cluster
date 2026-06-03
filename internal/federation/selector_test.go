package federation

import (
	"errors"
	"testing"
	"time"
)

// threeCells returns a deterministic 3-cell topology used across tests.
func threeCells() []Cell {
	return []Cell{
		{Name: "cell-eu-west", Region: "eu-west", LatencyMS: 12, CostPerHour: 1.2,
			ComplianceTags: []string{"gdpr", "soc2"}, Health: CellReady},
		{Name: "cell-eu-central", Region: "eu-central", LatencyMS: 25, CostPerHour: 0.8,
			ComplianceTags: []string{"gdpr"}, Health: CellReady},
		{Name: "cell-us-east", Region: "us-east", LatencyMS: 90, CostPerHour: 0.5,
			ComplianceTags: []string{"soc2", "hipaa"}, Health: CellReady},
	}
}

// deterministicSelector wires fixed UUID + advancing clock so Elapsed is
// non-zero and reproducible.
func deterministicSelector() *Selector {
	calls := 0
	return &Selector{
		now: func() time.Time {
			calls++
			return time.Unix(0, 0).Add(time.Duration(calls) * time.Millisecond)
		},
		newUUID: func() string { return "test-run-uuid-0001" },
	}
}

func TestSelect_DataLocalityWins(t *testing.T) {
	s := deterministicSelector()
	d, err := s.Select(threeCells(), Constraints{PreferredRegion: "eu-central"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Chosen != "cell-eu-central" {
		t.Fatalf("data locality: chose %q want cell-eu-central\nranked=%+v", d.Chosen, d.Ranked)
	}
}

func TestSelect_CostConstraintWins(t *testing.T) {
	s := deterministicSelector()
	// No region preference, cost weighted heavily -> cheapest (us-east) wins.
	d, err := s.Select(threeCells(), Constraints{
		Weights: Weights{Cost: 1.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Chosen != "cell-us-east" {
		t.Fatalf("cost: chose %q want cell-us-east\nranked=%+v", d.Chosen, d.Ranked)
	}
}

func TestSelect_LatencyConstraintWins(t *testing.T) {
	s := deterministicSelector()
	d, err := s.Select(threeCells(), Constraints{Weights: Weights{Latency: 1.0}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Chosen != "cell-eu-west" { // lowest latency 12ms
		t.Fatalf("latency: chose %q want cell-eu-west\nranked=%+v", d.Chosen, d.Ranked)
	}
}

func TestSelect_ComplianceHardFilter(t *testing.T) {
	s := deterministicSelector()
	// Require hipaa -> only cell-us-east qualifies even though others are
	// closer/cheaper-region.
	d, err := s.Select(threeCells(), Constraints{
		RequiredCompliance: []string{"hipaa"},
		PreferredRegion:    "eu-west", // would prefer eu-west but it lacks hipaa
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Chosen != "cell-us-east" {
		t.Fatalf("compliance: chose %q want cell-us-east\nranked=%+v", d.Chosen, d.Ranked)
	}
	// The eu cells must be recorded as filtered with a reason.
	var filteredEU int
	for _, cs := range d.Ranked {
		if cs.Cell.Region != "us-east" && cs.Filtered {
			filteredEU++
		}
	}
	if filteredEU != 2 {
		t.Fatalf("expected 2 eu cells filtered for missing hipaa, got %d\nranked=%+v", filteredEU, d.Ranked)
	}
}

func TestSelect_MaxLatencyAndCostFilters(t *testing.T) {
	s := deterministicSelector()
	d, err := s.Select(threeCells(), Constraints{MaxLatencyMS: 30}) // excludes us-east (90)
	if err != nil {
		t.Fatal(err)
	}
	for _, cs := range d.Ranked {
		if cs.Cell.Name == "cell-us-east" && !cs.Filtered {
			t.Fatalf("us-east should be filtered by MaxLatencyMS\n%+v", cs)
		}
	}

	d2, err := s.Select(threeCells(), Constraints{MaxCostPerHour: 0.6}) // only us-east(0.5) survives
	if err != nil {
		t.Fatal(err)
	}
	if d2.Chosen != "cell-us-east" {
		t.Fatalf("cost filter: chose %q want cell-us-east", d2.Chosen)
	}
}

func TestSelect_NoEligibleCell(t *testing.T) {
	s := deterministicSelector()
	_, err := s.Select(threeCells(), Constraints{RequiredCompliance: []string{"fedramp-high"}})
	if !errors.Is(err, ErrNoEligibleCell) {
		t.Fatalf("expected ErrNoEligibleCell, got %v", err)
	}
}

func TestSelect_EmitsRunUUID(t *testing.T) {
	s := NewSelector() // real UUID generator
	d, err := s.Select(threeCells(), Constraints{PreferredRegion: "eu-west"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.RunUUID) != 36 { // canonical UUID length
		t.Fatalf("expected a per-run UUID, got %q", d.RunUUID)
	}
	d2, _ := s.Select(threeCells(), Constraints{PreferredRegion: "eu-west"})
	if d.RunUUID == d2.RunUUID {
		t.Fatalf("run UUIDs must be unique per decision: %q == %q", d.RunUUID, d2.RunUUID)
	}
}

func TestReselect_FailsOverToSurvivingCell(t *testing.T) {
	s := deterministicSelector()
	cells := threeCells()
	// Initial placement: data locality eu-west.
	first, err := s.Select(cells, Constraints{PreferredRegion: "eu-west"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Chosen != "cell-eu-west" {
		t.Fatalf("initial placement chose %q want cell-eu-west", first.Chosen)
	}

	// Mark the hosting cell NotReady (gateway killed) and reselect.
	for i := range cells {
		if cells[i].Name == "cell-eu-west" {
			cells[i].Health = CellNotReady
		}
	}
	d, err := s.Reselect(cells, Constraints{PreferredRegion: "eu-west"}, "cell-eu-west")
	if err != nil {
		t.Fatal(err)
	}
	if !d.FailedOver || d.PreviousCell != "cell-eu-west" {
		t.Fatalf("expected failover from cell-eu-west, got %+v", d)
	}
	if d.Chosen == "cell-eu-west" || d.Chosen == "" {
		t.Fatalf("failover must pick a surviving cell, chose %q", d.Chosen)
	}
	// Surviving choice should be eu-central (gdpr, next best after eu-west for
	// an eu-west locality preference under default weights).
	if d.Chosen != "cell-eu-central" {
		t.Fatalf("failover chose %q want cell-eu-central\nranked=%+v", d.Chosen, d.Ranked)
	}
}
