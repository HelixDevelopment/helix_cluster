package gpupool

// Adversarial concurrency probe for pkg/gpupool.
//
// CONTRACT UNDER TEST (from gpupool.go doc, lines 18-19):
//
//	"All logic is pure Go and deterministic: no clocks, no network, no host
//	 or GPU access. Callers inject providers, devices and specs."
//
// PROTOCOL NOTE — THE OVER-COMMIT / FREELIST / DOUBLE-FREE FAMILY DOES NOT APPLY
// HERE, AND THIS IS PINNED, NOT ASSUMED:
//
//	gpupool is a *stateless, read-only* query package. PoolManager.Allocate
//	does NOT mutate state: it copies m.providers into a local slice, sorts the
//	copy, and returns a Provider BY VALUE. It never decrements FreeCapacity,
//	there is no freelist, no allocation table, no counter, and NO Free / Release
//	/ Return / Acquire method exists (verified by grep: zero writes to
//	FreeCapacity or m.providers in production code). Filter and Select are pure
//	functions over copies. NewPoolManager defensively copies its input.
//
//	Therefore "allocate beyond capacity under race", "lost freed GPU", and
//	"double-free inflates capacity" are STRUCTURALLY IMPOSSIBLE — there is no
//	mutable shared allocation state to race. The honest adversarial questions
//	that REMAIN, and that these tests answer SINK-SIDE, are:
//
//	  (A) UNGUARDED SHARED READS: many goroutines hammering Allocate/Filter/
//	      Select on ONE shared *PoolManager and ONE shared device slice — does
//	      -race observe any data race? (A read-only structure shared across
//	      goroutines is still a -race subject if anything writes it.)
//	  (B) DETERMINISM UNDER CONCURRENCY (sink-side correctness, not "no panic"):
//	      every concurrent Allocate must return the SAME oracle-correct provider;
//	      a torn read of m.providers (if Allocate ever mutated shared state)
//	      would surface as divergent winners. We assert exact winner identity,
//	      not merely absence of panic.
//	  (C) DEFENSIVE-COPY OWNERSHIP: NewPoolManager copies the caller's slice. A
//	      caller mutating ITS slice concurrently with Allocate must NOT change
//	      the pool's answer. This is the package's actual "ownership" invariant
//	      standing in for the freelist-integrity invariant of a stateful pool.
//
// THREE-WAY DISCRIMINATION VERDICT: the package DISCLAIMS statefulness ("no
// host/GPU access ... callers inject") rather than PROMISING a concurrent
// stateful allocator. It is a genuinely-safe stateless value-query. So per the
// protocol this is NO BUG (genuinely-safe), and these tests are contract-pinning
// (they PASS on unfixed prod and would BITE a future regression that introduced
// shared mutation). No env-gating, no skips.

import (
	"sync"
	"testing"
)

// fixedFleet is a deterministic provider fleet with a single unambiguous
// oracle winner for need=2: tier ascending, then FreeCapacity descending, then
// ID ascending. local-empty(free 0) is skipped; among the rest the best tier
// with sufficient free and largest free / smallest ID wins.
//
// Oracle for need=2: rp-some is the only RemoteProxy with free>=2 -> but wait,
// local tier providers: local-empty(0, skip), local-mid(free 5). local-mid is
// TierLocal with free 5 >= 2 => it is the strict winner (best tier, sufficient).
func fixedFleet() []Provider {
	return []Provider{
		{ID: "local-empty", Tier: TierLocal, Capacity: 8, FreeCapacity: 0, Healthy: true},
		{ID: "local-mid", Tier: TierLocal, Capacity: 8, FreeCapacity: 5, Healthy: true},
		{ID: "rp-some", Tier: TierRemoteProxy, Capacity: 8, FreeCapacity: 3, Healthy: true},
		{ID: "cloud-lots", Tier: TierCloud, Capacity: 16, FreeCapacity: 16, Healthy: true},
		{ID: "dec-huge", Tier: TierDecentralized, Capacity: 64, FreeCapacity: 64, Healthy: true},
	}
}

const adversarialWinner = "local-mid" // oracle winner of fixedFleet() for need=2

