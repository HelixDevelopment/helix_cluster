//go:build !linux && !darwin

package resources

// Portable GPU TFLOPS probe fallback (HXC-1300).
//
// On platforms without a DRM sysfs tree or a Metal/Apple GPU probe, the only
// hardware signal available is the PCI catalog (populated when a caller has
// already enumerated a GPU identity into g.Model via the portable parser). An
// unknown identity yields (0,false) so the caller falls through to the operator
// override label — never a fabricated figure.

// probeGPUTFLOPS resolves GPU TFLOPS from the known-model PCI catalog only.
func probeGPUTFLOPS(g GPUInfo) (float64, bool) {
	return gpuTFLOPSFromCatalog(g)
}

// hostGPUInfo reports no GPU on platforms without a portable enumeration path;
// the override label remains the way to declare GPU TFLOPS here.
func hostGPUInfo() (GPUInfo, error) {
	return GPUInfo{}, nil
}
