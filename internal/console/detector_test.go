package console

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNewDetector(t *testing.T) {
	d := NewDetector()
	if d == nil {
		t.Fatal("expected non-nil detector")
	}
	if d.procCPUInfoPath == "" {
		t.Error("expected procCPUInfoPath to be set")
	}
}

func TestDetect_NonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("skipping non-Linux path on Linux")
	}
	d := NewDetector()
	ct, conf, err := d.Detect()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != TypeUnknown {
		t.Errorf("expected TypeUnknown, got %s", ct)
	}
	if conf != 0.0 {
		t.Errorf("expected 0 confidence, got %f", conf)
	}
}

func TestDetect_Linux_MockCPUInfo(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("skipping Linux path on non-Linux")
	}

	tmpDir := t.TempDir()
	cpuinfo := filepath.Join(tmpDir, "cpuinfo")
	if err := os.WriteFile(cpuinfo, []byte("model name\t: AMD Jaguar (PlayStation 4)\n"), 0644); err != nil {
		t.Fatal(err)
	}

	d := NewDetector()
	d.procCPUInfoPath = cpuinfo
	d.deviceTreePath = filepath.Join(tmpDir, "nonexistent")

	ct, conf, err := d.Detect()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != TypePS4 {
		t.Errorf("expected TypePS4, got %s", ct)
	}
	if conf < 0.5 {
		t.Errorf("expected high confidence, got %f", conf)
	}
}

func TestDetect_Linux_MockDeviceTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("skipping Linux path on non-Linux")
	}

	tmpDir := t.TempDir()
	dtDir := filepath.Join(tmpDir, "device-tree")
	if err := os.MkdirAll(dtDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dtDir, "compatible"), []byte("sony,playstation5\x00"), 0644); err != nil {
		t.Fatal(err)
	}

	cpuinfo := filepath.Join(tmpDir, "cpuinfo")
	if err := os.WriteFile(cpuinfo, []byte("model name\t: Generic CPU\n"), 0644); err != nil {
		t.Fatal(err)
	}

	d := NewDetector()
	d.procCPUInfoPath = cpuinfo
	d.deviceTreePath = dtDir

	ct, conf, err := d.Detect()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != TypePS5 {
		t.Errorf("expected TypePS5, got %s", ct)
	}
	if conf < 0.8 {
		t.Errorf("expected high confidence, got %f", conf)
	}
}

func TestIsConsole(t *testing.T) {
	if runtime.GOOS != "linux" {
		d := NewDetector()
		if d.IsConsole() {
			t.Error("expected false on non-Linux")
		}
		return
	}

	tmpDir := t.TempDir()
	cpuinfo := filepath.Join(tmpDir, "cpuinfo")
	if err := os.WriteFile(cpuinfo, []byte("model name\t: AMD Zen 2 (PlayStation 5)\n"), 0644); err != nil {
		t.Fatal(err)
	}

	d := NewDetector()
	d.procCPUInfoPath = cpuinfo
	d.deviceTreePath = filepath.Join(tmpDir, "nonexistent")

	if !d.IsConsole() {
		t.Error("expected IsConsole true for PS5 signature")
	}
}

func TestIsConsole_NotConsole(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("skip on non-Linux")
	}

	tmpDir := t.TempDir()
	cpuinfo := filepath.Join(tmpDir, "cpuinfo")
	if err := os.WriteFile(cpuinfo, []byte("model name\t: Intel Core i7\n"), 0644); err != nil {
		t.Fatal(err)
	}

	d := NewDetector()
	d.procCPUInfoPath = cpuinfo
	d.deviceTreePath = filepath.Join(tmpDir, "nonexistent")

	if d.IsConsole() {
		t.Error("expected IsConsole false for generic CPU")
	}
}
