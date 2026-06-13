//go:build !darwin && !linux

package hwinventory

import (
	"context"
	"fmt"
	"runtime"
)

// detectCPU on an unsupported OS returns logical core count from the Go runtime
// (a real OS figure) and a clearly-labelled model. It does NOT fabricate a
// brand string. Total-memory probing has no portable equivalent here, so
// detectMemoryBytes reports an honest error rather than a fake value.
func detectCPU(ctx context.Context) (CPUInfo, error) {
	_ = ctx
	n := runtime.NumCPU()
	if n < 1 {
		n = 1
	}
	return CPUInfo{
		Model:         fmt.Sprintf("%s %s CPU", runtime.GOOS, runtime.GOARCH),
		LogicalCores:  n,
		PhysicalCores: n,
		Architecture:  runtime.GOARCH,
		Source:        "runtime.NumCPU",
	}, nil
}

// detectMemoryBytes has no portable real equivalent on unsupported platforms;
// returning an honest error (not a stub value) keeps the CLAUDE-2 no-fake-PASS
// guarantee. Supported hosts (darwin/linux) use their real probes above.
func detectMemoryBytes(ctx context.Context) (int64, string, error) {
	_ = ctx
	return 0, "", fmt.Errorf("hwinventory: total-memory probe not implemented for GOOS=%s", runtime.GOOS)
}
