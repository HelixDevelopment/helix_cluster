package federation

import (
	"sync"
	"testing"
)

// federation_adversarial2_test.go — DEEPER second-pass adversarial probes of the
// cross-cluster TRUST / admission boundary and the federated-state merge in
// internal/federation, focused on bugs the first adversarial pass missed.
//
// Probe classes:
//   (A) FAIL-OPEN ADMISSION — a cell whose liveness is NOT affirmatively Ready
//       (empty/unreported, or an unrecognized/hostile state string) must be
//       DENIED, not admitted by default. This is the load-bearing trust gate.
//   (B) FAILOVER CORRECTNESS — Reselect must never re-pick the down cell, and
//       must not regress the unknown-health denial.
//   (C) MERGE TOTAL ORDER — Aggregate folds per-cell status into a federation
//       view; the fold must be order-independent (a total order, not a
//       precedence that flips on input order).
//   (D) CONCURRENCY — concurrent cell add+kill+lookup is race-free and keeps the
//       placement/status accounting and the liveness gate consistent (-race).
//
// Anti-hang: bounded iteration counts, concurrency capped <= 8, no blocking ops.

// ---------------------------------------------------------------------------
// (A) FAIL-OPEN ADMISSION — the load-bearing trust gate. MUTATION-PROVING.
// ---------------------------------------------------------------------------

// TestAdversarial2_UnknownHealth_DeniedNotAdmitted is the sink-side proof of the
// admission contract: the selector docstring promises CellReady cells "can
// accept workloads" and CellNotReady "must not place". Any OTHER liveness value
// — an empty/unreported Health (""), or an unrecognized state a remote/hostile
// gateway might report ("Draining", "Unknown", "compromised", …) — is NOT an
// affirmative Ready and MUST be fail-closed (filtered, never Chosen).
//
// BUG (pre-fix): selectExcluding filtered ONLY `Health == CellNotReady`, so
// every non-Ready/non-NotReady cell was admitted by default — a default-allow
// trust gap. A federation could place a workload on a cell whose gateway was
// never confirmed reachable.
//
// This test FAILS on the unfixed selector (the cell is Chosen) and PASSES on the
// fixed one (the cell is filtered and Select returns ErrNoEligibleCell when it
// is the only candidate).
func TestAdversarial2_UnknownHealth_DeniedNotAdmitted(t *testing.T) {
	s := deterministicSelector()

	// Each of these is a non-affirmative liveness state. None may be Chosen.
	untrusted := []CellHealth{
		CellHealth(""),            // unreported / zero value
		CellHealth("Unknown"),     // explicit unknown
		CellHealth("Draining"),    // mid-lifecycle, not accepting work
		CellHealth("ready"),       // wrong case — not the Ready constant
		CellHealth("NotReadyy"),   // typo / near-miss
		CellHealth("compromised"), // hostile gateway state
	}

	for _, h := range untrusted {
		// Sole candidate: if admitted it WILL be Chosen with no error.
		cells := []Cell{
			{Name: "suspect", Region: "eu", LatencyMS: 1, CostPerHour: 0.1, Health: h},
		}
		d, err := s.Select(cells, Constraints{})
		if d.Chosen == "suspect" || err == nil {
			t.Fatalf("FAIL-OPEN ADMISSION: cell with non-Ready liveness %q was admitted "+
				"(chosen=%q err=%v); only an affirmative CellReady may be placed on", h, d.Chosen, err)
		}
		// And it must be recorded as filtered with a reason (explainable denial).
		if len(d.Ranked) != 1 || !d.Ranked[0].Filtered {
			t.Fatalf("liveness %q: expected the candidate filtered with a reason, ranked=%+v", h, d.Ranked)
		}
	}
}

