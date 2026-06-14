package gpupool

import (
	"sort"
	"testing"
)

// This file is the order-independence + selection-oracle audit for the two
// sort comparators in this package:
//
//	deviceLess           (used by PriorityScheduler.Select) — Tier asc, then
//	                     Utilization asc, then CostPerHour asc, then ID asc.
//	Allocate's SliceStable comparator — Tier asc, then FreeCapacity desc,
//	                     then ID asc.
//
// TRANSITIVITY VERDICT (both comparators): STRICT WEAK ORDERING (transitive).
// Each comparator is a lexicographic chain of ABSOLUTE per-element field
// comparisons with a total ID tie-break. There is NO pairwise relative "a and
// b are within tolerance of EACH OTHER" band anywhere — utilization equality is
// EXACT (`a.Utilization != b.Utilization`), not a tolerance band. Therefore the
// qos-class intransitivity (a pairwise near-tie band that makes sort.Slice pick
// an order-dependent winner) is ABSENT here. These tests PROVE that: the chosen
// element is identical across every input permutation, and equals an oracle that
// is computed WITHOUT sorting (so it can never share a sort bug).
//
// WHY EACH ASSERTION BITES:
//   - Order-independence (permuteSelect / permuteAllocate): if deviceLess (or
//     the Allocate comparator) were ever changed to a pairwise band — e.g.
//     `if withinFrac(a.Util, b.Util, frac) { return a.Cost < b.Cost }` — the
//     relation would become intransitive and sort.SliceStable would yield
//     different winners for different input orders on the near-tie CHAIN fleets
//     below; this test would then observe >1 distinct winner and FAIL. (Verified
//     load-bearing by the mutation experiment recorded in the report.)
//   - Oracle equality (oracleSelect / oracleAllocate): the oracle independently
//     computes the documented objective (tier > util > cost > id ; and tier >
//     freecap-desc > id) via a min-scan, no sort. If the comparator's field
//     order were flipped (e.g. cost before util, or freecap asc), the winner
//     would diverge from the oracle and FAIL.

// ---------- independent oracles (NO sort.* — pure min-scan) ----------

// oracleSelect returns the documented winner of PriorityScheduler.Select using
// a linear min-scan over the EXACT (tier, util, cost, id) lexicographic key.
// It deliberately does not call sort, so it cannot share any sort-comparator
// defect.
func oracleSelect(devs []Device) (Device, bool) {
	if len(devs) == 0 {
		return Device{}, false
	}
	best := devs[0]
	for _, d := range devs[1:] {
		if oracleDeviceLess(d, best) {
			best = d
		}
	}
	return best, true
}

// oracleDeviceLess mirrors the DOCUMENTED objective independently of deviceLess.
func oracleDeviceLess(a, b Device) bool {
	if a.Tier != b.Tier {
		return a.Tier < b.Tier
	}
	if a.Utilization != b.Utilization {
		return a.Utilization < b.Utilization
	}
	if a.CostPerHour != b.CostPerHour {
		return a.CostPerHour < b.CostPerHour
	}
	return a.ID < b.ID
}

// oracleAllocate returns the documented winning provider of PoolManager.Allocate
// for a given need: the HEALTHY provider with the best (tier asc, then
// FreeCapacity desc, then ID asc) key whose FreeCapacity >= need. Linear scan,
// no sort.
func oracleAllocate(provs []Provider, need int) (Provider, bool) {
	var best Provider
	found := false
	for _, p := range provs {
		if !p.Healthy || p.FreeCapacity < need {
			continue
		}
		if !found || oracleProviderLess(p, best) {
			best = p
			found = true
		}
	}
	return best, found
}

// oracleProviderLess mirrors the documented Allocate ordering independently.
func oracleProviderLess(a, b Provider) bool {
	if a.Tier != b.Tier {
		return a.Tier < b.Tier
	}
	if a.FreeCapacity != b.FreeCapacity {
		return a.FreeCapacity > b.FreeCapacity
	}
	return a.ID < b.ID
}

// ---------- permutation generator ----------

