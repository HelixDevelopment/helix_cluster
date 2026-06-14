package smartrouter

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// ===========================================================================
// AUDIT TESTS — transitivity / order-independence, oracle correctness, and
// concurrency (-race). These were added to audit pkg/smartrouter for the two
// bug classes flagged in a sibling package (pkg/qos):
//
//  1. An INTRANSITIVE near-tie "band" comparator fed to sort.Slice that makes
//     the chosen winner order-dependent across input permutations.
//  2. A PARTIAL-LOCK race where a read-looking accessor reads mutex-guarded
//     state without holding the lock while a concurrent writer mutates it.
//
// FINDINGS (proven by the tests below):
//   - smartrouter performs selection via single-pass argmin/argmax loops over a
//     TOTAL order (routeLatency '<', routeThroughput '>', routeCost '<',
//     routeBalanced weighted-score '>'). There is NO sort.Slice and NO relative
//     near-tie band, so there is no intransitive comparator to anchor. The only
//     order-sensitivity is the documented FIRST-SEEN tie-break on EXACTLY-equal
//     keys; TestRoute_OrderIndependence_DistinctKeys pins that distinct-key
//     selection is fully permutation-invariant, and the oracle test pins the
//     tie-break itself.
//   - Both Router methods (Route, Decisions) hold r.mu for their entire body;
//     every access to r.models / r.decisions is guarded.
//     TestRoute_ConcurrentRouteAndDecisions_NoRace exercises the read+write
//     interleave under -race to prove no partial-lock race.
// ===========================================================================

// permutations generates all permutations of the input slice (Heap's algorithm).
// Pools used here are small (<= 6) so factorial blow-up is bounded.
func permutations(in []Model) [][]Model {
	var out [][]Model
	a := make([]Model, len(in))
	copy(a, in)
	var gen func(k int)
	gen = func(k int) {
		if k == 1 {
			cp := make([]Model, len(a))
			copy(cp, a)
			out = append(out, cp)
			return
		}
		for i := 0; i < k; i++ {
			gen(k - 1)
			if k%2 == 0 {
				a[i], a[k-1] = a[k-1], a[i]
			} else {
				a[0], a[k-1] = a[k-1], a[0]
			}
		}
	}
	if len(a) == 0 {
		return [][]Model{{}}
	}
	gen(len(a))
	return out
}

// ---------------------------------------------------------------------------
// 1. ORDER-INDEPENDENCE
//
// For every strategy, over MANY model fleets (including deliberate near-tie
// chains where each model is "close to" its neighbour) with DISTINCT decisive
// keys, the chosen backend MUST be identical across ALL input permutations.
//
// WHY THIS BITES an intransitive band comparator: a relative "within X of each
// OTHER → secondary key" comparator is non-transitive, so sort.Slice /
// sort.SliceStable would pick an order-dependent winner. Feeding the SAME fleet
// in every permutation and asserting one single winner would then fail. The
// near-tie chains below (e.g. TTFT 10,11,12,13,14 with epsilon=2) are exactly
// the pathological "A~B, B~C, but A!~C" inputs that expose intransitivity.
// smartrouter has no such band, so this test must (and does) pass — and would
// FAIL if a band were introduced.
// ---------------------------------------------------------------------------

