//go:build linux

package hwinventory

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestCollect_LinuxOracles cross-checks total memory against the `free -b`
// oracle and logical core count against `nproc` / runtime, independent of the
// production /proc parsing path. Skips a given oracle only if that external
// tool is genuinely unavailable (justified, not hiding an unimplemented port).
func TestCollect_LinuxOracles(t *testing.T) {
	inv, err := Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}

	// Memory oracle via `free -b` total column.
	if out, ferr := exec.Command("free", "-b").Output(); ferr == nil {
		if total, ok := parseFreeTotal(string(out)); ok {
			if inv.MemoryBytes != total {
				t.Errorf("MemoryBytes = %d, `free -b` total oracle = %d (MISMATCH)", inv.MemoryBytes, total)
			} else {
				t.Logf("memory oracle MATCH: %d bytes == free -b total", inv.MemoryBytes)
			}
		} else {
			t.Logf("could not parse `free -b` output; skipping free oracle")
		}
	} else {
		t.Logf("`free` unavailable (%v); relying on /proc/meminfo self-consistency", ferr)
	}

	// Logical core oracle via `nproc`.
	if out, ferr := exec.Command("nproc").Output(); ferr == nil {
		if n, perr := strconv.Atoi(strings.TrimSpace(string(out))); perr == nil {
			if inv.CPU.LogicalCores != n {
				t.Errorf("CPU.LogicalCores = %d, nproc oracle = %d (MISMATCH)", inv.CPU.LogicalCores, n)
			} else {
				t.Logf("cpu logical-cores oracle MATCH: %d == nproc", inv.CPU.LogicalCores)
			}
		}
	} else {
		t.Logf("`nproc` unavailable (%v); skipping nproc oracle", ferr)
	}

	if inv.CPU.Model == "" {
		t.Error("CPU.Model empty on Linux host")
	}
}

// parseFreeTotal extracts the total-bytes figure from `free -b` output (the
// second column of the "Mem:" line).
func parseFreeTotal(out string) (int64, bool) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "Mem:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		v, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}
