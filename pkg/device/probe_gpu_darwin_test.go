//go:build darwin

package device

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// oracleGPUModel runs a direct, independent `system_profiler -json
// SPDisplaysDataType` and returns the first GPU's model (sppci_model, falling
// back to _name) plus whether it advertises a Metal family. It returns ok=false
// only if system_profiler cannot be executed (e.g. a stripped/sandboxed host).
func oracleGPUModel(t *testing.T) (model string, metal bool, ok bool) {
	t.Helper()
	out, err := exec.Command("system_profiler", "-json", "SPDisplaysDataType").Output()
	if err != nil {
		return "", false, false
	}
	var report struct {
		Displays []struct {
			Name        string `json:"_name"`
			Model       string `json:"sppci_model"`
			MetalFamily string `json:"spdisplays_mtlgpufamilysupport"`
		} `json:"SPDisplaysDataType"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("oracle: system_profiler JSON did not parse: %v", err)
	}
	for _, d := range report.Displays {
		m := strings.TrimSpace(d.Model)
		if m == "" {
			m = strings.TrimSpace(d.Name)
		}
		if m == "" {
			continue
		}
		return m, strings.TrimSpace(d.MetalFamily) != "", true
	}
	return "", false, false
}

// TestProbeDarwinGPU_RealHostMatchesOracle is the §7.1 / CLAUDE-2 real-host
// proof. It runs the genuine darwin GPU probe (system_profiler + sysctl) on THIS
// macOS host and asserts the parsed GPU model is non-empty AND equals what a
// direct, independent system_profiler invocation reports. It also asserts the
// Apple-Silicon unified-memory VRAM is positive. Skip ONLY if system_profiler is
// genuinely unavailable (justified, per §11.4.3 SKIP-with-reason) — never to
// hide an unimplemented port.
//
// §1.1 mutation: change the struct tag on spDisplayEntry.Model/Name (the JSON
// field path) in probe_gpu_darwin.go to a wrong key (e.g. `json:"_xname"`) ->
// gpuName returns "" for every entry, every entry is skipped, the probe reports
// an empty Model and zero Count, and this test FAILS on the non-empty + equality
// assertions. Restore the tags to pass again.
func TestProbeDarwinGPU_RealHostMatchesOracle(t *testing.T) {
	if _, err := exec.LookPath("system_profiler"); err != nil {
		t.Skip("system_profiler not available on PATH; cannot exercise real darwin GPU probe")
	}

	oracleModel, oracleMetal, ok := oracleGPUModel(t)
	if !ok {
		t.Skip("system_profiler reported no GPU on this host; nothing to assert against")
	}

	res, err := probeDarwinGPU(runSystemProfilerDisplays, darwinTotalMemoryBytes)
	if err != nil {
		t.Fatalf("probeDarwinGPU failed on real host: %v", err)
	}

	if res.Count <= 0 {
		t.Fatalf("real-host probe reported %d GPUs; expected at least 1", res.Count)
	}
	if res.Model == "" {
		t.Fatal("real-host probe returned an empty GPU model; the JSON field path is broken")
	}
	if res.Model != oracleModel {
		t.Fatalf("probe Model = %q, independent system_profiler oracle = %q; must match",
			res.Model, oracleModel)
	}
	if res.MetalSupport != oracleMetal {
		t.Errorf("probe MetalSupport = %v, oracle = %v", res.MetalSupport, oracleMetal)
	}
	// Apple-Silicon GPUs share unified memory and must report a positive pool.
	if strings.HasPrefix(res.Model, "Apple ") && res.VRAMBytes <= 0 {
		t.Errorf("Apple GPU %q reported non-positive VRAM %d (unified-memory probe broken)",
			res.Model, res.VRAMBytes)
	}
	t.Logf("real-host darwin GPU: model=%q count=%d vramBytes=%d metal=%v",
		res.Model, res.Count, res.VRAMBytes, res.MetalSupport)
}

// TestProbe_RealHostGPUWiredIntoDescriptor proves the public device.Probe()
// entry point actually surfaces the real GPU into the DeviceDescriptor on this
// macOS host (GPUCount > 0, GPUVRAMBytes > 0 for Apple Silicon). A regression
// that leaves Probe() hard-coding GPUCount: 0 fails here.
func TestProbe_RealHostGPUWiredIntoDescriptor(t *testing.T) {
	if _, err := exec.LookPath("system_profiler"); err != nil {
		t.Skip("system_profiler not available; cannot validate GPU wiring")
	}
	if _, _, ok := oracleGPUModel(t); !ok {
		t.Skip("no GPU reported by system_profiler on this host")
	}

	d, err := Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() error: %v", err)
	}
	if d.GPUCount <= 0 {
		t.Fatalf("Probe().GPUCount = %d on a real Mac with a GPU; Probe is not wired to platformProbeGPU", d.GPUCount)
	}
	if !d.IsAccelerated() {
		t.Errorf("Probe().IsAccelerated() = false despite GPUCount=%d", d.GPUCount)
	}
}

// TestParseVRAMBytes is a focused unit guard for the VRAM unit parser, so a
// regression in unit handling is caught without depending on the host.
func TestParseVRAMBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"8 GB", 8 * 1024 * 1024 * 1024},
		{"4096 MB", 4096 * 1024 * 1024},
		{"16384", 16384 * 1024 * 1024}, // bare number => MB
		{"", 0},
		{"n/a", 0},
	}
	for _, tc := range cases {
		if got := parseVRAMBytes(tc.in); got != tc.want {
			t.Errorf("parseVRAMBytes(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
