package local

// Adversarial probes for pkg/local. Package-internal so we can hit the
// unexported map directly and assert SINK-SIDE state (the stored map, the
// returned $/hr, the boolean verdict) rather than "it didn't panic".
//
// CLASS MAP (honest):
//
//	(a) UNGUARDED SHARED STATE  -> REAL surface. Register (Lock) + lookup
//	    family (RLock) over a shared map. Doc: "safe for concurrent use".
//	    Probed under -race with a conservation invariant.
//	(b) TOTAL-ORDER / DETERMINISM -> NO map-ordered surface. The registrar
//	    never ranges the map nor emits any ordered slice; the cost model is
//	    pure float arithmetic. We do NOT fabricate an ordering bug; instead
//	    we pin the real determinism contract: identical facts -> identical
//	    $/hr across repeated and concurrent reads.
//	(c) VALIDATION / FAIL-OPEN -> REAL surface. validate() gates with
//	    <=0 / <0 / >1 comparisons. NaN/+Inf are the adversarial inputs: all
//	    IEEE-754 comparisons against NaN are false, so a NaN field slips
//	    every guard and is STORED, then poisons every downstream TCO. +Inf
//	    price likewise satisfies ">0". This is the fail-open seam.
//	(d) NUMERIC / BOUNDARY -> CheaperThanCloud strict-<. The adversarial
//	    edge is NaN propagation: once a NaN TCO is admitted (via (c)), the
//	    comparison is silently false in BOTH directions — the scheduler can
//	    never burst to cloud for that card.
//
// Discrimination of each finding is in the FINAL REPORT, not here.

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// (a) UNGUARDED SHARED STATE — concurrency, -race + conservation.
// ---------------------------------------------------------------------------

// TestConcurrent_RegisterAndRead_RaceAndConservation hammers Register (writer)
// concurrently with EffectiveHourlyCost / CheaperThanCloud / lookup (readers).
// Under -race a missing/!held lock on the shared map would be flagged. The
// SINK-SIDE conservation assertion: every distinct id that returned nil from
// Register MUST be present and queryable afterward, with its exact TCO — no
// lost writes, no torn map entries.
//
// Contract pinned: LocalGPURegistrar is "safe for concurrent use".
func TestConcurrent_RegisterAndRead_RaceAndConservation(t *testing.T) {
	r := NewLocalGPURegistrar()

	const writers = 64
	var wg sync.WaitGroup

	// Each writer registers a unique id; we record the ones that succeeded so
	// we can prove conservation sink-side.
	registered := make([]string, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g := canonicalGPU()
			g.ID = fmt.Sprintf("gpu-%03d", i)
			if err := r.Register(g); err != nil {
				t.Errorf("Register(%s): %v", g.ID, err)
				return
			}
			registered[i] = g.ID
		}(i)
	}

	// Concurrent readers churning the RLock path while writes are in flight.
	stop := make(chan struct{})
	var rwg sync.WaitGroup
	for i := 0; i < 32; i++ {
		rwg.Add(1)
		go func() {
			defer rwg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					// Mix of hits and misses; we only care that the locked
					// path is exercised under the race detector.
					_, _ = r.EffectiveHourlyCost("gpu-000")
					_, _ = r.CheaperThanCloud("gpu-031", 2.0)
					_, _ = r.lookup("gpu-063")
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	rwg.Wait()

	// SINK-SIDE conservation: every successfully-registered id is present with
	// the exact canonical TCO (1.0625). A lost update would drop an id; a torn
	// write would yield a wrong/zero TCO.
	for _, id := range registered {
		if id == "" {
			t.Fatalf("a writer failed to register (empty id slot)")
		}
		got, err := r.EffectiveHourlyCost(id)
		if err != nil {
			t.Fatalf("post-run EffectiveHourlyCost(%s): %v", id, err)
		}
		if round4(got) != 1.0625 {
			t.Fatalf("post-run TCO(%s) = %v; want 1.0625 (torn/lost write)", id, round4(got))
		}
	}

	// Map cardinality must equal the writer count exactly: no dropped, no
	// duplicated entries.
	r.mu.RLock()
	n := len(r.gpus)
	r.mu.RUnlock()
	if n != writers {
		t.Fatalf("registrar holds %d gpus; want %d (lost/duplicate writes)", n, writers)
	}
}

// TestConcurrent_DuplicateRace_ExactlyOneWinner fires many Register calls for
// the SAME id concurrently. The duplicate guard sits under the same Lock as the
// store, so the contract is: exactly one succeeds, all others get
// ErrDuplicateID, and the survivor's facts are whatever the single winner
// stored — never a torn blend. SINK-SIDE: success count == 1 and the stored
// price is one of the contended prices (atomic, not interleaved).
func TestConcurrent_DuplicateRace_ExactlyOneWinner(t *testing.T) {
	r := NewLocalGPURegistrar()

	const n = 50
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		ok       int
		dupErrs  int
		otherErr int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g := canonicalGPU()
			g.ID = "contended"
			// Each goroutine offers a distinct, valid price so a torn write
			// would be detectable as an out-of-set value.
			g.PurchasePrice = 30000 + float64(i)
			err := r.Register(g)
			mu.Lock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrDuplicateID):
				dupErrs++
			default:
				otherErr++
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if ok != 1 {
		t.Fatalf("Register winners = %d; want exactly 1", ok)
	}
	if otherErr != 0 {
		t.Fatalf("unexpected non-duplicate errors = %d; want 0", otherErr)
	}
	if dupErrs != n-1 {
		t.Fatalf("ErrDuplicateID count = %d; want %d", dupErrs, n-1)
	}

	// SINK-SIDE: stored price is exactly one of the offered values, intact.
	g, exists := r.lookup("contended")
	if !exists {
		t.Fatalf("contended id missing after race")
	}
	if g.PurchasePrice < 30000 || g.PurchasePrice >= 30000+n {
		t.Fatalf("stored price %v outside offered set [30000,%d) (torn write)", g.PurchasePrice, 30000+n)
	}
}

