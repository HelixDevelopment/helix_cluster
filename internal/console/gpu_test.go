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
	// Redirect the DRM base path at the faked sysfs tree so Query() exercises
	// real detection instead of the host's /sys/class/drm.
	gq.drmBasePath = drmDir
	// Force the vulkan fallback to be unavailable so a PASS here can only come
	// from the DRM probe actually parsing the faked card.
	gq.vulkanLoaderPath = filepath.Join(tmpDir, "absent-libvulkan.so")

	info, err := gq.Query()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mutation: remove `info.ComputeOK = true` in queryLinux's DRM branch -> fails.
	if !info.ComputeOK {
		t.Error("expected ComputeOK true after successful DRM probe")
	}
	// Mutation: change `info.Vendor = vendor` in probeDRM to "" -> fails.
	if info.Vendor != "0x1002" {
		t.Errorf("expected vendor 0x1002, got %q", info.Vendor)
	}
	// Mutation: change `info.Name = strings.TrimPrefix(line, "DRIVER=")` -> fails.
	if info.Name != "amdgpu" {
		t.Errorf("expected name amdgpu, got %q", info.Name)
	}
	// Mutation: drop the `info.MemoryBytes = v` assignment -> fails.
	if info.MemoryBytes != 8589934592 {
		t.Errorf("expected VRAM 8589934592, got %d", info.MemoryBytes)
	}
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
