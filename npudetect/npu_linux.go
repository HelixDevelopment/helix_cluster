//go:build linux

package npudetect

import (
	"context"
	"os/exec"
	"strings"
)

// newDetector returns the Linux Detector wired to the real system roots.
func newDetector() Detector {
	return &linuxDetector{
		roots: linuxRoots{
			deviceTree: "/proc/device-tree",
			sys:        "/sys",
			dev:        "/dev",
		},
	}
}

type linuxDetector struct {
	roots linuxRoots
}

func (d *linuxDetector) Detect(ctx context.Context) ([]NPU, error) {
	// Real device-tree / sysfs probing (shared, unit-tested logic).
	npus := scanLinuxRoots(d.roots)

	// Additionally probe nvidia-smi for a discrete-GPU DLA marker when present
	// (desktop/server NVIDIA parts). This augments the Jetson device-tree path.
	if !hasVendor(npus, "NVIDIA") {
		if n, ok := detectNvidiaSMI(ctx); ok {
			npus = append(npus, n)
		}
	}
	return npus, nil
}

func hasVendor(npus []NPU, vendor string) bool {
	for _, n := range npus {
		if n.Vendor == vendor {
			return true
		}
	}
	return false
}

// detectNvidiaSMI queries nvidia-smi (if installed) for a GPU whose name implies
// an on-board DLA (Orin/Xavier class). Absence of nvidia-smi is not an error.
func detectNvidiaSMI(ctx context.Context) (NPU, bool) {
	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=name", "--format=csv,noheader").Output()
	if err != nil {
		return NPU{}, false
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return NPU{}, false
	}
	lower := strings.ToLower(name)
	if strings.Contains(lower, "orin") || strings.Contains(lower, "xavier") || strings.Contains(lower, "tegra") {
		return NPU{
			Vendor:  "NVIDIA",
			Model:   "NVIDIA DLA",
			Runtime: "TensorRT/DLA",
			Source:  "nvidia-smi name=" + name,
		}, true
	}
	return NPU{}, false
}
