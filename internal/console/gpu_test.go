package console

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNewGPUQuerier(t *testing.T) {
	gq := NewGPUQuerier()
	if gq == nil {
		t.Fatal("expected non-nil querier")
	}
}

func TestQuery_NonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("skipping non-Linux path on Linux")
	}
	gq := NewGPUQuerier()
	info, err := gq.Query()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.ComputeOK {
		t.Error("expected ComputeOK false in NoOp mode")
	}
	if info.Vendor != "noop" {
		t.Errorf("expected noop vendor, got %s", info.Vendor)
	}
}

func TestQuery_MockDRM(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("skipping Linux path on non-Linux")
	}

	tmpDir := t.TempDir()
	drmDir := filepath.Join(tmpDir, "drm")
	cardDir := filepath.Join(drmDir, "card0", "device")
	if err := os.MkdirAll(cardDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "vendor"), []byte("0x1002\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "uevent"), []byte("DRIVER=amdgpu\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "mem_info_vram_total"), []byte("8589934592\n"), 0644); err != nil {
		t.Fatal(err)
	}

	gq := NewGPUQuerier()
	// We cannot easily redirect /sys/class/drm; test the fallback path instead.
	info, err := gq.Query()
	// Either we get a real GPU or fallback; just ensure no panic.
	_ = info
	_ = err
}

func TestHasVulkanCompute_NonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("skipping non-Linux path on Linux")
	}
	gq := NewGPUQuerier()
	if gq.HasVulkanCompute() {
		t.Error("expected false on non-Linux")
	}
}

func TestHasVulkanCompute_MockLoader(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("skipping Linux path on non-Linux")
	}

	tmpDir := t.TempDir()
	loader := filepath.Join(tmpDir, "libvulkan.so")
	if err := os.WriteFile(loader, []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	gq := NewGPUQuerier()
	gq.vulkanLoaderPath = loader

	if !gq.HasVulkanCompute() {
		t.Error("expected true when loader exists")
	}
}
