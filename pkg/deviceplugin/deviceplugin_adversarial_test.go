package deviceplugin

// deviceplugin_adversarial_test.go — adversarial, SINK-SIDE probes for the
// Registry device-allocation invariant.
//
// Contract under test (from registry.go / types.go):
//   - "Registry is safe for concurrent use."
//   - Allocate "enforces that no model is ever oversubscribed past its reported,
//     healthy device count" and "The reservation is atomic: on rejection no
//     devices are reserved."
//   - Allocation.DeviceIDs are "the concrete devices reserved" — implying a
//     device is reserved for AT MOST ONE holder at a time.
//   - Release is idempotent: "Unknown or already-free IDs are ignored" and the
//     returned count reflects only IDs that were actually outstanding.
//   - Only HealthHealthy devices are allocatable.
//
// These tests assert SINK-SIDE behavior — that the *set* of devices handed out
// is disjoint and bounded, and that capacity is conserved — not merely "no
// panic". A -race-clean run is necessary but NOT sufficient: each test also
// pins a conservation / disjointness invariant that fails on a logic bug even
// without the race detector.
//
// FINDING (see final report): NO BUG. The prod Registry is correctly mutex-
// guarded; every probe below PASSES on unmodified prod under `-race -count=2`.
// These are therefore contract-PINNING tests (mutation-survivable): they bite
// when the single lock in Allocate/Release/ApplyFingerprint is removed.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// adversarialModel is the model string every device in this file uses.
const adversarialModel = "rtx3080"

// seedPool installs a fresh, fully-healthy pool of k devices of adversarialModel
// owned by one plugin and returns the Registry.
func seedPool(t *testing.T, k int) *Registry {
	t.Helper()
	reg := NewRegistry()
	devices := make([]Device, k)
	for i := 0; i < k; i++ {
		devices[i] = healthyGPU(fmt.Sprintf("adv-gpu-%04d", i))
	}
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "adversarial-plugin",
		ResourceKind: "gpu",
		Devices:      devices,
	}); err != nil {
		t.Fatalf("seedPool ApplyFingerprint(k=%d): %v", k, err)
	}
	return reg
}

// ---------------------------------------------------------------------------
// (a) DOUBLE-ALLOCATION UNDER RACE
//
// Many goroutines concurrently allocate single devices from a pool of K. We
// assert, at the SINK, that:
//   - No device ID is ever held by two holders at the same instant.
//   - The number of simultaneously-outstanding allocations never exceeds K.
// Allocators hold their reservation briefly (so overlap is real) before
// releasing, and a shared occupancy map (under its OWN test mutex, independent
// of the Registry's lock) detects any double-hand-out the instant it happens.
// ---------------------------------------------------------------------------
func TestAdversarial_NoDeviceAllocatedToTwoHoldersUnderRace(t *testing.T) {
	const (
		k        = 8
		racers   = 64
		rounds   = 40
	)
	reg := seedPool(t, k)

	// occupancy[id] == true means some holder currently believes it owns id.
	// This map is the independent SINK oracle; it is guarded by occMu, NOT by
	// the Registry lock, so a double-hand-out by the Registry is observable here.
	var occMu sync.Mutex
	occupancy := make(map[string]bool, k)

	var outstanding int64       // current number of held devices
	var maxOutstanding int64    // high-water mark
	var doubleHandout int64     // count of detected simultaneous double-allocs
	var overCapacity int64      // count of moments outstanding > k

	var wg sync.WaitGroup
	for g := 0; g < racers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				a, err := reg.Allocate(Resource{Kind: "gpu", Model: adversarialModel, Count: 1})
				if err != nil {
					// ErrOversubscription is the legitimate outcome when the pool
					// is momentarily exhausted; that is correct back-pressure.
					continue
				}
				// Claim the devices in the independent occupancy oracle.
				occMu.Lock()
				for _, id := range a.DeviceIDs {
					if occupancy[id] {
						atomic.AddInt64(&doubleHandout, 1)
					}
					occupancy[id] = true
				}
				occMu.Unlock()

				cur := atomic.AddInt64(&outstanding, int64(len(a.DeviceIDs)))
				for {
					hw := atomic.LoadInt64(&maxOutstanding)
					if cur <= hw || atomic.CompareAndSwapInt64(&maxOutstanding, hw, cur) {
						break
					}
				}
				if cur > int64(k) {
					atomic.AddInt64(&overCapacity, 1)
				}

				// Release the occupancy oracle, then the Registry.
				occMu.Lock()
				for _, id := range a.DeviceIDs {
					occupancy[id] = false
				}
				occMu.Unlock()
				atomic.AddInt64(&outstanding, -int64(len(a.DeviceIDs)))
				reg.Release(a.DeviceIDs...)
			}
		}()
	}
	wg.Wait()

	if d := atomic.LoadInt64(&doubleHandout); d != 0 {
		t.Fatalf("SINK violation: a device was handed to two holders simultaneously %d time(s) — Allocate is not atomic under race", d)
	}
	if o := atomic.LoadInt64(&overCapacity); o != 0 {
		t.Fatalf("SINK violation: simultaneously-outstanding allocations exceeded pool size %d on %d occasion(s) — over-commit under race", k, o)
	}
	if hw := atomic.LoadInt64(&maxOutstanding); hw > int64(k) {
		t.Fatalf("SINK violation: high-water outstanding=%d > pool=%d — over-commit under race", hw, k)
	}
}

