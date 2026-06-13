// Package edgeregistry implements a deterministic, pure-Go multi-tier edge node
// REGISTRATION registry for registry item HXC-1184.
//
// Edge devices across tiers T3..T8 register with the cluster. Each registration
// carries a device-tier label, a trust level, and a capability advertisement
// (compute class, memory, supported backends). The registry stores registrations
// keyed by node id, exposes lookup, tier-filtered listing, and a deterministic
// dump enumerating every registered node with its tier + trust + capabilities.
//
// This package is self-contained: it defines its own minimal node/tier/capability
// types and does NOT wrap any other in-repo package. It performs no network, host,
// or hardware access — it is the deterministic decision/verification logic only.
package edgeregistry

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Tier is a device tier label. The registry only admits tiers in the closed
// range T3..T8 (inclusive); any other value is rejected as out-of-range.
type Tier int

const (
	// TierMin / TierMax bound the admissible tier range (T3..T8).
	TierMin Tier = 3
	TierMax Tier = 8

	T3 Tier = 3
	T4 Tier = 4
	T5 Tier = 5
	T6 Tier = 6
	T7 Tier = 7
	T8 Tier = 8
)

// Valid reports whether t is within the admissible T3..T8 range.
func (t Tier) Valid() bool { return t >= TierMin && t <= TierMax }

// String renders the tier as its "Tn" label, or "T?(n)" when out of range so
// that a dropped/mis-assigned tier is observable rather than silently blank.
func (t Tier) String() string {
	if t.Valid() {
		return fmt.Sprintf("T%d", int(t))
	}
	return fmt.Sprintf("T?(%d)", int(t))
}

// TrustLevel describes how much the cluster trusts a registered edge node.
type TrustLevel int

const (
	TrustUntrusted TrustLevel = iota // no attestation
	TrustBasic                       // identity asserted, no attestation
	TrustAttested                    // hardware/software attested
	TrustVerified                    // attested + cluster-verified
)

// String renders the trust level as a stable human-readable label.
func (tl TrustLevel) String() string {
	switch tl {
	case TrustUntrusted:
		return "untrusted"
	case TrustBasic:
		return "basic"
	case TrustAttested:
		return "attested"
	case TrustVerified:
		return "verified"
	default:
		return fmt.Sprintf("trust(%d)", int(tl))
	}
}

// Capability is a single capability advertisement entry for an edge node.
type Capability struct {
	ComputeClass string   // e.g. "cpu-x64", "gpu-cuda", "npu"
	MemoryMB     int      // advertised usable memory in MB
	Backends     []string // supported execution backends, e.g. {"llama.cpp","onnx"}
}

// clone returns a deep copy so stored capabilities are isolated from caller mutation.
func (c Capability) clone() Capability {
	cp := c
	if c.Backends != nil {
		cp.Backends = append([]string(nil), c.Backends...)
	}
	return cp
}

// Registration is one edge node's registration record.
type Registration struct {
	NodeID string
	Tier   Tier
	Trust  TrustLevel
	Caps   Capability
}

// clone returns a deep copy of the registration.
func (r Registration) clone() Registration {
	cp := r
	cp.Caps = r.Caps.clone()
	return cp
}

// Typed registration errors.
var (
	// ErrEmptyNodeID is returned when a registration has no node id.
	ErrEmptyNodeID = errors.New("edgeregistry: empty node id")
	// ErrTierOutOfRange is returned when a registration's tier is outside T3..T8.
	ErrTierOutOfRange = errors.New("edgeregistry: tier out of range (must be T3..T8)")
	// ErrNotFound is returned by Get for an unknown node id.
	ErrNotFound = errors.New("edgeregistry: node not found")
)

// Registry stores edge node registrations keyed by node id.
//
// The zero value is NOT ready; use New. All reads/writes are deterministic and
// produce sorted output independent of insertion order.
//
// Concurrency: a Registry is safe for concurrent use by multiple goroutines.
// Edge devices register (writes) while the control plane looks them up (reads)
// at the same time; mu guards the byID map so a register never races a lookup.
// Writers take mu.Lock; readers take mu.RLock.
type Registry struct {
	mu   sync.RWMutex
	byID map[string]Registration
}

// New constructs an empty Registry.
func New() *Registry {
	return &Registry{byID: make(map[string]Registration)}
}

// Register validates and stores reg. The tier MUST be within T3..T8 and the
// node id MUST be non-empty. Re-registering an existing node id updates it in
// place (no duplicate entry is created).
func (r *Registry) Register(reg Registration) error {
	if reg.NodeID == "" {
		return ErrEmptyNodeID
	}
	if !reg.Tier.Valid() {
		return fmt.Errorf("%w: got %d", ErrTierOutOfRange, int(reg.Tier))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[reg.NodeID] = reg.clone()
	return nil
}

// Get returns the registration for id, or ErrNotFound if no such node exists.
func (r *Registry) Get(id string) (Registration, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.byID[id]
	if !ok {
		return Registration{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return reg.clone(), nil
}

// Len reports the number of registered nodes.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID)
}

// ListByTier returns all registrations whose tier == tier, sorted by node id.
// A tier with no registered nodes yields an empty (non-nil) slice.
func (r *Registry) ListByTier(tier Tier) []Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Registration, 0)
	for _, reg := range r.byID {
		if reg.Tier == tier {
			out = append(out, reg.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// Dump returns every registration sorted deterministically by node id. The
// returned slice is a deep copy; mutating it does not affect the registry.
func (r *Registry) Dump() []Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Registration, 0, len(r.byID))
	for _, reg := range r.byID {
		out = append(out, reg.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}