// TestAdversarial_ConcurrentAllocateNoRaceAndDeterministic drives N goroutines
// concurrently calling Allocate on ONE shared *PoolManager. Under -race this is
// the (A) unguarded-shared-state probe. SINK-SIDE it asserts (B): every single
// concurrent result is the exact oracle winner — not "no panic". A shared-state
// mutation regression (e.g. someone making Allocate decrement FreeCapacity in
// place on m.providers) would manifest as either a -race report or divergent
// winners caught here.
func TestAdversarial_ConcurrentAllocateNoRaceAndDeterministic(t *testing.T) {
	id := runUUID(t)
	m := NewPoolManager(fixedFleet())
	spec := WorkloadSpec{GPUCount: 2}

	// Sanity: the single-threaded answer is the oracle winner before we race it.
	if got, err := m.Allocate(spec); err != nil || got.ID != adversarialWinner {
		t.Fatalf("run=%s precondition: Allocate got %q err=%v, want %q", id, got.ID, err, adversarialWinner)
	}

	const goroutines = 256
	const perG = 200
	results := make([]string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			last := ""
			for i := 0; i < perG; i++ {
				got, err := m.Allocate(spec) // shared *PoolManager, concurrent
				if err != nil {
					last = "ERR:" + err.Error()
					continue
				}
				last = got.ID
			}
			results[g] = last
		}(g)
	}
	wg.Wait()

	// SINK-SIDE: every goroutine's observed allocation is the oracle winner.
	bad := 0
	for g, r := range results {
		if r != adversarialWinner {
			if bad < 5 {
				t.Errorf("run=%s goroutine %d observed allocation %q, want %q", id, g, r, adversarialWinner)
			}
			bad++
		}
	}
	if bad > 0 {
		t.Fatalf("run=%s %d/%d goroutines saw a non-oracle allocation (determinism under concurrency broken)",
			id, bad, goroutines)
	}
	t.Logf("run=%s %d goroutines x %d allocs all returned oracle winner %q; -race clean if no DATA RACE above",
		id, goroutines, perG, adversarialWinner)
}

// TestAdversarial_DefensiveCopyOwnershipUnderRace is the (C) ownership probe and
// the closest honest analogue to "freelist integrity". NewPoolManager copies
// the caller's slice, so the pool OWNS its view. We mutate the CALLER's original
// slice from many goroutines (writers) while many other goroutines call Allocate
// (readers) on the manager. Under -race, if NewPoolManager did NOT copy (i.e.
// the pool aliased the caller's backing array), the writer/reader pair would be
// a data race AND would corrupt the answer. We assert the pool's answer is
// completely immune to the external mutation: it always returns the oracle
// winner computed from the ORIGINAL fleet.
//
// MUTATION-PROOF (conceptual, documented — no prod change applied because there
// is no bug): if NewPoolManager's defensive copy (gpupool.go lines 117-119) were
// removed so it stored `providers` directly, this test would FAIL two ways under
// `go test -race`: (1) a DATA RACE between the external writers and Allocate's
// read of m.providers, and (2) divergent / non-oracle winners as the aliased
// backing array is mutated mid-Allocate. With the copy present (current prod) it
// PASSES. This bidirectionally pins the copy as load-bearing.
func TestAdversarial_DefensiveCopyOwnershipUnderRace(t *testing.T) {
	id := runUUID(t)
	original := fixedFleet()
	m := NewPoolManager(original) // pool takes a COPY per contract
	spec := WorkloadSpec{GPUCount: 2}

	const readers = 64
	const iters = 500
	// One writer per element: writers own DISJOINT indices so they never race
	// each OTHER (that would be a test artifact). The probe is writer-vs-Allocate:
	// concurrent mutation of the caller's backing array against the pool's read.
	writers := len(original)

	var writersWG, readersWG sync.WaitGroup
	stop := make(chan struct{})
	errc := make(chan string, readers*iters)

	// Writers: scribble on their OWN element of the CALLER's original slice
	// concurrently until signaled to stop. If the pool aliased this slice, -race
	// fires here against Allocate's read and the answer corrupts.
	for w := 0; w < writers; w++ {
		writersWG.Add(1)
		go func(w int) {
			defer writersWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				original[w].FreeCapacity = -1
				original[w].Healthy = w%2 == 0
				original[w].ID = "CORRUPTED"
				original[w].Tier = TierDecentralized
			}
		}(w)
	}

	// Readers: hammer Allocate; every answer must be the ORIGINAL oracle winner.
	for r := 0; r < readers; r++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for i := 0; i < iters; i++ {
				got, err := m.Allocate(spec)
				if err != nil {
					errc <- "unexpected error: " + err.Error()
					return
				}
				if got.ID != adversarialWinner {
					errc <- "got " + got.ID + " want " + adversarialWinner
					return
				}
			}
		}()
	}

	// Deterministic join: readers are bounded by iters and finish on their own;
	// once they're all done, signal the (unbounded) writers to stop, then drain.
	readersWG.Wait()
	close(stop)
	writersWG.Wait()
	close(errc)

	bad := 0
	for msg := range errc {
		if bad < 5 {
			t.Errorf("run=%s reader saw corruption leak: %s", id, msg)
		}
		bad++
	}
	if bad > 0 {
		t.Fatalf("run=%s defensive-copy ownership violated: %d reader observations were corrupted by external mutation", id, bad)
	}
	t.Logf("run=%s pool answer immune to %d concurrent external mutators across %d readers; -race clean if no DATA RACE above (defensive copy is load-bearing)",
		id, writers, readers)
}

