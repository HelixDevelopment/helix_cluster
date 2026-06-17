package gpupool

// Adversarial pass 2 for pkg/gpupool. The prior pass pinned the contracts
// (HXC-1494/1496/1498), the two sort comparators' strict-weak-ordering /
// order-independence, and the stateless-no-freelist concurrency surface. This
// pass attacks two classes the prior pass did NOT cover:
//
//   (1) NUMERIC EDGE — NaN in a selection metric (Utilization / CostPerHour).
//       deviceLess used raw `!=` and `<` on these floats. A NaN makes
//       `NaN != NaN` true and `NaN < x` / `x < NaN` both false, so the
//       comparator is no longer a strict weak ordering and sort.SliceStable
//       picks an INPUT-ORDER-DEPENDENT winner. The package's headline promise
//       is "deterministic selection", so a NaN-driven order-dependent Select is
//       a REAL determinism bug. FIXED in gpupool.go via floatLess/floatEqual
//       (NaN sorts last, total order). TestAdversarial2_NaNSelectDeterministic
//       is the mutation-proof: without the fix it FAILS (multiple winners),
//       with the fix it PASSES.
//
//   (2) RETURN-VALUE MAP ALIASING (characterization / LATENT-RISK pin, NOT a
//       prod-state bug). Filter and Select return Device VALUES, so scalar
//       fields and the result slice are independent copies (proven below). But
//       Device.Labels is a map: a struct value-copy shares the map header.
//       Mutating a returned device's Labels therefore mutates the CALLER's
//       input device's Labels. This is NOT internal pool-state corruption
//       (Filter/Select are stateless; PoolManager holds no maps and exposes no
//       accessor to its provider slice), and the Filter doc only promises "the
//       input SLICE is not mutated" — which still holds. We pin the exact
//       boundary (scalar+slice isolated; Labels map shared) so any future
//       change to copy semantics is caught, and document the footgun.

import (
	"math"
	"sync"
	"testing"
)

// TestAdversarial2_NaNSelectDeterministic is the load-bearing numeric-edge
// mutation-proof. A single device carries a NaN Utilization among finite peers
// in the same tier. Under the OLD comparator (raw `!=` / `<`), NaN breaks the
// strict-weak-ordering and sort.SliceStable returns different winners for
// different input permutations of the SAME set — a determinism violation. The
// fix (floatLess/floatEqual: NaN sorts last) restores a total order, so the
// winner is identical across all permutations and equals the finite oracle
// (the least-utilization finite device, since NaN is worst).
//
// MUTATION-PROOF: revert deviceLess to `if a.Utilization != b.Utilization {
// return a.Utilization < b.Utilization }` (and likewise CostPerHour) and this
// test FAILS with ">1 distinct winner". With the fix it PASSES.
func TestAdversarial2_NaNSelectDeterministic(t *testing.T) {
	id := runUUID(t)
	s := NewPriorityScheduler()

	// NaN in Utilization. Finite peers: lo(0.10) and hi(0.90). With NaN sorted
	// last, the deterministic winner is the least finite util => "lo".
	utilFleet := []Device{
		{ID: "nan-util", Tier: TierLocal, Utilization: math.NaN(), CostPerHour: 0.01},
		{ID: "lo", Tier: TierLocal, Utilization: 0.10, CostPerHour: 5.00},
		{ID: "hi", Tier: TierLocal, Utilization: 0.90, CostPerHour: 0.50},
	}
	assertPermInvariantWinner(t, id, s, "util-NaN", utilFleet, "lo")

	// NaN in CostPerHour, with the util tier-tie forcing the cost field to be
	// consulted. Equal tier+util among the two cost devices; NaN cost sorts last
	// so the finite-cost device ("cheap") wins; "busier" has higher util and is
	// dominated before cost matters.
	costFleet := []Device{
		{ID: "nan-cost", Tier: TierLocal, Utilization: 0.30, CostPerHour: math.NaN()},
		{ID: "cheap", Tier: TierLocal, Utilization: 0.30, CostPerHour: 1.00},
		{ID: "busier", Tier: TierLocal, Utilization: 0.99, CostPerHour: 0.00},
	}
	assertPermInvariantWinner(t, id, s, "cost-NaN", costFleet, "cheap")

	// All-NaN utilization in one tier -> NaNs are mutually equal under the total
	// order, so the ID tie-break decides deterministically => "aaa".
	allNaN := []Device{
		{ID: "ccc", Tier: TierLocal, Utilization: math.NaN(), CostPerHour: 1.0},
		{ID: "aaa", Tier: TierLocal, Utilization: math.NaN(), CostPerHour: 1.0},
		{ID: "bbb", Tier: TierLocal, Utilization: math.NaN(), CostPerHour: 1.0},
	}
	assertPermInvariantWinner(t, id, s, "all-NaN-util-id-breaks", allNaN, "aaa")
}

