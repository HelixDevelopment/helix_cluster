//go:build linux

package fpgadetect

import "context"

// newDetector returns the Linux Detector wired to the real system roots.
func newDetector() Detector {
	return &linuxDetector{
		roots: linuxRoots{
			sys: "/sys",
			dev: "/dev",
		},
	}
}

type linuxDetector struct {
	roots linuxRoots
}

// Detect performs the real Linux probe: it walks /sys/bus/pci/devices for known
// FPGA vendor IDs and inspects Xilinx XRT / Intel OPAE device nodes. The parsing
// is the shared, unit-tested scanLinuxRoots logic; here it runs against the real
// roots. A host with no FPGA yields an empty slice and nil error (never a
// fabricated device).
func (d *linuxDetector) Detect(_ context.Context) ([]FPGA, error) {
	return scanLinuxRoots(d.roots), nil
}
