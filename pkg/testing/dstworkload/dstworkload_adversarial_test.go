package dstworkload_test

// Adversarial determinism + correctness suite for pkg/testing/dstworkload.
//
// PRIMARY HYPOTHESIS (DETERMINISM BLUFF): the sibling pkg/testing/dst was found
// to claim "fully deterministic/reproducible" runs while leaking nondeterminism
// by ranging a Go map (randomized order) when building ordered output. This
// suite tests whether dstworkload — a deterministic-simulation WORKLOAD
// generator that schedules a task storm under seeded chaos — makes the same
// mistake. It does NOT: ListNodes() sorts node IDs (engine.go), the chaos
// catalog is an ordered slice (faults_extra.go FaultCatalog), and tasks are
// scheduled at strictly-distinct virtual timestamps. The suite below PINS that
// contract: same seed across 40+ runs => BYTE-IDENTICAL generated workload, and
// the seed (not wall-clock / map order / goroutine scheduling) is the only
// driver of the virtual-clock-derived output.
//
// VERDICT for this package: NO_BUG — determinism contract is real and is pinned
// here. Production code is left BYTE-IDENTICAL.

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dst"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dstworkload"
)

// withTimeout guards every test body against a hang (an engine that fails to
// drain its event queue would otherwise block forever). It runs fn in a
// goroutine and fails the test if fn does not complete within d.
func withTimeout(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("test body did not finish within %v (possible hang/deadlock)", d)
	}
}

// runWorkload runs Setup+Execution for a seed and returns the workload, failing
// the test on a Setup error.
func runWorkload(t *testing.T, seed int64) *dstworkload.FoundationDBWorkload {
	t.Helper()
	eng := dst.NewEngine(seed)
	w := dstworkload.NewWorkload(eng)
	if err := w.Setup(); err != nil {
		t.Fatalf("seed=%d Setup: %v", seed, err)
	}
	w.Execution()
	return w
}

// fingerprint produces a stable, byte-comparable serialization of everything in
// a finished workload that is DERIVED FROM THE SEED via the virtual clock:
//   - the full task->node assignment map (sorted by task ID),
//   - the sorted set of offline nodes after chaos,
//   - the sorted latency multiset (virtual clock),
//   - the P99 latency and assigned count.
//
// It deliberately EXCLUDES ThroughputPerSec, which the production code documents
// as wall-clock derived (workload.go: "real (wall-clock) elapsed time") and is
// therefore intentionally non-reproducible.
func fingerprint(t *testing.T, w *dstworkload.FoundationDBWorkload) string {
	t.Helper()
	var b strings.Builder

	// Assignment map, sorted by task ID — this is the core ordered output that a
	// map-range bug would scramble.
	asg := w.AssignmentsSnapshot()
	keys := make([]string, 0, len(asg))
	for k := range asg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString("ASSIGN\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s=%s\n", k, asg[k])
	}

	// Offline node set (sorted) — proves chaos mutated sim state deterministically.
	var offline []string
	for _, n := range w.Eng.ListNodes() {
		if !n.Online {
			offline = append(offline, n.ID)
		}
	}
	sort.Strings(offline)
	fmt.Fprintf(&b, "OFFLINE %v\n", offline)

	// Latencies (virtual clock). Already in event-processing order; sort to be
	// robust, then record. A wall-clock leak here would jitter these values.
	lats := w.LatenciesSnapshot()
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	b.WriteString("LAT")
	for _, l := range lats {
		fmt.Fprintf(&b, " %d", int64(l))
	}
	b.WriteString("\n")

	m := w.Metrics()
	fmt.Fprintf(&b, "ASSIGNED %d\nP99 %d\n", m.TasksAssigned, int64(m.LatencyP99))
	return b.String()
}

// TestDeterminism_SameSeed_ByteIdentical is the headline test: the same seed
// across 40+ independent runs must produce a BYTE-IDENTICAL generated workload
// (assignment map + offline set + virtual-clock latencies + P99). This is the
// exact class of bug found in the sibling dst package; here it must NOT
// reproduce.
func TestDeterminism_SameSeed_ByteIdentical(t *testing.T) {
	withTimeout(t, 30*time.Second, func() {
		const seed = int64(0)
		const runs = 48

		base := fingerprint(t, runWorkload(t, seed))
		for i := 1; i < runs; i++ {
			got := fingerprint(t, runWorkload(t, seed))
			if got != base {
				t.Fatalf("non-deterministic workload on run %d/%d for seed=%d\n--- base ---\n%s\n--- got ---\n%s",
					i, runs, seed, base, got)
			}
		}
	})
}

