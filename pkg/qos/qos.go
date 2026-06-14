// Package qos implements QoS-tier classification and routing for HelixCluster.
//
// A workload declares (or is classified into) one of four QoS classes. The
// router maps each class to a distinct placement objective over a fleet of
// candidate placements:
//
//   - RealTime    -> the LOWEST-LATENCY candidate (latency is paramount).
//   - Interactive -> low-latency but cost-aware (latency-dominant): among the
//     candidates whose latency is within the near-tie band of the fleet's BEST
//     (minimum) latency, the cheapest wins, so an absurdly expensive fastest
//     node loses to a comparably-fast cheaper one.
//   - Batch       -> throughput/cost balanced (cost-dominant): among the
//     candidates whose cost is within the near-tie band of the fleet's BEST
//     (minimum) cost, the fastest wins.
//   - BestEffort  -> the CHEAPEST candidate, preferring spot capacity.
//
// The near-tie band is anchored to the fleet-global best of the primary metric
// (not evaluated pairwise), which makes the "near-best" set a fixed set and the
// resulting class comparator a total order; consequently the chosen target is
// independent of the input ordering. Anchoring is required for correctness: a
// pairwise band is intransitive and would make the sort's winner depend on the
// candidate order.
//
// The package is pure Go and fully deterministic: Route is a total function of
// (class, candidates) with a stable tie-break, no clocks, randomness, network,
// or host access. It is self-contained and defines its own minimal types.
package qos

import (
	"errors"
	"sort"
)

// ErrNoCandidates is returned by Route when the candidate fleet is empty.
var ErrNoCandidates = errors.New("qos: no candidates")

// QoSClass enumerates the four supported quality-of-service tiers.
type QoSClass int

const (
	// RealTime workloads require the lowest achievable latency.
	RealTime QoSClass = iota
	// Interactive workloads want low latency but are cost-aware.
	Interactive
	// Batch workloads balance throughput against cost.
	Batch
	// BestEffort workloads take the cheapest (typically spot) capacity.
	BestEffort
)

// String returns the canonical label of the class.
func (c QoSClass) String() string {
	switch c {
	case RealTime:
		return "RealTime"
	case Interactive:
		return "Interactive"
	case Batch:
		return "Batch"
	case BestEffort:
		return "BestEffort"
	default:
		return "Unknown"
	}
}

// Valid reports whether c is one of the four defined classes.
func (c QoSClass) Valid() bool {
	return c >= RealTime && c <= BestEffort
}

// Candidate is a single placement option in the fleet.
type Candidate struct {
	// ID uniquely identifies the placement; used as the deterministic
	// final tie-break.
	ID string
	// LatencyMs is the expected request latency in milliseconds (lower is
	// better).
	LatencyMs float64
	// CostPerHour is the price of the placement per hour (lower is better).
	CostPerHour float64
	// Spot reports whether the placement is reclaimable spot capacity.
	Spot bool
}

// Decision records the outcome of routing one workload: the class that was
// applied (the label) and the chosen candidate.
type Decision struct {
	// Class is the QoS class label that was applied.
	Class QoSClass
	// Target is the selected candidate for the class objective.
	Target Candidate
}

// nearTie is the relative threshold within which two values are treated as a
// near-tie, allowing the secondary objective to decide. Expressed as a
// fraction of the better (smaller) value plus a small absolute floor so that
// zero-valued metrics still compare sanely.
const (
	nearTieFrac  = 0.10 // 10% band
	nearTieFloor = 1e-9
)

// minBy returns the minimum of key(c) over a non-empty candidate slice. It is
// used to anchor the near-tie band to the fleet's best value of the primary
// metric, which makes the "near-best" set fixed (independent of comparison
// order) and the resulting comparator a total order.
func minBy(cs []Candidate, key func(Candidate) float64) float64 {
	m := key(cs[0])
	for _, c := range cs[1:] {
		if v := key(c); v < m {
			m = v
		}
	}
	return m
}