func TestRoute_OrderIndependence_DistinctKeys(t *testing.T) {
	allStrategies := []Strategy{
		StrategyLatency, StrategyThroughput, StrategyCost, StrategyTEE, StrategyBalanced,
	}

	// Each fleet has DISTINCT decisive keys per strategy so the argmin/argmax is
	// unique — meaning a correct, order-independent router yields ONE winner
	// regardless of permutation. Near-tie chains stress a hypothetical band.
	fleets := map[string][]Model{
		"near-tie-ttft-chain": {
			// TTFT 10,11,12,13,14 — adjacent pairs are "near" (would fall in any
			// small epsilon band) but the global min is unambiguously m10.
			{ID: "m10", TTFTMillis: 10, ThroughputTokS: 105, CostPerMTokens: 1.4, TEE: true, Healthy: true},
			{ID: "m11", TTFTMillis: 11, ThroughputTokS: 104, CostPerMTokens: 1.3, TEE: true, Healthy: true},
			{ID: "m12", TTFTMillis: 12, ThroughputTokS: 103, CostPerMTokens: 1.2, TEE: true, Healthy: true},
			{ID: "m13", TTFTMillis: 13, ThroughputTokS: 102, CostPerMTokens: 1.1, TEE: true, Healthy: true},
			{ID: "m14", TTFTMillis: 14, ThroughputTokS: 101, CostPerMTokens: 1.0, TEE: true, Healthy: true},
		},
		"mixed-health": {
			{ID: "fast-unhealthy", TTFTMillis: 1, ThroughputTokS: 9999, CostPerMTokens: 0.01, TEE: true, Healthy: false},
			{ID: "a", TTFTMillis: 7, ThroughputTokS: 300, CostPerMTokens: 0.9, TEE: false, Healthy: true},
			{ID: "b", TTFTMillis: 8, ThroughputTokS: 600, CostPerMTokens: 0.7, TEE: true, Healthy: true},
			{ID: "c", TTFTMillis: 9, ThroughputTokS: 450, CostPerMTokens: 0.5, TEE: true, Healthy: true},
		},
		"spread": {
			{ID: "p", TTFTMillis: 3, ThroughputTokS: 800, CostPerMTokens: 2.0, TEE: false, Healthy: true},
			{ID: "q", TTFTMillis: 30, ThroughputTokS: 1200, CostPerMTokens: 0.3, TEE: true, Healthy: true},
			{ID: "rr", TTFTMillis: 15, ThroughputTokS: 100, CostPerMTokens: 1.1, TEE: true, Healthy: true},
		},
		// intransitive-flip-triple — the SHARP probe. TTFT {1,3,5}: pairs (1,3)
		// and (3,5) are "near" (Δ<3) but (1,5) is "far" (Δ=5). Throughputs
		// {50,90,99} are chosen so a PAIRWISE near-tie band comparator
		// ("within 3ms of EACH OTHER → prefer higher throughput") is INTRANSITIVE
		// and, under sort.SliceStable, yields 3 DIFFERENT winners across the 6
		// permutations (empirically verified by injecting that exact bug). The
		// correct global-argmin (lowest TTFT = "A", TTFT=1) is permutation-
		// invariant, so this fleet is the one that would FAIL loudly the instant
		// anyone replaces the argmin with an intransitive band — the pkg/qos bug
		// class. (Latency is the decisive strategy here; the others remain
		// unambiguous global extrema.)
		"intransitive-flip-triple": {
			{ID: "A", TTFTMillis: 1, ThroughputTokS: 50, CostPerMTokens: 1.0, TEE: true, Healthy: true},
			{ID: "B", TTFTMillis: 3, ThroughputTokS: 90, CostPerMTokens: 0.9, TEE: true, Healthy: true},
			{ID: "C", TTFTMillis: 5, ThroughputTokS: 99, CostPerMTokens: 0.8, TEE: true, Healthy: true},
		},
	}

	for fleetName, fleet := range fleets {
		perms := permutations(fleet)
		for _, s := range allStrategies {
			// Establish the canonical winner from the identity ordering.
			canonical, canonicalErr := New(fleet).Route("canon", s)

			for pi, perm := range perms {
				got, err := New(perm).Route("perm", s)
				if (err == nil) != (canonicalErr == nil) {
					t.Fatalf("[%s/%s] perm#%d error-presence differs: canonical err=%v, perm err=%v",
						fleetName, s, pi, canonicalErr, err)
				}
				if got != canonical {
					t.Errorf("[%s/%s] ORDER-DEPENDENT winner: perm#%d chose %q but canonical is %q (perm=%s)",
						fleetName, s, pi, got, canonical, ids(perm))
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 2. ROUTING CORRECTNESS vs INDEPENDENT ORACLE
//
// The chosen backend MUST equal the independently-computed argmin/argmax
// (with the documented FIRST-SEEN tie-break) for every strategy. WHY THIS
// BITES: if any comparator picked the wrong rank (e.g. inverted '<'/'>' or a
// band that overrode the true extremum), the oracle disagreement fails here.
// ---------------------------------------------------------------------------

func TestRoute_OracleArgExtremum(t *testing.T) {
	fleets := [][]Model{
		testPool(),
		{
			{ID: "solo", TTFTMillis: 5, ThroughputTokS: 200, CostPerMTokens: 1.0, TEE: true, Healthy: true},
		},
		{
			{ID: "h1", TTFTMillis: 4, ThroughputTokS: 700, CostPerMTokens: 0.6, TEE: false, Healthy: true},
			{ID: "h2", TTFTMillis: 4, ThroughputTokS: 700, CostPerMTokens: 0.6, TEE: false, Healthy: true}, // exact tie with h1
			{ID: "h3", TTFTMillis: 6, ThroughputTokS: 650, CostPerMTokens: 0.9, TEE: true, Healthy: true},
		},
	}

	for fi, fleet := range fleets {
		for _, s := range []Strategy{StrategyLatency, StrategyThroughput, StrategyCost, StrategyTEE, StrategyBalanced} {
			want, wantOK := oracle(fleet, s)
			got, err := New(fleet).Route("oracle", s)

			if (err == nil) != wantOK {
				t.Fatalf("fleet#%d/%s: oracle ok=%v, router err=%v", fi, s, wantOK, err)
			}
			if !wantOK {
				continue
			}
			if got != want {
				t.Errorf("fleet#%d/%s: oracle wants %q, router got %q", fi, s, want, got)
			}
		}
	}
}

// oracle re-implements the documented selection rule independently of the
// production code path, using the SAME first-seen tie-break (iterate in slice
// order; only a STRICTLY better candidate replaces the incumbent).
func oracle(pool []Model, s Strategy) (string, bool) {
	var healthy []Model
	for _, m := range pool {
		if m.Healthy {
			healthy = append(healthy, m)
		}
	}
	if len(healthy) == 0 {
		return "", false
	}
	switch s {
	case StrategyLatency:
		best := healthy[0]
		for _, m := range healthy[1:] {
			if m.TTFTMillis < best.TTFTMillis {
				best = m
			}
		}
		return best.ID, true
	case StrategyThroughput:
		best := healthy[0]
		for _, m := range healthy[1:] {
			if m.ThroughputTokS > best.ThroughputTokS {
				best = m
			}
		}
		return best.ID, true
	case StrategyCost:
		best := healthy[0]
		for _, m := range healthy[1:] {
			if m.CostPerMTokens < best.CostPerMTokens {
				best = m
			}
		}
		return best.ID, true
	case StrategyTEE:
		var tee []Model
		for _, m := range healthy {
			if m.TEE {
				tee = append(tee, m)
			}
		}
		if len(tee) == 0 {
			return "", false
		}
		best := tee[0]
		for _, m := range tee[1:] {
			if m.CostPerMTokens < best.CostPerMTokens {
				best = m
			}
		}
		return best.ID, true
	case StrategyBalanced:
		bestScore := -1.0
		bestID := ""
		for _, m := range healthy {
			if m.TTFTMillis == 0 || m.CostPerMTokens == 0 {
				continue
			}
			score := wTTFT*(1/m.TTFTMillis) + wTput*m.ThroughputTokS + wCost*(1/m.CostPerMTokens)
			if score > bestScore {
				bestScore = score
				bestID = m.ID
			}
		}
		if bestID == "" {
			return "", false
		}
		return bestID, true
	}
	return "", false
}

// ---------------------------------------------------------------------------
// 3. CONCURRENCY (-race)
//
// Route reads+writes r.models/r.decisions; Decisions reads r.decisions. Many
// goroutines route concurrently while others read the Decision log. WHY THIS
// BITES a partial-lock: if any guarded-state access dropped the lock, the Go
// race detector flags the concurrent map/slice access here. With full locking
// it is clean. Run under -race (-count>=10) for the interleave to surface.
// ---------------------------------------------------------------------------

func TestRoute_ConcurrentRouteAndDecisions_NoRace(t *testing.T) {
	r := New(testPool())
	strategies := []Strategy{
		StrategyLatency, StrategyThroughput, StrategyCost, StrategyTEE, StrategyBalanced,
	}

	const writers = 8
	const readers = 4
	const iters = 200

	var wg sync.WaitGroup
	var routeCount int64

	// Writers: concurrent Route (reads models, appends to decisions).
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				s := strategies[(w+i)%len(strategies)]
				if _, err := r.Route(fmt.Sprintf("w%d-i%d", w, i), s); err != nil {
					t.Errorf("Route error: %v", err)
					return
				}
				atomic.AddInt64(&routeCount, 1)
			}
		}(w)
	}

	// Readers: concurrent Decisions() snapshots racing the writers.
	for rd := 0; rd < readers; rd++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_ = r.Decisions()
			}
		}()
	}

	wg.Wait()

	got := len(r.Decisions())
	want := writers * iters
	if int64(got) != routeCount || got != want {
		t.Errorf("decision count mismatch: log=%d routeCount=%d want=%d (lost appends imply a race)",
			got, routeCount, want)
	}
}

