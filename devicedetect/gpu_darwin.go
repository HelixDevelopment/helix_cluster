//go:build darwin

package devicedetect

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// spDisplays mirrors the relevant subset of
// `system_profiler -json SPDisplaysDataType` output.
type spDisplays struct {
	SPDisplaysDataType []spDisplay `json:"SPDisplaysDataType"`
}

type spDisplay struct {
	Name string `json:"_name"`
	// sppci_model is the canonical chipset/model string, e.g. "Apple M3 Pro".
	Model string `json:"sppci_model"`
	// spdisplays_vendor is e.g. "sppci_vendor_Apple", "sppci_vendor_amd".
	Vendor string `json:"spdisplays_vendor"`
	// spdisplays_mtlgpufamilysupport e.g. "spdisplays_metal3".
	MetalFamily string `json:"spdisplays_mtlgpufamilysupport"`
	// sppci_cores e.g. "14".
	Cores string `json:"sppci_cores"`
	// sppci_bus e.g. "spdisplays_builtin" (integrated/unified) vs PCIe.
	Bus string `json:"sppci_bus"`
	// spdisplays_vram / _spdisplays_vram present on discrete-VRAM Macs,
	// e.g. "8 GB". Empty on Apple-Silicon unified-memory machines.
	VRAM  string `json:"spdisplays_vram"`
	VRAM2 string `json:"_spdisplays_vram"`
}

// detectGPUs is the real macOS implementation. It shells out to the system's
// system_profiler tool, parses the JSON, and returns the actual GPU(s) of this
// host. Unified-memory machines (Apple Silicon) report total system memory via
// sysctl hw.memsize as MemoryBytes.
func detectGPUs(ctx context.Context) ([]GPU, error) {
	out, err := runSystemProfiler(ctx)
	if err != nil {
		return nil, err
	}

	var parsed spDisplays
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("devicedetect: parse system_profiler JSON: %w", err)
	}

	unifiedMem := sysctlMemsize(ctx) // best-effort; 0 on failure.

	gpus := make([]GPU, 0, len(parsed.SPDisplaysDataType))
	for _, d := range parsed.SPDisplaysDataType {
		g := gpuFromDisplay(d, unifiedMem)
		if g.Model == "" || g.Vendor == "" {
			continue
		}
		gpus = append(gpus, g)
	}
	return gpus, nil
}

// gpuFromDisplay converts one parsed system_profiler entry into a GPU. It is
// kept separate from the exec so it is exercised by the JSON-fixture unit test
// in addition to the live host test.
func gpuFromDisplay(d spDisplay, unifiedMem int64) GPU {
	model := strings.TrimSpace(d.Model)
	if model == "" {
		model = strings.TrimSpace(d.Name)
	}

	g := GPU{
		Model:   model,
		Vendor:  normalizeDarwinVendor(d.Vendor, model),
		API:     metalAPI(d.MetalFamily),
		Cores:   atoiSafe(d.Cores),
		Source:  "system_profiler",
		Unified: isUnifiedBus(d.Bus),
	}

	// VRAM: prefer an explicitly reported discrete-VRAM figure; otherwise, for
	// unified-memory (built-in/Apple Silicon) GPUs report total system memory.
	if v := parseVRAM(d.VRAM, d.VRAM2); v > 0 {
		g.MemoryBytes = v
	} else if g.Unified {
		g.MemoryBytes = unifiedMem
	}

	return g
}

// normalizeDarwinVendor maps the spdisplays_vendor token (or, as a fallback,
// the model string) to a clean vendor name.
func normalizeDarwinVendor(raw, model string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	v = strings.TrimPrefix(v, "sppci_vendor_")
	switch {
	case strings.Contains(v, "apple"):
		return "Apple"
	case strings.Contains(v, "amd") || strings.Contains(v, "ati"):
		return "AMD"
	case strings.Contains(v, "nvidia"):
		return "NVIDIA"
	case strings.Contains(v, "intel"):
		return "Intel"
	}
	// Fallback: infer from the model string (older entries omit the vendor key).
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "apple"):
		return "Apple"
	case strings.Contains(m, "amd") || strings.Contains(m, "radeon"):
		return "AMD"
	case strings.Contains(m, "nvidia") || strings.Contains(m, "geforce"):
		return "NVIDIA"
	case strings.Contains(m, "intel"):
		return "Intel"
	}
	if v != "" {
		return strings.Title(v) //nolint:staticcheck // simple display fallback
	}
	return ""
}

// metalAPI returns "Metal" when the GPU advertises any Metal family support.
func metalAPI(family string) string {
	if strings.Contains(strings.ToLower(family), "metal") {
		return "Metal"
	}
	return ""
}

// isUnifiedBus reports whether the GPU is built-in (unified memory) rather than
// a discrete PCIe card.
func isUnifiedBus(bus string) bool {
	b := strings.ToLower(bus)
	return strings.Contains(b, "builtin") || strings.Contains(b, "built-in")
}

// parseVRAM parses a system_profiler VRAM string such as "8 GB" or "1536 MB"
// into bytes. Returns 0 when neither field is parseable.
func parseVRAM(primary, secondary string) int64 {
	for _, s := range []string{primary, secondary} {
		s = strings.TrimSpace(strings.ToUpper(s))
		if s == "" {
			continue
		}
		var unitMul int64
		switch {
		case strings.HasSuffix(s, "GB"):
			unitMul = 1 << 30
			s = strings.TrimSpace(strings.TrimSuffix(s, "GB"))
		case strings.HasSuffix(s, "MB"):
			unitMul = 1 << 20
			s = strings.TrimSpace(strings.TrimSuffix(s, "MB"))
		default:
			continue
		}
		if n, err := strconv.ParseFloat(s, 64); err == nil && n > 0 {
			return int64(n * float64(unitMul))
		}
	}
	return 0
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// runSystemProfiler executes the real macOS system_profiler tool. It is a
// package var so the fixture test can substitute a recorded payload without
// shelling out, while the live host test uses the real binary.
var runSystemProfiler = func(ctx context.Context) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "system_profiler", "-json", "SPDisplaysDataType").Output()
	if err != nil {
		return nil, fmt.Errorf("devicedetect: run system_profiler: %w", err)
	}
	return out, nil
}

// sysctlMemsize returns hw.memsize in bytes (total RAM, == unified GPU memory
// ceiling on Apple Silicon). Best-effort: returns 0 on any failure.
var sysctlMemsize = func(ctx context.Context) int64 {
	out, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
