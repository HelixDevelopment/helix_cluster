// pciparse.go contains the Linux PCI / sysfs parsing logic for FPGA detection.
// It is build-tag-free so it can be unit-tested against injectable fixture
// filesystem roots on ANY OS (including the macOS CI host, where the real Linux
// paths do not exist). The real Linux Detector in fpga_linux.go calls
// scanLinuxRoots with the real system roots.
package fpgadetect

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// linuxRoots bundles the filesystem roots a Linux FPGA probe inspects. On a
// real host these are "/sys" and "/dev"; in tests they point at fixture trees.
type linuxRoots struct {
	sys string // e.g. /sys
	dev string // e.g. /dev
}

// familyByVendorAndDevice maps a vendor + (optional) device ID to a best-effort
// FPGA family/platform name. The PCI device-ID space is large and not fully
// enumerated here; a "" result simply means "family not mapped" and is honest
// (we never guess a wrong family). Keys are normalised lower-case hex.
var familyByVendorAndDevice = map[string]map[string]string{
	pciVendorXilinx: {
		// Alveo data-center accelerator management/user functions (common IDs).
		"5000": "Alveo",
		"5004": "Alveo",
		"5008": "Alveo",
		"500c": "Alveo",
		"6987": "Alveo",
		"6988": "Alveo",
		"903f": "Versal",
	},
	pciVendorAltera: {
		// Intel PAC / Stratix-class acceleration cards.
		"09c4": "PAC/Stratix",
		"09c5": "PAC/Stratix",
		"0b30": "Agilex",
	},
	pciVendorLattice: {
		// Lattice device IDs are sparsely documented; left unmapped by default.
	},
}

// scanLinuxRoots inspects the given roots and returns every FPGA it can
// identify. It is pure filesystem inspection with no syscalls beyond
// stat/readdir/read, so it is fully exercisable from a unit test with fixture
// directories.
func scanLinuxRoots(r linuxRoots) []FPGA {
	var fpgas []FPGA
	fpgas = append(fpgas, detectPCIFPGAs(r)...)
	fpgas = append(fpgas, detectXilinxXRT(r)...)
	fpgas = append(fpgas, detectIntelOPAE(r)...)
	return fpgas
}

// --- PCI vendor-ID scan --------------------------------------------------

// detectPCIFPGAs walks <sys>/bus/pci/devices/*/vendor and returns an FPGA for
// every device whose vendor ID is a known FPGA vendor (Xilinx / Altera /
// Lattice). This is the primary, generic detection path.
func detectPCIFPGAs(r linuxRoots) []FPGA {
	if r.sys == "" {
		return nil
	}
	devicesDir := filepath.Join(r.sys, "bus", "pci", "devices")
	entries, err := os.ReadDir(devicesDir)
	if err != nil {
		return nil
	}

	// Deterministic ordering by PCI address for stable output.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var fpgas []FPGA
	for _, name := range names {
		devDir := filepath.Join(devicesDir, name)
		vendorID := normalizeHexID(readSysfsFile(filepath.Join(devDir, "vendor")))
		vendor := fpgaVendorName(vendorID)
		if vendor == "" {
			continue // not an FPGA vendor
		}
		deviceID := normalizeHexID(readSysfsFile(filepath.Join(devDir, "device")))

		f := FPGA{
			Vendor:      vendor,
			Family:      familyFor(vendorID, deviceID),
			Interface:   "PCIe",
			PCIVendorID: "0x" + vendorID,
			Source:      "sysfs " + devDir + " vendor=0x" + vendorID,
		}
		if deviceID != "" {
			f.PCIDeviceID = "0x" + deviceID
			f.Source += " device=0x" + deviceID
		}
		f.Model = modelName(vendor, f.Family, deviceID)
		fpgas = append(fpgas, f)
	}
	return fpgas
}

// familyFor returns the mapped family for a vendor+device, or "" if unmapped.
func familyFor(vendorID, deviceID string) string {
	if deviceID == "" {
		return ""
	}
	if m, ok := familyByVendorAndDevice[vendorID]; ok {
		return m[deviceID]
	}
	return ""
}

