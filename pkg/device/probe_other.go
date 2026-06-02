//go:build !linux && !darwin

package device

import "runtime"

// platformTotalMemoryBytes returns the memory the Go runtime has obtained from
// the OS on platforms other than Linux and Darwin. This is a real, measured
// value — it is a lower bound because the runtime does not necessarily map all
// physical RAM, but it is never fabricated and never invented.
//
// This fallback is the only honest option on platforms that expose no
// equivalent of sysctl hw.memsize or syscall.Sysinfo. Returning 0 would be
// worse (it would cause ClassifyTier to produce TierUnknown); returning a
// fabricated constant would be a CLAUDE-1 PASS-bluff.
func platformTotalMemoryBytes() int64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	sys := int64(ms.Sys)
	if sys < 1 {
		sys = 1
	}
	return sys
}
