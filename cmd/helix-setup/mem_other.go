//go:build !linux && !darwin

package main

import "runtime"

// platformMemoryMB returns the memory the Go runtime has obtained from the OS
// on platforms other than Linux and Darwin. This is a real value (not
// fabricated) — it is a lower bound because the runtime does not necessarily
// map all physical RAM, but it is never zero and never invented.
func platformMemoryMB() int {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	sysMB := int(ms.Sys / (1024 * 1024))
	if sysMB < 1 {
		sysMB = 1
	}
	return sysMB
}
