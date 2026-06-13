// Package phasegate_test — adversarial concurrency coverage for the
// immutable-after-build GateGraph (HXC-1386).
//
// THREAD-SAFETY CONTRACT (write-path analysis):
// GateGraph.gates is an unexported map[string]Gate. The ONLY write to it is
// `gg.gates[g.ID] = g` inside NewGateGraph's Pass-1 registration loop
// (phasegate.go:159) — a setup-time build performed entirely before the
// *GateGraph value is returned to any caller. There is no exported setter,
// no AddGate, no UpdateState, and no other assignment to gg.gates anywhere in
// the package. Every post-construction access (TopoOrder, Ready) is a pure
// READ. The package doc states "This is a pure in-memory model" with no
// concurrency claim either way.
//
// => CASE 3 (immutable-after-build, like pkg/modelrouter / pkg/fedtopology):
//
//	concurrent READS of a never-again-written map are safe by the Go memory
//	model — the construction happens-before the goroutines that observe the
//	returned pointer. A sync.RWMutex would be gold-plating and is NOT added.
//
// These tests do NOT mutate the graph from multiple goroutines (no such API
// exists). They hammer the real concurrent read surface (Ready + TopoOrder)
// under -race to prove the read paths are genuinely race-free, AND they assert
// the gating semantics under that concurrency: a closed (unmet) gate blocks,
// an open (met) gate passes, exactly per the documented rule.
package phasegate_test

import (
	"fmt"
	"sync"
	"testing"

	. "github.com/HelixDevelopment/helix_cluster/pkg/phasegate"
)

// metricsFor returns an observed-metric map that satisfies every SoakCondition
// of every DefaultPhase6Gates gate (the "all gates open" world).
func metricsForAllOpen() map[string]float64 {
	return map[string]float64{
		"formation_success_rate":          1.0,    // GTE 0.99  -> met
		"leader_election_latency_ms_p99":  100,    // LT  500   -> met
		"suspicion_convergence_ratio":     0.99,   // GTE 0.95  -> met
		"gossip_roundtrip_latency_ms_p99": 20,     // LT  100   -> met
		"cross_cell_latency_ms_p99":       10,     // LT  50    -> met
		"cross_cell_packet_loss_ratio":    0.0,    // LTE 0.001 -> met
		"cluster_health_score":            0.9995, // GTE 0.999 -> met
		"unscheduled_tasks_total":         0,      // EQ  0     -> met
	}
}

// metricsForAllClosed returns observed metrics that violate every gate's first
// condition (the "all gates closed" world).
func metricsForAllClosed() map[string]float64 {
	return map[string]float64{
		"formation_success_rate":          0.50, // GTE 0.99  -> UNMET (closed)
		"leader_election_latency_ms_p99":  9000, // LT  500   -> UNMET
		"suspicion_convergence_ratio":     0.10, // GTE 0.95  -> UNMET
		"gossip_roundtrip_latency_ms_p99": 9000, // LT  100   -> UNMET
		"cross_cell_latency_ms_p99":       9000, // LT  50    -> UNMET
		"cross_cell_packet_loss_ratio":    1.0,  // LTE 0.001 -> UNMET
		"cluster_health_score":            0.10, // GTE 0.999 -> UNMET
		"unscheduled_tasks_total":         7,    // EQ  0     -> UNMET
	}
}

