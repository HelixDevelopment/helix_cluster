//go:build darwin

package fpgadetect

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// newDetector returns the macOS Detector.
func newDetector() Detector { return &darwinDetector{run: runCommand} }

// runCommand executes a command and returns combined stdout. It is a package
// variable so tests can confirm the real path runs against the real host.
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

type commandRunner func(ctx context.Context, name string, args ...string) (string, error)

type darwinDetector struct {
	run commandRunner
}

// Detect performs the real macOS probe. On macOS an FPGA is attached over PCIe
// (rare on Apple silicon, which has no general PCIe slots), Thunderbolt (a TB
// chassis presenting a PCIe FPGA card), or USB (a JTAG programming cable / eval
// board). We query system_profiler for each domain and match known FPGA-vendor
// PCI IDs (Xilinx 0x10ee / Intel-Altera 0x1172 / Lattice 0x1204) or vendor name
// strings.
//
// On a typical Apple-silicon host there is NO FPGA, and the correct REAL result
// is an empty slice with a nil error — we never fabricate a device.
func (d *darwinDetector) Detect(ctx context.Context) ([]FPGA, error) {
	var fpgas []FPGA

	// PCIe / Thunderbolt-PCIe devices.
	if out, err := d.run(ctx, "system_profiler", "SPPCIDataType"); err == nil {
		fpgas = append(fpgas, parseSystemProfilerPCI(out, "PCIe")...)
	}
	if out, err := d.run(ctx, "system_profiler", "SPThunderboltDataType"); err == nil {
		fpgas = append(fpgas, parseSystemProfilerPCI(out, "Thunderbolt")...)
	}
	// USB-JTAG programming cables / eval boards.
	if out, err := d.run(ctx, "system_profiler", "SPUSBDataType"); err == nil {
		fpgas = append(fpgas, parseSystemProfilerUSB(out)...)
	}

	return fpgas, nil
}

// vendorIDFromLine extracts a normalised hex vendor id from a system_profiler
// "Vendor ID: 0x10ee  (Xilinx)" style line, returning "" if absent.
func vendorIDFromLine(value string) string {
	// value is the text after the colon, e.g. "0x10ee  (Xilinx Corporation)".
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return normalizeHexID(fields[0])
}

// nameMentionsFPGAVendor reports whether a free-text vendor/device name string
// names a known FPGA vendor. Used as a secondary signal when an ID is absent
// (some TB/USB entries carry only a name).
func nameMentionsFPGAVendor(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "xilinx"):
		return "Xilinx"
	case strings.Contains(l, "altera"):
		return "Intel/Altera"
	case strings.Contains(l, "lattice"):
		return "Lattice"
	}
	return ""
}

// parseSystemProfilerPCI scans system_profiler SPPCIDataType /
// SPThunderboltDataType output for FPGA-vendor devices. iface labels the attach
// interface ("PCIe" or "Thunderbolt"). It tracks the most recent Vendor ID and
// Device ID seen and emits an FPGA when a vendor-id (or vendor name) matches a
// known FPGA vendor. Device entries are delimited by the indented item headers
// (lines ending in ":"); we reset per-device state on each new item header.
func parseSystemProfilerPCI(out, iface string) []FPGA {
	var fpgas []FPGA

	var curVendorID, curDeviceID, curName, curVendorName string
	flush := func() {
		vendor := fpgaVendorName(curVendorID)
		if vendor == "" {
			vendor = nameMentionsFPGAVendor(curVendorName)
		}
		if vendor == "" {
			vendor = nameMentionsFPGAVendor(curName)
		}
		if vendor == "" {
			return
		}
		f := FPGA{
			Vendor:    vendor,
			Interface: iface,
			Source:    fmt.Sprintf("system_profiler vendor-id=0x%s name=%q", curVendorID, curName),
		}
		if curVendorID != "" {
			f.PCIVendorID = "0x" + curVendorID
		}
		if curDeviceID != "" {
			f.PCIDeviceID = "0x" + curDeviceID
		}
		if curName != "" {
			f.Model = curName
		} else {
			f.Model = vendor + " FPGA"
		}
		fpgas = append(fpgas, f)
	}

	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		// An item header is an indented line ending in ":" with no value after.
		if strings.HasSuffix(line, ":") && !strings.Contains(line, ": ") {
			flush()
			curVendorID, curDeviceID, curName, curVendorName = "", "", "", ""
			curName = strings.TrimSuffix(line, ":")
			continue
		}
		key, value, ok := splitKV(line)
		if !ok {
			continue
		}
		switch key {
		case "Vendor ID":
			curVendorID = vendorIDFromLine(value)
		case "Device ID":
			curDeviceID = vendorIDFromLine(value)
		case "Vendor Name", "Manufacturer":
			curVendorName = value
		case "Device Name", "Name":
			if curName == "" {
				curName = value
			}
		}
	}
	flush()
	return fpgas
}

// parseSystemProfilerUSB scans system_profiler SPUSBDataType output for USB
// devices whose Vendor ID is a known FPGA vendor, or whose name mentions one
// (e.g. a Digilent/Xilinx JTAG cable). Reported with Interface "USB".
func parseSystemProfilerUSB(out string) []FPGA {
	var fpgas []FPGA

	var curVendorID, curName string
	flush := func() {
		vendor := fpgaVendorName(curVendorID)
		if vendor == "" {
			vendor = nameMentionsFPGAVendor(curName)
		}
		if vendor == "" {
			return
		}
		f := FPGA{
			Vendor:    vendor,
			Interface: "USB",
			Source:    fmt.Sprintf("system_profiler SPUSBDataType vendor-id=0x%s name=%q", curVendorID, curName),
		}
		if curVendorID != "" {
			f.PCIVendorID = "0x" + curVendorID
		}
		if curName != "" {
			f.Model = curName
		} else {
			f.Model = vendor + " USB device"
		}
		fpgas = append(fpgas, f)
	}

	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, ":") && !strings.Contains(line, ": ") {
			flush()
			curVendorID, curName = "", ""
			curName = strings.TrimSuffix(line, ":")
			continue
		}
		key, value, ok := splitKV(line)
		if !ok {
			continue
		}
		switch key {
		case "Vendor ID":
			curVendorID = vendorIDFromLine(value)
		case "Manufacturer", "Vendor Name":
			if curName == "" || nameMentionsFPGAVendor(value) != "" {
				curName = value
			}
		}
	}
	flush()
	return fpgas
}

// splitKV splits a "Key: value" system_profiler line, returning the trimmed key
// and value. ok is false when the line has no "key: value" shape.
func splitKV(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}