// assertPermInvariantWinner drives every permutation of devs through Select and
// fails unless there is exactly one winner across all permutations and it equals
// want. This is the determinism sink-side check: a non-total comparator surfaces
// as multiple distinct winners here.
func assertPermInvariantWinner(t *testing.T, id string, s *PriorityScheduler, name string, devs []Device, want string) {
	t.Helper()
	winners := map[string]int{}
	permCount := 0
	permutations(len(devs), func(order []int) {
		permCount++
		got, err := s.Select(permuteDevices(devs, order))
		if err != nil {
			t.Fatalf("run=%s fleet=%s order=%v Select error: %v", id, name, order, err)
		}
		winners[got.ID]++
	})
	if len(winners) != 1 {
		t.Fatalf("run=%s fleet=%s NON-DETERMINISTIC under NaN: %d distinct winners across %d perms: %v (deviceLess is not a strict weak ordering for NaN)",
			id, name, len(winners), permCount, winners)
	}
	if winners[want] == 0 {
		t.Fatalf("run=%s fleet=%s deterministic winner is %v, want %q", id, name, winners, want)
	}
	t.Logf("run=%s fleet=%s STABLE winner=%q across %d perms (NaN total-ordered)", id, name, want, permCount)
}

// TestAdversarial2_FloatLessTotalOrder pins the helper invariants directly:
// finite-input neutrality (floatLess==<, floatEqual=====) so the fix is proven
// behavior-neutral for every valid input, and the NaN-last total-order axioms
// (irreflexive, antisymmetric, transitive over a NaN-containing sample) so the
// comparator deviceLess builds on is sound.
func TestAdversarial2_FloatLessTotalOrder(t *testing.T) {
	id := runUUID(t)
	nan := math.NaN()
	finite := []float64{-1.5, 0, 0.1, 0.105, 1, 7.0, math.MaxFloat64}

	// Finite neutrality: floatLess and floatEqual must mirror < and == exactly.
	for _, a := range finite {
		for _, b := range finite {
			if floatLess(a, b) != (a < b) {
				t.Fatalf("run=%s floatLess(%v,%v)=%v but (a<b)=%v (NOT finite-neutral)", id, a, b, floatLess(a, b), a < b)
			}
			if floatEqual(a, b) != (a == b) {
				t.Fatalf("run=%s floatEqual(%v,%v)=%v but (a==b)=%v (NOT finite-neutral)", id, a, b, floatEqual(a, b), a == b)
			}
		}
	}

	// NaN sorts strictly after every finite value, and NaN==NaN under the order.
	for _, f := range finite {
		if !floatLess(f, nan) {
			t.Fatalf("run=%s expected finite %v < NaN", id, f)
		}
		if floatLess(nan, f) {
			t.Fatalf("run=%s NaN must NOT be < finite %v", id, f)
		}
		if floatEqual(f, nan) {
			t.Fatalf("run=%s finite %v must not equal NaN", id, f)
		}
	}
	if floatLess(nan, nan) {
		t.Fatalf("run=%s floatLess(NaN,NaN) must be false (irreflexive)", id)
	}
	if !floatEqual(nan, nan) {
		t.Fatalf("run=%s floatEqual(NaN,NaN) must be true (so ID tie-break is consulted)", id)
	}

	// Trichotomy / antisymmetry over the full sample incl. NaN: for every pair,
	// exactly one of less(a,b), less(b,a), equal(a,b) holds.
	sample := append(append([]float64{}, finite...), nan)
	for _, a := range sample {
		for _, b := range sample {
			lt := floatLess(a, b)
			gt := floatLess(b, a)
			eq := floatEqual(a, b)
			n := 0
			if lt {
				n++
			}
			if gt {
				n++
			}
			if eq {
				n++
			}
			if n != 1 {
				t.Fatalf("run=%s trichotomy violated for (%v,%v): lt=%v gt=%v eq=%v", id, a, b, lt, gt, eq)
			}
		}
	}
	t.Logf("run=%s floatLess/floatEqual: finite-neutral + NaN-last total order verified", id)
}

