//go:build integration

// Package qualitygate_test integration tests exercise the quality gate against
// a real metric snapshot and assert sink-side Report facts with a per-run UUID
// so CI log correlation is unambiguous.
//
// These tests are NOT run during hermetic unit testing. The orchestrator runs
// them with -tags=integration against the real build environment.
package qualitygate_test

import (
	"testing"

	. "github.com/HelixDevelopment/helix_cluster/pkg/qualitygate"
)

// TestIntegration_QualityGate_CriticalBreach exercises the Validate function
// against a real metric snapshot that breaches a CRITICAL rule. The per-run
// UUID in the Report is asserted non-empty to prove this specific invocation
// produced its own Report (not a cached result).
func TestIntegration_QualityGate_CriticalBreach(t *testing.T) {
	// Build a metric map with a clear CRITICAL breach: recovery_time_p99 way
	// above the 30000ms threshold.
	metrics := map[string]float64{
		"helix_nodes_healthy":          5,
		"helix_nodes_total":            6,
		"helix_tasks_unscheduled":      0,
		"helix_tasks_unscheduled_rate": 0.3,
		"schedule_latency_p99":         400,
		"consensus_rounds_rate":        3,
		"test_duration_p95":            150,
		"firecracker_vcpu":             45,
		"chaos_faults_injected":        2,
		"recovery_time_p99":            60000, // CRITICAL breach
	}

	r := Validate(metrics)

	// Sink-side: gate must FAIL.
	if r.Pass {
		t.Fatalf("integration: expected FAIL for recovery_time_p99=60000, got PASS; UUID=%s", r.RunUUID)
	}

	// Per-run UUID must be non-empty and unique across two calls.
	if r.RunUUID == "" {
		t.Fatal("integration: RunUUID must be non-empty")
	}

	// A second call must produce a distinct UUID.
	r2 := Validate(metrics)
	if r2.RunUUID == r.RunUUID {
		t.Fatalf("integration: RunUUID not unique across calls: both=%q", r.RunUUID)
	}

	// Violation must be present for the correct metric and severity.
	found := false
	for _, v := range r.Violations {
		if v.Metric == "recovery_time_p99" && v.Severity == SeverityCritical {
			if v.Actual != 60000 {
				t.Errorf("integration: Violation.Actual=%v, want 60000", v.Actual)
			}
			if v.Threshold != 30000 {
				t.Errorf("integration: Violation.Threshold=%v, want 30000", v.Threshold)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("integration: no CRITICAL violation for recovery_time_p99; violations=%v", r.Violations)
	}
}

// TestIntegration_QualityGate_AllPass exercises the full all-in-range path and
// confirms the gate emits a PASS Report with all 9 KPIs evaluated, tagged with
// a fresh UUID.
func TestIntegration_QualityGate_AllPass(t *testing.T) {
	metrics := map[string]float64{
		"helix_nodes_healthy":          5,
		"helix_nodes_total":            6,
		"helix_tasks_unscheduled":      0,
		"helix_tasks_unscheduled_rate": 0.1,
		"schedule_latency_p99":         200,
		"consensus_rounds_rate":        2,
		"test_duration_p95":            100,
		"firecracker_vcpu":             30,
		"chaos_faults_injected":        3,
		"recovery_time_p99":            5000,
	}

	r := Validate(metrics)

	if !r.Pass {
		t.Fatalf("integration: expected PASS for all-in-range metrics, got FAIL; UUID=%s violations=%v",
			r.RunUUID, r.Violations)
	}
	if len(r.Violations) != 0 {
		t.Fatalf("integration: expected zero violations, got %d: %v", len(r.Violations), r.Violations)
	}
	if r.RunUUID == "" {
		t.Fatal("integration: RunUUID must be non-empty")
	}
	const wantKPIs = 9
	if len(r.EvaluatedKPIs) != wantKPIs {
		t.Fatalf("integration: expected %d evaluated KPIs, got %d: %v",
			wantKPIs, len(r.EvaluatedKPIs), r.EvaluatedKPIs)
	}
}

// TestIntegration_QualityGate_WarningOnlyDoesNotFail confirms that a WARNING
// breach alone (helix_tasks_unscheduled_rate >= 1) does not flip the gate to
// FAIL, but the WARNING violation IS recorded (sink-side evidence).
func TestIntegration_QualityGate_WarningOnlyDoesNotFail(t *testing.T) {
	metrics := map[string]float64{
		"helix_nodes_healthy":          5,
		"helix_nodes_total":            6,
		"helix_tasks_unscheduled":      0,
		"helix_tasks_unscheduled_rate": 2.5, // WARNING breach: >= 1
		"schedule_latency_p99":         200,
		"consensus_rounds_rate":        2,
		"test_duration_p95":            100,
		"firecracker_vcpu":             30,
		"chaos_faults_injected":        3,
		"recovery_time_p99":            5000,
	}

	r := Validate(metrics)

	// Gate must remain PASS (warning-only).
	if !r.Pass {
		t.Fatalf("integration: WARNING-only breach must not FAIL the gate; UUID=%s violations=%v",
			r.RunUUID, r.Violations)
	}

	// WARNING violation must be recorded as sink-side evidence.
	found := false
	for _, v := range r.Violations {
		if v.Metric == "helix_tasks_unscheduled_rate" && v.Severity == SeverityWarning {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("integration: expected WARNING violation for helix_tasks_unscheduled_rate; violations=%v",
			r.Violations)
	}
}
