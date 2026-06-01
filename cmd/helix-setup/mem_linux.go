//go:build linux

package main

import (
	"runtime"
	"syscall"
)

// platformMemoryMB returns total physical RAM in MiB on Linux by calling
// syscall.Sysinfo — a direct kernel call, no shell-out, no fabrication.
func platformMemoryMB() int {
	var si syscall.Sysinfo_t
	if err := syscall.Sysinfo(&si); err == nil {
		totalBytes := si.Totalram * uint64(si.Unit)
		if totalBytes > 0 {
			return int(totalBytes / (1024 * 1024))
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
