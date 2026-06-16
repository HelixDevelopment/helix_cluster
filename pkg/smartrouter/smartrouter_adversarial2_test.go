package smartrouter

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"testing"
)

// ===========================================================================
// ADVERSARIAL PASS 2 — DEEPER probes of routing CORRECTNESS, distinct from the
// already-fixed NaN/±Inf comparator bug (see smartrouter_adversarial_test.go)
// and the order-independence/oracle/race-count audit (smartrouter_audit_test.go).
//
// This pass targets gaps the prior tests do NOT cover:
//
//   1. SINK-SIDE concurrency CORRECTNESS, not just count. The existing race
//      test (TestRoute_ConcurrentRouteAndDecisions_NoRace) asserts only that
//      len(Decisions)==expected. It never checks that each recorded ModelID is
//      the CORRECT backend for its strategy. A router that, under concurrency,
//      appended a Decision naming the wrong (or a stale/unhealthy) backend would
//      still pass the count test. TestAdv2_ConcurrentDecisionsAreCorrect closes
//      that: every recorded Decision's ModelID must equal the independent
//      argmin/argmax oracle for its strategy. (Run under -race.)
//
//   2. routeBalanced's INTERNAL guard differs from the other routes. Latency /
//      Throughput / Cost use the NaN/±Inf-safe betterMin/betterMax (which fall
//      back to a deterministic first-seen pick when every key is poisoned).
//      routeBalanced instead (a) seeds bestScore := -1.0 and (b) skips any
//      ±Inf/NaN score — so a healthy pool whose BEST score is < -1.0, or whose
//      every score is ±Inf, yields ErrNoEligibleModel even though healthy
//      models exist. These probes PIN that asymmetry as the CURRENT contract
//      (they pass on today's code) so a future refactor cannot silently flip
//      "no backend" <-> "some backend" without a failing test. Classified
//      LATENT RISK in the report (only triggers on out-of-domain negative/Inf
//      metrics), hence PINNED, not "fixed".
//
//   3. Non-decisive-key poison must not perturb the decisive route. A glitch in
//      a metric the strategy does not rank on (e.g. negative throughput while
//      routing by Latency) must neither change the winner nor break determinism.
//
// All funcs here PASS on the code left behind (no production change made).
// ===========================================================================

// ---------------------------------------------------------------------------
// 1. CONCURRENCY CORRECTNESS (sink-side) — every recorded Decision names the
//    backend the oracle says is correct for that strategy. Run with -race.
// ---------------------------------------------------------------------------

