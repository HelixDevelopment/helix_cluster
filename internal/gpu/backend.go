// Package gpu provides detection, allocation, and health monitoring of GPU
// devices for the Helix cluster. This file defines the unified GPUBackend
// interface and associated value types used by the backend registry and all
// concrete backend implementations.
package gpu

import (
	"context"
	"errors"
	"time"
)

// ErrUnsupported is returned by GPUBackend operations that are not available
// on the current backend or host toolchain (e.g. Metal-compute execution on
// Apple Silicon where no compute toolchain is wired). Callers MUST test via
// errors.Is(err, ErrUnsupported) rather than string comparison.
var ErrUnsupported = errors.New("gpu: operation unsupported on this backend/host")

// DeviceInfo describes static, read-once properties of a GPU device.
type DeviceInfo struct {
	// ID is the backend-assigned device identifier (e.g. "gpu-0").
	ID string
	// UUID is a stable, world-unique identifier for the device.
	UUID string
	// Vendor is the device vendor name (e.g. "nvidia", "amd", "apple", "intel").
	Vendor string
	// Model is the human-readable model name (e.g. "Apple M3 GPU").
	Model string
	// MemoryMB is total device memory in mebibytes. May be 0 if the backend
	// cannot determine it without fabricating a value.
	MemoryMB int
	// ComputeUnits is the number of compute units / shader processors reported
	// by the device. May be 0 when the backend cannot read this value.
	ComputeUnits int
}

// DeviceStatus captures the dynamic health state of a GPU device.
type DeviceStatus struct {
	// ID is the device identifier from DeviceInfo.
	ID string
	// TemperatureC is the current die temperature in Celsius.
	TemperatureC float64
	// UtilizationPercent is the current shader/compute utilization in [0,100].
	UtilizationPercent float64
	// MemoryUsedMB is the number of mebibytes currently occupied on the device.
	MemoryUsedMB int
	// Healthy is false when the device is in an error or degraded state.
	Healthy bool
}

// GPUMetrics is a lightweight snapshot of runtime metrics for a single device.
// It is the per-sample type used by the monitoring loop and scrape endpoints.
type GPUMetrics struct {
	// TemperatureC is the current die temperature in Celsius.
	TemperatureC float64
	// UtilizationPercent is the current compute utilization in [0,100].
	UtilizationPercent float64
	// MemoryUsedMB is the number of mebibytes currently in use.
	MemoryUsedMB int
}

// GPUBackend is the unified interface that every vendor-specific GPU backend
// must satisfy. Implementations are registered in a BackendRegistry and probed
// by AutoDetect to find a usable backend at runtime.
//
// Implementations MUST be safe for concurrent use.
//
// Methods that cannot be supported on the current host or toolchain MUST
// return ErrUnsupported (not nil, and not a fabricated success).
type GPUBackend interface {
	// Vendor returns the vendor string this backend handles (e.g. "nvidia",
	// "amd", "apple", "intel"). It is used as the registry key and is always
	// lower-case.
	Vendor() string

	// DetectDevices probes the host for devices managed by this backend and
	// returns their static descriptions. A nil error with an empty slice means
	// the backend is functional but finds no devices. An error means the probe
	// could not be completed (toolchain missing, permission denied, etc.).
	DetectDevices(ctx context.Context) ([]DeviceInfo, error)

	// GetDeviceStatus returns the current health snapshot for the device
	// identified by id (as returned by DetectDevices).
	GetDeviceStatus(ctx context.Context, id string) (DeviceStatus, error)

	// GetMetrics returns a lightweight runtime-metric snapshot for the device
	// identified by id.
	GetMetrics(ctx context.Context, id string) (GPUMetrics, error)

	// AllocateMemory requests that mb mebibytes be reserved on the device
	// identified by id. On success it returns an opaque buffer handle that
	// must be passed to FreeMemory. Returns ErrUnsupported when the backend
	// has no memory-management facility wired.
	AllocateMemory(ctx context.Context, id string, mb int) (string, error)

	// FreeMemory releases the buffer identified by buf on the device id, as
	// previously allocated by AllocateMemory. Returns ErrUnsupported when the
	// backend has no memory-management facility wired.
	FreeMemory(ctx context.Context, id string, buf string) error

	// Execute dispatches jobID to the single device id, waiting up to timeout.
	// On success it returns an opaque execution receipt. Returns ErrUnsupported
	// when the backend has no compute-execution toolchain wired.
	Execute(ctx context.Context, jobID string, id string, timeout time.Duration) (string, error)

	// ExecuteDistributed dispatches jobID across the set of device ids, waiting
	// up to timeout. Returns a map from device id to execution receipt.
	// Returns ErrUnsupported when the backend has no distributed-compute
	// toolchain wired.
	ExecuteDistributed(ctx context.Context, jobID string, ids []string, timeout time.Duration) (map[string]string, error)

	// EnableMPS enables Multi-Process Service (or an equivalent time-slicing
	// mechanism) on device id, configuring a time slice of sliceMS milliseconds.
	// Returns ErrUnsupported when MPS is not available for this backend.
	EnableMPS(ctx context.Context, id string, sliceMS int) error

	// DisableMPS tears down Multi-Process Service on device id.
	// Returns ErrUnsupported when MPS is not available for this backend.
	DisableMPS(ctx context.Context, id string) error
}
