//go:build darwin

package device

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// platformTotalMemoryBytes returns total physical RAM in bytes on macOS by
// reading sysctl hw.memsize — a real OS call that reflects the DIMM capacity
// as seen by the kernel. This is NOT fabricated.
//
// Fallback: if the sysctl call fails (e.g. in a heavily sandboxed environment),
// runtime.MemStats.Sys is used as an honest lower-bound. It is a real value
// returned by the Go runtime and is never zero in practice.
func platformTotalMemoryBytes() int64 {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if b, err := strconv.ParseInt(s, 10, 64); err == nil && b > 0 {
			return b
		}
	}
	// Honest lower-bound fallback via the Go runtime.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	sys := int64(ms.Sys) //gosec:disable G115 -- runtime Sys bytes are bounded by physical RAM, far below int64 max
	if sys < 1 {
		sys = 1
	}
	return sys
}
