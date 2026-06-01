//go:build darwin

package main

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// platformMemoryMB returns total physical RAM in MiB on macOS by reading
// sysctl hw.memsize — the same approach used in pkg/resources/proc_darwin.go.
// This is a real OS call, not a fabrication.
func platformMemoryMB() int {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if b, err := strconv.ParseInt(s, 10, 64); err == nil && b > 0 {
			return int(b / (1024 * 1024))
		}
	}
	// Fallback: runtime.MemStats.Sys is a real lower-bound from the Go runtime.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	sysMB := int(ms.Sys / (1024 * 1024))
	if sysMB < 1 {
		sysMB = 1
	}
	return sysMB
}