// TestDeterminism_MultipleSeeds_EachReproducible checks the contract across a
// spread of seeds: each seed is independently reproducible (10 runs each).
func TestDeterminism_MultipleSeeds_EachReproducible(t *testing.T) {
	withTimeout(t, 30*time.Second, func() {
		for _, seed := range []int64{0, 1, 2, 3, 7, 42, 99, 12345, -1, 1 << 40} {
			base := fingerprint(t, runWorkload(t, seed))
			for i := 0; i < 10; i++ {
				got := fingerprint(t, runWorkload(t, seed))
				if got != base {
					t.Fatalf("seed=%d not reproducible on run %d:\n--- base ---\n%s\n--- got ---\n%s",
						seed, i, base, got)
				}
			}
		}
	})
}

// TestDeterminism_DifferentSeeds_Differ proves the seed actually DRIVES the
// generated workload: at least one pair of seeds must produce a different
// fingerprint. (If every seed produced the identical workload, the "seeded"
// claim would be a no-op even though it would still be "reproducible".)
//
// The discriminating signal is the offline-node set: seed 0 crashes node-4,
// seed 99 crashes node-1, most other seeds crash none. We assert that not all
// fingerprints collapse to one value.
func TestDeterminism_DifferentSeeds_Differ(t *testing.T) {
	withTimeout(t, 30*time.Second, func() {
		seeds := []int64{0, 1, 2, 3, 7, 42, 99, 12345}
		distinct := map[string]struct{}{}
		for _, s := range seeds {
			distinct[fingerprint(t, runWorkload(t, s))] = struct{}{}
		}
		if len(distinct) < 2 {
			t.Fatalf("all %d seeds produced identical workloads (%d distinct) — seed is not actually driving generation",
				len(seeds), len(distinct))
		}
	})
}

// TestDeterminism_VirtualClock_NotWallClock asserts that the seed-derived,
// virtual-clock outputs do not depend on wall time. We run the same seed,
// sleep, run again, and require byte-identical latencies and P99. A wall-clock
// leak into the generated workload would make these diverge across the sleep.
func TestDeterminism_VirtualClock_NotWallClock(t *testing.T) {
	withTimeout(t, 30*time.Second, func() {
		const seed = int64(7)
		latString := func(w *dstworkload.FoundationDBWorkload) string {
			lats := w.LatenciesSnapshot()
			var b strings.Builder
			for _, l := range lats {
				fmt.Fprintf(&b, "%d ", int64(l))
			}
			fmt.Fprintf(&b, "| p99=%d", int64(w.Metrics().LatencyP99))
			return b.String()
		}
		a := latString(runWorkload(t, seed))
		time.Sleep(15 * time.Millisecond)
		b := latString(runWorkload(t, seed))
		if a != b {
			t.Fatalf("virtual-clock workload changed across a wall-clock sleep (wall-clock leak):\n a=%q\n b=%q", a, b)
		}
		// Latencies are the strictly-monotone virtual timestamps 1µs..20µs.
		lats := runWorkload(t, seed).LatenciesSnapshot()
		if len(lats) != 20 {
			t.Fatalf("expected 20 virtual-clock latencies, got %d", len(lats))
		}
		for i, l := range lats {
			want := time.Duration(int64(i+1) * 1000) // (i+1) µs in ns
			if l != want {
				t.Fatalf("latency[%d]=%v, want %v (virtual clock should be 1µs-spaced)", i, l, want)
			}
		}
	})
}

// TestDeterminism_RaceClean exercises the same seed from multiple goroutines on
// independent engines; combined with -race this proves the generation path
// shares no mutable global state. Each goroutine builds its own engine/workload,
// so results must match the single-threaded fingerprint.
func TestDeterminism_RaceClean(t *testing.T) {
	withTimeout(t, 30*time.Second, func() {
		const seed = int64(3)
		want := fingerprint(t, runWorkload(t, seed))
		const g = 8
		results := make(chan string, g)
		for i := 0; i < g; i++ {
			go func() {
				eng := dst.NewEngine(seed)
				w := dstworkload.NewWorkload(eng)
				if err := w.Setup(); err != nil {
					results <- "setup-error:" + err.Error()
					return
				}
				w.Execution()
				results <- fingerprint(t, w)
			}()
		}
		for i := 0; i < g; i++ {
			if got := <-results; got != want {
				t.Fatalf("goroutine %d produced divergent workload:\n--- want ---\n%s\n--- got ---\n%s", i, want, got)
			}
		}
	})
}

