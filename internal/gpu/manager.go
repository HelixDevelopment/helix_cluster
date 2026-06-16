package gpu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

	// helixPoWFraction is the fraction [0,1] of GPU capacity reserved for
	// Helix Proof-of-Work workloads. Set by ReserveForHelixPoW; accessed under
	// mu for consistency with helixLoad.
	helixPoWFraction float64
	// helixLoad is the last-reported current Helix load fraction [0,1].
	// Set by SetHelixLoad; when this value exceeds 0.80, ChutesCapacity hard-
	// caps Chutes (batch/external) Available and FreeMemoryMB to zero
	// (Gepetto starvation guard).
	helixLoad float64

	// chutesAttested records, per GPU ID, the last GraVal attestation result.
	// A GPU must have an entry of true here to be eligible for Chutes inference
	// allocation (see attesthook.go). Guarded by mu like the rest of Manager's
	// in-memory state; nil until the first attestation is recorded.
	chutesAttested map[string]bool
}

// NewManager creates a new GPU Manager.
func NewManager() *Manager {
	return &Manager{
		gpus: make([]*GPU, 0),
	}
}

// DetectGPUs detects available GPUs using real, OS-native sources via the
// build-tag-selected detectGPUsPlatform (NVIDIA /proc on Linux, system_profiler
// on macOS, an explicit "unsupported" error elsewhere).
//
// Production NEVER falls back to a mock inventory: if detection fails OR finds
// zero devices, DetectGPUs returns that error honestly so callers observe "no
// GPUs detected" rather than fabricated hardware (CLAUDE-1/CLAUDE-2). Tests that
// need a controlled inventory must use InjectGPUsForTest instead.
func (m *Manager) DetectGPUs() error {
	gpus, err := detectGPUsPlatform()
	if err != nil {
		return fmt.Errorf("GPU detection failed: %w", err)
	}
	if len(gpus) == 0 {
		return fmt.Errorf("no GPUs detected")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.gpus = gpus
	m.started.Store(true)
	return nil
}

// nvidiaProcPath is the kernel-exposed directory describing NVIDIA GPUs. It is
// a package var (not a const) so tests can point detection at a synthetic tree.
var nvidiaProcPath = "/proc/driver/nvidia/gpus"

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

// InjectGPUsForTest installs a controlled GPU inventory and marks the Manager
// as started, bypassing real hardware detection. It exists SOLELY as a test
// seam so unit tests can exercise allocation/stats/offline logic against a
// deterministic multi-GPU inventory on any host. Production code paths use
// DetectGPUs, which reads real hardware and never invokes this.
func (m *Manager) InjectGPUsForTest(gpus []*GPU) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gpus = gpus
	m.started.Store(true)
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
	// Key on AllocatedTo (the binding fact that the job holds the device), NOT
	// on Status: if the Monitor flipped this job's GPU to Unhealthy/Offline after
	// it was handed out, the job STILL holds it. Re-allocating because the status
	// is no longer exactly Allocated would hand the job a SECOND physical GPU and
	// leak the first (which stays attributed to the job but is freed by only one
	// ReleaseGPU) — a double-allocation + accounting leak.
	for _, gpu := range m.gpus {
		if gpu.AllocatedTo == jobID {
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

	// Idempotency keys on AllocatedTo, not Status: a job whose held GPU was
	// flipped to Unhealthy/Offline by the Monitor still holds it. Matching only
	// Status==Allocated here would hand the job a SECOND device and leak the
	// first (see AllocateGPU for the same fix).
	for _, gpu := range m.gpus {
		if gpu.AllocatedTo == jobID {
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
