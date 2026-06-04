//go:build chaos

package federation

import (
	"sync"
	"testing"
)

// chaos_test.go exercises the global cell-selection / failover layer under cell
// failures (HXC-937, Constitution §11.4.85). It drives the REAL FakeHub state
// machine + the REAL Selector. The fault injected is a cascade of cell-gateway
// kills; the sink-side invariant is that placement reconciles onto a SURVIVING,
// Ready cell after each kill, and that when no Ready cell remains the selector
// correctly refuses to place (no phantom placement onto a dead cell).
//
// Run with: go test -tags chaos ./internal/federation/...

func cellsForChaos() []Cell {
	return []Cell{
		{Name: "eu-1", Region: "eu", LatencyMS: 10, CostPerHour: 1.0, ComplianceTags: []string{"gdpr"}, Health: CellReady},
		{Name: "eu-2", Region: "eu", LatencyMS: 20, CostPerHour: 1.2, ComplianceTags: []string{"gdpr"}, Health: CellReady},
		{Name: "eu-3", Region: "eu", LatencyMS: 30, CostPerHour: 0.9, ComplianceTags: []string{"gdpr"}, Health: CellReady},
	}
}

// TestChaosCascadingCellKillFailover kills the hosting cell repeatedly. After
// each kill the selector must reselect onto a different, still-Ready cell, and
// the hub must reflect the new placement. The final kill leaves no Ready cell;
// the selector MUST then return ErrNoEligibleCell rather than place on a dead
// cell (the false-availability guard).
func TestChaosCascadingCellKillFailover(t *testing.T) {
	hub := NewFakeHub(cellsForChaos())
	sel := NewSelector()
	cons := Constraints{PreferredRegion: "eu", RequiredCompliance: []string{"gdpr"}}

	const workload = "inference-svc"

	// Initial placement onto the best Ready cell.
	d, err := sel.Select(hub.Cells(), cons)
	if err != nil {
		t.Fatalf("initial select: %v", err)
	}
	hub.Place(workload, d.Chosen, 3)
	if got := hub.HostingCell(workload); got != d.Chosen {
		t.Fatalf("hub placement mismatch: hub=%q decision=%q", got, d.Chosen)
	}

	seen := map[string]bool{d.Chosen: true}

	// Kill the hosting cell twice; each time we must fail over to a NEW Ready cell.
	for round := 0; round < 2; round++ {
		down := hub.HostingCell(workload)
		hub.KillCell(down)

		// SINK-SIDE: the killed cell is reported NotReady by the hub.
		for _, c := range hub.Cells() {
			if c.Name == down && c.Health != CellNotReady {
				t.Fatalf("round %d: killed cell %q still Ready in hub", round, down)
			}
		}

		dec, err := sel.Reselect(hub.Cells(), cons, down)
		if err != nil {
			t.Fatalf("round %d: reselect after killing %q: %v", round, down, err)
		}
		if dec.Chosen == down {
			t.Fatalf("round %d: reselected the dead cell %q (no failover)", round, down)
		}
		// Chosen must be a Ready cell.
		for _, c := range hub.Cells() {
			if c.Name == dec.Chosen && c.Health == CellNotReady {
				t.Fatalf("round %d: failed over onto NotReady cell %q", round, dec.Chosen)
			}
		}
		hub.Place(workload, dec.Chosen, 3)
		seen[dec.Chosen] = true

		// SINK-SIDE: aggregated status shows the workload Running with all
		// replicas ready on the surviving cell (reconciliation succeeded).
		st := hub.Status(workload)
		if !st.FullyScheduled {
			t.Fatalf("round %d: workload not fully scheduled after failover to %q: %+v",
				round, dec.Chosen, st)
		}
		if st.TotalReady != 3 {
			t.Fatalf("round %d: expected 3 ready replicas after failover, got %d", round, st.TotalReady)
		}
	}

	if len(seen) < 3 {
		t.Fatalf("failover did not exercise distinct cells: visited %v", seen)
	}

	// Kill the LAST hosting cell — now every cell is NotReady. The selector MUST
	// refuse to place (no phantom placement onto a dead cell).
	last := hub.HostingCell(workload)
	hub.KillCell(last)
	_, err = sel.Reselect(hub.Cells(), cons, last)
	if err == nil {
		t.Fatal("selector placed a workload with NO Ready cell remaining (false availability)")
	}
}

// TestChaosConcurrentKillAndSelectNoPanic hammers the hub with concurrent kills
// and selections to surface data races / deadlocks (run with -race). The
// invariant is liveness/safety: no panic, no deadlock, and every successful
// decision names a cell the hub still considers Ready at decision time is not
// asserted strictly (kills race selects), but a chosen cell must always be a
// real candidate name — never empty when at least one cell is Ready throughout.
func TestChaosConcurrentKillAndSelectNoPanic(t *testing.T) {
	hub := NewFakeHub([]Cell{
		{Name: "a", Region: "eu", Health: CellReady},
		{Name: "b", Region: "eu", Health: CellReady},
		{Name: "c", Region: "eu", Health: CellReady},
		{Name: "d", Region: "eu", Health: CellReady},
	})
	sel := NewSelector()
	cons := Constraints{PreferredRegion: "eu"}

	var wg sync.WaitGroup
	// Reader/selector goroutines.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				cells := hub.Cells() // concurrent read of mutable hub state
				dec, err := sel.Select(cells, cons)
				if err == nil && dec.Chosen == "" {
					t.Errorf("select returned nil error but empty Chosen")
					return
				}
			}
		}()
	}
	// One killer goroutine kills cells gradually (but never all, so a Ready cell
	// usually remains; the selectors must never panic regardless).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, name := range []string{"a", "b"} {
			hub.KillCell(name)
		}
	}()
	wg.Wait()
}
