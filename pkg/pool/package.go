// Package pool implements a GPU pool manager for Helix Cluster OS.
//
// A PoolManager maintains a desired number of GPU instances across one or more
// providers. It scales the pool UP by provisioning instances toward a target
// and scales it DOWN by releasing idle (un-leased) instances — never releasing
// one that a caller currently holds. Callers lease an instance with Acquire and
// return it with ReturnToPool; the manager tracks each instance's state
// (idle/busy) and exposes pool-wide utilization.
//
// HONEST EXTERNAL SEAM: real GPU capacity comes from a cloud provider's
// provisioning API (e.g. AWS, GCP, a bare-metal allocator). That live API is
// OUT OF SCOPE for this package and is modelled behind the GPUProvider
// interface. ReferenceProvider is a software stand-in that satisfies the same
// contract in-memory for tests and local runs; a real adapter would implement
// GPUProvider against the cloud API. The PoolManager logic is identical either
// way — it only consumes the interface.
package pool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrPoolEmpty is returned by Acquire when no idle instance is available.
var ErrPoolEmpty = errors.New("pool: no idle instance available")

// ErrUnknownInstance is returned when ReturnToPool is called with an instance
// the manager does not track (wrong ID, or already released).
var ErrUnknownInstance = errors.New("pool: unknown instance")

// Spec describes the kind of GPU instance to provision. It is intentionally
// small; a real provider would carry far more (zone, image, spot/on-demand).
type Spec struct {
	// GPUType is the requested accelerator model, e.g. "A100", "H100".
	GPUType string
	// HourlyUSD is the advertised price per instance-hour, used for TCO math.
	HourlyUSD float64
}

// Instance is a single provisioned GPU instance. ID is provider-unique and is
// the handle used to release or look the instance up.
type Instance struct {
	// ID uniquely identifies the instance within its provider.
	ID string
	// GPUType echoes the provisioned accelerator model.
	GPUType string
	// HourlyUSD is the per-hour price the provider committed to.
	HourlyUSD float64
}

// GPUProvider is the seam over the out-of-scope live provisioning API.
//
// Provision allocates one instance matching spec. Release frees a previously
// provisioned instance. List returns the instances the provider currently
// believes are live. Capacity is the hard upper bound on live instances this
// provider can hold at once (a quota); a Provision beyond it must fail.
type GPUProvider interface {
	Provision(ctx context.Context, spec Spec) (Instance, error)
	Release(ctx context.Context, inst Instance) error
	List(ctx context.Context) ([]Instance, error)
	Capacity() int
}

// instState is a tracked instance plus its lease state and owning provider.
type instState struct {
	inst     Instance
	provider GPUProvider
	busy     bool
}

// PoolManager maintains a desired pool size across one or more providers and
// hands out leases. It is safe for concurrent use.
type PoolManager struct {
	mu        sync.Mutex
	providers []GPUProvider
	spec      Spec
	// tracked maps instance ID -> its state. This is the authoritative pool.
	tracked map[string]*instState
}

// NewPoolManager builds a manager that provisions instances of spec across the
// given providers (tried in order when scaling up). At least one provider is
// required.
func NewPoolManager(spec Spec, providers ...GPUProvider) (*PoolManager, error) {
	if len(providers) == 0 {
		return nil, errors.New("pool: at least one provider is required")
	}
	return &PoolManager{
		providers: providers,
		spec:      spec,
		tracked:   make(map[string]*instState),
	}, nil
}

// Size reports how many instances the pool currently tracks (idle + busy).
func (p *PoolManager) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.tracked)
}

// Busy reports how many tracked instances are currently leased out.
func (p *PoolManager) Busy() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, s := range p.tracked {
		if s.busy {
			n++
		}
	}
	return n
}

// Idle reports how many tracked instances are free to lease.
func (p *PoolManager) Idle() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.idleCountLocked()
}

func (p *PoolManager) idleCountLocked() int {
	n := 0
	for _, s := range p.tracked {
		if !s.busy {
			n++
		}
	}
	return n
}

// Utilization returns busy/total in [0,1]. An empty pool is 0.
func (p *PoolManager) Utilization() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := len(p.tracked)
	if total == 0 {
		return 0
	}
	busy := 0
	for _, s := range p.tracked {
		if s.busy {
			busy++
		}
	}
	return float64(busy) / float64(total)
}

// HourlyTCO sums the per-hour price of every tracked instance, idle or busy.
// This is the pool's running $/hr cost.
func (p *PoolManager) HourlyTCO() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	var total float64
	for _, s := range p.tracked {
		total += s.inst.HourlyUSD
	}
	return total
}

