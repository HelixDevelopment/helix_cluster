package pool

import "time"

// DeviceStatus enumerates the runtime states a GPU device can be in.
// It is intentionally a typed string so consumers can switch exhaustively.
type DeviceStatus string

const (
	// DeviceStatusIdle means the device is provisioned and available for
	// allocation.
	DeviceStatusIdle DeviceStatus = "idle"
	// DeviceStatusBusy means the device is currently allocated to a workload.
	DeviceStatusBusy DeviceStatus = "busy"
	// DeviceStatusDraining means the device is finishing its current workload
	// and will not accept new allocations.
	DeviceStatusDraining DeviceStatus = "draining"
	// DeviceStatusFailed means the device has reported an unrecoverable error.
	DeviceStatusFailed DeviceStatus = "failed"
)

// GPUTier classifies the performance tier of a GPU device; used by the
// scheduler to match workloads that require a minimum capability bracket.
type GPUTier string

const (
	GPUTierEntry GPUTier = "entry"
	GPUTierMid   GPUTier = "mid"
	GPUTierHigh  GPUTier = "high"
	GPUTierUltra GPUTier = "ultra"
)

// WorkloadType categorises the compute profile of a submitted workload. It
// drives affinity rules in the scheduler (e.g. inference workloads prefer
// lower-latency devices; training workloads prefer maximum throughput).
type WorkloadType string

const (
	WorkloadTypeInference  WorkloadType = "inference"
	WorkloadTypeTraining   WorkloadType = "training"
	WorkloadTypeBatch      WorkloadType = "batch"
	WorkloadTypeFinetuning WorkloadType = "finetuning"
)

// GPUDevice is the canonical data model for a single physical or virtual GPU
// device tracked by the pool. It combines static provisioning metadata
// (VRAMBytes, TFLOPSFP16, CostPerHour) with runtime state (Status,
// AllocatedTo) so a PoolStatus snapshot can be computed from a slice of
// GPUDevices without a separate lookup.
//
// JSON field names follow the project's documented snake_case convention
// (gpu_count, vram_bytes, cost_per_hour, …). Every field that participates in
// scheduler decisions must have a json tag so API / storage serialisation is
// deterministic.
type GPUDevice struct {
	// ID is the provider-unique, opaque identifier for this device.
	// Example: "aws-p4d-i-0abc1234", "gcp-a3-mega-001".
	ID string `json:"id"`

	// Tier classifies the device's performance bracket (entry/mid/high/ultra).
	Tier GPUTier `json:"tier"`

	// Provider names the cloud or bare-metal backend that owns this device,
	// e.g. "aws", "gcp", "equinix".
	Provider string `json:"provider"`

	// Model is the GPU chip model, e.g. "A100", "H100", "RTX 4090".
	Model string `json:"model"`

	// VRAMBytes is the total device memory in bytes. Used by the scheduler to
	// filter devices that satisfy MinVRAM in a WorkloadSpec.
	VRAMBytes uint64 `json:"vram_bytes"`

	// TFLOPSFP16 is the theoretical FP16 peak throughput in teraFLOPS. Used
	// for performance-tier matching.
	TFLOPSFP16 float64 `json:"tflops_fp16"`

	// Location is the cloud region or datacenter identifier where the device
	// resides, e.g. "us-east-1", "europe-west4-a".
	Location string `json:"location"`

	// CostPerHour is the advertised $/hr price for this device, used for TCO
	// enforcement (MaxCostHour in WorkloadSpec).
	CostPerHour float64 `json:"cost_per_hour"`

	// Labels are arbitrary key/value annotations that affinity rules and
	// tenant isolation policies can filter on.
	Labels map[string]string `json:"labels,omitempty"`

	// --- runtime state ---

	// Status is the current operational state of the device.
	Status DeviceStatus `json:"status"`

	// AllocatedTo is the ID of the workload that currently holds this device,
	// or empty if the device is idle.
	AllocatedTo string `json:"allocated_to,omitempty"`
}

