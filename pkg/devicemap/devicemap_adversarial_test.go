package devicemap

import (
	"reflect"
	"testing"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/HelixDevelopment/helix_cluster/pkg/device"
)

// This file pins the devicemap conversion contract with adversarial,
// mutation-verified probes. It complements map_test.go by adding:
//
//   - EXPLICIT per-field correspondence against a hand-built expected Node
//     (a symmetric field swap can round-trip and still be wrong; this bites it).
//   - A fully-distinct-value Descriptor round-trip (no value reused, so a
//     field swap in either direction is detectable).
//   - The partial-Node edges NodeToDescriptor must tolerate (Resources/Cpu/
//     Memory sub-messages nil; GPUs present but other sub-messages absent).
//   - Empty-string / zero-value, single-GPU-carries-all-VRAM, negative-cores
//     clamp, and large-value (no int32 wrap) edges.

// ── A. Explicit per-field correspondence: DescriptorToNode ───────────────────
//
// Hand-build the EXACT Node we expect from a Descriptor whose every mapped
// field holds a DISTINCT value. reflect.DeepEqual against the hand-built Node
// pins each field to its correct destination — a swap (e.g. Cores read from
// MemoryBytes) produces a different Node and fails here, even if it would
// happen to round-trip.
func TestDescriptorToNode_ExactFieldCorrespondence(t *testing.T) {
	d := device.DeviceDescriptor{
		ID:           "id-7",           // -> Node.Id
		Arch:         device.ArchAMD64, // -> Cpu.Arch == "amd64"
		CPUCores:     11,               // -> Cpu.Cores (distinct from everything)
		MemoryBytes:  9_000_000_001,    // -> Memory.TotalBytes (distinct)
		GPUCount:     1,                // -> len(Gpus) == 1
		GPUVRAMBytes: 7_000_000_003,    // -> Gpus[0].TotalMemory (single GPU carries all)
		// device-only fields set but must NOT influence the Node:
		NPUTops:          3.5,
		PowerBudgetWatts: 42.0,
		HasBattery:       true,
		Trust:            device.TrustConfidential,
	}

	want := &helixv1.Node{
		Id: "id-7",
		Resources: &helixv1.NodeResources{
			Cpu: &helixv1.CPUResources{
				Arch:  "amd64",
				Cores: 11,
			},
			Memory: &helixv1.MemoryResources{
				TotalBytes: 9_000_000_001,
			},
			Gpus: []*helixv1.GPUResource{
				{TotalMemory: 7_000_000_003},
			},
		},
	}

	got := DescriptorToNode(d)

	// Field-by-field so a failure names the offending field precisely.
	if got.GetId() != want.GetId() {
		t.Errorf("Id mis-mapped: got %q want %q", got.GetId(), want.GetId())
	}
	if a := got.GetResources().GetCpu().GetArch(); a != "amd64" {
		t.Errorf("Cpu.Arch mis-mapped: got %q want %q", a, "amd64")
	}
	if c := got.GetResources().GetCpu().GetCores(); c != 11 {
		t.Errorf("Cpu.Cores mis-mapped: got %d want 11 (a swap with Memory would show 9000000001)", c)
	}
	if m := got.GetResources().GetMemory().GetTotalBytes(); m != 9_000_000_001 {
		t.Errorf("Memory.TotalBytes mis-mapped: got %d want 9000000001", m)
	}
	if n := len(got.GetResources().GetGpus()); n != 1 {
		t.Fatalf("len(Gpus) mis-mapped: got %d want 1", n)
	}
	if v := got.GetResources().GetGpus()[0].GetTotalMemory(); v != 7_000_000_003 {
		t.Errorf("Gpus[0].TotalMemory mis-mapped: got %d want 7000000003 "+
			"(reading VRAM from GPUCount would show 1)", v)
	}

	// Whole-structure pin: catches any field landing somewhere unexpected.
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hand-built Node mismatch:\n got = %+v\nwant = %+v", got, want)
	}
}

