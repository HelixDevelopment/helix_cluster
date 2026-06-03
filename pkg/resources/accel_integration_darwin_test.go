//go:build integration && darwin

package resources

// Integration test for the darwin GPU-TFLOPS accelerator probe (HXC-1300).
//
// This runs against REAL macOS facilities: it calls system_profiler directly as
// an independent oracle and compares its GPU core count against the value the
// probe derives its TFLOPS from. No fakes. Guarded by //go:build integration so
// the hermetic unit run never reaches it. A SKIP is used only when the host has
// no Apple-Silicon-style integrated GPU core count to attribute (justified).

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"testing"

	"github.com/google/uuid"
)

// TestIntegration_AcceleratorProbe_DarwinGPUTFLOPS reads the real GPU via the
// probe and cross-checks the derived TFLOPS against an independent oracle
// (system_profiler core count fed through the same documented formula). A
// per-run UUID is logged for artifact traceability.
func TestIntegration_AcceleratorProbe_DarwinGPUTFLOPS(t *testing.T) {
	runID := uuid.New().String()
	t.Logf("run_id=%s", runID)

	// Oracle: call system_profiler directly and parse the core count ourselves.
	out, err := exec.Command("system_profiler", "SPDisplaysDataType").Output()
	if err != nil {
		t.Skipf("system_profiler unavailable (%v): cannot run the darwin GPU oracle", err)
	}
	oracleCores, ok := parseAppleGPUCores(string(out))
	if !ok {
		t.Skip("host exposes no Apple-style GPU core count (discrete/headless): " +
			"no integrated-GPU TFLOPS to attribute; override-label path covered by unit tests")
	}
	oracleTFLOPS := tflopsFromAppleCores(oracleCores)

	// Probe under test, reading the host's real GPU and no override.
	probe := NewAcceleratorProbeWith(MapLabelSource{}, hostGPUInfo)
	res, err := probe.Read(fmt.Sprintf("accel-darwin-%s", runID))
	if err != nil {
		t.Fatalf("AcceleratorProbe.Read: %v", err)
	}
	j, _ := json.MarshalIndent(res, "", "  ")
	t.Logf("accelerator snapshot:\n%s", string(j))
	t.Logf("oracle_gpu_cores=%d oracle_tflops=%v probe_tflops=%v", oracleCores, oracleTFLOPS, res.GPU.TFLOPS)

	if res.GPU.TFLOPS <= 0 {
		t.Fatalf("probe gpu_tflops = %v; must be positive on a host with an integrated GPU", res.GPU.TFLOPS)
	}
	if res.GPU.TFLOPS != oracleTFLOPS {
		t.Errorf("probe gpu_tflops=%v does not match oracle=%v (cores=%d): darwin derivation is broken",
			res.GPU.TFLOPS, oracleTFLOPS, oracleCores)
	}
}

// TestIntegration_AcceleratorProbe_OverrideNPULive proves the override-label path
// works end-to-end against a real probe instance (NPU has no hardware self-probe;
// an override-labeled NPU must surface as npu_tops>0).
func TestIntegration_AcceleratorProbe_OverrideNPULive(t *testing.T) {
	runID := uuid.New().String()
	t.Logf("run_id=%s", runID)

	probe := NewAcceleratorProbeWith(MapLabelSource{LabelNPUTops: "275"}, hostGPUInfo)
	res, err := probe.Read(fmt.Sprintf("accel-npu-%s", runID))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.Accelerators.NPUTops != 275 {
		t.Fatalf("npu_tops = %v, want 275 (override label)", res.Accelerators.NPUTops)
	}
}
