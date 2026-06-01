//go:build darwin

package gpu

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// spDisplaysReport is the top-level shape of `system_profiler -json
// SPDisplaysDataType`. Each element of SPDisplaysDataType describes one GPU.
type spDisplaysReport struct {
	Displays []spDisplay `json:"SPDisplaysDataType"`
}

// spDisplay captures the GPU-level fields we map onto a *GPU. Only the keys we
// actually consume are declared; system_profiler emits many more.
type spDisplay struct {
	Name        string `json:"_name"`
	MetalFamily string `json:"spdisplays_mtlgpufamilysupport"`
	Vendor      string `json:"spdisplays_vendor"`
	Cores       string `json:"sppci_cores"`
	Bus         string `json:"sppci_bus"`
	DeviceType  string `json:"sppci_device_type"`
}

// detectGPUsPlatform runs system_profiler against the real host and parses its
// JSON into GPU descriptors. It errors if system_profiler cannot be executed,
// so production never silently falls back to a fabricated inventory.
func detectGPUsPlatform() ([]*GPU, error) {
	out, err := exec.Command("system_profiler", "-json", "SPDisplaysDataType").Output()
	if err != nil {
		return nil, fmt.Errorf("system_profiler SPDisplaysDataType failed: %w", err)
	}
	return parseSystemProfilerDisplays(out, darwinUnifiedMemoryMB)
}

// parseSystemProfilerDisplays converts system_profiler JSON into GPUs. memMBFn
// supplies the unified-memory pool size in MB for Apple Silicon GPUs (which
// share system RAM); it is injected so unit tests can parse a fixture without
// depending on the host's actual RAM. A GPU on a host where memMBFn cannot
// determine the pool size keeps MemoryMB == 0 rather than fabricating a value.
func parseSystemProfilerDisplays(data []byte, memMBFn func() (int, error)) ([]*GPU, error) {
	var report spDisplaysReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("parse system_profiler JSON: %w", err)
	}

	// Resolve the unified-memory pool once. On Apple Silicon the GPU has no
	// dedicated VRAM: it draws from system RAM, so hw.memsize is the honest
	// upper bound for the shared pool. If we cannot read it, we report 0 (with
	// no fabricated number) and continue.
	unifiedMB, memErr := 0, error(nil)
	if memMBFn != nil {
		unifiedMB, memErr = memMBFn()
		if memErr != nil {
			unifiedMB = 0
		}
	}

	gpus := make([]*GPU, 0, len(report.Displays))
	for _, d := range report.Displays {
		name := strings.TrimSpace(d.Name)
		if name == "" {
			// A display entry with no name is not a usable GPU descriptor.
			continue
		}

		gpu := &GPU{
			ID:     fmt.Sprintf("gpu-%d", len(gpus)),
			UUID:   fmt.Sprintf("GPU-%s-%d", strings.ReplaceAll(name, " ", "-"), len(gpus)),
			Model:  name,
			Status: Available,
		}

		// Apple Silicon GPUs use unified memory; only attribute the shared pool
		// to them. A discrete/third-party GPU reporting its own VRAM is not
		// represented in this field set, so we leave MemoryMB at 0 rather than
		// guessing.
		if isAppleSiliconGPU(d) {
			gpu.MemoryMB = unifiedMB
		}

		gpus = append(gpus, gpu)
	}

	return gpus, nil
}

// isAppleSiliconGPU reports whether the display entry is an Apple integrated GPU
// that shares unified memory. It checks the vendor and the Apple-only core
// count field that system_profiler emits for integrated GPUs.
func isAppleSiliconGPU(d spDisplay) bool {
	if strings.Contains(d.Vendor, "Apple") {
		return true
	}
	if strings.HasPrefix(d.Name, "Apple ") {
		return true
	}
	// sppci_cores is reported for Apple integrated GPUs (e.g. "14").
	if _, err := strconv.Atoi(strings.TrimSpace(d.Cores)); err == nil && d.Cores != "" {
		return true
	}
	return false
}

// darwinUnifiedMemoryMB returns the unified-memory pool size in MB derived from
// `sysctl -n hw.memsize` (total physical RAM in bytes). On Apple Silicon the
// GPU shares this pool with the CPU, so it is the honest figure for the GPU's
// addressable memory.
func darwinUnifiedMemoryMB() (int, error) {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, fmt.Errorf("sysctl hw.memsize failed: %w", err)
	}
	bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse hw.memsize %q: %w", strings.TrimSpace(string(out)), err)
	}
	return int(bytes / (1024 * 1024)), nil
}
