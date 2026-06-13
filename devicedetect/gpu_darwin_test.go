//go:build darwin

package devicedetect

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// TestDetectGPUs_Host_RealMachine calls DetectGPUs on THIS macOS host and
// asserts that a real GPU with a non-empty Vendor/Model is returned. It then
// cross-checks the detected model against an INDEPENDENT oracle: the raw
// `system_profiler SPDisplaysDataType` text output. If detection returned
// wrong/empty data, this test fails — there is no hardcoded expected value.
func TestDetectGPUs_Host_RealMachine(t *testing.T) {
	ctx := context.Background()

	gpus, err := DetectGPUs(ctx)
	if err != nil {
		t.Fatalf("DetectGPUs failed on host: %v", err)
	}
	if len(gpus) == 0 {
		t.Fatal("DetectGPUs returned no GPUs on a macOS host that has one")
	}

	g := gpus[0]
	t.Logf("detected GPU: vendor=%q model=%q api=%q memBytes=%d cores=%d unified=%v source=%q",
		g.Vendor, g.Model, g.API, g.MemoryBytes, g.Cores, g.Unified, g.Source)

	if strings.TrimSpace(g.Vendor) == "" {
		t.Error("detected GPU has empty Vendor")
	}
	if strings.TrimSpace(g.Model) == "" {
		t.Error("detected GPU has empty Model")
	}

	// Independent oracle: the textual system_profiler output. We assert the
	// detected model string appears verbatim in the oracle's "Chipset Model"
	// line. This is what makes an anti-bluff mutation (fake model) fail.
	oracle := systemProfilerOracle(t)
	t.Logf("oracle (system_profiler SPDisplaysDataType):\n%s", oracle)

	if !strings.Contains(oracle, g.Model) {
		t.Fatalf("CROSS-CHECK FAILED: detected model %q not present in system_profiler oracle output", g.Model)
	}

	// The oracle's Chipset Model line must match the detected model exactly.
	chipset := oracleChipsetModel(t, oracle)
	if chipset == "" {
		t.Fatal("oracle did not report a Chipset Model line")
	}
	if chipset != g.Model {
		t.Fatalf("CROSS-CHECK FAILED: oracle Chipset Model=%q != detected Model=%q", chipset, g.Model)
	}
	t.Logf("CROSS-CHECK OK: detected model %q matches oracle chipset %q", g.Model, chipset)

	// On Apple Silicon the GPU is unified-memory; assert MemoryBytes is sane
	// (matches sysctl hw.memsize) when unified.
	if g.Unified {
		want := sysctlMemsize(ctx)
		if want > 0 && g.MemoryBytes != want {
			t.Errorf("unified MemoryBytes=%d != sysctl hw.memsize=%d", g.MemoryBytes, want)
		}
	}
}

// systemProfilerOracle runs the human-readable system_profiler as an oracle
// independent of the -json path the detector consumes.
func systemProfilerOracle(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("system_profiler", "SPDisplaysDataType").Output()
	if err != nil {
		t.Fatalf("oracle system_profiler failed: %v", err)
	}
	return string(out)
}

// oracleChipsetModel extracts the value of the first "Chipset Model:" line.
func oracleChipsetModel(t *testing.T, oracle string) string {
	t.Helper()
	for _, line := range strings.Split(oracle, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Chipset Model:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Chipset Model:"))
		}
	}
	return ""
}

// TestGPUFromDisplay_JSONFixture exercises the JSON parser deterministically
// against a recorded system_profiler -json payload (the exact shape this host
// produces), independent of the live machine.
func TestGPUFromDisplay_JSONFixture(t *testing.T) {
	const payload = `{
      "SPDisplaysDataType": [
        {
          "_name": "Apple M3 Pro",
          "spdisplays_mtlgpufamilysupport": "spdisplays_metal3",
          "spdisplays_vendor": "sppci_vendor_Apple",
          "sppci_bus": "spdisplays_builtin",
          "sppci_cores": "14",
          "sppci_device_type": "spdisplays_gpu",
          "sppci_model": "Apple M3 Pro"
        }
      ]
    }`

	var parsed spDisplays
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if len(parsed.SPDisplaysDataType) != 1 {
		t.Fatalf("want 1 display entry, got %d", len(parsed.SPDisplaysDataType))
	}

	g := gpuFromDisplay(parsed.SPDisplaysDataType[0], 19327352832)
	if g.Vendor != "Apple" {
		t.Errorf("vendor: got %q want Apple", g.Vendor)
	}
	if g.Model != "Apple M3 Pro" {
		t.Errorf("model: got %q want Apple M3 Pro", g.Model)
	}
	if g.API != "Metal" {
		t.Errorf("api: got %q want Metal", g.API)
	}
	if g.Cores != 14 {
		t.Errorf("cores: got %d want 14", g.Cores)
	}
	if !g.Unified {
		t.Error("expected Unified=true for built-in bus")
	}
	if g.MemoryBytes != 19327352832 {
		t.Errorf("memBytes: got %d want 19327352832 (unified mem)", g.MemoryBytes)
	}
}

// TestParseVRAM covers the discrete-VRAM string parsing path.
func TestParseVRAM(t *testing.T) {
	cases := []struct {
		a, b string
		want int64
	}{
		{"8 GB", "", 8 << 30},
		{"", "1536 MB", 1536 << 20},
		{"", "", 0},
		{"garbage", "", 0},
	}
	for _, c := range cases {
		if got := parseVRAM(c.a, c.b); got != c.want {
			t.Errorf("parseVRAM(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}
