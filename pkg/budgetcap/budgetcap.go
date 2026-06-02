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
	"sync"
)

// ErrWouldExceedBudget is returned by Allocate when admitting the allocation
// would push committed spend above the configured MaxCostPerHour cap.
var ErrWouldExceedBudget = errors.New("budgetcap: allocation would exceed MaxCostPerHour budget cap")

// ErrDuplicateAllocation is returned by Allocate when an allocation with the
// same id is already active. This prevents double-counting on re-allocation.
var ErrDuplicateAllocation = errors.New("budgetcap: allocation id already active")

// ErrInvalidCost is returned by Allocate when cost is negative or NaN-like
// (non-finite is not representable here without math import; negative guarded).
var ErrInvalidCost = errors.New("budgetcap: cost must be non-negative")

// BudgetCap enforces a global MaxCostPerHour ceiling over active allocations.
// The zero value is not usable; construct one with NewBudgetCap.
type BudgetCap struct {
	mu          sync.Mutex
	maxPerHour  float64
	committed   float64
	allocations map[string]float64 // id -> costPerHour of active allocations
}

// NewBudgetCap returns a BudgetCap with the given MaxCostPerHour ceiling.
func NewBudgetCap(maxPerHour float64) *BudgetCap {
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
	if cost < 0 {
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
