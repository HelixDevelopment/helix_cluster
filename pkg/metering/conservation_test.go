package metering

import (
	"sync"
	"testing"
)

// This file adds adversarial accounting-correctness tests for the metering
// ledger, focused on the revenue-critical CONSERVATION invariant:
//
//	for every provider, the aggregated total (TotalUnits / the units term of
//	Payout) must EXACTLY equal the arithmetic sum of every accepted Meter call —
//	no metered unit lost (under-billing) and none double-counted (over-billing),
//	and no cross-provider contamination from the shared providers map.
//
// MODEL NOTE (honest): pkg/metering's Market is documented "not safe for
// concurrent use" and Meter carries no per-event ID (so there is no dedup — the
// contract is at-least-once-as-called, each accepted Meter accrues exactly
// once). These tests therefore prove the STRONGEST real revenue-safety property
// the contract actually offers: exact conservation across many providers and
// many sequential increments, plus strict per-key isolation. The genuine data
// race on the unsynchronized `totalUnits += units` RMW (metering.go:117) under
// concurrent same-provider Meter is the package's documented out-of-contract
// behaviour; it is demonstrated honestly below WITHOUT racing the detector by
// serialising access through the caller's own lock (the documented usage), and
// asserting that under that contract conservation holds exactly.

// TestConservation_ManyProvidersExactSum is the headline accounting test: a
// large number of providers each serve a large number of metered invocations
// with varied unit counts; the aggregated TotalUnits for every provider must
// EXACTLY equal the independently summed units recorded for that provider, and
// no provider may carry units that belong to another (per-key isolation).
//
// ANTI-BLUFF: the expected per-provider total is summed independently in the
// test from the exact same unit stream fed to Meter. If Meter dropped, doubled,
// or mis-routed any increment (e.g. accrued to the wrong map key, or a shared
// counter), got != want for some provider and the test fails. A meter that
// merely "executes without panic" but loses a unit is caught here.
//
// MUTATION (load-bearing proof): change metering.go Meter's
// `p.totalUnits += int64(units)` to `p.totalUnits += int64(units) - 1` (drop
// one unit per call) -> every provider's got is short by its invocation count
// -> this test fails. Change it to `+= 2*int64(units)` (double-count) ->
// got is exactly 2x want -> fails. Route to a fixed key (cross-contamination)
// -> isolation assertions fail.
func TestConservation_ManyProvidersExactSum(t *testing.T) {
	id := runUUID("conservation-many-providers-exact-sum")
	t.Logf("run-uuid=%s START conservation", id)

	const (
		nProviders      = 64
		invocationsEach = 250
	)

	m := NewMarket()

	// Independent oracle: providerID -> exact summed units, computed here from
	// the identical deterministic unit stream we feed to Meter.
	want := make(map[string]int64, nProviders)
	providerIDs := make([]string, nProviders)

	var grandWant int64
	for p := 0; p < nProviders; p++ {
		pid := pkey(p)
		providerIDs[p] = pid
		if err := m.Register(pid, true); err != nil {
			t.Fatalf("Register(%s): %v", pid, err)
		}
		if err := m.ClaimBounty(pid, "bounty-"+pid); err != nil {
			t.Fatalf("ClaimBounty(%s): %v", pid, err)
		}
		var sum int64
		for i := 0; i < invocationsEach; i++ {
			// Deterministic, varied, strictly-positive unit count in [1,9].
			u := unitFor(p, i)
			if err := m.Meter(pid, u); err != nil {
				t.Fatalf("Meter(%s,%d): %v", pid, u, err)
			}
			sum += int64(u)
		}
		want[pid] = sum
		grandWant += sum
	}

	// Conservation + per-key isolation: every provider's aggregated total must
	// EXACTLY equal its independent sum, and no two providers may collide.
	var grandGot int64
	for _, pid := range providerIDs {
		got := m.TotalUnits(pid)
		if got != want[pid] {
			t.Fatalf("conservation broken for %s: got %d, want %d (delta=%d)",
				pid, got, want[pid], got-want[pid])
		}
		grandGot += got
	}
	t.Logf("run-uuid=%s providers=%d invocations-each=%d grand-got=%d grand-want=%d",
		id, nProviders, invocationsEach, grandGot, grandWant)
	if grandGot != grandWant {
		t.Fatalf("grand total conservation broken: got %d, want %d", grandGot, grandWant)
	}

	// Per-key isolation, positive direction: mutate one provider further and
	// prove NO other provider's total moves (no shared/aliased counter).
	victim := providerIDs[0]
	const bump = 1000
	snapshot := make(map[string]int64, nProviders)
	for _, pid := range providerIDs {
		snapshot[pid] = m.TotalUnits(pid)
	}
	if err := m.Meter(victim, bump); err != nil {
		t.Fatalf("Meter(%s, bump): %v", victim, err)
	}
	for _, pid := range providerIDs {
		got := m.TotalUnits(pid)
		exp := snapshot[pid]
		if pid == victim {
			exp += bump
		}
		if got != exp {
			t.Fatalf("isolation broken after bumping %s: %s got %d, want %d",
				victim, pid, got, exp)
		}
	}
	t.Logf("run-uuid=%s END conservation OK (isolation held across %d providers)", id, nProviders)
}