// WorkloadSpec describes the GPU resource requirements of a workload
// submission. The scheduler matches these requirements against the pool's
// GPUDevice inventory to produce a GPUAllocation.
//
// All JSON tags use the documented snake_case names (gpu_count, min_vram,
// max_cost_hour, max_latency_ms) so the representation is stable across
// serialisation round-trips.
type WorkloadSpec struct {
	// Type categorises the compute profile of the workload.
	Type WorkloadType `json:"type"`

	// GPUModel narrows device selection to a specific chip model, e.g.
	// "H100". Leave empty to accept any model that meets the other criteria.
	GPUModel string `json:"gpu_model,omitempty"`

	// GPUCount is the number of GPUDevices the workload requires.
	GPUCount int `json:"gpu_count"`

	// MinVRAM is the minimum acceptable VRAM per device, in bytes.
	MinVRAM uint64 `json:"min_vram"`

	// MaxLatencyMs is the maximum acceptable scheduling-to-start latency in
	// milliseconds, used by the scheduler to prefer pre-warmed instances.
	MaxLatencyMs int64 `json:"max_latency_ms"`

	// MaxCostHour is the maximum acceptable combined $/hr across all allocated
	// devices. A value of 0 means no cost cap.
	MaxCostHour float64 `json:"max_cost_hour"`

	// Duration is the expected wall-clock run time of the workload. The pool
	// manager can use this to schedule proactive scale-down after the workload
	// completes.
	Duration time.Duration `json:"duration_ns"`

	// Priority is the scheduling priority in [0, 100] where 100 is highest.
	// Higher-priority workloads preempt lower-priority ones when resources
	// are scarce (subject to the anti-eviction rules in CLAUDE-1).
	Priority int `json:"priority"`

	// Labels are caller-supplied annotations (e.g. tenant, job-id) that the
	// scheduler propagates to the resulting GPUAllocation.
	Labels map[string]string `json:"labels,omitempty"`
}

// GPUAllocation records the outcome of a successful scheduling decision: it
// maps one WorkloadSpec submission to the set of GPUDevice IDs that satisfy
// it, with timestamps for lease accounting.
type GPUAllocation struct {
	// AllocationID is a globally-unique identifier for this allocation event,
	// generated by the pool manager at allocation time.
	AllocationID string `json:"allocation_id"`

	// WorkloadID is the caller-supplied identifier for the workload being
	// served. It matches the value set by the workload submitter.
	WorkloadID string `json:"workload_id"`

	// DeviceIDs lists the IDs of every GPUDevice assigned to this workload.
	// len(DeviceIDs) == WorkloadSpec.GPUCount after a successful allocation.
	DeviceIDs []string `json:"device_ids"`

	// AllocatedAt is the wall-clock instant the allocation was committed.
	AllocatedAt time.Time `json:"allocated_at"`

	// ExpiresAt is the projected release time (AllocatedAt + WorkloadSpec.Duration).
	// The pool manager may reclaim devices if the workload overruns this bound.
	ExpiresAt time.Time `json:"expires_at"`

	// Labels are the labels propagated from the originating WorkloadSpec,
	// allowing operators to group allocations by tenant or job class.
	Labels map[string]string `json:"labels,omitempty"`
}

// PoolStatus is a point-in-time snapshot of the pool's aggregate capacity and
// utilisation. It is produced by inspecting the full GPUDevice slice; the
// scheduler exposes it as a health/metrics endpoint so operators can observe
// pool pressure without inspecting individual devices.
type PoolStatus struct {
	// TotalDevices is the count of all devices tracked by the pool,
	// regardless of status.
	TotalDevices int `json:"total_devices"`

	// IdleDevices is the count of devices in DeviceStatusIdle.
	IdleDevices int `json:"idle_devices"`

	// BusyDevices is the count of devices in DeviceStatusBusy.
	BusyDevices int `json:"busy_devices"`

	// DrainingDevices is the count of devices in DeviceStatusDraining.
	DrainingDevices int `json:"draining_devices"`

	// FailedDevices is the count of devices in DeviceStatusFailed.
	FailedDevices int `json:"failed_devices"`

	// TotalVRAMBytes is the sum of VRAMBytes across ALL tracked devices.
	TotalVRAMBytes uint64 `json:"total_vram_bytes"`

	// FreeVRAMBytes is the sum of VRAMBytes across idle devices only.
	FreeVRAMBytes uint64 `json:"free_vram_bytes"`

	// HourlyTCO is the summed CostPerHour across all tracked devices
	// (idle + busy), representing the running cost of the pool.
	HourlyTCO float64 `json:"hourly_tco"`

	// ActiveAllocations is the count of live GPUAllocations not yet released.
	ActiveAllocations int `json:"active_allocations"`
}

// ComputePoolStatus derives a PoolStatus from a snapshot of GPUDevices and the
// count of active allocations. It is a pure function (no side effects) so it
// can be called from any goroutine without locks beyond what the caller
// already holds on the devices slice.
func ComputePoolStatus(devices []GPUDevice, activeAllocations int) PoolStatus {
	var s PoolStatus
	s.ActiveAllocations = activeAllocations
	for i := range devices {
		d := &devices[i]
		s.TotalDevices++
		s.TotalVRAMBytes += d.VRAMBytes
		s.HourlyTCO += d.CostPerHour
		switch d.Status {
		case DeviceStatusIdle:
			s.IdleDevices++
			s.FreeVRAMBytes += d.VRAMBytes
		case DeviceStatusBusy:
			s.BusyDevices++
		case DeviceStatusDraining:
			s.DrainingDevices++
		case DeviceStatusFailed:
			s.FailedDevices++
		}
	}
	return s
}
