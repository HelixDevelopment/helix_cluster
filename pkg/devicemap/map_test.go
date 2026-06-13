package devicemap

import (
	"testing"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/HelixDevelopment/helix_cluster/pkg/device"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// representativeDescriptor returns a DeviceDescriptor with every field set to
// a non-zero representative value so mutations are detectable.
func representativeDescriptor() device.DeviceDescriptor {
	return device.DeviceDescriptor{
		ID:               "dev-abc-123",
		Arch:             device.ArchARM64,
		CPUCores:         16,
		MemoryBytes:      32 * 1024 * 1024 * 1024, // 32 GiB
		GPUCount:         2,
		GPUVRAMBytes:     24 * 1024 * 1024 * 1024,  // 24 GiB total across 2 GPUs
		NPUTops:          12.5,                     // device-only — no pb.Node equivalent
		PowerBudgetWatts: 65.0,                     // device-only — no pb.Node equivalent
		HasBattery:       true,                     // device-only — no pb.Node equivalent
		Trust:            device.TrustConfidential, // device-only — no pb.Node equivalent
	}
}

// mappedFieldsEqual returns true iff the MAPPED fields of d equal expected.
// It only checks the fields that survive a round-trip through pb.Node.
func mappedFieldsEqual(t *testing.T, got, want device.DeviceDescriptor) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID: got %q, want %q", got.ID, want.ID)
	}
	if got.Arch != want.Arch {
		t.Errorf("Arch: got %q, want %q", got.Arch, want.Arch)
	}
	if got.CPUCores != want.CPUCores {
		t.Errorf("CPUCores: got %d, want %d", got.CPUCores, want.CPUCores)
	}
	if got.MemoryBytes != want.MemoryBytes {
		t.Errorf("MemoryBytes: got %d, want %d", got.MemoryBytes, want.MemoryBytes)
	}
	if got.GPUCount != want.GPUCount {
		t.Errorf("GPUCount: got %d, want %d", got.GPUCount, want.GPUCount)
	}
	if got.GPUVRAMBytes != want.GPUVRAMBytes {
		t.Errorf("GPUVRAMBytes: got %d, want %d", got.GPUVRAMBytes, want.GPUVRAMBytes)
	}
}

// ── Test 1: Descriptor → Node → Descriptor identity over mapped fields ───────
//
// Build a Descriptor with representative values; convert to Node; convert back;
// assert every MAPPED field is unchanged.  A mutation that drops or mis-maps
// any of those fields breaks this test — proving the mapping is real.

func TestRoundTripMappedFields_DescriptorViaNode(t *testing.T) {
	orig := representativeDescriptor()

	node := DescriptorToNode(orig)
	if node == nil {
		t.Fatal("DescriptorToNode returned nil")
	}

	got := NodeToDescriptor(node)

	mappedFieldsEqual(t, got, orig)
}

// ── Test 2: Honest loss — device-only fields are NOT recovered ───────────────
//
// After a round-trip through pb.Node the device-only fields (NPUTops,
// PowerBudgetWatts, HasBattery, Trust) must be zero/default.  If the mapping
// were to fabricate values for these fields (e.g. by peeking at the original),
// this test would fail.  A future proto field that genuinely carries one of
// these should be added to Test 1 and removed from Test 2.

func TestRoundTripHonestLoss_DeviceOnlyFieldsAreDropped(t *testing.T) {
	orig := representativeDescriptor()
	// Confirm our fixture actually has non-zero device-only fields so the test
	// is not trivially vacuous.
	if orig.NPUTops == 0 {
		t.Fatal("fixture precondition: NPUTops must be non-zero")
	}
	if orig.PowerBudgetWatts == 0 {
		t.Fatal("fixture precondition: PowerBudgetWatts must be non-zero")
	}
	if !orig.HasBattery {
		t.Fatal("fixture precondition: HasBattery must be true")
	}
	if orig.Trust == device.TrustUntrusted {
		t.Fatal("fixture precondition: Trust must be non-zero (not TrustUntrusted)")
	}

	node := DescriptorToNode(orig)
	got := NodeToDescriptor(node)

	// These fields MUST be lost — the mapping is honest about them.
	if got.NPUTops != 0 {
		t.Errorf("ANTI-BLUFF: NPUTops was NOT lost after round-trip; got %v, want 0", got.NPUTops)
	}
	if got.PowerBudgetWatts != 0 {
		t.Errorf("ANTI-BLUFF: PowerBudgetWatts was NOT lost after round-trip; got %v, want 0", got.PowerBudgetWatts)
	}
	if got.HasBattery {
		t.Errorf("ANTI-BLUFF: HasBattery was NOT lost after round-trip; got true, want false")
	}
	if got.Trust != device.TrustUntrusted {
		t.Errorf("ANTI-BLUFF: Trust was NOT lost after round-trip; got %v, want TrustUntrusted(0)", got.Trust)
	}
}

// ── Test 3: Node → Descriptor → Node identity over mapped fields ─────────────
//
// Build a pb.Node directly, convert to Descriptor, convert back to Node, and
// assert the mapped fields survived both hops unchanged.