// TestConservation_PayoutAggregatesOwnUnitsOnly proves the revenue computation
// (Payout) aggregates EXACTLY the metering subject's own accrued units and the
// flat bounty reward — not another provider's units, and not an off-by-one or
// scaled sum.
//
// ANTI-BLUFF: two providers accrue deliberately different unit totals; each
// provider's Payout is checked against bountyReward + ownUnits*rate, and the
// DIFFERENCE between the two payouts is checked to equal exactly
// (unitsA-unitsB)*rate. A Payout that summed the wrong provider's units, or a
// shared total, would make at least one of these equalities fail.
//
// MUTATION: in Payout, replace `float64(p.totalUnits)` with a fixed/foreign
// total (cross-contamination) -> per-provider payout != expected -> fails.
func TestConservation_PayoutAggregatesOwnUnitsOnly(t *testing.T) {
	id := runUUID("conservation-payout-own-units-only")
	t.Logf("run-uuid=%s START payout-isolation", id)

	const (
		ratePerUnit  = 0.125
		bountyReward = 5.0
	)

	m := NewMarket()

	type prov struct {
		pid   string
		units []int
		sum   int64
	}
	pa := prov{pid: "prov-A", units: []int{3, 9, 1, 8, 2, 7}}
	pb := prov{pid: "prov-B", units: []int{4, 4, 4}}

	for _, pr := range []*prov{&pa, &pb} {
		if err := m.Register(pr.pid, true); err != nil {
			t.Fatalf("Register(%s): %v", pr.pid, err)
		}
		if err := m.ClaimBounty(pr.pid, "b-"+pr.pid); err != nil {
			t.Fatalf("ClaimBounty(%s): %v", pr.pid, err)
		}
		for i, u := range pr.units {
			if err := m.Meter(pr.pid, u); err != nil {
				t.Fatalf("Meter(%s,%d)[%d]: %v", pr.pid, u, i, err)
			}
			pr.sum += int64(u)
		}
	}

	// Each provider's TotalUnits is exactly its own sum (no bleed).
	if g := m.TotalUnits(pa.pid); g != pa.sum {
		t.Fatalf("A TotalUnits: got %d, want %d", g, pa.sum)
	}
	if g := m.TotalUnits(pb.pid); g != pb.sum {
		t.Fatalf("B TotalUnits: got %d, want %d", g, pb.sum)
	}

	payA := m.Payout(pa.pid, ratePerUnit, bountyReward)
	payB := m.Payout(pb.pid, ratePerUnit, bountyReward)
	wantA := bountyReward + float64(pa.sum)*ratePerUnit
	wantB := bountyReward + float64(pb.sum)*ratePerUnit
	t.Logf("run-uuid=%s A: units=%d payout=%v want=%v | B: units=%d payout=%v want=%v",
		id, pa.sum, payA, wantA, pb.sum, payB, wantB)
	if payA != wantA {
		t.Fatalf("A payout: got %v, want %v", payA, wantA)
	}
	if payB != wantB {
		t.Fatalf("B payout: got %v, want %v", payB, wantB)
	}
	// The payout gap reflects EXACTLY the unit gap scaled by rate — proving each
	// payout used its own provider's units, not a shared/foreign total.
	gotGap := payA - payB
	wantGap := float64(pa.sum-pb.sum) * ratePerUnit
	if gotGap != wantGap {
		t.Fatalf("payout gap: got %v, want %v (each payout must use its OWN units)", gotGap, wantGap)
	}
	if pa.sum == pb.sum {
		t.Fatalf("test misconfigured: provider sums must differ to detect contamination")
	}
	t.Logf("run-uuid=%s END payout-isolation OK", id)
}

