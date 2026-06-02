// Package costsched implements a deterministic, cost-aware GPU scheduler.
//
// A CostAwareScheduler selects the cheapest GPU Candidate that satisfies a
// given SLA. Selection is pure and deterministic: candidates are first
// filtered to those that MEET the SLA, then the lowest CostPerHour candidate
// is chosen, with ties broken by lexicographic Candidate ID. If no candidate
// meets the SLA, ErrNoEligible is returned.
//
// The scheduler records the cost of the most recent successful selection so
// callers can observe sink-side what was actually committed.
package costsched

import (
	"errors"
	"sort"
)

// ErrNoEligible is returned by Select when no candidate satisfies the SLA.
var ErrNoEligible = errors.New("costsched: no candidate meets the SLA")

// Candidate is a schedulable GPU offer with a price and SLA-relevant
// capability attributes.
type Candidate struct {
	// ID uniquely identifies the candidate and is used as a deterministic
	// tie-break when two candidates share the lowest cost.
	ID string

	// CostPerHour is the price of running this candidate for one hour.
	CostPerHour float64

	// VRAMGiB is the amount of GPU memory the candidate provides.
	VRAMGiB int

	// MaxLatencyMs is the best (lowest) latency the candidate can guarantee.
	// A candidate satisfies an SLA only if this value is <= the SLA's
	// MaxLatencyMs requirement.
	MaxLatencyMs int

	// ModelsSupported is the set of model identifiers this candidate can run.
	ModelsSupported map[string]bool
}

// SLA expresses the minimum requirements a Candidate must satisfy to be
// eligible for selection.
type SLA struct {
	// MinVRAMGiB is the minimum GPU memory required.
	MinVRAMGiB int

	// MaxLatencyMs is the maximum tolerable guaranteed latency. A candidate is
	// eligible only if its MaxLatencyMs is <= this value.
	MaxLatencyMs int

	// RequiredModel, if non-empty, must be present in a candidate's
	// ModelsSupported set for the candidate to be eligible.
	RequiredModel string
}

// Meets reports whether c satisfies every requirement of the SLA.
func (c Candidate) Meets(sla SLA) bool {
	if c.VRAMGiB < sla.MinVRAMGiB {
		return false
	}
	if c.MaxLatencyMs > sla.MaxLatencyMs {
		return false
	}
	if sla.RequiredModel != "" && !c.ModelsSupported[sla.RequiredModel] {
		return false
	}
	return true
}

// CostAwareScheduler selects SLA-meeting candidates by lowest cost and records
// the most recently selected cost.
type CostAwareScheduler struct {
	lastSelectedCost float64
	hasSelection     bool
}

// NewCostAwareScheduler returns a ready-to-use scheduler.
func NewCostAwareScheduler() *CostAwareScheduler {
	return &CostAwareScheduler{}
}

// Select filters candidates to those that MEET the SLA, then returns the one
// with the lowest CostPerHour. Ties are broken deterministically by ascending
// Candidate ID. If no candidate meets the SLA, it returns ErrNoEligible and
// does not update the recorded selection cost.
//
// The input slice is not mutated.
func (s *CostAwareScheduler) Select(candidates []Candidate, sla SLA) (Candidate, error) {
	eligible := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Meets(sla) {
			eligible = append(eligible, c)
		}
	}
	if len(eligible) == 0 {
		return Candidate{}, ErrNoEligible
	}

	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].CostPerHour != eligible[j].CostPerHour {
			return eligible[i].CostPerHour < eligible[j].CostPerHour
		}
		return eligible[i].ID < eligible[j].ID
	})

	chosen := eligible[0]
	s.lastSelectedCost = chosen.CostPerHour
	s.hasSelection = true
	return chosen, nil
}

// LastSelectedCost returns the CostPerHour of the most recent successful
// Select and whether any selection has been recorded.
func (s *CostAwareScheduler) LastSelectedCost() (cost float64, ok bool) {
	return s.lastSelectedCost, s.hasSelection
}
