//go:build linux

package resources

import "fmt"

// defaultSysfsBase is the real sysfs mount point on Linux. The DRM reader looks
// for cards under <base>/class/drm.
const defaultSysfsBase = "/sys"

// DRMGPUReader enumerates GPUs from the Linux DRM sysfs tree and exposes them
// through the shared Reader interface so it can be registered with a
// NodeAggregator. The base path is injectable: tests construct one over a fake
// tree under t.TempDir(); production code uses the default "/sys".
type DRMGPUReader struct {
	// Base is the sysfs base path (default "/sys" when empty).
	Base string
}

// NewDRMGPUReader creates a DRMGPUReader rooted at the real sysfs base ("/sys").
func NewDRMGPUReader() *DRMGPUReader {
	return &DRMGPUReader{Base: defaultSysfsBase}
}

// NewDRMGPUReaderAt creates a DRMGPUReader rooted at an arbitrary base path,
// used by tests to point at a fake sysfs tree.
func NewDRMGPUReaderAt(base string) *DRMGPUReader {
	if base == "" {
		base = defaultSysfsBase
	}
	return &DRMGPUReader{Base: base}
}

// Read implements Reader, populating only the GPU field of NodeResources from
// the DRM sysfs tree. Other fields are left zero so the aggregator's merge
// overlays GPU data without clobbering CPU/memory readers.
func (r *DRMGPUReader) Read(nodeID string) (NodeResources, error) {
	base := r.Base
	if base == "" {
		base = defaultSysfsBase
	}
	gpu, err := ReadGPUInfo(base)
	if err != nil {
		return NodeResources{}, fmt.Errorf("drm gpu read: %w", err)
	}
	return NodeResources{
		NodeID: nodeID,
		GPU:    gpu,
	}, nil
}

// Ensure DRMGPUReader satisfies the Reader interface.
var _ Reader = (*DRMGPUReader)(nil)
