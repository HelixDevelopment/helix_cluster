//go:build darwin

package resources

// On darwin, proc_mock.go is excluded from the production build (CLAUDE-2
// compliance: darwin must use the real DarwinReader). However, package_test.go
// still relies on MockReader / NewMockReader for aggregator and concurrency
// tests that do not test proc/sysctl behaviour.  This file provides a
// compile-only shim so those tests continue to compile and run on darwin.
//
// NOTE: MockReader is used ONLY in aggregator/concurrency/unit tests that
// need an injectable deterministic Reader.  It is NOT registered as the
// production darwin reader anywhere in non-test code.

import "fmt"

// MockReader returns synthetic resource data for use in aggregator unit tests.
// It is present at test-compile time on darwin because proc_mock.go is
// excluded from the darwin production build.
type MockReader struct {
	Cores       int
	Model       string
	Frequency   float64
	TotalKB     int64
	AvailableKB int64
	GPUCount    int
	GPUModel    string
	GPUMemory   int64
}

// NewMockReader creates a MockReader with sensible defaults.
func NewMockReader() *MockReader {
	return &MockReader{
		Cores:       8,
		Model:       "Mock CPU",
		Frequency:   2400.0,
		TotalKB:     16 * 1024 * 1024,
		AvailableKB: 8 * 1024 * 1024,
		GPUCount:    2,
		GPUModel:    "Mock GPU",
		GPUMemory:   8192,
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
			TotalKB: 500 * 1024 * 1024,
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
