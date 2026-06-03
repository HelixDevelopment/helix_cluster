//go:build !darwin

package device

// platformProbeGPU returns honest zeros on non-darwin hosts: this package's
// real GPU probe is currently implemented for macOS only (HXC-1634, via
// system_profiler). Linux GPU enumeration is performed by internal/gpu
// (NVIDIA /proc) and is not duplicated here; reporting 0 is honest and never a
// fabricated value (§CLAUDE-1 rule 5). When a native Linux device-probe lands
// it replaces this file behind a //go:build linux tag.
func platformProbeGPU() (count int, vramBytes int64) {
	return 0, 0
}