// permutations invokes fn with every permutation of indices [0..n). For n up to
// 7 this is at most 5040 permutations — exhaustive and cheap, and the most
// adversarial possible coverage for an order-dependence bug.
func permutations(n int, fn func(order []int)) {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	var rec func(k int)
	rec = func(k int) {
		if k == n {
			cp := make([]int, n)
			copy(cp, idx)
			fn(cp)
			return
		}
		for i := k; i < n; i++ {
			idx[k], idx[i] = idx[i], idx[k]
			rec(k + 1)
			idx[k], idx[i] = idx[i], idx[k]
		}
	}
	rec(0)
}

func permuteDevices(src []Device, order []int) []Device {
	out := make([]Device, len(order))
	for i, p := range order {
		out[i] = src[p]
	}
	return out
}

func permuteProviders(src []Provider, order []int) []Provider {
	out := make([]Provider, len(order))
	for i, p := range order {
		out[i] = src[p]
	}
	return out
}

// ---------- Select: order-independence + oracle across many fleets ----------

// TestSelect_OrderIndependenceAndOracle is the core bite. For each fleet — many
// of which are NEAR-TIE CHAINS deliberately engineered to expose intransitivity
// (a chain a~b~c~d where adjacent utils are within a hypothetical band but the
// endpoints are not) — it drives EVERY input permutation through Select and
// asserts:
//  1. The chosen device ID is identical across all permutations (the winner is
//     order-independent). A pairwise-band comparator would FAIL here on the
//     chain fleets.
//  2. The chosen device equals the independent no-sort oracle (objective
//     correctness). A field-order-flipped comparator would FAIL here.
func TestSelect_OrderIndependenceAndOracle(t *testing.T) {
	id := runUUID(t)
	type fleet struct {
		name string
		devs []Device
	}
	fleets := []fleet{
		{
			// Pure near-tie CHAIN on utilization within one tier: 0.10, 0.105,
			// 0.11, 0.115 — every adjacent pair is within 5%, but the endpoints
			// (0.10 vs 0.115) are NOT. A pairwise band keyed on cost would make
			// the winner depend on which order the chain is sorted in. With EXACT
			// equality (current code) the strictly-least util (0.10 -> "u100")
			// always wins regardless of cost.
			name: "util-near-tie-chain-single-tier",
			devs: []Device{
				{ID: "u100", Tier: TierLocal, Utilization: 0.100, CostPerHour: 9.00},
				{ID: "u105", Tier: TierLocal, Utilization: 0.105, CostPerHour: 1.00},
				{ID: "u110", Tier: TierLocal, Utilization: 0.110, CostPerHour: 0.50},
				{ID: "u115", Tier: TierLocal, Utilization: 0.115, CostPerHour: 0.10},
			},
		},
		{
			// Same chain but spanning tiers: the better tier must dominate the
			// near-tie utilization entirely. Lowest tier wins even though a
			// lower-tier device is far cheaper / less utilized.
			name: "util-chain-cross-tier-tier-dominates",
			devs: []Device{
				{ID: "cloud-idle-cheap", Tier: TierCloud, Utilization: 0.01, CostPerHour: 0.01},
				{ID: "local-a", Tier: TierLocal, Utilization: 0.40, CostPerHour: 8.00},
				{ID: "local-b", Tier: TierLocal, Utilization: 0.405, CostPerHour: 0.20},
				{ID: "local-c", Tier: TierLocal, Utilization: 0.41, CostPerHour: 0.05},
			},
		},
		{
			// Exact-equal utilization within tier -> cost must break the tie.
			// This is the documented "util ignored when equal" path. Cheapest of
			// the equal-util group wins.
			name: "exact-equal-util-cost-tiebreak",
			devs: []Device{
				{ID: "eq-pricey", Tier: TierLocal, Utilization: 0.30, CostPerHour: 9.00},
				{ID: "eq-mid", Tier: TierLocal, Utilization: 0.30, CostPerHour: 4.00},
				{ID: "eq-cheap", Tier: TierLocal, Utilization: 0.30, CostPerHour: 1.00},
				{ID: "busier", Tier: TierLocal, Utilization: 0.31, CostPerHour: 0.01},
			},
		},
		{
			// Full triple tie (tier+util+cost identical) across several devices
			// -> ID must be the deterministic final tie-break, order-independent.
			name: "full-triple-tie-id-breaks",
			devs: []Device{
				{ID: "ddd", Tier: TierLocal, Utilization: 0.5, CostPerHour: 2.0},
				{ID: "aaa", Tier: TierLocal, Utilization: 0.5, CostPerHour: 2.0},
				{ID: "ccc", Tier: TierLocal, Utilization: 0.5, CostPerHour: 2.0},
				{ID: "bbb", Tier: TierLocal, Utilization: 0.5, CostPerHour: 2.0},
			},
		},
		{
			// Mixed tiers, mixed utils, some cost ties — general fleet.
			name: "mixed-general",
			devs: []Device{
				{ID: "g-cloud-lowutil", Tier: TierCloud, Utilization: 0.02, CostPerHour: 0.10},
				{ID: "g-rp-mid", Tier: TierRemoteProxy, Utilization: 0.50, CostPerHour: 2.00},
				{ID: "g-local-hi", Tier: TierLocal, Utilization: 0.90, CostPerHour: 7.00},
				{ID: "g-local-lo", Tier: TierLocal, Utilization: 0.20, CostPerHour: 7.00},
				{ID: "g-dec", Tier: TierDecentralized, Utilization: 0.00, CostPerHour: 0.00},
			},
		},
	}

	for _, fl := range fleets {
		fl := fl
		t.Run(fl.name, func(t *testing.T) {
			want, ok := oracleSelect(fl.devs)
			if !ok {
				t.Fatalf("run=%s fleet=%s oracle found no device for non-empty fleet", id, fl.name)
			}
			s := NewPriorityScheduler()

			winners := map[string]int{}
			permCount := 0
			permutations(len(fl.devs), func(order []int) {
				permCount++
				perm := permuteDevices(fl.devs, order)
				got, err := s.Select(perm)
				if err != nil {
					t.Fatalf("run=%s fleet=%s order=%v Select error: %v", id, fl.name, order, err)
				}
				winners[got.ID]++
				// Objective check on EVERY permutation: chosen == oracle.
				if got.ID != want.ID {
					t.Fatalf("run=%s fleet=%s order=%v Select chose %q but oracle says %q (objective tier>util>cost>id violated)",
						id, fl.name, order, got.ID, want.ID)
				}
			})

			if len(winners) != 1 {
				t.Fatalf("run=%s fleet=%s ORDER-DEPENDENT: %d distinct winners across %d permutations: %v (comparator is intransitive)",
					id, fl.name, len(winners), permCount, winners)
			}
			t.Logf("run=%s fleet=%s STABLE winner=%q across %d permutations (oracle-agreed)",
				id, fl.name, want.ID, permCount)
		})
	}
}

