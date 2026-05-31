package gpu

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Manager manages GPU resources for the cluster.
type Manager struct {
	mu       sync.RWMutex
	gpus     []*GPU
	started  atomic.Bool
	monitor  *Monitor
}

// NewManager creates a new GPU Manager.
func NewManager() *Manager {
	return &Manager{
		gpus: make([]*GPU, 0),
	}
}

// DetectGPUs detects available GPUs. It attempts real detection on Linux,
// then falls back to mock GPUs for testing or non-Linux platforms.
func (m *Manager) DetectGPUs() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	gpus, err := detectGPUsReal()
	if err != nil || len(gpus) == 0 {
		gpus = detectGPUsMock()
	}

	m.gpus = gpus
	return nil
}

// detectGPUsReal attempts to read NVIDIA GPU information from /proc/driver/nvidia/gpus/.
// Returns empty on non-Linux platforms or when the path does not exist.
func detectGPUsReal() ([]*GPU, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("real GPU detection only supported on Linux")
	}

	basePath := "/proc/driver/nvidia/gpus"
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return nil, err
	}

	var gpus []*GPU
	for i, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		infoPath := filepath.Join(basePath, entry.Name(), "information")
		info, err := os.ReadFile(infoPath)
		if err != nil {
			continue
		}

		gpu := &GPU{
			ID:     fmt.Sprintf("gpu-%d", i),
			UUID:   fmt.Sprintf("GPU-%s", entry.Name()),
			Status: Available,
		}

		lines := strings.Split(string(info), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Model:") {
				gpu.Model = strings.TrimSpace(strings.TrimPrefix(line, "Model:"))
			} else if strings.HasPrefix(line, "Video Memory:") {
				memStr := strings.TrimSpace(strings.TrimPrefix(line, "Video Memory:"))
				memStr = strings.TrimSuffix(memStr, "MB")
				memStr = strings.TrimSuffix(memStr, "MiB")
				if mem, err := strconv.Atoi(strings.TrimSpace(memStr)); err == nil {
					gpu.MemoryMB = mem
				}
			}
		}

		if gpu.Model == "" {
			gpu.Model = "Unknown NVIDIA GPU"
		}
		gpus = append(gpus, gpu)
	}

	return gpus, nil
}

// detectGPUsMock returns simulated GPUs for testing.
func detectGPUsMock() []*GPU {
	return []*GPU{
		{
			ID:       "gpu-0",
			UUID:     "GPU-MOCK-0000-0000-0000-000000000001",
			Model:    "NVIDIA A100-SXM4-40GB",
			MemoryMB: 40960,
			Status:   Available,
		},
		{
			ID:       "gpu-1",
			UUID:     "GPU-MOCK-0000-0000-0000-000000000002",
			Model:    "NVIDIA A100-SXM4-40GB",
			MemoryMB: 40960,
			Status:   Available,
		},
		{
			ID:       "gpu-2",
			UUID:     "GPU-MOCK-0000-0000-0000-000000000003",
			Model:    "NVIDIA H100-SXM5-80GB",
			MemoryMB: 81920,
			Status:   Available,
		},
		{
			ID:       "gpu-3",
			UUID:     "GPU-MOCK-0000-0000-0000-000000000004",
			Model:    "NVIDIA H100-SXM5-80GB",
			MemoryMB: 81920,
			Status:   Available,
		},
	}
}

// AllocateGPU allocates an available GPU to the given jobID.
func (m *Manager) AllocateGPU(jobID string) (*GPU, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, gpu := range m.gpus {
		if gpu.Status == Available {
			gpu.Status = Allocated
			gpu.AllocatedTo = jobID
			return gpu.Clone(), nil
		}
	}
	return nil, fmt.Errorf("no available GPUs")
}

// ReleaseGPU releases the GPU allocated to the given jobID.
func (m *Manager) ReleaseGPU(jobID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, gpu := range m.gpus {
		if gpu.AllocatedTo == jobID {
			gpu.Status = Available
			gpu.AllocatedTo = ""
			return
		}
	}
}

// ListGPUs returns a snapshot of all GPUs and their status.
func (m *Manager) ListGPUs() []*GPU {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*GPU, len(m.gpus))
	for i, gpu := range m.gpus {
		result[i] = gpu.Clone()
	}
	return result
}

// GetGPUByID returns a GPU by its ID.
func (m *Manager) GetGPUByID(id string) (*GPU, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, gpu := range m.gpus {
		if gpu.ID == id {
			return gpu.Clone(), nil
		}
	}
	return nil, fmt.Errorf("GPU not found: %s", id)
}

// UpdateGPU updates the internal GPU state (used by Monitor).
func (m *Manager) updateGPU(id string, updateFn func(*GPU)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, gpu := range m.gpus {
		if gpu.ID == id {
			updateFn(gpu)
			return
		}
	}
}
