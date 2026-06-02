package smartrouter

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Shared fixtures
// ---------------------------------------------------------------------------

// testPool returns a fixed pool of models used by most tests.
// Layout:
//
//	alpha  — healthy, lowest TTFT  (2 ms),  medium throughput, medium cost, no TEE
//	beta   — healthy, medium TTFT  (10 ms), highest throughput, low cost,   TEE
//	gamma  — healthy, highest TTFT (50 ms), lowest throughput,  lowest cost, TEE
//	delta  — UNHEALTHY, lowest TTFT (1 ms), highest throughput, cheapest
//
// delta is the "otherwise-best" model for Latency/Throughput/Cost but is
// unhealthy, so it MUST never appear in any routing result.
func testPool() []Model {
	return []Model{
		{ID: "alpha", TTFTMillis: 2, ThroughputTokS: 500, CostPerMTokens: 1.5, TEE: false, Healthy: true},
		{ID: "beta", TTFTMillis: 10, ThroughputTokS: 900, CostPerMTokens: 0.8, TEE: true, Healthy: true},
		{ID: "gamma", TTFTMillis: 50, ThroughputTokS: 200, CostPerMTokens: 0.5, TEE: true, Healthy: true},
		{ID: "delta", TTFTMillis: 1, ThroughputTokS: 2000, CostPerMTokens: 0.1, TEE: true, Healthy: false},
	}
}

// ---------------------------------------------------------------------------
// Required named tests
// ---------------------------------------------------------------------------

// TestLatencyPicksLowestTTFT pins guard B (the < comparator in routeLatency).
// Inverting that comparator to > would return "gamma" (TTFT=50) instead of
// "alpha" (TTFT=2), which makes this test fail.
func TestLatencyPicksLowestTTFT(t *testing.T) {
	r := New(testPool())
	runID := "run-latency-001"

	got, err := r.Route(runID, StrategyLatency)
	if err != nil {
		t.Fatalf("Route(Latency) unexpected error: %v", err)
	}
	// guard B: alpha has TTFTMillis=2 which is the lowest among healthy models.
	// Inverting < to > in routeLatency would yield "gamma" (TTFT=50) and break this assertion.
	if got != "alpha" {
		t.Errorf("Latency: want model %q, got %q", "alpha", got)
	}

	// Assert sink-side: the Decision log must contain this runID.
	assertDecision(t, r, runID, StrategyLatency, "alpha")
}

// TestCostPicksCheapest pins guard C (the < comparator in routeCost).
// Inverting that comparator to > would return "alpha" (cost=1.5) instead of
// "gamma" (cost=0.5), which makes this test fail.
func TestCostPicksCheapest(t *testing.T) {
	r := New(testPool())
	runID := "run-cost-001"

	got, err := r.Route(runID, StrategyCost)
	if err != nil {
		t.Fatalf("Route(Cost) unexpected error: %v", err)
	}
	// guard C: gamma has CostPerMTokens=0.5 which is the lowest among healthy models.
	if got != "gamma" {
		t.Errorf("Cost: want model %q, got %q", "gamma", got)
	}

	assertDecision(t, r, runID, StrategyCost, "gamma")
}

// TestUnhealthyModelExcluded pins guard A (the Healthy filter in Route).
// Removing the Healthy guard would allow "delta" (TTFTMillis=1, cheapest,
// highest throughput) to be selected — breaking every strategy assertion.
// This specific test uses StrategyLatency: delta (TTFT=1) would win without
// guard A, but alpha (TTFT=2) must win because delta is unhealthy.
func TestUnhealthyModelExcluded(t *testing.T) {
	r := New(testPool())
	runID := "run-unhealthy-001"

	got, err := r.Route(runID, StrategyLatency)
	if err != nil {
		t.Fatalf("Route(Latency) unexpected error: %v", err)
	}
	// guard A: delta has TTFTMillis=1 but Healthy=false so it MUST NOT be chosen.
	// Removing the Healthy filter in Route would yield "delta" and break this assertion.
	if got == "delta" {
		t.Errorf("unhealthy model \"delta\" must never be selected, but it was")
	}
	if got != "alpha" {
		t.Errorf("expected healthy fastest model %q, got %q", "alpha", got)
	}

	assertDecision(t, r, runID, StrategyLatency, "alpha")
}

// ---------------------------------------------------------------------------
// Additional coverage tests
// ---------------------------------------------------------------------------

// TestThroughputPicksHighest confirms the highest ThroughputTokS healthy model wins.
func TestThroughputPicksHighest(t *testing.T) {
	r := New(testPool())
	runID := "run-tput-001"

	got, err := r.Route(runID, StrategyThroughput)
	if err != nil {
		t.Fatalf("Route(Throughput) unexpected error: %v", err)
	}
	// beta ThroughputTokS=900 is highest among healthy models (delta=2000 is unhealthy).
	if got != "beta" {
		t.Errorf("Throughput: want %q, got %q", "beta", got)
	}
	assertDecision(t, r, runID, StrategyThroughput, "beta")
}

// TestTEEPicksCheapestTEEModel confirms TEE strategy restricts to TEE models then picks cheapest.
func TestTEEPicksCheapestTEEModel(t *testing.T) {
	r := New(testPool())
	runID := "run-tee-001"

	got, err := r.Route(runID, StrategyTEE)
	if err != nil {
		t.Fatalf("Route(TEE) unexpected error: %v", err)
	}
	// Healthy TEE models: beta (cost=0.8), gamma (cost=0.5). Cheapest is gamma.
	if got != "gamma" {
		t.Errorf("TEE: want %q, got %q", "gamma", got)
	}
	assertDecision(t, r, runID, StrategyTEE, "gamma")
}

