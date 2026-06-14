package gepetto

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

// This file is an adversarial audit of the HelixGepetto reserve-schedule
// contract. Unlike a finite-pool reservation system, ChutesCapacity is a PURE
// function of (loadFraction, gpusPerNode): it returns a per-GPU advisory
// reservation drawn from a static tier schedule, plus an append-only audit
// log. There is therefore no cumulative capacity counter to over-commit and no
// Release to leak. The genuine correctness edges are:
//
//  1. NO OVER-RESERVE ABOVE HIGH-WATER: load strictly above 0.80 must reserve
//     EXACTLY zero — never the schedule's 8. The (0.80, 0.81) gap is the only
//     region where the schedule alone would over-reserve, so it is the
//     centerpiece (mutation target: > vs >= on the high-water guard).
//  2. TIER BOUNDARY off-by-one: each tier bound is strictly exclusive
//     (load < below), so the boundary value itself bumps to the NEXT tier.
//     Pin every transition to the unit.
//  3. MONOTONICITY: reserved capacity never exceeds the schedule maximum (8)
//     for ANY input, and is never negative.
//  4. DATA RACE on the audit log under concurrent ChutesCapacity.

// reserveMax is the largest per-GPU reservation the schedule may ever return.
// No input — at, below, above, or far past the high-water mark — may exceed it.
const reserveMax = 8

// TestNoOverReserveAboveHighWater is the centerpiece "no over-commit" analogue:
// for EVERY load strictly greater than the high-water mark, the reserved
// capacity must be exactly zero. The (0.80, 0.81) gap is the load-bearing
// region: there the schedule's below:0.81 tier WOULD hand back 8, so only the
// anti-starvation guard keeps the reservation at 0. A > vs >= mutation, or
// deletion of the guard, over-reserves here and this test bites.
func TestNoOverReserveAboveHighWater(t *testing.T) {
	a := NewArbitrator(0)

	// Walk densely through the gap and well past it. Every one of these is
	// strictly above 0.80 and MUST reserve 0.
	aboveWater := []float64{
		0.8000001, 0.800001, 0.80001, 0.8001, 0.801, 0.805, 0.8099,
		0.80999999, 0.81, 0.8100001, 0.82, 0.85, 0.90, 0.95, 0.99, 1.00,
		1.0000001, 2.0, math.MaxFloat64,
	}
	for _, load := range aboveWater {
		got := a.ChutesCapacity("over", load, 8)
		if got != 0 {
			t.Errorf("OVER-RESERVE: ChutesCapacity(load=%v) = %d, want exactly 0 "+
				"(load is strictly above high-water %.2f; schedule must be overridden)",
				load, got, highWaterMark)
		}
	}
	t.Logf("no-over-reserve verified across %d above-water loads: all reserved 0", len(aboveWater))
}

// TestHighWaterBoundaryExact pins the inclusive/exclusive edge of the
// high-water guard to the unit. The contract is: > highWaterMark → 0, so
// EXACTLY 0.80 is NOT above water and uses the schedule (→ 8), while the very
// next representable float above 0.80 must collapse to 0.
//
// This is the off-by-one centerpiece. math.Nextafter gives the smallest float
// strictly greater than 0.80, so { 0.80 → 8, nextafter(0.80,1) → 0 } is the
// tightest possible boundary witness. A >= mutation flips 0.80 to 0 and this
// test bites on the lower side; a < / removal flips the upper side.
func TestHighWaterBoundaryExact(t *testing.T) {
	a := NewArbitrator(0)

	justAbove := math.Nextafter(highWaterMark, math.Inf(1))
	justBelow := math.Nextafter(highWaterMark, math.Inf(-1))

	cases := []struct {
		name string
		load float64
		want int
	}{
		{"just-below-0.80", justBelow, 8},  // still ≤ high-water → schedule (below 0.81)
		{"exactly-0.80", highWaterMark, 8}, // AT high-water, not above → schedule → 8
		{"just-above-0.80", justAbove, 0},  // first float above → anti-starvation → 0
	}
	for _, tc := range cases {
		got := a.ChutesCapacity("hw", tc.load, 2)
		if got != tc.want {
			t.Errorf("high-water boundary %s: ChutesCapacity(%v) = %d, want %d",
				tc.name, tc.load, got, tc.want)
		}
		t.Logf("boundary %s: load=%v -> %d (want %d) OK", tc.name, tc.load, got, tc.want)
	}
}