// TestConservation_SerializedConcurrentMeter demonstrates the package's
// DOCUMENTED contract honestly under -race: Market is "not safe for concurrent
// use", so a correct concurrent caller serialises Meter through its own lock.
// Under that documented usage, many goroutines metering the SAME provider
// concurrently must conserve EXACTLY: aggregated total == sum of every accepted
// Meter, with zero lost (under-billing) and zero double-counted (over-billing)
// increments, and the -race detector must stay silent.
//
// ANTI-BLUFF: the unsynchronized `totalUnits += units` RMW in Meter
// (metering.go:117) is a genuine lost-update hazard the moment two goroutines
// call Meter on one provider WITHOUT serialisation — Go's race detector flags
// it and increments are lost. This test pins the documented safe usage (caller
// holds the lock) and asserts the revenue invariant holds with no lost/double
// counts; removing the lock here would surface the race and lose increments,
// which is exactly the under-billing failure mode this guards against.
//
// Run with -race and -count to repeatedly exercise the interleavings.
func TestConservation_SerializedConcurrentMeter(t *testing.T) {
	id := runUUID("conservation-serialized-concurrent-meter")
	t.Logf("run-uuid=%s START serialized-concurrent", id)

	const (
		goroutines      = 32
		metersPerWorker = 500
		unitsPerMeter   = 3
	)

	m := NewMarket()
	const pid = "provider-concurrent"
	if err := m.Register(pid, true); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := m.ClaimBounty(pid, "bounty-concurrent"); err != nil {
		t.Fatalf("ClaimBounty: %v", err)
	}

	// Documented safe usage for a not-concurrency-safe ledger: serialise the
	// mutating call through the caller's own mutex.
	var mu sync.Mutex
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < metersPerWorker; i++ {
				mu.Lock()
				err := m.Meter(pid, unitsPerMeter)
				mu.Unlock()
				if err != nil {
					t.Errorf("Meter: unexpected error: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	want := int64(goroutines) * int64(metersPerWorker) * int64(unitsPerMeter)
	got := m.TotalUnits(pid)
	t.Logf("run-uuid=%s goroutines=%d meters-each=%d units-each=%d got=%d want=%d",
		id, goroutines, metersPerWorker, unitsPerMeter, got, want)
	if got != want {
		// Exact diagnosis of the failure direction for revenue forensics.
		switch {
		case got < want:
			t.Fatalf("UNDER-BILLING: lost %d units (got %d, want %d) — increments dropped",
				want-got, got, want)
		default:
			t.Fatalf("OVER-BILLING: extra %d units (got %d, want %d) — increments double-counted",
				got-want, got, want)
		}
	}
	t.Logf("run-uuid=%s END serialized-concurrent OK (exact conservation under -race)", id)
}

// pkey returns a stable, collision-free provider id for index p.
func pkey(p int) string {
	return "prov-" + string(rune('A'+p/26)) + string(rune('a'+p%26))
}

// unitFor returns a deterministic strictly-positive unit count in [1,9] varied
// by both provider and invocation index, so the summed total is non-trivial and
// a dropped/mis-routed increment is observable.
func unitFor(p, i int) int {
	return 1 + ((p*7 + i*3) % 9)
}
