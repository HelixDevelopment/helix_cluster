package federation

import (
	"math"
	"sync"
	"testing"
)

// federation_adversarial_test.go — adversarial, SINK-SIDE probes of the global
// cell-selection / status-aggregation boundary in internal/federation.
//
// Probe classes (per the federation total-order / trust threat model):
//   (a) TOTAL ORDER at a replica/identity edge — equal-score resolution must be
//       deterministic and reproducible, as the selector docstring promises
//       ("ties broken deterministically by cell name so the decision is
//       reproducible").
//   (b) UNGUARDED SHARED STATE — concurrent add/kill/query on the FakeHub must
//       be race-free with conserved invariants (run under -race).
//   (c) FAIL-OPEN TRUST — a cell carrying a malformed (uncomputable) score must
//       NOT be admitted as the winner, and must not depend on input order.
//   (d) NUMERIC — NaN/±Inf in a measured field (LatencyMS/CostPerHour) must not
//       break the score threshold / total order.
//
// Anti-hang: concurrency capped <= 8; bounded iteration counts.

// ---------------------------------------------------------------------------
// (a) TOTAL ORDER — duplicate identity / equal-score determinism (CONTRACT PIN)
// ---------------------------------------------------------------------------

// TestAdversarial_DupNameEqualScore_Deterministic pins the documented contract:
// with a FIXED candidate order, two distinct cells that share a Name and tie on
// score resolve to a STABLE winner across many runs. This passes on prod and
// guards against a future regression that would make equal-name resolution
// order-sensitive (sort.SliceStable currently preserves input order on a full
// tie, which is deterministic *given* a deterministic input order).
func TestAdversarial_DupNameEqualScore_Deterministic(t *testing.T) {
	s := deterministicSelector()
	first := ""
	for i := 0; i < 300; i++ {
		cells := []Cell{
			{Name: "dup", Region: "eu", LatencyMS: 10, CostPerHour: 1.0, Health: CellReady},
			{Name: "dup", Region: "eu", LatencyMS: 99, CostPerHour: 1.0, Health: CellReady},
			{Name: "zzz", Region: "us", LatencyMS: 5, CostPerHour: 1.0, Health: CellReady},
		}
		d, err := s.Select(cells, Constraints{}) // no prefs: all dataLocality=full
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		// Encode the *physical* winner (name + a distinguishing field).
		key := d.Ranked[0].Cell.Name
		if d.Ranked[0].Cell.Name == "dup" {
			if d.Ranked[0].Cell.LatencyMS == 10 {
				key = "dup@10"
			} else {
				key = "dup@99"
			}
		}
		if first == "" {
			first = key
		} else if key != first {
			t.Fatalf("iter %d: equal-score winner is NONDETERMINISTIC: got %q, first %q", i, key, first)
		}
	}
	t.Logf("equal-score winner stable across 300 runs: %q", first)
}

