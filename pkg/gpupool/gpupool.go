// Package gpupool implements a GPU-provider-tier pool abstraction.
//
// It is intentionally self-contained and distinct from the node-level
// pkg/pool: this package models the GPU *provider* tiers (Local, RemoteProxy,
// Cloud, Decentralized) and the candidate Device set that backs them. It owns
// its own types and does not re-wrap any other in-repo package.
//
// The package covers three cohesive registry items:
//
//	HXC-1494  PoolManager.Allocate — tier-ordered provider walk; a higher
//	          (better) tier with zero free capacity is skipped so a healthy
//	          lower tier serves the workload.
//	HXC-1496  Filter — candidate filtering by WorkloadSpec (required labels
//	          such as TEE, and minimum VRAM); under-spec devices are excluded.
//	HXC-1498  PriorityScheduler.Select — deterministic selection ordered by
//	          tier, then utilization (least loaded), then cost.
//
// All logic is pure Go and deterministic: no clocks, no network, no host or
// GPU access. Callers inject providers, devices and specs.
package gpupool

import (
	"errors"
	"math"
	"sort"
)

// GPUTier is the provider tier. Lower numeric values are preferred (closer /
// cheaper / lower latency). The ordering of Allocate and Select walks tiers in
// ascending GPUTier order.
type GPUTier int

const (
	// TierLocal is on-host / on-cluster GPU capacity (most preferred).
	TierLocal GPUTier = 1
	// TierRemoteProxy is GPU capacity reached via a remote proxy.
	TierRemoteProxy GPUTier = 2
	// TierCloud is hyperscaler cloud GPU capacity.
	TierCloud GPUTier = 3
	// TierDecentralized is decentralized / marketplace GPU capacity (least preferred).
	TierDecentralized GPUTier = 4
)

// String renders the tier for logging.
func (t GPUTier) String() string {
	switch t {
	case TierLocal:
		return "Local"
	case TierRemoteProxy:
		return "RemoteProxy"
	case TierCloud:
		return "Cloud"
	case TierDecentralized:
		return "Decentralized"
	default:
		return "Unknown"
	}
}

// Provider is a GPU capacity provider that belongs to a tier. Capacity and
// FreeCapacity are expressed in whole GPU units (number of GPUs the provider
// can serve / currently has free).
type Provider struct {
	ID           string
	Tier         GPUTier
	Capacity     int
	FreeCapacity int
	// Healthy gates a provider entirely: an unhealthy provider is never
	// allocated regardless of reported free capacity.
	Healthy bool
}

// Device is an individual GPU candidate that the scheduler chooses among.
type Device struct {
	ID          string
	Tier        GPUTier
	VRAMGiB     int
	Labels      map[string]bool
	Utilization float64 // [0,1]; lower is less loaded.
	CostPerHour float64
}

// WorkloadSpec describes what a workload requires.
type WorkloadSpec struct {
	// MinVRAMGiB is the minimum per-device VRAM required.
	MinVRAMGiB int
	// RequireLabels are labels every candidate device must carry (e.g. "TEE").
	RequireLabels []string
	// GPUCount is the number of GPUs the workload needs. A provider must have
	// at least this much FreeCapacity to serve it. Defaults to 1 when <= 0.
	GPUCount int
}

// effectiveGPUCount normalizes GPUCount to a minimum of 1.
func (s WorkloadSpec) effectiveGPUCount() int {
	if s.GPUCount <= 0 {
		return 1
	}
	return s.GPUCount
}

// Errors returned by the package.
var (
	// ErrNoProvider is returned by Allocate when no provider in any tier has
	// sufficient free capacity for the spec.
	ErrNoProvider = errors.New("gpupool: no provider with sufficient free capacity")
	// ErrNoDevice is returned by Select when there are no candidate devices.
	ErrNoDevice = errors.New("gpupool: no candidate device to select")
)

// PoolManager owns the set of providers and performs tier-ordered allocation.
type PoolManager struct {
	providers []Provider
}

// NewPoolManager builds a PoolManager over a copy of the given providers.
func NewPoolManager(providers []Provider) *PoolManager {
	cp := make([]Provider, len(providers))
	copy(cp, providers)
	return &PoolManager{providers: cp}
}