// TestConcurrentReadsRaceFree launches many goroutines that concurrently call
// Ready (with both open- and closed-metric worlds) and TopoOrder against ONE
// shared *GateGraph built once. Under `go test -race` this proves the
// post-construction read surface of the immutable map is genuinely race-free,
// and it sink-side-asserts the gating outcome inside every goroutine: an open
// gate passes, a closed gate is blocked. A data race or a wrong gating verdict
// fails the test.
//
// MUTATION PROOF: invert the `met` result in evalCondition (phasegate.go, e.g.
// change `case GTE: return actual >= threshold` to `return actual < threshold`)
// and the openWorld goroutines see Ready==false -> t.Errorf fires.
func TestConcurrentReadsRaceFree(t *testing.T) {
	t.Parallel()

	gg, err := NewGateGraph(DefaultPhase6Gates())
	if err != nil {
		t.Fatalf("NewGateGraph: unexpected error: %v", err)
	}

	open := metricsForAllOpen()
	closed := metricsForAllClosed()
	gateIDs := []string{"6a", "6b", "6c", "6d"}

	const goroutines = 64
	const iters = 500

	var wg sync.WaitGroup
	errCh := make(chan string, goroutines*4)

	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				gid := gateIDs[(w+i)%len(gateIDs)]

				// READ path 1: open world -> gate must PASS.
				if ok, reasons := gg.Ready(gid, open); !ok {
					errCh <- fmt.Sprintf("gate %q expected OPEN/Ready but blocked: %v", gid, reasons)
				}

				// READ path 2: closed world -> gate must be BLOCKED with reasons.
				if ok, reasons := gg.Ready(gid, closed); ok {
					errCh <- fmt.Sprintf("gate %q expected CLOSED/not-Ready but passed", gid)
				} else if len(reasons) == 0 {
					errCh <- fmt.Sprintf("gate %q blocked but returned no reasons", gid)
				}

				// READ path 3: TopoOrder over the same shared map.
				ord, err := gg.TopoOrder()
				if err != nil {
					errCh <- fmt.Sprintf("TopoOrder error: %v", err)
				} else if len(ord) != 4 {
					errCh <- fmt.Sprintf("TopoOrder len=%d want 4", len(ord))
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

// TestConcurrentTopoOrderDeterministic proves that concurrent TopoOrder calls
// on the shared immutable graph all return the SAME deterministic ordering
// (6a,6b,6c,6d), i.e. concurrent reads neither race nor perturb the result.
//
// MUTATION PROOF: remove `sort.Strings(ready)` (phasegate.go:219) and the
// deterministic-order assertion below can diverge across goroutines.
func TestConcurrentTopoOrderDeterministic(t *testing.T) {
	t.Parallel()

	gg, err := NewGateGraph(DefaultPhase6Gates())
	if err != nil {
		t.Fatalf("NewGateGraph: unexpected error: %v", err)
	}

	want := []string{"6a", "6b", "6c", "6d"}

	const goroutines = 32
	var wg sync.WaitGroup
	bad := make(chan string, goroutines)

	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				got, err := gg.TopoOrder()
				if err != nil {
					bad <- fmt.Sprintf("TopoOrder error: %v", err)
					return
				}
				if len(got) != len(want) {
					bad <- fmt.Sprintf("len=%d want %d", len(got), len(want))
					return
				}
				for k := range want {
					if got[k] != want[k] {
						bad <- fmt.Sprintf("order[%d]=%q want %q", k, got[k], want[k])
						return
					}
				}
			}
		}()
	}

	wg.Wait()
	close(bad)
	for msg := range bad {
		t.Error(msg)
	}
}

// TestConcurrentGatingTransition asserts the load-bearing gating semantics
// under concurrency: for a custom graph with a single gate, half the
// goroutines read it through an "open" metric world and half through a
// "closed" one, simultaneously. The open readers MUST always see Ready==true
// and the closed readers MUST always see Ready==false. Because the metric maps
// are per-reader (not the graph), this isolates that the SAME graph correctly
// gates based purely on supplied observations, with no cross-goroutine
// interference.
//
// MUTATION PROOF: in Ready, change the absent-metric branch to `continue`
// without appending a reason AND treat unmet as met — the closed readers would
// observe Ready==true and fail. (Equivalently, invert evalCondition.)
func TestConcurrentGatingTransition(t *testing.T) {
	t.Parallel()

	gates := []Gate{
		{
			ID: "soak",
			Conditions: []SoakCondition{
				{Metric: "err_rate", Comparator: LTE, Threshold: 0.01, ForDuration: "1m"},
			},
		},
	}
	gg, err := NewGateGraph(gates)
	if err != nil {
		t.Fatalf("NewGateGraph: unexpected error: %v", err)
	}

	openWorld := map[string]float64{"err_rate": 0.0}   // 0.0 <= 0.01 -> open
	closedWorld := map[string]float64{"err_rate": 0.9} // 0.9 <= 0.01 false -> closed

	const goroutines = 40
	var wg sync.WaitGroup
	fail := make(chan string, goroutines)

	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			wantOpen := w%2 == 0
			world := closedWorld
			if wantOpen {
				world = openWorld
			}
			for i := 0; i < 1000; i++ {
				ok, reasons := gg.Ready("soak", world)
				if wantOpen && !ok {
					fail <- fmt.Sprintf("open reader saw blocked gate: %v", reasons)
					return
				}
				if !wantOpen && ok {
					fail <- "closed reader saw open gate"
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(fail)
	for msg := range fail {
		t.Error(msg)
	}
}
