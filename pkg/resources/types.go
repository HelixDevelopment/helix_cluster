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
// (see gpuclass.go): UUID carries a stable hardware identifier when the kernel
// exposes one (used to make Fingerprint distinct between otherwise-identical
// cards); Attested records that the GPU passed remote attestation; and
// ThermalThrottling records that the GPU is currently running with reduced
// sustained clocks. All three default to their zero value, preserving backward
// compatibility for every existing GPUInfo literal.
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
