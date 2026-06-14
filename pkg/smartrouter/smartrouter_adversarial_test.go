package smartrouter

import (
	"math"
	"testing"
)

// ===========================================================================
// ADVERSARIAL TESTS — NUMERIC POISON (NaN / ±Inf) in routing keys.
//
// Tonight's sibling-package bug class: a NaN/±Inf metric breaks a comparator's
// strict-weak-ordering so a POISON candidate seizes the top and traffic is
// mis-routed (sink-side wrong / nondeterministic).
//
// smartrouter selects via single-pass argmin/argmax loops seeded with
// best := pool[0]:
//
//	routeLatency:    if m.TTFTMillis    < best.TTFTMillis    { best = m }
//	routeCost:       if m.CostPerMTokens < best.CostPerMTokens { best = m }
//	routeThroughput: if m.ThroughputTokS > best.ThroughputTokS { best = m }
//
// IEEE-754 semantics: EVERY comparison against NaN is false. So if the
// argmin/argmax incumbent (pool[0], or whatever the loop has latched) holds a
// NaN key, no later strictly-better candidate can ever displace it — the NaN
// backend WINS. Because that depends on WHERE the NaN sits in the input slice,
// the chosen backend is also ORDER-DEPENDENT across permutations.
//
// The documented contract (smartrouter.go package doc) says Latency picks the
// "LOWEST TTFTMillis", Cost the "LOWEST CostPerMTokens", Throughput the
// "HIGHEST ThroughputTokS". A NaN is neither lowest nor highest, and the doc
// does NOT disclaim NaN/Inf. There is NO `< 0`-only guard that would even
// notice NaN. => REAL BUG vs the documented contract, asserted SINK-SIDE
// (the actual chosen model ID), not "no panic".
//
// NOTE TO COORDINATOR: TestAdv_NaNPoison_SinkWins and
// TestAdv_NaNPoison_OrderDependent FAIL on unfixed prod by design (they prove
// the bug). They are NOT gated behind any env var. The minimal fix is to make
// the comparators NaN-safe so a NaN-keyed model can never win a min/max it is
// not the true extremum of (e.g. treat NaN as worst). After the fix both go
// GREEN. The remaining tests are contract-pinning and pass either way.
// ===========================================================================

// nanPoisonPool builds a pool where every model is healthy and EXACTLY ONE
// model ("poison") carries a NaN in the decisive key for strategy s, placed at
// index `poisonIdx`. The other models have clean, distinct keys with a known
// true extremum ("good"). Returns the pool and the ID of the model that the
// documented contract REQUIRES to win (the clean extremum).
func nanPoisonPool(s Strategy, poisonIdx int) (pool []Model, wantWinner string) {
	// Three clean models with unambiguous extrema per strategy:
	//   good-lat : lowest TTFT (2)         <- Latency winner
	//   good-tput: highest throughput(900) <- Throughput winner
	//   good-cost: lowest cost (0.3)       <- Cost winner
	clean := []Model{
		{ID: "good-lat", TTFTMillis: 2, ThroughputTokS: 500, CostPerMTokens: 1.5, TEE: true, Healthy: true},
		{ID: "good-tput", TTFTMillis: 10, ThroughputTokS: 900, CostPerMTokens: 0.8, TEE: true, Healthy: true},
		{ID: "good-cost", TTFTMillis: 20, ThroughputTokS: 300, CostPerMTokens: 0.3, TEE: true, Healthy: true},
	}
	switch s {
	case StrategyLatency:
		wantWinner = "good-lat"
	case StrategyThroughput:
		wantWinner = "good-tput"
	case StrategyCost, StrategyTEE:
		wantWinner = "good-cost"
	default:
		wantWinner = "good-lat"
	}

	// Poison model: NaN in the decisive key, "attractive" but invalid elsewhere.
	poison := Model{ID: "POISON", TTFTMillis: 5, ThroughputTokS: 600, CostPerMTokens: 0.9, TEE: true, Healthy: true}
	nan := math.NaN()
	switch s {
	case StrategyLatency:
		poison.TTFTMillis = nan
	case StrategyThroughput:
		poison.ThroughputTokS = nan
	case StrategyCost, StrategyTEE:
		poison.CostPerMTokens = nan
	}

	// Insert poison at poisonIdx among the clean models.
	pool = make([]Model, 0, len(clean)+1)
	pool = append(pool, clean...)
	// place poison at the requested index (clamp)
	if poisonIdx < 0 {
		poisonIdx = 0
	}
	if poisonIdx > len(pool) {
		poisonIdx = len(pool)
	}
	pool = append(pool[:poisonIdx], append([]Model{poison}, pool[poisonIdx:]...)...)
	return pool, wantWinner
}

