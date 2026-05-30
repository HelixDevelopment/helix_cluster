//go:build !linux

package resources

import "fmt"

// MockReader returns synthetic resource data for development on non-Linux platforms.
type MockReader struct {
	// Synthetic values that can be overridden in tests.
	Cores      int
	Model      string
	Frequency  float64
	TotalKB    int64
	AvailableKB int64
	GPUCount   int
	GPUModel   string
	GPUMemory  int64
}

// NewMockReader creates a MockReader with sensible defaults.
func NewMockReader() *MockReader {
	return &MockReader{
		Cores:       8,
		Model:       "Mock CPU",
		Frequency:   2400.0,
		TotalKB:     16 * 1024 * 1024, // 16 GB in KB
		AvailableKB: 8 * 1024 * 1024,  // 8 GB in KB
		GPUCount:    2,
		GPUModel:    "Mock GPU",
		GPUMemory:   8192, // MB
	}
}

// Read collects synthetic resources for the given node ID.
func (m *MockReader) Read(nodeID string) (NodeResources, error) {
	if nodeID == "" {
		return NodeResources{}, fmt.Errorf("nodeID cannot be empty")
	}
	usedKB := m.TotalKB - m.AvailableKB
	if usedKB < 0 {
		usedKB = 0
	}
	return NodeResources{
		NodeID: nodeID,
		CPU: CPUInfo{
			Cores:     m.Cores,
			UsedCores: float64(m.Cores) * 0.25,
			Model:     m.Model,
			Frequency: m.Frequency,
		},
		Memory: MemoryInfo{
			TotalKB:     m.TotalKB,
			AvailableKB: m.AvailableKB,
			UsedKB:      usedKB,
		},
		GPU: GPUInfo{
			Count:  m.GPUCount,
			Model:  m.GPUModel,
			Memory: m.GPUMemory,
		},
		Disk: DiskInfo{
			TotalKB: 500 * 1024 * 1024, // 500 GB
			UsedKB:  100 * 1024 * 1024,
		},
		Network: NetworkInfo{
			Interfaces: []string{"eth0", "lo"},
			Bandwidth:  1000,
		},
	}, nil
}

// ReadCPUInfo returns synthetic CPU info.
func (m *MockReader) ReadCPUInfo() (CPUInfo, error) {
	return CPUInfo{
		Cores:     m.Cores,
		UsedCores: float64(m.Cores) * 0.25,
		Model:     m.Model,
		Frequency: m.Frequency,
	}, nil
}

// ReadMemInfo returns synthetic memory info.
func (m *MockReader) ReadMemInfo() (MemoryInfo, error) {
	usedKB := m.TotalKB - m.AvailableKB
	if usedKB < 0 {
		usedKB = 0
	}
	return MemoryInfo{
		TotalKB:     m.TotalKB,
		AvailableKB: m.AvailableKB,
		UsedKB:      usedKB,
	}, nil
}
