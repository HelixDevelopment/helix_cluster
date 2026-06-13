//go:build darwin

package hwinventory

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// detectCPU returns real CPU info on macOS using sysctl:
//
//   - machdep.cpu.brand_string for the model (e.g. "Apple M3 Pro")
//   - hw.ncpu             for logical core count
//   - hw.physicalcpu      for physical core count (falls back to logical)
func detectCPU(ctx context.Context) (CPUInfo, error) {
	model, err := sysctlString(ctx, "machdep.cpu.brand_string")
	if err != nil || model == "" {
		// Apple Silicon always exposes machdep.cpu.brand_string; treat absence
		// as a genuine probe failure rather than silently falling back.
		return CPUInfo{}, fmt.Errorf("sysctl machdep.cpu.brand_string: %w", err)
	}

	logical, err := sysctlInt(ctx, "hw.ncpu")
	if err != nil || logical < 1 {
		// Fall back to the Go runtime view, which is itself a real OS figure.
		logical = runtime.NumCPU()
	}

	physical, err := sysctlInt(ctx, "hw.physicalcpu")
	if err != nil || physical < 1 {
		physical = logical
	}

	return CPUInfo{
		Model:         model,
		LogicalCores:  logical,
		PhysicalCores: physical,
		Architecture:  runtime.GOARCH,
		Source:        "sysctl(machdep.cpu.brand_string,hw.ncpu,hw.physicalcpu)",
	}, nil
}

// detectMemoryBytes returns total physical memory on macOS via sysctl
// hw.memsize (bytes).
func detectMemoryBytes(ctx context.Context) (int64, string, error) {
	v, err := sysctlInt64(ctx, "hw.memsize")
	if err != nil {
		return 0, "", fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	if v <= 0 {
		return 0, "", fmt.Errorf("sysctl hw.memsize returned %d", v)
	}
	return v, "sysctl(hw.memsize)", nil
}

// sysctlString runs `sysctl -n <key>` and returns the trimmed string value.
func sysctlString(ctx context.Context, key string) (string, error) {
	out, err := exec.CommandContext(ctx, "sysctl", "-n", key).Output()
	if err != nil {
		return "", fmt.Errorf("exec sysctl -n %s: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// sysctlInt runs `sysctl -n <key>` and parses the result as an int.
func sysctlInt(ctx context.Context, key string) (int, error) {
	s, err := sysctlString(ctx, key)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parse sysctl %s value %q: %w", key, s, err)
	}
	return n, nil
}

// sysctlInt64 runs `sysctl -n <key>` and parses the result as an int64.
func sysctlInt64(ctx context.Context, key string) (int64, error) {
	s, err := sysctlString(ctx, key)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse sysctl %s value %q: %w", key, s, err)
	}
	return n, nil
}
