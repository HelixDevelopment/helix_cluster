package resources

import (
	"sync"
	"time"
)

// CPUInfo holds CPU resource details.
type CPUInfo struct {
	Cores     int     `json:"cores"`
	UsedCores float64 `json:"used_cores"`
	Model     string  `json:"model"`
	Frequency float64 `json:"frequency_mhz"`
}

// MemoryInfo holds memory resource details.
type MemoryInfo struct {
	TotalKB     int64 `json:"total_kb"`
	AvailableKB int64 `json:"available_kb"`
	UsedKB      int64 `json:"used_kb"`
}

// GPUInfo holds GPU resource details.
//
// Count/Model/Memory are the original aggregate fields populated from the DRM
// sysfs enumeration. The remaining fields are ADDITIVE scheduler-facing signals
// (see gpuclass.go and accel.go): UUID carries a stable hardware identifier when
// the kernel exposes one (used to make Fingerprint distinct between
// otherwise-identical cards); Attested records that the GPU passed remote
// attestation; ThermalThrottling records that the GPU is currently running with
// reduced sustained clocks; and TFLOPS carries the peak FP32 throughput from a
// real per-OS probe or override label. All default to their zero value,
// preserving backward compatibility for every existing GPUInfo literal.
type GPUInfo struct {
	Count  int    `json:"count"`
	Model  string `json:"model"`
	Memory int64  `json:"memory_mb"`
	// UUID is a stable per-GPU hardware identifier (e.g. the DRM unique_id),
	// empty when the platform does not expose one.
	UUID string `json:"uuid,omitempty"`
	// Attested is true when the GPU has passed remote attestation.
	Attested bool `json:"attested,omitempty"`
	// ThermalThrottling is true when the GPU is currently thermally throttling.
	ThermalThrottling bool `json:"thermal_throttling,omitempty"`
	// TFLOPS is the GPU's peak single-precision (FP32) throughput in
	// teraflops, derived from a real per-OS probe (Apple Silicon GPU core
	// count on darwin, a known-model PCI table on linux) or from an
	// override label when the host cannot probe it. It defaults to 0 for any
	// existing GPUInfo literal, preserving backward compatibility.
	TFLOPS float64 `json:"tflops,omitempty"`
}

// Accelerators holds non-GPU accelerator presence/capacity reported for a node.
//
// These fields are ADDITIVE: they all default to their zero value so every
// existing NodeResources literal keeps the same JSON shape and meaning. Unlike
// the GPU DRM enumeration, NPUs / FPGAs / QPUs are not exposed through a
// portable sysfs node on the hosts we target, so their capacity is supplied
// through an OVERRIDE LABEL (an operator-set node label / env var) which the
// probe honors verbatim. A non-zero value therefore means "an operator has
// declared this accelerator on the node", which is exactly what the scheduler
// needs to place accelerator-bound work.
type Accelerators struct {
	// NPUTops is the declared neural-processing-unit throughput in TOPS
	// (tera-operations per second). Zero means no NPU is declared.
	NPUTops float64 `json:"npu_tops,omitempty"`
	// FPGALogicElements is the declared FPGA fabric size in logic elements.
	// Zero means no FPGA is declared.
	FPGALogicElements int64 `json:"fpga_logic_elements,omitempty"`
	// QPUPresent is true when a quantum-processing unit is declared present.
	QPUPresent bool `json:"qpu_present,omitempty"`
}

// DiskInfo holds disk resource details.
type DiskInfo struct {
	TotalKB int64 `json:"total_kb"`
	UsedKB  int64 `json:"used_kb"`
}

// NetworkInfo holds network resource details.
type NetworkInfo struct {
	Interfaces []string `json:"interfaces"`
	Bandwidth  int64    `json:"bandwidth_mbps"`
}

// NodeResources is a complete snapshot of a node's resources.
type NodeResources struct {
	NodeID  string      `json:"node_id"`
	CPU     CPUInfo     `json:"cpu"`
	Memory  MemoryInfo  `json:"memory"`
	GPU     GPUInfo     `json:"gpu"`
	Disk    DiskInfo    `json:"disk"`
	Network NetworkInfo `json:"network"`
	// Accelerators carries non-GPU accelerator (NPU/FPGA/QPU) declarations
	// for the node. It is additive and defaults to its zero value.
	Accelerators Accelerators `json:"accelerators,omitempty"`
}

// ResourceSnapshot is a timestamped reading of node resources.
type ResourceSnapshot struct {
	NodeID    string        `json:"node_id"`
	Resources NodeResources `json:"resources"`
	Timestamp time.Time     `json:"timestamp"`
}

// Reader is the interface for resource collection backends.
type Reader interface {
	// Read collects resources for the given node ID.
	Read(nodeID string) (NodeResources, error)
}

// Aggregator collects and serves node resource state.
type Aggregator interface {
	// Collect gathers resources from all registered readers.
	Collect() error
	// GetNode returns the latest snapshot for a node, or ok=false if unknown.
	GetNode(id string) (ResourceSnapshot, bool)
	// ListNodes returns all known node snapshots.
	ListNodes() []ResourceSnapshot
}

// nodeState holds the latest snapshot and metadata for a single node.
type nodeState struct {
	snap ResourceSnapshot
	ttl  time.Duration
	mu   sync.RWMutex
}

// isExpired reports whether the node's snapshot is older than its TTL.
func (ns *nodeState) isExpired(now time.Time) bool {
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	return now.Sub(ns.snap.Timestamp) > ns.ttl
}

// update sets a new snapshot and resets the TTL clock.
func (ns *nodeState) update(snap ResourceSnapshot, ttl time.Duration) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.snap = snap
	ns.ttl = ttl
}

// get returns the current snapshot.
func (ns *nodeState) get() ResourceSnapshot {
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	return ns.snap
}
