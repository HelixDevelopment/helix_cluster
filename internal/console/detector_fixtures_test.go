package console

import (
	"path/filepath"
	"runtime"
	"testing"
)

// fixture-driven hardware-identification tests.
//
// Each fixture under testdata/<deviceclass>/ contains a real-shaped /proc/cpuinfo
// ("cpuinfo") plus a device-tree subtree ("device-tree/compatible" with NUL-
// separated entries, exactly as the kernel exposes /proc/device-tree/compatible).
// The detector's injectable-path API (procCPUInfoPath, deviceTreePath) is pointed
// at each fixture and we assert the SPECIFIC classification label the SoC-match
// table must produce.
//
// The OS-independent scoring helpers (checkCPUInfo / checkDeviceTree / detectLinux)
// are exercised on every host OS; the GOOS-gated public Detect() is verified on
// Linux. Corrupting any entry in the SoC-match table (the strings.Contains rows in
// checkCPUInfo / checkDeviceTree) flips at least one label below and fails the test.

type deviceClassCase struct {
	name string
	dir  string
	// expected classification from the CPU-info match table alone.
	wantCPUType ConsoleType
	// true when checkCPUInfo must yield a non-zero score (a real CPU match).
	wantCPUScored bool
	// expected classification from the device-tree match table alone.
	wantDTType ConsoleType
	// expected device-tree score (exact: the table uses fixed 1.0 / 0.5 / 0.0).
	wantDTScore float64
	// expected fused detectLinux() classification + whether it is a console.
	wantDetectType    ConsoleType
	wantIsConsoleHigh bool // true => fused confidence must clear the 0.5 IsConsole bar
}

func deviceClassCases() []deviceClassCase {
	return []deviceClassCase{
		{
			// PS4 (AMD Jaguar) — CPU table matches Jaguar AND the "playstation"/"ps4"
			// model hint; device tree advertises playstation4.
			name:              "ps4_jaguar",
			dir:               "ps4_jaguar",
			wantCPUType:       TypePS4,
			wantCPUScored:     true,
			wantDTType:        TypePS4,
			wantDTScore:       1.0,
			wantDetectType:    TypePS4,
			wantIsConsoleHigh: true,
		},
		{
			// PS5 (AMD Zen 2 custom APU) — CPU table matches "zen 2"; device tree
			// advertises playstation5.
			name:              "ps5_zen2",
			dir:               "ps5_zen2",
			wantCPUType:       TypePS5,
			wantCPUScored:     true,
			wantDTType:        TypePS5,
			wantDTScore:       1.0,
			wantDetectType:    TypePS5,
			wantIsConsoleHigh: true,
		},
		{
			// x86 server (Intel Xeon) — no console signature anywhere.
			name:              "x86_server",
			dir:               "x86_server",
			wantCPUType:       TypeUnknown,
			wantCPUScored:     false,
			wantDTType:        TypeUnknown,
			wantDTScore:       0.0,
			wantDetectType:    TypeUnknown,
			wantIsConsoleHigh: false,
		},
		{
			// Rockchip RK3588 ARM SoC — ARM cpuinfo with no x86 vendor, no console
			// match; device tree advertises rockchip,rk3588.
			name:              "rk3588_soc",
			dir:               "rk3588_soc",
			wantCPUType:       TypeUnknown,
			wantCPUScored:     false,
			wantDTType:        TypeUnknown,
			wantDTScore:       0.0,
			wantDetectType:    TypeUnknown,
			wantIsConsoleHigh: false,
		},
		{
			// Raspberry Pi 4 (BCM2711) — ARM board, no console match.
			name:              "raspberrypi",
			dir:               "raspberrypi",
			wantCPUType:       TypeUnknown,
			wantCPUScored:     false,
			wantDTType:        TypeUnknown,
			wantDTScore:       0.0,
			wantDetectType:    TypeUnknown,
			wantIsConsoleHigh: false,
		},
		{
			// Generic / unknown box — generic Intel desktop CPU, generic board.
			name:              "generic_unknown",
			dir:               "generic_unknown",
			wantCPUType:       TypeUnknown,
			wantCPUScored:     false,
			wantDTType:        TypeUnknown,
			wantDTScore:       0.0,
			wantDetectType:    TypeUnknown,
			wantIsConsoleHigh: false,
		},
	}
}

func newFixtureDetector(class string) *Detector {
	d := NewDetector()
	d.procCPUInfoPath = filepath.Join("testdata", class, "cpuinfo")
	d.deviceTreePath = filepath.Join("testdata", class, "device-tree")
	return d
}