// TestSelect_FullOrderingOrderIndependence goes beyond the single winner: it
// asserts the ENTIRE sorted order is permutation-invariant for a near-tie chain
// fleet. An intransitive comparator can produce a stable top-1 by luck on some
// fleets but will scramble the full ranking; this catches that broader class.
func TestSelect_FullOrderingOrderIndependence(t *testing.T) {
	id := runUUID(t)
	base := []Device{
		{ID: "a", Tier: TierLocal, Utilization: 0.20, CostPerHour: 5.0},
		{ID: "b", Tier: TierLocal, Utilization: 0.205, CostPerHour: 1.0},
		{ID: "c", Tier: TierLocal, Utilization: 0.21, CostPerHour: 0.5},
		{ID: "d", Tier: TierCloud, Utilization: 0.01, CostPerHour: 0.01},
		{ID: "e", Tier: TierLocal, Utilization: 0.20, CostPerHour: 2.0},
	}

	fullSort := func(devs []Device) []string {
		cp := make([]Device, len(devs))
		copy(cp, devs)
		sort.SliceStable(cp, func(i, j int) bool { return deviceLess(cp[i], cp[j]) })
		ids := make([]string, len(cp))
		for i, d := range cp {
			ids[i] = d.ID
		}
		return ids
	}

	var canonical []string
	permCount := 0
	permutations(len(base), func(order []int) {
		permCount++
		got := fullSort(permuteDevices(base, order))
		if canonical == nil {
			canonical = got
			return
		}
		if len(got) != len(canonical) {
			t.Fatalf("run=%s length mismatch", id)
		}
		for i := range got {
			if got[i] != canonical[i] {
				t.Fatalf("run=%s ORDER-DEPENDENT full ranking: order=%v gave %v but canonical=%v at pos %d",
					id, order, got, canonical, i)
			}
		}
	})
	t.Logf("run=%s full ranking %v stable across %d permutations", id, canonical, permCount)
}

