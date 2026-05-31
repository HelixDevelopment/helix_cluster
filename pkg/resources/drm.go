package resources

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DRMClassDir is the conventional subpath, under the sysfs base, that holds the
// per-card DRM device directories on Linux (e.g. /sys/class/drm/card0).
const DRMClassDir = "class/drm"

// GPUDevice is a single GPU enumerated from the DRM sysfs tree. It captures the
// raw PCI identity (vendor and device IDs, exactly as the kernel exposes them in
// the "0x...." hex form) and the total VRAM in bytes parsed from
// mem_info_vram_total.
//
// VendorID/DeviceID are kept as the canonical lower-case "0x" hex strings rather
// than decoded integers so the caller can render them losslessly; VRAMBytes is
// the decimal byte count from mem_info_vram_total.
type GPUDevice struct {
	// Card is the DRM card directory name, e.g. "card0".
	Card string
	// VendorID is the PCI vendor id as the kernel reports it, e.g. "0x1002".
	VendorID string
	// DeviceID is the PCI device id as the kernel reports it, e.g. "0x73bf".
	DeviceID string
	// VRAMBytes is the total VRAM in bytes from mem_info_vram_total.
	VRAMBytes int64
}

// cardDirRe-free matcher: a DRM "card" device is a directory named "cardN" where
// N is a run of digits. Render-node directories ("renderD128") and the control
// nodes must NOT be counted as GPUs, otherwise the count is inflated.
func isCardDir(name string) bool {
	if !strings.HasPrefix(name, "card") {
		return false
	}
	suffix := name[len("card"):]
	if suffix == "" {
		return false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// readVendorHex reads a sysfs PCI id file (vendor / device) and validates that
// it is a well-formed "0x...." hex token. A garbled or empty value is an error
// (never silently coerced to "0x0"), so callers can decide to skip or fail.
func readVendorHex(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if !strings.HasPrefix(s, "0x") {
		return "", fmt.Errorf("malformed pci id %q: missing 0x prefix", s)
	}
	hex := s[len("0x"):]
	if hex == "" {
		return "", fmt.Errorf("malformed pci id %q: empty", s)
	}
	// The remainder must be valid hexadecimal; ParseUint enforces that without
	// us coercing bad bytes to zero.
	if _, err := strconv.ParseUint(hex, 16, 64); err != nil {
		return "", fmt.Errorf("malformed pci id %q: %w", s, err)
	}
	return s, nil
}

// readVRAMBytes parses mem_info_vram_total, a single decimal byte count. A
// missing file yields (0, false, nil): not every DRM device exposes VRAM (e.g.
// integrated GPUs), and that is not an error. A present-but-garbled value IS an
// error so we never report bogus capacity.
func readVRAMBytes(path string) (bytes int64, present bool, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, false, fmt.Errorf("mem_info_vram_total: empty")
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("mem_info_vram_total %q: %w", s, err)
	}
	if v < 0 {
		return 0, false, fmt.Errorf("mem_info_vram_total %q: negative", s)
	}
	return v, true, nil
}

// ParseDRMTree enumerates GPUs from a DRM sysfs tree rooted at base.
//
// base is the sysfs base path (e.g. "/sys"); cards are looked up under
// base/class/drm/cardN/device/. Making base a parameter is what lets tests build
// a fake tree under t.TempDir() and run this parser on ANY OS without real
// hardware.
//
// For each cardN directory it reads device/vendor and device/device (the PCI
// ids) and device/mem_info_vram_total (VRAM). A card whose vendor or device id
// file is missing or garbled is skipped (it is not a usable GPU entry) — but a
// card that HAS a vendor/device id and a present-but-garbled VRAM file is a hard
// error, so corrupt capacity is never silently reported as zero. An entirely
// absent class/drm directory yields zero GPUs and no error.
func ParseDRMTree(base string) ([]GPUDevice, error) {
	drmDir := filepath.Join(base, DRMClassDir)
	entries, err := os.ReadDir(drmDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // no DRM subsystem present: zero GPUs, not an error
		}
		return nil, fmt.Errorf("read drm dir %q: %w", drmDir, err)
	}

	// Sort for deterministic ordering independent of readdir order.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var gpus []GPUDevice
	for _, name := range names {
		if !isCardDir(name) {
			continue
		}
		deviceDir := filepath.Join(drmDir, name, "device")

		vendor, err := readVendorHex(filepath.Join(deviceDir, "vendor"))
		if err != nil {
			// Missing or garbled vendor: not a usable GPU entry. Skip it.
			continue
		}
		device, err := readVendorHex(filepath.Join(deviceDir, "device"))
		if err != nil {
			continue
		}
		vram, _, err := readVRAMBytes(filepath.Join(deviceDir, "mem_info_vram_total"))
		if err != nil {
			return nil, fmt.Errorf("card %s: %w", name, err)
		}

		gpus = append(gpus, GPUDevice{
			Card:      name,
			VendorID:  vendor,
			DeviceID:  device,
			VRAMBytes: vram,
		})
	}
	return gpus, nil
}

// GPUInfoFromDevices folds a slice of enumerated GPUDevices into the aggregated
// GPUInfo shape used across the package. Count is the number of GPUs; Memory is
// reported in MiB (matching GPUInfo.Memory's memory_mb JSON tag) and is the SUM
// of per-card VRAM, so ignoring mem_info_vram_total would visibly change it.
// Model is a compact identity string ("vendor:device", or "vendor:device x N"
// when all cards share one identity) so the heterogeneous-vs-homogeneous case is
// distinguishable.
func GPUInfoFromDevices(devs []GPUDevice) GPUInfo {
	info := GPUInfo{Count: len(devs)}
	if len(devs) == 0 {
		return info
	}

	var totalBytes int64
	sameIdentity := true
	first := devs[0].VendorID + ":" + devs[0].DeviceID
	for _, d := range devs {
		totalBytes += d.VRAMBytes
		if d.VendorID+":"+d.DeviceID != first {
			sameIdentity = false
		}
	}
	info.Memory = totalBytes / (1024 * 1024) // bytes -> MiB

	if sameIdentity {
		if len(devs) == 1 {
			info.Model = first
		} else {
			info.Model = fmt.Sprintf("%s x %d", first, len(devs))
		}
	} else {
		parts := make([]string, 0, len(devs))
		for _, d := range devs {
			parts = append(parts, d.VendorID+":"+d.DeviceID)
		}
		info.Model = strings.Join(parts, ",")
	}
	return info
}

// ReadGPUInfo is the portable entry point: it parses the DRM tree under base and
// returns the aggregated GPUInfo. It is OS-independent (pure filesystem walk),
// so the same code path is exercised by tests on darwin against a t.TempDir()
// fixture and in production on Linux against "/sys".
func ReadGPUInfo(base string) (GPUInfo, error) {
	devs, err := ParseDRMTree(base)
	if err != nil {
		return GPUInfo{}, err
	}
	return GPUInfoFromDevices(devs), nil
}
