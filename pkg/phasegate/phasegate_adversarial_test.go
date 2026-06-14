// Package phasegate_test — adversarial, mutation-verified audit of the
// phasegate gating state machine (HXC-1386 follow-up audit).
//
// This file complements phasegate_concurrent_test.go and phasegate_test.go.
// It targets the four contract-critical properties an auditor must pin:
//
//	(A) DETERMINISTIC TOPO ORDER that is independent of the INPUT slice
//	    ordering — i.e. no map-iteration-order leak. The existing
//	    TestTopoOrder_Deterministic only proves two calls on ONE graph agree;
//	    it does NOT prove that two graphs built from the SAME edge set in a
//	    DIFFERENT slice order produce the SAME order. That is the property that
//	    actually catches a map-iteration leak, and it is proven here with a
//	    multi-root diamond whose inputs are deterministically permuted.
//
//	(B) GATE PREDICATE BOUNDARY for the documented GTE contract
//	    (actual >= threshold): at threshold-epsilon it stays gated, at exactly
//	    threshold it opens. The GTE comparison is the documented-correct
//	    behavior and is NOT to be "fixed".
//
//	(C) CYCLE HANDLING returns a clean typed error (no infinite loop, no panic),
//	    proven for a self-loop and a multi-node cycle, under a watchdog timeout.
//
//	(D) CONCURRENT READ RACE-FREEDOM over a richer (non-linear) graph than the
//	    default chain, hammered under -race, with sink-side gating assertions.
//
// FINDING (see report): no data race, no nondeterminism, no early-open, no
// cycle hang was found. Each test below carries an explicit MUTATION PROOF
// describing the canonical production mutation that makes it fail.
package phasegate_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	. "github.com/HelixDevelopment/helix_cluster/pkg/phasegate"
)

// diamondGates returns a non-linear DAG used to stress tie-breaking:
//
//	     root
//	    /    \
//	 left    right        (left < right lexicographically)
//	    \    /
//	    merge
//	   /  |  \
//	z1   z2   z3          (three sinks, all depend on merge)
//
// Multiple nodes share in-degree-zero / become-ready-together moments, so the
// only thing that makes the output stable is the lexicographic tie-break. The
// expected order is fully determined by Kahn + lex tie-break.
func diamondGates() []Gate {
	cond := []SoakCondition{
		{Metric: "m", Comparator: GTE, Threshold: 1, ForDuration: "1s"},
	}
	return []Gate{
		{ID: "root", DependsOn: nil, Conditions: cond},
		{ID: "left", DependsOn: []string{"root"}, Conditions: cond},
		{ID: "right", DependsOn: []string{"root"}, Conditions: cond},
		{ID: "merge", DependsOn: []string{"left", "right"}, Conditions: cond},
		{ID: "z1", DependsOn: []string{"merge"}, Conditions: cond},
		{ID: "z2", DependsOn: []string{"merge"}, Conditions: cond},
		{ID: "z3", DependsOn: []string{"merge"}, Conditions: cond},
	}
}

// permute returns a deterministic rotation of the input slice by k positions,
// giving us many distinct input orderings of the SAME logical graph without
// importing math/rand (keeps the test hermetic).
func permute(gs []Gate, k int) []Gate {
	n := len(gs)
	out := make([]Gate, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, gs[(i+k)%n])
	}
	return out
}

