//go:build !darwin && !linux

package fpgadetect

import (
	"context"
	"fmt"
	"runtime"
)

// newDetector returns the honest fallback Detector for platforms without a real
// FPGA-probe implementation.
func newDetector() Detector { return &unsupportedDetector{} }

type unsupportedDetector struct{}

// Detect is the honest fallback for platforms without a real FPGA-probe
// implementation. Per HelixConstitution §11.4.3 it returns a typed,
// reason-bearing error instead of a fake empty PASS, so callers (and tests) can
// SKIP-with-reason rather than be misled into believing no FPGA exists.
func (unsupportedDetector) Detect(_ context.Context) ([]FPGA, error) {
	return nil, fmt.Errorf("fpgadetect: FPGA detection not implemented for GOOS=%s (no real per-OS probe)", runtime.GOOS)
}
