// Package gpu provides detection, allocation, and health monitoring of GPU
// devices for the Helix cluster. Detection reads REAL, OS-native sources via a
// build-tag-selected detectGPUsPlatform: NVIDIA /proc on Linux and
// system_profiler on macOS (unsupported OSes return an explicit error).
// Production never substitutes a mock inventory; tests that need a controlled
// inventory use Manager.InjectGPUsForTest. The Manager type owns the device
// inventory and is safe for concurrent use; the Monitor type periodically
// refreshes per-GPU health metrics.
package gpu

import (
	"encoding/json"
	"fmt"
)

// GPUStatus represents the current status of a GPU.
type GPUStatus int

const (
	Available GPUStatus = iota
	Allocated
	Unhealthy
	Offline
)

var gpuStatusNames = map[GPUStatus]string{
	Available: "available",
	Allocated: "allocated",
	Unhealthy: "unhealthy",
	Offline:   "offline",
}

var gpuStatusValues = map[string]GPUStatus{
	"available": Available,
	"allocated": Allocated,
	"unhealthy": Unhealthy,
	"offline":   Offline,
}

func (s GPUStatus) String() string {
	if name, ok := gpuStatusNames[s]; ok {
		return name
	}
	return fmt.Sprintf("GPUStatus(%d)", s)
}

// MarshalJSON implements json.Marshaler.
func (s GPUStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *GPUStatus) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	if v, ok := gpuStatusValues[str]; ok {
		*s = v
		return nil
	}
	return fmt.Errorf("invalid GPUStatus: %s", str)
}

// GPU represents a single GPU device.
type GPU struct {
	ID                 string    `json:"id"`
	UUID               string    `json:"uuid"`
	Model              string    `json:"model"`
	MemoryMB           int       `json:"memory_mb"`
	UtilizationPercent float64   `json:"utilization_percent"`
	TemperatureC       float64   `json:"temperature_c"`
	AllocatedTo        string    `json:"allocated_to"`
	Status             GPUStatus `json:"status"`
}

// Clone returns a deep copy of the GPU.
func (g *GPU) Clone() *GPU {
	if g == nil {
		return nil
	}
	clone := *g
	return &clone
}
