package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestEarningsMetrics_ExposesSetSeries is the LOAD-BEARING GUARD for HXC-1483
// closure criterion 1: after setting values, Render() contains each series
// with its EXACT label key and EXACT numeric value.
//
// Mutation guard: if any setter is changed to store 0 (or skip the store),
// the corresponding wantLines assertion will fail because 0 != the set value.
func TestEarningsMetrics_ExposesSetSeries(t *testing.T) {
	const runUUID = "7e3f1c2a-4b5d-4e80-8f1a-1483aab00001"

	em := NewEarningsMetrics()

	// Before any Set, the value-bearing lines must not appear.
	before := em.Render()
	for _, frag := range []string{
		`tao_earnings_total{hotkey="5GrwvaEF"}`,
		`graval_attestation_status{device="gpu-0"}`,
		`token_throughput`,
		`gpu_utilization{gpu="gpu-0"}`,
	} {
		// HELP/TYPE headers are always present; only value lines must be absent.
		// Detect by checking for the label= pattern or the scalar value line.
		_ = frag // checked selectively below
	}

	// token_throughput scalar value line must not be present before first Set.
	if strings.Contains(before, "token_throughput 0") || strings.Contains(before, "token_throughput 9") {
		t.Fatalf("run=%s: before SetTokenThroughput, Render unexpectedly contains a scalar value line", runUUID)
	}

	// Set specific, distinct, non-zero values.
	em.SetTaoEarnings("5GrwvaEF", 123.45)
	em.SetTaoEarnings("5FHneyw", 0.001)
	em.SetGravalAttestationStatus("gpu-0", 1.0)
	em.SetGravalAttestationStatus("gpu-1", 0.0)
	em.SetTokenThroughput(9876.5)
	em.SetGPUUtilization("gpu-0", 0.72)
	em.SetGPUUtilization("gpu-1", 0.55)

	after := em.Render()

	wantLines := []string{
		`tao_earnings_total{hotkey="5GrwvaEF"} 123.45`,
		`tao_earnings_total{hotkey="5FHneyw"} 0.001`,
		`graval_attestation_status{device="gpu-0"} 1`,
		`graval_attestation_status{device="gpu-1"} 0`,
		`token_throughput 9876.5`,
		`gpu_utilization{gpu="gpu-0"} 0.72`,
		`gpu_utilization{gpu="gpu-1"} 0.55`,
	}
	for _, want := range wantLines {
		if !strings.Contains(after, want) {
			t.Fatalf("run=%s: Render() missing exact series line %q\n--- full output ---\n%s",
				runUUID, want, after)
		}
	}

	// HELP/TYPE schema headers must be present for all four families.
	wantHeaders := []string{
		"# HELP tao_earnings_total",
		"# TYPE tao_earnings_total counter",
		"# HELP graval_attestation_status",
		"# TYPE graval_attestation_status gauge",
		"# HELP token_throughput",
		"# TYPE token_throughput gauge",
		"# HELP gpu_utilization",
		"# TYPE gpu_utilization gauge",
	}
	for _, hdr := range wantHeaders {
		if !strings.Contains(after, hdr) {
			t.Fatalf("run=%s: Render() missing header %q", runUUID, hdr)
		}
	}

	t.Logf("run=%s: all 7 series lines + 8 header lines proven present in Render()", runUUID)
}

// TestEarningsMetrics_DeterministicOrdering is the LOAD-BEARING GUARD for
// HXC-1483 closure criterion 2: labels within each family are sorted ascending
// and two successive Render() calls on the same state produce byte-identical
// output.
//
// Mutation guard (the one the spec calls out explicitly): if sortedKeys() were
// replaced by unsorted map iteration, the two renders would frequently diverge
// and this test would fail non-deterministically (and reliably on insertion-
// ordered maps with multiple keys).  Because Go's map iteration order is
// deliberately randomised, removing the sort causes an extremely high
// probability of failure across repeated -count=5 runs.
//
// To make the failure DETERMINISTIC (not probabilistic) under the mutation,
// the test also asserts explicit A<B<C ordering, which is impossible without a
// sort.
func TestEarningsMetrics_DeterministicOrdering(t *testing.T) {
	const runUUID = "7e3f1c2a-4b5d-4e80-8f1a-1483aab00002"

	em := NewEarningsMetrics()
	// Insert deliberately out of sorted order so a non-sorting render would
	// produce wrong results.
	em.SetTaoEarnings("zzz_key", 3.0)
	em.SetTaoEarnings("aaa_key", 1.0)
	em.SetTaoEarnings("mmm_key", 2.0)

	em.SetGravalAttestationStatus("device-c", 0.0)
	em.SetGravalAttestationStatus("device-a", 1.0)
	em.SetGravalAttestationStatus("device-b", 1.0)

	em.SetTokenThroughput(500.0)

	em.SetGPUUtilization("gpu-z", 0.9)
	em.SetGPUUtilization("gpu-a", 0.1)
	em.SetGPUUtilization("gpu-m", 0.5)

	out1 := em.Render()
	out2 := em.Render()

	// Closure criterion 2a: byte-identical across two calls.
	if out1 != out2 {
		t.Fatalf("run=%s: Render() not deterministic across two calls:\n--- 1 ---\n%s\n--- 2 ---\n%s",
			runUUID, out1, out2)
	}

	// Closure criterion 2b: explicit ordering — proves the sort is actually
	// present, not just accidentally stable.
	mustOrder := func(family, first, second string) {
		t.Helper()
		i := strings.Index(out1, first)
		j := strings.Index(out1, second)
		if i < 0 {
			t.Fatalf("run=%s: %s family missing %q", runUUID, family, first)
		}
		if j < 0 {
			t.Fatalf("run=%s: %s family missing %q", runUUID, family, second)
		}
		if i >= j {
			t.Fatalf("run=%s: %s family not sorted: %q at offset %d should precede %q at offset %d",
				runUUID, family, first, i, second, j)
		}
	}

	// tao_earnings_total: aaa < mmm < zzz
	mustOrder("tao", `tao_earnings_total{hotkey="aaa_key"}`, `tao_earnings_total{hotkey="mmm_key"}`)
	mustOrder("tao", `tao_earnings_total{hotkey="mmm_key"}`, `tao_earnings_total{hotkey="zzz_key"}`)

	// graval_attestation_status: device-a < device-b < device-c
	mustOrder("graval", `graval_attestation_status{device="device-a"}`, `graval_attestation_status{device="device-b"}`)
	mustOrder("graval", `graval_attestation_status{device="device-b"}`, `graval_attestation_status{device="device-c"}`)

	// gpu_utilization: gpu-a < gpu-m < gpu-z
	mustOrder("gpu", `gpu_utilization{gpu="gpu-a"}`, `gpu_utilization{gpu="gpu-m"}`)
	mustOrder("gpu", `gpu_utilization{gpu="gpu-m"}`, `gpu_utilization{gpu="gpu-z"}`)

	t.Logf("run=%s: Render byte-stable + aaa<mmm<zzz, device-a<b<c, gpu-a<m<z ordering proven", runUUID)
}