// ---------------------------------------------------------------------------
// (b) UNGUARDED SHARED MUTABLE STATE / CONSERVATION
//
// Concurrent Allocate / Release / ApplyFingerprint (re-register) churn the
// shared device & allocated maps. After the storm we quiesce and assert the
// CONSERVATION invariant the existing suite does not check directly:
//
//     allocated + available == K   (for a stable, fully-healthy pool)
//
// We measure `allocated` by draining the pool one device at a time, and
// `available` is what remained allocatable before the drain. Their sum must be
// exactly K — no phantom capacity, no lost devices. A lost update on the
// allocated map (write raced away) would leave a device wedged-allocated and
// shrink the sum below K; a double-free path inflating availability would push
// it above K.
// ---------------------------------------------------------------------------
func TestAdversarial_ConservationAllocatedPlusAvailableEqualsK(t *testing.T) {
	const (
		k       = 12
		workers = 48
		rounds  = 60
	)
	reg := seedPool(t, k)

	base := make([]Device, k)
	for i := 0; i < k; i++ {
		base[i] = healthyGPU(fmt.Sprintf("adv-gpu-%04d", i))
	}

	var wg sync.WaitGroup
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				switch id % 3 {
				case 0: // allocate-then-release a small burst
					a, err := reg.Allocate(Resource{Kind: "gpu", Model: adversarialModel, Count: 2})
					if err == nil {
						reg.Release(a.DeviceIDs...)
					}
				case 1: // single allocate/release
					a, err := reg.Allocate(Resource{Kind: "gpu", Model: adversarialModel, Count: 1})
					if err == nil {
						reg.Release(a.DeviceIDs...)
					}
				default: // idempotent re-register of the full set
					_ = reg.ApplyFingerprint(Fingerprint{
						PluginName:   "adversarial-plugin",
						ResourceKind: "gpu",
						Devices:      base,
					})
				}
			}
		}(g)
	}
	wg.Wait()

	// Quiesce: restore the full, stable inventory.
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "adversarial-plugin",
		ResourceKind: "gpu",
		Devices:      base,
	}); err != nil {
		t.Fatalf("quiesce ApplyFingerprint: %v", err)
	}

	// available BEFORE draining.
	available := reg.Allocatable(adversarialModel)

	// Drain: allocate one-by-one until exhausted; count = currently-allocatable
	// that we could claim now. Because we quiesced to zero outstanding, every
	// device should be free, so allocated-after-drain should equal K and
	// available should already equal K.
	var drained []Allocation
	for {
		a, err := reg.Allocate(Resource{Kind: "gpu", Model: adversarialModel, Count: 1})
		if err != nil {
			break
		}
		drained = append(drained, a)
	}
	allocatedNow := 0
	seen := make(map[string]bool)
	for _, a := range drained {
		for _, id := range a.DeviceIDs {
			if seen[id] {
				t.Fatalf("SINK violation: device %q drained twice — double-allocation leaked through concurrent churn", id)
			}
			seen[id] = true
			allocatedNow++
		}
	}
	for _, a := range drained {
		reg.Release(a.DeviceIDs...)
	}

	if available != k {
		t.Fatalf("conservation broken before drain: available=%d, want K=%d (lost or phantom capacity after concurrent churn)", available, k)
	}
	if allocatedNow != k {
		t.Fatalf("conservation broken: drained %d distinct devices, want K=%d (a device was wedged-allocated by a lost update)", allocatedNow, k)
	}
	// available (pre-drain, all free) + 0 outstanding == K already proven;
	// the strict sum identity allocated+available==K holds at every quiescent
	// point because outstanding was 0.
}

