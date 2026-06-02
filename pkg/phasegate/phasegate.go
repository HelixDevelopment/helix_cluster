// Package phasegate implements the Helix Cluster OS sub-phase dependency-gate
// model and soak-condition validator (HXC-1386).
//
// A Gate is a named checkpoint that may depend on other gates (forming a DAG)
// and carries a set of SoakConditions — named metric thresholds that must hold
// before the gate is considered Ready. This package provides:
//
//   - Structural validation of individual Gate definitions.
//   - Construction of a GateGraph with full acyclicity checking (Kahn's
//     algorithm) and dependency-target existence checking.
//   - Deterministic topological ordering of gates (Kahn, ties broken by ID).
//   - A pure-function Ready evaluator that accepts an observed metric map and
//     returns whether every SoakCondition is satisfied.
//   - DefaultPhase6Gates, the canonical 6a→6b→6c→6d gate sequence.
//
// This is a pure in-memory model: Ready is a pure function over supplied
// metrics; no real soaking or runtime observation is performed here.
package phasegate

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// ---------------------------------------------------------------------------
// Comparator
// ---------------------------------------------------------------------------

// Comparator enumerates the comparison operator applied to a SoakCondition.
type Comparator int

const (
	// GTE requires observed >= threshold.
	GTE Comparator = iota
	// LTE requires observed <= threshold.
	LTE
	// GT requires observed > threshold.
	GT
	// LT requires observed < threshold.
	LT
	// EQ requires observed == threshold (exact float64 equality).
	EQ
)

// String returns the human-readable comparator symbol.
func (c Comparator) String() string {
	switch c {
	case GTE:
		return ">="
	case LTE:
		return "<="
	case GT:
		return ">"
	case LT:
		return "<"
	case EQ:
		return "=="
	default:
		return "?"
	}
}

// ---------------------------------------------------------------------------
// SoakCondition
// ---------------------------------------------------------------------------

// SoakCondition is a named metric threshold that must hold for a Gate to be
// considered Ready.  ForDuration is an informational annotation expressing how
// long the condition must hold in real operation; it is validated by
// time.ParseDuration but is not used in the pure Ready evaluation.
type SoakCondition struct {
	// Metric is the key that must appear in the observed metric map.
	Metric string
	// Comparator is the operator applied between the observed value and Threshold.
	Comparator Comparator
	// Threshold is the required value.
	Threshold float64
	// ForDuration is a time.Duration string (e.g. "5m", "30s") expressing how
	// long the condition must hold in real operation. Must be parseable by
	// time.ParseDuration.
	ForDuration string
}

// ---------------------------------------------------------------------------
// Gate
// ---------------------------------------------------------------------------

// Gate is a named checkpoint in the sub-phase dependency graph.  It may depend
// on zero or more other gates (by ID) and carries zero or more SoakConditions.
type Gate struct {
	// ID uniquely identifies this gate within a GateGraph.
	ID string
	// DependsOn lists the IDs of gates that must be Ready before this gate may
	// be promoted.
	DependsOn []string
	// Conditions are the soak conditions evaluated by Ready.
	Conditions []SoakCondition
}

