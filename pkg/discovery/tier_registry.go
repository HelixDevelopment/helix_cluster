package discovery

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/HelixDevelopment/helix_cluster/pkg/tierdef"
)

// ErrInvalidTier is returned when an unknown tier name is supplied to TierRegistry.
var ErrInvalidTier = errors.New("tier not found in tierdef registry")

// tierEntry is one registered instance together with its resolved tier rank.
type tierEntry struct {
	inst *Instance
	tier string
	rank int // position in tierdef.Registry.Tiers (0 = T1, 14 = T15)
}

// TierRegistry is a thread-safe, in-memory registry that associates each
// Instance with a tier from a [tierdef.Registry]. Instances can be selected
// by minimum tier rank.
type TierRegistry struct {
	mu      sync.RWMutex
	td      *tierdef.Registry
	entries map[string]*tierEntry // keyed by Instance.ID
}

// NewTierRegistry creates a TierRegistry backed by the given [tierdef.Registry].
// td must not be nil.
func NewTierRegistry(td *tierdef.Registry) *TierRegistry {
	if td == nil {
		panic("discovery.NewTierRegistry: nil tierdef.Registry")
	}
	return &TierRegistry{
		td:      td,
		entries: make(map[string]*tierEntry),
	}
}

// Register adds or replaces inst in the registry under the given tier name.
// It returns [ErrInvalidTier] (wrapped) when tier is not present in the
// backing [tierdef.Registry], and [ErrNilInstance] when inst is nil.
func (r *TierRegistry) Register(inst *Instance, tier string) error {
	if inst == nil {
		return fmt.Errorf("tier_registry.Register: %w", ErrNilInstance)
	}
	// Validate tier against the canonical tier ordering.
	rank, err := r.rankOf(tier)
	if err != nil {
		return fmt.Errorf("tier_registry.Register tier %q: %w", tier, ErrInvalidTier)
	}
	// Copy the instance so callers cannot mutate the stored value.
	instCopy := *inst
	entry := &tierEntry{
		inst: &instCopy,
		tier: tier,
		rank: rank,
	}
	r.mu.Lock()
	r.entries[inst.ID] = entry
	r.mu.Unlock()
	return nil
}

// Deregister removes the instance with the given id from the registry. It is a
// no-op if the id is not registered.
func (r *TierRegistry) Deregister(id string) {
	r.mu.Lock()
	delete(r.entries, id)
	r.mu.Unlock()
}

// SelectByTier returns all registered instances whose tier rank is greater than
// or equal to the rank of minTier, ordered ascending by tier (T1 first, T15
// last). Instances at the same tier are ordered by ID for determinism.
//
// It returns [ErrInvalidTier] (wrapped) when minTier is not recognised by the
// backing [tierdef.Registry].
func (r *TierRegistry) SelectByTier(minTier string) ([]*Instance, error) {
	minRank, err := r.rankOf(minTier)
	if err != nil {
		return nil, fmt.Errorf("SelectByTier: tier %q: %w", minTier, ErrInvalidTier)
	}

	r.mu.RLock()
	// Collect matching entries into a local slice so we can sort without
	// holding the lock longer than needed.
	var matched []*tierEntry
	for _, e := range r.entries {
		if e.rank >= minRank {
			matched = append(matched, e)
		}
	}
	r.mu.RUnlock()

	// Sort ascending by (rank, ID) for a stable, deterministic order.
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].rank != matched[j].rank {
			return matched[i].rank < matched[j].rank
		}
		return matched[i].inst.ID < matched[j].inst.ID
	})

	out := make([]*Instance, len(matched))
	for i, e := range matched {
		instCopy := *e.inst
		out[i] = &instCopy
	}
	return out, nil
}

// SelectExact returns all registered instances whose tier is exactly tier,
// ordered by ID.  It returns [ErrInvalidTier] for an unrecognised tier.
func (r *TierRegistry) SelectExact(tier string) ([]*Instance, error) {
	rank, err := r.rankOf(tier)
	if err != nil {
		return nil, fmt.Errorf("SelectExact: tier %q: %w", tier, ErrInvalidTier)
	}

	r.mu.RLock()
	var matched []*tierEntry
	for _, e := range r.entries {
		if e.rank == rank {
			matched = append(matched, e)
		}
	}
	r.mu.RUnlock()

	sort.Slice(matched, func(i, j int) bool {
		return matched[i].inst.ID < matched[j].inst.ID
	})

	out := make([]*Instance, len(matched))
	for i, e := range matched {
		instCopy := *e.inst
		out[i] = &instCopy
	}
	return out, nil
}

// rankOf resolves a tier name to its 0-based position in td.Tiers.
func (r *TierRegistry) rankOf(tier string) (int, error) {
	for i, td := range r.td.Tiers {
		if td.Tier == tier {
			return i, nil
		}
	}
	return -1, ErrInvalidTier
}