// ---------------------------------------------------------------------------
// (c) DOUBLE-FREE / FREE-OF-UNALLOCATED — phantom-capacity probe.
//
// The danger: returning a device twice (or freeing one never allocated) must
// NOT inflate availability beyond K and must NOT later permit over-allocation.
// We assert Release is idempotent at the SINK: capacity stays exactly K.
// ---------------------------------------------------------------------------
func TestAdversarial_DoubleFreeDoesNotInflateCapacity(t *testing.T) {
	const k = 4
	reg := seedPool(t, k)

	// Allocate the whole pool.
	a, err := reg.Allocate(Resource{Kind: "gpu", Model: adversarialModel, Count: k})
	if err != nil {
		t.Fatalf("initial full Allocate: %v", err)
	}
	if got := reg.Allocatable(adversarialModel); got != 0 {
		t.Fatalf("after full allocation, allocatable=%d, want 0", got)
	}

	// Free everything ONCE — count must equal k (all were outstanding).
	if n := reg.Release(a.DeviceIDs...); n != k {
		t.Fatalf("first Release returned %d, want %d (all k were outstanding)", n, k)
	}
	if got := reg.Allocatable(adversarialModel); got != k {
		t.Fatalf("after first Release, allocatable=%d, want %d", got, k)
	}

	// DOUBLE-FREE the same IDs — count must be 0 (none outstanding) and capacity
	// must remain exactly k, NOT 2k. This is the phantom-capacity guard.
	if n := reg.Release(a.DeviceIDs...); n != 0 {
		t.Fatalf("double Release returned %d, want 0 (idempotency broken — phantom frees counted)", n)
	}
	if got := reg.Allocatable(adversarialModel); got != k {
		t.Fatalf("after double Release, allocatable=%d, want %d — phantom capacity inflated past K", got, k)
	}

	// FREE-OF-UNALLOCATED: never-seen IDs must also be ignored and not inflate.
	if n := reg.Release("ghost-1", "ghost-2", "ghost-3"); n != 0 {
		t.Fatalf("Release of unknown IDs returned %d, want 0", n)
	}
	if got := reg.Allocatable(adversarialModel); got != k {
		t.Fatalf("after free-of-unallocated, allocatable=%d, want %d", got, k)
	}

	// SINK proof: the inflated availability must not enable over-allocation.
	// Requesting k+1 after the double/ghost frees must STILL be rejected.
	if _, err := reg.Allocate(Resource{Kind: "gpu", Model: adversarialModel, Count: k + 1}); err == nil {
		t.Fatalf("over-allocation succeeded after double-free — phantom capacity let %d devices out of a pool of %d", k+1, k)
	}
	// And allocating exactly k must succeed (capacity not corrupted downward).
	full, err := reg.Allocate(Resource{Kind: "gpu", Model: adversarialModel, Count: k})
	if err != nil {
		t.Fatalf("post-doublefree full Allocate failed: %v — capacity corrupted downward", err)
	}
	if len(full.DeviceIDs) != k {
		t.Fatalf("post-doublefree full Allocate returned %d IDs, want %d", len(full.DeviceIDs), k)
	}
}

// TestAdversarial_ConcurrentDoubleFreeNeverOversubscribes drives the double-free
// path under contention: workers repeatedly allocate-then-Release-TWICE. If the
// duplicate Release ever credited a phantom slot, the total simultaneously
// outstanding would exceed K. We assert it never does, via the occupancy oracle.
func TestAdversarial_ConcurrentDoubleFreeNeverOversubscribes(t *testing.T) {
	const (
		k       = 6
		workers = 40
		rounds  = 50
	)
	reg := seedPool(t, k)

	var outstanding int64
	var over int64
	var wg sync.WaitGroup
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				a, err := reg.Allocate(Resource{Kind: "gpu", Model: adversarialModel, Count: 1})
				if err != nil {
					continue
				}
				cur := atomic.AddInt64(&outstanding, 1)
				if cur > int64(k) {
					atomic.AddInt64(&over, 1)
				}
				atomic.AddInt64(&outstanding, -1)
				// Double Release of the same IDs — must be idempotent.
				reg.Release(a.DeviceIDs...)
				reg.Release(a.DeviceIDs...)
			}
		}()
	}
	wg.Wait()

	if o := atomic.LoadInt64(&over); o != 0 {
		t.Fatalf("SINK violation: outstanding exceeded pool=%d on %d occasion(s) under concurrent double-free", k, o)
	}
	// Final capacity must be exactly k — no phantom slots accumulated.
	if got := reg.Allocatable(adversarialModel); got != k {
		t.Fatalf("after concurrent double-free storm, allocatable=%d, want %d — phantom capacity accumulated", got, k)
	}
}

// ---------------------------------------------------------------------------
// (d) IDENTITY: duplicate IDs rejected; unhealthy not allocatable; total order.
// ---------------------------------------------------------------------------

