package metrics

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Fake ResourceSource — deterministic, injectable
// ---------------------------------------------------------------------------

// fakeSource returns a fixed ResourceSample or an error, controlled by the test.
type fakeSource struct {
	sample ResourceSample
	err    error
}

func (f *fakeSource) Sample() (ResourceSample, error) {
	if f.err != nil {
		return ResourceSample{}, f.err
	}
	return f.sample, nil
}

// ---------------------------------------------------------------------------
// Helper: scrape the collector handler and return the body as a string.
// ---------------------------------------------------------------------------

func scrapeHandler(t *testing.T, h http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handler returned %d; body: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// ---------------------------------------------------------------------------
// TestCollector_CPUMemPresent: fake source with CPU 42 % and Mem 67 %;
// scrape must contain EXACT gauge lines for both metrics.
// This is the primary unit test that proves sink-side behavior.
// ---------------------------------------------------------------------------

func TestCollector_CPUMemPresent(t *testing.T) {
	src := &fakeSource{
		sample: ResourceSample{
			NodeID: "node-test-01",
			CPUPct: 42.5,
			MemPct: 67.0,
			HasGPU: false,
		},
	}
	nc := NewNodeCollector(src, CollectorConfig{NodeID: "node-test-01"})
	body := scrapeHandler(t, nc.Handler())

	// Exact gauge lines must be present.
	wantCPU := `helix_node_cpu_pct{node_id="node-test-01"} 42.5`
	wantMem := `helix_node_mem_pct{node_id="node-test-01"} 67`
	if !strings.Contains(body, wantCPU) {
		t.Errorf("missing CPU gauge line %q in scrape output:\n%s", wantCPU, body)
	}
	if !strings.Contains(body, wantMem) {
		t.Errorf("missing mem gauge line %q in scrape output:\n%s", wantMem, body)
	}

	// HELP and TYPE lines must precede each gauge.
	if !strings.Contains(body, "# HELP helix_node_cpu_pct") {
		t.Errorf("missing HELP line for cpu_pct:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE helix_node_cpu_pct gauge") {
		t.Errorf("missing TYPE line for cpu_pct:\n%s", body)
	}
	if !strings.Contains(body, "# HELP helix_node_mem_pct") {
		t.Errorf("missing HELP line for mem_pct:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE helix_node_mem_pct gauge") {
		t.Errorf("missing TYPE line for mem_pct:\n%s", body)
	}
}

// ---------------------------------------------------------------------------
// TestCollector_GPUPresentWhenHasGPU: when HasGPU=true the scrape must
// contain helix_node_gpu_pct with the EXACT reported value.
// ---------------------------------------------------------------------------

func TestCollector_GPUPresentWhenHasGPU(t *testing.T) {
	src := &fakeSource{
		sample: ResourceSample{
			NodeID: "gpu-node",
			CPUPct: 10.0,
			MemPct: 20.0,
			GPUPct: 88.8,
			HasGPU: true,
		},
	}
	nc := NewNodeCollector(src, CollectorConfig{NodeID: "gpu-node"})
	body := scrapeHandler(t, nc.Handler())

	wantGPU := `helix_node_gpu_pct{node_id="gpu-node"} 88.8`
	if !strings.Contains(body, wantGPU) {
		t.Errorf("missing GPU gauge line %q in scrape output:\n%s", wantGPU, body)
	}
	if !strings.Contains(body, "# HELP helix_node_gpu_pct") {
		t.Errorf("missing HELP line for gpu_pct:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE helix_node_gpu_pct gauge") {
		t.Errorf("missing TYPE line for gpu_pct:\n%s", body)
	}
}

// ---------------------------------------------------------------------------
// TestCollector_GPUAbsentWhenNoGPU: when HasGPU=false the scrape must NOT
// contain helix_node_gpu_pct (no fabricated GPU line for CPU-only nodes).
// This is the mutation-targeted assertion: removing the HasGPU guard causes
// the GPU line to appear unconditionally and fails the Contains-not check.
// ---------------------------------------------------------------------------

func TestCollector_GPUAbsentWhenNoGPU(t *testing.T) {
	src := &fakeSource{
		sample: ResourceSample{
			NodeID: "cpu-only",
			CPUPct: 30.0,
			MemPct: 50.0,
			HasGPU: false,
			GPUPct: 0,
		},
	}
	nc := NewNodeCollector(src, CollectorConfig{NodeID: "cpu-only"})
	body := scrapeHandler(t, nc.Handler())

	if strings.Contains(body, "helix_node_gpu_pct") {
		t.Errorf("GPU metric MUST NOT appear when HasGPU=false; got:\n%s", body)
	}
}

// ---------------------------------------------------------------------------
// TestCollector_ExactValues: pinned exact values for both CPU and mem to catch
// any rounding, truncation, or re-labelling regressions.
// ---------------------------------------------------------------------------

func TestCollector_ExactValues(t *testing.T) {
	cases := []struct {
		nodeID string
		cpuPct float64
		memPct float64
		gpuPct float64
		hasGPU bool
	}{
		{"n1", 0.0, 0.0, 0.0, false},
		{"n2", 100.0, 100.0, 100.0, true},
		{"n3", 13.37, 99.1, 55.5, true},
		{"n4", 50.0, 33.33, 0.0, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.nodeID, func(t *testing.T) {
			src := &fakeSource{
				sample: ResourceSample{
					NodeID: tc.nodeID,
					CPUPct: tc.cpuPct,
					MemPct: tc.memPct,
					GPUPct: tc.gpuPct,
					HasGPU: tc.hasGPU,
				},
			}
			nc := NewNodeCollector(src, CollectorConfig{NodeID: tc.nodeID})
			body := scrapeHandler(t, nc.Handler())

			wantCPU := fmt.Sprintf("helix_node_cpu_pct{node_id=%q} %g", tc.nodeID, tc.cpuPct)
			wantMem := fmt.Sprintf("helix_node_mem_pct{node_id=%q} %g", tc.nodeID, tc.memPct)
			if !strings.Contains(body, wantCPU) {
				t.Errorf("[%s] missing CPU line %q:\n%s", tc.nodeID, wantCPU, body)
			}
			if !strings.Contains(body, wantMem) {
				t.Errorf("[%s] missing mem line %q:\n%s", tc.nodeID, wantMem, body)
			}
			if tc.hasGPU {
				wantGPU := fmt.Sprintf("helix_node_gpu_pct{node_id=%q} %g", tc.nodeID, tc.gpuPct)
				if !strings.Contains(body, wantGPU) {
					t.Errorf("[%s] missing GPU line %q:\n%s", tc.nodeID, wantGPU, body)
				}
			} else {
				if strings.Contains(body, "helix_node_gpu_pct") {
					t.Errorf("[%s] GPU line must be absent when HasGPU=false:\n%s", tc.nodeID, body)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCollector_SourceError: when the source returns an error the handler
// must return 500 and must NOT emit any gauge lines.
// ---------------------------------------------------------------------------

func TestCollector_SourceError(t *testing.T) {
	src := &fakeSource{err: fmt.Errorf("disk on fire")}
	nc := NewNodeCollector(src, CollectorConfig{NodeID: "err-node"})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	nc.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on source error, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "helix_node_cpu_pct") {
		t.Errorf("must not emit gauge on error; got:\n%s", body)
	}
}

// ---------------------------------------------------------------------------
// TestCollector_ContentType: response must carry text/plain; charset=utf-8.
// ---------------------------------------------------------------------------

func TestCollector_ContentType(t *testing.T) {
	src := &fakeSource{sample: ResourceSample{NodeID: "ct-node", CPUPct: 1, MemPct: 2}}
	nc := NewNodeCollector(src, CollectorConfig{NodeID: "ct-node"})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	nc.Handler().ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != "text/plain; charset=utf-8" {
		t.Errorf("expected Content-Type text/plain; charset=utf-8, got %q", ct)
	}
}

// ---------------------------------------------------------------------------
// TestCollector_HandlerOverRealHTTP: mounts Handler on httptest.NewServer and
// issues a real HTTP GET over loopback TCP, validating that the scrape
// endpoint is reachable and returns correct data.  This proves end-user
// observable behavior: a Prometheus scraper hitting the node over the network.
// ---------------------------------------------------------------------------

func TestCollector_HandlerOverRealHTTP(t *testing.T) {
	src := &fakeSource{
		sample: ResourceSample{
			NodeID: "net-node",
			CPUPct: 77.7,
			MemPct: 55.5,
			GPUPct: 33.3,
			HasGPU: true,
		},
	}
	nc := NewNodeCollector(src, CollectorConfig{NodeID: "net-node"})
	ts := httptest.NewServer(nc.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	body := string(bodyBytes)

	required := []string{
		`helix_node_cpu_pct{node_id="net-node"} 77.7`,
		`helix_node_mem_pct{node_id="net-node"} 55.5`,
		`helix_node_gpu_pct{node_id="net-node"} 33.3`,
	}
	for _, line := range required {
		if !strings.Contains(body, line) {
			t.Errorf("real HTTP scrape missing required line %q\nfull body:\n%s", line, body)
		}
	}
}

// ---------------------------------------------------------------------------
// TestCollector_Snapshot: Collect() stores the sample; Snapshot() returns it.
// ---------------------------------------------------------------------------

func TestCollector_Snapshot(t *testing.T) {
	s := ResourceSample{NodeID: "snap-node", CPUPct: 12.3, MemPct: 45.6, HasGPU: false}
	src := &fakeSource{sample: s}
	nc := NewNodeCollector(src, CollectorConfig{NodeID: "snap-node"})

	if err := nc.Collect(); err != nil {
		t.Fatalf("Collect() error: %v", err)
	}
	got := nc.Snapshot()
	if got.CPUPct != s.CPUPct {
		t.Errorf("Snapshot CPUPct: want %g, got %g", s.CPUPct, got.CPUPct)
	}
	if got.MemPct != s.MemPct {
		t.Errorf("Snapshot MemPct: want %g, got %g", s.MemPct, got.MemPct)
	}
}

// ---------------------------------------------------------------------------
// TestCollector_DefaultNodeID: when CollectorConfig.NodeID is empty, the
// collector defaults to "local" as the node_id label.
// ---------------------------------------------------------------------------

func TestCollector_DefaultNodeID(t *testing.T) {
	src := &fakeSource{sample: ResourceSample{CPUPct: 5, MemPct: 10}}
	nc := NewNodeCollector(src, CollectorConfig{}) // empty NodeID
	body := scrapeHandler(t, nc.Handler())

	// The sample has no NodeID either; both render() paths should use "local".
	if !strings.Contains(body, `node_id="local"`) {
		t.Errorf(`expected node_id="local" label in scrape output:\n%s`, body)
	}
}

// ---------------------------------------------------------------------------
// TestCollector_GPULabel_MutationProof: the GPU metric name must be exactly
// helix_node_gpu_pct — a mislabelled name (e.g. helix_node_gpu_util) fails.
// This is the mutation-proof assertion for the gauge name.
// ---------------------------------------------------------------------------

func TestCollector_GPULabel_MutationProof(t *testing.T) {
	src := &fakeSource{
		sample: ResourceSample{
			NodeID: "mut-node",
			GPUPct: 99.9,
			HasGPU: true,
		},
	}
	nc := NewNodeCollector(src, CollectorConfig{NodeID: "mut-node"})
	body := scrapeHandler(t, nc.Handler())

	// Exact metric name check — any rename of the gauge string breaks this.
	if !strings.Contains(body, "helix_node_gpu_pct") {
		t.Errorf("GPU gauge must be named helix_node_gpu_pct; got:\n%s", body)
	}
	// Ensure it is NOT under a different (wrong) name.  We check for names that
	// would only appear if the implementation used the wrong string.  The correct
	// name "helix_node_gpu_pct" is already checked above; the wrong names below
	// must be completely absent (not even as prefixes of correct lines).
	for _, wrong := range []string{"helix_node_gpu_util", "helix_gpu_pct"} {
		if strings.Contains(body, wrong) {
			t.Errorf("unexpected wrong gauge name %q found in output:\n%s", wrong, body)
		}
	}
}