// TestTEENoEligibleModel confirms ErrNoEligibleModel when no healthy TEE model exists.
func TestTEENoEligibleModel(t *testing.T) {
	pool := []Model{
		{ID: "x", TTFTMillis: 5, ThroughputTokS: 300, CostPerMTokens: 1.0, TEE: false, Healthy: true},
		{ID: "y", TTFTMillis: 3, ThroughputTokS: 100, CostPerMTokens: 2.0, TEE: true, Healthy: false},
	}
	r := New(pool)
	_, err := r.Route("run-tee-noeligible", StrategyTEE)
	if !errors.Is(err, ErrNoEligibleModel) {
		t.Errorf("want ErrNoEligibleModel, got %v", err)
	}
}

// TestAllUnhealthyReturnsError confirms Route returns ErrNoEligibleModel when
// every model in the pool is unhealthy regardless of strategy.
func TestAllUnhealthyReturnsError(t *testing.T) {
	pool := []Model{
		{ID: "a", TTFTMillis: 5, ThroughputTokS: 300, CostPerMTokens: 1.0, Healthy: false},
		{ID: "b", TTFTMillis: 3, ThroughputTokS: 100, CostPerMTokens: 2.0, Healthy: false},
	}
	r := New(pool)
	for _, s := range []Strategy{StrategyLatency, StrategyThroughput, StrategyCost, StrategyTEE, StrategyBalanced} {
		_, err := r.Route("run-allunhealthy", s)
		if !errors.Is(err, ErrNoEligibleModel) {
			t.Errorf("strategy %s: want ErrNoEligibleModel, got %v", s, err)
		}
	}
}

// TestBalancedScoring verifies that the Balanced strategy selects the model
// whose weighted score (0.4/TTFT + 0.3*tput + 0.3/cost) is highest.
func TestBalancedScoring(t *testing.T) {
	// Construct two models where the manual calculation clearly favours "fast".
	// fast: TTFT=2  → 0.4*(1/2)=0.200, tput=100 → 0.3*100=30, cost=1 → 0.3*(1/1)=0.300 ⇒ 30.500
	// slow: TTFT=20 → 0.4*(1/20)=0.020, tput=50  → 0.3*50=15,  cost=2 → 0.3*(1/2)=0.150 ⇒ 15.170
	pool := []Model{
		{ID: "fast", TTFTMillis: 2, ThroughputTokS: 100, CostPerMTokens: 1.0, Healthy: true},
		{ID: "slow", TTFTMillis: 20, ThroughputTokS: 50, CostPerMTokens: 2.0, Healthy: true},
	}
	r := New(pool)
	runID := "run-balanced-001"

	got, err := r.Route(runID, StrategyBalanced)
	if err != nil {
		t.Fatalf("Route(Balanced) unexpected error: %v", err)
	}
	if got != "fast" {
		t.Errorf("Balanced: want %q, got %q", "fast", got)
	}
	assertDecision(t, r, runID, StrategyBalanced, "fast")
}

// TestUnknownStrategyReturnsError confirms an unknown strategy string is rejected.
func TestUnknownStrategyReturnsError(t *testing.T) {
	r := New(testPool())
	_, err := r.Route("run-unknown", Strategy("bogus"))
	if err == nil {
		t.Fatal("expected error for unknown strategy, got nil")
	}
}

// TestDecisionsAreAppended confirms multiple calls accumulate in order.
func TestDecisionsAreAppended(t *testing.T) {
	r := New(testPool())

	runs := []struct {
		id       string
		strategy Strategy
	}{
		{"r1", StrategyLatency},
		{"r2", StrategyCost},
		{"r3", StrategyThroughput},
	}
	for _, run := range runs {
		if _, err := r.Route(run.id, run.strategy); err != nil {
			t.Fatalf("Route(%s, %s) error: %v", run.id, run.strategy, err)
		}
	}

	decisions := r.Decisions()
	if len(decisions) != len(runs) {
		t.Fatalf("want %d decisions, got %d", len(runs), len(decisions))
	}
	for i, run := range runs {
		if decisions[i].RunID != run.id {
			t.Errorf("decision[%d] RunID: want %q, got %q", i, run.id, decisions[i].RunID)
		}
		if decisions[i].Strategy != run.strategy {
			t.Errorf("decision[%d] Strategy: want %q, got %q", i, run.strategy, decisions[i].Strategy)
		}
	}
}

// TestPoolIsolation confirms that mutating the slice passed to New does not
// affect the Router's internal copy.
func TestPoolIsolation(t *testing.T) {
	pool := testPool()
	r := New(pool)

	// Corrupt the caller's slice.
	for i := range pool {
		pool[i].Healthy = false
	}

	got, err := r.Route("run-isolation", StrategyLatency)
	if err != nil {
		t.Fatalf("Route should still find healthy models after external slice mutation: %v", err)
	}
	if got == "" {
		t.Error("expected a non-empty model ID")
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// assertDecision is a sink-side assertion: it checks that a Decision with the
// given runID, strategy, and modelID was appended to the router's log.
func assertDecision(t *testing.T, r *Router, runID string, strategy Strategy, modelID string) {
	t.Helper()
	for _, d := range r.Decisions() {
		if d.RunID == runID && d.Strategy == strategy && d.ModelID == modelID {
			return
		}
	}
	t.Errorf("no decision found with RunID=%q Strategy=%q ModelID=%q; decisions: %+v",
		runID, strategy, modelID, r.Decisions())
}