// TestAdversarial2_FilterScalarAndSliceIsolated pins the POSITIVE isolation
// guarantee that genuinely holds: Filter returns Device VALUES, so mutating a
// returned device's scalar fields, or appending to the returned slice, leaves
// the caller's input slice and its devices' scalar fields untouched. This is
// the half of the return-isolation contract the package actually upholds.
func TestAdversarial2_FilterScalarAndSliceIsolated(t *testing.T) {
	id := runUUID(t)
	in := []Device{
		{ID: "a", Tier: TierLocal, VRAMGiB: 80, Utilization: 0.10, CostPerHour: 1.0, Labels: map[string]bool{"TEE": true}},
		{ID: "b", Tier: TierCloud, VRAMGiB: 48, Utilization: 0.20, CostPerHour: 2.0, Labels: map[string]bool{"TEE": true}},
	}
	out := Filter(WorkloadSpec{MinVRAMGiB: 40, RequireLabels: []string{"TEE"}}, in)
	if len(out) != 2 {
		t.Fatalf("run=%s precondition: expected 2 survivors, got %d", id, len(out))
	}

	// Mutate every scalar field of the output AND grow the output slice.
	for i := range out {
		out[i].ID = "MUT"
		out[i].VRAMGiB = -999
		out[i].Tier = TierDecentralized
		out[i].Utilization = 9.99
		out[i].CostPerHour = -1.0
	}
	out = append(out, Device{ID: "EXTRA"})

	// SINK-SIDE: the caller's input is byte-identical on every scalar field, and
	// its slice length is unchanged.
	want := []Device{
		{ID: "a", Tier: TierLocal, VRAMGiB: 80, Utilization: 0.10, CostPerHour: 1.0},
		{ID: "b", Tier: TierCloud, VRAMGiB: 48, Utilization: 0.20, CostPerHour: 2.0},
	}
	if len(in) != 2 {
		t.Fatalf("run=%s input slice length changed to %d (append leaked)", id, len(in))
	}
	for i := range want {
		if in[i].ID != want[i].ID || in[i].VRAMGiB != want[i].VRAMGiB || in[i].Tier != want[i].Tier ||
			in[i].Utilization != want[i].Utilization || in[i].CostPerHour != want[i].CostPerHour {
			t.Fatalf("run=%s input device[%d] scalar fields corrupted by output mutation: got %+v want %+v",
				id, i, in[i], want[i])
		}
	}
	t.Logf("run=%s Filter return scalars + slice are isolated from input (value-copy upheld)", id)
}