// Validate returns an error if the Gate definition is structurally invalid:
//   - ID must be non-empty.
//   - Each SoakCondition must have a non-empty Metric, a ForDuration parseable
//     by time.ParseDuration, and a finite (non-NaN, non-Inf) Threshold.
func (g Gate) Validate() error {
	if g.ID == "" {
		return fmt.Errorf("gate ID must not be empty")
	}
	for i, sc := range g.Conditions {
		if sc.Metric == "" {
			return fmt.Errorf("gate %q condition[%d]: Metric must not be empty", g.ID, i)
		}
		if _, err := time.ParseDuration(sc.ForDuration); err != nil {
			return fmt.Errorf("gate %q condition[%d] metric %q: ForDuration %q: %w",
				g.ID, i, sc.Metric, sc.ForDuration, err)
		}
		if math.IsNaN(sc.Threshold) || math.IsInf(sc.Threshold, 0) {
			return fmt.Errorf("gate %q condition[%d] metric %q: Threshold must be finite, got %v",
				g.ID, i, sc.Metric, sc.Threshold)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// GateGraph
// ---------------------------------------------------------------------------

// GateGraph holds a validated, acyclic set of Gates.
type GateGraph struct {
	// gates maps gate ID to Gate, in original insertion order.
	gates map[string]Gate
	// order preserves the original slice order for Kahn initialization.
	order []string
}

// NewGateGraph constructs a GateGraph from a slice of Gate definitions and
// returns an error for any of the following:
//   - Duplicate gate ID.
//   - A DependsOn entry that references a non-existent gate ID.
//   - A cycle in the dependency graph (detected via Kahn's algorithm).
//
// Each Gate is also structurally validated via Gate.Validate().
func NewGateGraph(gates []Gate) (*GateGraph, error) {
	gg := &GateGraph{
		gates: make(map[string]Gate, len(gates)),
		order: make([]string, 0, len(gates)),
	}

	// Pass 1: validate and register all gates; detect duplicates.
	for _, g := range gates {
		if err := g.Validate(); err != nil {
			return nil, fmt.Errorf("NewGateGraph: invalid gate: %w", err)
		}
		if _, exists := gg.gates[g.ID]; exists {
			return nil, fmt.Errorf("NewGateGraph: duplicate gate ID %q", g.ID)
		}
		gg.gates[g.ID] = g
		gg.order = append(gg.order, g.ID)
	}

	// Pass 2: verify all DependsOn targets exist.
	for _, g := range gates {
		for _, dep := range g.DependsOn {
			if _, exists := gg.gates[dep]; !exists {
				return nil, fmt.Errorf("NewGateGraph: gate %q depends on %q which does not exist",
					g.ID, dep)
			}
		}
	}

	// Pass 3: cycle detection via Kahn's algorithm.
	if _, err := kahnOrder(gg.gates, gg.order); err != nil {
		return nil, fmt.Errorf("NewGateGraph: %w", err)
	}

	return gg, nil
}

// TopoOrder returns a deterministic topological ordering of all gate IDs.
// Ties (multiple candidates with in-degree zero) are broken lexicographically
// by gate ID, yielding a stable order for the same graph on every run.
//
// An error is returned only if an unexpected cycle is detected (which cannot
// happen if NewGateGraph succeeded, but is guarded for safety).
func (gg *GateGraph) TopoOrder() ([]string, error) {
	return kahnOrder(gg.gates, gg.order)
}

// kahnOrder performs Kahn's topological sort on the gate set.
// The iteration order is seeded by the original registration order (for
// in-degree initialization), and ties in the ready queue are broken by
// lexicographic gate ID to guarantee determinism.
func kahnOrder(gates map[string]Gate, order []string) ([]string, error) {
	// Build in-degree map and adjacency list.
	inDegree := make(map[string]int, len(gates))
	// adj maps each gate to the gates that depend on it (reverse edges).
	adj := make(map[string][]string, len(gates))

	for _, id := range order {
		if _, seen := inDegree[id]; !seen {
			inDegree[id] = 0
		}
		g := gates[id]
		for _, dep := range g.DependsOn {
			inDegree[id]++
			adj[dep] = append(adj[dep], id)
		}
	}

	// Seed the ready queue with all zero in-degree nodes.
	var ready []string
	for _, id := range order {
		if inDegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)

	result := make([]string, 0, len(gates))
	for len(ready) > 0 {
		// Pop the lexicographically smallest ready node.
		cur := ready[0]
		ready = ready[1:]
		result = append(result, cur)

		// Reduce in-degree for all dependents of cur.
		newlyReady := make([]string, 0)
		for _, dependent := range adj[cur] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				newlyReady = append(newlyReady, dependent)
			}
		}
		// Sort newly ready nodes before merging into the queue to preserve
		// determinism when multiple nodes become available simultaneously.
		sort.Strings(newlyReady)
		ready = mergeSorted(ready, newlyReady)
	}

	if len(result) != len(gates) {
		return nil, fmt.Errorf("cycle detected: processed %d of %d gates", len(result), len(gates))
	}
	return result, nil
}

// mergeSorted merges two sorted slices into one sorted slice.
func mergeSorted(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] <= b[j] {
			out = append(out, a[i])
			i++
		} else {
			out = append(out, b[j])
			j++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}

// ---------------------------------------------------------------------------
// Ready
// ---------------------------------------------------------------------------

// Ready evaluates the SoakConditions of the gate identified by gateID against
// the supplied observed metric map.  It returns:
//
//   - (true, nil)              — all conditions are satisfied.
//   - (false, reasons)         — one or more conditions are unmet; reasons
//     contains one string per unmet condition of the form
//     "metric <op> threshold (observed <actual>)" or
//     "metric: metric not present in observed map".
//
// A metric key absent from observed is treated as unmet (not as zero).
// DependsOn is NOT evaluated here; the caller is responsible for ensuring
// dependencies are Ready before promoting this gate.
func (gg *GateGraph) Ready(gateID string, observed map[string]float64) (bool, []string) {
	g, ok := gg.gates[gateID]
	if !ok {
		return false, []string{fmt.Sprintf("gate %q not found in graph", gateID)}
	}

	var reasons []string
	for _, sc := range g.Conditions {
		actual, present := observed[sc.Metric]
		if !present {
			reasons = append(reasons, fmt.Sprintf("%s: metric not present in observed map", sc.Metric))
			continue
		}

		met := evalCondition(sc.Comparator, actual, sc.Threshold)
		if !met {
			reasons = append(reasons, fmt.Sprintf("%s %s %v (observed %v)",
				sc.Metric, sc.Comparator.String(), sc.Threshold, actual))
		}
	}

	if len(reasons) > 0 {
		return false, reasons
	}
	return true, nil
}

// evalCondition returns true when actual satisfies (comparator threshold).
func evalCondition(c Comparator, actual, threshold float64) bool {
	switch c {
	case GTE:
		return actual >= threshold
	case LTE:
		return actual <= threshold
	case GT:
		return actual > threshold
	case LT:
		return actual < threshold
	case EQ:
		return actual == threshold
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// DefaultPhase6Gates
// ---------------------------------------------------------------------------

// DefaultPhase6Gates returns the canonical four-gate phase-6 progression:
//
//	6a (bootstrap quorum) → 6b (gossip convergence) → 6c (cross-cell routing)
//	→ 6d (formation stable)
//
// Each gate carries real, operationally meaningful SoakConditions expressed
// over metrics that Helix Cluster OS instruments during formation:
//
//   - 6a: formation success rate and leader election latency must be healthy
//     before gossip convergence work begins.
//   - 6b: suspicion convergence ratio and gossip round-trip latency must be
//     tight before cross-cell routing is tested.
//   - 6c: cross-cell p99 latency must be low and packet-loss near-zero before
//     declaring the formation stable.
//   - 6d: cluster-wide health score must be high and unscheduled tasks must be
//     zero, capping the entire sub-phase sequence.
func DefaultPhase6Gates() []Gate {
	return []Gate{
		{
			ID:        "6a",
			DependsOn: []string{},
			Conditions: []SoakCondition{
				{
					Metric:      "formation_success_rate",
					Comparator:  GTE,
					Threshold:   0.99,
					ForDuration: "2m",
				},
				{
					Metric:      "leader_election_latency_ms_p99",
					Comparator:  LT,
					Threshold:   500,
					ForDuration: "2m",
				},
			},
		},
		{
			ID:        "6b",
			DependsOn: []string{"6a"},
			Conditions: []SoakCondition{
				{
					Metric:      "suspicion_convergence_ratio",
					Comparator:  GTE,
					Threshold:   0.95,
					ForDuration: "3m",
				},
				{
					Metric:      "gossip_roundtrip_latency_ms_p99",
					Comparator:  LT,
					Threshold:   100,
					ForDuration: "3m",
				},
			},
		},
		{
			ID:        "6c",
			DependsOn: []string{"6b"},
			Conditions: []SoakCondition{
				{
					Metric:      "cross_cell_latency_ms_p99",
					Comparator:  LT,
					Threshold:   50,
					ForDuration: "5m",
				},
				{
					Metric:      "cross_cell_packet_loss_ratio",
					Comparator:  LTE,
					Threshold:   0.001,
					ForDuration: "5m",
				},
			},
		},
		{
			ID:        "6d",
			DependsOn: []string{"6c"},
			Conditions: []SoakCondition{
				{
					Metric:      "cluster_health_score",
					Comparator:  GTE,
					Threshold:   0.999,
					ForDuration: "10m",
				},
				{
					Metric:      "unscheduled_tasks_total",
					Comparator:  EQ,
					Threshold:   0,
					ForDuration: "10m",
				},
			},
		},
	}
}

