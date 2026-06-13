// Package hwinventory implements a unified host hardware-inventory engine for
// the Helix Cluster OS device-discovery layer. It aggregates the existing
// per-OS GPU, NPU and FPGA detectors (devicedetect.DetectGPUs /
// npudetect.DetectNPUs / fpgadetect.DetectFPGAs) together with real CPU and
// total-memory probes into a single, JSON-renderable host capability report.
//
// A runnable CLI is provided at ./cmd/hwinfo: `go run ./cmd/hwinfo` prints the
// real host capability report (CPU, memory, GPUs, NPUs, FPGAs) as JSON.
//
// Per the Helix CLAUDE-2 Cross-Platform Parity Guarantee, the CPU and memory
// probes are implemented per-OS behind build tags with a REAL implementation
// for each supported platform — there are no mock/stub paths:
//
//   - darwin: cpu_darwin.go — sysctl hw.ncpu / hw.physicalcpu /
//     machdep.cpu.brand_string and hw.memsize.
//   - linux:  cpu_linux.go  — /proc/cpuinfo and /proc/meminfo.
//
// The GPU, NPU and FPGA portions are delegated to the proven sibling modules,
// which themselves provide real per-OS detection.
package hwinventory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/HelixDevelopment/helix_cluster/devicedetect"
	"github.com/HelixDevelopment/helix_cluster/fpgadetect"
	"github.com/HelixDevelopment/helix_cluster/npudetect"
)

// CPUInfo describes the host CPU as measured by the per-OS probe.
type CPUInfo struct {
	// Model is the human-readable CPU brand string, e.g. "Apple M3 Pro" or
	// "Intel(R) Xeon(R) ...". Never empty when Collect succeeds.
	Model string `json:"model"`

	// LogicalCores is the number of logical CPUs (hardware threads) the OS
	// reports. Always >= 1 on a successful collect.
	LogicalCores int `json:"logical_cores"`

	// PhysicalCores is the number of physical cores when the OS exposes it,
	// otherwise it falls back to LogicalCores. Always >= 1.
	PhysicalCores int `json:"physical_cores"`

	// Architecture is the Go runtime architecture, e.g. "arm64", "amd64".
	Architecture string `json:"architecture"`

	// Source documents which backend produced the CPU figures, e.g.
	// "sysctl(hw.ncpu,machdep.cpu.brand_string)" or "/proc/cpuinfo".
	Source string `json:"source,omitempty"`
}

// Inventory is the unified host capability report: real CPU + total memory
// plus the detected GPUs and NPUs.
type Inventory struct {
	// Host is the OS hostname.
	Host string `json:"host"`

	// OS / Arch are the Go runtime GOOS / GOARCH of the running binary.
	OS   string `json:"os"`
	Arch string `json:"arch"`

	// CPU holds the real CPU model and core counts.
	CPU CPUInfo `json:"cpu"`

	// MemoryBytes is the total physical system memory in bytes. Always > 0 on
	// a successful collect (cross-checkable against the OS oracle).
	MemoryBytes int64 `json:"memory_bytes"`

	// MemorySource documents which backend produced MemoryBytes, e.g.
	// "sysctl(hw.memsize)" or "/proc/meminfo:MemTotal".
	MemorySource string `json:"memory_source,omitempty"`

	// GPUs are the detected graphics/compute processors (may be empty on a
	// host with no detectable GPU; never nil).
	GPUs []devicedetect.GPU `json:"gpus"`

	// NPUs are the detected neural accelerators (may be empty; never nil).
	NPUs []npudetect.NPU `json:"npus"`

	// FPGAs are the detected FPGAs / FPGA accelerator cards (may be empty on a
	// host with no detectable FPGA — as on this Apple-silicon host; never nil).
	FPGAs []fpgadetect.FPGA `json:"fpgas"`
}

// Collect probes the current host and assembles a full Inventory: real CPU
// info and total memory via the per-OS probes, plus the GPU and NPU detectors.
//
// It returns a non-nil error only on a genuine probe failure of a mandatory
// component (CPU or memory). GPU/NPU detection failures are surfaced as errors
// too, since on supported hosts these detectors return (empty, nil) rather than
// an error when no device is present.
func Collect(ctx context.Context) (Inventory, error) {
	inv := Inventory{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
		// Ensure non-nil slices so the JSON report always renders [] not null.
		GPUs:  []devicedetect.GPU{},
		NPUs:  []npudetect.NPU{},
		FPGAs: []fpgadetect.FPGA{},
	}

	host, err := os.Hostname()
	if err != nil {
		return Inventory{}, fmt.Errorf("hwinventory: resolve hostname: %w", err)
	}
	inv.Host = host

	cpu, err := detectCPU(ctx)
	if err != nil {
		return Inventory{}, fmt.Errorf("hwinventory: detect CPU: %w", err)
	}
	if cpu.LogicalCores < 1 {
		return Inventory{}, fmt.Errorf("hwinventory: CPU probe returned %d logical cores (want >= 1)", cpu.LogicalCores)
	}
	if cpu.Model == "" {
		return Inventory{}, fmt.Errorf("hwinventory: CPU probe returned empty model")
	}
	inv.CPU = cpu

	memBytes, memSource, err := detectMemoryBytes(ctx)
	if err != nil {
		return Inventory{}, fmt.Errorf("hwinventory: detect memory: %w", err)
	}
	if memBytes <= 0 {
		return Inventory{}, fmt.Errorf("hwinventory: memory probe returned %d bytes (want > 0)", memBytes)
	}
	inv.MemoryBytes = memBytes
	inv.MemorySource = memSource

	gpus, err := devicedetect.DetectGPUs(ctx)
	if err != nil {
		return Inventory{}, fmt.Errorf("hwinventory: detect GPUs: %w", err)
	}
	if gpus != nil {
		inv.GPUs = gpus
	}

	npus, err := npudetect.DetectNPUs(ctx)
	if err != nil {
		return Inventory{}, fmt.Errorf("hwinventory: detect NPUs: %w", err)
	}
	if npus != nil {
		inv.NPUs = npus
	}

	fpgas, err := fpgadetect.DetectFPGAs(ctx)
	if err != nil {
		return Inventory{}, fmt.Errorf("hwinventory: detect FPGAs: %w", err)
	}
	if fpgas != nil {
		inv.FPGAs = fpgas
	}

	return inv, nil
}

// JSON renders the inventory as indented JSON suitable for the host capability
// report / evidence log.
func (inv Inventory) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("hwinventory: marshal inventory: %w", err)
	}
	return b, nil
}
