// Package redundantexec implements a BOINC-style redundant-execution trust
// scorer. A task is dispatched redundantly to N workers, each returning a
// result. A validator finds quorum (majority) agreement on the result value;
// the agreed value becomes the validated canonical result. Workers that agreed
// with the quorum gain trust; workers that disagreed (returned a minority
// value) lose trust. If no quorum is reached the task has no validated result
// and a typed error is returned.
//
// The package is pure Go, deterministic, and self-contained. It performs no
// network/host I/O and consults no wall clock: trust is updated purely from the
// supplied result set, so the same inputs always produce the same outputs.
package redundantexec

import (
	"errors"
	"fmt"
	"sort"
)

// ErrNoQuorum is returned by Validator.Validate when the submitted results do
// not reach the configured quorum threshold for any single value. When this
// error is returned, NO canonical value has been chosen.
var ErrNoQuorum = errors.New("redundantexec: no quorum reached")

// ErrNoResults is returned when Validate is called with an empty result set.
var ErrNoResults = errors.New("redundantexec: no results submitted")

// Worker is a participant that executes redundantly dispatched tasks. Trust is
// a running reputation score in the range [TrustFloor, TrustCeil].
type Worker struct {
	ID    string
	Trust float64
}

// Result pairs a worker ID with the value that worker returned for a task. The
// Value is compared by Go equality (==), so it must be a comparable type
// (string, integer, hash, etc.). Callers that need to compare structured data
// should hash it to a string first.
type Result struct {
	WorkerID string
	Value    any
}

// Validated is the outcome of a successful quorum validation. Value is the
// canonical result agreed upon by the quorum; AgreeingWorkers lists the IDs of
// the workers that returned that value (sorted for determinism); AgreeCount and
// TotalCount describe the quorum margin.
type Validated struct {
	TaskID          string
	Value           any
	AgreeingWorkers []string
	AgreeCount      int
	TotalCount      int
}

// Trust reward/penalty deltas and clamp bounds. Trust is a bounded reputation:
// agreeing nudges a worker up by TrustReward, disagreeing nudges it down by
// TrustPenalty, and the value is clamped to [TrustFloor, TrustCeil].
const (
	TrustReward  = 0.05
	TrustPenalty = 0.10
	TrustFloor   = 0.0
	TrustCeil    = 1.0
)

// Validator scores redundant executions. Quorum is the agreement requirement,
// expressed as a fraction of the total number of submitted results in the
// inclusive range (0, 1]. A value MUST strictly exceed the threshold count to
// win — i.e. agreeCount > ceil-style threshold. See thresholdCount for the
// exact documented rule.
type Validator struct {
	// QuorumFraction is the minimum fraction of the total results that must
	// agree on a single value for it to be validated. For example 0.5 means a
	// strict majority is required: with 5 results, ceil(0.5*5)=3 is the minimum
	// agreeing count, but the documented boundary rule (see thresholdCount)
	// requires agreeCount >= that minimum AND a strict plurality.
	QuorumFraction float64

	// workers holds the persistent trust state keyed by worker ID. It is
	// created lazily so a zero Validator is usable.
	workers map[string]*Worker
}

// NewValidator constructs a Validator with the given quorum fraction. It panics
// if fraction is not in (0, 1].
func NewValidator(quorumFraction float64) *Validator {
	if quorumFraction <= 0 || quorumFraction > 1 {
		panic(fmt.Sprintf("redundantexec: quorum fraction %v out of range (0,1]", quorumFraction))
	}
	return &Validator{
		QuorumFraction: quorumFraction,
		workers:        make(map[string]*Worker),
	}
}

// Trust returns the current trust score for a worker. Unknown workers default
// to InitialTrust.
func (v *Validator) Trust(workerID string) float64 {
	if v.workers == nil {
		return InitialTrust
	}
	if w, ok := v.workers[workerID]; ok {
		return w.Trust
	}
	return InitialTrust
}