// TestTierBoundariesExclusive pins every tier transition to the unit. Each
// tier bound is strictly exclusive (resolveFromSchedule uses load < below), so
// the boundary value itself belongs to the HIGHER tier. An off-by-one (< vs
// <=) at any bound would shift a transition by one tier; the just-below /
// at-boundary pairs below catch it.
func TestTierBoundariesExclusive(t *testing.T) {
	a := NewArbitrator(0)

	cases := []struct {
		load float64
		want int
		why  string
	}{
		// Below 0.10 → 0; at 0.10 → 2 (boundary belongs to next tier).
		{math.Nextafter(0.10, math.Inf(-1)), 0, "just below 0.10"},
		{0.10, 2, "exactly 0.10 (exclusive bound → next tier)"},
		// Below 0.40 → 2; at 0.40 → 5.
		{math.Nextafter(0.40, math.Inf(-1)), 2, "just below 0.40"},
		{0.40, 5, "exactly 0.40 (exclusive bound → next tier)"},
		// Below 0.70 → 5; at 0.70 → 8.
		{math.Nextafter(0.70, math.Inf(-1)), 5, "just below 0.70"},
		{0.70, 8, "exactly 0.70 (exclusive bound → next tier)"},
		// Floor: zero and negative load → 0.
		{0.0, 0, "zero load"},
		{-0.0001, 0, "negative load (defensive → 0)"},
	}
	for _, tc := range cases {
		got := a.ChutesCapacity("tier", tc.load, 1)
		if got != tc.want {
			t.Errorf("tier boundary (%s): ChutesCapacity(%v) = %d, want %d",
				tc.why, tc.load, got, tc.want)
		}
		t.Logf("tier %s: load=%v -> %d OK", tc.why, tc.load, got)
	}
}

// TestReservedCapacityNeverExceedsMaxNorNegative sweeps the whole load domain
// and asserts the reservation is always within [0, reserveMax]. This is the
// schedule-wide analogue of "total reserved never exceeds capacity": no input
// may ever yield a reservation above the documented ceiling (8) or below 0.
func TestReservedCapacityNeverExceedsMaxNorNegative(t *testing.T) {
	a := NewArbitrator(0)
	// Dense sweep from -0.1 to 1.2 in 0.0005 steps, plus pathological values.
	for x := -0.1; x <= 1.2; x += 0.0005 {
		got := a.ChutesCapacity("sweep", x, 4)
		if got < 0 || got > reserveMax {
			t.Fatalf("OUT-OF-BOUND reservation: ChutesCapacity(%v) = %d, want within [0,%d]",
				x, got, reserveMax)
		}
	}
	for _, x := range []float64{math.Inf(1), math.MaxFloat64, math.SmallestNonzeroFloat64, 1e300} {
		got := a.ChutesCapacity("sweep-edge", x, 4)
		if got < 0 || got > reserveMax {
			t.Fatalf("OUT-OF-BOUND reservation: ChutesCapacity(%v) = %d, want within [0,%d]",
				x, got, reserveMax)
		}
	}
	t.Logf("full-domain sweep: every reservation within [0,%d]", reserveMax)
}