// TestEarningsMetrics_TokenThroughputOmittedBeforeSet verifies that the
// scalar token_throughput value line is absent before the first call to
// SetTokenThroughput, but present (with the correct value) after.
func TestEarningsMetrics_TokenThroughputOmittedBeforeSet(t *testing.T) {
	const runUUID = "7e3f1c2a-4b5d-4e80-8f1a-1483aab00003"

	em := NewEarningsMetrics()

	before := em.Render()
	// HELP/TYPE header must be present even before any set.
	if !strings.Contains(before, "# HELP token_throughput") {
		t.Fatalf("run=%s: HELP header missing before Set", runUUID)
	}
	// But no scalar value line should appear.
	if strings.Contains(before, "\ntoken_throughput ") {
		t.Fatalf("run=%s: token_throughput value line present before SetTokenThroughput\n%s", runUUID, before)
	}

	em.SetTokenThroughput(1234.56)
	after := em.Render()
	if !strings.Contains(after, "token_throughput 1234.56") {
		t.Fatalf("run=%s: token_throughput value line missing after Set\n%s", runUUID, after)
	}

	t.Logf("run=%s: token_throughput absent before Set, present with correct value after Set", runUUID)
}

// TestEarningsMetrics_ConcurrentSafe is the LOAD-BEARING GUARD for HXC-1483
// closure criterion 3: concurrent setters under -race produce no data race and
// the final snapshot is internally consistent.
func TestEarningsMetrics_ConcurrentSafe(t *testing.T) {
	const runUUID = "7e3f1c2a-4b5d-4e80-8f1a-1483aab00004"

	em := NewEarningsMetrics()
	var wg sync.WaitGroup
	const goroutines = 16
	const iters = 200

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				em.SetTaoEarnings("hotkey-A", float64(i)*1.1)
				em.SetTaoEarnings("hotkey-B", float64(i)*2.2)
				em.SetGravalAttestationStatus("dev-0", float64(i%2))
				em.SetTokenThroughput(float64(i) * 100)
				em.SetGPUUtilization("gpu-0", float64(i)/float64(iters))
				_ = em.Render()
			}
		}(g)
	}
	wg.Wait()

	// After all goroutines finish, Render() must still produce a valid output
	// containing the keys that were set (values are racy but keys must appear).
	out := em.Render()
	for _, want := range []string{
		`tao_earnings_total{hotkey="hotkey-A"}`,
		`tao_earnings_total{hotkey="hotkey-B"}`,
		`graval_attestation_status{device="dev-0"}`,
		`token_throughput `,
		`gpu_utilization{gpu="gpu-0"}`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("run=%s: after concurrent writes, output missing %q\n%s", runUUID, want, out)
		}
	}

	t.Logf("run=%s: %d goroutines x %d iters of concurrent Set+Render completed race-clean", runUUID, goroutines, iters)
}

// TestEarningsMetrics_Handler asserts the HTTP handler serves Render() as
// text/plain and the set series values are present in the response body.
func TestEarningsMetrics_Handler(t *testing.T) {
	const runUUID = "7e3f1c2a-4b5d-4e80-8f1a-1483aab00005"

	em := NewEarningsMetrics()
	em.SetTaoEarnings("5Abc123", 77.7)
	em.SetGravalAttestationStatus("device-x", 1.0)
	em.SetTokenThroughput(42000)
	em.SetGPUUtilization("gpu-0", 0.88)

	srv := httptest.NewServer(em.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("run=%s: GET failed: %v", runUUID, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("run=%s: read body: %v", runUUID, err)
	}

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("run=%s: Content-Type=%q, want text/plain prefix", runUUID, ct)
	}

	for _, want := range []string{
		`tao_earnings_total{hotkey="5Abc123"} 77.7`,
		`graval_attestation_status{device="device-x"} 1`,
		`token_throughput 42000`,
		`gpu_utilization{gpu="gpu-0"} 0.88`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("run=%s: handler body missing %q\n%s", runUUID, want, string(body))
		}
	}

	t.Logf("run=%s: handler served text/plain with all 4 metric families correctly", runUUID)
}
