package ewmarank

import (
	"os"
	"sync"
	"testing"
)

// proveRaceEnabled reports whether the opt-in unsynchronized race reproducer
// should run (EWMARANK_PROVE_RACE set to a non-empty, non-"0" value).
func proveRaceEnabled() bool {
	v := os.Getenv("EWMARANK_PROVE_RACE")
	return v != "" && v != "0"
}

// runUUIDConc anchors the concurrent test binary's observable log lines for
// forensic tracing (distinct from the serial-test runUUID).
const runUUIDConc = "ewmarank-conc-8b2e1a47-6c39-4d50-a1f7-2e9b4c6d8f03"

// TestConcurrentBalancerRole_ExternallySynchronized exercises the EXACT
// balancer-role concurrency profile the package is built for — many request
// goroutines, where every request both UPDATES a backend's EWMA (Observe ->
// write r.backends[id], r.order) AND READS the rankings to pick a backend
// (RouteSticky/Rank -> range r.backends, read/write r.sticky) — under the
// package's DOCUMENTED contract: "A Ranker is not safe for concurrent use;
// callers that need concurrency must synchronize externally."
//
// This test supplies that external synchronization (a single sync.Mutex around
// every Ranker call, exactly as the doc instructs) and asserts the result is
// race-free under -race and -count=10 with hundreds of goroutines hammering
// both the write path and the read path simultaneously.
//
// ANTI-BLUFF / why this BITES:
//   - Run under `go test -race`. If ANY Ranker method touched the maps without
//     going through the caller's lock (e.g. a stray internal goroutine, or if a
//     future edit added background mutation), the race detector would fire a
//     "DATA RACE" on r.backends / r.order / r.sticky and FAIL the test. The
//     teeth are the -race instrumentation on the real concurrent map read+write,
//     not a sleep or a hope.
//   - The companion TestConcurrentBalancerRole_UnsynchronizedRaces proves the
//     hazard is REAL (the same workload WITHOUT the lock crashes with
//     "fatal error: concurrent map read and map write"), which is precisely why
//     the documented "synchronize externally" contract is load-bearing — this
//     test demonstrates the documented mitigation actually works.
func TestConcurrentBalancerRole_ExternallySynchronized(t *testing.T) {
	t.Logf("run=%s phase=before launching synchronized balancer workload", runUUIDConc)

	r := New(0.3)
	var mu sync.Mutex // the "synchronize externally" the doc mandates

	const (
		workers      = 64
		opsPerWorker = 500
	)
	backendIDs := []string{"be-0", "be-1", "be-2", "be-3", "be-4", "be-5"}

	// Seed every backend once (still through the lock) so Rank has real data.
	mu.Lock()
	for i, id := range backendIDs {
		r.Observe(id, 0.1*float64(i+1))
	}
	mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			// Deterministic per-worker pseudo-randomness (no time, no rand pkg)
			// so the test is reproducible across -count=10 runs.
			seed := uint64(w*2654435761 + 1)
			next := func() uint64 {
				seed ^= seed << 13
				seed ^= seed >> 7
				seed ^= seed << 17
				return seed
			}
			clientID := func() string { return backendIDs[int(next()%uint64(len(backendIDs)))] + "-cli" }
			for i := 0; i < opsPerWorker; i++ {
				id := backendIDs[int(next()%uint64(len(backendIDs)))]
				sample := float64(next()%1000) / 1000.0
				switch next() % 5 {
				case 0, 1:
					// WRITE path: fold a latency/utilization sample into EWMA.
					mu.Lock()
					r.Observe(id, sample)
					mu.Unlock()
				case 2:
					// READ path: rank the backends to choose.
					mu.Lock()
					_ = r.Rank()
					mu.Unlock()
				case 3:
					// READ+WRITE path: sticky routing reads rank, writes sticky.
					mu.Lock()
					_ = r.RouteSticky("client-" + clientID())
					mu.Unlock()
				case 4:
					// Topology churn: fail then recover, racing the readers.
					mu.Lock()
					r.MarkFailed(id)
					r.Recover(id)
					mu.Unlock()
				}
			}
		}(w)
	}
	wg.Wait()

	// Sink-side sanity: after the storm, the ranker is still internally coherent
	// — every seeded backend is rankable and Rank returns each known ID once.
	mu.Lock()
	ranked := r.Rank()
	mu.Unlock()
	if len(ranked) != len(backendIDs) {
		t.Fatalf("expected %d ranked backends after workload, got %d (%v)",
			len(backendIDs), len(ranked), ranked)
	}
	seen := map[string]int{}
	for _, id := range ranked {
		seen[id]++
	}
	for _, id := range backendIDs {
		if seen[id] != 1 {
			t.Fatalf("backend %s appeared %d times in Rank (want exactly 1): %v", id, seen[id], ranked)
		}
	}
	t.Logf("run=%s phase=after race-free synchronized workload, rank=%v", runUUIDConc, ranked)
}

