package burstcapacity

import (
	"math"
	"sync"
	"testing"
)

// This file pins the capacity contract of the burstcapacity estimator with
// adversarial, mutation-verified probes.
//
// IMPORTANT MODEL NOTE: pkg/burstcapacity is a PURE, STATELESS estimator. It is
// NOT an admission/slot limiter — there is no Admit/Release lifecycle, no
// in-flight counter, no mutex, no token bucket. Estimator.ActivateBurst is a
// pure function of its arguments. Therefore the classic limiter bug classes map
// onto this contract as follows:
//
//   OVER-ADMIT      -> over-provisioning: sizing EstimatedUnits to the FULL
//                      demand instead of the EXCESS, or selecting a provider
//                      whose Capacity is below the required excess.
//   UNDER-ADMIT /   -> boundary off-by-ones: threshold must be STRICT (util ==
//   off-by-one         threshold => no burst); excess must be STRICTLY positive
//                      (demand == localCapacity => no burst); provider Capacity
//                      == excess must be SUFFICIENT (the exact admit boundary).
//   LEAK / DOUBLE-  -> not applicable: no slot is acquired/returned. We instead
//   RELEASE            assert IDEMPOTENCE/PURITY: repeated identical calls yield
//                      byte-identical results (no erosion, no drift).
//   DATA RACE       -> the function shares no state; we still PROVE it under
//                      -race by hammering it concurrently and asserting every
//                      goroutine observes the identical deterministic result.

// --- BOUNDARY: utilization threshold is STRICT (> not >=) ---------------------

// At util EXACTLY == threshold, the doc says burst activates only when util is
// "strictly greater than e.Threshold". So util == threshold must NOT burst.
//
// Mutation that bites: changing line 61 `if localUtil <= e.Threshold` to
// `if localUtil < e.Threshold` makes util==threshold fall through and burst.
func TestBoundary_UtilEqualsThreshold_NoBurst(t *testing.T) {
	e := Estimator{Threshold: 0.90}
	providers := []Provider{{ID: "remote-a", CostPerUnit: 1.0, Capacity: 500}}

	// util EXACTLY at the threshold; demand exceeds capacity so the ONLY thing
	// preventing a burst is the strict-threshold rule.
	alloc, activated := e.ActivateBurst(0.90, 100.0, 130.0, providers)
	if activated {
		t.Fatalf("util==threshold(0.90) must NOT burst (strict >), got %+v", alloc)
	}
	if alloc != (Allocation{}) {
		t.Fatalf("expected zero Allocation at util==threshold, got %+v", alloc)
	}
}

// One ULP above the threshold MUST burst — pins the boundary from the other
// side so the strict-> test above cannot be satisfied by an always-off mutation.
func TestBoundary_UtilJustAboveThreshold_Bursts(t *testing.T) {
	e := Estimator{Threshold: 0.90}
	providers := []Provider{{ID: "remote-a", CostPerUnit: 1.0, Capacity: 500}}

	justAbove := math.Nextafter(0.90, 1.0) // smallest float64 > 0.90
	alloc, activated := e.ActivateBurst(justAbove, 100.0, 130.0, providers)
	if !activated {
		t.Fatalf("util just above threshold (%v) MUST burst", justAbove)
	}
	if alloc.EstimatedUnits != 30.0 {
		t.Fatalf("excess = %.4f, want 30.0", alloc.EstimatedUnits)
	}
}

// --- BOUNDARY: excess must be STRICTLY positive -------------------------------

// demand EXACTLY == localCapacity => excess 0 => no spillover => no burst.
//
// Mutation that bites: changing line 67 `if excess <= 0` to `if excess < 0`
// would activate a zero-sized burst at the boundary.
func TestBoundary_DemandEqualsCapacity_NoBurst(t *testing.T) {
	e := Estimator{Threshold: 0.90}
	providers := []Provider{{ID: "remote-a", CostPerUnit: 1.0, Capacity: 500}}

	alloc, activated := e.ActivateBurst(0.99, 100.0, 100.0, providers)
	if activated {
		t.Fatalf("demand==capacity must NOT burst (excess==0), got %+v", alloc)
	}
}

// One ULP of demand above capacity MUST burst with that tiny excess, and the
// provider sized to it. Pins the positive-excess boundary from above.
func TestBoundary_DemandJustAboveCapacity_BurstsTinyExcess(t *testing.T) {
	e := Estimator{Threshold: 0.90}
	providers := []Provider{{ID: "remote-a", CostPerUnit: 1.0, Capacity: 500}}

	demand := math.Nextafter(100.0, math.Inf(1))
	alloc, activated := e.ActivateBurst(0.99, 100.0, demand, providers)
	if !activated {
		t.Fatalf("demand just above capacity MUST burst")
	}
	wantExcess := demand - 100.0
	if alloc.EstimatedUnits != wantExcess {
		t.Fatalf("excess = %v, want %v", alloc.EstimatedUnits, wantExcess)
	}
	if alloc.EstimatedUnits <= 0 {
		t.Fatalf("excess must be strictly positive, got %v", alloc.EstimatedUnits)
	}
}

