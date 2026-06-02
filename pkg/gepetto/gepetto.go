// Package gepetto implements the HelixGepetto dual-resource arbitration
// strategy that decides how much overflow capacity to reserve on the Chutes
// marketplace as local GPU load rises, with an anti-starvation high-water
// cut-off.
//
// Reserve schedule (per-GPU chutes overflow capacity):
//
//	load < 0.10  → 0   (idle: no overflow reservation needed)
//	load < 0.40  → 2   (light load: small reserve)
//	load < 0.70  → 5   (moderate load: moderate reserve)
//	load ≤ 0.80  → 8   (heavy load: maximum reserve)
//	load > 0.80  → 0   (anti-starvation: above high-water mark, no reservation)
//
// Anti-starvation rule: when loadFraction is STRICTLY GREATER than 0.80 (the
// high-water mark) the returned chutes capacity is exactly zero so local
// interactive work is never starved by overflow reservation.
//
// Audit trail: every call records the runID, input loadFraction, gpusPerNode,
// and resolved capacity in an append-only in-memory log accessible via
// Records().
package gepetto

import "sync"

// highWaterMark is the load fraction above which zero capacity is reserved to
// prevent local interactive work from being starved by Chutes overflow.
// MUTATION GUARD — removing the branch that enforces this makes
// TestZeroCapacityAboveHighWater fail: the 0.85 case would return 8 instead
// of 0.
const highWaterMark = 0.80

// loadTier describes a single entry in the reserve schedule.
type loadTier struct {
	// below is the exclusive upper bound of the load fraction for this tier.
	below float64
	// reservePerGPU is the per-GPU chutes overflow capacity assigned to this tier.
	reservePerGPU int
}

// reserveSchedule is the ordered list of tiers from lowest to highest load.
// Tiers are evaluated in order; the first whose below threshold exceeds the
// load fraction wins. The anti-starvation cut-off (> highWaterMark → 0) is
// applied before consulting this table, so the last tier (below=0.81) covers
// exactly load ≤ 0.80.
var reserveSchedule = []loadTier{
	{below: 0.10, reservePerGPU: 0},
	{below: 0.40, reservePerGPU: 2},
	{below: 0.70, reservePerGPU: 5},
	{below: 0.81, reservePerGPU: 8}, // covers (0.70, 0.80] inclusive
}

// Record holds the immutable audit entry written by every ChutesCapacity call.
type Record struct {
	// RunID is the caller-supplied correlation identifier.
	RunID string
	// LoadFraction is the local GPU load reported by the caller.
	LoadFraction float64
	// GPUsPerNode is the number of GPUs per node supplied by the caller.
	GPUsPerNode int
	// ResolvedCapacity is the per-GPU overflow capacity that was returned.
	ResolvedCapacity int
}

// Arbitrator is the HelixGepetto dual-resource arbitration engine.
// The zero value is ready for use; construct with NewArbitrator for an
// explicit pre-allocated log.
type Arbitrator struct {
	mu      sync.Mutex
	records []Record
}

// NewArbitrator returns an Arbitrator with a pre-allocated record log of the
// given capacity (a hint; the log grows beyond it as needed).
func NewArbitrator(logCapacity int) *Arbitrator {
	if logCapacity < 0 {
		logCapacity = 0
	}
	return &Arbitrator{records: make([]Record, 0, logCapacity)}
}

// ChutesCapacity returns the per-GPU chutes overflow capacity to reserve for
// the given local GPU loadFraction and gpusPerNode count.
//
// Anti-starvation rule (MUTATION GUARD — see TestZeroCapacityAboveHighWater):
// when loadFraction is strictly greater than highWaterMark (0.80) the returned
// capacity is exactly zero, regardless of gpusPerNode.
//
// Below the high-water mark the capacity follows the reserve schedule. Callers
// may pass any gpusPerNode ≥ 0; the value is recorded verbatim and does not
// affect the per-GPU capacity calculation.
func (a *Arbitrator) ChutesCapacity(runID string, loadFraction float64, gpusPerNode int) int {
	// Anti-starvation high-water cut-off.
	// MUTATION GUARD: removing this branch makes TestZeroCapacityAboveHighWater fail.
	if loadFraction > highWaterMark {
		a.record(runID, loadFraction, gpusPerNode, 0)
		return 0
	}

	capacity := resolveFromSchedule(loadFraction)
	a.record(runID, loadFraction, gpusPerNode, capacity)
	return capacity
}

// resolveFromSchedule walks the reserve schedule and returns the per-GPU
// capacity for the given loadFraction. When loadFraction does not match any
// tier (e.g. negative or NaN) zero is returned defensively.
func resolveFromSchedule(loadFraction float64) int {
	for _, tier := range reserveSchedule {
		if loadFraction < tier.below {
			return tier.reservePerGPU
		}
	}
	// loadFraction ≥ 0.81: should be covered by the anti-starvation guard above
	// but return zero defensively in case of floating-point edge cases.
	return 0
}

// record appends an audit entry to the in-memory log. It is safe for
// concurrent use.
func (a *Arbitrator) record(runID string, loadFraction float64, gpusPerNode, capacity int) {
	a.mu.Lock()
	a.records = append(a.records, Record{
		RunID:            runID,
		LoadFraction:     loadFraction,
		GPUsPerNode:      gpusPerNode,
		ResolvedCapacity: capacity,
	})
	a.mu.Unlock()
}

// Records returns a snapshot of all audit records written so far. The returned
// slice is a copy; mutations by the caller do not affect the internal log.
func (a *Arbitrator) Records() []Record {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Record, len(a.records))
	copy(out, a.records)
	return out
}
