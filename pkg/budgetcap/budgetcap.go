// Package budgetcap implements a global MaxCostPerHour budget cap enforcer
// over allocations. It is pure Go, deterministic, and safe for concurrent use.
//
// A BudgetCap is configured with a MaxCostPerHour ceiling. It tracks the
// committed spend as the sum of the costs of all currently-active allocations.
// Allocate admits a new allocation iff committed+cost <= cap; otherwise it
// rejects with ErrWouldExceedBudget and leaves committed unchanged. Release
// removes an allocation, freeing its cost from committed. The committed total
// must never exceed the configured cap.
package budgetcap

import (
	"errors"
	"math"
	"sync"
)

// ErrWouldExceedBudget is returned by Allocate when admitting the allocation
// would push committed spend above the configured MaxCostPerHour cap.
var ErrWouldExceedBudget = errors.New("budgetcap: allocation would exceed MaxCostPerHour budget cap")

// ErrDuplicateAllocation is returned by Allocate when an allocation with the
// same id is already active. This prevents double-counting on re-allocation.
var ErrDuplicateAllocation = errors.New("budgetcap: allocation id already active")

// ErrInvalidCost is returned by Allocate when cost is negative or non-finite
// (NaN or ±Inf). Non-finite costs are rejected because a NaN would otherwise
// silently poison the running total and bypass the cap entirely.
var ErrInvalidCost = errors.New("budgetcap: cost must be non-negative and finite")

// BudgetCap enforces a global MaxCostPerHour ceiling over active allocations.
// The zero value is not usable; construct one with NewBudgetCap.
type BudgetCap struct {
	mu          sync.Mutex
	maxPerHour  float64
	committed   float64
	allocations map[string]float64 // id -> costPerHour of active allocations
}

// NewBudgetCap returns a BudgetCap with the given MaxCostPerHour ceiling.
//
// A NaN ceiling is invalid configuration and is clamped to 0 (the most
// restrictive, fail-closed cap). This is safety-critical: with a NaN cap the
// admission test committed+cost > cap is NaN>NaN == false for EVERY charge, so
// an un-sanitized NaN cap would admit every allocation of arbitrary size — a
// total cap bypass / unbounded over-spend (the construction-side twin of the
// NaN-cost poison). +Inf is preserved as a legitimate "unlimited" ceiling.
func NewBudgetCap(maxPerHour float64) *BudgetCap {
	if math.IsNaN(maxPerHour) {
		maxPerHour = 0
	}
	return &BudgetCap{
		maxPerHour:  maxPerHour,
		allocations: make(map[string]float64),
	}
}

// Allocate admits an allocation of cost (per hour) under id. It returns nil and
// records the allocation iff committed+cost <= cap; otherwise it returns
// ErrWouldExceedBudget and does NOT change committed. A duplicate id is
// rejected with ErrDuplicateAllocation; a negative cost with ErrInvalidCost.
func (b *BudgetCap) Allocate(id string, cost float64) error {
	// Reject negative AND non-finite (NaN/±Inf) costs. NaN is the dangerous one:
	// NaN<0 is false and committed+NaN>maxPerHour is false (every NaN comparison
	// is false), so a single NaN cost would pass admission, poison `committed` to
	// NaN permanently, and thereafter admit EVERY allocation regardless of size —
	// a total cap bypass / unbounded over-spend. (The ErrInvalidCost contract
	// already documents "negative or NaN-like"; this makes the code honor it.)
	if cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return ErrInvalidCost
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.allocations[id]; exists {
		return ErrDuplicateAllocation
	}
	// Admission check: the reserve must never be violated.
	if b.committed+cost > b.maxPerHour {
		return ErrWouldExceedBudget
	}
	b.allocations[id] = cost
	b.committed += cost
	return nil
}

// Release removes the allocation under id, freeing its cost from committed.
// Releasing an unknown id is a no-op.
func (b *BudgetCap) Release(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cost, exists := b.allocations[id]
	if !exists {
		return
	}
	delete(b.allocations, id)
	b.committed -= cost
}

// Committed returns the current committed spend (sum of active allocation costs).
func (b *BudgetCap) Committed() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.committed
}

// Remaining returns the headroom under the cap: max(cap-committed, 0 not forced).
func (b *BudgetCap) Remaining() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxPerHour - b.committed
}