// TestAdversarial2_UnknownHealth_NeverBeatsReady proves the gate sink-side in a
// MIXED field: an unknown-liveness cell that would score HIGHER (faster/cheaper)
// than the only Ready cell must still lose to the Ready cell — admission is a
// hard gate, not a soft penalty. FAILS pre-fix (the unknown cell wins on score).
func TestAdversarial2_UnknownHealth_NeverBeatsReady(t *testing.T) {
	s := deterministicSelector()
	cells := []Cell{
		// "fast" has unknown health but a strictly better latency/cost profile.
		{Name: "fast-unknown", Region: "eu", LatencyMS: 1, CostPerHour: 0.01, Health: CellHealth("")},
		// "ok" is the genuinely-Ready, slower cell.
		{Name: "ok-ready", Region: "eu", LatencyMS: 50, CostPerHour: 2.0, Health: CellReady},
	}
	for _, order := range [][]Cell{{cells[0], cells[1]}, {cells[1], cells[0]}} {
		d, err := s.Select(order, Constraints{Weights: Weights{Latency: 1.0, Cost: 1.0}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.Chosen != "ok-ready" {
			t.Fatalf("admission gate bypassed by score: chose %q want ok-ready (unknown-health cell must not win)", d.Chosen)
		}
	}
}

// ---------------------------------------------------------------------------
// (B) FAILOVER CORRECTNESS — Reselect honors the same fail-closed gate and never
//     re-picks the down cell. Passes on fixed code; pins the failover path.
// ---------------------------------------------------------------------------

// TestAdversarial2_Reselect_UnknownSurvivorDenied: during failover the only
// "surviving" candidate has unknown liveness. The down cell is excluded by name;
// the survivor must NOT be admitted just because it is not literally NotReady.
// Failover with no affirmatively-Ready survivor must return ErrNoEligibleCell
// (no phantom placement onto an unconfirmed cell). FAILS pre-fix.
func TestAdversarial2_Reselect_UnknownSurvivorDenied(t *testing.T) {
	s := deterministicSelector()
	cells := []Cell{
		{Name: "host", Region: "eu", LatencyMS: 5, CostPerHour: 1.0, Health: CellNotReady},
		{Name: "survivor", Region: "eu", LatencyMS: 5, CostPerHour: 1.0, Health: CellHealth("")},
	}
	d, err := s.Reselect(cells, Constraints{}, "host")
	if err == nil {
		t.Fatalf("failover admitted an unknown-liveness survivor %q (no affirmatively Ready cell remained)", d.Chosen)
	}
	if d.Chosen != "" {
		t.Fatalf("failover with no Ready survivor must choose nothing, chose %q", d.Chosen)
	}
}

// TestAdversarial2_Reselect_NeverRepicksDownCell pins that the excluded (down)
// cell is never Chosen even when it is the highest-scoring and still flagged
// Ready in the (stale) snapshot the caller passes. Passes on prod; guards the
// name-exclusion gate against regression.
func TestAdversarial2_Reselect_NeverRepicksDownCell(t *testing.T) {
	s := deterministicSelector()
	// down cell is the BEST on score and still Ready in the snapshot (stale).
	cells := []Cell{
		{Name: "down", Region: "eu", LatencyMS: 1, CostPerHour: 0.1, Health: CellReady},
		{Name: "alive", Region: "eu", LatencyMS: 40, CostPerHour: 3.0, Health: CellReady},
	}
	d, err := s.Reselect(cells, Constraints{Weights: Weights{Latency: 1.0, Cost: 1.0}}, "down")
	if err != nil {
		t.Fatalf("reselect: %v", err)
	}
	if d.Chosen == "down" {
		t.Fatalf("failover re-picked the down cell despite exclusion (chose %q)", d.Chosen)
	}
	if d.Chosen != "alive" {
		t.Fatalf("failover should land on the surviving cell, chose %q", d.Chosen)
	}
	if d.PreviousCell != "down" || !d.FailedOver {
		t.Fatalf("failover bookkeeping wrong: %+v", d)
	}
}

// ---------------------------------------------------------------------------
// (C) MERGE TOTAL ORDER — Aggregate must be a permutation-invariant fold.
// ---------------------------------------------------------------------------

// TestAdversarial2_Aggregate_OrderIndependentMerge feeds the SAME set of per-cell
// statuses in many permutations and asserts the federation-wide summary is
// identical — the merge is a total order over the set, not a precedence that
// flips with arrival order (which would let a stale per-cell report mask a
// fresher one depending on ingestion order). Passes on prod; pins the contract.
func TestAdversarial2_Aggregate_OrderIndependentMerge(t *testing.T) {
	base := []WorkStatus{
		{Cluster: "cell-a", Healthy: true, ReadyReplicas: 2, DesiredReplicas: 3},
		{Cluster: "cell-b", Healthy: true, ReadyReplicas: 3, DesiredReplicas: 3},
		{Cluster: "cell-c", Healthy: false, ReadyReplicas: 0, DesiredReplicas: 1},
	}
	perms := [][]int{
		{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0},
	}
	var want *AggregatedStatus
	for _, p := range perms {
		in := []WorkStatus{base[p[0]], base[p[1]], base[p[2]]}
		got := Aggregate("svc", in)
		if want == nil {
			g := got
			want = &g
			continue
		}
		if got.TotalReady != want.TotalReady ||
			got.TotalDesired != want.TotalDesired ||
			got.ReadyClusters != want.ReadyClusters ||
			got.FullyScheduled != want.FullyScheduled ||
			len(got.PerCluster) != len(want.PerCluster) {
			t.Fatalf("merge not order-independent: perm %v -> %+v, want %+v", p, got, *want)
		}
		// PerCluster must be sorted identically regardless of input order.
		for i := range got.PerCluster {
			if got.PerCluster[i].Cluster != want.PerCluster[i].Cluster {
				t.Fatalf("merge PerCluster order differs for perm %v: %+v vs %+v",
					p, got.PerCluster, want.PerCluster)
			}
		}
	}
	// Sink-side sanity: totals are the true sums, FullyScheduled is false (5/7).
	if want.TotalReady != 5 || want.TotalDesired != 7 || want.FullyScheduled {
		t.Fatalf("aggregate sums wrong: %+v", *want)
	}
}

// ---------------------------------------------------------------------------
// (D) CONCURRENCY — concurrent cell add(Place)/kill/lookup, conserved accounting
//     and a fail-closed selection over the live snapshot (run under -race).
// ---------------------------------------------------------------------------

// TestAdversarial2_ConcurrentPlaceKillSelect_NoLostAdmission hammers the hub with
// concurrent placements and kills while a selector goroutine repeatedly selects
// over the live Cells() snapshot. SINK-SIDE: every non-error selection names a
// cell that the SAME snapshot reported affirmatively Ready — a killed/unknown
// cell is never silently admitted under concurrency (no lost-update to the
// liveness gate). Capped goroutines; bounded iterations; no blocking ops.
func TestAdversarial2_ConcurrentPlaceKillSelect_NoLostAdmission(t *testing.T) {
	hub := NewFakeHub([]Cell{
		{Name: "a", Region: "eu", LatencyMS: 5, CostPerHour: 1, Health: CellReady},
		{Name: "b", Region: "eu", LatencyMS: 6, CostPerHour: 1, Health: CellReady},
		{Name: "c", Region: "eu", LatencyMS: 7, CostPerHour: 1, Health: CellReady},
	})
	sel := NewSelector()
	cons := Constraints{PreferredRegion: "eu"}
	names := []string{"a", "b", "c"}

	const workers = 6
	const iters = 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				switch (w + i) % 3 {
				case 0:
					hub.Place("wl", names[(w+i)%len(names)], 2)
				case 1:
					// Take a single snapshot and decide over THAT snapshot, then
					// verify the choice was Ready in the same snapshot — atomic
					// gate check, immune to a concurrent kill landing after.
					snap := hub.Cells()
					d, err := sel.Select(snap, cons)
					if err != nil {
						continue
					}
					if d.Chosen == "" {
						t.Errorf("nil error but empty Chosen")
						return
					}
					ready := false
					for _, c := range snap {
						if c.Name == d.Chosen {
							ready = c.Health == CellReady
						}
					}
					if !ready {
						t.Errorf("ADMITTED non-Ready cell %q under concurrency", d.Chosen)
						return
					}
				case 2:
					hub.KillCell(names[(w+i)%len(names)])
				}
			}
		}(w)
	}
	wg.Wait()

	// Settle: a fresh hub (kills are sticky) and assert a clean placement still
	// conserves accounting.
	fresh := NewFakeHub([]Cell{{Name: "a", Region: "eu", LatencyMS: 5, Health: CellReady}})
	fresh.Place("wl", "a", 2)
	st := fresh.Status("wl")
	if st.TotalDesired != 2 || st.TotalReady != 2 || !st.FullyScheduled {
		t.Fatalf("post-storm clean placement accounting wrong: %+v", st)
	}
}