// --- BOUNDARY (CENTERPIECE): provider Capacity == excess is SUFFICIENT --------

// The core "cap never exceeded" invariant: a provider may be chosen ONLY if its
// Capacity is at least the excess. The EXACT boundary is Capacity == excess,
// which MUST be sufficient (admit), while Capacity == excess - epsilon MUST be
// rejected (the next-smaller provider does not fit).
//
// Mutation that bites BOTH ways:
//   - line 91 `if p.Capacity < need` -> `if p.Capacity <= need` makes the
//     exact-fit provider be SKIPPED (under-admit at the boundary): the
//     ExactFit subtest then sees no burst / wrong provider and FAILS.
//   - line 91 `<` -> `<=`-relaxed-to-always-pass (e.g. removing the check)
//     makes a too-small provider be accepted (over-admit): the JustTooSmall
//     subtest then wrongly activates and FAILS.
func TestBoundary_ProviderCapacityEqualsExcess_IsSufficient(t *testing.T) {
	e := Estimator{Threshold: 0.90}
	localCapacity := 100.0
	demand := 150.0
	excess := demand - localCapacity // 50, derived

	t.Run("ExactFit_Admits", func(t *testing.T) {
		// Only provider has Capacity EXACTLY == excess. Must be chosen.
		providers := []Provider{{ID: "exact", CostPerUnit: 1.0, Capacity: excess}}
		alloc, activated := e.ActivateBurst(0.99, localCapacity, demand, providers)
		if !activated {
			t.Fatalf("provider with Capacity==excess(%.1f) MUST be sufficient", excess)
		}
		if alloc.ProviderID != "exact" {
			t.Fatalf("chose %q, want exact", alloc.ProviderID)
		}
		if alloc.EstimatedUnits != excess {
			t.Fatalf("units = %.1f, want %.1f", alloc.EstimatedUnits, excess)
		}
	})

	t.Run("JustTooSmall_Rejects", func(t *testing.T) {
		// Capacity one ULP below the excess: must NOT be chosen -> no burst.
		tooSmall := math.Nextafter(excess, 0) // largest float64 < excess
		providers := []Provider{{ID: "tooSmall", CostPerUnit: 0.01, Capacity: tooSmall}}
		alloc, activated := e.ActivateBurst(0.99, localCapacity, demand, providers)
		if activated {
			t.Fatalf("provider Capacity(%v) < excess(%.1f) must be rejected, got %+v",
				tooSmall, excess, alloc)
		}
	})
}

// --- SELECTION: cheapest-sufficient, with deterministic tie-breaking ----------

// When two SUFFICIENT providers share the lowest cost, the tie breaks to lower
// Capacity, then to lexicographically smaller ID. This pins betterChoice so a
// non-deterministic / first-wins mutation is caught.
//
// Mutation that bites: dropping the Capacity tie-break (lines 108-110) or the
// ID tie-break makes selection depend on slice order; the shuffled subtests
// below then disagree.
func TestSelection_TieBreak_LowerCapacityThenID(t *testing.T) {
	e := Estimator{Threshold: 0.90}
	localCapacity := 100.0
	demand := 150.0 // excess = 50

	// Two equally cheap, equally SUFFICIENT providers differing only in capacity:
	// the SMALLER-capacity one wins (tighter fit).
	t.Run("LowerCapacityWins", func(t *testing.T) {
		for _, order := range [][]Provider{
			{
				{ID: "big", CostPerUnit: 1.0, Capacity: 1000},
				{ID: "tight", CostPerUnit: 1.0, Capacity: 60},
			},
			{ // reversed input order must not change the winner
				{ID: "tight", CostPerUnit: 1.0, Capacity: 60},
				{ID: "big", CostPerUnit: 1.0, Capacity: 1000},
			},
		} {
			alloc, activated := e.ActivateBurst(0.99, localCapacity, demand, order)
			if !activated || alloc.ProviderID != "tight" {
				t.Fatalf("order %v: chose %q activated=%v, want tight (lower capacity)",
					order, alloc.ProviderID, activated)
			}
		}
	})

	// Equal cost AND equal capacity -> smallest ID wins, regardless of order.
	t.Run("LowerIDWins", func(t *testing.T) {
		for _, order := range [][]Provider{
			{
				{ID: "zeta", CostPerUnit: 1.0, Capacity: 60},
				{ID: "alpha", CostPerUnit: 1.0, Capacity: 60},
			},
			{
				{ID: "alpha", CostPerUnit: 1.0, Capacity: 60},
				{ID: "zeta", CostPerUnit: 1.0, Capacity: 60},
			},
		} {
			alloc, _ := e.ActivateBurst(0.99, localCapacity, demand, order)
			if alloc.ProviderID != "alpha" {
				t.Fatalf("order %v: chose %q, want alpha (smallest ID)", order, alloc.ProviderID)
			}
		}
	})
}

