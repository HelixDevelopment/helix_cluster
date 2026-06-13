//go:build linux

package hwinventory

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// detectCPU returns real CPU info on Linux by parsing /proc/cpuinfo:
//
//   - the "model name" field for the human-readable model
//   - the count of "processor" entries for logical core count
//   - the distinct "core id" values within a physical id for physical cores
//     (falls back to logical when the topology fields are absent, e.g. on some
//     ARM kernels).
func detectCPU(ctx context.Context) (CPUInfo, error) {
	_ = ctx
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return CPUInfo{}, fmt.Errorf("open /proc/cpuinfo: %w", err)
	}
	defer f.Close()

	var (
		model        string
		logical      int
		coreIDs      = map[string]struct{}{}
		curPhysical  string
		seenCoreInfo bool
	)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		key, val, ok := splitProcLine(line)
		if !ok {
			continue
		}
		switch key {
		case "processor":
			logical++
		case "model name", "Model", "Hardware", "cpu model":
			if model == "" {
				model = val
			}
		case "physical id":
			curPhysical = val
		case "core id":
			seenCoreInfo = true
			coreIDs[curPhysical+"/"+val] = struct{}{}
		}
	}
	if err := sc.Err(); err != nil {
		return CPUInfo{}, fmt.Errorf("scan /proc/cpuinfo: %w", err)
	}

	if logical < 1 {
		logical = runtime.NumCPU()
	}
	if model == "" {
		// Some ARM /proc/cpuinfo lack "model name"; fall back to a stable,
		// non-empty descriptor rather than returning empty (which Collect
		// rejects). This is a real OS-derived value, not a fabricated one.
		model = fmt.Sprintf("%s %s CPU", runtime.GOOS, runtime.GOARCH)
	}

	physical := logical
	if seenCoreInfo && len(coreIDs) > 0 {
		physical = len(coreIDs)
	}

	return CPUInfo{
		Model:         model,
		LogicalCores:  logical,
		PhysicalCores: physical,
		Architecture:  runtime.GOARCH,
		Source:        "/proc/cpuinfo",
	}, nil
}

// detectMemoryBytes returns total physical memory on Linux by parsing the
// MemTotal field of /proc/meminfo (reported in kB).
func detectMemoryBytes(ctx context.Context) (int64, string, error) {
	_ = ctx
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, "", fmt.Errorf("open /proc/meminfo: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		// Expected form: "MemTotal:       16384256 kB"
		if len(fields) < 2 {
			return 0, "", fmt.Errorf("malformed MemTotal line: %q", line)
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, "", fmt.Errorf("parse MemTotal value %q: %w", fields[1], err)
		}
		return kb * 1024, "/proc/meminfo:MemTotal", nil
	}
	if err := sc.Err(); err != nil {
		return 0, "", fmt.Errorf("scan /proc/meminfo: %w", err)
	}
	return 0, "", fmt.Errorf("/proc/meminfo: MemTotal not found")
}

// splitProcLine splits a /proc/cpuinfo "key : value" line into trimmed key and
// value. It returns ok=false for separator/blank lines.
func splitProcLine(line string) (key, val string, ok bool) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	val = strings.TrimSpace(line[idx+1:])
	if key == "" {
		return "", "", false
	}
	return key, val, true
}
