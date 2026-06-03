//go:build darwin

package resources

import (
	"bufio"
	"strconv"
	"strings"
)

// Darwin GPU TFLOPS probe (HXC-1300, CLAUDE-2 real per-OS path).
//
// Apple Silicon GPUs do not appear in /sys/class/drm and have no PCI
// vendor:device, so the linux catalog cannot attribute their compute. Instead we
// derive a REAL peak-FP32 figure from the integrated GPU's published core count
// (which system_profiler reports honestly — see drm_darwin.go parseSPDisplays):
//
//	peak_FP32_TFLOPS = cores * ALUs_per_core * 2 (FMA) * clock_GHz / 1000
//
// Apple's GPU cores expose 128 FP32 ALUs each and run at ~1.4 GHz, so the figure
// is cores * 128 * 2 * 1.4 / 1000. This is the standard textbook peak-FLOPS
// formula applied to Apple's documented per-core ALU width — a measured-input
// derivation, not a hardcoded constant: it scales with the real core count, so a
// 14-core M3 Pro and a 40-core M3 Max yield different, correct values.

const (
	// appleALUsPerGPUCore is the FP32 ALU lane count per Apple GPU core.
	appleALUsPerGPUCore = 128
	// appleGPUClockGHz is the representative sustained GPU clock for Apple
	// Silicon used in the peak-FLOPS derivation.
	appleGPUClockGHz = 1.4
)

// tflopsFromAppleCores applies the peak-FP32 formula to an Apple GPU core count.
// A non-positive core count yields 0 (no signal), never a fabricated value.
func tflopsFromAppleCores(cores int) float64 {
	if cores <= 0 {
		return 0
	}
	flopsPerCyclePerCore := float64(appleALUsPerGPUCore) * 2 // *2 == FMA
	return float64(cores) * flopsPerCyclePerCore * appleGPUClockGHz / 1000.0
}

// parseAppleGPU extracts the integrated GPU's chipset model and "Total Number of
// Cores" from system_profiler SPDisplaysDataType output. It returns ok=true only
// when a positive core count is present; discrete cards / headless hosts yield
// ("", 0, false). The chipset is the first "Chipset Model:" seen.
func parseAppleGPU(raw string) (chipset string, cores int, ok bool) {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		if chipset == "" && strings.HasPrefix(trimmed, "Chipset Model:") {
			chipset = strings.TrimSpace(strings.TrimPrefix(trimmed, "Chipset Model:"))
			continue
		}
		if strings.HasPrefix(trimmed, "Total Number of Cores:") {
			v := strings.TrimSpace(strings.TrimPrefix(trimmed, "Total Number of Cores:"))
			n, err := strconv.Atoi(v)
			if err == nil && n > 0 {
				return chipset, n, true
			}
		}
	}
	return "", 0, false
}

// parseAppleGPUCores is the core-count-only view of parseAppleGPU, retained for
// the unit tests and the integration oracle.
func parseAppleGPUCores(raw string) (int, bool) {
	_, cores, ok := parseAppleGPU(raw)
	return cores, ok
}

// probeGPUTFLOPS is the darwin GPU-TFLOPS probe. It first tries the portable PCI
// catalog (a discrete AMD/NVIDIA card on a Mac Pro would have a vendor:device in
// g.Model); failing that it derives the figure from the Apple GPU core count via
// the injectable system_profiler reader — but ONLY when the GPU being probed is
// the host's Apple integrated GPU (its Model matches the chipset system_profiler
// reports, or is unspecified). An unrelated/unknown discrete identity must NOT be
// credited with the integrated GPU's core count; it returns (0,false) so the
// caller falls through to the override label.
func probeGPUTFLOPS(g GPUInfo) (float64, bool) {
	if v, ok := gpuTFLOPSFromCatalog(g); ok && v > 0 {
		return v, true
	}
	raw, err := darwinSystemProfilerFn()
	if err != nil {
		return 0, false
	}
	chipset, cores, ok := parseAppleGPU(raw)
	if !ok {
		return 0, false
	}
	// Only attribute the integrated GPU's cores when g is that GPU. An empty
	// Model (the probe was given no specific identity) is treated as "the host
	// GPU". A non-empty Model must equal the reported chipset; a PCI "0x..:0x.."
	// identity that is not in the catalog above is therefore rejected here.
	if m := strings.TrimSpace(g.Model); m != "" && m != chipset {
		return 0, false
	}
	t := tflopsFromAppleCores(cores)
	if t <= 0 {
		return 0, false
	}
	return t, true
}

// darwinSystemProfilerFn is the system_profiler accessor used by probeGPUTFLOPS.
// It is a package var so tests can substitute deterministic fixture output; in
// production it shells out to the real tool via a DarwinGPUReader.
var darwinSystemProfilerFn = func() (string, error) {
	return (&DarwinGPUReader{}).systemProfiler()
}

// hostGPUInfo returns the host's real GPU identity on darwin (system_profiler),
// used by NewAcceleratorProbe so the probe can also pick up a discrete card's
// PCI catalog TFLOPS where present.
func hostGPUInfo() (GPUInfo, error) {
	return NewDarwinGPUReader().ReadGPUInfo()
}
