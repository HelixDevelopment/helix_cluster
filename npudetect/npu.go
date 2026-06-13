// Package npudetect provides cross-platform discovery of neural processing
// units (NPUs) / AI accelerators behind a single interface.
//
// Per the Helix CLAUDE-2 Cross-Platform Parity Guarantee, every supported OS
// implements a REAL probe of its host hardware using that OS's proper system
// facility — there are no mock/stub implementations for this real-operation
// feature. Platform code is split by build tags behind the single Detector
// interface declared here:
//
//   - darwin: detect.go is npu_darwin.go (system_profiler + ioreg)
//   - linux:  npu_linux.go (device-tree / sysfs probing of RKNN, DLA, Hexagon)
//
// The Linux device-tree/sysfs parsing logic is factored into dtparse.go so it
// is unit-testable against injectable fixture filesystem roots on any OS.
package npudetect

import "context"

// NPU describes a single detected neural-processing accelerator.
type NPU struct {
	// Vendor is the silicon vendor, e.g. "Apple", "Rockchip", "NVIDIA",
	// "Qualcomm".
	Vendor string
	// Model is a human-readable model name, e.g. "Apple Neural Engine",
	// "Rockchip RKNN NPU", "NVIDIA DLA".
	Model string
	// Runtime is the software runtime/stack used to drive the accelerator,
	// e.g. "CoreML/ANE", "RKNN", "TensorRT/DLA", "QNN/Hexagon".
	Runtime string
	// TOPS is the approximate peak throughput in tera-operations per second.
	// It MAY be approximate (vendors do not always publish exact figures);
	// the Source field documents where the value came from. A value of 0
	// means "unknown / not mapped".
	TOPS float64
	// Source documents how this NPU was detected and where any TOPS estimate
	// originates, e.g. "system_profiler:Apple M3 Pro; ioreg ane@; TOPS est.
	// from Apple M3 family 16-core ANE (~18 TOPS, vendor figure)".
	Source string
}

// Detector probes the host for NPUs/accelerators. Each supported OS provides a
// real implementation via build tags.
type Detector interface {
	Detect(ctx context.Context) ([]NPU, error)
}

// DetectNPUs returns the NPUs/accelerators present on the host. It dispatches
// to the per-OS Detector selected at build time. It never returns a fabricated
// device: on a host with no detectable NPU it returns an empty slice and a nil
// error.
func DetectNPUs(ctx context.Context) ([]NPU, error) {
	return newDetector().Detect(ctx)
}