// Allocate walks the providers in ascending tier order and returns the first
// HEALTHY provider whose FreeCapacity is at least the spec's GPU count. A
// better-tier provider that reports zero (or insufficient) free capacity is
// skipped, allowing a healthy lower tier to serve. (HXC-1494)
//
// Within a tier, providers are considered in descending FreeCapacity order so
// the most-available provider of the winning tier is chosen; this is a stable,
// deterministic tie-break and does not affect cross-tier ordering.
func (m *PoolManager) Allocate(spec WorkloadSpec) (Provider, error) {
	need := spec.effectiveGPUCount()

	// Stable, deterministic ordering: tier ascending (primary), then
	// FreeCapacity descending, then ID ascending.
	ordered := make([]Provider, len(m.providers))
	copy(ordered, m.providers)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		if a.FreeCapacity != b.FreeCapacity {
			return a.FreeCapacity > b.FreeCapacity
		}
		return a.ID < b.ID
	})

	for _, p := range ordered {
		if !p.Healthy {
			continue
		}
		if p.FreeCapacity >= need {
			return p, nil
		}
		// Insufficient (including zero) free capacity: skip this tier's
		// provider and continue the walk into lower tiers.
	}
	return Provider{}, ErrNoProvider
}

// Filter returns the subset of devices that satisfy the spec: each device must
// carry every required label and meet the minimum VRAM. The input slice is not
// mutated; ordering of the survivors is preserved. (HXC-1496)
func Filter(spec WorkloadSpec, devices []Device) []Device {
	out := make([]Device, 0, len(devices))
	for _, d := range devices {
		if d.VRAMGiB < spec.MinVRAMGiB {
			continue
		}
		if !hasAllLabels(d, spec.RequireLabels) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// hasAllLabels reports whether the device carries every required label.
func hasAllLabels(d Device, required []string) bool {
	for _, l := range required {
		if !d.Labels[l] {
			return false
		}
	}
	return true
}

// PriorityScheduler selects a single device from a candidate set using a fixed
// priority order: tier (ascending, best first), then utilization (ascending,
// least loaded first), then cost (ascending, cheapest first). ID is the final
// deterministic tie-break. (HXC-1498)
type PriorityScheduler struct{}

// NewPriorityScheduler returns a PriorityScheduler.
func NewPriorityScheduler() *PriorityScheduler {
	return &PriorityScheduler{}
}

// floatLess is a total ordering over float64 that agrees with < for all
// non-NaN values and deterministically sorts NaN LAST (a NaN is considered
// greater than every number and equal to another NaN). Without this, a NaN
// Utilization or CostPerHour would make deviceLess violate the strict-weak-
// ordering contract sort requires, yielding an order-dependent (non-
// deterministic) Select winner. For finite inputs floatLess(a,b) == (a < b)
// and floatEqual(a,b) == (a == b), so this is behavior-neutral for every
// valid input the package documents.
func floatLess(a, b float64) bool {
	aNaN, bNaN := math.IsNaN(a), math.IsNaN(b)
	if aNaN || bNaN {
		// NaN sorts last: a<b iff a is a number and b is NaN.
		return !aNaN && bNaN
	}
	return a < b
}

// floatEqual reports value equality under the floatLess total order: finite
// values compare with ==, and two NaNs are treated as equal (so the next
// tie-break field is consulted rather than producing an inconsistent order).
func floatEqual(a, b float64) bool {
	if math.IsNaN(a) && math.IsNaN(b) {
		return true
	}
	return a == b
}

// Less reports whether candidate a outranks candidate b under the
// tier > utilization > cost ordering. Exported indirectly via Select; kept as
// an unexported helper used by the sort.
func deviceLess(a, b Device) bool {
	if a.Tier != b.Tier {
		return a.Tier < b.Tier
	}
	if !floatEqual(a.Utilization, b.Utilization) {
		return floatLess(a.Utilization, b.Utilization)
	}
	if !floatEqual(a.CostPerHour, b.CostPerHour) {
		return floatLess(a.CostPerHour, b.CostPerHour)
	}
	return a.ID < b.ID
}

// Select returns the highest-priority device from candidates. The ordering is
// tier first, then least utilization, then lowest cost. Returns ErrNoDevice if
// the candidate set is empty. (HXC-1498)
func (s *PriorityScheduler) Select(candidates []Device) (Device, error) {
	if len(candidates) == 0 {
		return Device{}, ErrNoDevice
	}
	ordered := make([]Device, len(candidates))
	copy(ordered, candidates)
	sort.SliceStable(ordered, func(i, j int) bool {
		return deviceLess(ordered[i], ordered[j])
	})
	return ordered[0], nil
}
