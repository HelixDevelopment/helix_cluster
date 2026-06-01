//go:build !linux && !darwin

package resources

// DRMGPUReader is the portable no-GPU fallback on non-Linux platforms, where the
// /sys/class/drm DRM sysfs tree does not exist. It keeps the package building
// everywhere while exposing the same API surface as the Linux reader; the parser
// itself (ParseDRMTree / ReadGPUInfo in drm.go) remains fully testable on any OS
// against an injected base path.
type DRMGPUReader struct {
	// Base mirrors the Linux field so callers can construct the reader
	// identically across platforms; it is honored by ReadAt-style helpers.
	Base string
}

// NewDRMGPUReader creates a fallback reader. On non-Linux hosts there is no DRM
// sysfs tree, so Read reports zero GPUs.
func NewDRMGPUReader() *DRMGPUReader {
	return &DRMGPUReader{}
}

// NewDRMGPUReaderAt creates a fallback reader pinned to a base path. This lets
// the same constructor be used in cross-OS tests that point at a fake tree.
func NewDRMGPUReaderAt(base string) *DRMGPUReader {
	return &DRMGPUReader{Base: base}
}

// Read implements Reader. With an empty Base it reports zero GPUs (the honest
// answer on a host without a DRM sysfs tree). With a non-empty Base it parses
// that tree via the portable parser, so tests can still exercise real
// enumeration on darwin against a t.TempDir() fixture.
func (r *DRMGPUReader) Read(nodeID string) (NodeResources, error) {
	if r.Base == "" {
		return NodeResources{NodeID: nodeID}, nil
	}
	gpu, err := ReadGPUInfo(r.Base)
	if err != nil {
		return NodeResources{}, err
	}
	return NodeResources{NodeID: nodeID, GPU: gpu}, nil
}

// Ensure DRMGPUReader satisfies the Reader interface.
var _ Reader = (*DRMGPUReader)(nil)