// TestTopoOrder_InputOrderIndependent is the CENTERPIECE nondeterminism probe.
// It builds the SAME diamond graph from many different INPUT slice orderings
// and asserts every resulting TopoOrder is byte-for-byte identical. A topo sort
// that leaked map-iteration order (or insertion order) into the result would
// diverge across these permutations even though the graph is identical.
//
// The expected order is the unique Kahn-with-lexicographic-tie-break order:
//
//	root, left, right, merge, z1, z2, z3
//
// MUTATION PROOF: in kahnOrder, seed the ready queue / collect newlyReady by
// ranging the gates MAP instead of the sorted slice (e.g. replace the
// `sort.Strings(ready)` on phasegate.go:219 with nothing, or build `ready` by
// `for id := range gates`), and the permutations diverge → this test FAILS.
func TestTopoOrder_InputOrderIndependent(t *testing.T) {
	t.Parallel()

	want := []string{"root", "left", "right", "merge", "z1", "z2", "z3"}
	base := diamondGates()

	var canonical []string
	for k := 0; k < len(base)*3; k++ {
		perm := permute(base, k)
		gg, err := NewGateGraph(perm)
		if err != nil {
			t.Fatalf("permutation k=%d: NewGateGraph error: %v", k, err)
		}
		got, err := gg.TopoOrder()
		if err != nil {
			t.Fatalf("permutation k=%d: TopoOrder error: %v", k, err)
		}

		// Must equal the analytically-expected order.
		if len(got) != len(want) {
			t.Fatalf("permutation k=%d: len=%d want %d (%v)", k, len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("permutation k=%d: order[%d]=%q want %q (full=%v)",
					k, i, got[i], want[i], got)
			}
		}

		// And must equal the order produced by the very first permutation,
		// pinning input-order independence directly.
		if canonical == nil {
			canonical = got
			continue
		}
		for i := range canonical {
			if got[i] != canonical[i] {
				t.Fatalf("permutation k=%d diverged from canonical at [%d]: %q vs %q",
					k, i, got[i], canonical[i])
			}
		}
	}
}

// TestTopoOrder_TieBreakIsLexicographic isolates the tie-break rule: a graph of
// independent roots (no edges) MUST come back in lexicographic order regardless
// of the order they were supplied in. This is the single property that proves
// the ready-queue is sorted, not map/insertion ordered.
//
// MUTATION PROOF: remove `sort.Strings(ready)` (phasegate.go:219) → with the
// reversed input below the output would lead with "delta"/insertion order
// instead of "alpha" → assertion FAILS.
func TestTopoOrder_TieBreakIsLexicographic(t *testing.T) {
	t.Parallel()

	cond := []SoakCondition{{Metric: "m", Comparator: GTE, Threshold: 1, ForDuration: "1s"}}
	// Supplied in deliberately NON-lexicographic (reverse) order.
	gates := []Gate{
		{ID: "delta", Conditions: cond},
		{ID: "charlie", Conditions: cond},
		{ID: "bravo", Conditions: cond},
		{ID: "alpha", Conditions: cond},
	}
	gg, err := NewGateGraph(gates)
	if err != nil {
		t.Fatalf("NewGateGraph: %v", err)
	}
	got, err := gg.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder: %v", err)
	}
	want := []string{"alpha", "bravo", "charlie", "delta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("independent roots not lex-ordered: order[%d]=%q want %q (full=%v)",
				i, got[i], want[i], got)
		}
	}
}

// TestReady_GTEBoundary pins the DOCUMENTED-CORRECT GTE predicate
// (actual >= threshold) at the exact boundary: threshold-epsilon stays gated,
// exactly-threshold opens, threshold+epsilon stays open. The GTE comparison is
// correct and must NOT be changed; this test pins it so a future "fix" to GT
// (actual > threshold) is caught.
//
// MUTATION PROOF: change `case GTE: return actual >= threshold` to
// `return actual > threshold` (phasegate.go:313) → the exactly-threshold case
// flips to gated → "at threshold must OPEN" FAILS.
func TestReady_GTEBoundary(t *testing.T) {
	t.Parallel()

	const threshold = 0.99
	gates := []Gate{
		{ID: "g", Conditions: []SoakCondition{
			{Metric: "rate", Comparator: GTE, Threshold: threshold, ForDuration: "1s"},
		}},
	}
	gg, err := NewGateGraph(gates)
	if err != nil {
		t.Fatalf("NewGateGraph: %v", err)
	}

	// One ULP below threshold: must stay GATED (closed).
	below := threshold - 1e-9
	if ok, reasons := gg.Ready("g", map[string]float64{"rate": below}); ok {
		t.Errorf("at threshold-epsilon (%v) gate opened early; want gated. reasons=%v", below, reasons)
	} else if len(reasons) == 0 {
		t.Errorf("gated gate returned no reasons at %v", below)
	}

	// Exactly threshold: GTE must OPEN.
	if ok, reasons := gg.Ready("g", map[string]float64{"rate": threshold}); !ok {
		t.Errorf("at exactly threshold (%v) gate stayed gated; GTE must open. reasons=%v", threshold, reasons)
	}

	// Above threshold: must stay OPEN.
	above := threshold + 1e-9
	if ok, _ := gg.Ready("g", map[string]float64{"rate": above}); !ok {
		t.Errorf("at threshold+epsilon (%v) gate gated; want open", above)
	}
}