// --- Correctness edges -----------------------------------------------------

// TestCorrectness_AllTasksAssigned_AndInvariantsPass confirms the generated
// workload is internally consistent: every scheduled task is assigned (to the
// lowest-ID online node), Check() passes, and the count is the full storm.
func TestCorrectness_FullRunInvariants(t *testing.T) {
	withTimeout(t, 30*time.Second, func() {
		w := runWorkload(t, 0)
		if err := w.Check(); err != nil {
			t.Fatalf("Check() failed on a valid run: %v", err)
		}
		m := w.Metrics()
		if m.TasksAssigned != 20 {
			t.Fatalf("TasksAssigned=%d, want 20", m.TasksAssigned)
		}
		// All tasks should route to node-0 (lowest ID, online under seed 0).
		asg := w.AssignmentsSnapshot()
		for task, node := range asg {
			if node != "node-0" {
				t.Fatalf("task %s assigned to %s, want node-0 (lowest online ID)", task, node)
			}
		}
	})
}

// TestCorrectness_Percentile99_Edges exercises percentile99 indirectly via
// Metrics() under runs of different sizes is not possible (taskCount is fixed),
// so we assert the documented nearest-rank property on the produced P99: with
// 20 strictly-increasing latencies 1µs..20µs, int(20*0.99)=19 => 20µs.
func TestCorrectness_Percentile99_NearestRank(t *testing.T) {
	withTimeout(t, 30*time.Second, func() {
		m := runWorkload(t, 0).Metrics()
		if m.LatencyP99 != 20*time.Microsecond {
			t.Fatalf("P99=%v, want 20µs (nearest-rank index 19 of 1µs..20µs)", m.LatencyP99)
		}
	})
}

// TestCorrectness_EmptyWorkloadState exercises the metric/invariant paths on an
// empty injected state via the exported test seam. No tasks, no assignments:
// Metrics must not divide-by-zero or panic, P99 must be 0, and NoLostTasks must
// pass vacuously.
func TestCorrectness_EmptyWorkloadState(t *testing.T) {
	withTimeout(t, 10*time.Second, func() {
		eng := dst.NewEngine(0)
		eng.RegisterNode("node-0", "10.0.0.0:7777")
		eng.RegisterNode("node-1", "10.0.0.1:7777")
		eng.RegisterNode("node-2", "10.0.0.2:7777")
		w := dstworkload.NewWorkloadWithState(eng,
			[]string{"node-0", "node-1", "node-2"},
			map[string]string{}, // no assignments
			nil,                  // no tasks
			nil,                  // no duplicates
		)
		m := w.Metrics()
		if m.TasksAssigned != 0 {
			t.Fatalf("TasksAssigned=%d on empty state, want 0", m.TasksAssigned)
		}
		if m.LatencyP99 != 0 {
			t.Fatalf("P99=%v on empty state, want 0", m.LatencyP99)
		}
		if m.ThroughputPerSec != 0 {
			t.Fatalf("Throughput=%v on empty state, want 0 (no wall time, no assignments)", m.ThroughputPerSec)
		}
		if err := dstworkload.NoLostTasks(w); err != nil {
			t.Fatalf("NoLostTasks should pass vacuously on empty task list: %v", err)
		}
	})
}

// TestCorrectness_QuorumFailure_Setup verifies Setup-time quorum is not the path
// under test here (Setup always registers 5 online nodes), but the invariant
// itself bites when quorum is lost. This complements the existing bite test by
// driving it through a freshly-built engine with exactly 2 online nodes.
func TestCorrectness_QuorumInvariantBites(t *testing.T) {
	withTimeout(t, 10*time.Second, func() {
		eng := dst.NewEngine(0)
		eng.RegisterNode("node-0", "a")
		eng.RegisterNode("node-1", "b")
		eng.RegisterNode("node-2", "c")
		if n, ok := eng.GetNode("node-2"); ok {
			n.Online = false
		}
		w := dstworkload.NewWorkloadWithState(eng,
			[]string{"node-0", "node-1", "node-2"},
			map[string]string{}, nil, nil)
		if err := dstworkload.QuorumAtLeast3(w); err == nil {
			t.Fatal("QuorumAtLeast3 did not bite with only 2 online nodes")
		}
	})
}
