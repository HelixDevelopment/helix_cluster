package devicedetect

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// detectGPUsFromSysfs parses a Linux-style sysfs DRM tree rooted at root,
// walking card*/device/{vendor,device} (and of_node/compatible for SoC GPUs)
// and mapping PCI vendor IDs to vendor names.
//
// It lives in a platform-neutral file (no build tag) so the parser can be
// exercised against a fixture tree on any OS — proving the Linux detection
// logic on the macOS dev host even though the live /sys probe only runs on
// Linux.
func detectGPUsFromSysfs(root string) ([]GPU, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			// No DRM subsystem (e.g. headless without GPU): not an error.
			return []GPU{}, nil
		}
		return nil, fmt.Errorf("devicedetect: read %s: %w", root, err)
	}

	// Collect plain "cardN" entries (skip "cardN-HDMI..." connector nodes).
	cards := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "card") {
			continue
		}
		if strings.Contains(name, "-") {
			continue // connector, not a card device
		}
		cards = append(cards, name)
	}
	sort.Strings(cards)

	gpus := make([]GPU, 0, len(cards))
	for _, card := range cards {
		devDir := filepath.Join(root, card, "device")
		g, ok := gpuFromSysfsDevice(devDir)
		if !ok {
			continue
		}
		gpus = append(gpus, g)
	}
	return gpus, nil
}

// gpuFromSysfsDevice reads the {vendor,device} (and optional of_node) of a
// single DRM device directory and builds a GPU. ok is false when the directory
// has no usable identity.
func gpuFromSysfsDevice(devDir string) (GPU, bool) {
	vendorID := normalizeHexID(readSysfsFile(filepath.Join(devDir, "vendor")))
	deviceID := normalizeHexID(readSysfsFile(filepath.Join(devDir, "device")))

	// ARM Mali and other SoC GPUs have no PCI vendor/device; they expose a
	// device-tree compatible string instead.
	if vendorID == "" {
		if g, ok := gpuFromDeviceTree(devDir); ok {
			return g, true
		}
		return GPU{}, false
	}

	vendor := pciVendorName(vendorID)
	if vendor == "" {
		vendor = "0x" + vendorID
	}

	g := GPU{
		Vendor:      vendor,
		Model:       pciModelName(vendor, deviceID),
		API:         defaultAPIForVendor(vendor),
		PCIVendorID: "0x" + vendorID,
		Source:      "sysfs",
	}
	if deviceID != "" {
		g.PCIDeviceID = "0x" + deviceID
	}
	return g, true
}

// gpuFromDeviceTree handles SoC GPUs (e.g. ARM Mali) that expose a
// device-tree "compatible" string under of_node rather than PCI IDs.
func gpuFromDeviceTree(devDir string) (GPU, bool) {
	compat := readSysfsFile(filepath.Join(devDir, "of_node", "compatible"))
	if compat == "" {
		return GPU{}, false
	}
	c := strings.ToLower(compat)
	switch {
	case strings.Contains(c, "mali"):
		return GPU{Vendor: "ARM", Model: maliModel(compat), API: "Vulkan", Source: "device-tree"}, true
	case strings.Contains(c, "adreno") || strings.Contains(c, "qcom"):
		return GPU{Vendor: "Qualcomm", Model: "Adreno", API: "Vulkan", Source: "device-tree"}, true
	}
	return GPU{}, false
}

// maliModel extracts a readable Mali model from a DT compatible string such as
// "arm,mali-g78\x00arm,mali-midgard".
func maliModel(compat string) string {
	parts := strings.FieldsFunc(compat, func(r rune) bool { return r == 0 || r == ',' || r == '\n' })
	for _, p := range parts {
		if strings.Contains(strings.ToLower(p), "mali") {
			return strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return "Mali"
}

// defaultAPIForVendor picks the most representative API per vendor for Linux.
func defaultAPIForVendor(vendor string) string {
	switch vendor {
	case "NVIDIA":
		return "CUDA"
	case "AMD":
		return "ROCm"
	case "Intel":
		return "Vulkan"
	case "ARM", "Qualcomm":
		return "Vulkan"
	}
	return "Vulkan"
}

// pciModelName produces a best-effort model string from vendor + device ID.
// A full PCI-ID database lookup is out of scope here; the device ID is retained
// so the live nvidia-smi/vulkaninfo enrichment can supply the marketing name.
func pciModelName(vendor, deviceID string) string {
	if deviceID == "" {
		return vendor + " GPU"
	}
	return fmt.Sprintf("%s device 0x%s", vendor, deviceID)
}

func readSysfsFile(path string) string {
	b, err := os.ReadFile(path) //nolint:gosec // path built from a controlled root + fixed leaf names
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
