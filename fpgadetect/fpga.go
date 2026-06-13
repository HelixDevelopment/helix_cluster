// Package fpgadetect provides cross-platform discovery of FPGAs (field-
// programmable gate arrays) / FPGA accelerator cards behind a single interface.
//
// Per the Helix CLAUDE-2 Cross-Platform Parity Guarantee, every supported OS
// implements a REAL probe of its host hardware using that OS's proper system
// facility — there are no mock/stub implementations for this real-operation
// feature. Platform code is split by build tags behind the single Detector
// interface declared here:
//
//   - darwin: fpga_darwin.go (system_profiler SPPCIDataType / SPThunderboltDataType
//     / SPUSBDataType, matched against known FPGA-vendor PCI IDs and name strings)
//   - linux:  fpga_linux.go (walks /sys/bus/pci/devices/<dev>/vendor for Xilinx
//     0x10ee / Altera 0x1172 / Lattice 0x1204, plus Xilinx XRT and Intel OPAE
//     device nodes)
//
// The Linux PCI/sysfs parsing logic is factored into pciparse.go so it is
// unit-testable against an injectable fixture sysfs root on any OS (including
// the macOS dev host, where the real Linux paths do not exist).
package fpgadetect

import "context"

// FPGA describes a single detected FPGA device or accelerator card.
type FPGA struct {
	// Vendor is the silicon vendor, e.g. "Xilinx", "Intel/Altera", "Lattice".
	Vendor string
	// Model is a human-readable model/board name when known, e.g.
	// "Xilinx Alveo U250", or a best-effort "<vendor> device 0x<id>".
	Model string
	// Family is the FPGA family/platform when identifiable, e.g. "Alveo",
	// "Stratix 10", "ECP5". Empty when not mapped.
	Family string
	// Interface is the host attach interface: "PCIe", "Thunderbolt", or "USB"
	// (USB-JTAG programming cables / eval boards).
	Interface string
	// PCIVendorID is the PCI vendor ID in "0x10ee" form when the device exposes
	// one (PCIe/Thunderbolt-PCIe attach). Empty for pure USB-JTAG devices.
	PCIVendorID string
	// PCIDeviceID is the PCI device ID in "0x<id>" form when available.
	PCIDeviceID string
	// Source documents how this FPGA was detected, e.g.
	// "sysfs /sys/bus/pci/devices/0000:01:00.0 vendor=0x10ee" or
	// "system_profiler SPPCIDataType vendor-id=0x10ee".
	Source string
}

// Detector probes the host for FPGAs. Each supported OS provides a real
// implementation via build tags.
type Detector interface {
	Detect(ctx context.Context) ([]FPGA, error)
}

// DetectFPGAs returns the FPGAs present on the host. It dispatches to the
// per-OS Detector selected at build time. It never returns a fabricated
// device: on a host with no detectable FPGA it returns an empty slice and a
// nil error.
func DetectFPGAs(ctx context.Context) ([]FPGA, error) {
	return newDetector().Detect(ctx)
}

// FPGA vendor PCI IDs (normalised, lower-case, no "0x"). These are the
// authoritative match keys shared by the Linux sysfs parser and the macOS
// system_profiler parser.
const (
	pciVendorXilinx  = "10ee" // Xilinx / AMD (Alveo, Versal, UltraScale)
	pciVendorAltera  = "1172" // Intel/Altera (Stratix, Arria, Agilex)
	pciVendorLattice = "1204" // Lattice Semiconductor
)

// fpgaVendorName maps a normalised PCI vendor ID to a human vendor name, or ""
// if the ID is not a known FPGA vendor.
func fpgaVendorName(vendorID string) string {
	switch normalizeHexID(vendorID) {
	case pciVendorXilinx:
		return "Xilinx"
	case pciVendorAltera:
		return "Intel/Altera"
	case pciVendorLattice:
		return "Lattice"
	default:
		return ""
	}
}

// normalizeHexID lower-cases and strips a leading "0x" and surrounding
// whitespace from a PCI id token (sysfs file contents or system_profiler text).
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
