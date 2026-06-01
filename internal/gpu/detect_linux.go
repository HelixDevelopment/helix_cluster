//go:build linux

package gpu

// detectGPUsPlatform reads real NVIDIA GPU descriptors from the kernel-exposed
// /proc tree on Linux. It reuses detectGPUsFrom, the platform-independent
// parser, pointed at nvidiaProcPath.
func detectGPUsPlatform() ([]*GPU, error) {
	return detectGPUsFrom(nvidiaProcPath)
}
