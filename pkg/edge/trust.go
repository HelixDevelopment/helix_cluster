// Package edge provides edge-device admission control for Helix Cluster OS.
// It covers trust-level classification, workload-restriction enforcement,
// work-unit resource limiting, and declarative per-tier schedule rules.
//
// The package reuses [github.com/HelixDevelopment/helix_cluster/pkg/device]
// (DeviceDescriptor, Tier, TrustClass, ClassifyTier) as classification input.
// All logic is pure-Go and hermetic; no real hardware probing is performed here.
package edge

import (
	"fmt"
	"strings"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/device"
)

// TrustLevel is the edge-admission trust category assigned to a device at
// registration. It gates which workloads and schedule policies a device may
// receive. Numerically higher values carry MORE trust.
type TrustLevel int

const (
	// TrustLevelUnknown is the zero/uninitialised value. A device with this
	// level MUST NOT be admitted to any workload.
	TrustLevelUnknown TrustLevel = iota

	// EDGE_DONOR is the lowest admission tier: consumer/personal devices
	// (smartphones, tablets) that donate spare capacity. They may only run
	// non-sensitive, easily-evictable workloads (AI inference, small-batch,
	// data processing).
	EDGE_DONOR

	// SEMI is a semi-trusted device: embedded consoles, Android-based
	// embedded systems, or any battery device whose trust class is Basic or
	// below. It may not handle sensitive data, persistent storage, or network
	// relay.
	SEMI

	// STANDARD is a trusted edge node (SBC/Armbian-class, embedded Linux on
	// known hardware). Suitable for most workloads except those explicitly
	// requiring FULL.
	STANDARD

	// FULL is a fully-attested server-grade device. No workload restrictions
	// apply; it may handle sensitive data, persistent storage, and all other
	// workload types.
	FULL
)

// String returns the canonical string label for a TrustLevel.
func (tl TrustLevel) String() string {
	switch tl {
	case FULL:
		return "FULL"
	case STANDARD:
		return "STANDARD"
	case SEMI:
		return "SEMI"
	case EDGE_DONOR:
		return "EDGE_DONOR"
	default:
		return "UNKNOWN"
	}
}

// ParseTrustLevel parses the canonical label produced by String() back to a
// TrustLevel. It returns (TrustLevelUnknown, error) on an unrecognised label.
func ParseTrustLevel(s string) (TrustLevel, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "FULL":
		return FULL, nil
	case "STANDARD":
		return STANDARD, nil
	case "SEMI":
		return SEMI, nil
	case "EDGE_DONOR":
		return EDGE_DONOR, nil
	default:
		return TrustLevelUnknown, fmt.Errorf("edge: unknown TrustLevel %q", s)
	}
}

// IsValid reports whether tl is a known, non-zero TrustLevel.
func (tl TrustLevel) IsValid() bool {
	return tl >= EDGE_DONOR && tl <= FULL
}

// TrustBinding is an immutable record that captures the identity-to-trust
// assignment made at device registration. It is the audit artefact required
// by CLAUDE-1 sink-side evidence.
type TrustBinding struct {
	// DeviceID is the stable identifier from DeviceDescriptor.ID.
	DeviceID string
	// Level is the trust level assigned.
	Level TrustLevel
	// Tier is the device.Tier computed during classification (for audit).
	Tier device.Tier
	// TrustClass is the device.TrustClass from the descriptor.
	TrustClass device.TrustClass
	// AssignedAt is the wall-clock time of assignment.
	AssignedAt time.Time
	// Rationale explains which classification rule fired.
	Rationale string
}

// AssignTrustLevel classifies d and returns the TrustLevel assigned at
// registration together with a TrustBinding audit record.
//
// Mapping rules (evaluated top-to-bottom; first match wins):
//
//  1. device.TrustConfidential  → FULL  (confidential-compute attestation)
//  2. device.TrustAttested      → FULL  (verified secure-boot)
//  3. iOS-class (HasBattery, ARM64, no GPU, >0 NPU)  → EDGE_DONOR
//  4. Android-class (HasBattery, ARM64, no real GPU OR low VRAM ≤ 512 MiB)
//     → SEMI
//  5. T3/T4 SBC with TrustBasic or higher  → STANDARD
//  6. T5+ with TrustBasic or higher        → STANDARD
//  7. Battery device not matched above     → EDGE_DONOR
//  8. Everything else                      → SEMI
func AssignTrustLevel(d device.DeviceDescriptor) (TrustLevel, TrustBinding) {
	tier := device.ClassifyTier(d)
	now := time.Now().UTC()

	assign := func(level TrustLevel, rationale string) (TrustLevel, TrustBinding) {
		return level, TrustBinding{
			DeviceID:   d.ID,
			Level:      level,
			Tier:       tier,
			TrustClass: d.Trust,
			AssignedAt: now,
			Rationale:  rationale,
		}
	}

	// Rule 1 & 2: hardware attestation → FULL regardless of device form-factor.
	if d.Trust >= device.TrustAttested {
		return assign(FULL, "device.TrustClass >= TrustAttested → FULL")
	}

	// Rule 3: iOS-class heuristic.
	// iPhone/iPad: battery-powered, ARM64, no discrete GPU, has an NPU.
	if d.HasBattery && d.Arch == device.ArchARM64 && d.GPUCount == 0 && d.NPUTops > 0 {
		return assign(EDGE_DONOR, "iOS-class (battery+ARM64+NPU, no GPU) → EDGE_DONOR")
	}

	// Rule 4: Android-class heuristic.
	// Battery-powered ARM64 with no GPU or only a very small integrated GPU
	// (≤512 MiB VRAM) qualifies as Android-class embedded/console.
	const androidMaxVRAMBytes = int64(512) << 20 // 512 MiB
	if d.HasBattery && d.Arch == device.ArchARM64 &&
		(d.GPUCount == 0 || d.GPUVRAMBytes <= androidMaxVRAMBytes) {
		return assign(SEMI, "Android-class (battery+ARM64, low/no GPU) → SEMI")
	}

	// Rule 5 & 6: SBC/edge or higher with at least Basic trust → STANDARD.
	if tier >= device.T3 && d.Trust >= device.TrustBasic {
		return assign(STANDARD, fmt.Sprintf("SBC/edge+ (tier=%s, trust=%d) → STANDARD", tier, d.Trust))
	}

	// Rule 7: any remaining battery device.
	if d.HasBattery {
		return assign(EDGE_DONOR, "battery device (unmatched rules) → EDGE_DONOR")
	}

	// Rule 8: default.
	return assign(SEMI, "default rule → SEMI")
}