// TestAdv_NaNPoison_SinkWins proves SINK-SIDE that a NaN-keyed backend, when it
// occupies the argmin/argmax incumbent seat (index 0), is chosen even though a
// strictly-better clean backend exists. The known-good extremum is NOT chosen
// and the Decision log records the POISON model — concrete mis-routing.
//
// FAILS on unfixed prod (proves the bug). PASSES once comparators are NaN-safe.
func TestAdv_NaNPoison_SinkWins(t *testing.T) {
	for _, s := range []Strategy{StrategyLatency, StrategyThroughput, StrategyCost} {
		// Poison at index 0 -> it seats the loop incumbent; no `m < NaN`/`m > NaN`
		// can ever be true, so it is never displaced.
		pool, wantWinner := nanPoisonPool(s, 0)
		r := New(pool)
		got, err := r.Route("adv-nan-"+string(s), s)
		if err != nil {
			t.Fatalf("[%s] unexpected error: %v", s, err)
		}

		// SINK-SIDE assertion #1: the chosen model must be the clean extremum,
		// never the NaN poison.
		if got == "POISON" {
			t.Errorf("[%s] SINK MIS-ROUTED: NaN-poison backend %q was chosen; "+
				"contract requires clean extremum %q", s, got, wantWinner)
		}
		if got != wantWinner {
			t.Errorf("[%s] SINK WRONG: want clean extremum %q, got %q", s, wantWinner, got)
		}

		// SINK-SIDE assertion #2: the Decision log (the real downstream record
		// that callers act on) must NOT name the poison backend.
		for _, d := range r.Decisions() {
			if d.ModelID == "POISON" {
				t.Errorf("[%s] Decision log routed traffic to NaN-poison backend: %+v", s, d)
			}
		}
	}
}

// TestAdv_NaNPoison_OrderDependent proves NONDETERMINISM: the SAME fleet
// containing one NaN-poison model yields DIFFERENT winners depending only on
// the input ordering (where the NaN lands relative to the argmin/argmax loop).
// A correct router is permutation-invariant for a fixed fleet. This is the
// strict-weak-ordering violation made sink-visible.
//
// FAILS on unfixed prod (NaN at index 0 wins; NaN elsewhere does not -> >=2
// distinct winners). PASSES once comparators are NaN-safe (single winner).
func TestAdv_NaNPoison_OrderDependent(t *testing.T) {
	for _, s := range []Strategy{StrategyLatency, StrategyThroughput, StrategyCost} {
		// Build the base fleet (poison + 3 clean) once, then permute it fully.
		base, wantWinner := nanPoisonPool(s, 0)
		perms := permutations(base)

		winners := map[string]int{}
		for _, perm := range perms {
			got, err := New(perm).Route("adv-perm", s)
			if err != nil {
				t.Fatalf("[%s] unexpected error on perm %s: %v", s, ids(perm), err)
			}
			winners[got]++
		}

		// A deterministic, contract-honoring router selects ONE winner — the
		// clean extremum — across ALL permutations of a fixed fleet.
		if len(winners) != 1 {
			t.Errorf("[%s] ORDER-DEPENDENT (NaN strict-weak-ordering break): "+
				"a fixed fleet produced %d distinct winners across permutations: %v "+
				"(contract requires the single clean extremum %q)",
				s, len(winners), winners, wantWinner)
		}
		if c := winners["POISON"]; c > 0 {
			t.Errorf("[%s] NaN-poison backend won %d of %d permutations; it must never win",
				s, c, len(perms))
		}
		if _, ok := winners[wantWinner]; !ok || len(winners) != 1 {
			t.Errorf("[%s] clean extremum %q did not win all permutations; winners=%v",
				s, wantWinner, winners)
		}
	}
}

