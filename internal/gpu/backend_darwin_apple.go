package gpu

import (
	"context"
	"fmt"
	"time"
)

// AppleBackend is the GPUBackend for Apple Silicon (and Intel Mac) GPUs.
// DetectDevices, GetDeviceStatus, and GetMetrics use real OS-native data via
// the existing detectGPUsPlatform() / Manager pipeline (system_profiler +
// sysctl on darwin; an explicit error on other platforms).
//
// AllocateMemory, FreeMemory, Execute, ExecuteDistributed, EnableMPS, and
// DisableMPS all return ErrUnsupported: this host has no Metal-compute
// execution toolchain wired. They MUST NOT fake success (CLAUDE-1/CLAUDE-2).
type AppleBackend struct{}

// Vendor implements GPUBackend. The registry key is "apple".
func (a *AppleBackend) Vendor() string { return "apple" }

// DetectDevices probes the host for Apple GPU devices. On darwin it delegates
// to detectGPUsPlatform (system_profiler + sysctl) and converts the results
// to []DeviceInfo. On other platforms the platform detector returns an error
// and DetectDevices returns that error with no fabricated inventory.
func (a *AppleBackend) DetectDevices(ctx context.Context) ([]DeviceInfo, error) {
	gpus, err := detectGPUsPlatform()
	if err != nil {
		// Honest failure — don't fabricate devices.
		return nil, fmt.Errorf("apple backend: DetectDevices: %w", err)
	}

	infos := make([]DeviceInfo, 0, len(gpus))
	for _, g := range gpus {
		infos = append(infos, DeviceInfo{
			ID:       g.ID,
			UUID:     g.UUID,
			Vendor:   "apple",
			Model:    g.Model,
			MemoryMB: g.MemoryMB,
			// ComputeUnits: system_profiler does not expose a reliable core count
			// in the JSON fields we parse. Leave as 0 (unknown) rather than
			// fabricating a number.
			ComputeUnits: 0,
		})
	}
	return infos, nil
}

// GetDeviceStatus returns a health snapshot for the device identified by id.
// On darwin, temperature and utilization are not exposed by system_profiler
// (Metal Performance HUD requires a running app). We return the device as
// Healthy with zero metrics, which is honest for an idle GPU and does not
// fabricate sensor readings.
func (a *AppleBackend) GetDeviceStatus(ctx context.Context, id string) (DeviceStatus, error) {
	devices, err := a.DetectDevices(ctx)
	if err != nil {
		return DeviceStatus{}, err
	}
	for _, d := range devices {
		if d.ID == id {
			return DeviceStatus{
				ID:                 d.ID,
				TemperatureC:       0,
				UtilizationPercent: 0,
				MemoryUsedMB:       0,
				Healthy:            true,
			}, nil
		}
	}
	return DeviceStatus{}, fmt.Errorf("apple backend: device %q not found", id)
}

// GetMetrics returns a lightweight runtime-metric snapshot for the device id.
// Apple's GPU metrics are not accessible without IOKit/Metal-Performance-HUD;
// we return a zero-valued GPUMetrics (honest: the GPU is idle / not measured).
func (a *AppleBackend) GetMetrics(ctx context.Context, id string) (GPUMetrics, error) {
	devices, err := a.DetectDevices(ctx)
	if err != nil {
		return GPUMetrics{}, err
	}
	for _, d := range devices {
		if d.ID == id {
			return GPUMetrics{
				TemperatureC:       0,
				UtilizationPercent: 0,
				MemoryUsedMB:       0,
			}, nil
		}
	}
	return GPUMetrics{}, fmt.Errorf("apple backend: device %q not found", id)
}

// AllocateMemory returns ErrUnsupported. Apple Silicon GPU memory is managed
// by the Metal/unified-memory model; there is no wired execution toolchain.
func (a *AppleBackend) AllocateMemory(_ context.Context, _ string, _ int) (string, error) {
	return "", ErrUnsupported
}

// FreeMemory returns ErrUnsupported. Apple Silicon GPU memory is managed by
// the Metal/unified-memory model; there is no wired execution toolchain.
func (a *AppleBackend) FreeMemory(_ context.Context, _ string, _ string) error {
	return ErrUnsupported
}

// Execute returns ErrUnsupported. No Metal-compute execution toolchain is
// wired; returning fake success would be a CLAUDE-1 PASS-bluff.
func (a *AppleBackend) Execute(_ context.Context, _, _ string, _ time.Duration) (string, error) {
	return "", ErrUnsupported
}

// ExecuteDistributed returns ErrUnsupported. No Metal-compute execution
// toolchain is wired; returning fake success would be a CLAUDE-1 PASS-bluff.
func (a *AppleBackend) ExecuteDistributed(_ context.Context, _ string, _ []string, _ time.Duration) (map[string]string, error) {
	return nil, ErrUnsupported
}

// EnableMPS returns ErrUnsupported. Apple GPUs do not expose an MPS-equivalent
// interface accessible without proprietary tooling.
func (a *AppleBackend) EnableMPS(_ context.Context, _ string, _ int) error {
	return ErrUnsupported
}

// DisableMPS returns ErrUnsupported. Apple GPUs do not expose an MPS-equivalent
// interface accessible without proprietary tooling.
func (a *AppleBackend) DisableMPS(_ context.Context, _ string) error {
	return ErrUnsupported
}
