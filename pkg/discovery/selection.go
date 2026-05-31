// Package discovery provides service discovery for Helix Cluster OS.
package discovery

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// ErrNoInstances is returned when selection finds no eligible (healthy,
// non-expired) instance for a service.
var ErrNoInstances = errors.New("no eligible instances available")

// WeightedSelector performs deterministic health-aware weighted load balancing
// using the smooth weighted round-robin (SWRR) algorithm — the same scheme used
// by nginx upstream balancing.
//
// SWRR is fully deterministic and stateful: it carries a "current weight" per
// instance ID across Pick calls and contains NO time-based randomness, so a
// given sequence of inputs always yields the same sequence of picks. Over a
// full cycle (sum of effective weights) each instance is chosen exactly in
// proportion to its weight, and selections are smoothly interleaved rather than
// returned in bursts.
type WeightedSelector struct {
	mu sync.Mutex
	// current holds the running "current weight" state keyed by instance ID.
	current map[string]int
}

// NewWeightedSelector returns a ready-to-use selector with empty state.
func NewWeightedSelector() *WeightedSelector {
	return &WeightedSelector{current: make(map[string]int)}
}

// effectiveWeight returns the weight used for balancing; values <= 0 default to 1.
func effectiveWeight(inst *Instance) int {
	if inst.Weight <= 0 {
		return 1
	}
	return inst.Weight
}

// Pick selects one instance from the given slice using smooth weighted
// round-robin. It returns nil if the slice is empty. The caller is responsible
// for passing only eligible (healthy, non-expired) instances; Pick itself does
// not filter, keeping the algorithm reusable.
//
// State is keyed by instance ID, so instances appearing/disappearing between
// calls are handled gracefully (stale IDs are pruned).
func (s *WeightedSelector) Pick(instances []*Instance) *Instance {
	if len(instances) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Iterate in a stable, deterministic order independent of caller slice order
	// so that two selectors fed equivalent inputs produce identical sequences.
	ordered := make([]*Instance, len(instances))
	copy(ordered, instances)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	// Prune state for IDs no longer present.
	present := make(map[string]struct{}, len(ordered))
	for _, inst := range ordered {
		present[inst.ID] = struct{}{}
	}
	for id := range s.current {
		if _, ok := present[id]; !ok {
			delete(s.current, id)
		}
	}

	total := 0
	var best *Instance
	bestCurrent := 0
	for _, inst := range ordered {
		w := effectiveWeight(inst)
		total += w
		cur := s.current[inst.ID] + w
		s.current[inst.ID] = cur
		if best == nil || cur > bestCurrent {
			best = inst
			bestCurrent = cur
		}
	}

	if best != nil {
		s.current[best.ID] -= total
	}
	return best
}

// Select returns a single healthy, non-expired instance for the service chosen
// by deterministic weighted load balancing. Eligibility is evaluated against
// the registry's injected Clock so expired instances are excluded without any
// real sleep. It returns ErrNoInstances if nothing is eligible.
func (r *ServiceRegistry) Select(ctx context.Context, service string) (*Instance, error) {
	// Lookup already filters to healthy instances from the backend.
	candidates, err := r.Lookup(ctx, service)
	if err != nil {
		return nil, err
	}

	now := r.now()
	eligible := candidates[:0]
	for _, inst := range candidates {
		if !inst.Healthy {
			continue
		}
		if inst.TTL > 0 && !inst.LastSeen.IsZero() && now.Sub(inst.LastSeen) > inst.TTL {
			continue // expired
		}
		eligible = append(eligible, inst)
	}

	pick := r.selector.Pick(eligible)
	if pick == nil {
		return nil, ErrNoInstances
	}
	return pick, nil
}
