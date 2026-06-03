package resources

import (
	"encoding/json"
	"testing"
	"time"
)

// TestGPUInfo_HasTFLOPSField proves GPUInfo gained a TFLOPS field that was
// previously absent (HXC-1300 closure (b)). It round-trips through JSON to
// confirm the field is real and serialized, not a phantom.
//
// Mutation killed: removing GPUInfo.TFLOPS (or its json tag) fails to compile /
// fails the JSON assertion below.
func TestGPUInfo_HasTFLOPSField(t *testing.T) {
	g := GPUInfo{Count: 1, Model: "0x10de:0x2684", TFLOPS: 82.6}
	if g.TFLOPS != 82.6 {
		t.Fatalf("GPUInfo.TFLOPS = %v, want 82.6", g.TFLOPS)
	}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back GPUInfo
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.TFLOPS != 82.6 {
		t.Fatalf("round-tripped TFLOPS = %v, want 82.6 (json=%s)", back.TFLOPS, b)
	}
}

// TestGPUInfo_TFLOPS_BackwardCompat proves the field is additive: a legacy
// GPUInfo literal with no TFLOPS still defaults to 0 and the field is omitted
// from JSON (omitempty), so existing consumers see no shape change.
func TestGPUInfo_TFLOPS_BackwardCompat(t *testing.T) {
	g := GPUInfo{Count: 1, Model: "0x10de:0x2204"}
	if g.TFLOPS != 0 {
		t.Fatalf("legacy GPUInfo.TFLOPS = %v, want 0", g.TFLOPS)
	}
	b, _ := json.Marshal(g)
	if got := string(b); contains(got, "tflops") {
		t.Fatalf("zero TFLOPS must be omitted from JSON, got %s", got)
	}
}

// TestReadAccelerators_OverrideLabel is HXC-1300 closure (a): an override-labeled
// NPU yields NPUTops>0, and FPGA/QPU overrides are honored verbatim.
//
// Mutation killed: a ReadAccelerators that ignores the label source (returns a
// zero Accelerators) fails every assertion here.
func TestReadAccelerators_OverrideLabel(t *testing.T) {
	src := MapLabelSource{
		LabelNPUTops:           "275",
		LabelFPGALogicElements: "1200000",
		LabelQPUPresent:        "true",
	}
	a, err := ReadAccelerators(src)
	if err != nil {
		t.Fatalf("ReadAccelerators: %v", err)
	}
	if a.NPUTops != 275 {
		t.Errorf("NPUTops = %v, want 275 (override label not honored)", a.NPUTops)
	}
	if a.FPGALogicElements != 1200000 {
		t.Errorf("FPGALogicElements = %v, want 1200000", a.FPGALogicElements)
	}
	if !a.QPUPresent {
		t.Errorf("QPUPresent = false, want true")
	}
}

// TestReadAccelerators_Unset proves an absent label leaves the field at zero
// (no fabrication) — the honest "no accelerator declared" answer.
func TestReadAccelerators_Unset(t *testing.T) {
	a, err := ReadAccelerators(MapLabelSource{})
	if err != nil {
		t.Fatalf("ReadAccelerators: %v", err)
	}
	if a.NPUTops != 0 || a.FPGALogicElements != 0 || a.QPUPresent {
		t.Fatalf("unset labels must yield zero Accelerators, got %+v", a)
	}
}

// TestReadAccelerators_GarbledIsError proves a typo'd override is surfaced as an
// error, never silently coerced to zero (anti-bluff).
func TestReadAccelerators_GarbledIsError(t *testing.T) {
	if _, err := ReadAccelerators(MapLabelSource{LabelNPUTops: "fast"}); err == nil {
		t.Fatal("expected error for non-numeric NPU override, got nil")
	}
	if _, err := ReadAccelerators(MapLabelSource{LabelFPGALogicElements: "-5"}); err == nil {
		t.Fatal("expected error for negative FPGA override, got nil")
	}
	if _, err := ReadAccelerators(MapLabelSource{LabelQPUPresent: "maybe"}); err == nil {
		t.Fatal("expected error for non-boolean QPU override, got nil")
	}
}