// ---------------------------------------------------------------------------
// (b) DETERMINISM — no map-ordered surface; pin pure repeatability instead.
// ---------------------------------------------------------------------------

// TestDeterministic_RepeatedReads pins the real determinism contract for this
// package: the cost model is pure, so the same registered facts yield the same
// $/hr on every read, including under concurrency. There is NO map-iteration /
// ordered-output surface to attack here (the registrar never ranges its map),
// so this is a contract pin, not a bug hunt.
func TestDeterministic_RepeatedReads(t *testing.T) {
	r := NewLocalGPURegistrar()
	if err := r.Register(canonicalGPU()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	first, err := r.EffectiveHourlyCost("h100-0")
	if err != nil {
		t.Fatalf("EffectiveHourlyCost: %v", err)
	}

	results := make([]float64, 200)
	var wg sync.WaitGroup
	for i := 0; i < len(results); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := r.EffectiveHourlyCost("h100-0")
			if err != nil {
				t.Errorf("read %d: %v", i, err)
				return
			}
			results[i] = v
		}(i)
	}
	wg.Wait()

	for i, v := range results {
		if v != first { // exact bit-for-bit: pure arithmetic, same inputs
			t.Fatalf("read %d = %v; want bit-identical %v (nondeterministic)", i, v, first)
		}
	}
}

// ---------------------------------------------------------------------------
// (c) VALIDATION / FAIL-OPEN — NaN / +Inf slip the range guards.
// ---------------------------------------------------------------------------

