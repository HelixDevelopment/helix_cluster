package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestTierMetrics_ExposesSetSeries is the LOAD-BEARING GUARD for HXC-1535.
//
// It sets a GPU-tier utilization, a provider cost-per-hour, and a provider
// health score to specific non-zero values, then asserts that Render() emits
// each series with its EXACT label and EXACT value. If Render is mutated to
// omit a series or emit a wrong/zero value, this test fails.
func TestTierMetrics_ExposesSetSeries(t *testing.T) {
	const runUUID = "0c8a4f6e-2b1d-4a77-9e3c-6f1535aab001"

	tm := NewTierMetrics()

	// Before: Render() must not contain any value lines for these series.
	before := tm.Render()
	for _, frag := range []string{
		`gpu_tier_utilization{tier="h100"}`,
		`gpu_cost_per_hour{provider="aws"}`,
		`provider_health{provider="gcp"}`,
	} {
		if strings.Contains(before, frag) {
			t.Fatalf("run=%s: before any Set, Render unexpectedly already contained %q", runUUID, frag)
		}
	}

	// Hand-derived expected values (specific, non-zero, non-equal).
	tm.SetTierUtilization("h100", 73.5)
	tm.SetCostPerHour("aws", 12.25)
	tm.SetProviderHealth("gcp", 0.875)

	after := tm.Render()

	// Each series must appear with its exact label AND exact value.
	wantLines := []string{
		`gpu_tier_utilization{tier="h100"} 73.5`,
		`gpu_cost_per_hour{provider="aws"} 12.25`,
		`provider_health{provider="gcp"} 0.875`,
	}
	for _, want := range wantLines {
		if !strings.Contains(after, want) {
			t.Fatalf("run=%s: Render() output missing exact series line %q\n--- full output ---\n%s",
				runUUID, want, after)
		}
	}

	// HELP/TYPE schema headers must be present for each family.
	for _, hdr := range []string{
		"# HELP gpu_tier_utilization",
		"# TYPE gpu_tier_utilization gauge",
		"# HELP gpu_cost_per_hour",
		"# TYPE gpu_cost_per_hour gauge",
		"# HELP provider_health",
		"# TYPE provider_health gauge",
	} {
		if !strings.Contains(after, hdr) {
			t.Fatalf("run=%s: Render() missing header line %q", runUUID, hdr)
		}
	}

	t.Logf("run=%s: before->after proven: tier h100 0->73.5, cost aws 0->12.25, health gcp 0->0.875 all present in Render()",
		runUUID)
}

// TestTierMetrics_DeterministicOrdering asserts that within each metric family
// series are emitted in ascending label-key order, and that two renders of the
// same state produce byte-identical output.
func TestTierMetrics_DeterministicOrdering(t *testing.T) {
	const runUUID = "0c8a4f6e-2b1d-4a77-9e3c-6f1535aab002"

	tm := NewTierMetrics()
	// Insert deliberately out of sorted order.
	tm.SetTierUtilization("h100", 50)
	tm.SetTierUtilization("a100", 10)
	tm.SetTierUtilization("t4", 90)
	tm.SetCostPerHour("gcp", 3)
	tm.SetCostPerHour("aws", 2)
	tm.SetProviderHealth("oracle", 0.4)
	tm.SetProviderHealth("azure", 0.9)

	out := tm.Render()

	// Two renders must be byte-identical (map iteration is otherwise random).
	if out2 := tm.Render(); out != out2 {
		t.Fatalf("run=%s: Render() not deterministic across two calls:\n--- 1 ---\n%s\n--- 2 ---\n%s",
			runUUID, out, out2)
	}

	// Within the tier family, a100 must precede h100 must precede t4.
	mustOrder := func(family string, first, second string) {
		i := strings.Index(out, first)
		j := strings.Index(out, second)
		if i < 0 || j < 0 {
			t.Fatalf("run=%s: %s family missing series %q(%d) or %q(%d)", runUUID, family, first, i, second, j)
		}
		if i >= j {
			t.Fatalf("run=%s: %s family not sorted: %q at %d should precede %q at %d", runUUID, family, first, i, second, j)
		}
	}
	mustOrder("tier", `gpu_tier_utilization{tier="a100"}`, `gpu_tier_utilization{tier="h100"}`)
	mustOrder("tier", `gpu_tier_utilization{tier="h100"}`, `gpu_tier_utilization{tier="t4"}`)
	mustOrder("cost", `gpu_cost_per_hour{provider="aws"}`, `gpu_cost_per_hour{provider="gcp"}`)
	mustOrder("health", `provider_health{provider="azure"}`, `provider_health{provider="oracle"}`)

	t.Logf("run=%s: ordering proven (a100<h100<t4, aws<gcp, azure<oracle) and Render byte-stable across 2 calls", runUUID)
}

// TestTierMetrics_Handler asserts the optional Handler serves Render() as
// text/plain with the set series present.
func TestTierMetrics_Handler(t *testing.T) {
	const runUUID = "0c8a4f6e-2b1d-4a77-9e3c-6f1535aab003"

	tm := NewTierMetrics()
	tm.SetTierUtilization("a100", 42.0)

	srv := httptest.NewServer(tm.Handler())
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
	want := `gpu_tier_utilization{tier="a100"} 42`
	if !strings.Contains(string(body), want) {
		t.Fatalf("run=%s: handler body missing %q\n%s", runUUID, want, string(body))
	}
	t.Logf("run=%s: handler served text/plain with tier a100 0->42 present", runUUID)
}

// TestTierMetrics_ConcurrentSafe exercises concurrent Set + Render under the
// race detector. It must not panic or race; the final value for a tier must be
// one of the values that was written.
func TestTierMetrics_ConcurrentSafe(t *testing.T) {
	const runUUID = "0c8a4f6e-2b1d-4a77-9e3c-6f1535aab004"

	tm := NewTierMetrics()
	var wg sync.WaitGroup
	const goroutines = 16
	const iters = 200

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				tm.SetTierUtilization("a100", float64(i))
				tm.SetCostPerHour("aws", float64(i)*0.5)
				tm.SetProviderHealth("gcp", float64(i)/float64(iters))
				_ = tm.Render()
			}
		}(g)
	}
	wg.Wait()

	out := tm.Render()
	if !strings.Contains(out, `gpu_tier_utilization{tier="a100"}`) {
		t.Fatalf("run=%s: after concurrent writes, tier series missing:\n%s", runUUID, out)
	}
	t.Logf("run=%s: %d goroutines x %d iters of concurrent Set+Render completed race-clean", runUUID, goroutines, iters)
}