// ---------- Allocate: order-independence + oracle ----------

// TestAllocate_OrderIndependenceAndOracle drives many provider fleets through
// every permutation and asserts the allocated provider is identical every time
// and equals the no-sort oracle. Allocate's comparator (tier asc, freecap desc,
// id asc) is a lexicographic total order; this proves it.
func TestAllocate_OrderIndependenceAndOracle(t *testing.T) {
	id := runUUID(t)
	type fleet struct {
		name  string
		provs []Provider
		need  int
	}
	fleets := []fleet{
		{
			// Same-tier freecap tie chain + an ID tie-break group; best tier with
			// largest free wins, ID breaks exact-free ties.
			name: "same-tier-freecap-and-id",
			provs: []Provider{
				{ID: "p-free8", Tier: TierLocal, Capacity: 8, FreeCapacity: 8, Healthy: true},
				{ID: "p-free4-b", Tier: TierLocal, Capacity: 8, FreeCapacity: 4, Healthy: true},
				{ID: "p-free4-a", Tier: TierLocal, Capacity: 8, FreeCapacity: 4, Healthy: true},
				{ID: "p-free2", Tier: TierLocal, Capacity: 8, FreeCapacity: 2, Healthy: true},
			},
			need: 2,
		},
		{
			// Better tier is empty -> must be skipped for a healthy lower tier.
			name: "better-tier-empty-skipped",
			provs: []Provider{
				{ID: "local-empty", Tier: TierLocal, Capacity: 8, FreeCapacity: 0, Healthy: true},
				{ID: "rp-some", Tier: TierRemoteProxy, Capacity: 8, FreeCapacity: 3, Healthy: true},
				{ID: "cloud-lots", Tier: TierCloud, Capacity: 16, FreeCapacity: 16, Healthy: true},
			},
			need: 2,
		},
		{
			// Unhealthy best-tier-large provider must be ignored even though it
			// has the most free capacity.
			name: "unhealthy-best-ignored",
			provs: []Provider{
				{ID: "local-big-DOWN", Tier: TierLocal, Capacity: 32, FreeCapacity: 32, Healthy: false},
				{ID: "local-small-up", Tier: TierLocal, Capacity: 8, FreeCapacity: 5, Healthy: true},
				{ID: "cloud-up", Tier: TierCloud, Capacity: 8, FreeCapacity: 8, Healthy: true},
			},
			need: 4,
		},
		{
			// Cross-tier with mixed health and freecap.
			name: "cross-tier-mixed",
			provs: []Provider{
				{ID: "a-dec", Tier: TierDecentralized, Capacity: 64, FreeCapacity: 64, Healthy: true},
				{ID: "b-cloud", Tier: TierCloud, Capacity: 16, FreeCapacity: 1, Healthy: true},
				{ID: "c-cloud", Tier: TierCloud, Capacity: 16, FreeCapacity: 9, Healthy: true},
				{ID: "d-rp", Tier: TierRemoteProxy, Capacity: 8, FreeCapacity: 0, Healthy: true},
			},
			need: 3,
		},
	}

	for _, fl := range fleets {
		fl := fl
		t.Run(fl.name, func(t *testing.T) {
			wantP, wantOK := oracleAllocate(fl.provs, fl.need)
			winners := map[string]int{}
			permCount := 0
			permutations(len(fl.provs), func(order []int) {
				permCount++
				perm := permuteProviders(fl.provs, order)
				m := NewPoolManager(perm)
				got, err := m.Allocate(WorkloadSpec{GPUCount: fl.need})
				if wantOK {
					if err != nil {
						t.Fatalf("run=%s fleet=%s order=%v unexpected Allocate error: %v (oracle expected %q)",
							id, fl.name, order, err, wantP.ID)
					}
					winners[got.ID]++
					if got.ID != wantP.ID {
						t.Fatalf("run=%s fleet=%s order=%v Allocate chose %q but oracle says %q (tier>freecap-desc>id violated)",
							id, fl.name, order, got.ID, wantP.ID)
					}
				} else {
					if err == nil {
						t.Fatalf("run=%s fleet=%s order=%v Allocate returned %q but oracle expected none",
							id, fl.name, order, got.ID)
					}
				}
			})
			if wantOK {
				if len(winners) != 1 {
					t.Fatalf("run=%s fleet=%s ORDER-DEPENDENT allocation: %d distinct winners across %d perms: %v",
						id, fl.name, len(winners), permCount, winners)
				}
				t.Logf("run=%s fleet=%s STABLE allocation=%q across %d permutations (oracle-agreed)",
					id, fl.name, wantP.ID, permCount)
			}
		})
	}
}

