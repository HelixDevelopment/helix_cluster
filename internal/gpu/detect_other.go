//go:build !linux && !darwin

package gpu

import (
	"fmt"
	"runtime"
)

// detectGPUsPlatform reports that real GPU detection is not implemented for the
// current OS. Production must surface this honestly rather than substituting a
// mock inventory.
func detectGPUsPlatform() ([]*GPU, error) {
	return nil, fmt.Errorf("GPU detection unsupported on %s", runtime.GOOS)
}
