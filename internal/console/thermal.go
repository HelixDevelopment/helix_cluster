package console

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// ThermalZone represents a single thermal zone reading.
type ThermalZone struct {
	Name    string
	Type    string
	TempC   float64 // temperature in Celsius
	Path    string
}

// ThermalMonitor reads sysfs thermal zones and detects throttling.
type ThermalMonitor struct {
	sysfsBase string
	mu        sync.RWMutex
	zones     []ThermalZone
}

// NewThermalMonitor creates a new thermal monitor.
func NewThermalMonitor() *ThermalMonitor {
	return &ThermalMonitor{
		sysfsBase: "/sys/class/thermal",
	}
}

// ReadZones discovers and reads all thermal zones.
// On non-Linux platforms it returns an empty slice without error.
func (tm *ThermalMonitor) ReadZones() ([]ThermalZone, error) {
	if runtime.GOOS != "linux" {
		return []ThermalZone{}, nil
	}

	zones, err := tm.readZonesLinux()
	if err != nil {
		return nil, err
	}

	tm.mu.Lock()
	tm.zones = zones
	tm.mu.Unlock()
	return zones, nil
}

func (tm *ThermalMonitor) readZonesLinux() ([]ThermalZone, error) {
	entries, err := os.ReadDir(tm.sysfsBase)
	if err != nil {
		if os.IsNotExist(err) {
			return []ThermalZone{}, nil
		}
		return nil, err
	}

	var zones []ThermalZone
	for _, entry := range entries {
		if !entry.IsDir() && !strings.HasPrefix(entry.Name(), "thermal_zone") {
			continue
		}
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), "thermal_zone") {
			continue
		}

		zonePath := filepath.Join(tm.sysfsBase, entry.Name())
		zone, err := tm.readZone(zonePath)
		if err != nil {
			continue // skip unreadable zones
		}
		zones = append(zones, zone)
	}
	return zones, nil
}

func (tm *ThermalMonitor) readZone(path string) (ThermalZone, error) {
	nameBytes, _ := os.ReadFile(filepath.Join(path, "type"))
	name := strings.TrimSpace(string(nameBytes))

	tempBytes, err := os.ReadFile(filepath.Join(path, "temp"))
	if err != nil {
		return ThermalZone{}, err
	}
	tempMilli, err := strconv.ParseInt(strings.TrimSpace(string(tempBytes)), 10, 64)
	if err != nil {
		return ThermalZone{}, err
	}

	return ThermalZone{
		Name:  name,
		Type:  name,
		TempC: float64(tempMilli) / 1000.0,
		Path:  path,
	}, nil
}

// Zones returns the last read thermal zones.
func (tm *ThermalMonitor) Zones() []ThermalZone {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	out := make([]ThermalZone, len(tm.zones))
	copy(out, tm.zones)
	return out
}

// ThrottleDetected returns true if any zone exceeds the given threshold (°C).
func (tm *ThermalMonitor) ThrottleDetected(thresholdC float64) bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	for _, z := range tm.zones {
		if z.TempC >= thresholdC {
			return true
		}
	}
	return false
}

// MaxTemp returns the highest temperature among all zones.
func (tm *ThermalMonitor) MaxTemp() (float64, error) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	if len(tm.zones) == 0 {
		return 0, fmt.Errorf("no thermal zones available")
	}
	max := tm.zones[0].TempC
	for _, z := range tm.zones[1:] {
		if z.TempC > max {
			max = z.TempC
		}
	}
	return max, nil
}
