// dtparse.go contains the Linux device-tree / sysfs parsing logic for NPU
// detection. It is build-tag-free so it can be unit-tested against injectable
// fixture filesystem roots on ANY OS (including the macOS CI host, where the
// real Linux paths do not exist). The real Linux Detector in npu_linux.go calls
// scanLinuxRoots with the real system roots.
package npudetect

import (
	"os"
	"path/filepath"
	"strings"
)

// linuxRoots bundles the filesystem roots a Linux NPU probe inspects. On a real
// host these are "/proc/device-tree", "/sys", and "/dev"; in tests they point
// at fixture trees.
type linuxRoots struct {
	deviceTree string // e.g. /proc/device-tree
	sys        string // e.g. /sys
	dev        string // e.g. /dev
}

// scanLinuxRoots inspects the given roots and returns every NPU it can identify.
// It is pure filesystem inspection with no syscalls beyond stat/readdir/read, so
// it is fully exercisable from a unit test with fixture directories.
func scanLinuxRoots(r linuxRoots) []NPU {
	var npus []NPU
	if n, ok := detectRockchipRKNN(r); ok {
		npus = append(npus, n)
	}
	if n, ok := detectNvidiaDLA(r); ok {
		npus = append(npus, n)
	}
	if n, ok := detectQualcommHexagon(r); ok {
		npus = append(npus, n)
	}
	return npus
}

// --- Rockchip RKNN -------------------------------------------------------

// detectRockchipRKNN detects a Rockchip RKNPU via any of:
//   - a devfreq node matching *npu* under <sys>/class/devfreq
//   - a device-tree node named rknpu (compatible "rockchip,rk*-rknpu")
//   - a /dev/rknpu character device
func detectRockchipRKNN(r linuxRoots) (NPU, bool) {
	found := false
	source := ""

	// 1. /sys/class/devfreq/*npu*
	if r.sys != "" {
		devfreq := filepath.Join(r.sys, "class", "devfreq")
		if entries, err := os.ReadDir(devfreq); err == nil {
			for _, e := range entries {
				if strings.Contains(strings.ToLower(e.Name()), "npu") {
					found = true
					source = "sysfs devfreq " + e.Name()
					break
				}
			}
		}
	}

	// 2. device-tree rknpu node + compatible string
	if r.deviceTree != "" {
		if node, compat := findDTNode(r.deviceTree, "rknpu"); node != "" {
			found = true
			s := "device-tree node " + filepath.Base(node)
			if compat != "" {
				s += " compatible=" + compat
			}
			if source == "" {
				source = s
			} else {
				source += "; " + s
			}
		} else if compat := findDTByCompatibleContains(r.deviceTree, "rknpu"); compat != "" {
			found = true
			cs := "device-tree compatible=" + compat
			if source == "" {
				source = cs
			} else {
				source += "; " + cs
			}
		}
	}

	// 3. /dev/rknpu
	if r.dev != "" {
		if _, err := os.Stat(filepath.Join(r.dev, "rknpu")); err == nil {
			found = true
			if source == "" {
				source = "/dev/rknpu present"
			} else {
				source += "; /dev/rknpu present"
			}
		}
	}

	if !found {
		return NPU{}, false
	}
	return NPU{
		Vendor:  "Rockchip",
		Model:   "Rockchip RKNN NPU",
		Runtime: "RKNN",
		Source:  source,
	}, true
}

// --- NVIDIA DLA ----------------------------------------------------------

// detectNvidiaDLA detects an NVIDIA Deep Learning Accelerator (Tegra/Jetson)
// via a device-tree nvdla / "nvidia,tegra*-nvdla" node, or an nvidia-smi marker
// captured into the fixture (real host probing of nvidia-smi is handled in
// npu_linux.go; here we cover the device-tree path which is the Jetson signal).
func detectNvidiaDLA(r linuxRoots) (NPU, bool) {
	if r.deviceTree == "" {
		return NPU{}, false
	}
	source := ""
	if node, compat := findDTNode(r.deviceTree, "nvdla"); node != "" {
		source = "device-tree node " + filepath.Base(node)
		if compat != "" {
			source += " compatible=" + compat
		}
	} else if compat := findDTByCompatibleContains(r.deviceTree, "nvdla"); compat != "" {
		source = "device-tree compatible=" + compat
	} else if compat := findDTByCompatibleContains(r.deviceTree, "nvidia,tegra"); compat != "" && strings.Contains(compat, "dla") {
		source = "device-tree compatible=" + compat
	}
	if source == "" {
		return NPU{}, false
	}
	return NPU{
		Vendor:  "NVIDIA",
		Model:   "NVIDIA DLA",
		Runtime: "TensorRT/DLA",
		Source:  source,
	}, true
}

// --- Qualcomm Hexagon ----------------------------------------------------

// detectQualcommHexagon detects a Qualcomm Hexagon DSP/NPU via a device-tree
// node compatible with "qcom,*-cdsp" / containing "hexagon" or "adsp".
func detectQualcommHexagon(r linuxRoots) (NPU, bool) {
	if r.deviceTree == "" {
		return NPU{}, false
	}
	source := ""
	if node, compat := findDTNode(r.deviceTree, "hexagon"); node != "" {
		source = "device-tree node " + filepath.Base(node)
		if compat != "" {
			source += " compatible=" + compat
		}
	} else if compat := findDTByCompatibleContains(r.deviceTree, "hexagon"); compat != "" {
		source = "device-tree compatible=" + compat
	} else if compat := findDTByCompatibleContains(r.deviceTree, "qcom"); compat != "" &&
		(strings.Contains(compat, "cdsp") || strings.Contains(compat, "npu")) {
		source = "device-tree compatible=" + compat
	}
	if source == "" {
		return NPU{}, false
	}
	return NPU{
		Vendor:  "Qualcomm",
		Model:   "Qualcomm Hexagon NPU",
		Runtime: "QNN/Hexagon",
		Source:  source,
	}, true
}

// --- device-tree helpers -------------------------------------------------

// findDTNode walks the device-tree root looking for a directory whose base name
// starts with nameContains (device-tree nodes are named like "rknpu@fdab0000").
// It returns the node path and, if present, the trimmed contents of that node's
// "compatible" property file.
func findDTNode(dtRoot, nameContains string) (nodePath, compatible string) {
	_ = filepath.WalkDir(dtRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		base := d.Name()
		// match "rknpu" or "rknpu@fdab0000"
		if base == nameContains || strings.HasPrefix(base, nameContains+"@") {
			nodePath = path
			compatible = readDTString(filepath.Join(path, "compatible"))
			return filepath.SkipAll
		}
		return nil
	})
	return nodePath, compatible
}

// findDTByCompatibleContains walks the device-tree root reading every
// "compatible" property and returns the first whose value contains sub.
func findDTByCompatibleContains(dtRoot, sub string) string {
	var found string
	_ = filepath.WalkDir(dtRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "compatible" {
			return nil
		}
		v := readDTString(path)
		if strings.Contains(v, sub) {
			found = v
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// readDTString reads a device-tree property file. Device-tree string properties
// are NUL-terminated (and may be a NUL-separated list); we normalise NULs to
// spaces and trim.
func readDTString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.ReplaceAll(string(b), "\x00", " ")
	return strings.TrimSpace(s)
}
