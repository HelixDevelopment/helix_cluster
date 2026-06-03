//go:build linux

package resources

// Linux GPU TFLOPS probe (HXC-1300).
//
// On Linux a GPU's PCI vendor:device identity is read from real sysfs by
// ParseDRMTree (drm.go) and carried in GPUInfo.Model. probeGPUTFLOPS attributes
// peak FP32 compute from that identity via the known-model gpuTFLOPSCatalog
// (accel.go). An identity that is not in the catalog yields (0,false) so the
// caller falls through to the operator override label — we never fabricate a
// figure for an unknown card.

// probeGPUTFLOPS resolves GPU TFLOPS from the sysfs PCI identity catalog.
func probeGPUTFLOPS(g GPUInfo) (float64, bool) {
	return gpuTFLOPSFromCatalog(g)
}

// hostGPUInfo returns the host's real GPU identity on linux by enumerating the
// DRM sysfs tree, used by NewAcceleratorProbe.
func hostGPUInfo() (GPUInfo, error) {
	return NewDRMGPUReader().readGPUInfo()
}

// readGPUInfo reads the aggregated GPUInfo from this reader's sysfs base.
func (r *DRMGPUReader) readGPUInfo() (GPUInfo, error) {
	base := r.Base
	if base == "" {
		base = defaultSysfsBase
	}
	return ReadGPUInfo(base)
}
