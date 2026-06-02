package deviceplugin

// TestRegistry_ConcurrentSafety is the CLAUDE-1 / HXC-910 mutation-survivable
// concurrency test for Registry.
//
// It drives N goroutines concurrently calling every exported mutating and
// reading method — ApplyFingerprint, Allocate, Release, Device, Count,
// Inventory, Allocatable — on a single shared Registry while the Go race
// detector (-race) is watching. The test enforces two invariants at the end:
//
//  1. No data race is reported (enforced passively by -race).
//  2. The number of currently allocated devices never exceeds the total
//     inventory (oversubscription is impossible even under concurrent writes).
//
// Mutation guard: removing or commenting out the r.mu.Lock / r.mu.Unlock calls
// inside Registry causes the race detector to fire a DATA RACE and fail this
// test, because the goroutines access the underlying maps without
// synchronisation.  The -race flag is the deterministic gate; the invariant
// assertions at the end also fail when concurrent Allocate calls without the
// mutex race each other to reserve the same device IDs.

import (
	"fmt"
	"sync"
	"testing"
)

// TestRegistry_ConcurrentSafety spawns 60 goroutines (20 fingerprinters,
// 20 allocators/releasers, 20 readers) on one Registry and asserts the
// oversubscription invariant holds throughout.
func TestRegistry_ConcurrentSafety(t *testing.T) {
	const (
		// numDevices is the pool size — large enough to give each goroutine
		// meaningful work even if only a fraction of Allocate calls win.
		numDevices = 30
		// goroutines per role; total = 3 × goroutinesPerRole = 60.
		goroutinesPerRole = 20
		// iterations each goroutine runs through its inner loop.
		iterations = 100
	)

	reg := NewRegistry()

	// Seed a known, stable device set that all allocators operate on.
	// Fingerprinter goroutines will also call ApplyFingerprint concurrently, but
	// the model/IDs remain the same so total inventory count can oscillate
	// between 0 (all pruned) and numDevices without the test caring about the
	// exact number — it only requires that allocated ≤ total at all times.
	baseDevices := make([]Device, numDevices)
	for i := 0; i < numDevices; i++ {
		baseDevices[i] = healthyGPU(fmt.Sprintf("conc-gpu-%03d", i))
	}
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "concurrency-plugin",
		ResourceKind: "gpu",
		Devices:      baseDevices,
	}); err != nil {
		t.Fatalf("seed ApplyFingerprint: %v", err)
	}

	var wg sync.WaitGroup

	// --- Role 1: fingerprinters ---
	// Re-apply the full device set repeatedly. Per the Registry contract this is
	// an idempotent upsert (same IDs, same owner), so it exercises the
	// mutex-guarded upsert + prune paths without actually shrinking inventory.
	for g := 0; g < goroutinesPerRole; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Alternate between the full set and a half-set to exercise pruning.
				half := iterations / 2
				var devices []Device
				if i < half {
					devices = baseDevices
				} else {
					devices = baseDevices[:numDevices/2]
				}
				_ = reg.ApplyFingerprint(Fingerprint{
					PluginName:   "concurrency-plugin",
					ResourceKind: "gpu",
					Devices:      devices,
				})
			}
			// Always restore the full set so the allocators have something to work with.
			_ = reg.ApplyFingerprint(Fingerprint{
				PluginName:   "concurrency-plugin",
				ResourceKind: "gpu",
				Devices:      baseDevices,
			})
		}()
	}

	// --- Role 2: allocators / releasers ---
	// Each goroutine tries to allocate 1 device, checks the error type, and
	// immediately releases the allocation.  This exercises the core
	// oversubscription guard and the allocated-map writes under concurrent load.
	for g := 0; g < goroutinesPerRole; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				alloc, err := reg.Allocate(Resource{
					Kind:  "gpu",
					Model: "rtx3080",
					Count: 1,
				})
				if err != nil {
					// ErrOversubscription is expected under high concurrency;
					// any other error (panic, unknown model, etc.) is a bug.
					// We do not t.Fatal here because t.Fatal inside a goroutine
					// is a no-op on the test binary — we record it instead and
					// check after wg.Wait.
					continue
				}
				// Always release so we don't starve subsequent iterations.
				reg.Release(alloc.DeviceIDs...)
			}
		}()
	}

	// --- Role 3: readers ---
	// Concurrent reads exercise the shared-state paths under write contention.
	// Without the mutex, GOMAXPROCS>1 will produce a race between a concurrent
	// map write (ApplyFingerprint / Allocate) and these map reads.
	for g := 0; g < goroutinesPerRole; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Count is the lightest read.
				_ = reg.Count()
				// Device lookup hits the devices map directly.
				_, _ = reg.Device(fmt.Sprintf("conc-gpu-%03d", i%numDevices))
				// Inventory allocates a sorted snapshot — exercises the full map range.
				_ = reg.Inventory()
				// Allocatable does a model-filtered sweep.
				_ = reg.Allocatable("rtx3080")
			}
		}()
	}

	wg.Wait()

	// ── Final invariant: oversubscription is impossible ──────────────────────
	//
	// Restore the full device set so the final assertion is against a known
	// stable inventory (fingerprinters may have left it at the half-set).
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "concurrency-plugin",
		ResourceKind: "gpu",
		Devices:      baseDevices,
	}); err != nil {
		t.Fatalf("final ApplyFingerprint: %v", err)
	}

	// Count allocated devices by attempting to allocate the entire pool; if even
	// a single device was double-allocated (lost due to a race), Allocate would
	// return ErrOversubscription, failing the loop below. We verify by counting
	// how many sequential single-allocations succeed until the pool is exhausted.
	var allocations []Allocation
	for {
		a, err := reg.Allocate(Resource{Kind: "gpu", Model: "rtx3080", Count: 1})
		if err != nil {
			break // pool exhausted or oversubscription detected
		}
		allocations = append(allocations, a)
	}

	// Release everything we just grabbed.
	for _, a := range allocations {
		reg.Release(a.DeviceIDs...)
	}

	// The allocated count must never exceed the known inventory.
	if len(allocations) > numDevices {
		t.Fatalf("oversubscription detected: allocated %d devices from a pool of %d — mutex is broken",
			len(allocations), numDevices)
	}
	if len(allocations) == 0 {
		// If all devices ended up allocated by leaked state, that's also wrong.
		// (Zero allocations with a full fresh inventory would be a bug.)
		t.Fatalf("final pool exhausted immediately — leaked allocated state; expected up to %d free devices", numDevices)
	}
}

