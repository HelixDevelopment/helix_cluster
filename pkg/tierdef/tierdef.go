// Package tierdef provides the authoritative T1-T15 tier-definition registry
// for the Helix Cluster OS. Each tier specifies the minimum hardware capabilities
// a device must meet to be classified at that tier.
//
// Compared with pkg/deviceprofile (T1-T8, flat capability registry) and
// pkg/device (Tier classification logic), tierdef owns the full 15-rung ladder
// from T1 (tiny embedded) to T15 (extreme multi-GPU server) and the Meets
// predicate that decides whether a device satisfies a tier's requirements.
package tierdef

// TierDef describes the minimum hardware requirements for a single tier.
// All min_* fields are inclusive lower bounds (>=). A zero MinGPU means no GPU
// is required; in that case MinGPUVRAMMB is ignored by Meets.
type TierDef struct {
	Tier           string  `yaml:"tier"`
	MinCPUCores    int     `yaml:"min_cpu_cores"`
	MinMemoryMB    int     `yaml:"min_memory_mb"`
	MinGPU         int     `yaml:"min_gpu"`
	MinGPUVRAMMB   int     `yaml:"min_gpu_vram_mb"`
	MinNetworkMbps int     `yaml:"min_network_mbps"`
	MaxPowerW      float64 `yaml:"max_power_w"`
}

// DeviceCaps describes the measured capabilities of a physical device. All
// fields are in the same units as the corresponding TierDef min_* fields.
type DeviceCaps struct {
	CPUCores    int
	MemoryMB    int
	GPUCount    int
	GPUVRAMMB   int
	NetworkMbps int
	PowerBudgetW float64
}

// Meets reports whether the device described by c satisfies this tier's minimum
// requirements. The check is:
//
//	CPUCores    >= MinCPUCores
//	MemoryMB    >= MinMemoryMB
//	GPUCount    >= MinGPU
//	GPUVRAMMB   >= MinGPUVRAMMB   (only when MinGPU > 0)
//	NetworkMbps >= MinNetworkMbps
//
// All boundaries are inclusive (>=). MaxPowerW is a budget ceiling defined for
// informational scheduling use and is NOT enforced by Meets.
func (t TierDef) Meets(c DeviceCaps) bool {
	if c.CPUCores < t.MinCPUCores {
		return false
	}
	if c.MemoryMB < t.MinMemoryMB {
		return false
	}
	if c.GPUCount < t.MinGPU {
		return false
	}
	if t.MinGPU > 0 && c.GPUVRAMMB < t.MinGPUVRAMMB {
		return false
	}
	if c.NetworkMbps < t.MinNetworkMbps {
		return false
	}
	return true
}

// Registry is an ordered, validated collection of TierDefs (T1..T15).
// It is constructed exclusively via LoadTiers or Default — there is no public
// constructor. The internal slice preserves document order (T1 first, T15 last).
type Registry struct {
	SchemaVersion int
	Tiers         []TierDef
	index         map[string]int // tier name -> position in Tiers
}

// Get returns the TierDef for the named tier (e.g. "T8") and whether it was
// found. The look-up is O(1).
func (r *Registry) Get(tier string) (TierDef, bool) {
	i, ok := r.index[tier]
	if !ok {
		return TierDef{}, false
	}
	return r.Tiers[i], true
}
