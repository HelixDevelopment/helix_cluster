package device

import (
	"github.com/HelixDevelopment/helix_cluster/pkg/tierdef"
)

// mibBytes is 1 MiB in bytes, used for unit conversion from bytes to MB.
const mibBytes = int64(1) << 20

// networkUnknown is a sentinel value for DeviceCaps.NetworkMbps that indicates
// the source descriptor (DeviceDescriptor) has no network field. Setting it to
// math.MaxInt32 (≈2 Gbps) ensures the network gate in TierDef.Meets is never the
// sole reason a capable device is classified lower than its CPU/GPU capabilities
// warrant. The value is intentionally very large — no realistic tier will require
// more than 400,000 Mbps (400 Gbps) and even that ceiling is met by this sentinel.
const networkUnknown = int(^uint(0) >> 1) // math.MaxInt

// AssignTier maps a DeviceDescriptor to the highest Tier whose requirements are
// met by the device, using the provided tierdef.Registry as the authoritative
// source of tier definitions.
//
// The algorithm iterates from T15 (highest) down to T1 (lowest) and returns the
// first tier whose TierDef.Meets(caps) is true. This ensures a device is assigned
// the highest tier it qualifies for, not merely the lowest.
//
// Conversion rules from DeviceDescriptor to tierdef.DeviceCaps:
//
//	CPUCores     <- d.CPUCores                (direct)
//	MemoryMB     <- d.MemoryBytes / MiB       (floor division; 0 for negative)
//	GPUCount     <- d.GPUCount                (direct)
//	GPUVRAMMB    <- d.GPUVRAMBytes / MiB
//	NetworkMbps  <- networkUnknown (math.MaxInt) — DeviceDescriptor carries no
//	                network field; the sentinel value satisfies any tier's
//	                min_network_mbps gate so network is never the sole disqualifier.
//	PowerBudgetW <- d.PowerBudgetWatts        (direct)
//
// An empty/degenerate descriptor (CPUCores <= 0 AND MemoryBytes <= 0) returns
// TierUnknown, consistent with ClassifyTier. A descriptor with at least one
// non-zero capability that does not meet T1 minimum requirements still returns
// T1 (the registry guarantees T1 has the lowest thresholds).
//
// If the registry is nil or contains no tiers, TierUnknown is returned.
func AssignTier(d DeviceDescriptor, reg *tierdef.Registry) Tier {
	// Degenerate descriptor — mirrors ClassifyTier's TierUnknown gate.
	if d.CPUCores <= 0 && d.MemoryBytes <= 0 {
		return TierUnknown
	}

	if reg == nil || len(reg.Tiers) == 0 {
		return TierUnknown
	}

	caps := descriptorToCaps(d)

	// Iterate from HIGHEST tier index down to LOWEST (T15 → T1).
	// Return the first tier whose requirements are satisfied.
	for i := len(reg.Tiers) - 1; i >= 0; i-- {
		td := reg.Tiers[i]
		if td.Meets(caps) {
			return tierNameToConst(td.Tier)
		}
	}

	// No tier met — fall back to T1 for any device with partial capability.
	return T1
}

// descriptorToCaps converts a DeviceDescriptor to the tierdef.DeviceCaps
// representation used by TierDef.Meets. MemoryMB and GPUVRAMMB are floor-divided
// from bytes; negative byte values yield 0 (clamped).
func descriptorToCaps(d DeviceDescriptor) tierdef.DeviceCaps {
	memMB := 0
	if d.MemoryBytes > 0 {
		memMB = int(d.MemoryBytes / mibBytes)
	}
	gpuVRAMMB := 0
	if d.GPUVRAMBytes > 0 {
		gpuVRAMMB = int(d.GPUVRAMBytes / mibBytes)
	}
	return tierdef.DeviceCaps{
		CPUCores:     d.CPUCores,
		MemoryMB:     memMB,
		GPUCount:     d.GPUCount,
		GPUVRAMMB:    gpuVRAMMB,
		NetworkMbps:  networkUnknown, // DeviceDescriptor has no network field; use sentinel so network gate is never the sole disqualifier
		PowerBudgetW: d.PowerBudgetWatts,
	}
}

// tierNameToConst maps a tier name string (e.g. "T7") to the corresponding
// device.Tier constant. Returns TierUnknown for any unrecognised name.
// This is the single authoritative mapping between the string representation
// used in tierdef YAML and the numeric Tier constants used in pkg/device.
func tierNameToConst(name string) Tier {
	switch name {
	case "T1":
		return T1
	case "T2":
		return T2
	case "T3":
		return T3
	case "T4":
		return T4
	case "T5":
		return T5
	case "T6":
		return T6
	case "T7":
		return T7
	case "T8":
		return T8
	case "T9":
		return T9
	case "T10":
		return T10
	case "T11":
		return T11
	case "T12":
		return T12
	case "T13":
		return T13
	case "T14":
		return T14
	case "T15":
		return T15
	default:
		return TierUnknown
	}
}
