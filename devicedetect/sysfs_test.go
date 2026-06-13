package devicedetect

import "testing"

// TestDetectGPUsFromSysfs_Fixture proves the Linux sysfs parser against a
// fixture DRM tree (testdata/sysfs). This runs on ANY OS — including the macOS
// dev host — so the Linux detection logic is unit-proven here even though the
// live /sys/class/drm probe only executes on a real Linux box.
//
// The fixture contains, in order:
//
//	card0      NVIDIA discrete  vendor 0x10de device 0x2684
//	card0-DP-1 connector node   (must be ignored)
//	card1      AMD              vendor 0x1002 device 0x744c
//	card2      Intel iGPU       vendor 0x8086 device 0x9bc8
//	card3      ARM Mali         of_node/compatible = arm,mali-g78
func TestDetectGPUsFromSysfs_Fixture(t *testing.T) {
	gpus, err := detectGPUsFromSysfs("testdata/sysfs")
	if err != nil {
		t.Fatalf("detectGPUsFromSysfs: %v", err)
	}

	// 4 cards (card0..card3); the card0-DP-1 connector must be skipped.
	if len(gpus) != 4 {
		t.Fatalf("want 4 GPUs (NVIDIA, AMD, Intel, ARM), got %d: %+v", len(gpus), gpus)
	}

	want := []struct {
		vendor, api, pciVendor, pciDevice string
	}{
		{"NVIDIA", "CUDA", "0x10de", "0x2684"},
		{"AMD", "ROCm", "0x1002", "0x744c"},
		{"Intel", "Vulkan", "0x8086", "0x9bc8"},
		{"ARM", "Vulkan", "", ""}, // device-tree, no PCI ids
	}

	for i, w := range want {
		g := gpus[i]
		if g.Vendor != w.vendor {
			t.Errorf("gpu[%d] vendor: got %q want %q", i, g.Vendor, w.vendor)
		}
		if g.API != w.api {
			t.Errorf("gpu[%d] api: got %q want %q", i, g.API, w.api)
		}
		if g.PCIVendorID != w.pciVendor {
			t.Errorf("gpu[%d] PCIVendorID: got %q want %q", i, g.PCIVendorID, w.pciVendor)
		}
		if g.PCIDeviceID != w.pciDevice {
			t.Errorf("gpu[%d] PCIDeviceID: got %q want %q", i, g.PCIDeviceID, w.pciDevice)
		}
		if g.Model == "" {
			t.Errorf("gpu[%d] empty model", i)
		}
	}

	// ARM Mali model must be extracted from the device-tree compatible string.
	if got := gpus[3].Model; got != "Mali-g78" {
		t.Errorf("ARM Mali model: got %q want %q", got, "Mali-g78")
	}
	t.Logf("parsed %d GPUs from fixture: NVIDIA=%q AMD=%q Intel=%q ARM=%q",
		len(gpus), gpus[0].Model, gpus[1].Model, gpus[2].Model, gpus[3].Model)
}

// TestPCIVendorName covers the PCI vendor ID mapping used by the sysfs parser.
func TestPCIVendorName(t *testing.T) {
	cases := map[string]string{
		"0x10de": "NVIDIA",
		"10de":   "NVIDIA",
		"0x1002": "AMD",
		"0x8086": "Intel",
		"0x106b": "Apple",
		"0x13b5": "ARM",
		"0xdead": "",
	}
	for in, want := range cases {
		if got := pciVendorName(in); got != want {
			t.Errorf("pciVendorName(%q)=%q want %q", in, got, want)
		}
	}
}

// TestDetectGPUsFromSysfs_NoSubsystem returns no GPUs and no error when the
// DRM root is absent (headless host without a GPU).
func TestDetectGPUsFromSysfs_NoSubsystem(t *testing.T) {
	gpus, err := detectGPUsFromSysfs("testdata/does-not-exist")
	if err != nil {
		t.Fatalf("expected nil error for missing DRM root, got %v", err)
	}
	if len(gpus) != 0 {
		t.Fatalf("expected 0 GPUs, got %d", len(gpus))
	}
}
