//go:build linux

package device

import (
	"runtime"
	"syscall"
)

// platformTotalMemoryBytes returns total physical RAM in bytes on Linux by
// calling syscall.Sysinfo — a direct kernel syscall, no shell-out, no
// fabrication. Totalram * Unit gives the byte-accurate physical RAM size.
//
// Fallback: if the Sysinfo call fails (e.g. in a container with restricted
// syscalls), runtime.MemStats.Sys is used as an honest lower-bound.
func platformTotalMemoryBytes() int64 {
	var si syscall.Sysinfo_t
	if err := syscall.Sysinfo(&si); err == nil {
		total := int64(si.Totalram) * int64(si.Unit)
		if total > 0 {
			return total
		}
	}
	// Honest lower-bound fallback via the Go runtime.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	sys := int64(ms.Sys)
	if sys < 1 {
		sys = 1
	}
	return sys
}
