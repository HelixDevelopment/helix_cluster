//go:build !darwin && !linux

package devicedetect

import (
	"context"
	"fmt"
	"runtime"
)

// detectGPUs is the honest fallback for platforms without a real GPU-probe
// implementation. Per HelixConstitution §11.4.3 it returns a typed,
// reason-bearing error instead of a fake empty PASS, so callers (and tests)
// can SKIP-with-reason rather than be misled into believing no GPU exists.
func detectGPUs(_ context.Context) ([]GPU, error) {
	return nil, fmt.Errorf("devicedetect: GPU detection not implemented for GOOS=%s (no real per-OS probe)", runtime.GOOS)
}