// TestDetectDeviceClass_CPUInfoTable asserts the CPU-info match table classifies
// each device class to the correct label. This is the load-bearing assertion:
// corrupting any strings.Contains row in checkCPUInfo (e.g. the "zen 2" -> PS5
// row, or the jaguar -> PS4 row) flips wantCPUType for a console fixture and
// fails this test.
func TestDetectDeviceClass_CPUInfoTable(t *testing.T) {
	for _, tc := range deviceClassCases() {
		t.Run(tc.name, func(t *testing.T) {
			d := newFixtureDetector(tc.dir)
			ct, score, err := d.checkCPUInfo()
			if err != nil {
				t.Fatalf("checkCPUInfo(%s): unexpected error: %v", tc.dir, err)
			}
			if ct != tc.wantCPUType {
				t.Errorf("checkCPUInfo(%s): got type %q, want %q", tc.dir, ct, tc.wantCPUType)
			}
			if tc.wantCPUScored && score <= 0.0 {
				t.Errorf("checkCPUInfo(%s): expected a positive match score, got %f", tc.dir, score)
			}
			if !tc.wantCPUScored && score != 0.0 {
				t.Errorf("checkCPUInfo(%s): expected zero score for non-console CPU, got %f", tc.dir, score)
			}
		})
	}
}

// TestDetectDeviceClass_DeviceTreeTable asserts the device-tree match table
// classifies each fixture's compatible string to the correct label and exact
// score. Corrupting the device-tree match table (e.g. swapping the ps4/ps5
// branches, or changing a fixed score) flips a label/score below and fails.
func TestDetectDeviceClass_DeviceTreeTable(t *testing.T) {
	for _, tc := range deviceClassCases() {
		t.Run(tc.name, func(t *testing.T) {
			d := newFixtureDetector(tc.dir)
			ct, score, err := d.checkDeviceTree()
			if err != nil {
				t.Fatalf("checkDeviceTree(%s): unexpected error: %v", tc.dir, err)
			}
			if ct != tc.wantDTType {
				t.Errorf("checkDeviceTree(%s): got type %q, want %q", tc.dir, ct, tc.wantDTType)
			}
			if score != tc.wantDTScore {
				t.Errorf("checkDeviceTree(%s): got score %f, want %f", tc.dir, score, tc.wantDTScore)
			}
		})
	}
}

// TestDetectDeviceClass_Fused drives the full fusion path (detectLinux) plus the
// IsConsole verdict for each device class. This proves the end-to-end
// classification an end user relies on: consoles classify as their console type
// and read as a console; servers/SoCs/boards classify as Unknown and are NOT
// treated as console hardware. Corrupting the SoC-match table flips one of these.
func TestDetectDeviceClass_Fused(t *testing.T) {
	for _, tc := range deviceClassCases() {
		t.Run(tc.name, func(t *testing.T) {
			d := newFixtureDetector(tc.dir)
			ct, conf, err := d.detectLinux()
			if err != nil {
				t.Fatalf("detectLinux(%s): unexpected error: %v", tc.dir, err)
			}
			if ct != tc.wantDetectType {
				t.Errorf("detectLinux(%s): got type %q, want %q", tc.dir, ct, tc.wantDetectType)
			}
			isConsole := ct != TypeUnknown && conf >= 0.5
			if isConsole != tc.wantIsConsoleHigh {
				t.Errorf("detectLinux(%s): isConsole=%v (conf %f), want %v", tc.dir, isConsole, conf, tc.wantIsConsoleHigh)
			}
		})
	}
}

// TestDetectDeviceClass_PublicDetect exercises the GOOS-gated public Detect() on
// Linux against the fixtures. On non-Linux hosts Detect() is defined to return
// TypeUnknown/0.0, so we assert that documented contract instead of skipping
// blind (CLAUDE-2: the host path is always asserted).
func TestDetectDeviceClass_PublicDetect(t *testing.T) {
	for _, tc := range deviceClassCases() {
		t.Run(tc.name, func(t *testing.T) {
			d := newFixtureDetector(tc.dir)
			ct, conf, err := d.Detect()
			if err != nil {
				t.Fatalf("Detect(%s): unexpected error: %v", tc.dir, err)
			}
			if runtime.GOOS != "linux" {
				if ct != TypeUnknown || conf != 0.0 {
					t.Errorf("Detect(%s) on %s: got (%q,%f), want (unknown,0.0)", tc.dir, runtime.GOOS, ct, conf)
				}
				return
			}
			if ct != tc.wantDetectType {
				t.Errorf("Detect(%s): got type %q, want %q", tc.dir, ct, tc.wantDetectType)
			}
		})
	}
}