func TestAdv2_ConcurrentDecisionsAreCorrect(t *testing.T) {
	pool := testPool()
	r := New(pool)

	strategies := []Strategy{
		StrategyLatency, StrategyThroughput, StrategyCost, StrategyTEE, StrategyBalanced,
	}

	// Precompute the single correct winner per strategy from the independent
	// oracle (re-used from the audit test file, same package).
	want := map[Strategy]string{}
	for _, s := range strategies {
		id, ok := oracle(pool, s)
		if !ok {
			t.Fatalf("oracle has no winner for %s on testPool()", s)
		}
		want[s] = id
	}

	const writers = 8
	const iters = 150
	var wg sync.WaitGroup

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				s := strategies[(w+i)%len(strategies)]
				got, err := r.Route(fmt.Sprintf("w%d-i%d-%s", w, i, s), s)
				if err != nil {
					t.Errorf("Route(%s) error: %v", s, err)
					return
				}
				// Immediate sink-side check on the return value.
				if got != want[s] {
					t.Errorf("Route(%s) returned %q, want %q", s, got, want[s])
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// Sink-side check on the persisted Decision log: EVERY recorded decision
	// must name the correct backend for its strategy. A concurrency bug that
	// raced the wrong ModelID into the log is caught here even though the count
	// is right.
	decisions := r.Decisions()
	if len(decisions) != writers*iters {
		t.Fatalf("decision count: got %d want %d", len(decisions), writers*iters)
	}
	for _, d := range decisions {
		if exp, ok := want[d.Strategy]; !ok {
			t.Errorf("decision with unknown strategy: %+v", d)
		} else if d.ModelID != exp {
			t.Errorf("MIS-ROUTED in log: strategy %s recorded %q, oracle requires %q (RunID=%s)",
				d.Strategy, d.ModelID, exp, d.RunID)
		}
	}
}

// ---------------------------------------------------------------------------
// 2a. PIN: routeBalanced returns ErrNoEligibleModel when the BEST healthy
//     model's weighted score is below the -1.0 sentinel. This is the current
//     (documented-by-implementation) behavior. LATENT RISK: a healthy backend
//     exists but the router reports "no eligible model" — a "routed nothing"
//     sink-side outcome that the min/max routes do NOT exhibit (they always
//     return a healthy backend). Pinned so a refactor of the sentinel surfaces
//     as a failing test rather than a silent contract change.
// ---------------------------------------------------------------------------

func TestAdv2_Balanced_SentinelPhantomNoEligible_Pinned(t *testing.T) {
	// Single healthy model whose score is < -1.0:
	//   TTFT=10 -> 0.4*0.1 = 0.04
	//   tput=1  -> 0.3*1   = 0.30
	//   cost=-0.1 -> 0.3*(1/-0.1) = -3.0  => score = -2.66  (< -1.0 sentinel)
	pool := []Model{
		{ID: "only", TTFTMillis: 10, ThroughputTokS: 1, CostPerMTokens: -0.1, Healthy: true},
	}
	id, err := New(pool).Route("adv2-sentinel", StrategyBalanced)

	// CURRENT contract: phantom "no eligible model" despite a healthy backend.
	if !errors.Is(err, ErrNoEligibleModel) {
		t.Fatalf("balanced sentinel contract changed: got id=%q err=%v; "+
			"if routeBalanced now returns the healthy backend for sub-(-1.0) "+
			"scores, update this pin AND the package doc", id, err)
	}
	if id != "" {
		t.Errorf("expected empty id with ErrNoEligibleModel, got %q", id)
	}

	// Contrast: the SAME single healthy model routes fine under the min/max
	// strategies (they never reject a healthy backend). This documents the
	// asymmetry sink-side.
	for _, s := range []Strategy{StrategyLatency, StrategyThroughput, StrategyCost} {
		got, err := New(pool).Route("adv2-sentinel-contrast-"+string(s), s)
		if err != nil {
			t.Errorf("[%s] min/max route must accept the lone healthy backend, got err=%v", s, err)
		}
		if got != "only" {
			t.Errorf("[%s] want %q, got %q", s, "only", got)
		}
	}
}

// 2b. PIN: routeBalanced skips ±Inf scores, so an all-Inf-score healthy pool
//     yields ErrNoEligibleModel — whereas an all-±Inf-key min/max pool returns
//     a deterministic healthy fallback (pool[0]). Pins the asymmetry.
func TestAdv2_Balanced_AllInfScore_vs_MinMaxFallback_Pinned(t *testing.T) {
	inf := math.Inf(1)

	balPool := []Model{
		{ID: "a", TTFTMillis: 10, ThroughputTokS: inf, CostPerMTokens: 1.0, Healthy: true},
		{ID: "b", TTFTMillis: 20, ThroughputTokS: inf, CostPerMTokens: 1.0, Healthy: true},
	}
	if id, err := New(balPool).Route("adv2-balinf", StrategyBalanced); !errors.Is(err, ErrNoEligibleModel) {
		t.Fatalf("balanced all-Inf-score contract changed: got id=%q err=%v", id, err)
	}

	// Min/max contrast: all-Inf decisive key -> deterministic first-seen healthy
	// fallback (NOT an error). Pinned across permutations for determinism.
	latPool := []Model{
		{ID: "x", TTFTMillis: inf, ThroughputTokS: 1, CostPerMTokens: 1.0, Healthy: true},
		{ID: "y", TTFTMillis: inf, ThroughputTokS: 1, CostPerMTokens: 1.0, Healthy: true},
		{ID: "z", TTFTMillis: inf, ThroughputTokS: 1, CostPerMTokens: 1.0, Healthy: true},
	}
	for _, perm := range permutations(latPool) {
		got, err := New(perm).Route("adv2-latinf", StrategyLatency)
		if err != nil {
			t.Fatalf("latency all-Inf must not error, got %v (perm %s)", err, ids(perm))
		}
		if got != perm[0].ID {
			t.Errorf("latency all-Inf must be first-seen-deterministic: perm %s chose %q, want %q",
				ids(perm), got, perm[0].ID)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. NON-DECISIVE-KEY POISON MUST NOT PERTURB THE DECISIVE ROUTE.
//
// Route by Latency while throughput & cost carry finite negative "glitch"
// values; the latency winner (lowest TTFT) and its determinism across all
// permutations must be unaffected. Symmetric probe for Throughput and Cost.
// This is broader than the audit test (which used only non-negative keys).
// ---------------------------------------------------------------------------

func TestAdv2_NonDecisiveKeyPoison_DoesNotPerturb(t *testing.T) {
	type tc struct {
		name string
		s    Strategy
		pool []Model
		want string // global argmin/argmax on the DECISIVE key only
	}
	cases := []tc{
		{
			name: "latency-decisive_negative-tput-cost",
			s:    StrategyLatency,
			pool: []Model{
				{ID: "lo", TTFTMillis: 2, ThroughputTokS: -100, CostPerMTokens: -5, Healthy: true},
				{ID: "mid", TTFTMillis: 5, ThroughputTokS: -50, CostPerMTokens: -1, Healthy: true},
				{ID: "hi", TTFTMillis: 9, ThroughputTokS: -10, CostPerMTokens: -0.5, Healthy: true},
			},
			want: "lo",
		},
		{
			name: "throughput-decisive_negative-ttft-cost",
			s:    StrategyThroughput,
			pool: []Model{
				{ID: "lo", TTFTMillis: -1, ThroughputTokS: 100, CostPerMTokens: -5, Healthy: true},
				{ID: "mid", TTFTMillis: -2, ThroughputTokS: 500, CostPerMTokens: -1, Healthy: true},
				{ID: "hi", TTFTMillis: -3, ThroughputTokS: 900, CostPerMTokens: -0.5, Healthy: true},
			},
			want: "hi",
		},
		{
			name: "cost-decisive_negative-ttft-tput",
			s:    StrategyCost,
			pool: []Model{
				{ID: "lo", TTFTMillis: -1, ThroughputTokS: -100, CostPerMTokens: 0.5, Healthy: true},
				{ID: "mid", TTFTMillis: -2, ThroughputTokS: -50, CostPerMTokens: 1.0, Healthy: true},
				{ID: "hi", TTFTMillis: -3, ThroughputTokS: -10, CostPerMTokens: 2.0, Healthy: true},
			},
			want: "lo",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Winner must be the decisive-key extremum AND identical across all
			// permutations (determinism is not perturbed by the poisoned keys).
			winners := map[string]int{}
			for _, perm := range permutations(c.pool) {
				got, err := New(perm).Route("adv2-perturb", c.s)
				if err != nil {
					t.Fatalf("[%s] unexpected error perm %s: %v", c.name, ids(perm), err)
				}
				winners[got]++
			}
			if len(winners) != 1 {
				t.Errorf("[%s] non-decisive poison perturbed determinism: %d distinct winners %v",
					c.name, len(winners), winners)
			}
			if winners[c.want] == 0 {
				t.Errorf("[%s] decisive-key extremum %q did not win; winners=%v", c.name, c.want, winners)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. TEE filter integrity: an unhealthy TEE model that is BOTH the cheapest and
//    TEE-capable must never be selected; the cheapest HEALTHY TEE model wins.
//    Existing tests cover "no eligible TEE" and a clean cheapest-TEE pick, but
//    not the adversarial overlap where the unhealthy model dominates the TEE
//    sub-pool on the decisive (cost) key. Independent oracle cross-check.
// ---------------------------------------------------------------------------

func TestAdv2_TEE_UnhealthyCheapestTEEExcluded(t *testing.T) {
	pool := []Model{
		// Cheapest AND TEE, but UNHEALTHY -> must be excluded before TEE filtering.
		{ID: "ghost", TTFTMillis: 1, ThroughputTokS: 5000, CostPerMTokens: 0.01, TEE: true, Healthy: false},
		// Healthy TEE models; cheapest healthy TEE is "cheaptee".
		{ID: "cheaptee", TTFTMillis: 8, ThroughputTokS: 400, CostPerMTokens: 0.4, TEE: true, Healthy: true},
		{ID: "deartee", TTFTMillis: 6, ThroughputTokS: 600, CostPerMTokens: 0.9, TEE: true, Healthy: true},
		// Healthy but non-TEE (must be filtered out by TEE strategy) and cheapest overall.
		{ID: "nontee", TTFTMillis: 3, ThroughputTokS: 800, CostPerMTokens: 0.1, TEE: false, Healthy: true},
	}

	want, ok := oracle(pool, StrategyTEE)
	if !ok {
		t.Fatal("oracle must find a TEE winner")
	}
	if want != "cheaptee" {
		t.Fatalf("oracle sanity: want cheaptee, got %q", want)
	}

	// Permutation-invariant + correct, asserted sink-side via the Decision log.
	for _, perm := range permutations(pool) {
		r := New(perm)
		got, err := r.Route("adv2-tee", StrategyTEE)
		if err != nil {
			t.Fatalf("perm %s: unexpected error %v", ids(perm), err)
		}
		if got == "ghost" {
			t.Errorf("perm %s: unhealthy cheapest TEE model 'ghost' was selected", ids(perm))
		}
		if got == "nontee" {
			t.Errorf("perm %s: non-TEE model 'nontee' selected under StrategyTEE", ids(perm))
		}
		if got != "cheaptee" {
			t.Errorf("perm %s: want 'cheaptee', got %q", ids(perm), got)
		}
		assertDecision(t, r, "adv2-tee", StrategyTEE, "cheaptee")
	}
}

// ---------------------------------------------------------------------------
// 5. TOTAL-ORDER stress for the decisive comparators: a randomized-but-fixed
//    set of DISTINCT finite keys (incl. negatives & large magnitudes) must
//    yield the same winner as a fully-sorted oracle, across every permutation.
//    This guards against any future comparator that is not a total order on the
//    finite reals (the prior fix made NaN/Inf safe; this pins finite totality).
// ---------------------------------------------------------------------------

func TestAdv2_FiniteTotalOrder_MatchesSortedOracle(t *testing.T) {
	// Distinct finite TTFT keys including negatives and a large magnitude.
	keys := []float64{-1e6, -3.5, -0.0001, 0.5, 2, 7, 42, 1e9}
	pool := make([]Model, len(keys))
	for i, k := range keys {
		pool[i] = Model{
			ID:             fmt.Sprintf("k%d", i),
			TTFTMillis:     k,
			ThroughputTokS: 100 + float64(i), // distinct
			CostPerMTokens: 1 + float64(i),   // distinct, positive
			TEE:            true,
			Healthy:        true,
		}
	}

	// Independent total-order oracle: lowest TTFT wins (Latency).
	sorted := make([]Model, len(pool))
	copy(sorted, pool)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TTFTMillis < sorted[j].TTFTMillis })
	wantLat := sorted[0].ID

	// Permutations of 8 elements = 40320; cap by sampling rotations + reversal to
	// keep runtime bounded while still exercising order sensitivity.
	variants := orderVariants(pool)
	for _, v := range variants {
		got, err := New(v).Route("adv2-total", StrategyLatency)
		if err != nil {
			t.Fatalf("variant %s: unexpected error %v", ids(v), err)
		}
		if got != wantLat {
			t.Errorf("variant %s: latency total-order winner want %q, got %q", ids(v), wantLat, got)
		}
	}
}

// orderVariants returns a bounded family of orderings (all rotations and the
// reversal of each) — enough to expose order-dependence without factorial cost.
func orderVariants(in []Model) [][]Model {
	n := len(in)
	var out [][]Model
	for shift := 0; shift < n; shift++ {
		rot := make([]Model, n)
		for i := 0; i < n; i++ {
			rot[i] = in[(i+shift)%n]
		}
		out = append(out, rot)
		rev := make([]Model, n)
		for i := 0; i < n; i++ {
			rev[i] = rot[n-1-i]
		}
		out = append(out, rev)
	}
	return out
}
