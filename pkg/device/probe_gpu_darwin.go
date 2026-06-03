//go:build darwin

package device

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
)

// spDisplaysReport is the top-level shape of
// `system_profiler -json SPDisplaysDataType`. Each element of
// SPDisplaysDataType describes one GPU as seen by the real macOS host.
type spDisplaysReport struct {
	Displays []spDisplayEntry `json:"SPDisplaysDataType"`
}

// spDisplayEntry captures the GPU-level fields we consume. Only the keys we use
// are declared; system_profiler emits many more (attached panels, etc.).
type spDisplayEntry struct {
	Name        string `json:"_name"`
	Model       string `json:"sppci_model"`
	MetalFamily string `json:"spdisplays_mtlgpufamilysupport"`
	Vendor      string `json:"spdisplays_vendor"`
	Cores       string `json:"sppci_cores"`
	Bus         string `json:"sppci_bus"`
	DeviceType  string `json:"sppci_device_type"`
	// VRAM fields used by discrete GPUs. Apple-Silicon integrated GPUs do not
	// emit these (they share unified memory), so they are parsed only when set.
	VRAM       string `json:"spdisplays_vram"`
	VRAMShared string `json:"spdisplays_vram_shared"`
}

// gpuProbeResult is the parsed, OS-independent outcome of a darwin GPU probe.
// It is exposed (lower-case, package-internal) so the darwin test can assert
// the parsed model/Metal-support against the live system_profiler oracle.
type gpuProbeResult struct {
	Count        int
	VRAMBytes    int64
	Model        string // model of the first GPU; "" if none detected
	MetalSupport bool   // true if any GPU reports a Metal family
}

// platformProbeGPU returns the real GPU count and total VRAM for this macOS host
// by shelling out to `system_profiler -json SPDisplaysDataType` and, for
// Apple-Silicon unified-memory GPUs, `sysctl -n hw.memsize`. A failure to run or
// parse system_profiler yields (0, 0) — an honest "no GPU detected" rather than
// a fabricated value (§CLAUDE-1 rule 5, §CLAUDE-2 rule 1).
func platformProbeGPU() (count int, vramBytes int64) {
	res, err := probeDarwinGPU(runSystemProfilerDisplays, darwinTotalMemoryBytes)
	if err != nil {
		return 0, 0
	}
	return res.Count, res.VRAMBytes
}

// runSystemProfilerDisplays executes the real system_profiler command. It is a
// package var so tests can substitute a captured-fixture oracle while the
// integration path still runs the genuine binary.
var runSystemProfilerDisplays = func() ([]byte, error) {
	return exec.Command("system_profiler", "-json", "SPDisplaysDataType").Output()
}

// darwinTotalMemoryBytes reads total physical RAM via `sysctl -n hw.memsize`.
// On Apple Silicon the GPU shares this pool (unified memory), so it is the
// honest figure for the integrated GPU's addressable VRAM. Returns 0 on failure
// so the probe reports honest 0 rather than a fabricated number.
func darwinTotalMemoryBytes() int64 {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	b, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || b <= 0 {
		return 0
	}
	return b
}

// probeDarwinGPU parses system_profiler JSON (supplied by spFn) into a
// gpuProbeResult. memFn supplies the unified-memory pool size in bytes for
// Apple-Silicon GPUs; it is injected so tests can run without depending on the
// host RAM. A GPU whose VRAM cannot be determined contributes 0 VRAM rather than
// a guessed value.
func probeDarwinGPU(spFn func() ([]byte, error), memFn func() int64) (gpuProbeResult, error) {
	data, err := spFn()
	if err != nil {
		return gpuProbeResult{}, err
	}

	var report spDisplaysReport
	if err := json.Unmarshal(data, &report); err != nil {
		return gpuProbeResult{}, err
	}

	var unified int64
	if memFn != nil {
		unified = memFn()
	}

	var res gpuProbeResult
	for _, d := range report.Displays {
		name := gpuName(d)
		if name == "" {
			// An entry with no GPU name/model is not a usable GPU descriptor.
			continue
		}
		res.Count++
		if res.Model == "" {
			res.Model = name
		}
		if strings.TrimSpace(d.MetalFamily) != "" {
			res.MetalSupport = true
		}

		// VRAM sourcing: discrete GPUs report spdisplays_vram(_shared); Apple
		// Silicon integrated GPUs share unified memory (hw.memsize).
		if vb := parseVRAMBytes(d.VRAM); vb > 0 {
			res.VRAMBytes += vb
		} else if vb := parseVRAMBytes(d.VRAMShared); vb > 0 {
			res.VRAMBytes += vb
		} else if isAppleSiliconEntry(d) {
			res.VRAMBytes += unified
		}
	}

	return res, nil
}

// gpuName returns the canonical GPU model string, preferring sppci_model (the
// hardware model) and falling back to _name. Either is the real device label.
func gpuName(d spDisplayEntry) string {
	if m := strings.TrimSpace(d.Model); m != "" {
		return m
	}
	return strings.TrimSpace(d.Name)
}

// isAppleSiliconEntry reports whether the entry is an Apple integrated GPU that
// shares unified memory (and therefore draws VRAM from hw.memsize).
func isAppleSiliconEntry(d spDisplayEntry) bool {
	if strings.Contains(d.Vendor, "Apple") {
		return true
	}
	if strings.HasPrefix(gpuName(d), "Apple ") {
		return true
	}
	if c := strings.TrimSpace(d.Cores); c != "" {
		if _, err := strconv.Atoi(c); err == nil {
			return true
		}
	}
	return false
}

// parseVRAMBytes converts a system_profiler VRAM string such as "8 GB",
// "4096 MB", or "16384" (MB implied) into bytes. Returns 0 if it cannot be
// parsed — never a fabricated value.
func parseVRAMBytes(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	fields := strings.Fields(s)
	numStr := fields[0]
	n, err := strconv.ParseFloat(numStr, 64)
	if err != nil || n <= 0 {
		return 0
	}
	unit := "mb"
	if len(fields) > 1 {
		unit = strings.ToLower(fields[1])
	}
	switch {
	case strings.HasPrefix(unit, "gb"):
		return int64(n * 1024 * 1024 * 1024)
	case strings.HasPrefix(unit, "mb"):
		return int64(n * 1024 * 1024)
	case strings.HasPrefix(unit, "kb"):
		return int64(n * 1024)
	default:
		// Bare number: system_profiler historically reports VRAM in MB.
		return int64(n * 1024 * 1024)
	}
}