// TestAdversarial_DuplicateDeviceIDsRejectedAtomically pins the contract that a
// fingerprint reporting the same ID twice is rejected and leaves inventory
// completely unchanged (no partial upsert).
func TestAdversarial_DuplicateDeviceIDsRejectedAtomically(t *testing.T) {
	reg := NewRegistry()
	// Pre-seed one known device so we can prove atomicity (no partial mutation).
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "p",
		ResourceKind: "gpu",
		Devices:      []Device{healthyGPU("keep-0")},
	}); err != nil {
		t.Fatalf("pre-seed: %v", err)
	}
	before := reg.Count()

	dup := healthyGPU("dup-1")
	err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "p",
		ResourceKind: "gpu",
		Devices:      []Device{dup, healthyGPU("new-2"), dup},
	})
	if err == nil {
		t.Fatalf("duplicate device ID was accepted; want rejection")
	}
	// Atomic-on-error: the rejected fingerprint must not have upserted new-2 nor
	// pruned keep-0.
	if got := reg.Count(); got != before {
		t.Fatalf("inventory changed after rejected duplicate fingerprint: count=%d, want %d (non-atomic partial apply)", got, before)
	}
	if _, ok := reg.Device("new-2"); ok {
		t.Fatalf("new-2 was upserted despite the fingerprint being rejected — non-atomic ingest")
	}
	if _, ok := reg.Device("keep-0"); !ok {
		t.Fatalf("keep-0 was pruned despite the fingerprint being rejected — non-atomic ingest")
	}
}

// TestAdversarial_UnhealthyDeviceNotAllocatable pins that an unhealthy device is
// never handed out, and that flipping it healthy makes it allocatable (proving
// the filter, not a blanket exclusion).
func TestAdversarial_UnhealthyDeviceNotAllocatable(t *testing.T) {
	reg := NewRegistry()
	sick := healthyGPU("sick-0")
	sick.Health = HealthUnhealthy
	well := healthyGPU("well-0")
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "p",
		ResourceKind: "gpu",
		Devices:      []Device{sick, well},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if got := reg.Allocatable(adversarialModel); got != 1 {
		t.Fatalf("allocatable=%d, want 1 (only the healthy device)", got)
	}
	// Requesting 2 must be rejected: the unhealthy device must NOT be counted.
	if _, err := reg.Allocate(Resource{Kind: "gpu", Model: adversarialModel, Count: 2}); err == nil {
		t.Fatalf("allocation of 2 succeeded but only 1 device is healthy — unhealthy device was scheduled onto")
	}
	a, err := reg.Allocate(Resource{Kind: "gpu", Model: adversarialModel, Count: 1})
	if err != nil {
		t.Fatalf("allocate 1 healthy: %v", err)
	}
	for _, id := range a.DeviceIDs {
		if id == "sick-0" {
			t.Fatalf("SINK violation: unhealthy device %q was allocated", id)
		}
	}
	reg.Release(a.DeviceIDs...)

	// Flip the device healthy via re-fingerprint; now 2 must be allocatable.
	sick.Health = HealthHealthy
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "p",
		ResourceKind: "gpu",
		Devices:      []Device{sick, well},
	}); err != nil {
		t.Fatalf("re-apply healthy: %v", err)
	}
	if got := reg.Allocatable(adversarialModel); got != 2 {
		t.Fatalf("after healing, allocatable=%d, want 2 — health filter is a blanket exclusion, not a real predicate", got)
	}
}

// TestAdversarial_AllocationIsDeterministicTotalOrder pins that on equal-priority
// devices the allocator picks the lowest IDs in sorted order — a stable total
// order, so repeated allocate/release cycles are reproducible (no nondeterministic
// map-iteration leakage into the chosen set).
func TestAdversarial_AllocationIsDeterministicTotalOrder(t *testing.T) {
	const k = 16
	reg := seedPool(t, k)

	var first []string
	for cycle := 0; cycle < 25; cycle++ {
		a, err := reg.Allocate(Resource{Kind: "gpu", Model: adversarialModel, Count: 3})
		if err != nil {
			t.Fatalf("cycle %d allocate: %v", cycle, err)
		}
		// Must be the three lowest IDs, in sorted order.
		want := []string{"adv-gpu-0000", "adv-gpu-0001", "adv-gpu-0002"}
		if len(a.DeviceIDs) != len(want) {
			t.Fatalf("cycle %d: got %d ids, want %d", cycle, len(a.DeviceIDs), len(want))
		}
		for i := range want {
			if a.DeviceIDs[i] != want[i] {
				t.Fatalf("cycle %d: chosen[%d]=%q, want %q — allocation is not a stable total order", cycle, i, a.DeviceIDs[i], want[i])
			}
		}
		if cycle == 0 {
			first = append(first, a.DeviceIDs...)
		} else {
			for i := range first {
				if a.DeviceIDs[i] != first[i] {
					t.Fatalf("cycle %d diverged from cycle 0 selection at %d: %q vs %q", cycle, i, a.DeviceIDs[i], first[i])
				}
			}
		}
		reg.Release(a.DeviceIDs...)
	}
}