// TestReady_DependencyGateBlockedUntilUpstreamMetrics asserts the load-bearing
// gating SEMANTIC across a dependency edge using the real DefaultPhase6Gates:
// when 6a's metrics are unmet, 6a is gated; when only 6a's metrics are present
// and met, 6a opens but the DOWNSTREAM 6b stays gated because its own
// conditions are absent. This proves a downstream phase does NOT open early
// merely because an upstream opened.
//
// MUTATION PROOF: make Ready treat an absent metric as MET (drop the
// `!present` reason branch and `continue`, phasegate.go:291-293) → 6b would
// report Ready with no 6b metrics supplied → "6b must stay gated" FAILS.
func TestReady_DependencyGateBlockedUntilUpstreamMetrics(t *testing.T) {
	t.Parallel()

	gg, err := NewGateGraph(DefaultPhase6Gates())
	if err != nil {
		t.Fatalf("NewGateGraph: %v", err)
	}

	// Only 6a's metrics present, and satisfied.
	only6a := map[string]float64{
		"formation_success_rate":         0.999,
		"leader_election_latency_ms_p99": 100,
	}

	if ok, reasons := gg.Ready("6a", only6a); !ok {
		t.Errorf("6a with its own metrics met must OPEN; reasons=%v", reasons)
	}
	// 6b's conditions reference metrics not in only6a -> must stay gated.
	if ok, _ := gg.Ready("6b", only6a); ok {
		t.Error("6b opened early with no 6b metrics supplied; want gated")
	}
}

// TestCycle_NoHangSelfLoop proves a self-dependency (a depends on a) yields a
// clean error from NewGateGraph rather than an infinite loop or panic. A
// watchdog goroutine bounds the wall-clock time so a hypothetical hang is
// reported as a failure instead of stalling the suite.
//
// MUTATION PROOF: weaken kahnOrder's final completeness check
// (`if len(result) != len(gates)`) to always succeed → the self-loop is no
// longer reported as a cycle → "want cycle error" FAILS.
func TestCycle_NoHangSelfLoop(t *testing.T) {
	t.Parallel()
	runWithWatchdog(t, "self-loop", func() {
		gates := []Gate{
			{ID: "a", DependsOn: []string{"a"},
				Conditions: []SoakCondition{{Metric: "m", Comparator: GTE, Threshold: 1, ForDuration: "1s"}}},
		}
		if _, err := NewGateGraph(gates); err == nil {
			t.Error("self-loop a->a: expected cycle error, got nil")
		}
	})
}

// TestCycle_NoHangMultiNode proves a multi-node cycle (a->b->c->a) is reported
// as a clean error, under the same watchdog.
//
// MUTATION PROOF: same as above — neutering the completeness check lets the
// cycle slip through and this assertion FAILS.
func TestCycle_NoHangMultiNode(t *testing.T) {
	t.Parallel()
	runWithWatchdog(t, "multi-node cycle", func() {
		cond := []SoakCondition{{Metric: "m", Comparator: GTE, Threshold: 1, ForDuration: "1s"}}
		gates := []Gate{
			{ID: "a", DependsOn: []string{"c"}, Conditions: cond},
			{ID: "b", DependsOn: []string{"a"}, Conditions: cond},
			{ID: "c", DependsOn: []string{"b"}, Conditions: cond},
		}
		if _, err := NewGateGraph(gates); err == nil {
			t.Error("cycle a->b->c->a: expected cycle error, got nil")
		}
	})
}