func TestRoundTripMappedFields_NodeViaDescriptor(t *testing.T) {
	orig := &helixv1.Node{
		Id: "node-xyz-456",
		Resources: &helixv1.NodeResources{
			Cpu: &helixv1.CPUResources{
				Arch:  "amd64",
				Cores: 8,
			},
			Memory: &helixv1.MemoryResources{
				TotalBytes: 16 * 1024 * 1024 * 1024,
			},
			Gpus: []*helixv1.GPUResource{
				{TotalMemory: 8 * 1024 * 1024 * 1024},
				{TotalMemory: 8 * 1024 * 1024 * 1024},
			},
		},
	}

	d := NodeToDescriptor(orig)
	got := DescriptorToNode(d)

	// Id
	if got.GetId() != orig.GetId() {
		t.Errorf("Id: got %q, want %q", got.GetId(), orig.GetId())
	}
	// Arch
	if got.GetResources().GetCpu().GetArch() != orig.GetResources().GetCpu().GetArch() {
		t.Errorf("Cpu.Arch: got %q, want %q",
			got.GetResources().GetCpu().GetArch(),
			orig.GetResources().GetCpu().GetArch())
	}
	// Cores
	if got.GetResources().GetCpu().GetCores() != orig.GetResources().GetCpu().GetCores() {
		t.Errorf("Cpu.Cores: got %d, want %d",
			got.GetResources().GetCpu().GetCores(),
			orig.GetResources().GetCpu().GetCores())
	}
	// Memory.TotalBytes
	if got.GetResources().GetMemory().GetTotalBytes() != orig.GetResources().GetMemory().GetTotalBytes() {
		t.Errorf("Memory.TotalBytes: got %d, want %d",
			got.GetResources().GetMemory().GetTotalBytes(),
			orig.GetResources().GetMemory().GetTotalBytes())
	}
	// GPU count
	if len(got.GetResources().GetGpus()) != len(orig.GetResources().GetGpus()) {
		t.Errorf("len(Gpus): got %d, want %d",
			len(got.GetResources().GetGpus()),
			len(orig.GetResources().GetGpus()))
	}
	// Total GPU VRAM
	var wantVRAM, gotVRAM int64
	for _, g := range orig.GetResources().GetGpus() {
		wantVRAM += g.GetTotalMemory()
	}
	for _, g := range got.GetResources().GetGpus() {
		gotVRAM += g.GetTotalMemory()
	}
	if gotVRAM != wantVRAM {
		t.Errorf("total GPU VRAM: got %d, want %d", gotVRAM, wantVRAM)
	}
}

// ── Additional edge-case tests ────────────────────────────────────────────────

// TestNilNodeToDescriptor confirms that NodeToDescriptor(nil) returns a safe
// zero-value Descriptor without panicking.
func TestNilNodeToDescriptor(t *testing.T) {
	d := NodeToDescriptor(nil)
	if d.ID != "" || d.CPUCores != 0 || d.MemoryBytes != 0 || d.GPUCount != 0 {
		t.Errorf("expected zero Descriptor from nil Node, got %+v", d)
	}
}

// TestZeroGPUCount confirms that a Descriptor with GPUCount==0 produces an
// empty Gpus slice (not a panic).
func TestZeroGPUCount(t *testing.T) {
	d := device.DeviceDescriptor{
		ID:           "cpu-only",
		Arch:         device.ArchAMD64,
		CPUCores:     4,
		MemoryBytes:  8 * 1024 * 1024 * 1024,
		GPUCount:     0,
		GPUVRAMBytes: 0,
	}
	n := DescriptorToNode(d)
	if len(n.GetResources().GetGpus()) != 0 {
		t.Errorf("expected 0 GPU entries for GPUCount=0, got %d", len(n.GetResources().GetGpus()))
	}

	back := NodeToDescriptor(n)
	if back.GPUCount != 0 {
		t.Errorf("GPUCount: got %d, want 0", back.GPUCount)
	}
	if back.GPUVRAMBytes != 0 {
		t.Errorf("GPUVRAMBytes: got %d, want 0", back.GPUVRAMBytes)
	}
}

// TestGPUVRAMEvenDistribution verifies that the aggregate VRAM is preserved
// across an even distribution and that the per-GPU entries carry equal shares.
func TestGPUVRAMEvenDistribution(t *testing.T) {
	const count = 4
	const totalVRAM = 32 * 1024 * 1024 * 1024 // 32 GiB, divisible by 4

	d := device.DeviceDescriptor{
		GPUCount:     count,
		GPUVRAMBytes: totalVRAM,
	}
	n := DescriptorToNode(d)
	gpus := n.GetResources().GetGpus()
	if len(gpus) != count {
		t.Fatalf("GPU count: got %d, want %d", len(gpus), count)
	}

	perGPU := int64(totalVRAM / count)
	for i, g := range gpus {
		if g.GetTotalMemory() != perGPU {
			t.Errorf("GPU[%d].TotalMemory: got %d, want %d", i, g.GetTotalMemory(), perGPU)
		}
	}
}

// TestGPUVRAMUneven verifies that when VRAM is not evenly divisible the
// aggregate is still perfectly preserved (remainder lands on the last GPU).
func TestGPUVRAMUneven(t *testing.T) {
	const count = 3
	const totalVRAM = 10 * 1024 * 1024 * 1024 // 10 GiB, not divisible by 3

	d := device.DeviceDescriptor{
		GPUCount:     count,
		GPUVRAMBytes: totalVRAM,
	}
	n := DescriptorToNode(d)

	var got int64
	for _, g := range n.GetResources().GetGpus() {
		got += g.GetTotalMemory()
	}
	if got != totalVRAM {
		t.Errorf("total VRAM preserved: got %d, want %d", got, totalVRAM)
	}

	back := NodeToDescriptor(n)
	if back.GPUVRAMBytes != totalVRAM {
		t.Errorf("GPUVRAMBytes round-trip: got %d, want %d", back.GPUVRAMBytes, totalVRAM)
	}
}