// ── B. Explicit per-field correspondence: NodeToDescriptor ───────────────────
//
// Hand-build the EXACT Descriptor expected from a Node with distinct field
// values. Pins each Node field to its Descriptor destination.
func TestNodeToDescriptor_ExactFieldCorrespondence(t *testing.T) {
	n := &helixv1.Node{
		Id: "node-9",
		Resources: &helixv1.NodeResources{
			Cpu: &helixv1.CPUResources{
				Arch:  "arm64",
				Cores: 13,
			},
			Memory: &helixv1.MemoryResources{
				TotalBytes: 5_000_000_007,
			},
			Gpus: []*helixv1.GPUResource{
				{TotalMemory: 1_000_000_001},
				{TotalMemory: 2_000_000_002},
				{TotalMemory: 3_000_000_003}, // sum = 6_000_000_006, count = 3
			},
		},
	}

	want := device.DeviceDescriptor{
		ID:           "node-9",
		Arch:         device.ArchARM64,
		CPUCores:     13,
		MemoryBytes:  5_000_000_007,
		GPUCount:     3,
		GPUVRAMBytes: 6_000_000_006,
		// device-only fields MUST be zero — unrecoverable from a Node.
	}

	got := NodeToDescriptor(n)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("NodeToDescriptor field correspondence broken:\n got = %+v\nwant = %+v", got, want)
	}
	// Pin the two count/sum derivations explicitly (a swap would still differ).
	if got.GPUCount != 3 {
		t.Errorf("GPUCount must be len(Gpus)=3, got %d (reading from VRAM sum would be huge)", got.GPUCount)
	}
	if got.GPUVRAMBytes != 6_000_000_006 {
		t.Errorf("GPUVRAMBytes must be sum=6000000006, got %d (reading from count would be 3)", got.GPUVRAMBytes)
	}
}

// ── C. Distinct-value full round-trip (Descriptor -> Node -> Descriptor) ──────
//
// Every mapped field holds a value distinct from every other field, so a
// field swap anywhere in either direction corrupts the result and fails the
// per-field comparison. Uses GPUCount==1 so GPUVRAMBytes is exactly preserved
// (no even-distribution remainder ambiguity), pinning VRAM identity tightly.
func TestRoundTrip_DistinctValues_ExactMappedFields(t *testing.T) {
	orig := device.DeviceDescriptor{
		ID:           "distinct-id",
		Arch:         device.ArchARM64,
		CPUCores:     17,
		MemoryBytes:  123_456_789_012,
		GPUCount:     1,
		GPUVRAMBytes: 98_765_432_109,
	}

	got := NodeToDescriptor(DescriptorToNode(orig))

	if got.ID != orig.ID {
		t.Errorf("ID: got %q want %q", got.ID, orig.ID)
	}
	if got.Arch != orig.Arch {
		t.Errorf("Arch: got %q want %q", got.Arch, orig.Arch)
	}
	if got.CPUCores != orig.CPUCores {
		t.Errorf("CPUCores: got %d want %d", got.CPUCores, orig.CPUCores)
	}
	if got.MemoryBytes != orig.MemoryBytes {
		t.Errorf("MemoryBytes: got %d want %d", got.MemoryBytes, orig.MemoryBytes)
	}
	if got.GPUCount != orig.GPUCount {
		t.Errorf("GPUCount: got %d want %d", got.GPUCount, orig.GPUCount)
	}
	if got.GPUVRAMBytes != orig.GPUVRAMBytes {
		t.Errorf("GPUVRAMBytes: got %d want %d", got.GPUVRAMBytes, orig.GPUVRAMBytes)
	}
}

// ── D. Partial Node: nil sub-messages must not panic and must zero-default ───
//
// NodeToDescriptor walks Resources -> Cpu/Memory/Gpus with nil guards. These
// cases assert no panic and correct zero defaulting when sub-messages are
// absent. Dropping a nil guard in production turns these into panics.
func TestNodeToDescriptor_PartialNode_NilSubMessages(t *testing.T) {
	cases := []struct {
		name string
		node *helixv1.Node
		want device.DeviceDescriptor
	}{
		{
			name: "ResourcesNil",
			node: &helixv1.Node{Id: "r-nil"},
			want: device.DeviceDescriptor{ID: "r-nil"},
		},
		{
			name: "CpuAndMemoryNil_GpusPresent",
			node: &helixv1.Node{
				Id: "cpu-mem-nil",
				Resources: &helixv1.NodeResources{
					// Cpu nil, Memory nil
					Gpus: []*helixv1.GPUResource{
						{TotalMemory: 100},
						{TotalMemory: 200},
					},
				},
			},
			// Arch/Cores/Memory default to zero; GPU count+sum still computed.
			want: device.DeviceDescriptor{
				ID:           "cpu-mem-nil",
				GPUCount:     2,
				GPUVRAMBytes: 300,
			},
		},
		{
			name: "GpusNil_CpuMemoryPresent",
			node: &helixv1.Node{
				Id: "gpus-nil",
				Resources: &helixv1.NodeResources{
					Cpu:    &helixv1.CPUResources{Arch: "amd64", Cores: 4},
					Memory: &helixv1.MemoryResources{TotalBytes: 1024},
				},
			},
			want: device.DeviceDescriptor{
				ID:          "gpus-nil",
				Arch:        device.ArchAMD64,
				CPUCores:    4,
				MemoryBytes: 1024,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NodeToDescriptor(tc.node) // must not panic
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("partial node %s:\n got = %+v\nwant = %+v", tc.name, got, tc.want)
			}
		})
	}
}

