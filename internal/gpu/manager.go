package gpu

import (
	"context"
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
	mu   sync.RWMutex
	gpus []*GPU
	// started is set once DetectGPUs has populated the inventory. Allocation
	// is refused until then so a caller cannot reserve from an empty/undetected
	// pool and silently get "no available GPUs" instead of a clear lifecycle
	// error.
	started atomic.Bool
	monitor *Monitor
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
	m.started.Store(true)
	return nil
}

// nvidiaProcPath is the kernel-exposed directory describing NVIDIA GPUs. It is
// a package var (not a const) so tests can point detection at a synthetic tree.
var nvidiaProcPath = "/proc/driver/nvidia/gpus"

// detectGPUsReal attempts to read NVIDIA GPU information from
// /proc/driver/nvidia/gpus/. Returns an error on non-Linux platforms or when
// the path does not exist.
func detectGPUsReal() ([]*GPU, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("real GPU detection only supported on Linux")
	}
	return detectGPUsFrom(nvidiaProcPath)
}

// detectGPUsFrom parses NVIDIA GPU descriptors from basePath. It is the
// platform-independent core of detectGPUsReal and is exercised directly by
// tests against a synthetic directory tree.
func detectGPUsFrom(basePath string) ([]*GPU, error) {
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return nil, err
	}

	var gpus []*GPU
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		infoPath := filepath.Join(basePath, entry.Name(), "information")
		info, err := os.ReadFile(infoPath)
		if err != nil {
			continue
		}

		// Index by the number of GPUs actually discovered so far, not the raw
		// directory-listing position: non-directory entries or unreadable
		// "information" files must not create gaps or skips in the IDs.
		gpu := &GPU{
			ID:     fmt.Sprintf("gpu-%d", len(gpus)),
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

// AllocateGPU allocates an available GPU to the given jobID. Allocation is
// idempotent per job: if jobID already holds a GPU, that same GPU is returned
// rather than reserving a second device. An empty jobID is rejected so that a
// caller cannot accidentally mass-bind unallocated GPUs (which all carry an
// empty AllocatedTo).
func (m *Manager) AllocateGPU(jobID string) (*GPU, error) {
	if jobID == "" {
		return nil, fmt.Errorf("jobID must not be empty")
	}
	if !m.started.Load() {
		return nil, fmt.Errorf("GPU inventory not detected; call DetectGPUs first")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Honor an existing allocation for this job before consuming a new GPU.
	for _, gpu := range m.gpus {
		if gpu.Status == Allocated && gpu.AllocatedTo == jobID {
			return gpu.Clone(), nil
		}
	}

	for _, gpu := range m.gpus {
		if gpu.Status == Available {
			gpu.Status = Allocated
			gpu.AllocatedTo = jobID
			return gpu.Clone(), nil
		}
	}
	return nil, fmt.Errorf("no available GPUs")
}

// AllocateGPUByMemory allocates an Available GPU with at least minMemoryMB of
// total memory to jobID, preferring the smallest-fitting device so larger GPUs
// remain free for heavier jobs (best-fit). Like AllocateGPU it is idempotent
// per job, but a job that already holds a GPU smaller than minMemoryMB is
// reported as an error rather than silently returning an undersized device.
func (m *Manager) AllocateGPUByMemory(jobID string, minMemoryMB int) (*GPU, error) {
	if jobID == "" {
		return nil, fmt.Errorf("jobID must not be empty")
	}
	if !m.started.Load() {
		return nil, fmt.Errorf("GPU inventory not detected; call DetectGPUs first")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, gpu := range m.gpus {
		if gpu.Status == Allocated && gpu.AllocatedTo == jobID {
			if gpu.MemoryMB < minMemoryMB {
				return nil, fmt.Errorf("job %s already holds GPU %s with %dMB < required %dMB", jobID, gpu.ID, gpu.MemoryMB, minMemoryMB)
			}
			return gpu.Clone(), nil
		}
	}

	var best *GPU
	for _, gpu := range m.gpus {
		if gpu.Status != Available || gpu.MemoryMB < minMemoryMB {
			continue
		}
		if best == nil || gpu.MemoryMB < best.MemoryMB {
			best = gpu
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no available GPU with at least %dMB memory", minMemoryMB)
	}

	best.Status = Allocated
	best.AllocatedTo = jobID
	return best.Clone(), nil
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

// Stats summarizes the current GPU inventory. It is the aggregate view used by
// observability and scheduling components.
type Stats struct {
	Total         int `json:"total"`
	Available     int `json:"available"`
	Allocated     int `json:"allocated"`
	Unhealthy     int `json:"unhealthy"`
	Offline       int `json:"offline"`
	TotalMemoryMB int `json:"total_memory_mb"`
	// FreeMemoryMB is the total memory of GPUs that are currently Available.
	FreeMemoryMB int `json:"free_memory_mb"`
}

// Stats returns an aggregate snapshot of the GPU inventory.
func (m *Manager) Stats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var s Stats
	for _, gpu := range m.gpus {
		s.Total++
		s.TotalMemoryMB += gpu.MemoryMB
		switch gpu.Status {
		case Available:
			s.Available++
			s.FreeMemoryMB += gpu.MemoryMB
		case Allocated:
			s.Allocated++
		case Unhealthy:
			s.Unhealthy++
		case Offline:
			s.Offline++
		}
	}
	return s
}

// SetOffline marks a GPU as Offline for maintenance/draining. An Allocated GPU
// cannot be taken offline without first releasing its job, so SetOffline
// refuses to silently evict a running job and returns an error instead.
func (m *Manager) SetOffline(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, gpu := range m.gpus {
		if gpu.ID != id {
			continue
		}
		if gpu.Status == Allocated {
			return fmt.Errorf("GPU %s is allocated to %s; release it before taking offline", id, gpu.AllocatedTo)
		}
		gpu.Status = Offline
		return nil
	}
	return fmt.Errorf("GPU not found: %s", id)
}

// SetOnline returns an Offline GPU to the Available pool. GPUs that are not
// Offline are left unchanged (an Allocated or Unhealthy GPU must not be forced
// back to Available by this call).
func (m *Manager) SetOnline(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, gpu := range m.gpus {
		if gpu.ID != id {
			continue
		}
		if gpu.Status == Offline {
			gpu.Status = Available
			gpu.AllocatedTo = ""
		}
		return nil
	}
	return fmt.Errorf("GPU not found: %s", id)
}

// StartMonitoring creates and starts a background health Monitor bound to this
// Manager. It is a no-op (returning the already-running Monitor) if monitoring
// is already active. The returned Monitor can be inspected; StopMonitoring
// tears it down.
func (m *Manager) StartMonitoring(ctx context.Context, opts ...MonitorOption) (*Monitor, error) {
	m.mu.Lock()
	if m.monitor != nil && m.monitor.IsRunning() {
		mon := m.monitor
		m.mu.Unlock()
		return mon, nil
	}
	mon := newMonitor(m, opts...)
	m.monitor = mon
	m.mu.Unlock()

	if err := mon.Start(ctx); err != nil {
		// Start failed: clear the field so we don't leak a non-running Monitor
		// and so the next StartMonitoring builds a fresh one instead of seeing
		// a stale (m.monitor != nil && !IsRunning()) monitor.
		m.mu.Lock()
		if m.monitor == mon {
			m.monitor = nil
		}
		m.mu.Unlock()
		return nil, err
	}
	return mon, nil
}

// StopMonitoring stops the background Monitor if one is running.
func (m *Manager) StopMonitoring() {
	m.mu.Lock()
	mon := m.monitor
	m.mu.Unlock()

	if mon != nil {
		mon.Stop()
	}
}