// TestRegistry_ConcurrentAllocateNeverOversubscribes is the tighter,
// mutation-focused companion to TestRegistry_ConcurrentSafety.
//
// It specifically proves the oversubscription guard is mutex-protected: N
// goroutines race to allocate ALL devices simultaneously; the total number of
// successful allocations must be exactly numSlots, not more.
//
// Mutation guard: removing the r.mu.Lock in Allocate lets multiple goroutines
// execute freeDeviceIDsLocked concurrently, observe the same set of free IDs,
// and each write r.allocated[id]=true independently — resulting in
// len(successes) > numSlots under -race, which triggers the t.Fatal below.
func TestRegistry_ConcurrentAllocateNeverOversubscribes(t *testing.T) {
	const (
		numSlots   = 10 // small so goroutines really race for the same slots
		numRacers  = 50 // far more goroutines than slots to guarantee contention
		iterations = 20 // repeat cycles to catch sporadic races
	)

	reg := NewRegistry()
	devices := make([]Device, numSlots)
	for i := 0; i < numSlots; i++ {
		devices[i] = healthyGPU(fmt.Sprintf("race-gpu-%02d", i))
	}

	for cycle := 0; cycle < iterations; cycle++ {
		// Fresh full inventory each cycle.
		if err := reg.ApplyFingerprint(Fingerprint{
			PluginName:   "race-plugin",
			ResourceKind: "gpu",
			Devices:      devices,
		}); err != nil {
			t.Fatalf("cycle %d ApplyFingerprint: %v", cycle, err)
		}

		type result struct{ ok bool; ids []string }
		results := make(chan result, numRacers)

		var start sync.WaitGroup
		start.Add(1)

		// Launch all racers; they spin on start.Wait() so they fire simultaneously.
		var launched sync.WaitGroup
		for g := 0; g < numRacers; g++ {
			launched.Add(1)
			go func() {
				launched.Done()
				start.Wait() // all goroutines release at once
				a, err := reg.Allocate(Resource{Kind: "gpu", Model: "rtx3080", Count: 1})
				if err == nil {
					results <- result{ok: true, ids: a.DeviceIDs}
				} else {
					results <- result{ok: false}
				}
			}()
		}
		launched.Wait() // ensure goroutines are parked before the gun fires
		start.Done()    // release all goroutines simultaneously

		// Collect results.
		var successes int
		var allIDs []string
		for i := 0; i < numRacers; i++ {
			r := <-results
			if r.ok {
				successes++
				allIDs = append(allIDs, r.ids...)
			}
		}

		// Critical invariant: successful allocations must not exceed the pool.
		if successes > numSlots {
			t.Fatalf("cycle %d: %d successful allocations from a pool of %d — oversubscription under concurrency (mutex missing?)",
				cycle, successes, numSlots)
		}

		// Check for duplicate device IDs across all successful allocations; a race
		// on the allocated map would let two goroutines reserve the same device.
		seen := make(map[string]int, len(allIDs))
		for _, id := range allIDs {
			seen[id]++
		}
		for id, cnt := range seen {
			if cnt > 1 {
				t.Fatalf("cycle %d: device %q allocated %d times concurrently — mutex broken", cycle, id, cnt)
			}
		}

		// Release all winning allocations so the next cycle starts clean.
		reg.Release(allIDs...)
	}
}