// ── E. Empty / zero Descriptor round-trips to empty / zero ───────────────────
func TestRoundTrip_EmptyDescriptor(t *testing.T) {
	var orig device.DeviceDescriptor // all zero, empty ID, empty Arch

	node := DescriptorToNode(orig)
	if node == nil {
		t.Fatal("DescriptorToNode(zero) returned nil")
	}
	if node.GetId() != "" {
		t.Errorf("empty ID corrupted: got %q", node.GetId())
	}
	if node.GetResources().GetCpu().GetArch() != "" {
		t.Errorf("empty Arch corrupted: got %q", node.GetResources().GetCpu().GetArch())
	}
	if n := len(node.GetResources().GetGpus()); n != 0 {
		t.Errorf("zero GPUCount must yield no GPU entries, got %d", n)
	}

	got := NodeToDescriptor(node)
	if !reflect.DeepEqual(got, orig) {
		t.Errorf("empty round-trip not identity:\n got = %+v\nwant = %+v", got, orig)
	}
}

// ── F. Single GPU carries all VRAM exactly (no distribution loss) ────────────
func TestSingleGPU_CarriesAllVRAMExactly(t *testing.T) {
	const vram = 17_179_869_184 // 16 GiB, odd-ish to defeat accidental even split
	d := device.DeviceDescriptor{GPUCount: 1, GPUVRAMBytes: vram}

	gpus := DescriptorToNode(d).GetResources().GetGpus()
	if len(gpus) != 1 {
		t.Fatalf("want 1 GPU, got %d", len(gpus))
	}
	if gpus[0].GetTotalMemory() != vram {
		t.Errorf("single GPU must carry all VRAM: got %d want %d", gpus[0].GetTotalMemory(), vram)
	}
}

// ── G. Negative CPUCores clamps to 0 (no int32 wrap), other fields intact ────
func TestNegativeCPUCores_ClampsToZero(t *testing.T) {
	d := device.DeviceDescriptor{
		ID:       "neg",
		Arch:     device.ArchAMD64,
		CPUCores: -5,
	}
	got := DescriptorToNode(d).GetResources().GetCpu().GetCores()
	if got != 0 {
		t.Errorf("negative CPUCores must clamp to 0, got %d", got)
	}
}

// ── H. Large CPUCores does not wrap to negative int32 ────────────────────────
//
// clampInt32 caps values above math.MaxInt32; a value past the boundary must
// remain a large non-negative int32, never wrap negative.
func TestLargeCPUCores_CapsWithoutWrap(t *testing.T) {
	const big = 0x7fffffff + 100 // beyond int32 max
	d := device.DeviceDescriptor{CPUCores: big}
	got := DescriptorToNode(d).GetResources().GetCpu().GetCores()
	if got < 0 {
		t.Fatalf("CPUCores wrapped negative: got %d", got)
	}
	if got != 0x7fffffff {
		t.Errorf("CPUCores must cap at int32 max, got %d want %d", got, int32(0x7fffffff))
	}
}

// ── I. GPUCount>1 even VRAM split preserves the aggregate sum exactly ────────
//
// With multiple GPUs the per-GPU breakdown is intentionally lossy, but the
// SUM (which is what NodeToDescriptor recovers) must equal the original VRAM
// regardless of divisibility — pin both directions.
func TestMultiGPU_AggregateVRAMExactBothDirections(t *testing.T) {
	for _, tc := range []struct {
		count int
		vram  int64
	}{
		{count: 2, vram: 24 * 1024 * 1024 * 1024},
		{count: 3, vram: 10_000_000_001}, // not divisible by 3
		{count: 7, vram: 1},              // remainder dominates
	} {
		d := device.DeviceDescriptor{GPUCount: tc.count, GPUVRAMBytes: tc.vram}
		node := DescriptorToNode(d)

		if got := len(node.GetResources().GetGpus()); got != tc.count {
			t.Errorf("count=%d: GPU entries got %d want %d", tc.count, got, tc.count)
		}
		var sum int64
		for _, g := range node.GetResources().GetGpus() {
			sum += g.GetTotalMemory()
		}
		if sum != tc.vram {
			t.Errorf("count=%d: Node GPU VRAM sum got %d want %d", tc.count, sum, tc.vram)
		}

		back := NodeToDescriptor(node)
		if back.GPUCount != tc.count {
			t.Errorf("count=%d: round-trip GPUCount got %d want %d", tc.count, back.GPUCount, tc.count)
		}
		if back.GPUVRAMBytes != tc.vram {
			t.Errorf("count=%d: round-trip GPUVRAMBytes got %d want %d", tc.count, back.GPUVRAMBytes, tc.vram)
		}
	}
}
