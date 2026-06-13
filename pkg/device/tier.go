package device

// Tier is a device's compute-class tier. Tiers form a documented 15-rung ladder
// from T1 (lowest: deeply-constrained microcontroller/handheld) to T15 (highest:
// extreme multi-GPU server-class). Higher tiers are numerically greater, so tiers
// are directly comparable with <, >, etc.
type Tier int

const (
	// TierUnknown is the zero value, used only for an invalid/empty descriptor.
	TierUnknown Tier = iota
	// T1 — micro: deeply constrained (e.g. MCU-class, <2 cores, <512MiB).
	T1
	// T2 — handheld: low-power battery SBC/phone-class.
	T2
	// T3 — SBC/edge: small board (Raspberry-Pi-class), no real accelerator.
	T3
	// T4 — workstation-lite: desktop-class CPU, modest RAM, maybe iGPU.
	T4
	// T5 — accelerated edge: has a real GPU or meaningful NPU.
	T5
	// T6 — workstation: many cores + discrete GPU with substantial VRAM.
	T6
	// T7 — server: high core count + capable GPU(s).
	T7
	// T8 — server-class accelerated: top compute (>=128 cores or multi-GPU farm).
	T8
	// T9 — server+: dual-socket capable, multi-GPU.
	T9
	// T10 — HPC entry: 64-core class + 4 GPUs.
	T10
	// T11 — HPC mid: high-bandwidth interconnect, 4 large GPUs.
	T11
	// T12 — HPC upper: 128 cores + 4 large GPUs.
	T12
	// T13 — AI server: 8-GPU node.
	T13
	// T14 — large AI server: 8 large-VRAM GPUs.
	T14
	// T15 — extreme multi-GPU server: flagship tier.
	T15
)

// String returns the canonical tier label (e.g. "T3"), or "T0" for TierUnknown.
func (t Tier) String() string {
	switch t {
	case T1:
		return "T1"
	case T2:
		return "T2"
	case T3:
		return "T3"
	case T4:
		return "T4"
	case T5:
		return "T5"
	case T6:
		return "T6"
	case T7:
		return "T7"
	case T8:
		return "T8"
	case T9:
		return "T9"
	case T10:
		return "T10"
	case T11:
		return "T11"
	case T12:
		return "T12"
	case T13:
		return "T13"
	case T14:
		return "T14"
	case T15:
		return "T15"
	default:
		return "T0"
	}
}

// Byte-size helpers for readable thresholds.
const (
	mib = int64(1) << 20
	gib = int64(1) << 30
)

// Documented classification thresholds. ClassifyTier evaluates these from the
// HIGHEST tier downward and returns the first tier whose gate the descriptor
// satisfies. Boundaries are inclusive on the lower edge (>=), so a descriptor
// exactly at a threshold lands in that tier and one unit below drops to the
// next-lower tier.
const (
	// T8: server-class accelerated.
	t8MinCores = 128
	t8MultiGPU = 4 // OR: 4+ GPUs regardless of core count
	// T7: server.
	t7MinCores   = 64
	t7MinGPUVRAM = 16 * gib
	// T6: workstation with discrete GPU.
	t6MinCores   = 16
	t6MinGPUVRAM = 8 * gib
	// T5: accelerated edge — any real accelerator above a floor.
	t5MinGPUVRAM = 2 * gib
	t5MinNPUTops = 10.0
	// T4: workstation-lite (non-accelerated desktop).
	t4MinCores  = 8
	t4MinMemory = 16 * gib
	// T3: SBC/edge.
	t3MinCores  = 4
	t3MinMemory = 2 * gib
	// T2: handheld (battery, modest).
	t2MinCores  = 2
	t2MinMemory = 512 * mib
)

// ClassifyTier deterministically maps a descriptor to a Tier using the
// documented thresholds above. It is a pure function: same input -> same tier,
// no I/O, no randomness. Gates are evaluated high-to-low and the first match
// wins, which makes each tier's gate the inclusive lower boundary of that tier.
//
// An empty/degenerate descriptor (no cores and no memory) is TierUnknown.
func ClassifyTier(d DeviceDescriptor) Tier {
	if d.CPUCores <= 0 && d.MemoryBytes <= 0 {
		return TierUnknown
	}

	// T8: 128+ cores, OR a multi-GPU farm (4+ GPUs).
	if d.CPUCores >= t8MinCores || d.GPUCount >= t8MultiGPU {
		return T8
	}
	// T7: 64+ cores with a capable GPU (>=16GiB VRAM).
	if d.CPUCores >= t7MinCores && d.GPUCount >= 1 && d.GPUVRAMBytes >= t7MinGPUVRAM {
		return T7
	}
	// T6: 16+ cores with a discrete GPU (>=8GiB VRAM).
	if d.CPUCores >= t6MinCores && d.GPUCount >= 1 && d.GPUVRAMBytes >= t6MinGPUVRAM {
		return T6
	}
	// T5: accelerated edge — a real GPU (>=2GiB VRAM) or a meaningful NPU.
	if (d.GPUCount >= 1 && d.GPUVRAMBytes >= t5MinGPUVRAM) || d.NPUTops >= t5MinNPUTops {
		return T5
	}
	// T4: workstation-lite — strong CPU + RAM, no qualifying accelerator.
	if d.CPUCores >= t4MinCores && d.MemoryBytes >= t4MinMemory {
		return T4
	}
	// T3: SBC/edge.
	if d.CPUCores >= t3MinCores && d.MemoryBytes >= t3MinMemory {
		return T3
	}
	// T2: handheld.
	if d.CPUCores >= t2MinCores && d.MemoryBytes >= t2MinMemory {
		return T2
	}
	// T1: everything else with at least some capability.
	return T1
}

// archWeight returns a small multiplicative bonus reflecting typical per-core
// throughput differences between architectures. It is deterministic and bounded;
// it never reorders devices of the same architecture.
func archWeight(a Arch) float64 {
	switch a {
	case ArchAMD64:
		return 1.10
	case ArchARM64:
		return 1.00
	case ArchRISCV64:
		return 0.90
	default:
		return 1.00
	}
}

// EffectiveCompute returns a unitless deterministic score summarising a device's
// usable compute. It is STRICTLY MONOTONIC: increasing CPUCores, GPUCount,
// GPUVRAMBytes, or NPUTops (all else equal) strictly increases the score. The
// formula weights CPU cores, GPU count + VRAM, and NPU TOPS, then applies the
// per-architecture weight.
//
// Weights (per unit):
//
//	CPU core   : 1.0
//	GPU device : 40.0  (a discrete GPU dominates a handful of CPU cores)
//	GPU VRAM   : 10.0 per GiB
//	NPU TOPS   : 0.5 per TOP
//
// The whole CPU+GPU+NPU sum is scaled by archWeight(Arch).
func EffectiveCompute(d DeviceDescriptor) float64 {
	cores := float64(maxInt(d.CPUCores, 0))
	gpus := float64(maxInt(d.GPUCount, 0))
	vramGiB := float64(maxInt64(d.GPUVRAMBytes, 0)) / float64(gib)
	npu := maxFloat(d.NPUTops, 0)

	base := cores*1.0 + gpus*40.0 + vramGiB*10.0 + npu*0.5
	return base * archWeight(d.Arch)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