// ScaleTo drives the pool toward exactly target instances. If the pool has
// fewer, it provisions the difference (scale up). If it has more, it releases
// idle instances down toward target (scale down), and never releases a busy
// (leased) one. It returns the pool size after scaling.
//
// Scale-down can leave the pool above target if too many instances are busy:
// the manager will not evict a live lease. Callers can re-invoke once leases
// are returned.
//
// Scale-up is best-effort, not all-or-nothing. If a Provision call fails
// mid-loop, all already-provisioned instances for this call are retained in
// the pool and the partial size is returned alongside the error. Callers must
// not assume the pool reaches target on a non-nil return.
func (p *PoolManager) ScaleTo(ctx context.Context, target int) (int, error) {
	if target < 0 {
		return 0, fmt.Errorf("pool: negative target %d", target)
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	current := len(p.tracked)
	switch {
	case current < target:
		if err := p.scaleUpLocked(ctx, target-current); err != nil {
			return len(p.tracked), err
		}
	case current > target:
		if err := p.scaleDownLocked(ctx, current-target); err != nil {
			return len(p.tracked), err
		}
	}
	return len(p.tracked), nil
}

// scaleUpLocked provisions n instances, distributing across providers and
// respecting each provider's Capacity. Caller holds p.mu.
func (p *PoolManager) scaleUpLocked(ctx context.Context, n int) error {
	for i := 0; i < n; i++ {
		prov, err := p.pickProviderLocked()
		if err != nil {
			return err
		}
		inst, err := prov.Provision(ctx, p.spec)
		if err != nil {
			return fmt.Errorf("pool: provision: %w", err)
		}
		if _, dup := p.tracked[inst.ID]; dup {
			return fmt.Errorf("pool: provider returned duplicate instance ID %q", inst.ID)
		}
		p.tracked[inst.ID] = &instState{inst: inst, provider: prov, busy: false}
	}
	return nil
}

// pickProviderLocked returns the first provider with spare capacity relative to
// how many of its instances the pool already tracks. Caller holds p.mu.
func (p *PoolManager) pickProviderLocked() (GPUProvider, error) {
	used := make(map[GPUProvider]int, len(p.providers))
	for _, s := range p.tracked {
		used[s.provider]++
	}
	for _, prov := range p.providers {
		if used[prov] < prov.Capacity() {
			return prov, nil
		}
	}
	return nil, errors.New("pool: all providers at capacity")
}

// scaleDownLocked releases up to n idle instances. It never touches a busy
// instance. Releases happen in a deterministic order (by ID) so behaviour is
// reproducible. Caller holds p.mu.
func (p *PoolManager) scaleDownLocked(ctx context.Context, n int) error {
	idleIDs := make([]string, 0, len(p.tracked))
	for id, s := range p.tracked {
		if !s.busy {
			idleIDs = append(idleIDs, id)
		}
	}
	sort.Strings(idleIDs)

	released := 0
	for _, id := range idleIDs {
		if released >= n {
			break
		}
		s := p.tracked[id]
		if err := s.provider.Release(ctx, s.inst); err != nil {
			return fmt.Errorf("pool: release %q: %w", id, err)
		}
		delete(p.tracked, id)
		released++
	}
	return nil
}

// Acquire leases one idle instance, marking it busy, and returns it. It
// checks ctx.Err() before attempting the acquire so a cancelled or timed-out
// context is respected immediately; Acquire does not block waiting for an idle
// instance to become available. If no idle instance is available it returns
// ErrPoolEmpty (the caller may ScaleTo a larger target first). The returned
// Instance must be handed back via ReturnToPool when the caller is done.
func (p *PoolManager) Acquire(ctx context.Context) (Instance, error) {
	// Honour context cancellation/timeout before touching pool state so callers
	// that time out during concurrent scale-ups observe a clean abort.
	if err := ctx.Err(); err != nil {
		return Instance{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	ids := make([]string, 0, len(p.tracked))
	for id, s := range p.tracked {
		if !s.busy {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return Instance{}, ErrPoolEmpty
	}
	// Sort for deterministic selection (always picks the lexicographically
	// smallest idle ID). This is intentionally optimised for correctness and
	// reproducibility rather than throughput: for the documented small-pool
	// scale (tens of instances) the O(n log n) cost is negligible. A
	// production pool with hundreds of instances could replace this with a
	// single O(n) min-scan without allocation.
	sort.Strings(ids)
	s := p.tracked[ids[0]]
	s.busy = true
	return s.inst, nil
}

// ReturnToPool marks a previously-acquired instance idle again so it can be
// re-leased or scaled down. Returning an instance the manager does not track
// yields ErrUnknownInstance.
func (p *PoolManager) ReturnToPool(inst Instance) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.tracked[inst.ID]
	if !ok {
		return ErrUnknownInstance
	}
	s.busy = false
	return nil
}