// InitialTrust is the trust assigned to a worker the first time it is seen.
const InitialTrust = 0.5

// thresholdCount returns the minimum number of agreeing results required for a
// value to reach quorum given total submitted results.
//
// Documented rule: a value reaches quorum iff its agreeing count is
// >= ceil(QuorumFraction * total). For QuorumFraction == 0.5 and total == 4,
// ceil(0.5*4) == 2, so 2 agreeing results meet the threshold count; however a
// 2-2 tie does not yield a unique winner, so Validate additionally requires a
// strict plurality (no other value ties the leader). The boundary therefore
// behaves as: exactly-at-threshold with a unique leader VALIDATES; a tie at the
// threshold does NOT (ErrNoQuorum).
func (v *Validator) thresholdCount(total int) int {
	// ceil(frac * total) without floating round-off surprises.
	t := int(v.QuorumFraction * float64(total))
	if float64(t) < v.QuorumFraction*float64(total) {
		t++
	}
	if t < 1 {
		t = 1
	}
	return t
}

// Validate inspects the redundant results for taskID, finds the quorum-agreed
// canonical value, updates the trust of every participating worker (reward for
// agreeing with the quorum, penalty for disagreeing), and returns the Validated
// outcome.
//
// If no value reaches the quorum threshold, or the leader is tied, ErrNoQuorum
// is returned and NO trust is changed and NO canonical value is chosen.
func (v *Validator) Validate(taskID string, results []Result) (Validated, error) {
	if v.workers == nil {
		v.workers = make(map[string]*Worker)
	}
	if len(results) == 0 {
		return Validated{}, ErrNoResults
	}

	total := len(results)

	// Tally agreement per value. We keep insertion order of distinct values so
	// tie-breaking is deterministic and independent of map iteration order.
	type tally struct {
		value any
		count int
		order int
	}
	tallies := make(map[any]*tally)
	order := 0
	for _, r := range results {
		t, ok := tallies[r.Value]
		if !ok {
			t = &tally{value: r.Value, order: order}
			order++
			tallies[r.Value] = t
		}
		t.count++
	}

	// Find the leading value (highest count). Ties are detected explicitly.
	var leader *tally
	leaderTied := false
	for _, t := range tallies {
		switch {
		case leader == nil || t.count > leader.count:
			leader = t
			leaderTied = false
		case t.count == leader.count:
			leaderTied = true
		}
	}

	threshold := v.thresholdCount(total)

	// Quorum requires: leader meets threshold count AND is a strict plurality
	// (no tie for the lead).
	if leader == nil || leader.count < threshold || leaderTied {
		return Validated{}, ErrNoQuorum
	}

	canonical := leader.value

	// Update trust: reward agreers, penalize disagreers. Trust mutation only
	// happens once a quorum has been established, so non-quorum tasks never
	// perturb reputation.
	agreeing := make([]string, 0, leader.count)
	for _, r := range results {
		w := v.worker(r.WorkerID)
		if r.Value == canonical {
			w.Trust = clamp(w.Trust + TrustReward)
			agreeing = append(agreeing, r.WorkerID)
		} else {
			w.Trust = clamp(w.Trust - TrustPenalty)
		}
	}
	sort.Strings(agreeing)

	return Validated{
		TaskID:          taskID,
		Value:           canonical,
		AgreeingWorkers: agreeing,
		AgreeCount:      leader.count,
		TotalCount:      total,
	}, nil
}

// worker returns the persistent worker record, creating it at InitialTrust on
// first sight.
func (v *Validator) worker(id string) *Worker {
	w, ok := v.workers[id]
	if !ok {
		w = &Worker{ID: id, Trust: InitialTrust}
		v.workers[id] = w
	}
	return w
}

// clamp bounds a trust value to [TrustFloor, TrustCeil].
func clamp(x float64) float64 {
	if x < TrustFloor {
		return TrustFloor
	}
	if x > TrustCeil {
		return TrustCeil
	}
	return x
}
