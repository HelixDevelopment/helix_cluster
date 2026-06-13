package hwinventory

import (
	"context"
	"encoding/json"
	"testing"
)

// TestCollect_HostReport exercises Collect() against the REAL host and asserts
// the unified inventory is well-formed and end-user-meaningful: a CPU with a
// non-empty model and >= 1 core, total memory > 0, and (on this Apple host)
// at least one GPU and the NPU. It also renders and re-parses the JSON report,
// printing it to the test log as host capability evidence.
//
// Per-OS oracle cross-checks (sysctl on macOS, /proc on Linux) live in the
// build-tagged *_oracle_*_test.go files and run as subtests of this binary.
func TestCollect_HostReport(t *testing.T) {
	inv, err := Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}

	if inv.Host == "" {
		t.Error("Host is empty")
	}
	if inv.CPU.LogicalCores < 1 {
		t.Errorf("CPU.LogicalCores = %d, want >= 1", inv.CPU.LogicalCores)
	}
	if inv.CPU.PhysicalCores < 1 {
		t.Errorf("CPU.PhysicalCores = %d, want >= 1", inv.CPU.PhysicalCores)
	}
	if inv.CPU.Model == "" {
		t.Error("CPU.Model is empty (must be a real, non-empty CPU model)")
	}
	if inv.MemoryBytes <= 0 {
		t.Errorf("MemoryBytes = %d, want > 0", inv.MemoryBytes)
	}

	// Render JSON and round-trip it to prove all fields marshal cleanly.
	raw, err := inv.JSON()
	if err != nil {
		t.Fatalf("Inventory.JSON() error: %v", err)
	}
	var back Inventory
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("re-parse inventory JSON: %v", err)
	}
	if back.MemoryBytes != inv.MemoryBytes || back.CPU.Model != inv.CPU.Model {
		t.Errorf("JSON round-trip mismatch: got mem=%d model=%q want mem=%d model=%q",
			back.MemoryBytes, back.CPU.Model, inv.MemoryBytes, inv.CPU.Model)
	}

	t.Logf("HOST CAPABILITY REPORT (real inventory):\n%s", string(raw))
	t.Logf("summary: host=%s os=%s/%s cpu=%q logical=%d physical=%d mem=%d bytes (%.2f GiB) gpus=%d npus=%d fpgas=%d",
		inv.Host, inv.OS, inv.Arch, inv.CPU.Model, inv.CPU.LogicalCores, inv.CPU.PhysicalCores,
		inv.MemoryBytes, float64(inv.MemoryBytes)/(1<<30), len(inv.GPUs), len(inv.NPUs), len(inv.FPGAs))

	for i, g := range inv.GPUs {
		t.Logf("  GPU[%d]: vendor=%q model=%q api=%q cores=%d unified=%v mem=%d source=%q",
			i, g.Vendor, g.Model, g.API, g.Cores, g.Unified, g.MemoryBytes, g.Source)
		if g.Vendor == "" || g.Model == "" {
			t.Errorf("GPU[%d] has empty vendor/model: %+v", i, g)
		}
	}
	for i, n := range inv.NPUs {
		t.Logf("  NPU[%d]: vendor=%q model=%q runtime=%q tops=%.1f source=%q",
			i, n.Vendor, n.Model, n.Runtime, n.TOPS, n.Source)
		if n.Vendor == "" || n.Model == "" {
			t.Errorf("NPU[%d] has empty vendor/model: %+v", i, n)
		}
	}

	// FPGAs: the inventory MUST carry a non-nil FPGAs slice so the report
	// renders [] (not null). On this Apple-silicon host the fpgadetect oracle
	// confirms there is no FPGA, so the slice is empty here — but any FPGA that
	// IS reported must be a real, fully-identified device (non-empty vendor).
	if inv.FPGAs == nil {
		t.Error("FPGAs slice is nil (Collect must populate a non-nil slice via fpgadetect.DetectFPGAs)")
	}
	for i, f := range inv.FPGAs {
		t.Logf("  FPGA[%d]: vendor=%q model=%q family=%q iface=%q pciVendor=%q source=%q",
			i, f.Vendor, f.Model, f.Family, f.Interface, f.PCIVendorID, f.Source)
		if f.Vendor == "" {
			t.Errorf("FPGA[%d] has empty vendor: %+v", i, f)
		}
	}
}

// TestInventory_JSON_AllFields confirms the JSON report includes every
// top-level field name an end user / downstream consumer relies on.
func TestInventory_JSON_AllFields(t *testing.T) {
	inv, err := Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}
	raw, err := inv.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, key := range []string{"host", "os", "arch", "cpu", "memory_bytes", "gpus", "npus", "fpgas"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON report missing top-level field %q", key)
		}
	}
	var cpu map[string]json.RawMessage
	if err := json.Unmarshal(m["cpu"], &cpu); err != nil {
		t.Fatalf("unmarshal cpu object: %v", err)
	}
	for _, key := range []string{"model", "logical_cores", "physical_cores", "architecture"} {
		if _, ok := cpu[key]; !ok {
			t.Errorf("cpu object missing field %q", key)
		}
	}
}
