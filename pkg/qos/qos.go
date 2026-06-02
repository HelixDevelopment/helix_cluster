// Package qos implements QoS-tier classification and routing for HelixCluster.
//
// A workload declares (or is classified into) one of four QoS classes. The
// router maps each class to a distinct placement objective over a fleet of
// candidate placements:
//
//   - RealTime    -> the LOWEST-LATENCY candidate (latency is paramount).
//   - Interactive -> low-latency but cost-aware (latency-dominant, cost
//     breaks near-ties so an absurdly expensive fastest node loses to a
//     comparably-fast cheaper one).
//   - Batch       -> throughput/cost balanced (cost-dominant, latency only
//     breaks near-ties).
//   - BestEffort  -> the CHEAPEST candidate, preferring spot capacity.
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

// within reports whether a and b are within the near-tie band of each other.
func within(a, b float64) bool {
	lo := a
	if b < lo {
		lo = b
	}
	band := lo*nearTieFrac + nearTieFloor
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= band
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
		// Latency-dominant, cost-aware. Latency decides unless the two are
		// within the near-tie band, in which case the cheaper one wins.
		less = func(a, b Candidate) bool {
			if !within(a.LatencyMs, b.LatencyMs) {
				return a.LatencyMs < b.LatencyMs
			}
			if a.CostPerHour != b.CostPerHour {
				return a.CostPerHour < b.CostPerHour
			}
			if a.LatencyMs != b.LatencyMs {
				return a.LatencyMs < b.LatencyMs
			}
			return a.ID < b.ID
		}
	case Batch:
		// Cost-dominant, latency-aware. Cost decides unless within the
		// near-tie band, then the faster one wins.
		less = func(a, b Candidate) bool {
			if !within(a.CostPerHour, b.CostPerHour) {
				return a.CostPerHour < b.CostPerHour
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