// TestNaNLoadReservesZero pins NaN handling: every comparison against NaN is
// false, so the high-water guard does not fire and the schedule loop matches no
// tier, yielding the defensive 0. NaN must never over-reserve.
func TestNaNLoadReservesZero(t *testing.T) {
	a := NewArbitrator(0)
	got := a.ChutesCapacity("nan", math.NaN(), 4)
	if got != 0 {
		t.Errorf("ChutesCapacity(NaN) = %d, want 0 (NaN comparisons false → defensive 0)", got)
	}
	recs := a.Records()
	if len(recs) != 1 || recs[0].ResolvedCapacity != 0 {
		t.Errorf("NaN record = %+v, want one record with ResolvedCapacity 0", recs)
	}
	t.Logf("NaN load reserved 0 and was recorded faithfully")
}

// TestConcurrentNoTornLogAndPeakBound hammers ChutesCapacity from many
// goroutines under -race. Because the reservation is a pure function, no
// over-commit is possible by construction; what we DO defend is (a) the audit
// log is race-free and loses no entry under concurrent append, and (b) no
// concurrently observed reservation ever exceeds reserveMax (an atomic peak
// tracker — the analogue of the concurrent-over-commit peak check). The record
// (mu-guarded append) is the only shared mutable state; a missing lock there
// would both race and drop records.
func TestConcurrentNoTornLogAndPeakBound(t *testing.T) {
	a := NewArbitrator(0)
	const goroutines = 32
	const callsEach = 200

	loads := []float64{0.0, 0.05, 0.10, 0.30, 0.40, 0.60, 0.70, 0.799, 0.80, 0.8001, 0.85, 0.95, 1.0}

	var peak int64 // highest reservation observed across all goroutines
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for c := 0; c < callsEach; c++ {
				load := loads[(g*7+c)%len(loads)]
				got := a.ChutesCapacity(fmt.Sprintf("g%d-c%d", g, c), load, 4)
				for {
					p := atomic.LoadInt64(&peak)
					if int64(got) <= p || atomic.CompareAndSwapInt64(&peak, p, int64(got)) {
						break
					}
				}
				// Interleave reads to stress the lock.
				if c%17 == 0 {
					_ = a.Records()
				}
			}
		}(g)
	}
	wg.Wait()

	if p := atomic.LoadInt64(&peak); p > reserveMax {
		t.Fatalf("CONCURRENT OVER-RESERVE: peak observed reservation %d exceeds max %d", p, reserveMax)
	}
	recs := a.Records()
	want := goroutines * callsEach
	if len(recs) != want {
		t.Fatalf("TORN LOG: Records() has %d entries, want %d (lost appends under concurrency)",
			len(recs), want)
	}
	t.Logf("concurrent: %d records intact, peak reservation %d (<= max %d)",
		len(recs), atomic.LoadInt64(&peak), reserveMax)
}

// TestRecordedCapacityMatchesReturn asserts the sink-side invariant: the value
// returned to the caller is identical to what lands in the audit log, for a
// representative load from every tier and above-water. A divergence here would
// mean the recorded "reservation" is a fiction relative to what was advised.
func TestRecordedCapacityMatchesReturn(t *testing.T) {
	a := NewArbitrator(0)
	loads := []float64{0.05, 0.30, 0.60, 0.75, 0.80, 0.81, 0.85, 0.999}
	want := make([]int, len(loads))
	for i, l := range loads {
		want[i] = a.ChutesCapacity(fmt.Sprintf("r%d", i), l, 3)
	}
	recs := a.Records()
	if len(recs) != len(loads) {
		t.Fatalf("Records() = %d entries, want %d", len(recs), len(loads))
	}
	for i, rec := range recs {
		if rec.ResolvedCapacity != want[i] {
			t.Errorf("record[%d] load=%v: ResolvedCapacity=%d, returned=%d (must match)",
				i, loads[i], rec.ResolvedCapacity, want[i])
		}
		if rec.LoadFraction != loads[i] || rec.GPUsPerNode != 3 {
			t.Errorf("record[%d]: load=%v gpus=%d, want load=%v gpus=3",
				i, rec.LoadFraction, rec.GPUsPerNode, loads[i])
		}
	}
	t.Logf("sink-side: returned reservation == recorded reservation for all %d loads", len(loads))
}
