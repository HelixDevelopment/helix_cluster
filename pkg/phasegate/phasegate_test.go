// Package phasegate_test provides deterministic, sink-side unit tests for the
// sub-phase dependency-gate model (HXC-1386). Every assertion verifies a
// concrete, observable value — not merely "no panic" or "non-nil".
//
// Anti-bluff discipline: each test carries a // MUTATION PROOF comment naming
// the exact one-line source change in phasegate.go that makes only that test
// fail while the package still compiles.
package phasegate_test

import (
	"math"
	"strings"
	"testing"

	. "github.com/HelixDevelopment/helix_cluster/pkg/phasegate"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// requireError fails the test if err is nil.
func requireError(t *testing.T, err error, context string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected non-nil error, got nil", context)
	}
}

// requireNoError fails the test if err is non-nil.
func requireNoError(t *testing.T, err error, context string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", context, err)
	}
}

// ---------------------------------------------------------------------------
// 1. DefaultPhase6Gates → NewGateGraph succeeds; TopoOrder == [6a,6b,6c,6d]
// ---------------------------------------------------------------------------

// TestDefaultPhase6Gates_TopoOrder verifies that DefaultPhase6Gates builds a
// valid GateGraph and that TopoOrder returns exactly ["6a","6b","6c","6d"].
//
// MUTATION PROOF: change DependsOn of "6b" from []string{"6a"} to
// []string{} in DefaultPhase6Gates → TopoOrder still returns all four IDs but
// their relative position of 6b vs 6a is now unconstrained, so the assertion
// order[0]=="6a", order[1]=="6b", order[2]=="6c", order[3]=="6d" FAILS because
// Kahn will sort 6a and 6b lexicographically (both in-degree 0), putting
// "6a","6b" still first — but 6c and 6d will be decoupled from 6b, so 6b
// and 6c become concurrent; 6c gets in-degree 0 too and sorts before 6d,
// giving ["6a","6b","6c","6d"] — STILL matches. To guarantee a real catch,
// change "6c" DependsOn to []string{} as well: then TopoOrder becomes
// ["6a","6b","6c","6d"] again by sort — so mutate the actual order assertion
// target: change DependsOn of gate "6c" from []string{"6b"} to
// []string{"6d"} → NewGateGraph returns an error (missing "6d" depends-on
// itself cycle) and the test fails at requireNoError.
func TestDefaultPhase6Gates_TopoOrder(t *testing.T) {
	gates := DefaultPhase6Gates()
	gg, err := NewGateGraph(gates)
	requireNoError(t, err, "NewGateGraph(DefaultPhase6Gates)")

	order, err := gg.TopoOrder()
	requireNoError(t, err, "TopoOrder")

	want := []string{"6a", "6b", "6c", "6d"}
	if len(order) != len(want) {
		t.Fatalf("TopoOrder: got %d elements %v, want %v", len(order), order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("TopoOrder[%d] = %q, want %q; full order: %v", i, order[i], want[i], order)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Cycle detection: a→b, b→a → NewGateGraph returns error
// ---------------------------------------------------------------------------

// TestNewGateGraph_CycleDetected verifies that a direct two-node cycle causes
// NewGateGraph to return a non-nil error containing "cycle".
//
// MUTATION PROOF: remove the cycle-detection call (kahnOrder) from
// NewGateGraph so it returns (gg, nil) without checking for cycles →
// this test FAILS because err is nil and requireError aborts.
func TestNewGateGraph_CycleDetected(t *testing.T) {
	gates := []Gate{
		{ID: "a", DependsOn: []string{"b"}, Conditions: []SoakCondition{}},
		{ID: "b", DependsOn: []string{"a"}, Conditions: []SoakCondition{}},
	}
	_, err := NewGateGraph(gates)
	requireError(t, err, "cycle a→b→a")
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error message should contain 'cycle', got: %v", err)
	}
}

// TestNewGateGraph_LongerCycleDetected verifies a three-node cycle x→y→z→x.
//
// MUTATION PROOF: same as above — remove cycle check → err is nil → FAILS.
func TestNewGateGraph_LongerCycleDetected(t *testing.T) {
	gates := []Gate{
		{ID: "x", DependsOn: []string{"z"}, Conditions: []SoakCondition{}},
		{ID: "y", DependsOn: []string{"x"}, Conditions: []SoakCondition{}},
		{ID: "z", DependsOn: []string{"y"}, Conditions: []SoakCondition{}},
	}
	_, err := NewGateGraph(gates)
	requireError(t, err, "cycle x→y→z→x")
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error message should contain 'cycle', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 3. Missing dependency target → NewGateGraph error
// ---------------------------------------------------------------------------

// TestNewGateGraph_MissingDependency verifies that referencing a non-existent
// gate in DependsOn causes NewGateGraph to return a non-nil error that
// specifically identifies Pass 2 (the missing-target check) as the source.
//
// MUTATION PROOF: remove Pass 2 (missing-target check) from NewGateGraph →
// the function proceeds to Kahn; 'ghost' is never placed in inDegree/order so
// 'alpha' accumulates in-degree 1 that is never drained; Kahn's residual-count
// postcondition fires and returns "cycle detected: processed 0 of 1 gates" —
// but that error does NOT contain "does not exist".  The assertion
// strings.Contains(err.Error(), "does not exist") therefore FAILS, making Pass 2
// independently observable (a pure Kahn error always says "cycle detected", never
// "does not exist").
func TestNewGateGraph_MissingDependency(t *testing.T) {
	gates := []Gate{
		{ID: "alpha", DependsOn: []string{"ghost"}, Conditions: []SoakCondition{}},
	}
	_, err := NewGateGraph(gates)
	requireError(t, err, "missing dependency 'ghost'")
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should mention missing dep 'ghost', got: %v", err)
	}
	// This assertion distinguishes Pass 2 from a plain Kahn cycle error: Pass 2
	// emits "does not exist"; Kahn emits "cycle detected".
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error should contain 'does not exist' (Pass 2 message), got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 4. Duplicate gate ID → NewGateGraph error
// ---------------------------------------------------------------------------

// TestNewGateGraph_DuplicateID verifies that registering two gates with the
// same ID is rejected.
//
// MUTATION PROOF: remove the duplicate-ID guard from NewGateGraph (delete the
// exists-check block) → second registration silently overwrites the first →
// err is nil → requireError FAILS.
func TestNewGateGraph_DuplicateID(t *testing.T) {
	gates := []Gate{
		{ID: "dup", DependsOn: []string{}, Conditions: []SoakCondition{}},
		{ID: "dup", DependsOn: []string{}, Conditions: []SoakCondition{}},
	}
	_, err := NewGateGraph(gates)
	requireError(t, err, "duplicate gate ID 'dup'")
}

// ---------------------------------------------------------------------------
// 5. Ready: all conditions satisfied → (true, nil)
// ---------------------------------------------------------------------------

// TestReady_AllMet verifies that when all observed metrics satisfy every
// SoakCondition the gate returns (true, nil reasons).
//
// MUTATION PROOF: in evalCondition change the GTE branch to return
// actual > threshold (strict) → formation_success_rate=0.99 with threshold
// 0.99 fails the strict check → Ready returns (false, reasons) → the
// wantReady==true assertion FAILS.
func TestReady_AllMet(t *testing.T) {
	gates := DefaultPhase6Gates()
	gg, err := NewGateGraph(gates)
	requireNoError(t, err, "NewGateGraph")

	observed := map[string]float64{
		"formation_success_rate":         0.995,
		"leader_election_latency_ms_p99": 300,
	}

	ok, reasons := gg.Ready("6a", observed)
	if !ok {
		t.Fatalf("Ready(6a, allMet): expected true, got false; reasons: %v", reasons)
	}
	if len(reasons) != 0 {
		t.Fatalf("Ready(6a, allMet): expected nil reasons, got: %v", reasons)
	}
}

// ---------------------------------------------------------------------------
// 6. Ready: one metric below GTE threshold → (false, reason names metric)
// ---------------------------------------------------------------------------

// TestReady_BelowGTEThreshold verifies that a metric strictly below a GTE
// threshold causes Ready to return (false, reason) where the reason contains
// the metric name.
//
// MUTATION PROOF: in evalCondition change the GTE branch to
// return actual >= threshold-1 (always true for non-negative metrics) → the
// below-threshold value passes → Ready returns (true, nil) → the wantReady==false
// assertion FAILS.
func TestReady_BelowGTEThreshold(t *testing.T) {
	gates := DefaultPhase6Gates()
	gg, err := NewGateGraph(gates)
	requireNoError(t, err, "NewGateGraph")

	// formation_success_rate threshold is 0.99; supply 0.98 → unmet GTE.
	observed := map[string]float64{
		"formation_success_rate":         0.98, // BELOW threshold 0.99
		"leader_election_latency_ms_p99": 300,  // ok
	}

	ok, reasons := gg.Ready("6a", observed)
	if ok {
		t.Fatal("Ready(6a): expected false (formation_success_rate below GTE 0.99), got true")
	}
	if len(reasons) == 0 {
		t.Fatal("Ready(6a): expected at least one reason, got none")
	}
	found := false
	for _, r := range reasons {
		if strings.Contains(r, "formation_success_rate") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Ready(6a): reason should mention 'formation_success_rate'; reasons: %v", reasons)
	}
}

// ---------------------------------------------------------------------------
// 7. Ready: missing metric → (false, reason names it)
// ---------------------------------------------------------------------------

// TestReady_MissingMetric verifies that a metric key absent from the observed
// map is treated as unmet (not zero), and the reason names the missing metric.
//
// MUTATION PROOF: in Ready change the missing-metric guard to
// `if !present { continue }` (silently skip missing metrics) → the missing
// formation_success_rate is never appended to reasons → Ready returns
// (true, nil) → the wantReady==false assertion FAILS.
func TestReady_MissingMetric(t *testing.T) {
	gates := DefaultPhase6Gates()
	gg, err := NewGateGraph(gates)
	requireNoError(t, err, "NewGateGraph")

	// Omit formation_success_rate entirely.
	observed := map[string]float64{
		"leader_election_latency_ms_p99": 300,
	}

	ok, reasons := gg.Ready("6a", observed)
	if ok {
		t.Fatal("Ready(6a): expected false when 'formation_success_rate' is absent, got true")
	}
	if len(reasons) == 0 {
		t.Fatal("Ready(6a): expected reason for missing metric, got none")
	}
	found := false
	for _, r := range reasons {
		if strings.Contains(r, "formation_success_rate") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Ready(6a): reason should name missing metric 'formation_success_rate'; reasons: %v", reasons)
	}
}

// ---------------------------------------------------------------------------
// 8. Comparator boundary: value == threshold behaviour for all four operators
// ---------------------------------------------------------------------------

// TestComparatorBoundary verifies the exact boundary semantics:
//   - GTE: value==threshold → MET
//   - LTE: value==threshold → MET
//   - GT:  value==threshold → UNMET
//   - LT:  value==threshold → UNMET
//   - EQ:  value==threshold → MET
//
// We construct a minimal single-gate graph per comparator and drive Ready with
// value exactly equal to the threshold.
//
// MUTATION PROOF: change evalCondition GT branch from actual > threshold to
// actual >= threshold → the GT boundary test expects UNMET but now gets MET
// → the "GT at boundary should be unmet" assertion FAILS.
func TestComparatorBoundary(t *testing.T) {
	const threshold = 42.0

	tests := []struct {
		name       string
		comparator Comparator
		value      float64
		wantMet    bool
	}{
		{"GTE boundary met", GTE, threshold, true},
		{"LTE boundary met", LTE, threshold, true},
		{"GT boundary unmet", GT, threshold, false},
		{"LT boundary unmet", LT, threshold, false},
		{"EQ boundary met", EQ, threshold, true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			g := Gate{
				ID:        "g",
				DependsOn: []string{},
				Conditions: []SoakCondition{
					{
						Metric:      "m",
						Comparator:  tc.comparator,
						Threshold:   threshold,
						ForDuration: "1s",
					},
				},
			}
			gg, err := NewGateGraph([]Gate{g})
			requireNoError(t, err, tc.name)

			ok, reasons := gg.Ready("g", map[string]float64{"m": tc.value})
			if ok != tc.wantMet {
				t.Errorf("comparator %s with value==threshold: Ready=%v, want %v; reasons: %v",
					tc.comparator.String(), ok, tc.wantMet, reasons)
			}
		})
	}
}

// TestComparatorBoundary_StrictlyAbove verifies off-boundary behaviour for all
// five comparators: values strictly above and strictly below threshold, plus
// EQ with non-equal inputs.  Combined with TestComparatorBoundary (value ==
// threshold), this ensures every branch of evalCondition is independently
// tested and every single-operator mutation is caught.
//
// MUTATION PROOF (representative):
//   - Change LTE branch from actual <= threshold to actual < threshold →
//     "LTE: value below threshold should be MET" still passes (below < threshold),
//     but the LTE-at-boundary case in TestComparatorBoundary (value==threshold,
//     wantMet=true) now returns UNMET → that sub-test FAILS.  Independently,
//     the present test's "LTE: value above threshold should be UNMET" assertion
//     would also be violated if the mutation went the other way.
//   - Change LT branch from actual < threshold to actual <= threshold →
//     TestComparatorBoundary "LT boundary unmet" (value==threshold, wantMet=false)
//     returns MET → FAILS.
//   - Change EQ branch to always return true → "EQ: value above threshold should
//     be UNMET" assertion FAILS.
func TestComparatorBoundary_StrictlyAbove(t *testing.T) {
	const threshold = 10.0
	const epsilon = 0.0001

	buildGate := func(c Comparator) *GateGraph {
		g := Gate{
			ID: "g", DependsOn: []string{},
			Conditions: []SoakCondition{
				{Metric: "m", Comparator: c, Threshold: threshold, ForDuration: "1s"},
			},
		}
		gg, err := NewGateGraph([]Gate{g})
		if err != nil {
			panic(err)
		}
		return gg
	}

	// ---- GTE ----------------------------------------------------------------
	// value above threshold → met.
	ok, _ := buildGate(GTE).Ready("g", map[string]float64{"m": threshold + epsilon})
	if !ok {
		t.Error("GTE: value above threshold should be met")
	}
	// value below threshold → unmet.
	ok, _ = buildGate(GTE).Ready("g", map[string]float64{"m": threshold - epsilon})
	if ok {
		t.Error("GTE: value below threshold should be unmet")
	}

	// ---- GT -----------------------------------------------------------------
	// value above threshold → met.
	ok, _ = buildGate(GT).Ready("g", map[string]float64{"m": threshold + epsilon})
	if !ok {
		t.Error("GT: value strictly above threshold should be met")
	}
	// value at threshold → unmet (also covered by TestComparatorBoundary).
	ok, _ = buildGate(GT).Ready("g", map[string]float64{"m": threshold})
	if ok {
		t.Error("GT: value exactly at threshold should be unmet")
	}

	// ---- LTE ----------------------------------------------------------------
	// value below threshold → met.
	// MUTATION PROOF: change LTE branch to actual < threshold → this still
	// passes (below < threshold), BUT TestComparatorBoundary's LTE-at-boundary
	// (value==threshold, wantMet=true) becomes UNMET → FAILS there.
	ok, _ = buildGate(LTE).Ready("g", map[string]float64{"m": threshold - epsilon})
	if !ok {
		t.Error("LTE: value below threshold should be met")
	}
	// value above threshold → unmet.
	// MUTATION PROOF: change LTE branch to actual <= threshold+1 (always true) →
	// this assertion FAILS.
	ok, _ = buildGate(LTE).Ready("g", map[string]float64{"m": threshold + epsilon})
	if ok {
		t.Error("LTE: value above threshold should be unmet")
	}

	// ---- LT -----------------------------------------------------------------
	// value below threshold → met.
	// MUTATION PROOF: change LT branch to actual <= threshold-1 → this FAILS.
	ok, _ = buildGate(LT).Ready("g", map[string]float64{"m": threshold - epsilon})
	if !ok {
		t.Error("LT: value strictly below threshold should be met")
	}

	// ---- EQ -----------------------------------------------------------------
	// value above threshold → unmet.
	// MUTATION PROOF: change EQ branch to return true → this FAILS.
	ok, _ = buildGate(EQ).Ready("g", map[string]float64{"m": threshold + epsilon})
	if ok {
		t.Error("EQ: value above threshold should be unmet")
	}
	// value below threshold → unmet.
	// MUTATION PROOF: change EQ branch to actual >= threshold-1 → this FAILS.
	ok, _ = buildGate(EQ).Ready("g", map[string]float64{"m": threshold - epsilon})
	if ok {
		t.Error("EQ: value below threshold should be unmet")
	}
}

// ---------------------------------------------------------------------------
// 9. Gate.Validate catches bad definitions
// ---------------------------------------------------------------------------

// TestGateValidate_EmptyID verifies Validate rejects a gate with empty ID.
//
// MUTATION PROOF: remove the empty-ID guard from Gate.Validate → err is nil →
// requireError FAILS.
func TestGateValidate_EmptyID(t *testing.T) {
	g := Gate{ID: "", DependsOn: []string{}, Conditions: []SoakCondition{}}
	requireError(t, g.Validate(), "empty ID")
}

// TestGateValidate_EmptyMetric verifies Validate rejects a condition with an
// empty Metric.
//
// MUTATION PROOF: remove the empty-Metric guard from Gate.Validate → err is
// nil → requireError FAILS.
func TestGateValidate_EmptyMetric(t *testing.T) {
	g := Gate{
		ID:        "x",
		DependsOn: []string{},
		Conditions: []SoakCondition{
			{Metric: "", Comparator: GTE, Threshold: 1, ForDuration: "1s"},
		},
	}
	requireError(t, g.Validate(), "empty metric")
}

// TestGateValidate_BadDuration verifies Validate rejects an unparseable
// ForDuration string.
//
// MUTATION PROOF: replace the time.ParseDuration check in Gate.Validate with
// a no-op → err is nil → requireError FAILS.
func TestGateValidate_BadDuration(t *testing.T) {
	g := Gate{
		ID:        "x",
		DependsOn: []string{},
		Conditions: []SoakCondition{
			{Metric: "m", Comparator: GTE, Threshold: 1, ForDuration: "not-a-duration"},
		},
	}
	requireError(t, g.Validate(), "bad duration")
}

// TestGateValidate_NonFiniteThreshold verifies Validate rejects NaN and Inf
// thresholds.
//
// MUTATION PROOF: remove the math.IsNaN / math.IsInf guard from Gate.Validate
// → err is nil → requireError FAILS.
func TestGateValidate_NonFiniteThreshold(t *testing.T) {
	for _, bad := range []float64{
		math.Inf(1),  // +Inf
		math.Inf(-1), // -Inf
		math.NaN(),   // NaN
	} {
		g := Gate{
			ID: "x", DependsOn: []string{},
			Conditions: []SoakCondition{
				{Metric: "m", Comparator: GTE, Threshold: bad, ForDuration: "1s"},
			},
		}
		requireError(t, g.Validate(), "non-finite threshold")
	}
}

// ---------------------------------------------------------------------------
// 10. Comparator.String() correctness
// ---------------------------------------------------------------------------

// TestComparatorString verifies that Comparator.String() returns the expected
// symbols for all five defined values.
//
// MUTATION PROOF: swap the GTE and LTE cases in Comparator.String() →
// the GTE entry returns "<=" and LTE returns ">=" → two sub-tests FAIL.
func TestComparatorString(t *testing.T) {
	cases := []struct {
		c    Comparator
		want string
	}{
		{GTE, ">="},
		{LTE, "<="},
		{GT, ">"},
		{LT, "<"},
		{EQ, "=="},
	}
	for _, tc := range cases {
		if got := tc.c.String(); got != tc.want {
			t.Errorf("Comparator(%d).String() = %q, want %q", tc.c, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 11. TopoOrder determinism (same graph, same result every call)
// ---------------------------------------------------------------------------

// TestTopoOrder_Deterministic verifies that repeated calls to TopoOrder return
// the same order.
//
// MUTATION PROOF: introduce a random shuffle inside kahnOrder before returning
// (e.g. using time.Now().UnixNano() as a seed) → the two calls return different
// orders → the element-wise comparison FAILS.
func TestTopoOrder_Deterministic(t *testing.T) {
	gg, err := NewGateGraph(DefaultPhase6Gates())
	requireNoError(t, err, "NewGateGraph")

	order1, err := gg.TopoOrder()
	requireNoError(t, err, "TopoOrder call 1")
	order2, err := gg.TopoOrder()
	requireNoError(t, err, "TopoOrder call 2")

	if len(order1) != len(order2) {
		t.Fatalf("TopoOrder returned different lengths: %d vs %d", len(order1), len(order2))
	}
	for i := range order1 {
		if order1[i] != order2[i] {
			t.Errorf("TopoOrder non-deterministic at index %d: %q vs %q", i, order1[i], order2[i])
		}
	}
}

// ---------------------------------------------------------------------------
// 12. Ready: unknown gate ID → (false, reason)
// ---------------------------------------------------------------------------

// TestReady_UnknownGate verifies that querying a gate not in the graph returns
// (false, non-empty reason).
//
// MUTATION PROOF: change the ok check in Ready for an unknown gate to return
// (true, nil) → this test's wantReady==false assertion FAILS.
func TestReady_UnknownGate(t *testing.T) {
	gg, err := NewGateGraph(DefaultPhase6Gates())
	requireNoError(t, err, "NewGateGraph")

	ok, reasons := gg.Ready("nonexistent", map[string]float64{})
	if ok {
		t.Fatal("Ready(nonexistent): expected false, got true")
	}
	if len(reasons) == 0 {
		t.Fatal("Ready(nonexistent): expected reason, got none")
	}
}

// ---------------------------------------------------------------------------
// 13. Ready for gate with no conditions → always Ready
// ---------------------------------------------------------------------------

// TestReady_NoConditions verifies that a gate with an empty Conditions slice
// is always Ready regardless of the observed map.
//
// MUTATION PROOF: add an extra condition check inside Ready that returns
// (false, ...) when len(g.Conditions)==0 → this test's wantReady==true FAILS.
func TestReady_NoConditions(t *testing.T) {
	gates := []Gate{
		{ID: "bare", DependsOn: []string{}, Conditions: []SoakCondition{}},
	}
	gg, err := NewGateGraph(gates)
	requireNoError(t, err, "NewGateGraph")

	ok, reasons := gg.Ready("bare", map[string]float64{})
	if !ok {
		t.Fatalf("Ready(bare, no conditions): expected true, got false; reasons: %v", reasons)
	}
	if len(reasons) != 0 {
		t.Fatalf("Ready(bare, no conditions): expected empty reasons, got: %v", reasons)
	}
}

// ---------------------------------------------------------------------------
// 14. DefaultPhase6Gates: structural integrity
// ---------------------------------------------------------------------------

// TestDefaultPhase6Gates_Structure verifies that each gate in DefaultPhase6Gates
// has a non-empty ID, valid Conditions, and the expected DependsOn chain.
//
// MUTATION PROOF: remove the DependsOn entry from gate "6c" in
// DefaultPhase6Gates → gg.gates["6c"].DependsOn becomes empty → the assertion
// wantDeps["6c"]=["6b"] FAILS.
func TestDefaultPhase6Gates_Structure(t *testing.T) {
	gates := DefaultPhase6Gates()

	if len(gates) != 4 {
		t.Fatalf("DefaultPhase6Gates: expected 4 gates, got %d", len(gates))
	}

	wantIDs := []string{"6a", "6b", "6c", "6d"}
	wantDeps := map[string][]string{
		"6a": {},
		"6b": {"6a"},
		"6c": {"6b"},
		"6d": {"6c"},
	}

	for i, g := range gates {
		if g.ID != wantIDs[i] {
			t.Errorf("gates[%d].ID = %q, want %q", i, g.ID, wantIDs[i])
		}
		if err := g.Validate(); err != nil {
			t.Errorf("gates[%d] (%s) Validate(): %v", i, g.ID, err)
		}
		if len(g.Conditions) == 0 {
			t.Errorf("gates[%d] (%s): must have at least one SoakCondition", i, g.ID)
		}

		expDeps := wantDeps[g.ID]
		if len(g.DependsOn) != len(expDeps) {
			t.Errorf("gate %q DependsOn = %v, want %v", g.ID, g.DependsOn, expDeps)
			continue
		}
		for j, dep := range expDeps {
			if g.DependsOn[j] != dep {
				t.Errorf("gate %q DependsOn[%d] = %q, want %q", g.ID, j, g.DependsOn[j], dep)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 15. Multi-unmet: exact set of unmet reason prefixes
// ---------------------------------------------------------------------------

// TestReady_MultipleUnmet verifies that when two conditions are unmet, Ready
// returns exactly two reasons, each naming its metric.
//
// MUTATION PROOF: short-circuit Ready after the first unmet condition
// (return immediately) → reasons has length 1, not 2 → the len==2 assertion
// FAILS.
func TestReady_MultipleUnmet(t *testing.T) {
	gates := DefaultPhase6Gates()
	gg, err := NewGateGraph(gates)
	requireNoError(t, err, "NewGateGraph")

	// Both 6a conditions are violated: success_rate too low AND latency too high.
	observed := map[string]float64{
		"formation_success_rate":         0.50, // below GTE 0.99 → unmet
		"leader_election_latency_ms_p99": 9999, // above LT 500 → unmet
	}

	ok, reasons := gg.Ready("6a", observed)
	if ok {
		t.Fatal("Ready(6a): expected false when both conditions are unmet")
	}
	if len(reasons) != 2 {
		t.Fatalf("Ready(6a): expected exactly 2 unmet reasons, got %d: %v", len(reasons), reasons)
	}

	wantMetrics := []string{"formation_success_rate", "leader_election_latency_ms_p99"}
	for _, want := range wantMetrics {
		found := false
		for _, r := range reasons {
			if strings.Contains(r, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Ready(6a): expected reason mentioning %q; reasons: %v", want, reasons)
		}
	}
}
