package console

import (
	"os"
	"path/filepath"
	"testing"
)

// probeDRM and queryLinux are OS-independent (the GOOS gate lives in Query).
// With the injectable drmBasePath they can be exercised on any platform using
// a faked sysfs tree, proving real parsing of vendor / driver / VRAM.

func newFakeDRM(t *testing.T) (gq *GPUQuerier, drmBase string) {
	t.Helper()
	drmBase = filepath.Join(t.TempDir(), "drm")
	gq = NewGPUQuerier()
	gq.drmBasePath = drmBase
	// Point the vulkan loader somewhere absent so the fallback never fires
	// unless a test wants it to.
	gq.vulkanLoaderPath = filepath.Join(t.TempDir(), "absent-libvulkan.so")
	return gq, drmBase
}

func writeCard(t *testing.T, drmBase, card string, files map[string]string) {
	t.Helper()
	dev := filepath.Join(drmBase, card, "device")
	if err := os.MkdirAll(dev, 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dev, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProbeDRM_ParsesVendorDriverVRAM(t *testing.T) {
	gq, drmBase := newFakeDRM(t)
	writeCard(t, drmBase, "card0", map[string]string{
		"vendor":              "0x1002\n",
		"uevent":              "MAJOR=226\nDRIVER=amdgpu\nPCI_ID=1002:163F\n",
		"mem_info_vram_total": "17163091968\n",
	})

	info := &GPUInfo{}
	if err := gq.probeDRM(info); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mutation: change info.Vendor = vendor to info.Vendor = "" -> fails.
	if info.Vendor != "0x1002" {
		t.Errorf("expected vendor 0x1002, got %q", info.Vendor)
	}
	// Mutation: change strings.TrimPrefix(line, "DRIVER=") to "" -> fails.
	if info.Name != "amdgpu" {
		t.Errorf("expected driver amdgpu, got %q", info.Name)
	}
	// Mutation: drop the ParseInt assignment (info.MemoryBytes = v) -> fails.
	if info.MemoryBytes != 17163091968 {
		t.Errorf("expected VRAM 17163091968, got %d", info.MemoryBytes)
	}
}

func TestProbeDRM_SkipsNonCardEntries(t *testing.T) {
	gq, drmBase := newFakeDRM(t)
	// "by-path" sorts before "card0" in ReadDir order; it is NOT a card and
	// must be skipped. It carries a DRIVER= line, so if the card guard is
	// removed it would set Name and break the loop early, leaving the wrong
	// vendor 0xBADD before card0 is ever reached.
	writeCard(t, drmBase, "by-path", map[string]string{
		"vendor": "0xBADD\n",
		"uevent": "DRIVER=fakedrv\n",
	})
	writeCard(t, drmBase, "card0", map[string]string{
		"vendor": "0x1002\n",
		"uevent": "DRIVER=amdgpu\n",
	})

	info := &GPUInfo{}
	if err := gq.probeDRM(info); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mutation: remove the `strings.HasPrefix(entry.Name(), "card")` guard ->
	// by-path (0xBADD) clobbers the vendor and this fails.
	if info.Vendor != "0x1002" {
		t.Errorf("expected only card* parsed, got vendor %q", info.Vendor)
	}
}

func TestProbeDRM_NoDRMDir(t *testing.T) {
	gq, _ := newFakeDRM(t) // drmBase never created
	info := &GPUInfo{}
	if err := gq.probeDRM(info); err == nil {
		t.Fatal("expected error when DRM base directory is absent")
	}
}

func TestProbeDRM_CardWithoutVendor(t *testing.T) {
	gq, drmBase := newFakeDRM(t)
	// card0 exists but has no readable "vendor" file -> os.ReadFile fails,
	// the loop `continue`s, leaving Vendor empty -> "no DRM device found".
	if err := os.MkdirAll(filepath.Join(drmBase, "card0", "device"), 0755); err != nil {
		t.Fatal(err)
	}
	info := &GPUInfo{}
	err := gq.probeDRM(info)
	// Mutation: change the final `if info.Vendor == ""` check to always return
	// nil -> this expectation of an error fails.
	if err == nil {
		t.Fatal("expected error when no card exposes a vendor")
	}
}

func TestQueryLinux_DRMSuccessSetsComputeOK(t *testing.T) {
	gq, drmBase := newFakeDRM(t)
	writeCard(t, drmBase, "card0", map[string]string{
		"vendor": "0x1002\n",
		"uevent": "DRIVER=amdgpu\n",
	})

	info, err := gq.queryLinux()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mutation: remove `info.ComputeOK = true` in the DRM-success branch -> fails.
	if !info.ComputeOK {
		t.Error("expected ComputeOK true after successful DRM probe")
	}
	if info.Name != "amdgpu" {
		t.Errorf("expected name amdgpu, got %q", info.Name)
	}
}

func TestQueryLinux_FallbackToVulkan(t *testing.T) {
	gq, _ := newFakeDRM(t) // no DRM tree -> probeDRM fails
	loader := filepath.Join(t.TempDir(), "libvulkan.so")
	if err := os.WriteFile(loader, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	gq.vulkanLoaderPath = loader

	info, err := gq.queryLinux()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mutation: change info.Name = "vulkan-capable" to "" -> the final
	// `if info.Name == ""` returns an error and this fails.
	if info.Name != "vulkan-capable" {
		t.Errorf("expected vulkan-capable fallback, got %q", info.Name)
	}
	if !info.ComputeOK {
		t.Error("expected ComputeOK true via vulkan fallback")
	}
}

func TestQueryLinux_NoGPUNoVulkan(t *testing.T) {
	gq, _ := newFakeDRM(t) // no DRM, vulkan loader absent
	info, err := gq.queryLinux()
	// Mutation: change `return nil, fmt.Errorf("no GPU detected")` to
	// `return info, nil` -> this expectation of an error fails.
	if err == nil {
		t.Fatalf("expected error when no GPU and no vulkan loader, got info %+v", info)
	}
}

func TestHasVulkanCompute_AbsentLoader(t *testing.T) {
	gq := NewGPUQuerier()
	gq.vulkanLoaderPath = filepath.Join(t.TempDir(), "nope.so")
	// On non-linux this is false via the GOOS guard; on linux it is false
	// because the file is absent. Either way the contract is "false".
	if gq.HasVulkanCompute() {
		t.Error("expected false when loader file is absent")
	}
}
