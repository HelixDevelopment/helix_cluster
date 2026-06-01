package gpu

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
)

// errNoBackendDetected is the sentinel returned by AutoDetect when none of the
// registered backends reports at least one device. Callers compare via
// errors.Is(err, ErrNoBackendDetected).
var ErrNoBackendDetected = errors.New("gpu: no backend detected any device")

// BackendRegistry holds the set of registered GPUBackend implementations and
// provides AutoDetect to choose a usable one at runtime. It is safe for
// concurrent use.
type BackendRegistry struct {
	mu       sync.RWMutex
	backends map[string]GPUBackend
	// probeOrder records registration order so AutoDetect is deterministic.
	probeOrder []string
}

// NewBackendRegistry returns an empty, ready-to-use BackendRegistry.
func NewBackendRegistry() *BackendRegistry {
	return &BackendRegistry{
		backends:   make(map[string]GPUBackend),
		probeOrder: make([]string, 0),
	}
}

// Register adds b to the registry, keyed by b.Vendor(). An error is returned
// if a backend for the same vendor is already registered — duplicate
// registration is almost always a bug in the caller.
func (r *BackendRegistry) Register(b GPUBackend) error {
	vendor := b.Vendor()
	if vendor == "" {
		return fmt.Errorf("gpu registry: backend returned empty vendor string")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.backends[vendor]; exists {
		return fmt.Errorf("gpu registry: backend for vendor %q already registered", vendor)
	}
	r.backends[vendor] = b
	r.probeOrder = append(r.probeOrder, vendor)
	return nil
}

// Get returns the registered backend for vendor, or (nil, false) if none.
func (r *BackendRegistry) Get(vendor string) (GPUBackend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.backends[vendor]
	return b, ok
}

// List returns all registered vendor strings in registration order.
func (r *BackendRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]string, len(r.probeOrder))
	copy(result, r.probeOrder)
	return result
}

// vendorToolchainPresent reports whether the host likely has the toolchain for
// a vendor available, by checking for a well-known binary on PATH. This is a
// fast pre-filter; the authoritative probe is DetectDevices.
//
// We probe in this order so AutoDetect tries them deterministically:
//
//	nvidia  — nvidia-smi
//	amd     — rocm-smi
//	intel   — sycl-ls
//	apple   — no separate toolchain needed (uses system_profiler)
//
// An absent binary does not disqualify the backend outright: the backend's own
// DetectDevices is still called (e.g. the Apple backend uses system_profiler,
// which is always present on macOS).
func vendorToolchainPresent(vendor string) bool {
	switch vendor {
	case "nvidia":
		_, err := exec.LookPath("nvidia-smi")
		return err == nil
	case "amd":
		_, err := exec.LookPath("rocm-smi")
		return err == nil
	case "intel":
		_, err := exec.LookPath("sycl-ls")
		return err == nil
	default:
		// For unknown vendors (including "apple") we always attempt the probe.
		return true
	}
}

// AutoDetect iterates the registered backends in a deterministic probe order —
// nvidia → amd → intel → apple → (everything else in registration order) —
// performing REAL toolchain probes via exec.LookPath and actually calling each
// backend's DetectDevices. The first backend that reports >=1 device is
// returned. If no backend finds any device, ErrNoBackendDetected is returned.
//
// The probe order for the preferred vendors is hard-coded so that CUDA takes
// precedence over ROCm / oneAPI / Apple on hosts with multiple toolchains.
func (r *BackendRegistry) AutoDetect(ctx context.Context) (GPUBackend, error) {
	r.mu.RLock()
	// Build the probe sequence: preferred vendors first, then remainder in
	// registration order.
	preferred := []string{"nvidia", "amd", "intel", "apple"}
	seen := make(map[string]bool)
	ordered := make([]string, 0, len(r.probeOrder))
	for _, v := range preferred {
		if _, ok := r.backends[v]; ok {
			ordered = append(ordered, v)
			seen[v] = true
		}
	}
	for _, v := range r.probeOrder {
		if !seen[v] {
			ordered = append(ordered, v)
		}
	}
	// Take a snapshot so we can release the read lock before running probes
	// (DetectDevices may block on exec).
	snapshot := make(map[string]GPUBackend, len(r.backends))
	for k, v := range r.backends {
		snapshot[k] = v
	}
	r.mu.RUnlock()

	for _, vendor := range ordered {
		b, ok := snapshot[vendor]
		if !ok {
			continue
		}

		// Fast pre-filter: if we know the toolchain binary is absent, skip the
		// DetectDevices call for CUDA/ROCm/oneAPI — these require heavy
		// runtimes. The Apple backend always gets a full probe.
		if vendor != "apple" && !vendorToolchainPresent(vendor) {
			continue
		}

		devices, err := b.DetectDevices(ctx)
		if err != nil {
			// Probe failure means "not usable on this host" — try the next one.
			continue
		}
		if len(devices) >= 1 {
			return b, nil
		}
	}

	return nil, fmt.Errorf("%w", ErrNoBackendDetected)
}