// ---------------------------------------------------------------------------
// (a)+(c)+(d) NaN SCORE breaks the total order AND admits a malformed cell.
// ---------------------------------------------------------------------------
//
// A measured field (LatencyMS / CostPerHour) carrying NaN — exactly the kind of
// malformed telemetry a federation layer ingests from a remote, possibly
// hostile, cell gateway — produces a NaN score. NaN comparisons are
// non-transitive, so sort.SliceStable yields an order-DEPENDENT winner. The
// selector docstring promises the decision is "reproducible"; a NaN score
// violates that, and worse, the malformed cell is sometimes ADMITTED as Chosen.
//
// SINK-SIDE assertions:
//   - the Chosen cell is identical regardless of input order (reproducible), AND
//   - a cell whose score cannot be computed (NaN) is NEVER admitted as Chosen.
//
// This test FAILS on unfixed prod (the bug). It is intentionally NOT env-gated.
func TestAdversarial_NaNScore_BreaksTotalOrder_AdmitsMalformed(t *testing.T) {
	s := deterministicSelector()
	nan := math.NaN()

	good := Cell{Name: "good", Region: "eu", LatencyMS: 10, CostPerHour: 1.0, Health: CellReady}
	mid := Cell{Name: "mid", Region: "eu", LatencyMS: 20, CostPerHour: 1.0, Health: CellReady}
	// "bad" reports an unmeasurable (NaN) latency: malformed/hostile telemetry.
	bad := Cell{Name: "bad", Region: "eu", LatencyMS: nan, CostPerHour: 1.0, Health: CellReady}

	orders := [][]Cell{
		{good, mid, bad},
		{good, bad, mid},
		{mid, good, bad},
		{mid, bad, good},
		{bad, good, mid},
		{bad, mid, good},
	}

	chosen := map[string]int{}
	admittedBad := 0
	for _, o := range orders {
		// Latency weight 1.0 so the NaN latency dominates the score.
		d, err := s.Select(o, Constraints{Weights: Weights{Latency: 1.0}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		chosen[d.Chosen]++
		if d.Chosen == "bad" {
			admittedBad++
		}
	}

	// SINK-SIDE 1: reproducibility — the winner must be the same for every order.
	if len(chosen) != 1 {
		t.Fatalf("NaN score breaks total order: Chosen depends on input ordering: %v "+
			"(selector contract promises a reproducible decision)", chosen)
	}
	// SINK-SIDE 2: a malformed (NaN-score) cell must never be admitted.
	if admittedBad != 0 {
		t.Fatalf("FAIL-OPEN: malformed cell 'bad' (NaN latency) admitted as Chosen in %d/%d orderings",
			admittedBad, len(orders))
	}
}

// TestAdversarial_NaNScore_FilteredNotChosen asserts the direct sink: a single
// NaN-score candidate alongside a healthy one must never win, in EITHER input
// order. Fails on unfixed prod when the NaN cell is listed first.
func TestAdversarial_NaNScore_FilteredNotChosen(t *testing.T) {
	s := deterministicSelector()
	nan := math.NaN()
	good := Cell{Name: "good", Region: "eu", LatencyMS: 10, CostPerHour: 1.0, Health: CellReady}
	bad := Cell{Name: "bad", Region: "eu", LatencyMS: nan, CostPerHour: 1.0, Health: CellReady}

	for _, order := range [][]Cell{{good, bad}, {bad, good}} {
		d, err := s.Select(order, Constraints{Weights: Weights{Latency: 1.0}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.Chosen == "bad" {
			t.Fatalf("NaN-score cell admitted as Chosen with input order %v->%v",
				order[0].Name, order[1].Name)
		}
	}
}

// ---------------------------------------------------------------------------
// (d) NUMERIC — Inf handling + negative-latency latent note.
// ---------------------------------------------------------------------------

// TestAdversarial_InfLatency_DeterministicLoser documents that +Inf latency is
// handled gracefully (score -> 0, never chosen over a finite cell). This passes
// on prod and pins the well-behaved Inf path.
func TestAdversarial_InfLatency_DeterministicLoser(t *testing.T) {
	s := deterministicSelector()
	cells := []Cell{
		{Name: "good", Region: "eu", LatencyMS: 10, CostPerHour: 1.0, Health: CellReady},
		{Name: "inf", Region: "eu", LatencyMS: math.Inf(1), CostPerHour: 1.0, Health: CellReady},
	}
	d, err := s.Select(cells, Constraints{Weights: Weights{Latency: 1.0}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Chosen != "good" {
		t.Fatalf("+Inf latency cell should never beat a finite cell, chose %q", d.Chosen)
	}
}

// TestAdversarial_NegativeLatencyScoresPerfect_LatentNote is a NON-failing
// documentation probe of a LATENT RISK: the `LatencyMS <= 0` guard treats ANY
// non-positive latency (including a nonsensical negative / -Inf value) as the
// ideal 0ms cell, scoring it a perfect 1.0. A cell reporting garbage negative
// latency thus wins over a legitimately fast cell. This is arguably intended
// (treat <=0 as "ideal"), so it is NOT asserted as a failure — only logged so
// the behavior is on record.
func TestAdversarial_NegativeLatencyScoresPerfect_LatentNote(t *testing.T) {
	s := deterministicSelector()
	cells := []Cell{
		{Name: "real", Region: "eu", LatencyMS: 10, CostPerHour: 1.0, Health: CellReady},
		{Name: "garbage", Region: "eu", LatencyMS: -1000, CostPerHour: 1.0, Health: CellReady},
	}
	d, err := s.Select(cells, Constraints{Weights: Weights{Latency: 1.0}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LATENT: negative-latency cell %q scored as ideal and chosen=%q "+
		"(<=0 latency collapses to perfect 1.0)", "garbage", d.Chosen)
}

// ---------------------------------------------------------------------------
// (b) UNGUARDED SHARED STATE — concurrent FakeHub ops, conservation (run -race)
// ---------------------------------------------------------------------------

// TestAdversarial_ConcurrentHub_RaceAndConservation hammers the FakeHub with
// concurrent placements, kills, status reads and Cells() snapshots under capped
// goroutines. Under -race this surfaces any data race on the cell/placement/
// status maps. The conservation invariant: after the storm, for every workload
// the aggregated DesiredReplicas equals the per-cell desired sum reported by
// Status (no torn/lost-update accounting), and a Status read never observes a
// torn WorkStatus (Ready in (0..Desired]).
func TestAdversarial_ConcurrentHub_RaceAndConservation(t *testing.T) {
	hub := NewFakeHub([]Cell{
		{Name: "a", Region: "eu", LatencyMS: 5, Health: CellReady},
		{Name: "b", Region: "eu", LatencyMS: 6, Health: CellReady},
		{Name: "c", Region: "eu", LatencyMS: 7, Health: CellReady},
		{Name: "d", Region: "eu", LatencyMS: 8, Health: CellReady},
	})

	const workers = 8 // capped
	const iters = 250
	cellNames := []string{"a", "b", "c", "d"}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				wl := "wl" // single shared workload exercises the placement map hard
				switch (w + i) % 4 {
				case 0:
					hub.Place(wl, cellNames[(w+i)%len(cellNames)], 3)
				case 1:
					st := hub.Status(wl)
					// SINK-SIDE conservation: never observe a torn status where
					// ready exceeds desired on any cell.
					for _, ws := range st.PerCluster {
						if ws.ReadyReplicas > ws.DesiredReplicas {
							t.Errorf("torn WorkStatus: ready=%d > desired=%d on %q",
								ws.ReadyReplicas, ws.DesiredReplicas, ws.Cluster)
							return
						}
					}
				case 2:
					_ = hub.Cells() // concurrent snapshot read
				case 3:
					hub.KillCell(cellNames[(w+i)%len(cellNames)])
				}
			}
		}(w)
	}
	wg.Wait()

	// Final settle: place deterministically and assert conservation sink-side.
	hub.Place("wl", "a", 3)
	st := hub.Status("wl")
	if st.TotalDesired != 3 {
		t.Fatalf("conservation: after final single placement TotalDesired=%d want 3 (stale per-cell desired leaked): %+v",
			st.TotalDesired, st.PerCluster)
	}
	if got := hub.HostingCell("wl"); got != "a" {
		t.Fatalf("hosting cell after final place = %q want a", got)
	}
}