// TestAdv_PlusInfThroughput_SinkWins probes the Balanced strategy: a +Inf
// throughput produces a +Inf score that beats every finite score, so a poison
// backend with an absurd/sensor-glitched throughput seizes the route. The
// routeBalanced guard only skips TTFTMillis==0 / CostPerMTokens==0; it does NOT
// reject ±Inf. Documented contract: Balanced is a weighted score of REAL
// metrics; a +Inf metric is not a real measurement.
//
// This is reported as a LATENT RISK note rather than a hard prod-failing probe:
// see report. The assertion below is written to FAIL on unfixed prod so the
// coordinator can see the sink mis-route; it PASSES once Inf is sanitised.
func TestAdv_PlusInfThroughput_SinkWins(t *testing.T) {
	pool := []Model{
		// Clean, genuinely-best balanced model (high finite score).
		{ID: "good-bal", TTFTMillis: 2, ThroughputTokS: 1000, CostPerMTokens: 0.5, TEE: true, Healthy: true},
		// Poison: +Inf throughput -> +Inf score -> dominates argmax.
		{ID: "POISON-INF", TTFTMillis: 50, ThroughputTokS: math.Inf(1), CostPerMTokens: 5.0, TEE: true, Healthy: true},
	}
	r := New(pool)
	got, err := r.Route("adv-inf-bal", StrategyBalanced)
	if err != nil {
		t.Fatalf("Balanced unexpected error: %v", err)
	}
	if got == "POISON-INF" {
		t.Errorf("Balanced SINK MIS-ROUTED: +Inf-throughput poison %q chosen over finite-best good-bal", got)
	}
}

// ---------------------------------------------------------------------------
// CONTRACT-PINNING (pass regardless of fix) — these document & lock the
// behaviour the fix must PRESERVE, so a NaN-safety fix cannot regress normal
// routing or the documented first-seen tie-break.
// ---------------------------------------------------------------------------

// TestAdv_CleanPools_StillCorrect re-asserts, sink-side, that with NO poison
// the documented extrema are chosen. Pins that any NaN-safety fix must not
// disturb the happy path.
func TestAdv_CleanPools_StillCorrect(t *testing.T) {
	r := New(testPool())
	cases := []struct {
		s    Strategy
		want string
	}{
		{StrategyLatency, "alpha"},
		{StrategyThroughput, "beta"},
		{StrategyCost, "gamma"},
		{StrategyTEE, "gamma"},
	}
	for _, c := range cases {
		got, err := r.Route("adv-clean-"+string(c.s), c.s)
		if err != nil {
			t.Fatalf("[%s] error: %v", c.s, err)
		}
		if got != c.want {
			t.Errorf("[%s] want %q, got %q", c.s, c.want, got)
		}
	}
}

// TestAdv_AllNaNKey_DeterministicFallback pins that even when EVERY candidate's
// decisive key is NaN, the router must still pick deterministically (first-seen)
// rather than nondeterministically. A NaN-safe comparator that treats NaN as
// "never strictly better" yields the FIRST element across all permutations.
//
// This passes on unfixed prod too (all-NaN -> no displacement -> pool[0]), so
// it is a true contract pin, not a manufactured failure. It guards against a
// fix that accidentally makes the all-NaN case order-dependent.
func TestAdv_AllNaNKey_DeterministicFallback(t *testing.T) {
	nan := math.NaN()
	base := []Model{
		{ID: "n1", TTFTMillis: nan, ThroughputTokS: 100, CostPerMTokens: 1.0, TEE: true, Healthy: true},
		{ID: "n2", TTFTMillis: nan, ThroughputTokS: 200, CostPerMTokens: 1.0, TEE: true, Healthy: true},
		{ID: "n3", TTFTMillis: nan, ThroughputTokS: 300, CostPerMTokens: 1.0, TEE: true, Healthy: true},
	}
	// Latency is the decisive (all-NaN) strategy here.
	first := New(base).Route // bind
	got0, err := first("adv-allnan", StrategyLatency)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Whatever the deterministic choice is, it must be identical across every
	// permutation of the SAME all-NaN fleet.
	for _, perm := range permutations(base) {
		got, err := New(perm).Route("adv-allnan-perm", StrategyLatency)
		if err != nil {
			t.Fatalf("unexpected error on perm %s: %v", ids(perm), err)
		}
		// For each permutation, the deterministic winner must equal that
		// permutation's first element (first-seen tie-break under "NaN never
		// strictly better"). This is the determinism we require.
		if got != perm[0].ID {
			t.Errorf("all-NaN latency not first-seen-deterministic: perm %s chose %q, want %q",
				ids(perm), got, perm[0].ID)
		}
	}
	_ = got0
}