// modelName builds a best-effort model string. When a family is known it is
// used; otherwise the device ID is retained so live enrichment can supply a
// marketing name later.
func modelName(vendor, family, deviceID string) string {
	if family != "" {
		return vendor + " " + family
	}
	if deviceID == "" {
		return vendor + " FPGA"
	}
	return vendor + " FPGA device 0x" + deviceID
}

// --- Xilinx XRT (Alveo / Versal runtime) ---------------------------------

// detectXilinxXRT detects a Xilinx Runtime (XRT) managed device via either a
// /dev/xclmgmt* management node or a <sys>/class/xrt entry. These appear on
// hosts with Alveo cards and the XRT stack installed. Detection here augments
// the generic PCI scan (the same physical card may surface in both); callers
// can dedup by PCI address if needed, but for discovery we report the XRT
// signal explicitly because it proves the runtime is present, not just the PCI
// function.
func detectXilinxXRT(r linuxRoots) []FPGA {
	var fpgas []FPGA

	// /dev/xclmgmt* management character devices.
	if r.dev != "" {
		if entries, err := os.ReadDir(r.dev); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "xclmgmt") {
					fpgas = append(fpgas, FPGA{
						Vendor:      "Xilinx",
						Model:       "Xilinx Alveo (XRT)",
						Family:      "Alveo",
						Interface:   "PCIe",
						PCIVendorID: "0x" + pciVendorXilinx,
						Source:      "Xilinx XRT /dev/" + e.Name(),
					})
				}
			}
		}
	}

	// /sys/class/xrt entries.
	if r.sys != "" {
		xrtClass := filepath.Join(r.sys, "class", "xrt")
		if entries, err := os.ReadDir(xrtClass); err == nil && len(entries) > 0 {
			fpgas = append(fpgas, FPGA{
				Vendor:      "Xilinx",
				Model:       "Xilinx Alveo (XRT)",
				Family:      "Alveo",
				Interface:   "PCIe",
				PCIVendorID: "0x" + pciVendorXilinx,
				Source:      "Xilinx XRT " + xrtClass + " (" + entries[0].Name() + ")",
			})
		}
	}
	return fpgas
}

// --- Intel OPAE (Open Programmable Acceleration Engine) -------------------

// detectIntelOPAE detects an Intel FPGA managed by the OPAE/FPGA framework via
// a /dev/intel-fpga* node or a <sys>/class/fpga* entry (the in-kernel FPGA
// manager/region/bridge classes).
func detectIntelOPAE(r linuxRoots) []FPGA {
	var fpgas []FPGA

	// /dev/intel-fpga* nodes (e.g. intel-fpga-port.0, intel-fpga-fme.0).
	if r.dev != "" {
		if entries, err := os.ReadDir(r.dev); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "intel-fpga") {
					fpgas = append(fpgas, FPGA{
						Vendor:      "Intel/Altera",
						Model:       "Intel FPGA (OPAE)",
						Interface:   "PCIe",
						PCIVendorID: "0x" + pciVendorAltera,
						Source:      "Intel OPAE /dev/" + e.Name(),
					})
					break // one record per host is sufficient for discovery
				}
			}
		}
	}

	// /sys/class/fpga* (fpga, fpga_manager, fpga_region) — kernel FPGA mgr.
	if r.sys != "" {
		classDir := filepath.Join(r.sys, "class")
		if entries, err := os.ReadDir(classDir); err == nil {
			for _, e := range entries {
				if !strings.HasPrefix(e.Name(), "fpga") {
					continue
				}
				sub := filepath.Join(classDir, e.Name())
				if subEntries, err := os.ReadDir(sub); err == nil && len(subEntries) > 0 {
					fpgas = append(fpgas, FPGA{
						Vendor:    "Intel/Altera",
						Model:     "Intel FPGA (OPAE)",
						Interface: "PCIe",
						Source:    "Intel OPAE " + sub + " (" + subEntries[0].Name() + ")",
					})
					break
				}
			}
		}
	}
	return fpgas
}

// --- helpers -------------------------------------------------------------

// readSysfsFile reads a sysfs file and trims surrounding whitespace. Missing
// files yield "" (the absence of a probe target is not an error).
func readSysfsFile(path string) string {
	b, err := os.ReadFile(path) //nolint:gosec // path built from a controlled root + fixed leaf names
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
