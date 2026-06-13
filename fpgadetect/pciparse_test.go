package fpgadetect

import (
	"os"
	"path/filepath"
	"testing"
)

// writePCIDevice creates a fixture PCI device directory under
// <sys>/bus/pci/devices/<addr> with the given vendor/device id files.
func writePCIDevice(t *testing.T, sys, addr, vendor, device string) {
	t.Helper()
	dir := filepath.Join(sys, "bus", "pci", "devices", addr)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor"), []byte(vendor+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if device != "" {
		if err := os.WriteFile(filepath.Join(dir, "device"), []byte(device+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// buildFixtureRoots constructs a fixture sysfs/dev tree describing a Xilinx
// Alveo (0x10ee), an Intel/Altera PAC (0x1172), a Lattice device (0x1204), and
// a non-FPGA device (Intel 0x8086) that MUST be ignored — plus Xilinx XRT and
// Intel OPAE device nodes.
func buildFixtureRoots(t *testing.T) linuxRoots {
	t.Helper()
	base := t.TempDir()
	sys := filepath.Join(base, "sys")
	dev := filepath.Join(base, "dev")

	// Xilinx Alveo U250 management function (0x10ee, device 0x5000 -> Alveo).
	writePCIDevice(t, sys, "0000:01:00.0", "0x10ee", "0x5000")
	// Intel/Altera PAC (0x1172, device 0x09c4 -> PAC/Stratix).
	writePCIDevice(t, sys, "0000:02:00.0", "0x1172", "0x09c4")
	// Lattice device (0x1204, unmapped device id -> family "").
	writePCIDevice(t, sys, "0000:03:00.0", "0x1204", "0x1234")
	// Non-FPGA: Intel host bridge (0x8086) — MUST be ignored.
	writePCIDevice(t, sys, "0000:00:00.0", "0x8086", "0x1234")

	// Xilinx XRT management node + sysfs class.
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dev, "xclmgmt128"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sys, "class", "xrt", "xrt.0"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Intel OPAE device node + sysfs class.
	if err := os.WriteFile(filepath.Join(dev, "intel-fpga-fme.0"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sys, "class", "fpga_region", "region0"), 0o755); err != nil {
		t.Fatal(err)
	}

	return linuxRoots{sys: sys, dev: dev}
}

func TestDetectPCIFPGAs_AllThreeVendors(t *testing.T) {
	roots := buildFixtureRoots(t)
	fpgas := detectPCIFPGAs(roots)

	byVendor := map[string]FPGA{}
	for _, f := range fpgas {
		byVendor[f.Vendor] = f
	}

	xi, ok := byVendor["Xilinx"]
	if !ok {
		t.Fatalf("expected Xilinx FPGA by PCI vendor 0x10ee, got %+v", fpgas)
	}
	if xi.PCIVendorID != "0x10ee" {
		t.Errorf("Xilinx PCIVendorID = %q, want 0x10ee", xi.PCIVendorID)
	}
	if xi.PCIDeviceID != "0x5000" {
		t.Errorf("Xilinx PCIDeviceID = %q, want 0x5000", xi.PCIDeviceID)
	}
	if xi.Family != "Alveo" {
		t.Errorf("Xilinx Family = %q, want Alveo", xi.Family)
	}
	if xi.Interface != "PCIe" {
		t.Errorf("Xilinx Interface = %q, want PCIe", xi.Interface)
	}

	al, ok := byVendor["Intel/Altera"]
	if !ok {
		t.Fatalf("expected Intel/Altera FPGA by PCI vendor 0x1172, got %+v", fpgas)
	}
	if al.PCIVendorID != "0x1172" || al.Family != "PAC/Stratix" {
		t.Errorf("Altera fields wrong: %+v", al)
	}

	la, ok := byVendor["Lattice"]
	if !ok {
		t.Fatalf("expected Lattice FPGA by PCI vendor 0x1204, got %+v", fpgas)
	}
	if la.PCIVendorID != "0x1204" {
		t.Errorf("Lattice PCIVendorID = %q, want 0x1204", la.PCIVendorID)
	}
	// Unmapped device id -> empty family, model falls back to device-id form.
	if la.Family != "" {
		t.Errorf("Lattice Family = %q, want empty (unmapped)", la.Family)
	}
	if la.Model != "Lattice FPGA device 0x1234" {
		t.Errorf("Lattice Model = %q, want fallback form", la.Model)
	}

	// The Intel host bridge (0x8086) MUST NOT be reported.
	if _, bad := byVendor["Intel"]; bad {
		t.Errorf("non-FPGA Intel host bridge wrongly reported: %+v", fpgas)
	}
	if len(fpgas) != 3 {
		t.Errorf("expected exactly 3 PCI FPGAs, got %d: %+v", len(fpgas), fpgas)
	}
}

func TestDetectXilinxXRT(t *testing.T) {
	roots := buildFixtureRoots(t)
	fpgas := detectXilinxXRT(roots)
	if len(fpgas) == 0 {
		t.Fatalf("expected XRT detection via /dev/xclmgmt* and /sys/class/xrt")
	}
	for _, f := range fpgas {
		if f.Vendor != "Xilinx" || f.Family != "Alveo" {
			t.Errorf("XRT record wrong: %+v", f)
		}
	}
}

func TestDetectIntelOPAE(t *testing.T) {
	roots := buildFixtureRoots(t)
	fpgas := detectIntelOPAE(roots)
	if len(fpgas) == 0 {
		t.Fatalf("expected OPAE detection via /dev/intel-fpga* and /sys/class/fpga*")
	}
	for _, f := range fpgas {
		if f.Vendor != "Intel/Altera" {
			t.Errorf("OPAE record wrong vendor: %+v", f)
		}
	}
}

func TestScanLinuxRoots_FullHost(t *testing.T) {
	roots := buildFixtureRoots(t)
	fpgas := scanLinuxRoots(roots)
	// 3 PCI + XRT (dev + sysfs = 2) + OPAE (dev + sysfs = 2) = 7 records.
	if len(fpgas) < 4 {
		t.Fatalf("expected the PCI + XRT + OPAE signals, got %d: %+v", len(fpgas), fpgas)
	}
	vendors := map[string]bool{}
	for _, f := range fpgas {
		vendors[f.Vendor] = true
	}
	for _, want := range []string{"Xilinx", "Intel/Altera", "Lattice"} {
		if !vendors[want] {
			t.Errorf("missing vendor %q in full-host scan: %+v", want, fpgas)
		}
	}
}

func TestScanLinuxRoots_NoFPGA(t *testing.T) {
	base := t.TempDir()
	sys := filepath.Join(base, "sys")
	dev := filepath.Join(base, "dev")
	// Only a non-FPGA PCI device present.
	writePCIDevice(t, sys, "0000:00:00.0", "0x8086", "0x29c0")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := scanLinuxRoots(linuxRoots{sys: sys, dev: dev}); len(got) != 0 {
		t.Errorf("expected no FPGAs on non-FPGA host, got %+v", got)
	}
}

func TestScanLinuxRoots_MissingRoots(t *testing.T) {
	// Non-existent roots must yield empty, not panic or error.
	if got := scanLinuxRoots(linuxRoots{sys: "/nonexistent-xyz", dev: "/nonexistent-xyz"}); len(got) != 0 {
		t.Errorf("expected empty on missing roots, got %+v", got)
	}
}

func TestFPGAVendorName(t *testing.T) {
	cases := map[string]string{
		"0x10ee": "Xilinx",
		"10ee":   "Xilinx",
		"0x1172": "Intel/Altera",
		"0x1204": "Lattice",
		"0x8086": "",
		"":       "",
	}
	for in, want := range cases {
		if got := fpgaVendorName(in); got != want {
			t.Errorf("fpgaVendorName(%q) = %q, want %q", in, got, want)
		}
	}
}
