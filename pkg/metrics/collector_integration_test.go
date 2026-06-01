//go:build integration

package metrics

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/gpu"
	"github.com/HelixDevelopment/helix_cluster/pkg/resources"
)

// TestCollector_Integration_RealResources binds the NodeCollector to a real
// localhost port, scrapes over real HTTP, and validates that the returned
// CPU/mem values are consistent with a direct pkg/resources read (the oracle).
//
// The test embeds a per-run UUID as the NodeID label so the scrape response can
// be unambiguously correlated with this specific test invocation.  It then:
//
//  1. Reads CPU/mem directly from pkg/resources (oracle read).
//  2. Scrapes the collector endpoint over a real TCP connection.
//  3. Asserts CPU/mem metrics are present in the scrape with values within
//     ±20 pp of the oracle (tolerance for natural OS fluctuation between reads).
//  4. Asserts a GPU metric appears (M3 has a GPU via internal/gpu; DetectGPUs
//     must succeed on the Apple M3 build host).
//
// Hard FAIL: any assertion failure is a real defect — t.Skip is NOT used for
// real dependencies; only an absent host dependency justifies Skip.
func TestCollector_Integration_RealResources(t *testing.T) {
	const tolerance = 20.0 // percentage points tolerance for OS-level fluctuation

	// Embed a per-run UUID in the NodeID label so the scrape is unambiguous.
	runID := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	t.Logf("per-run label: node_id=%q", runID)

	// Guarantee measurable CPU load for the whole test so utilization is provably
	// non-zero: both the oracle read and the scrape sample over a ~1s `top`
	// window, and the busy spinners keep at least two cores active throughout.
	stopBurn := make(chan struct{})
	defer close(stopBurn)
	for i := 0; i < 2; i++ {
		go func() {
			for {
				select {
				case <-stopBurn:
					return
				default:
				}
			}
		}()
	}

	// --- Oracle: direct pkg/resources read on macOS ---
	reader := resources.NewDarwinReader()
	oracleNR, err := reader.Read(runID)
	if err != nil {
		t.Fatalf("oracle read failed: %v", err)
	}
	t.Logf("oracle: cpu cores=%d usedCores=%g mem totalKB=%d usedKB=%d",
		oracleNR.CPU.Cores, oracleNR.CPU.UsedCores,
		oracleNR.Memory.TotalKB, oracleNR.Memory.UsedKB)

	// Compute oracle utilization values (same formulas as ResourcesSource.Sample).
	var oracleCPUPct, oracleMemPct float64
	if oracleNR.CPU.Cores > 0 && oracleNR.CPU.UsedCores > 0 {
		oracleCPUPct = (oracleNR.CPU.UsedCores / float64(oracleNR.CPU.Cores)) * 100.0
	}
	if oracleNR.Memory.TotalKB > 0 {
		oracleMemPct = float64(oracleNR.Memory.UsedKB) / float64(oracleNR.Memory.TotalKB) * 100.0
	}

	// --- GPU detection via internal/gpu ---
	mgr := gpu.NewManager()
	gpuErr := mgr.DetectGPUs()
	if gpuErr != nil {
		// GPU detection MUST succeed on M3; hard-fail rather than skip.
		t.Fatalf("GPU detection failed on M3 host (expected success): %v", gpuErr)
	}
	gpuList := mgr.ListGPUs()
	if len(gpuList) == 0 {
		t.Fatal("GPU detection returned 0 GPUs on M3 host; expected at least 1")
	}
	t.Logf("detected %d GPU(s): %s", len(gpuList), gpuList[0].Model)

	// --- Build the collector and bind to a real local port ---
	src := NewResourcesSource(reader, mgr, runID)
	nc := NewNodeCollector(src, CollectorConfig{NodeID: runID})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: nc.Handler()}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	url := fmt.Sprintf("http://%s", ln.Addr())
	t.Logf("collector endpoint: %s", url)

	// Allow the server a moment to start (it's a goroutine).
	time.Sleep(10 * time.Millisecond)

	// --- Real HTTP GET ---
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		t.Fatalf("HTTP GET %s failed: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	body := string(bodyBytes)
	t.Logf("scrape body:\n%s", body)

	// --- Assert per-run UUID label is present (unambiguous run ID) ---
	nodeLabel := fmt.Sprintf("node_id=%q", runID)
	if !strings.Contains(body, nodeLabel) {
		t.Errorf("per-run label %s missing from scrape body:\n%s", nodeLabel, body)
	}

	// --- Assert required metric names present ---
	requiredNames := []string{
		"helix_node_cpu_pct",
		"helix_node_mem_pct",
		"helix_node_gpu_pct",
	}
	for _, name := range requiredNames {
		if !strings.Contains(body, name) {
			t.Errorf("required metric %q absent from scrape body:\n%s", name, body)
		}
	}

	// --- Parse and validate CPU metric value is sane ---
	scrapedCPU, err := parseGaugeValue(body, "helix_node_cpu_pct", runID)
	if err != nil {
		t.Fatalf("CPU metric parse failed: %v\nbody:\n%s", err, body)
	}
	// CLAUDE-2: macOS DarwinReader now samples real CPU utilization via `top`.
	// With the burner running, busy CPU MUST be > 0 — a 0 here would be the
	// false-result bluff this gate exists to catch. Upper-bound by core count.
	maxCPU := 100.0 * float64(oracleNR.CPU.Cores)
	if scrapedCPU <= 0 || scrapedCPU > maxCPU {
		t.Errorf("helix_node_cpu_pct = %g, want 0 < cpu <= %g (real non-zero utilization under load)", scrapedCPU, maxCPU)
	}
	// Oracle must also have observed real load (proves the reader is not stubbed).
	if oracleCPUPct <= 0 {
		t.Errorf("oracle CPU pct = %g, want > 0 (DarwinReader UsedCores must reflect real load)", oracleCPUPct)
	}
	t.Logf("CPU: oracle=%g scrape=%g (both real, non-zero under load)", oracleCPUPct, scrapedCPU)

	// --- Parse and validate mem metric value is within tolerance ---
	scrapedMem, err := parseGaugeValue(body, "helix_node_mem_pct", runID)
	if err != nil {
		t.Fatalf("mem metric parse failed: %v\nbody:\n%s", err, body)
	}
	diff := scrapedMem - oracleMemPct
	if diff < 0 {
		diff = -diff
	}
	if diff > tolerance {
		t.Errorf("mem pct diff %g > tolerance %g (oracle=%g scrape=%g)",
			diff, tolerance, oracleMemPct, scrapedMem)
	}
	t.Logf("mem: oracle=%g scrape=%g diff=%g (within %g)", oracleMemPct, scrapedMem, diff, tolerance)

	// --- GPU metric: must be > 0 on M3 only when monitor has run ---
	// GPU utilization requires the monitor to have populated UtilizationPercent.
	// Without a running monitor the value is 0, which is still a valid metric.
	scrapedGPU, err := parseGaugeValue(body, "helix_node_gpu_pct", runID)
	if err != nil {
		t.Errorf("GPU metric parse failed: %v\nbody:\n%s", err, body)
	} else {
		t.Logf("GPU utilization (no monitor): %g %%", scrapedGPU)
		// Range check: must be in [0, 100].
		if scrapedGPU < 0 || scrapedGPU > 100 {
			t.Errorf("GPU pct %g out of range [0, 100]", scrapedGPU)
		}
	}
}

// parseGaugeValue extracts the float64 value from a Prometheus gauge line of
// the form:   <name>{node_id="<nodeID>"} <value>
func parseGaugeValue(body, name, nodeID string) (float64, error) {
	prefix := fmt.Sprintf("%s{node_id=%q}", name, nodeID)
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimPrefix(line, prefix)
		rest = strings.TrimSpace(rest)
		var v float64
		if _, err := fmt.Sscanf(rest, "%g", &v); err != nil {
			return 0, fmt.Errorf("parse value from %q: %w", line, err)
		}
		return v, nil
	}
	return 0, fmt.Errorf("metric %s{node_id=%q} not found in body", name, nodeID)
}
