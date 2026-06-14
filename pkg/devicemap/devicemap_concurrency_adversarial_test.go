package devicemap

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/HelixDevelopment/helix_cluster/pkg/device"
)

// devicemap_concurrency_adversarial_test.go — concurrency-contract pin.
//
// CONTEXT / FINDING (honest three-way discrimination per the brief):
//
// The brief hypothesised pkg/devicemap as a "shared mutable device map /
// registration / allocation layer" carrying the costtracker/helixnet bug
// class: an unguarded shared map (data race + lost update), over-commit /
// double-allocation, duplicate-ID clobber, and release asymmetry.
//
// SINK-SIDE REALITY (read map.go in full): pkg/devicemap holds NONE of that.
// It is a PURE, STATELESS conversion layer between device.DeviceDescriptor and
// *helixv1.Node. There is:
//
//   - no package-level variable of any kind (the only `var` in the package is
//     a function-local block inside NodeToDescriptor),
//   - no map, no slice field, no sync.Mutex / sync.RWMutex / atomic,
//   - no goroutine, no channel,
//   - no register / allocate / release / lookup, no capacity, no state machine,
//   - no device-ID registry, no allocation accounting.
//
// DescriptorToNode takes a Descriptor BY VALUE and returns a freshly-allocated
// *Node (every &helixv1.X{} is a new heap object). NodeToDescriptor reads
// only the *Node the caller passed and returns a Descriptor BY VALUE. Neither
// reads nor writes any state shared across calls. Therefore the entire bug
// class in the brief (data race, lost update, over-commit, double-allocation,
// duplicate-ID clobber, phantom-capacity release) is STRUCTURALLY IMPOSSIBLE
// here — there is no shared mutable cell to race over and no accounting to
// inflate. This is the "genuinely safe (no contract to break)" outcome, NOT a
// "documented-unsafe" disclaim and NOT a real bug.
//
// Per the anti-bluff rule we do not merely ASSERT safety — we PROVE it
// sink-side: (1) a -race run with many goroutines hammering both directions
// over a SHARED input proves no data race; (2) a conservation/determinism
// assertion proves no lost update / no cross-goroutine clobber: every
// concurrent conversion of the same input yields a byte-identical result, and
// outputs do not alias each other (a shared-mutable-cell implementation would
// leak writes across goroutines and fail this). Both a clean -race run AND
// these sink-side equality/aliasing assertions are required to claim safety;
// neither alone suffices.
//
// This file is a CONTRACT PIN, not a reproducer: it PASSES on unmodified prod.
// If a future refactor introduces real shared state (a registry map, an
// allocation counter) without a guard, the -race detector trips here and the
// determinism assertion fails — converting this pin into the bite.

// goldenInput is the single SHARED Descriptor every goroutine converts.
// Distinct, non-trivial field values so any cross-goroutine corruption is
// visible rather than masked by zero/equal values.
func goldenInput() device.DeviceDescriptor {
	return device.DeviceDescriptor{
		ID:               "shared-golden-42",
		Arch:             device.ArchARM64,
		CPUCores:         24,
		MemoryBytes:      64 * 1024 * 1024 * 1024,
		GPUCount:         4,
		GPUVRAMBytes:     48_000_000_007, // not divisible by 4 -> remainder on last
		NPUTops:          9.5,
		PowerBudgetWatts: 130.0,
		HasBattery:       true,
		Trust:            device.TrustConfidential,
	}
}

// goldenNode is the deterministic expected Node for goldenInput, computed once
// SERIALLY (single goroutine, no possibility of a race) before any concurrency
// starts. Concurrent results are compared against this immutable oracle.
func goldenNode(t *testing.T) *helixv1.Node {
	t.Helper()
	return DescriptorToNode(goldenInput())
}

// ── 1. Concurrent DescriptorToNode over a SHARED input: race-clean + deterministic.
//
// Many goroutines convert the SAME by-value Descriptor concurrently. Under
// `-race` this proves no data race. The sink-side assertion proves no lost
// update / no clobber: every goroutine's Node must DeepEqual the serial oracle.
// If devicemap ever grew a shared mutable cell that calls wrote through, the
// concurrent writers would diverge from the serial oracle and this fails.
func TestConcurrent_DescriptorToNode_RaceCleanAndDeterministic(t *testing.T) {
	const goroutines = 256
	const perG = 64

	want := goldenNode(t)
	in := goldenInput() // shared value; copied by-value into each call

	var wg sync.WaitGroup
	var mismatches int64
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				got := DescriptorToNode(in)
				if !reflect.DeepEqual(got, want) {
					atomic.AddInt64(&mismatches, 1)
				}
			}
		}()
	}
	wg.Wait()

	if mismatches != 0 {
		t.Fatalf("ANTI-BLUFF: %d/%d concurrent DescriptorToNode results diverged "+
			"from the serial oracle — a shared-mutable-cell write leaked across "+
			"goroutines (lost-update/clobber class)", mismatches, int64(goroutines*perG))
	}
}

