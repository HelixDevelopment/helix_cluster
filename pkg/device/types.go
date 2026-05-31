// Package device provides device capability descriptors and a deterministic
// tier classifier for Phase 5 heterogeneous-device support in Helix Cluster OS.
//
// The package takes device capabilities as INPUT (a DeviceDescriptor struct) and
// classifies, scores, and routes them deterministically using documented
// thresholds. It does NOT probe real hardware: the only seam that would touch
// real hardware is the Prober interface, which here ships with a software
// reference implementation (StaticProber) that returns a fixed descriptor. Any
// genuine hardware-probe backend must be supplied by the caller and is honestly
// out of scope for this package.
package device

// Arch is a CPU instruction-set architecture identifier. It mirrors the values
// used by Go's GOARCH so descriptors can be populated from runtime.GOARCH or
// from an inventory record without translation.
type Arch string

const (
	// ArchUnknown is the zero value; classification still works but EffectiveCompute
	// applies no architecture bonus.
	ArchUnknown Arch = ""
	// ArchAMD64 is 64-bit x86.
	ArchAMD64 Arch = "amd64"
	// ArchARM64 is 64-bit ARM (AArch64).
	ArchARM64 Arch = "arm64"
	// ArchRISCV64 is 64-bit RISC-V.
	ArchRISCV64 Arch = "riscv64"
)

// TrustClass expresses the hardware/attestation trust level of a device. Higher
// values are more trusted. It does not affect tier classification (which is a
// pure compute-class judgement) but is carried for routing/admission decisions.
type TrustClass int

const (
	// TrustUntrusted is an unattested, possibly hostile device (e.g. BYOD edge).
	TrustUntrusted TrustClass = iota
	// TrustBasic is a known but un-attested device.
	TrustBasic
	// TrustAttested has verified secure-boot / measured-boot evidence.
	TrustAttested
	// TrustConfidential supports confidential-compute (TEE/SEV-SNP/TDX).
	TrustConfidential
)

// DeviceDescriptor is a complete, probe-independent description of a device's
// capabilities. All fields are plain data so a descriptor can be marshalled,
// stored, and replayed deterministically. No method on this type performs I/O.
type DeviceDescriptor struct {
	// ID is a stable identifier for the device.
	ID string `json:"id"`
	// Arch is the CPU architecture.
	Arch Arch `json:"arch"`
	// CPUCores is the number of logical CPU cores. Must be >= 0.
	CPUCores int `json:"cpu_cores"`
	// MemoryBytes is total system RAM in bytes. Must be >= 0.
	MemoryBytes int64 `json:"memory_bytes"`
	// GPUCount is the number of discrete/integrated GPUs.
	GPUCount int `json:"gpu_count"`
	// GPUVRAMBytes is total GPU video memory in bytes across all GPUs.
	GPUVRAMBytes int64 `json:"gpu_vram_bytes"`
	// NPUTops is neural-accelerator throughput in INT8 TOPS (tera-ops/sec).
	NPUTops float64 `json:"npu_tops"`
	// PowerBudgetWatts is the sustained power budget in watts.
	PowerBudgetWatts float64 `json:"power_budget_watts"`
	// HasBattery is true for battery-powered (mobile/handheld) devices.
	HasBattery bool `json:"has_battery"`
	// Trust is the device's trust class.
	Trust TrustClass `json:"trust"`
}

// IsAccelerated reports whether the device has any compute accelerator, i.e. at
// least one GPU or a non-zero NPU. Pure-CPU devices return false.
func (d DeviceDescriptor) IsAccelerated() bool {
	return d.GPUCount > 0 || d.NPUTops > 0
}

// Prober is the seam for obtaining a DeviceDescriptor. A real implementation
// would inspect actual hardware (sysfs, NVML, /proc, etc.). This package ships
// only StaticProber, a software reference that returns a fixed descriptor; real
// hardware probing is intentionally out of scope here.
type Prober interface {
	// Probe returns the descriptor for the local (or referenced) device.
	Probe() (DeviceDescriptor, error)
}

// StaticProber is a software reference Prober that returns a fixed descriptor.
// It performs NO hardware access; it exists so callers and tests have a
// deterministic Prober without a real hardware backend.
type StaticProber struct {
	Descriptor DeviceDescriptor
}

// Probe returns the configured fixed descriptor. It never errors and never
// touches hardware.
func (p StaticProber) Probe() (DeviceDescriptor, error) {
	return p.Descriptor, nil
}
