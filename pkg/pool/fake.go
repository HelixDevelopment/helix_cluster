package pool

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// ReferenceProvider is a software, in-memory GPUProvider. It is the HONEST
// stand-in for a real cloud GPU provisioning API: it allocates Instances from a
// monotonic counter, holds them in a map, and enforces a Capacity quota — but
// it never talks to any cloud. A production adapter would implement GPUProvider
// against a real API while preserving this exact contract.
//
// It also records call counts (ProvisionCalls / ReleaseCalls) so tests can
// assert the manager ACTUALLY drove the provider, not merely that the pool's
// own bookkeeping changed. ReferenceProvider is safe for concurrent use.
type ReferenceProvider struct {
	mu       sync.Mutex
	name     string
	capacity int
	seq      int
	live     map[string]Instance

	provisionCalls int
	releaseCalls   int

	// failProvision, when set, makes the next Provision return this error and
	// then clears itself. Used to surface provisioning failure deterministically.
	failProvision error
}

// NewReferenceProvider builds a provider with the given name prefix (used to
// make instance IDs human-readable and provider-unique) and capacity quota.
func NewReferenceProvider(name string, capacity int) *ReferenceProvider {
	return &ReferenceProvider{
		name:     name,
		capacity: capacity,
		live:     make(map[string]Instance),
	}
}

// Capacity implements GPUProvider.
func (r *ReferenceProvider) Capacity() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.capacity
}

// FailNextProvision arms the provider so the next Provision call returns err.
// It is a test seam for exercising the provisioning-failure path.
func (r *ReferenceProvider) FailNextProvision(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failProvision = err
}

// Provision implements GPUProvider. It returns an error if armed via
// FailNextProvision or if the live count is already at capacity.
func (r *ReferenceProvider) Provision(ctx context.Context, spec Spec) (Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.provisionCalls++

	if r.failProvision != nil {
		err := r.failProvision
		r.failProvision = nil
		return Instance{}, err
	}
	if len(r.live) >= r.capacity {
		return Instance{}, fmt.Errorf("referenceprovider %q: at capacity %d", r.name, r.capacity)
	}
	r.seq++
	inst := Instance{
		ID:        fmt.Sprintf("%s-%04d", r.name, r.seq),
		GPUType:   spec.GPUType,
		HourlyUSD: spec.HourlyUSD,
	}
	r.live[inst.ID] = inst
	return inst, nil
}

// Release implements GPUProvider. Releasing an instance it does not hold is an
// error, so double-release / wrong-provider bugs surface instead of silently
// passing.
func (r *ReferenceProvider) Release(ctx context.Context, inst Instance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseCalls++
	if _, ok := r.live[inst.ID]; !ok {
		return fmt.Errorf("referenceprovider %q: release of unknown instance %q", r.name, inst.ID)
	}
	delete(r.live, inst.ID)
	return nil
}

// List implements GPUProvider, returning the live instances in ID order.
func (r *ReferenceProvider) List(ctx context.Context) ([]Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Instance, 0, len(r.live))
	for _, inst := range r.live {
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// LiveCount is the sink-side truth: how many instances the provider itself
// currently holds. Tests assert on this to prove provisioning/release really
// reached the provider.
func (r *ReferenceProvider) LiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.live)
}

// ProvisionCalls returns how many times Provision was invoked.
func (r *ReferenceProvider) ProvisionCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.provisionCalls
}

// ReleaseCalls returns how many times Release was invoked.
func (r *ReferenceProvider) ReleaseCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.releaseCalls
}
