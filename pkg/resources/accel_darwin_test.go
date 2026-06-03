//go:build darwin

package resources

import "testing"

const appleM3ProSPFixture = `Graphics/Displays:

    Apple M3 Pro:

      Chipset Model: Apple M3 Pro
      Type: GPU
      Bus: Built-In
      Total Number of Cores: 14
      Vendor: Apple (0x106b)
      Metal Support: Metal 3
`

// TestParseAppleGPUCores proves the system_profiler core-count extraction works
// on real Apple Silicon output. Mutation killed: a parser that ignores
// "Total Number of Cores" (returns 0) fails the count assertion.
func TestParseAppleGPUCores(t *testing.T) {
	cores, ok := parseAppleGPUCores(appleM3ProSPFixture)
	if !ok {
		t.Fatal("parseAppleGPUCores: ok=false, want a core count")
	}
	if cores != 14 {
		t.Fatalf("cores = %d, want 14", cores)
	}

	if _, ok := parseAppleGPUCores("Graphics/Displays:\n"); ok {
		t.Error("no-core output must yield ok=false")
	}
}

// TestTFLOPSFromAppleCores proves the peak-FP32 derivation scales with the real
// core count and matches the documented formula (cores*128*2*1.4/1000).
//
// Mutation killed: replacing tflopsFromAppleCores with a constant fails the
// "14-core != 40-core" inequality and the exact-value check below.
func TestTFLOPSFromAppleCores(t *testing.T) {
	got14 := tflopsFromAppleCores(14)
	want14 := 14.0 * appleALUsPerGPUCore * 2 * appleGPUClockGHz / 1000.0
	if got14 != want14 {
		t.Fatalf("tflopsFromAppleCores(14) = %v, want %v", got14, want14)
	}
	if got14 <= 0 {
		t.Fatalf("14-core TFLOPS must be positive, got %v", got14)
	}

	got40 := tflopsFromAppleCores(40)
	if !(got40 > got14) {
		t.Fatalf("40-core (%v) must exceed 14-core (%v): derivation does not scale", got40, got14)
	}
	if tflopsFromAppleCores(0) != 0 {
		t.Fatal("zero cores must yield 0 TFLOPS (no fabrication)")
	}
}

// TestProbeGPUTFLOPS_DarwinAppleSilicon proves the darwin probe derives a real,
// positive TFLOPS from injected Apple Silicon system_profiler output (no PCI
// identity present). This is the CLAUDE-2 real darwin path under test.
func TestProbeGPUTFLOPS_DarwinAppleSilicon(t *testing.T) {
	orig := darwinSystemProfilerFn
	t.Cleanup(func() { darwinSystemProfilerFn = orig })
	darwinSystemProfilerFn = func() (string, error) { return appleM3ProSPFixture, nil }

	// Apple GPU has no vendor:device, so g.Model is the Apple chipset name.
	v, ok := probeGPUTFLOPS(GPUInfo{Model: "Apple M3 Pro"})
	if !ok {
		t.Fatal("probeGPUTFLOPS: ok=false on Apple Silicon, want a derived value")
	}
	want := tflopsFromAppleCores(14)
	if v != want {
		t.Fatalf("probeGPUTFLOPS = %v, want %v (14-core derivation)", v, want)
	}
}

// TestResolveGPUTFLOPS_DarwinProbeBeatsOverride proves the darwin hardware probe
// takes precedence over an override label: with a real Apple GPU read, the
// derived value is used even when an override is set.
func TestResolveGPUTFLOPS_DarwinProbeBeatsOverride(t *testing.T) {
	orig := darwinSystemProfilerFn
	t.Cleanup(func() { darwinSystemProfilerFn = orig })
	darwinSystemProfilerFn = func() (string, error) { return appleM3ProSPFixture, nil }

	v, err := resolveGPUTFLOPS(GPUInfo{Model: "Apple M3 Pro"}, MapLabelSource{LabelGPUTFLOPS: "999"})
	if err != nil {
		t.Fatalf("resolveGPUTFLOPS: %v", err)
	}
	if v == 999 {
		t.Fatal("override must NOT beat the real darwin probe")
	}
	if v != tflopsFromAppleCores(14) {
		t.Fatalf("resolveGPUTFLOPS = %v, want %v", v, tflopsFromAppleCores(14))
	}
}