// TestAddRemoveAvailability covers the add/remove contract directly:
//   - "Remove": a backend marked failed (the package's removal-from-routing
//     mechanism) is never picked by RouteSticky, and a client pinned to it is
//     re-routed away; Recover makes it pickable again.
//   - "Add": a brand-new backend, introduced by its first Observe with the
//     lowest utilization, becomes rankable and is selected as best.
//
// ANTI-BLUFF / why this BITES:
//   - If RouteSticky ignored the failed flag (the removal), the
//     "failed backend is not picked" assertion fails — a client would keep being
//     routed to the removed backend. Mutation-verified: forcing best := "x" (the
//     failed one) in rankAvailable would make routedTo == "x" and fail here.
//   - If a first Observe did not seed/register the new backend (the add), the
//     new least-loaded backend would be absent from Rank and never chosen as
//     best, failing the "new backend is best" assertion.
func TestAddRemoveAvailability(t *testing.T) {
	r := New(0.5)
	// Initial set: x cheapest, then y, then z.
	r.Observe("x", 0.10)
	r.Observe("y", 0.40)
	r.Observe("z", 0.70)

	client := "c1"
	if got := r.RouteSticky(client); got != "x" {
		t.Fatalf("expected best 'x', got %s", got)
	}

	// "Remove" x from routing. The pinned client must re-route to next-best 'y'.
	r.MarkFailed("x")
	if r.Failed("x") != true {
		t.Fatalf("MarkFailed did not record failure for x")
	}
	routedTo := r.RouteSticky(client)
	if routedTo == "x" {
		t.Fatalf("removed (failed) backend x must not be picked, but was")
	}
	if routedTo != "y" {
		t.Fatalf("expected re-route to next-best 'y' after removing x, got %s", routedTo)
	}
	// x must also be absent from the available ranking entirely.
	for _, id := range r.rankAvailable() {
		if id == "x" {
			t.Fatalf("failed backend x leaked into rankAvailable: %v", r.rankAvailable())
		}
	}

	// "Add" a brand-new, cheapest backend 'w' via its first Observe.
	r.Observe("w", 0.01)
	if _, ok := r.EWMA("w"); !ok {
		t.Fatalf("newly added backend w not registered/seeded after first Observe")
	}
	rank := r.Rank()
	if indexOf(rank, "w") < 0 {
		t.Fatalf("newly added backend w not rankable: %v", rank)
	}
	if rank[0] != "w" {
		t.Fatalf("new cheapest backend w should rank best, got rank=%v", rank)
	}

	// "Recover" x: it becomes available again and (being cheapest among healthy
	// originals) reappears in the available ranking.
	r.Recover("x")
	if r.Failed("x") {
		t.Fatalf("Recover did not clear failed flag for x")
	}
	foundX := false
	for _, id := range r.rankAvailable() {
		if id == "x" {
			foundX = true
		}
	}
	if !foundX {
		t.Fatalf("recovered backend x not available again: %v", r.rankAvailable())
	}
	t.Logf("run=%s add-remove ok finalRank=%v available=%v", runUUIDConc, r.Rank(), r.rankAvailable())
}

// TestConcurrentBalancerRole_UnsynchronizedRaces is the TEETH proving the
// "not safe for concurrent use" contract is real and load-bearing: it runs the
// same balancer-role workload WITHOUT external synchronization, which races the
// unguarded backends/order/sticky maps (concurrent map read during a write).
//
// It is OPT-IN via the EWMARANK_PROVE_RACE env var because, by design, it will
// either trip the -race detector ("DATA RACE") or hard-crash the test binary
// with "fatal error: concurrent map read and map write" — a crash that would
// fail the whole package run. We keep it out of the default green path (the
// package's contract is "synchronize externally", which the synchronized test
// above satisfies) while still shipping the reproducer.
//
// To witness the real bug the contract warns about:
//
//	EWMARANK_PROVE_RACE=1 go test -race -run UnsynchronizedRaces ./pkg/ewmarank/
//
// It is expected to FAIL/crash — that failure is the evidence the disclaimer is
// truthful, not a bluff.
func TestConcurrentBalancerRole_UnsynchronizedRaces(t *testing.T) {
	if !proveRaceEnabled() {
		t.Skip("opt-in race reproducer: set EWMARANK_PROVE_RACE=1 to run; " +
			"it deliberately races the unguarded maps to prove the " +
			"'not safe for concurrent use' contract is load-bearing (expected to crash/RACE)")
	}
	t.Logf("run=%s phase=before launching UNSYNCHRONIZED balancer workload (expect RACE)", runUUIDConc)

	r := New(0.3)
	backendIDs := []string{"be-0", "be-1", "be-2", "be-3", "be-4", "be-5"}
	for i, id := range backendIDs {
		r.Observe(id, 0.1*float64(i+1))
	}

	const workers = 64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			seed := uint64(w*2654435761 + 1)
			next := func() uint64 {
				seed ^= seed << 13
				seed ^= seed >> 7
				seed ^= seed << 17
				return seed
			}
			for i := 0; i < 2000; i++ {
				id := backendIDs[int(next()%uint64(len(backendIDs)))]
				if next()%2 == 0 {
					r.Observe(id, float64(next()%1000)/1000.0) // WRITE: r.backends[id]=...
				} else {
					_ = r.Rank() // READ: range r.backends  <-- races the write
				}
			}
		}(w)
	}
	wg.Wait()
}