// ---------- edge cases ----------

// TestSelect_EdgeCases covers single, all-equal, and empty candidate sets for
// the documented no-panic behavior.
func TestSelect_EdgeCases(t *testing.T) {
	id := runUUID(t)
	s := NewPriorityScheduler()

	// single
	one := []Device{{ID: "solo", Tier: TierLocal, Utilization: 0.4, CostPerHour: 1.0}}
	got, err := s.Select(one)
	if err != nil || got.ID != "solo" {
		t.Fatalf("run=%s single: got %q err=%v, want solo", id, got.ID, err)
	}

	// all-equal except ID -> lexicographically smallest ID, order-independent.
	allEq := []Device{
		{ID: "m", Tier: TierLocal, Utilization: 0.5, CostPerHour: 2.0},
		{ID: "z", Tier: TierLocal, Utilization: 0.5, CostPerHour: 2.0},
		{ID: "a", Tier: TierLocal, Utilization: 0.5, CostPerHour: 2.0},
	}
	winners := map[string]bool{}
	permutations(len(allEq), func(order []int) {
		g, e := s.Select(permuteDevices(allEq, order))
		if e != nil {
			t.Fatalf("run=%s all-equal Select error: %v", id, e)
		}
		winners[g.ID] = true
	})
	if len(winners) != 1 || !winners["a"] {
		t.Fatalf("run=%s all-equal: expected sole winner 'a', got %v", id, winners)
	}

	// empty
	if _, e := s.Select(nil); e == nil {
		t.Fatalf("run=%s empty: expected error, got nil", id)
	}
	t.Logf("run=%s edge cases OK (single, all-equal->'a', empty->err)", id)
}

// TestAllocate_EdgeCases covers single provider, empty set, and all-unhealthy.
func TestAllocate_EdgeCases(t *testing.T) {
	id := runUUID(t)

	// single healthy sufficient
	m := NewPoolManager([]Provider{{ID: "solo", Tier: TierLocal, Capacity: 4, FreeCapacity: 4, Healthy: true}})
	got, err := m.Allocate(WorkloadSpec{GPUCount: 2})
	if err != nil || got.ID != "solo" {
		t.Fatalf("run=%s single: got %q err=%v", id, got.ID, err)
	}

	// empty providers
	if _, e := NewPoolManager(nil).Allocate(WorkloadSpec{GPUCount: 1}); e == nil {
		t.Fatalf("run=%s empty providers: expected ErrNoProvider, got nil", id)
	}

	// all unhealthy
	if _, e := NewPoolManager([]Provider{
		{ID: "x", Tier: TierLocal, FreeCapacity: 8, Healthy: false},
	}).Allocate(WorkloadSpec{GPUCount: 1}); e == nil {
		t.Fatalf("run=%s all-unhealthy: expected ErrNoProvider, got nil", id)
	}
	t.Logf("run=%s allocate edge cases OK", id)
}
