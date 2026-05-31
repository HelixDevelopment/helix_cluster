package resources

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCard creates a fake DRM card device directory under base:
//
//	<base>/class/drm/<card>/device/{vendor,device,mem_info_vram_total}
//
// Any of vendor/device/vram passed as "" is omitted, so tests can model a card
// that is missing a file. This builds a tree shaped exactly like real sysfs,
// letting the parser be exercised against real bytes on any OS.
func writeCard(t *testing.T, base, card, vendor, device, vram string) {
	t.Helper()
	dev := filepath.Join(base, DRMClassDir, card, "device")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dev, err)
	}
	write := func(name, content string) {
		if content == "" {
			return
		}
		if err := os.WriteFile(filepath.Join(dev, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", dev, name, err)
		}
	}
	write("vendor", vendor)
	write("device", device)
	write("mem_info_vram_total", vram)
}

// TestParseDRMTree_TwoCards is the headline test: a fake tree with exactly 2
// cards must yield exactly 2 GPUs with the exact parsed vendor/device/VRAM.
//
// Mutation that this kills:
//   - hardcoding count=1 (e.g. returning gpus[:1] or info.Count = 1) fails the
//     "len == 2" / "Count == 2" assertions.
//   - ignoring mem_info_vram_total (e.g. VRAMBytes left 0) fails the exact
//     per-card VRAMBytes and the summed Memory(MiB) assertions.
func TestParseDRMTree_TwoCards(t *testing.T) {
	base := t.TempDir()
	// card0: AMD Radeon, 8 GiB VRAM (8589934592 bytes).
	writeCard(t, base, "card0", "0x1002\n", "0x73bf\n", "8589934592\n")
	// card1: NVIDIA, 16 GiB VRAM (17179869184 bytes).
	writeCard(t, base, "card1", "0x10de\n", "0x2204\n", "17179869184\n")
	// Decoy entries that MUST NOT be counted as GPUs.
	if err := os.MkdirAll(filepath.Join(base, DRMClassDir, "renderD128"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, DRMClassDir, "card0-DP-1"), 0o755); err != nil {
		t.Fatal(err)
	}

	devs, err := ParseDRMTree(base)
	if err != nil {
		t.Fatalf("ParseDRMTree: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("expected exactly 2 GPUs, got %d: %+v", len(devs), devs)
	}

	// Deterministic order (sorted): card0 then card1.
	if devs[0].Card != "card0" {
		t.Errorf("devs[0].Card = %q, want card0", devs[0].Card)
	}
	if devs[0].VendorID != "0x1002" {
		t.Errorf("devs[0].VendorID = %q, want 0x1002", devs[0].VendorID)
	}
	if devs[0].DeviceID != "0x73bf" {
		t.Errorf("devs[0].DeviceID = %q, want 0x73bf", devs[0].DeviceID)
	}
	if devs[0].VRAMBytes != 8589934592 {
		t.Errorf("devs[0].VRAMBytes = %d, want 8589934592", devs[0].VRAMBytes)
	}
	if devs[1].Card != "card1" {
		t.Errorf("devs[1].Card = %q, want card1", devs[1].Card)
	}
	if devs[1].VendorID != "0x10de" {
		t.Errorf("devs[1].VendorID = %q, want 0x10de", devs[1].VendorID)
	}
	if devs[1].DeviceID != "0x2204" {
		t.Errorf("devs[1].DeviceID = %q, want 0x2204", devs[1].DeviceID)
	}
	if devs[1].VRAMBytes != 17179869184 {
		t.Errorf("devs[1].VRAMBytes = %d, want 17179869184", devs[1].VRAMBytes)
	}

	// Aggregated GPUInfo: Count == 2, Memory == (8+16) GiB in MiB = 24576.
	info := GPUInfoFromDevices(devs)
	if info.Count != 2 {
		t.Errorf("info.Count = %d, want 2", info.Count)
	}
	if info.Memory != 24576 {
		t.Errorf("info.Memory(MiB) = %d, want 24576 (8GiB+16GiB)", info.Memory)
	}
	// Heterogeneous identities must be preserved distinctly.
	want := "0x1002:0x73bf,0x10de:0x2204"
	if info.Model != want {
		t.Errorf("info.Model = %q, want %q", info.Model, want)
	}
}

// TestParseDRMTree_EmptyTree: an empty tree (no class/drm at all) yields zero
// GPUs and no error.
//
// Mutation that this kills: returning an error for a missing DRM dir, or
// defaulting Count to 1, fails these assertions.
func TestParseDRMTree_EmptyTree(t *testing.T) {
	base := t.TempDir() // no class/drm created
	devs, err := ParseDRMTree(base)
	if err != nil {
		t.Fatalf("expected nil error for empty tree, got %v", err)
	}
	if len(devs) != 0 {
		t.Fatalf("expected 0 GPUs for empty tree, got %d", len(devs))
	}
	info := GPUInfoFromDevices(devs)
	if info.Count != 0 {
		t.Errorf("info.Count = %d, want 0", info.Count)
	}
	if info.Memory != 0 {
		t.Errorf("info.Memory = %d, want 0", info.Memory)
	}
}

// TestParseDRMTree_EmptyDRMDir: class/drm exists but contains no card dirs.
func TestParseDRMTree_EmptyDRMDir(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, DRMClassDir), 0o755); err != nil {
		t.Fatal(err)
	}
	devs, err := ParseDRMTree(base)
	if err != nil {
		t.Fatalf("ParseDRMTree: %v", err)
	}
	if len(devs) != 0 {
		t.Fatalf("expected 0 GPUs, got %d", len(devs))
	}
}

