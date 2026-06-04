// Package devicemap provides conversion helpers between [device.DeviceDescriptor]
// and [helixv1.Node] (the protobuf wire type used by the Helix control plane).
//
// # Mapping coverage
//
// Only fields that exist in BOTH types are transferred; the mapping is
// intentionally and honestly LOSSY.  Fields that exist only in
// [device.DeviceDescriptor] — NPUTops, PowerBudgetWatts, HasBattery, Trust —
// have no equivalent in pb.Node and are silently dropped on every
// Descriptor→Node conversion.  Callers that need those fields must not rely
// on a round-trip to preserve them.
//
// Mapped fields (identity over the round-trip):
//
//	DeviceDescriptor.ID          ↔  Node.Id
//	DeviceDescriptor.Arch        ↔  Node.Resources.Cpu.Arch  (string form)
//	DeviceDescriptor.CPUCores    ↔  Node.Resources.Cpu.Cores (int32)
//	DeviceDescriptor.MemoryBytes ↔  Node.Resources.Memory.TotalBytes
//	DeviceDescriptor.GPUCount    ↔  len(Node.Resources.Gpus)
//	DeviceDescriptor.GPUVRAMBytes ↔ sum(GPUResource.TotalMemory)
//	                                 distributed evenly across the GPU slice.
//
// The GPU VRAM distribution is evenly spread across GPUCount synthetic GPU
// entries; per-GPU VRAM breakdown is lost.  A single entry carries all VRAM
// when GPUCount==1; zero VRAM entries are omitted from the Gpus slice when
// GPUVRAMBytes==0 but GPUCount>0 (the count is still carried via slice
// length).
package devicemap

import (
	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/pkg/device"
)

// DescriptorToNode converts a [device.DeviceDescriptor] to a *[helixv1.Node].
//
// Only the fields listed in the package doc are transferred.  All device-only
// fields (NPUTops, PowerBudgetWatts, HasBattery, Trust) are dropped — the
// resulting Node carries no representation of them.  Node fields with no
// Descriptor equivalent (Hostname, IpAddresses, WgPubkey, SpiffeId, Status,
// Role, Region, Version, Labels) are left at their zero values.
// clampInt32 narrows a non-negative count to the int32 range used by the proto
// schema without wrapping: negatives become 0, oversized values cap at int32max.
func clampInt32(v int) int32 {
	if v < 0 {
		return 0
	}
	if v > 0x7fffffff {
		return 0x7fffffff
	}
	return int32(v) //gosec:disable G115 -- bounds-checked to [0,0x7fffffff] above
}

func DescriptorToNode(d device.DeviceDescriptor) *helixv1.Node {
	gpus := buildGPUSlice(d.GPUCount, d.GPUVRAMBytes)

	return &helixv1.Node{
		Id: d.ID,
		Resources: &helixv1.NodeResources{
			Cpu: &helixv1.CPUResources{
				Arch:  string(d.Arch),
				Cores: clampInt32(d.CPUCores),
			},
			Memory: &helixv1.MemoryResources{
				TotalBytes: d.MemoryBytes,
			},
			Gpus: gpus,
		},
	}
}

// NodeToDescriptor converts a *[helixv1.Node] to a [device.DeviceDescriptor].
//
// Only the fields listed in the package doc are transferred.  Node fields that
// have no Descriptor equivalent (Hostname, IpAddresses, Status, …) are
// ignored.  The device-only fields (NPUTops, PowerBudgetWatts, HasBattery,
// Trust) are always zero/default in the result — they cannot be recovered
// from a Node.
//
// A nil n returns a zero DeviceDescriptor.
func NodeToDescriptor(n *helixv1.Node) device.DeviceDescriptor {
	if n == nil {
		return device.DeviceDescriptor{}
	}

	var (
		arch         device.Arch
		cpuCores     int
		memoryBytes  int64
		gpuCount     int
		gpuVRAMBytes int64
	)

	if r := n.GetResources(); r != nil {
		if cpu := r.GetCpu(); cpu != nil {
			arch = device.Arch(cpu.GetArch())
			cpuCores = int(cpu.GetCores())
		}
		if mem := r.GetMemory(); mem != nil {
			memoryBytes = mem.GetTotalBytes()
		}
		gpus := r.GetGpus()
		gpuCount = len(gpus)
		for _, g := range gpus {
			gpuVRAMBytes += g.GetTotalMemory()
		}
	}

	return device.DeviceDescriptor{
		ID:           n.GetId(),
		Arch:         arch,
		CPUCores:     cpuCores,
		MemoryBytes:  memoryBytes,
		GPUCount:     gpuCount,
		GPUVRAMBytes: gpuVRAMBytes,
		// NPUTops, PowerBudgetWatts, HasBattery, Trust: no pb.Node equivalent;
		// always zero/default after a round-trip through Node.
	}
}

// buildGPUSlice constructs the []*helixv1.GPUResource slice for a given GPU
// count and aggregate VRAM.  VRAM is distributed evenly across all GPUs;
// any remainder bytes are added to the last entry.  If count is zero or
// negative the slice is nil.
func buildGPUSlice(count int, totalVRAMBytes int64) []*helixv1.GPUResource {
	if count <= 0 {
		return nil
	}

	gpus := make([]*helixv1.GPUResource, count)
	perGPU := totalVRAMBytes / int64(count)
	remainder := totalVRAMBytes - perGPU*int64(count)

	for i := range gpus {
		mem := perGPU
		if i == count-1 {
			mem += remainder // last GPU absorbs rounding remainder
		}
		gpus[i] = &helixv1.GPUResource{TotalMemory: mem}
	}

	return gpus
}
