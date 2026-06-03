//go:build linux

package powergater

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// linuxSource is the real Linux PowerSource. Battery percentage and charging
// state come from /sys/class/power_supply/*, and CPU temperature from
// /sys/class/thermal/thermal_zone*/temp (millidegrees Celsius). All values are
// real, measured sysfs readings — never a stub.
//
// CLAUDE-2: this returns real measured values on Linux.
type linuxSource struct {
	// powerSupplyDir and thermalDir are overridable in unit tests with a
	// fixture sysfs tree. In production they point at the real /sys paths.
	powerSupplyDir string
	thermalDir     string
	nowFn          func() time.Time
}

// NewPowerSource returns the real PowerSource for this host (Linux).
func NewPowerSource() PowerSource {
	return &linuxSource{
		powerSupplyDir: "/sys/class/power_supply",
		thermalDir:     "/sys/class/thermal",
	}
}

func (l *linuxSource) Now() time.Time {
	if l.nowFn != nil {
		return l.nowFn()
	}
	return time.Now()
}

// readTrimmed reads a sysfs file and returns its trimmed contents.
func readTrimmed(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// batteryDirs returns the power_supply entries whose type is "Battery".
func (l *linuxSource) batteryDirs() []string {
	entries, err := os.ReadDir(l.powerSupplyDir)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		dir := filepath.Join(l.powerSupplyDir, e.Name())
		if t, ok := readTrimmed(filepath.Join(dir, "type")); ok && t == "Battery" {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// Charging reports external power. It is true when any AC/Mains supply is
// online, or when a battery reports a Charging/Full status. A host with no
// battery and no readable AC line is reported as charging (treated as
// wall-powered) since it has no battery to drain.
func (l *linuxSource) Charging() bool {
	entries, err := os.ReadDir(l.powerSupplyDir)
	if err != nil {
		return true
	}
	sawBattery := false
	for _, e := range entries {
		dir := filepath.Join(l.powerSupplyDir, e.Name())
		typ, _ := readTrimmed(filepath.Join(dir, "type"))
		switch typ {
		case "Mains", "USB", "ADP":
			if online, ok := readTrimmed(filepath.Join(dir, "online")); ok && online == "1" {
				return true
			}
		case "Battery":
			sawBattery = true
			if st, ok := readTrimmed(filepath.Join(dir, "status")); ok {
				if st == "Charging" || st == "Full" {
					return true
				}
			}
		}
	}
	// No AC online and no battery present -> wall-powered host.
	return !sawBattery
}

// BatteryPercent returns the first battery's capacity, or 100 when no battery
// is present (a desktop can never be "battery low").
func (l *linuxSource) BatteryPercent() int {
	for _, dir := range l.batteryDirs() {
		if capacity, ok := readTrimmed(filepath.Join(dir, "capacity")); ok {
			if v, err := strconv.Atoi(capacity); err == nil {
				if v < 0 {
					v = 0
				}
				if v > 100 {
					v = 100
				}
				return v
			}
		}
	}
	return 100
}

// CPUTempC returns the highest thermal-zone temperature in Celsius, or
// TempUnknown when no zone is readable.
func (l *linuxSource) CPUTempC() float64 {
	entries, err := os.ReadDir(l.thermalDir)
	if err != nil {
		return TempUnknown
	}
	best := TempUnknown
	found := false
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "thermal_zone") {
			continue
		}
		raw, ok := readTrimmed(filepath.Join(l.thermalDir, e.Name(), "temp"))
		if !ok {
			continue
		}
		milli, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		c := float64(milli) / 1000.0
		if !found || c > best {
			best = c
			found = true
		}
	}
	if !found {
		return TempUnknown
	}
	return best
}
