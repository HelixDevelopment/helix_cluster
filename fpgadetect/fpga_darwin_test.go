//go:build darwin

package fpgadetect

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// fpgaVendorIDs are the PCI vendor IDs we treat as FPGA vendors; the oracle
// scans system_profiler text for any of these (in 0x-prefixed and bare form)
// or the vendor name strings.
var fpgaVendorIDs = []string{"0x10ee", "0x1172", "0x1204"}
var fpgaVendorNames = []string{"xilinx", "altera", "lattice"}

// oracleHasFPGA independently asks system_profiler across the PCI, Thunderbolt
// and USB domains whether ANY known-FPGA-vendor device is present. This is the
// anti-bluff cross-check: the detector MUST agree with this oracle. If the
// detector fabricates an FPGA on a host where the oracle sees none, the test
// below FAILS.
func oracleHasFPGA(t *testing.T) (present bool, evidence string) {
	t.Helper()
	var all strings.Builder
	for _, dt := range []string{"SPPCIDataType", "SPThunderboltDataType", "SPUSBDataType"} {
		out, err := exec.Command("system_profiler", dt).Output()
		if err != nil {
			// A domain may be unavailable; record and continue.
			t.Logf("oracle: system_profiler %s unavailable: %v", dt, err)
			continue
		}
		all.Write(out)
		all.WriteByte('\n')
	}
	text := strings.ToLower(all.String())
	for _, id := range fpgaVendorIDs {
		if strings.Contains(text, id) {
			return true, "vendor-id " + id
		}
	}
	for _, name := range fpgaVendorNames {
		if strings.Contains(text, name) {
			return true, "vendor-name " + name
		}
	}
	return false, "no FPGA-vendor (0x10ee/0x1172/0x1204 or xilinx/altera/lattice) device in SPPCIDataType/SPThunderboltDataType/SPUSBDataType"
}

// TestDetectFPGAs_HostMatchesOracle is the REAL host closure test. It runs the
// live detector and the independent system_profiler oracle, and asserts they
// agree. On this Apple-silicon host the oracle shows NO FPGA, so the detector
// MUST return an empty slice with nil error — proven, not assumed.
func TestDetectFPGAs_HostMatchesOracle(t *testing.T) {
	oraclePresent, evidence := oracleHasFPGA(t)
	t.Logf("ORACLE (system_profiler SPPCIDataType/SPThunderboltDataType/SPUSBDataType): FPGA present=%v (%s)", oraclePresent, evidence)

	fpgas, err := DetectFPGAs(context.Background())
	if err != nil {
		t.Fatalf("DetectFPGAs error: %v", err)
	}
	t.Logf("DETECTED %d FPGA(s): %+v", len(fpgas), fpgas)

	// ANTI-BLUFF CROSS-CHECK: detector result must agree with the oracle.
	if oraclePresent && len(fpgas) == 0 {
		t.Errorf("oracle sees an FPGA (%s) but detector found none — false negative", evidence)
	}
	if !oraclePresent && len(fpgas) != 0 {
		t.Errorf("oracle sees NO FPGA (%s) but detector fabricated %d device(s): %+v — anti-bluff FAIL", evidence, len(fpgas), fpgas)
	}

	if !oraclePresent {
		if len(fpgas) != 0 {
			t.Fatalf("expected empty slice on no-FPGA host, got %+v", fpgas)
		}
		t.Logf("CROSS-CHECK OK: detector returned empty, matching the no-FPGA oracle")
	} else {
		// If a real FPGA is present, every record must carry a real vendor.
		for _, f := range fpgas {
			if f.Vendor == "" {
				t.Errorf("detected FPGA with empty Vendor: %+v", f)
			}
			t.Logf("DETECTED FPGA: Vendor=%q Model=%q Family=%q Interface=%q Source=%q", f.Vendor, f.Model, f.Family, f.Interface, f.Source)
		}
	}
}

// TestDetectFPGAs_IsLive proves the detector actually shells out to
// system_profiler (it is not a hardcoded constant): an injected runner that
// returns a synthetic Xilinx PCI device makes the detector report it, and a
// runner returning empty output makes it report nothing.
func TestDetectFPGAs_IsLive(t *testing.T) {
	// Synthetic SPPCIDataType output describing a Xilinx Alveo card.
	const xilinxPCI = `PCI:

    pci-bridge:

      Type: FPGA Accelerator
      Vendor ID: 0x10ee  (Xilinx Corporation)
      Device ID: 0x5000
      Device Name: Alveo U250
`
	d := &darwinDetector{run: func(_ context.Context, name string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "SPPCIDataType" {
			return xilinxPCI, nil
		}
		return "", nil
	}}
	fpgas, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(fpgas) != 1 {
		t.Fatalf("expected 1 FPGA from synthetic Xilinx PCI, got %d: %+v", len(fpgas), fpgas)
	}
	f := fpgas[0]
	if f.Vendor != "Xilinx" || f.PCIVendorID != "0x10ee" || f.Interface != "PCIe" {
		t.Errorf("parsed Xilinx record wrong: %+v", f)
	}
	if f.PCIDeviceID != "0x5000" {
		t.Errorf("Device ID not parsed: %+v", f)
	}

	// Empty output -> empty result (no fabrication).
	dEmpty := &darwinDetector{run: func(_ context.Context, _ string, _ ...string) (string, error) {
		return "", nil
	}}
	got, err := dEmpty.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect(empty): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty system_profiler output must yield no FPGA, got %+v", got)
	}
}

// TestParseSystemProfilerUSB_JTAG covers the USB-JTAG cable path (e.g. a
// Digilent/Xilinx programming cable presenting an FPGA vendor id).
func TestParseSystemProfilerUSB_JTAG(t *testing.T) {
	const usb = `USB:

    USB 3.1 Bus:

      JTAG-HS3 Programming Cable:
        Vendor ID: 0x0403  (Future Technology Devices)
        Manufacturer: Digilent
      Xilinx Platform Cable USB II:
        Vendor ID: 0x03fd
        Manufacturer: Xilinx
`
	fpgas := parseSystemProfilerUSB(usb)
	if len(fpgas) != 1 {
		t.Fatalf("expected 1 USB FPGA device (Xilinx by name), got %d: %+v", len(fpgas), fpgas)
	}
	if fpgas[0].Vendor != "Xilinx" || fpgas[0].Interface != "USB" {
		t.Errorf("USB Xilinx record wrong: %+v", fpgas[0])
	}
}

// TestParseSystemProfilerPCI_NoFPGA proves the parser ignores non-FPGA PCI
// devices (so the no-FPGA host empty result is a real parse outcome).
func TestParseSystemProfilerPCI_NoFPGA(t *testing.T) {
	const pci = `PCI:

    ethernet:

      Type: Network Controller
      Vendor ID: 0x8086  (Intel Corporation)
      Device ID: 0x10d3
`
	if fpgas := parseSystemProfilerPCI(pci, "PCIe"); len(fpgas) != 0 {
		t.Errorf("non-FPGA PCI must yield no FPGA, got %+v", fpgas)
	}
}