// runWithWatchdog runs fn in a goroutine and fails the test if it does not
// return within a generous bound, converting any infinite-loop regression into
// a reported failure rather than a hung test binary.
func runWithWatchdog(t *testing.T, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: did not complete within watchdog window (possible infinite loop)", name)
	}
}

// TestConcurrentReadsRaceFree_RichGraph hammers concurrent Ready + TopoOrder
// over the non-linear diamond graph (richer fan-out/fan-in than the default
// chain) under -race, with sink-side gating assertions inside every goroutine.
// This widens the concurrent read surface beyond the existing linear-chain
// concurrency test.
//
// MUTATION PROOF: invert evalCondition's GTE branch
// (`return actual < threshold`) → the open-world readers see Ready==false →
// t.Error fires. A data race on gg.gates would be reported by -race.
func TestConcurrentReadsRaceFree_RichGraph(t *testing.T) {
	t.Parallel()

	gg, err := NewGateGraph(diamondGates())
	if err != nil {
		t.Fatalf("NewGateGraph: %v", err)
	}

	openWorld := map[string]float64{"m": 5}   // GTE 1 -> open
	closedWorld := map[string]float64{"m": 0} // GTE 1 -> gated
	ids := []string{"root", "left", "right", "merge", "z1", "z2", "z3"}

	const goroutines = 48
	const iters = 400

	var wg sync.WaitGroup
	errCh := make(chan string, goroutines*3)

	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := ids[(w+i)%len(ids)]

				if ok, reasons := gg.Ready(id, openWorld); !ok {
					errCh <- fmt.Sprintf("gate %q expected OPEN but gated: %v", id, reasons)
				}
				if ok, reasons := gg.Ready(id, closedWorld); ok {
					errCh <- fmt.Sprintf("gate %q expected GATED but opened", id)
				} else if len(reasons) == 0 {
					errCh <- fmt.Sprintf("gate %q gated with no reasons", id)
				}

				ord, err := gg.TopoOrder()
				if err != nil {
					errCh <- fmt.Sprintf("TopoOrder error: %v", err)
				} else if len(ord) != len(ids) {
					errCh <- fmt.Sprintf("TopoOrder len=%d want %d", len(ord), len(ids))
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
}

// TestEdge_EmptyAndSingle pins the degenerate graphs: an empty gate set yields
// an empty (non-error) topo order, and a single isolated gate yields a
// one-element order. These edges must not panic or error.
//
// MUTATION PROOF: change kahnOrder's completeness check to
// `len(result) != len(gates)+1` → the empty/single cases would falsely report a
// cycle → these assertions FAIL.
func TestEdge_EmptyAndSingle(t *testing.T) {
	t.Parallel()

	// Empty set.
	gg, err := NewGateGraph(nil)
	if err != nil {
		t.Fatalf("empty NewGateGraph: %v", err)
	}
	ord, err := gg.TopoOrder()
	if err != nil {
		t.Fatalf("empty TopoOrder: %v", err)
	}
	if len(ord) != 0 {
		t.Fatalf("empty TopoOrder len=%d want 0", len(ord))
	}

	// Single isolated gate.
	gg, err = NewGateGraph([]Gate{
		{ID: "solo", Conditions: []SoakCondition{
			{Metric: "m", Comparator: GTE, Threshold: 1, ForDuration: "1s"}}},
	})
	if err != nil {
		t.Fatalf("single NewGateGraph: %v", err)
	}
	ord, err = gg.TopoOrder()
	if err != nil {
		t.Fatalf("single TopoOrder: %v", err)
	}
	if len(ord) != 1 || ord[0] != "solo" {
		t.Fatalf("single TopoOrder=%v want [solo]", ord)
	}
}
