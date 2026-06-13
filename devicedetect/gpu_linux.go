//go:build linux

package devicedetect

import (
	"context"
	"os/exec"
	"strings"
)

// defaultSysfsRoot is the live sysfs DRM root probed on a real Linux host.
const defaultSysfsRoot = "/sys/class/drm"

// detectGPUs is the real Linux implementation. On a live Linux host it walks
// /sys/class/drm/card*/device/{vendor,device}, maps PCI vendor IDs to vendor
// names, and (best-effort) enriches the discrete-NVIDIA model name via
// nvidia-smi when that binary is present.
//
// The parsing lives in the platform-neutral sysfs.go (detectGPUsFromSysfs) so
// it is unit-testable against an injected fixture sysfs tree on ANY OS,
// including the macOS dev host (see sysfs_test.go).
func detectGPUs(ctx context.Context) ([]GPU, error) {
	gpus, err := detectGPUsFromSysfs(defaultSysfsRoot)
	if err != nil {
		return nil, err
	}
	enrichLinuxModels(ctx, gpus)
	return gpus, nil
}

// enrichLinuxModels upgrades NVIDIA model names using nvidia-smi when present,
// on a live host only. This is exercised on Linux CI; the macOS host test
// proves the sysfs parser via the fixture instead.
func enrichLinuxModels(ctx context.Context, gpus []GPU) {
	hasNVIDIA := false
	for i := range gpus {
		if gpus[i].Vendor == "NVIDIA" {
			hasNVIDIA = true
			break
		}
	}
	if !hasNVIDIA {
		return
	}
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return
	}
	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=name,memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	idx := 0
	for i := range gpus {
		if gpus[i].Vendor != "NVIDIA" || idx >= len(lines) {
			continue
		}
		fields := strings.Split(lines[idx], ",")
		if len(fields) >= 1 {
			if name := strings.TrimSpace(fields[0]); name != "" {
				gpus[i].Model = "NVIDIA " + strings.TrimPrefix(name, "NVIDIA ")
			}
		}
		if len(fields) >= 2 {
			if mb := atoiSafeLinux(strings.TrimSpace(fields[1])); mb > 0 {
				gpus[i].MemoryBytes = int64(mb) * (1 << 20)
			}
		}
		idx++
	}
}

func atoiSafeLinux(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}