// Route selects the candidate that best satisfies the objective of class over
// candidates and returns a Decision recording both the applied class label and
// the chosen target. It returns ErrNoCandidates if candidates is empty and an
// error if class is not a defined QoSClass.
//
// Selection is deterministic: ties under the primary (and, where applicable,
// secondary) objective are broken by ascending Candidate.ID. The input slice
// is never mutated.
func Route(class QoSClass, candidates []Candidate) (Decision, error) {
	if len(candidates) == 0 {
		return Decision{}, ErrNoCandidates
	}
	if !class.Valid() {
		return Decision{}, errors.New("qos: invalid QoSClass")
	}

	// Work on a copy so callers' slices are never reordered.
	cs := make([]Candidate, len(candidates))
	copy(cs, candidates)

	var less func(a, b Candidate) bool
	switch class {
	case RealTime:
		// Pure latency. Cost and spot are irrelevant; ID breaks exact ties.
		less = func(a, b Candidate) bool {
			if a.LatencyMs != b.LatencyMs {
				return a.LatencyMs < b.LatencyMs
			}
			return a.ID < b.ID
		}
	case Interactive:
		// Latency-dominant, cost-aware. The near-tie band is anchored to the
		// fleet's *minimum* latency (computed once), so "near-best" is a fixed
		// set rather than a pairwise relation: candidates whose latency is
		// within the band of the best latency are mutually cost-ranked; nodes
		// outside the band lose on latency. A pairwise within() here would be
		// intransitive and make sort.SliceStable's winner depend on input
		// order (a determinism defect). See lat anchor below.
		anchor := minBy(cs, func(c Candidate) float64 { return c.LatencyMs })
		band := anchor*nearTieFrac + nearTieFloor
		near := func(c Candidate) bool { return c.LatencyMs <= anchor+band }
		less = func(a, b Candidate) bool {
			an, bn := near(a), near(b)
			if an != bn {
				return an // an anchored (near-best) node beats a non-anchored one
			}
			if !an {
				// Neither is near-best: pure latency, then ID.
				if a.LatencyMs != b.LatencyMs {
					return a.LatencyMs < b.LatencyMs
				}
				return a.ID < b.ID
			}
			// Both near-best: cheaper wins, then faster, then ID.
			if a.CostPerHour != b.CostPerHour {
				return a.CostPerHour < b.CostPerHour
			}
			if a.LatencyMs != b.LatencyMs {
				return a.LatencyMs < b.LatencyMs
			}
			return a.ID < b.ID
		}
	case Batch:
		// Cost-dominant, latency-aware. Symmetric to Interactive but anchored
		// to the fleet's *minimum* cost: candidates within the band of the
		// cheapest are mutually latency-ranked; the rest lose on cost. Anchoring
		// (vs a pairwise within()) keeps the comparator a total order so the
		// chosen target is order-independent.
		anchor := minBy(cs, func(c Candidate) float64 { return c.CostPerHour })
		band := anchor*nearTieFrac + nearTieFloor
		near := func(c Candidate) bool { return c.CostPerHour <= anchor+band }
		less = func(a, b Candidate) bool {
			an, bn := near(a), near(b)
			if an != bn {
				return an
			}
			if !an {
				if a.CostPerHour != b.CostPerHour {
					return a.CostPerHour < b.CostPerHour
				}
				return a.ID < b.ID
			}
			if a.LatencyMs != b.LatencyMs {
				return a.LatencyMs < b.LatencyMs
			}
			if a.CostPerHour != b.CostPerHour {
				return a.CostPerHour < b.CostPerHour
			}
			return a.ID < b.ID
		}
	case BestEffort:
		// Cheapest, preferring spot capacity. Among equal cost a spot node
		// is preferred (it is the canonical best-effort/reclaimable target);
		// then ID.
		less = func(a, b Candidate) bool {
			if a.CostPerHour != b.CostPerHour {
				return a.CostPerHour < b.CostPerHour
			}
			if a.Spot != b.Spot {
				return a.Spot // spot==true sorts before spot==false
			}
			return a.ID < b.ID
		}
	}

	sort.SliceStable(cs, func(i, j int) bool { return less(cs[i], cs[j]) })
	return Decision{Class: class, Target: cs[0]}, nil
}