// Over-provision guard: EstimatedUnits is ALWAYS the excess, never the full
// demand, and is always <= the chosen provider's Capacity (cap never exceeded).
//
// Mutation that bites: line 79 `EstimatedUnits: excess` -> `: demand` makes
// units exceed the provider Capacity here and the assertion fails.
func TestInvariant_UnitsNeverExceedChosenCapacity(t *testing.T) {
	e := Estimator{Threshold: 0.90}
	// excess = 50; chosen provider capacity 50 (exact). Full-demand (150) would
	// blow past it.
	providers := []Provider{{ID: "p", CostPerUnit: 1.0, Capacity: 50}}
	alloc, activated := e.ActivateBurst(0.99, 100.0, 150.0, providers)
	if !activated {
		t.Fatal("expected burst")
	}
	if alloc.EstimatedUnits > providers[0].Capacity {
		t.Fatalf("OVER-PROVISION: units %.1f exceed chosen capacity %.1f",
			alloc.EstimatedUnits, providers[0].Capacity)
	}
	if alloc.EstimatedUnits != 50.0 {
		t.Fatalf("units = %.1f, want excess 50.0 (not full demand)", alloc.EstimatedUnits)
	}
}

// --- PURITY / IDEMPOTENCE (the leak/double-release analog) --------------------

// Repeated identical calls must yield byte-identical results — no erosion, no
// drift, no hidden accumulation. This is the stateless analog of "acquire+
// release cycled many times keeps effective capacity constant".
func TestPurity_RepeatedCallsAreIdentical(t *testing.T) {
	e := Estimator{Threshold: 0.90}
	providers := []Provider{
		{ID: "a", CostPerUnit: 2.0, Capacity: 200},
		{ID: "b", CostPerUnit: 1.0, Capacity: 80},
		{ID: "c", CostPerUnit: 3.0, Capacity: 1000},
	}
	first, firstOK := e.ActivateBurst(0.95, 100.0, 130.0, providers)
	for i := 0; i < 10000; i++ {
		got, ok := e.ActivateBurst(0.95, 100.0, 130.0, providers)
		if ok != firstOK || got != first {
			t.Fatalf("call %d drifted: got (%+v,%v), want (%+v,%v)",
				i, got, ok, first, firstOK)
		}
	}
}

// --- CONCURRENCY (-race proof of statelessness) -------------------------------

// The estimator shares no mutable state, so concurrent ActivateBurst calls must
// be race-free and every goroutine must observe the IDENTICAL deterministic
// result. Run under `go test -race`.
func TestConcurrency_RaceFreeAndDeterministic(t *testing.T) {
	e := Estimator{Threshold: 0.90}
	providers := []Provider{
		{ID: "expensive", CostPerUnit: 5.0, Capacity: 1000},
		{ID: "cheap-tiny", CostPerUnit: 0.5, Capacity: 10},
		{ID: "cheap-big", CostPerUnit: 1.0, Capacity: 80},
		{ID: "mid", CostPerUnit: 3.0, Capacity: 200},
	}
	// Expected deterministic answer (cheapest SUFFICIENT for excess 50).
	wantAlloc, wantOK := e.ActivateBurst(0.99, 100.0, 150.0, providers)
	if !wantOK || wantAlloc.ProviderID != "cheap-big" {
		t.Fatalf("precondition: want cheap-big, got %+v ok=%v", wantAlloc, wantOK)
	}

	const goroutines = 64
	const iters = 2000
	var wg sync.WaitGroup
	errCh := make(chan string, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each goroutine has its OWN provider slice to avoid sharing test
			// fixtures (the production code must not need this, but it keeps the
			// test honest about what it is exercising).
			local := []Provider{
				{ID: "expensive", CostPerUnit: 5.0, Capacity: 1000},
				{ID: "cheap-tiny", CostPerUnit: 0.5, Capacity: 10},
				{ID: "cheap-big", CostPerUnit: 1.0, Capacity: 80},
				{ID: "mid", CostPerUnit: 3.0, Capacity: 200},
			}
			for i := 0; i < iters; i++ {
				got, ok := e.ActivateBurst(0.99, 100.0, 150.0, local)
				if !ok || got != wantAlloc {
					errCh <- "non-deterministic result under concurrency"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	if msg, bad := <-errCh; bad {
		t.Fatalf("concurrency violation: %s", msg)
	}
}

// --- EDGE: empty provider set, and zero-capacity local ------------------------

func TestEdge_NoProviders_NoBurst(t *testing.T) {
	e := Estimator{Threshold: 0.90}
	alloc, activated := e.ActivateBurst(0.99, 100.0, 200.0, nil)
	if activated {
		t.Fatalf("no providers => no burst, got %+v", alloc)
	}
}

// localCapacity 0 with positive demand above threshold: the entire demand is
// excess, and a sufficient provider absorbs it.
func TestEdge_ZeroLocalCapacity_ExcessIsFullDemand(t *testing.T) {
	e := Estimator{Threshold: 0.90}
	providers := []Provider{{ID: "p", CostPerUnit: 1.0, Capacity: 100}}
	alloc, activated := e.ActivateBurst(0.99, 0.0, 100.0, providers)
	if !activated {
		t.Fatal("zero local capacity with demand should burst")
	}
	if alloc.EstimatedUnits != 100.0 {
		t.Fatalf("excess = %.1f, want 100.0 (full demand when local cap is 0)", alloc.EstimatedUnits)
	}
}