// TestGPUTFLOPSFromCatalog proves the linux/portable PCI catalog attributes real
// peak compute and that distinct known cards have distinct values (so a flattened
// catalog is detected). Unknown identities return ok=false.
func TestGPUTFLOPSFromCatalog(t *testing.T) {
	cases := []struct {
		model string
		want  float64
	}{
		{"0x10de:0x2330", 67.0},        // H100
		{"0x10de:0x2684", 82.6},        // RTX 4090
		{"0x10de:0x2684 x 2", 82.6},    // " x N" suffix resolves to leading card
		{"0x1002:0x73bf", 23.0},        // RX 6900 XT
		{"0x8086:0x9a60", 2.1},         // Intel iGPU
	}
	for _, tc := range cases {
		got, ok := gpuTFLOPSFromCatalog(GPUInfo{Model: tc.model})
		if !ok {
			t.Errorf("gpuTFLOPSFromCatalog(%q): ok=false, want a value", tc.model)
			continue
		}
		if got != tc.want {
			t.Errorf("gpuTFLOPSFromCatalog(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
	if _, ok := gpuTFLOPSFromCatalog(GPUInfo{Model: "0xdead:0xbeef"}); ok {
		t.Error("unknown identity must return ok=false")
	}
	if _, ok := gpuTFLOPSFromCatalog(GPUInfo{Model: ""}); ok {
		t.Error("empty model must return ok=false")
	}
}

// TestResolveGPUTFLOPS_OverrideFallback proves the precedence chain: when the
// host probe and catalog cannot attribute a value (unknown identity), the
// operator override label is honored.
func TestResolveGPUTFLOPS_OverrideFallback(t *testing.T) {
	// Unknown GPU identity so probeGPUTFLOPS/catalog return no value, forcing the
	// override path on every OS.
	g := GPUInfo{Model: "0xdead:0xbeef"}
	src := MapLabelSource{LabelGPUTFLOPS: "12.5"}
	v, err := resolveGPUTFLOPS(g, src)
	if err != nil {
		t.Fatalf("resolveGPUTFLOPS: %v", err)
	}
	if v != 12.5 {
		t.Fatalf("resolveGPUTFLOPS override = %v, want 12.5", v)
	}

	// No override and unknown identity -> honest zero.
	v0, err := resolveGPUTFLOPS(g, MapLabelSource{})
	if err != nil {
		t.Fatalf("resolveGPUTFLOPS: %v", err)
	}
	if v0 != 0 {
		t.Fatalf("resolveGPUTFLOPS with no signal = %v, want 0", v0)
	}
}

// TestResolveGPUTFLOPS_CatalogBeatsOverride proves a known card's catalog value
// is used in preference to an override (the override is a fallback, not an
// override of a real probe).
func TestResolveGPUTFLOPS_CatalogBeatsOverride(t *testing.T) {
	g := GPUInfo{Model: "0x10de:0x2330"} // H100 -> 67.0
	src := MapLabelSource{LabelGPUTFLOPS: "1.0"}
	v, err := resolveGPUTFLOPS(g, src)
	if err != nil {
		t.Fatalf("resolveGPUTFLOPS: %v", err)
	}
	if v != 67.0 {
		t.Fatalf("resolveGPUTFLOPS = %v, want 67.0 (catalog must beat override)", v)
	}
}

// TestAcceleratorProbe_Read_OverrideNPUAndGPU is the end-to-end closure test: an
// AcceleratorProbe driven by override labels reports npu_tops>0 and gpu_tflops,
// proving the feature is usable as an operator would use it.
func TestAcceleratorProbe_Read_OverrideNPUAndGPU(t *testing.T) {
	labels := MapLabelSource{
		LabelNPUTops:    "400",
		LabelGPUTFLOPS:  "30",
		LabelQPUPresent: "1",
	}
	// Unknown GPU identity -> GPU TFLOPS must come from the override label.
	gpuFn := func() (GPUInfo, error) { return GPUInfo{Model: "0xdead:0xbeef"}, nil }
	p := NewAcceleratorProbeWith(labels, gpuFn)

	res, err := p.Read("node-1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.Accelerators.NPUTops <= 0 {
		t.Errorf("npu_tops = %v, want > 0 (override-labeled NPU)", res.Accelerators.NPUTops)
	}
	if res.GPU.TFLOPS <= 0 {
		t.Errorf("gpu_tflops = %v, want > 0 (override label)", res.GPU.TFLOPS)
	}
	if !res.Accelerators.QPUPresent {
		t.Errorf("qpu_present = false, want true")
	}
}

// TestAcceleratorProbe_Read_KnownGPUNoOverride proves the probe attributes
// TFLOPS to a known card with NO override present (real catalog signal).
func TestAcceleratorProbe_Read_KnownGPUNoOverride(t *testing.T) {
	gpuFn := func() (GPUInfo, error) { return GPUInfo{Count: 1, Model: "0x10de:0x2684"}, nil }
	p := NewAcceleratorProbeWith(MapLabelSource{}, gpuFn)
	res, err := p.Read("node-2")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.GPU.TFLOPS != 82.6 {
		t.Fatalf("gpu_tflops = %v, want 82.6 (RTX 4090 catalog)", res.GPU.TFLOPS)
	}
}

// TestAcceleratorProbe_AggregatorMerge proves the full sink-side path: the probe
// wired into a real NodeAggregator alongside a GPU reader yields a snapshot whose
// GPU keeps its Count/Model AND gains TFLOPS, with NPU declared — the exact shape
// the scheduler consumes.
func TestAcceleratorProbe_AggregatorMerge(t *testing.T) {
	// A reader that enumerates the GPU (Count/Model) but no TFLOPS.
	gpuReader := readerFunc(func(nodeID string) (NodeResources, error) {
		return NodeResources{
			NodeID: nodeID,
			GPU:    GPUInfo{Count: 1, Model: "0x10de:0x2684"},
		}, nil
	})
	// The accelerator probe contributes TFLOPS (catalog) + NPU (override).
	probe := NewAcceleratorProbeWith(
		MapLabelSource{LabelNPUTops: "275"},
		func() (GPUInfo, error) { return GPUInfo{Model: "0x10de:0x2684"}, nil },
	)

	agg := NewNodeAggregator(time.Minute)
	agg.RegisterReader(gpuReader)
	agg.RegisterReader(probe)
	agg.RegisterNode("n1")
	if err := agg.Collect(); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	snap, ok := agg.GetNode("n1")
	if !ok {
		t.Fatal("node n1 missing after Collect")
	}
	g := snap.Resources.GPU
	if g.Count != 1 || g.Model != "0x10de:0x2684" {
		t.Errorf("GPU identity lost in merge: %+v", g)
	}
	if g.TFLOPS != 82.6 {
		t.Errorf("merged gpu_tflops = %v, want 82.6", g.TFLOPS)
	}
	if snap.Resources.Accelerators.NPUTops != 275 {
		t.Errorf("merged npu_tops = %v, want 275", snap.Resources.Accelerators.NPUTops)
	}
}

// contains is a tiny strings.Contains alias kept local to avoid an import churn.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// readerFunc adapts a function to the Reader interface for tests.
type readerFunc func(nodeID string) (NodeResources, error)

func (f readerFunc) Read(nodeID string) (NodeResources, error) { return f(nodeID) }