// TestAdversarial_ConcurrentFilterAndSelectPureNoRace shares ONE device slice
// across many goroutines calling the pure Filter and Select. These must neither
// race nor mutate the shared input. SINK-SIDE we assert the input slice is
// byte-identical after the storm (no in-place mutation) AND every Select result
// equals the no-sort oracle — proving purity, not just no-panic.
func TestAdversarial_ConcurrentFilterAndSelectPureNoRace(t *testing.T) {
	id := runUUID(t)
	devices := []Device{
		{ID: "d-cloud", Tier: TierCloud, VRAMGiB: 80, Utilization: 0.02, CostPerHour: 0.10, Labels: map[string]bool{"TEE": true}},
		{ID: "d-local-busy", Tier: TierLocal, VRAMGiB: 48, Utilization: 0.90, CostPerHour: 7.00, Labels: map[string]bool{"TEE": true}},
		{ID: "d-local-idle", Tier: TierLocal, VRAMGiB: 40, Utilization: 0.10, CostPerHour: 3.00, Labels: map[string]bool{"TEE": true}},
		{ID: "d-small", Tier: TierLocal, VRAMGiB: 16, Utilization: 0.00, CostPerHour: 0.00, Labels: map[string]bool{"TEE": true}},
	}
	spec := WorkloadSpec{MinVRAMGiB: 40, RequireLabels: []string{"TEE"}}

	// Snapshot of the shared input for an after-the-storm mutation check.
	type snap struct {
		ID   string
		VRAM int
		Tier GPUTier
		Util float64
		Cost float64
		TEE  bool
	}
	before := make([]snap, len(devices))
	for i, d := range devices {
		before[i] = snap{d.ID, d.VRAMGiB, d.Tier, d.Utilization, d.CostPerHour, d.Labels["TEE"]}
	}

	// Oracle: Filter then min-scan Select over the fixed fleet => d-local-idle.
	const wantSelect = "d-local-idle"

	const goroutines = 256
	const perG = 200
	got := make([]string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	s := NewPriorityScheduler()
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			last := ""
			for i := 0; i < perG; i++ {
				cands := Filter(spec, devices) // shared devices, concurrent read
				sel, err := s.Select(cands)
				if err != nil {
					last = "ERR:" + err.Error()
					continue
				}
				last = sel.ID
			}
			got[g] = last
		}(g)
	}
	wg.Wait()

	for g, r := range got {
		if r != wantSelect {
			t.Fatalf("run=%s goroutine %d Filter+Select => %q, want oracle %q", id, g, r, wantSelect)
		}
	}

	// SINK-SIDE purity: the shared input must be byte-identical (no in-place
	// mutation by Filter/Select). A mutation would surface here even if -race
	// happened not to schedule a conflicting pair.
	for i, d := range devices {
		b := before[i]
		if d.ID != b.ID || d.VRAMGiB != b.VRAM || d.Tier != b.Tier ||
			d.Utilization != b.Util || d.CostPerHour != b.Cost || d.Labels["TEE"] != b.TEE {
			t.Fatalf("run=%s shared input device[%d] mutated by Filter/Select: before=%+v after=%+v", id, i, b, d)
		}
	}
	t.Logf("run=%s %d goroutines x %d Filter+Select all => %q; shared input unmutated; -race clean if no DATA RACE above",
		id, goroutines, perG, wantSelect)
}
