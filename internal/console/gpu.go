package console

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// GPUInfo holds basic GPU compute information.
type GPUInfo struct {
	Vendor      string
	Name        string
	MemoryBytes int64
	ComputeOK   bool
}

// GPUQuerier probes GPU compute capabilities.
type GPUQuerier struct {
	vulkanLoaderPath string
}

// NewGPUQuerier creates a new GPU querier.
func NewGPUQuerier() *GPUQuerier {
	return &GPUQuerier{
		vulkanLoaderPath: "/usr/lib/libvulkan.so",
	}
}

// Query returns GPU information.
// On non-Linux platforms it returns a NoOp GPUInfo with ComputeOK=false.
func (gq *GPUQuerier) Query() (*GPUInfo, error) {
	if runtime.GOOS != "linux" {
		return &GPUInfo{Vendor: "noop", Name: "noop", ComputeOK: false}, nil
	}
	return gq.queryLinux()
}

func (gq *GPUQuerier) queryLinux() (*GPUInfo, error) {
	info := &GPUInfo{ComputeOK: false}

	// Try to detect via /sys/class/drm for AMD GPUs (common on consoles).
	if err := gq.probeDRM(info); err == nil && info.Name != "" {
		info.ComputeOK = true
		return info, nil
	}

	// Fallback: check for Vulkan loader presence as a heuristic.
	if _, err := os.Stat(gq.vulkanLoaderPath); err == nil {
		info.Vendor = "unknown"
		info.Name = "vulkan-capable"
		info.ComputeOK = true
	}

	if info.Name == "" {
		return nil, fmt.Errorf("no GPU detected")
	}
	return info, nil
}

func (gq *GPUQuerier) probeDRM(info *GPUInfo) error {
	drmPath := "/sys/class/drm"
	entries, err := os.ReadDir(drmPath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "card") {
			continue
		}
		devicePath := filepath.Join(drmPath, entry.Name(), "device")
		vendorBytes, err := os.ReadFile(filepath.Join(devicePath, "vendor"))
		if err != nil {
			continue
		}
		vendor := strings.TrimSpace(string(vendorBytes))
		info.Vendor = vendor

		// Try to read device name from uevent or modalias.
		ueventBytes, _ := os.ReadFile(filepath.Join(devicePath, "uevent"))
		for _, line := range strings.Split(string(ueventBytes), "\n") {
			if strings.HasPrefix(line, "DRIVER=") {
				info.Name = strings.TrimPrefix(line, "DRIVER=")
			}
		}

		// Try to read VRAM total from mem_info_vram_total (AMD-specific sysfs).
		vramBytes, err := os.ReadFile(filepath.Join(devicePath, "mem_info_vram_total"))
		if err == nil {
			vramStr := strings.TrimSpace(string(vramBytes))
			if v, err := strconv.ParseInt(vramStr, 10, 64); err == nil {
				info.MemoryBytes = v
			}
		}

		// If we found a name, stop at first card.
		if info.Name != "" {
			break
		}
	}

	if info.Vendor == "" {
		return fmt.Errorf("no DRM device found")
	}
	return nil
}

// HasVulkanCompute returns true if Vulkan compute is likely available.
func (gq *GPUQuerier) HasVulkanCompute() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := os.Stat(gq.vulkanLoaderPath)
	return err == nil
}