// TestRegister_RejectsNonFinite is the adversarial validation probe. validate()
// gates numeric fields with ordered comparisons (<=0, <0, >1). Against a NaN,
// every such comparison is false, so a NaN field passes ALL guards and is
// stored; +Inf likewise satisfies ">0" and "<=1"-style checks where relevant.
// A stored non-finite field then poisons the TCO (NaN/Inf $/hr).
//
// SINK-SIDE assertions:
//  1. Register MUST reject each non-finite spec with ErrInvalidGPU.
//  2. If it (wrongly) accepts, the resulting EffectiveHourlyCost is non-finite
//     — we assert that too, so the failure message proves the poison, not just
//     "validation missing".
//
// This test FAILS on unfixed prod (NaN/+Inf are currently admitted). It is
// NOT env-gated: it is a real, reproducible fail-open defect, left red by
// design per the protocol. See FINAL REPORT for the minimal fix.
func TestRegister_RejectsNonFinite(t *testing.T) {
	base := canonicalGPU()
	mut := func(f func(*GPU)) GPU { g := base; f(&g); return g }

	cases := []struct {
		name string
		gpu  GPU
	}{
		{"NaN price", mut(func(g *GPU) { g.PurchasePrice = math.NaN() })},
		{"Inf price", mut(func(g *GPU) { g.PurchasePrice = math.Inf(1) })},
		{"NaN lifespan", mut(func(g *GPU) { g.LifespanYears = math.NaN() })},
		{"NaN watts", mut(func(g *GPU) { g.PowerDrawWatts = math.NaN() })},
		{"Inf watts", mut(func(g *GPU) { g.PowerDrawWatts = math.Inf(1) })},
		{"NaN tariff", mut(func(g *GPU) { g.PowerPriceKWh = math.NaN() })},
		{"NaN utilization", mut(func(g *GPU) { g.Utilization = math.NaN() })},
		{"Inf utilization", mut(func(g *GPU) { g.Utilization = math.Inf(1) })},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewLocalGPURegistrar()
			err := r.Register(tc.gpu)
			if errors.Is(err, ErrInvalidGPU) {
				return // correctly rejected
			}
			// Fail-open: it was admitted. Prove the poison sink-side.
			tco, qerr := r.EffectiveHourlyCost(tc.gpu.ID)
			t.Fatalf("Register(%s) err = %v; want ErrInvalidGPU. "+
				"Admitted spec yields non-finite TCO=%v (finite=%v, queryErr=%v) — fail-open",
				tc.name, err, tco, !math.IsInf(tco, 0) && !math.IsNaN(tco), qerr)
		})
	}
}

// TestCheaperThanCloud_NaNPoison shows the downstream consequence of the (c)
// fail-open in the (d) numeric-boundary surface: if a NaN-utilization card is
// admitted, its TCO is NaN, and `NaN < cloudHourly` is false for EVERY
// cloudHourly. The scheduler would therefore NEVER burst that card to cloud,
// no matter how cheap the cloud offer — a silent stuck-local decision.
//
// This also FAILS on unfixed prod (the card is admitted). Once validation
// rejects non-finite specs, the card is never registered and CheaperThanCloud
// returns ErrUnknownGPU instead — which this test accepts as the fixed state.
func TestCheaperThanCloud_NaNPoison(t *testing.T) {
	r := NewLocalGPURegistrar()
	g := canonicalGPU()
	g.ID = "poison"
	g.Utilization = math.NaN()

	err := r.Register(g)
	if errors.Is(err, ErrInvalidGPU) {
		// Fixed behavior: rejected at the door. Confirm it is truly absent.
		if _, qerr := r.CheaperThanCloud("poison", 1e9); !errors.Is(qerr, ErrUnknownGPU) {
			t.Fatalf("after rejection, CheaperThanCloud err = %v; want ErrUnknownGPU", qerr)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected Register error: %v", err)
	}

	// Unfixed behavior: admitted. Demonstrate the stuck-local poison.
	tco, _ := r.EffectiveHourlyCost("poison")
	if !math.IsNaN(tco) {
		t.Fatalf("expected NaN TCO from NaN utilization, got %v", tco)
	}
	// Even an absurdly cheap cloud offer cannot win against a NaN local TCO.
	cheaper, err := r.CheaperThanCloud("poison", 1e-9)
	if err != nil {
		t.Fatalf("CheaperThanCloud: %v", err)
	}
	t.Fatalf("NaN-TCO card reports CheaperThanCloud(1e-9)=%v; the card is "+
		"silently stuck-local (NaN<x is always false) — admitted by fail-open validation", cheaper)
}
