//go:build linux

package powergater

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sysfsRoots returns fake /sys/class/power_supply + /sys/class/thermal paths
// under a per-test temp dir; subdirs are created lazily by put.
func sysfsRoots(t *testing.T) (powerDir, thermalDir string) {
	t.Helper()
	root := t.TempDir()
	return filepath.Join(root, "power_supply"), filepath.Join(root, "thermal")
}

func put(t *testing.T, path, val string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(val), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxSource_FixtureTree(t *testing.T) {
	powerDir, thermalDir := sysfsRoots(t)

	// AC offline, battery discharging at 42%.
	put(t, filepath.Join(powerDir, "AC", "type"), "Mains\n")
	put(t, filepath.Join(powerDir, "AC", "online"), "0\n")
	put(t, filepath.Join(powerDir, "BAT0", "type"), "Battery\n")
	put(t, filepath.Join(powerDir, "BAT0", "status"), "Discharging\n")
	put(t, filepath.Join(powerDir, "BAT0", "capacity"), "42\n")

	// Two thermal zones; reader should pick the max (55.0C).
	put(t, filepath.Join(thermalDir, "thermal_zone0", "temp"), "48000\n")
	put(t, filepath.Join(thermalDir, "thermal_zone1", "temp"), "55000\n")

	src := &linuxSource{powerSupplyDir: powerDir, thermalDir: thermalDir}

	if src.Charging() {
		t.Errorf("AC offline + discharging should not be charging")
	}
	if pct := src.BatteryPercent(); pct != 42 {
		t.Errorf("battery = %d want 42", pct)
	}
	if temp := src.CPUTempC(); math.Abs(temp-55.0) > 0.001 {
		t.Errorf("temp = %v want 55.0 (max zone)", temp)
	}

	// Flip AC online -> charging.
	put(t, filepath.Join(powerDir, "AC", "online"), "1\n")
	if !src.Charging() {
		t.Errorf("AC online should be charging")
	}
}

func TestLinuxSource_NoBatteryReads100(t *testing.T) {
	powerDir, thermalDir := sysfsRoots(t)
	put(t, filepath.Join(powerDir, "AC", "type"), "Mains\n")
	put(t, filepath.Join(powerDir, "AC", "online"), "1\n")
	src := &linuxSource{powerSupplyDir: powerDir, thermalDir: thermalDir}
	if pct := src.BatteryPercent(); pct != 100 {
		t.Errorf("no battery should read 100, got %d", pct)
	}
	if !src.Charging() {
		t.Errorf("AC online desktop should be charging")
	}
}

// TestLinuxSource_RealHost is the CLAUDE-2 integration test on a real Linux
// host: read battery via the real reader and cross-check against `cat`-equiv
// sysfs oracle. Skipped only when the host genuinely has no power_supply class
// (e.g. a container) — justified, not hiding an unimplemented port.
func TestLinuxSource_RealHost(t *testing.T) {
	const real = "/sys/class/power_supply"
	if _, err := os.Stat(real); err != nil {
		t.Skip("no /sys/class/power_supply on this host (container); no battery to read")
	}
	src := NewPowerSource()
	pct := src.BatteryPercent()
	if pct < 0 || pct > 100 {
		t.Fatalf("battery %d out of range", pct)
	}
	// Oracle: find a Battery dir and read capacity directly.
	entries, _ := os.ReadDir(real)
	for _, e := range entries {
		typPath := filepath.Join(real, e.Name(), "type")
		b, err := exec.Command("cat", typPath).Output()
		if err != nil || strings.TrimSpace(string(b)) != "Battery" {
			continue
		}
		capB, err := exec.Command("cat", filepath.Join(real, e.Name(), "capacity")).Output()
		if err != nil {
			continue
		}
		want, _ := strconv.Atoi(strings.TrimSpace(string(capB)))
		if pct != want {
			t.Errorf("reader battery %d != sysfs oracle %d", pct, want)
		}
		t.Logf("REAL linux host battery=%d%% (oracle ok)", pct)
		return
	}
	t.Logf("no battery present; reader returned %d%%", pct)
}