// TestAdversarial2_LabelsMapAliasingDocumented characterizes (pins) the ONE
// place isolation does NOT hold: Device.Labels is a map and is shared by value-
// copy between input and the Filter/Select result. This is documented LATENT
// RISK, not a pool-state bug — both the input and the output are caller-owned,
// Filter/Select keep no internal state, and the Filter doc only promises the
// input SLICE is unmutated (it is). We assert the CURRENT shared-map behavior so
// a future change to deep-copy semantics is a deliberate, test-visible decision.
//
// CALLER GUIDANCE (the footgun): a caller that mutates Labels on a device
// returned by Filter/Select will mutate the corresponding input device's Labels.
// Callers needing an independent Labels map must clone it themselves.
func TestAdversarial2_LabelsMapAliasingDocumented(t *testing.T) {
	id := runUUID(t)

	// --- Filter path ---
	in := []Device{{ID: "a", Tier: TierLocal, VRAMGiB: 80, Labels: map[string]bool{"TEE": true}}}
	out := Filter(WorkloadSpec{MinVRAMGiB: 40, RequireLabels: []string{"TEE"}}, in)
	if len(out) != 1 {
		t.Fatalf("run=%s precondition: expected 1 survivor", id)
	}
	out[0].Labels["TEE"] = false
	out[0].Labels["INJECTED"] = true
	// Documented current behavior: the input device's map reflects the mutation.
	if in[0].Labels["TEE"] {
		t.Fatalf("run=%s CONTRACT DRIFT: Filter now deep-copies Labels (input TEE survived). "+
			"If this is intentional, update the doc + this pin.", id)
	}
	if !in[0].Labels["INJECTED"] {
		t.Fatalf("run=%s CONTRACT DRIFT: injected key not visible on input — Labels no longer aliased. "+
			"If Filter now clones Labels, update the doc + this pin.", id)
	}

	// --- Select path ---
	sin := []Device{{ID: "s", Tier: TierLocal, Utilization: 0.1, Labels: map[string]bool{"TEE": true}}}
	got, err := NewPriorityScheduler().Select(sin)
	if err != nil {
		t.Fatalf("run=%s Select error: %v", id, err)
	}
	got.Labels["TEE"] = false
	if sin[0].Labels["TEE"] {
		t.Fatalf("run=%s CONTRACT DRIFT: Select now deep-copies Labels. Update the doc + this pin.", id)
	}
	t.Logf("run=%s LATENT RISK pinned: Filter/Select share Device.Labels map with input (value-copy). "+
		"Callers must clone Labels before mutating returned devices.", id)
}

// TestAdversarial2_NaNNoRaceUnderConcurrency runs the NaN-containing Select
// across many goroutines on ONE shared slice to confirm the fix introduces no
// data race and the winner stays the single deterministic value under -race.
// Bounded iterations; no blocking ops -> cannot hang.
func TestAdversarial2_NaNNoRaceUnderConcurrency(t *testing.T) {
	id := runUUID(t)
	s := NewPriorityScheduler()
	devs := []Device{
		{ID: "nan", Tier: TierLocal, Utilization: math.NaN(), CostPerHour: 0.01},
		{ID: "lo", Tier: TierLocal, Utilization: 0.10, CostPerHour: 5.00},
		{ID: "hi", Tier: TierLocal, Utilization: 0.90, CostPerHour: 0.50},
	}
	const goroutines = 64
	const perG = 100
	out := make([]string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			last := ""
			for i := 0; i < perG; i++ {
				sel, err := s.Select(devs) // shared input, concurrent read
				if err != nil {
					last = "ERR"
					continue
				}
				last = sel.ID
			}
			out[g] = last
		}(g)
	}
	wg.Wait()
	for g, r := range out {
		if r != "lo" {
			t.Fatalf("run=%s goroutine %d Select=%q under NaN, want deterministic %q", id, g, r, "lo")
		}
	}
	t.Logf("run=%s %d goroutines x %d NaN-Select all => %q; -race clean if no DATA RACE above", id, goroutines, perG, "lo")
}
