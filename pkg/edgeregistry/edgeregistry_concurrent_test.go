package edgeregistry

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

// concRunUUID uniquely tags this concurrency suite's output so a PASS-bluff
// (test silently skipped/short-circuited) is observable in captured logs.
const concRunUUID = "edgeregistry-conc-9d4e7a21-6c0b-4f88-bb3a-1e2f5c8a0d77"

// TestConcurrentRegisterLookup_Race is an ADVERSARIAL data-race probe.
//
// The Registry is a register/lookup registry whose role is concurrent: edge
// devices register (writing the byID map) while the control plane looks them up
// (reading the same map). Register does `r.byID[id] = ...` (a map WRITE) and
// Get/Len/ListByTier/Dump iterate or index the SAME map (map READS) with NO
// synchronization. Under `go test -race` (and very likely without it, as a
// fatal "concurrent map read and map write" runtime crash) this MUST trip if
// the registry is unsynchronized.
//
// WHY THIS BITES: the race detector instruments every map access; with writers
// and readers hitting r.byID simultaneously and no mutex, it reports a data
// race (and Go's runtime may independently throw the unrecoverable
// "concurrent map read and map write" fatal). Add a sync.RWMutex (writers Lock,
// readers RLock) and the race disappears. Revert the mutex -> race/crash
// returns. That revert-to-crash is the load-bearing proof.
func TestConcurrentRegisterLookup_Race(t *testing.T) {
	r := New()

	const writers = 8
	const readers = 8
	const iters = 400

	// Seed a few nodes so readers have hits immediately, not just misses.
	for _, tier := range allTiers {
		if err := r.Register(makeReg(tier)); err != nil {
			t.Fatalf("seed Register(%v): %v", tier, err)
		}
	}
	t.Logf("run=%s seeded len=%d", concRunUUID, r.Len())

	var wg sync.WaitGroup

	// Writers: continuously (re-)register nodes -> map WRITES.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				reg := Registration{
					NodeID: fmt.Sprintf("w%d-n%d", w, i%16),
					Tier:   allTiers[(w+i)%len(allTiers)],
					Trust:  TrustBasic,
					Caps:   Capability{ComputeClass: "cpu", MemoryMB: 256, Backends: []string{"b"}},
				}
				if err := r.Register(reg); err != nil {
					t.Errorf("Register: %v", err)
					return
				}
			}
		}(w)
	}

	// Readers: continuously look up / list / dump -> map READS.
	for rd := 0; rd < readers; rd++ {
		wg.Add(1)
		go func(rd int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// Lookup of a possibly-present and a definitely-absent id; a miss
				// must be a clean error, never a panic.
				if _, err := r.Get(fmt.Sprintf("w%d-n%d", rd%writers, i%16)); err != nil && !errors.Is(err, ErrNotFound) {
					t.Errorf("Get unexpected error: %v", err)
					return
				}
				if _, err := r.Get("absent-id"); err != nil && !errors.Is(err, ErrNotFound) {
					t.Errorf("Get(absent) unexpected error: %v", err)
					return
				}
				_ = r.Len()
				_ = r.ListByTier(allTiers[i%len(allTiers)])
				_ = r.Dump()
			}
		}(rd)
	}

	wg.Wait()
	t.Logf("run=%s done len=%d", concRunUUID, r.Len())
}

// TestConcurrentChurnConservation proves CORRECTNESS under concurrent writes:
// after N goroutines each register a disjoint set of nodes, the registry holds
// EXACTLY the union (no lost registration, no ghost entry). Re-registration of
// the same id must update in place (no duplicate). This is the conservation law
// for a registry: every committed Register is findable afterwards and Len equals
// the exact live set.
//
// WHY THIS BITES: if the map write is racy/lost (the bug a mutex fixes), a
// concurrent Register can be dropped -> the final Len/Get assertions fail
// (missing node). With correct synchronization every disjoint id survives.
// Disjoint key spaces make the EXPECTED final set deterministic regardless of
// interleaving, so this is a clean conservation oracle.
func TestConcurrentChurnConservation(t *testing.T) {
	r := New()

	const workers = 10
	const perWorker = 50 // distinct nodes per worker

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := fmt.Sprintf("conserve-w%02d-n%03d", w, i)
				reg := Registration{
					NodeID: id,
					Tier:   allTiers[(w+i)%len(allTiers)],
					Trust:  TrustAttested,
					Caps:   Capability{ComputeClass: "cpu", MemoryMB: 100 + i, Backends: []string{"x"}},
				}
				// Register twice to exercise the in-place update path concurrently.
				if err := r.Register(reg); err != nil {
					t.Errorf("Register %q: %v", id, err)
					return
				}
				if err := r.Register(reg); err != nil {
					t.Errorf("re-Register %q: %v", id, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	want := workers * perWorker
	t.Logf("run=%s want=%d got len=%d", concRunUUID, want, r.Len())
	if r.Len() != want {
		t.Fatalf("conservation: Len = %d, want %d (lost registration or ghost/duplicate)", r.Len(), want)
	}

	// Every committed id must be findable with its exact stored attributes.
	for w := 0; w < workers; w++ {
		for i := 0; i < perWorker; i++ {
			id := fmt.Sprintf("conserve-w%02d-n%03d", w, i)
			got, err := r.Get(id)
			if err != nil {
				t.Fatalf("Get(%q) after churn: %v (lost registration)", id, err)
			}
			wantTier := allTiers[(w+i)%len(allTiers)]
			if got.Tier != wantTier || got.Caps.MemoryMB != 100+i {
				t.Errorf("Get(%q) = tier %v mem %d, want tier %v mem %d",
					id, got.Tier, got.Caps.MemoryMB, wantTier, 100+i)
			}
		}
	}

	// No ghost: an id that was never registered is a clean miss, not a panic.
	if _, err := r.Get("never-registered"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(ghost): err = %v, want ErrNotFound", err)
	}

	// Dump must contain exactly the live set, sorted and unique.
	dump := r.Dump()
	if len(dump) != want {
		t.Fatalf("Dump len = %d, want %d", len(dump), want)
	}
	for i := 1; i < len(dump); i++ {
		if dump[i-1].NodeID >= dump[i].NodeID {
			t.Fatalf("Dump not strictly sorted/unique: %q >= %q", dump[i-1].NodeID, dump[i].NodeID)
		}
	}
}