// TestParseDRMTree_MissingVendorSkipped: a card with no vendor file is skipped,
// not counted, and not fatal — the other valid card still enumerates.
//
// Mutation that this kills: counting a card without validating its vendor file
// (e.g. appending a GPUDevice before the vendor read succeeds) would make this
// return 2 instead of 1.
func TestParseDRMTree_MissingVendorSkipped(t *testing.T) {
	base := t.TempDir()
	// card0 has device + vram but NO vendor file.
	writeCard(t, base, "card0", "", "0x73bf\n", "8589934592\n")
	// card1 is fully valid.
	writeCard(t, base, "card1", "0x10de\n", "0x2204\n", "17179869184\n")

	devs, err := ParseDRMTree(base)
	if err != nil {
		t.Fatalf("ParseDRMTree: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("expected 1 GPU (card0 skipped for missing vendor), got %d: %+v", len(devs), devs)
	}
	if devs[0].Card != "card1" {
		t.Errorf("surviving card = %q, want card1", devs[0].Card)
	}
	if devs[0].VRAMBytes != 17179869184 {
		t.Errorf("VRAMBytes = %d, want 17179869184", devs[0].VRAMBytes)
	}
}

// TestParseDRMTree_GarbledVendorSkipped: a card whose vendor file is present but
// garbled (not "0x...." hex) is skipped, not silently coerced to 0x0.
//
// Mutation that this kills: dropping the hex validation in readVendorHex (just
// returning the raw trimmed string) would count card0 and make len == 2.
func TestParseDRMTree_GarbledVendorSkipped(t *testing.T) {
	base := t.TempDir()
	writeCard(t, base, "card0", "garbage\n", "0x73bf\n", "8589934592\n")
	writeCard(t, base, "card1", "0x10de\n", "0x2204\n", "17179869184\n")

	devs, err := ParseDRMTree(base)
	if err != nil {
		t.Fatalf("ParseDRMTree: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("expected 1 GPU (garbled vendor skipped), got %d: %+v", len(devs), devs)
	}
	if devs[0].VendorID != "0x10de" {
		t.Errorf("VendorID = %q, want 0x10de", devs[0].VendorID)
	}
}

// TestParseDRMTree_GarbledVRAMErrors: a card with valid vendor/device but a
// present-but-garbled mem_info_vram_total is a hard error (corrupt capacity must
// never be reported as zero).
//
// Mutation that this kills: swallowing the VRAM parse error and defaulting
// VRAMBytes to 0 would make this return nil error.
func TestParseDRMTree_GarbledVRAMErrors(t *testing.T) {
	base := t.TempDir()
	writeCard(t, base, "card0", "0x1002\n", "0x73bf\n", "not-a-number\n")

	_, err := ParseDRMTree(base)
	if err == nil {
		t.Fatal("expected error for garbled mem_info_vram_total, got nil")
	}
}

// TestParseDRMTree_NoVRAMFileIsZero: an integrated GPU with vendor/device but no
// mem_info_vram_total file is enumerated with VRAMBytes == 0 and no error.
func TestParseDRMTree_NoVRAMFileIsZero(t *testing.T) {
	base := t.TempDir()
	writeCard(t, base, "card0", "0x8086\n", "0x9a60\n", "") // no vram file

	devs, err := ParseDRMTree(base)
	if err != nil {
		t.Fatalf("ParseDRMTree: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(devs))
	}
	if devs[0].VRAMBytes != 0 {
		t.Errorf("VRAMBytes = %d, want 0 for integrated GPU without VRAM file", devs[0].VRAMBytes)
	}
}

// TestGPUInfoFromDevices_HomogeneousModel: two identical cards collapse to a
// "id x N" model and SUM their VRAM.
//
// Mutation that this kills: summing only one card's VRAM, or using max instead
// of sum, fails the Memory assertion.
func TestGPUInfoFromDevices_HomogeneousModel(t *testing.T) {
	devs := []GPUDevice{
		{Card: "card0", VendorID: "0x10de", DeviceID: "0x2204", VRAMBytes: 17179869184},
		{Card: "card1", VendorID: "0x10de", DeviceID: "0x2204", VRAMBytes: 17179869184},
	}
	info := GPUInfoFromDevices(devs)
	if info.Count != 2 {
		t.Errorf("Count = %d, want 2", info.Count)
	}
	if info.Model != "0x10de:0x2204 x 2" {
		t.Errorf("Model = %q, want %q", info.Model, "0x10de:0x2204 x 2")
	}
	if info.Memory != 32768 { // 2 * 16 GiB in MiB
		t.Errorf("Memory(MiB) = %d, want 32768", info.Memory)
	}
}

// TestDRMGPUReader_AtBase_IntegratesWithAggregator wires the reader (constructed
// over a fake tree) into a NodeAggregator and asserts the GPU field surfaces in
// the merged snapshot. NewDRMGPUReaderAt is available on all platforms so this
// runs on darwin too.
//
// Mutation that this kills: dropping the GPU branch in mergeResources, or the
// reader leaving GPU zero, fails the Count==2 snapshot assertion.
func TestDRMGPUReader_AtBase_IntegratesWithAggregator(t *testing.T) {
	base := t.TempDir()
	writeCard(t, base, "card0", "0x1002\n", "0x73bf\n", "8589934592\n")
	writeCard(t, base, "card1", "0x10de\n", "0x2204\n", "17179869184\n")

	r := NewDRMGPUReaderAt(base)
	res, err := r.Read("node-gpu")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.NodeID != "node-gpu" {
		t.Errorf("NodeID = %q, want node-gpu", res.NodeID)
	}
	if res.GPU.Count != 2 {
		t.Fatalf("GPU.Count = %d, want 2", res.GPU.Count)
	}
	if res.GPU.Memory != 24576 {
		t.Errorf("GPU.Memory(MiB) = %d, want 24576", res.GPU.Memory)
	}

	var _ Reader = r
	agg := NewNodeAggregator(5 * time.Minute)
	agg.RegisterReader(r)
	agg.RegisterNode("node-gpu")
	if err := agg.Collect(); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	snap, ok := agg.GetNode("node-gpu")
	if !ok {
		t.Fatal("expected node-gpu snapshot")
	}
	if snap.Resources.GPU.Count != 2 {
		t.Errorf("aggregated GPU.Count = %d, want 2", snap.Resources.GPU.Count)
	}
	if snap.Resources.GPU.Memory != 24576 {
		t.Errorf("aggregated GPU.Memory = %d, want 24576", snap.Resources.GPU.Memory)
	}
}

// TestDRMGPUReader_FallbackBaseEmpty: with an empty Base the reader reports zero
// GPUs and no error on every platform (the honest no-DRM answer). This documents
// the portable fallback contract.
func TestDRMGPUReader_FallbackBaseEmpty(t *testing.T) {
	// On non-Linux an empty Base returns zero GPUs directly; on Linux an empty
	// Base resolves to "/sys" which on CI may or may not have GPUs. To keep this
	// test deterministic on every platform, point at a nonexistent base so the
	// missing-DRM-dir path yields zero GPUs regardless of OS.
	r := NewDRMGPUReaderAt(filepath.Join(t.TempDir(), "nonexistent"))
	res, err := r.Read("n")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.GPU.Count != 0 {
		t.Errorf("GPU.Count = %d, want 0 for nonexistent base", res.GPU.Count)
	}
}
