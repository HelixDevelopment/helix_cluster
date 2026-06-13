//go:build darwin

package hwinventory

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// sysctlOracle runs `sysctl -n <key>` directly as an INDEPENDENT oracle (the
// production code uses sysctl too, but the test re-derives the value via its
// own exec path and string parsing, so a hardcoded/mutated production value is
// caught by the equality assertions below).
func sysctlOracle(t *testing.T, key string) string {
	t.Helper()
	out, err := exec.Command("sysctl", "-n", key).Output()
	if err != nil {
		t.Fatalf("oracle sysctl -n %s: %v", key, err)
	}
	return strings.TrimSpace(string(out))
}

// TestCollect_DarwinOracles cross-checks the collected CPU model, logical core
// count, and total memory against the macOS sysctl oracles, and asserts the
// Apple GPU + Apple Neural Engine are present on this host.
func TestCollect_DarwinOracles(t *testing.T) {
	inv, err := Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}

	// --- Memory oracle: hw.memsize must match exactly. ---
	memOracleStr := sysctlOracle(t, "hw.memsize")
	memOracle, err := strconv.ParseInt(memOracleStr, 10, 64)
	if err != nil {
		t.Fatalf("parse hw.memsize oracle %q: %v", memOracleStr, err)
	}
	if inv.MemoryBytes != memOracle {
		t.Errorf("MemoryBytes = %d, sysctl hw.memsize oracle = %d (MISMATCH)", inv.MemoryBytes, memOracle)
	} else {
		t.Logf("memory oracle MATCH: %d bytes == sysctl hw.memsize", inv.MemoryBytes)
	}

	// --- CPU model oracle: machdep.cpu.brand_string must match exactly. ---
	modelOracle := sysctlOracle(t, "machdep.cpu.brand_string")
	if inv.CPU.Model != modelOracle {
		t.Errorf("CPU.Model = %q, sysctl machdep.cpu.brand_string oracle = %q (MISMATCH)", inv.CPU.Model, modelOracle)
	} else {
		t.Logf("cpu model oracle MATCH: %q == sysctl machdep.cpu.brand_string", inv.CPU.Model)
	}

	// --- CPU logical cores oracle: hw.ncpu must match exactly. ---
	ncpuStr := sysctlOracle(t, "hw.ncpu")
	ncpuOracle, err := strconv.Atoi(ncpuStr)
	if err != nil {
		t.Fatalf("parse hw.ncpu oracle %q: %v", ncpuStr, err)
	}
	if inv.CPU.LogicalCores != ncpuOracle {
		t.Errorf("CPU.LogicalCores = %d, sysctl hw.ncpu oracle = %d (MISMATCH)", inv.CPU.LogicalCores, ncpuOracle)
	} else {
		t.Logf("cpu logical-cores oracle MATCH: %d == sysctl hw.ncpu", inv.CPU.LogicalCores)
	}

	// --- GPU: Apple Silicon host must report >= 1 GPU, vendor Apple. ---
	if len(inv.GPUs) < 1 {
		t.Fatalf("expected >= 1 GPU on this Apple host, got 0")
	}
	foundAppleGPU := false
	for _, g := range inv.GPUs {
		if strings.EqualFold(g.Vendor, "Apple") {
			foundAppleGPU = true
		}
	}
	if !foundAppleGPU {
		t.Errorf("no Apple GPU among detected GPUs: %+v", inv.GPUs)
	} else {
		t.Logf("GPU oracle MATCH: Apple GPU present (%q)", inv.GPUs[0].Model)
	}

	// --- NPU: Apple Neural Engine must be present. ---
	if len(inv.NPUs) < 1 {
		t.Fatalf("expected >= 1 NPU (Apple Neural Engine) on this host, got 0")
	}
	foundANE := false
	for _, n := range inv.NPUs {
		if strings.Contains(strings.ToLower(n.Model), "neural engine") || strings.EqualFold(n.Vendor, "Apple") {
			foundANE = true
		}
	}
	if !foundANE {
		t.Errorf("Apple Neural Engine not found among NPUs: %+v", inv.NPUs)
	} else {
		t.Logf("NPU oracle MATCH: %q present", inv.NPUs[0].Model)
	}
}
