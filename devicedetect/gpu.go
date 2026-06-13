// Package devicedetect implements a cross-platform GPU discovery engine for the
// Helix Cluster OS device-discovery layer (registry item HXC-1305).
//
// A single interface, DetectGPUs, is implemented per-OS behind build tags with
// a REAL implementation for each supported platform (no stubs for this
// real-operation feature, per HelixConstitution CLAUDE-2):
//
//   - darwin: gpu_darwin.go — parses `system_profiler -json SPDisplaysDataType`
//     plus sysctl for unified-memory sizing. Returns the real Apple GPU.
//   - linux:  gpu_linux.go  — parses /sys/class/drm/card*/device/{vendor,device}
//     with PCI-ID vendor mapping (NVIDIA/AMD/Intel) and ARM Mali via device-tree,
//     optionally enriched by vulkaninfo/nvidia-smi when present.
package devicedetect

import "context"

// GPU describes a single detected graphics processor.
//
// Fields are populated on a best-effort basis: a detector MUST always set a
// non-empty Vendor and Model when it returns a GPU, but MemoryBytes may be 0
// when the platform does not expose a reliable VRAM/unified-memory figure.
type GPU struct {
	// Vendor is the human-readable GPU vendor, e.g. "Apple", "NVIDIA", "AMD",
	// "Intel", "ARM". Never empty for a returned GPU.
	Vendor string `json:"vendor"`

	// Model is the chipset/marketing model, e.g. "Apple M3 Pro",
	// "NVIDIA GeForce RTX 4090". Never empty for a returned GPU.
	Model string `json:"model"`

	// API is the primary graphics/compute API associated with this GPU on this
	// host, e.g. "Metal", "Vulkan", "CUDA", "ROCm". May be empty if unknown.
	API string `json:"api,omitempty"`

	// MemoryBytes is dedicated VRAM in bytes, or — for unified-memory
	// architectures such as Apple Silicon — the total system memory shared with
	// the GPU. 0 when the platform exposes no reliable figure.
	MemoryBytes int64 `json:"memory_bytes,omitempty"`

	// Unified is true when the GPU shares system memory (no dedicated VRAM),
	// such as Apple Silicon or an integrated Intel iGPU.
	Unified bool `json:"unified,omitempty"`

	// PCIVendorID / PCIDeviceID are the raw PCI identifiers when available
	// (Linux discrete/integrated GPUs). Empty on platforms that do not expose
	// PCI IDs (e.g. macOS).
	PCIVendorID string `json:"pci_vendor_id,omitempty"`
	PCIDeviceID string `json:"pci_device_id,omitempty"`

	// Cores is the reported GPU core count when the platform exposes it
	// (e.g. Apple Silicon), otherwise 0.
	Cores int `json:"cores,omitempty"`

	// Source identifies which detection backend produced this record, e.g.
	// "system_profiler", "sysfs", "nvidia-smi". Useful for evidence/audit.
	Source string `json:"source,omitempty"`
}

// DetectGPUs returns the GPUs present on the current host. The implementation
// is selected at build time by the OS-specific files (gpu_darwin.go /
// gpu_linux.go / gpu_unsupported.go).
//
// It returns a non-nil error on a genuine probe failure. On a host that simply
// has no detectable GPU it returns an empty slice and a nil error, allowing the
// caller to distinguish "no GPU" from "probe failed".
func DetectGPUs(ctx context.Context) ([]GPU, error) {
	return detectGPUs(ctx)
}

// pciVendorName maps a PCI vendor ID (lower-case hex, no "0x" prefix) to a
// human-readable vendor. Shared by the Linux sysfs path and kept in the
// platform-neutral file so it is unit-testable on any OS.
func pciVendorName(vendorID string) string {
	switch normalizeHexID(vendorID) {
	case "10de":
		return "NVIDIA"
	case "1002", "1022":
		return "AMD"
	case "8086":
		return "Intel"
	case "106b":
		return "Apple"
	case "13b5", "14ac":
		return "ARM"
	case "5143":
		return "Qualcomm"
	default:
		return ""
	}
}

// normalizeHexID lower-cases and strips a leading "0x" and surrounding
// whitespace/newlines from a sysfs PCI id token.
func normalizeHexID(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			continue
		case c >= 'A' && c <= 'F':
			out = append(out, c+('a'-'A'))
		default:
			out = append(out, c)
		}
	}
	res := string(out)
	if len(res) >= 2 && res[0] == '0' && res[1] == 'x' {
		res = res[2:]
	}
	return res
}