// ---------------------------------------------------------------------------
// 4. EDGE CASES — no panic on single / all-equal / empty pools.
// ---------------------------------------------------------------------------

func TestRoute_EdgeCases(t *testing.T) {
	strategies := []Strategy{
		StrategyLatency, StrategyThroughput, StrategyCost, StrategyTEE, StrategyBalanced,
	}

	t.Run("empty", func(t *testing.T) {
		r := New(nil)
		for _, s := range strategies {
			_, err := r.Route("empty", s)
			if err == nil {
				t.Errorf("%s: expected ErrNoEligibleModel for empty pool, got nil", s)
			}
		}
	})

	t.Run("single-healthy", func(t *testing.T) {
		r := New([]Model{
			{ID: "only", TTFTMillis: 5, ThroughputTokS: 300, CostPerMTokens: 1.0, TEE: true, Healthy: true},
		})
		for _, s := range strategies {
			got, err := r.Route("single", s)
			if err != nil {
				t.Errorf("%s: unexpected error on single-model pool: %v", s, err)
				continue
			}
			if got != "only" {
				t.Errorf("%s: want %q, got %q", s, "only", got)
			}
		}
	})

	t.Run("all-equal", func(t *testing.T) {
		// Every model identical except ID — exercises tie-break paths; must not
		// panic and must return some healthy member deterministically (first-seen).
		pool := []Model{
			{ID: "e1", TTFTMillis: 7, ThroughputTokS: 400, CostPerMTokens: 1.1, TEE: true, Healthy: true},
			{ID: "e2", TTFTMillis: 7, ThroughputTokS: 400, CostPerMTokens: 1.1, TEE: true, Healthy: true},
			{ID: "e3", TTFTMillis: 7, ThroughputTokS: 400, CostPerMTokens: 1.1, TEE: true, Healthy: true},
		}
		for _, s := range strategies {
			got, err := New(pool).Route("equal", s)
			if err != nil {
				t.Errorf("%s: unexpected error on all-equal pool: %v", s, err)
				continue
			}
			if got != "e1" {
				t.Errorf("%s: first-seen tie-break should pick %q, got %q", s, "e1", got)
			}
		}
	})
}

// ids renders a permutation's model IDs for failure messages.
func ids(pool []Model) string {
	out := make([]string, len(pool))
	for i, m := range pool {
		out[i] = m.ID
	}
	return fmt.Sprintf("%v", out)
}