// ── 2. Concurrent NodeToDescriptor over a SHARED *Node: race-clean + deterministic.
//
// All goroutines READ the same *helixv1.Node concurrently and convert it. The
// input is never mutated; every result must equal the serial oracle. A clean
// -race run here proves the read path shares no mutable state across calls.
func TestConcurrent_NodeToDescriptor_RaceCleanAndDeterministic(t *testing.T) {
	const goroutines = 256
	const perG = 64

	shared := goldenNode(t) // single *Node read concurrently by all goroutines
	wantDesc := NodeToDescriptor(shared)

	var wg sync.WaitGroup
	var mismatches int64
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				got := NodeToDescriptor(shared)
				if !reflect.DeepEqual(got, wantDesc) {
					atomic.AddInt64(&mismatches, 1)
				}
			}
		}()
	}
	wg.Wait()

	if mismatches != 0 {
		t.Fatalf("ANTI-BLUFF: %d/%d concurrent NodeToDescriptor results diverged "+
			"from the serial oracle while reading a shared *Node", mismatches,
			int64(goroutines*perG))
	}
}

// ── 3. Mixed read/write/round-trip storm: the full costtracker-class probe.
//
// Goroutines run a MIX of DescriptorToNode, NodeToDescriptor, and full
// round-trips concurrently over shared inputs — the closest analogue to the
// "read-by-many / written-by-few" pattern that races in costtracker/helixnet.
// Because devicemap shares no mutable state, this is race-clean and every
// branch's result equals its serial oracle. This is the assertion that would
// BITE if devicemap were refactored into a stateful registry without a lock.
func TestConcurrent_MixedStorm_RaceCleanAndConserved(t *testing.T) {
	const goroutines = 300
	const perG = 50

	in := goldenInput()
	sharedNode := DescriptorToNode(in)

	wantNode := DescriptorToNode(in)
	wantDesc := NodeToDescriptor(sharedNode)
	wantRound := NodeToDescriptor(DescriptorToNode(in))

	var wg sync.WaitGroup
	var bad int64
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				switch (id + i) % 3 {
				case 0:
					if !reflect.DeepEqual(DescriptorToNode(in), wantNode) {
						atomic.AddInt64(&bad, 1)
					}
				case 1:
					if !reflect.DeepEqual(NodeToDescriptor(sharedNode), wantDesc) {
						atomic.AddInt64(&bad, 1)
					}
				default:
					if !reflect.DeepEqual(NodeToDescriptor(DescriptorToNode(in)), wantRound) {
						atomic.AddInt64(&bad, 1)
					}
				}
			}
		}(g)
	}
	wg.Wait()

	if bad != 0 {
		t.Fatalf("ANTI-BLUFF: %d concurrent mixed operations diverged from their "+
			"serial oracles — shared mutable state leaked across goroutines", bad)
	}
}

// ── 4. Output non-aliasing: distinct calls return INDEPENDENT objects.
//
// A correct pure builder returns a FRESH *Node per call: mutating one result
// must never be visible through another. If DescriptorToNode ever cached and
// returned a shared *Node (the classic "register returns the stored pointer"
// foot-gun that makes concurrent callers clobber each other), the two results
// would alias and the mutation would leak. This pins the freshness invariant
// that makes the concurrent tests above safe in the first place.
func TestDescriptorToNode_ResultsDoNotAlias(t *testing.T) {
	in := goldenInput()

	a := DescriptorToNode(in)
	b := DescriptorToNode(in)

	if a == b {
		t.Fatal("ANTI-BLUFF: two DescriptorToNode calls returned the SAME *Node " +
			"pointer — a shared/cached result would let concurrent callers clobber " +
			"each other")
	}
	if a.GetResources() == b.GetResources() {
		t.Fatal("ANTI-BLUFF: nested Resources pointer is shared between results")
	}
	if a.GetResources().GetCpu() == b.GetResources().GetCpu() {
		t.Fatal("ANTI-BLUFF: nested Cpu pointer is shared between results")
	}
	if len(a.GetResources().GetGpus()) > 0 &&
		&a.GetResources().GetGpus()[0] == &b.GetResources().GetGpus()[0] {
		t.Fatal("ANTI-BLUFF: GPU slice backing array is shared between results")
	}

	// Mutate a's deep field; b must be unaffected (proves independence).
	origCores := b.GetResources().GetCpu().GetCores()
	a.Resources.Cpu.Cores = origCores + 999
	if b.GetResources().GetCpu().GetCores() != origCores {
		t.Fatalf("ANTI-BLUFF: mutating result A changed result B (Cores %d -> %d): "+
			"results alias shared state", origCores, b.GetResources().GetCpu().GetCores())
	}
}

// ── 5. Input is not mutated by conversion (no write-through to shared input).
//
// NodeToDescriptor must treat its *Node argument as read-only. If it ever
// wrote back into the shared input (e.g. normalising fields in place), every
// concurrent reader of that same *Node would race. Pin read-only-ness by
// snapshotting the input, running the conversion under concurrency, and
// asserting the input is byte-identical afterward.
func TestNodeToDescriptor_DoesNotMutateInput(t *testing.T) {
	const goroutines = 128

	shared := goldenNode(t)
	before := DescriptorToNode(goldenInput()) // independent identical snapshot
	if !reflect.DeepEqual(shared, before) {
		t.Fatalf("precondition: snapshot must equal shared input")
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			_ = NodeToDescriptor(shared)
		}()
	}
	wg.Wait()

	if !reflect.DeepEqual(shared, before) {
		t.Fatalf("ANTI-BLUFF: NodeToDescriptor mutated its shared input under "+
			"concurrency:\n after = %+v\nbefore = %+v", shared, before)
	}
}
