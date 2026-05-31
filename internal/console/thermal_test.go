package console

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNewThermalMonitor(t *testing.T) {
	tm := NewThermalMonitor()
	if tm == nil {
		t.Fatal("expected non-nil monitor")
	}
}

func TestReadZones_NonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("skipping non-Linux path on Linux")
	}
	tm := NewThermalMonitor()
	zones, err := tm.ReadZones()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(zones) != 0 {
		t.Errorf("expected 0 zones, got %d", len(zones))
	}
}

func TestReadZones_MockLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("skipping Linux path on non-Linux")
	}

	tmpDir := t.TempDir()
	zoneDir := filepath.Join(tmpDir, "thermal_zone0")
	if err := os.MkdirAll(zoneDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zoneDir, "type"), []byte("cpu-thermal\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zoneDir, "temp"), []byte("55000\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tm := NewThermalMonitor()
	tm.sysfsBase = tmpDir

	zones, err := tm.ReadZones()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(zones) != 1 {
		t.Fatalf("expected 1 zone, got %d", len(zones))
	}
	if zones[0].Name != "cpu-thermal" {
		t.Errorf("expected name cpu-thermal, got %s", zones[0].Name)
	}
	if zones[0].TempC != 55.0 {
		t.Errorf("expected 55.0C, got %f", zones[0].TempC)
	}
}

func TestThrottleDetected(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("skipping Linux path on non-Linux")
	}

	tmpDir := t.TempDir()
	zoneDir := filepath.Join(tmpDir, "thermal_zone0")
	if err := os.MkdirAll(zoneDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zoneDir, "type"), []byte("gpu-thermal\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zoneDir, "temp"), []byte("85000\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tm := NewThermalMonitor()
	tm.sysfsBase = tmpDir

	if _, err := tm.ReadZones(); err != nil {
		t.Fatal(err)
	}

	if !tm.ThrottleDetected(80.0) {
		t.Error("expected throttle detected above 80C")
	}
	if tm.ThrottleDetected(90.0) {
		t.Error("expected no throttle below 90C")
	}
}

func TestMaxTemp(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("skipping Linux path on non-Linux")
	}

	tmpDir := t.TempDir()
	for i, temp := range []string{"45000", "65000"} {
		zoneDir := filepath.Join(tmpDir, "thermal_zone"+string(rune('0'+i)))
		if err := os.MkdirAll(zoneDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(zoneDir, "type"), []byte("zone\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(zoneDir, "temp"), []byte(temp+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tm := NewThermalMonitor()
	tm.sysfsBase = tmpDir

	if _, err := tm.ReadZones(); err != nil {
		t.Fatal(err)
	}

	max, err := tm.MaxTemp()
	if err != nil {
		t.Fatal(err)
	}
	if max != 65.0 {
		t.Errorf("expected max 65.0, got %f", max)
	}
}

func TestMaxTemp_NoZones(t *testing.T) {
	tm := NewThermalMonitor()
	_, err := tm.MaxTemp()
	if err == nil {
		t.Error("expected error when no zones available")
	}
}
