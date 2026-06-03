// Package device — probe.go
//
// Probe returns a DeviceDescriptor populated with REAL host values obtained
// from the operating system. It is the hardware-probe implementation that was
// explicitly out-of-scope for StaticProber; Probe fills that role.
//
// Field sourcing:
//
//	ID            — os.Hostname() with a fixed suffix so it is always non-empty.
//	Arch          — runtime.GOARCH mapped to the Arch const.
//	CPUCores      — runtime.NumCPU() (logical CPUs seen by the scheduler).
//	MemoryBytes   — per-OS native call via platformTotalMemoryBytes()
//	                (darwin: sysctl hw.memsize; linux: syscall.Sysinfo; other:
//	                 runtime.MemStats.Sys as an honest lower-bound).
//	GPUCount      — real per-OS GPU probe via platformProbeGPU()
//	GPUVRAMBytes    (darwin: system_profiler SPDisplaysDataType -json + sysctl
//	                 hw.memsize for the Apple-Silicon unified-memory pool; linux/
//	                 other: honest 0 until a native probe lands). A probe failure
//	                 yields honest 0, never a fabricated value.
//	NPUTops       — 0; NPU enumeration is delegated to a specialised subsystem.
//	PowerBudgetWatts — 0 for the same reason.
//	HasBattery    — false (conservative default; battery detection needs IOKit/
//	                ACPI and belongs in a separate pkg/battery).
//	Trust         — TrustBasic: the device is known (it is the local host) but
//	                has no cryptographic attestation yet.
package device

import (
	"context"
	"fmt"
	"os"
	"runtime"
)

// goarchToArch maps a GOARCH string to the typed Arch constant used throughout
// this package. Any unrecognised architecture falls back to ArchUnknown, which
// is valid and causes ClassifyTier/EffectiveCompute to apply no arch bonus.
func goarchToArch(goarch string) Arch {
	switch goarch {
	case "amd64":
		return ArchAMD64
	case "arm64":
		return ArchARM64
	case "riscv64":
		return ArchRISCV64
	default:
		return ArchUnknown
	}
}

// Probe returns a DeviceDescriptor populated with real values measured from the
// local host at call time. The ctx argument is reserved for future use (e.g.
// timeout for slow NVML / IOKit calls) and is not currently consumed; callers
// should still pass a non-nil context so that future implementations can honour
// cancellation without a breaking change.
//
// Errors are returned only for genuine failures (e.g. os.Hostname() failing,
// which is extremely rare). A zero MemoryBytes is treated as an honest
// lower-bound fallback rather than an error.
func Probe(ctx context.Context) (DeviceDescriptor, error) {
	// --- ID ---
	hostname, err := os.Hostname()
	if err != nil {
		return DeviceDescriptor{}, fmt.Errorf("device.Probe: hostname lookup failed: %w", err)
	}
	if hostname == "" {
		return DeviceDescriptor{}, fmt.Errorf("device.Probe: os.Hostname returned empty string")
	}
	// Append a short qualifier to distinguish the host identity from a raw
	// network name and to ensure uniqueness within a cluster namespace.
	id := hostname + ".local"

	// --- Arch ---
	arch := goarchToArch(runtime.GOARCH)

	// --- CPUCores ---
	cores := runtime.NumCPU()

	// --- MemoryBytes ---
	// platformTotalMemoryBytes is defined per OS in probe_{darwin,linux,other}.go.
	memBytes := platformTotalMemoryBytes()

	// --- GPU (count + VRAM) ---
	// platformProbeGPU is defined per OS in probe_gpu_{darwin,linux,other}.go.
	// It returns REAL, OS-native GPU data: on darwin via
	// `system_profiler SPDisplaysDataType -json` (+ sysctl hw.memsize for the
	// Apple-Silicon unified-memory pool). A probe error is non-fatal — a host
	// with no detectable GPU honestly reports zero, never a fabricated value
	// (§CLAUDE-1 rule 5, §CLAUDE-2 rule 1).
	gpuCount, gpuVRAM := platformProbeGPU()

	return DeviceDescriptor{
		ID:          id,
		Arch:        arch,
		CPUCores:    cores,
		MemoryBytes: memBytes,
		// GPU count + VRAM are probed per-OS via platformProbeGPU (real system
		// facility per OS). NPU / power-budget / battery remain delegated to
		// specialised subsystems and are honest zeros until probed.
		GPUCount:         gpuCount,
		GPUVRAMBytes:     gpuVRAM,
		NPUTops:          0,
		PowerBudgetWatts: 0,
		HasBattery:       false,
		// TrustBasic: the device is the local host (known) but has no
		// cryptographic attestation evidence yet.
		Trust: TrustBasic,
	}, nil
}
