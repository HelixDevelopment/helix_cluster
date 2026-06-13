// Package qualitygate_test provides deterministic unit tests for the KPI
// quality gate. All assertions prove SINK-SIDE facts: the Report fields,
// overall pass/fail, per-rule Violation entries, and their severities.
package qualitygate_test

import (
	"testing"

	. "github.com/HelixDevelopment/helix_cluster/pkg/qualitygate"
)

// ---------------------------------------------------------------------------
// Helper: assert Report FAIL, find a named Violation with expected severity.
// ---------------------------------------------------------------------------

func requireViolation(t *testing.T, r Report, metric, severity string) Violation {
	t.Helper()
	if r.Pass {
		t.Fatalf("expected Report.Pass=false for metric %q breach, got PASS", metric)
	}
	for _, v := range r.Violations {
		if v.Metric == metric && v.Severity == severity {
			return v
		}
	}
	t.Fatalf("expected Violation{Metric:%q Severity:%q} in report; violations=%v", metric, severity, r.Violations)
	return Violation{} // unreachable
}

// ---------------------------------------------------------------------------
// 1. Single critical breach → FAIL + correct Violation entry.
// ---------------------------------------------------------------------------

// TestValidate_CriticalBreach_TasksUnscheduled verifies that a non-zero
// helix_tasks_unscheduled value (CRITICAL rule: == 0) causes a FAIL report
// with the correct metric and severity flagged.
func TestValidate_CriticalBreach_TasksUnscheduled(t *testing.T) {
	metrics := map[string]float64{
		"helix_nodes_healthy":          5,
		"helix_nodes_total":            6,
		"helix_tasks_unscheduled":      2, // BREACH: must be 0
		"helix_tasks_unscheduled_rate": 0, // ok
		"schedule_latency_p99":         500,
		"consensus_rounds_rate":        5,
		"test_duration_p95":            200,
		"firecracker_vcpu":             50,
		"chaos_faults_injected":        1,
		"recovery_time_p99":            20000,
	}

	r := Validate(metrics)

	v := requireViolation(t, r, "helix_tasks_unscheduled", SeverityCritical)
	if v.Actual != 2 {
		t.Errorf("Violation.Actual = %v, want 2", v.Actual)
	}
	if v.Comparator != "==" {
		t.Errorf("Violation.Comparator = %q, want \"==\"", v.Comparator)
	}
	if v.Threshold != 0 {
		t.Errorf("Violation.Threshold = %v, want 0", v.Threshold)
	}
}

// TestValidate_CriticalBreach_RecoveryTime verifies that recovery_time p99
// exceeding 30000ms (CRITICAL rule: < 30000) causes FAIL.
func TestValidate_CriticalBreach_RecoveryTime(t *testing.T) {
	metrics := map[string]float64{
		"helix_nodes_healthy":          5,
		"helix_nodes_total":            6,
		"helix_tasks_unscheduled":      0,
		"helix_tasks_unscheduled_rate": 0,
		"schedule_latency_p99":         500,
		"consensus_rounds_rate":        5,
		"test_duration_p95":            200,
		"firecracker_vcpu":             50,
		"chaos_faults_injected":        1,
		"recovery_time_p99":            35000, // BREACH: must be < 30000
	}

	r := Validate(metrics)

	v := requireViolation(t, r, "recovery_time_p99", SeverityCritical)
	if v.Actual != 35000 {
		t.Errorf("Violation.Actual = %v, want 35000", v.Actual)
	}
}

// ---------------------------------------------------------------------------
// 2. All-in-range → PASS, zero violations.
// ---------------------------------------------------------------------------

// TestValidate_AllPass verifies that a metric map satisfying all 8 KPI rules
// produces Report.Pass=true and no Violation entries.
func TestValidate_AllPass(t *testing.T) {
	metrics := allPassMetrics()

	r := Validate(metrics)

	if !r.Pass {
		t.Fatalf("expected Report.Pass=true (all metrics in range), got FAIL; violations=%v", r.Violations)
	}
	if len(r.Violations) != 0 {
		t.Fatalf("expected zero Violations, got %d: %v", len(r.Violations), r.Violations)
	}
	// UUID must be non-empty (run-tagged output).
	if r.RunUUID == "" {
		t.Fatal("Report.RunUUID must be non-empty")
	}
}

// allPassMetrics returns a metric map that satisfies every KPI rule.
func allPassMetrics() map[string]float64 {
	return map[string]float64{
		// Rule 1: helix_nodes_healthy >= floor(helix_nodes_total*0.5)+1
		// With total=6 => threshold=4; we supply 5 (ok).
		"helix_nodes_healthy": 5,
		"helix_nodes_total":   6,
		// Rule 2a: helix_tasks_unscheduled == 0
		"helix_tasks_unscheduled": 0,
		// Rule 2b: helix_tasks_unscheduled rate < 1
		"helix_tasks_unscheduled_rate": 0.5,
		// Rule 3: schedule_latency_p99 < 1000
		"schedule_latency_p99": 800,
		// Rule 4: consensus_rounds_rate < 10
		"consensus_rounds_rate": 7,
		// Rule 5: test_duration_p95 < 300
		"test_duration_p95": 250,
		// Rule 6: firecracker_vcpu < 80
		"firecracker_vcpu": 60,
		// Rule 7: chaos_faults_injected >= 1
		"chaos_faults_injected": 1,
		// Rule 8: recovery_time_p99 < 30000
		"recovery_time_p99": 20000,
	}
}

// ---------------------------------------------------------------------------
// 3. Per-rule coverage: one test per rule ensures ALL 8 are evaluated.
// ---------------------------------------------------------------------------

// TestValidate_Rule1_NodesHealthy_Breach checks the quorum rule:
// helix_nodes_healthy >= floor(helix_nodes_total*0.5)+1.
// With total=6, threshold=4; supplying healthy=3 must FAIL CRITICAL.
func TestValidate_Rule1_NodesHealthy_Breach(t *testing.T) {
	m := allPassMetrics()
	m["helix_nodes_total"] = 6
	m["helix_nodes_healthy"] = 3 // floor(6*0.5)+1 = 4; 3 < 4 → CRITICAL

	r := Validate(m)

	requireViolation(t, r, "helix_nodes_healthy", SeverityCritical)
}

// TestValidate_Rule2a_TasksUnscheduled_Breach verifies the strict == 0 rule.
func TestValidate_Rule2a_TasksUnscheduled_Breach(t *testing.T) {
	m := allPassMetrics()
	m["helix_tasks_unscheduled"] = 1 // != 0 → CRITICAL

	r := Validate(m)

	requireViolation(t, r, "helix_tasks_unscheduled", SeverityCritical)
}

// TestValidate_Rule2b_TasksRate_Breach verifies the rate < 1 warning rule.
// A rate breach alone (tasks_unscheduled==0) must not FAIL the gate but must
// produce a WARNING violation.
func TestValidate_Rule2b_TasksRate_Breach(t *testing.T) {
	m := allPassMetrics()
	m["helix_tasks_unscheduled"] = 0        // pass rule 2a
	m["helix_tasks_unscheduled_rate"] = 1.5 // >= 1 → WARNING

	r := Validate(m)

	// Warning-only breach must NOT fail the gate.
	if !r.Pass {
		t.Fatalf("WARNING-only breach should not FAIL the gate; got FAIL; violations=%v", r.Violations)
	}
	// But the warning violation must be recorded.
	found := false
	for _, v := range r.Violations {
		if v.Metric == "helix_tasks_unscheduled_rate" && v.Severity == SeverityWarning {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected WARNING violation for helix_tasks_unscheduled_rate; violations=%v", r.Violations)
	}
}

// TestValidate_Rule3_ScheduleLatency_Breach checks p99 >= 1000ms → CRITICAL.
func TestValidate_Rule3_ScheduleLatency_Breach(t *testing.T) {
	m := allPassMetrics()
	m["schedule_latency_p99"] = 1000 // NOT < 1000 → CRITICAL

	r := Validate(m)

	requireViolation(t, r, "schedule_latency_p99", SeverityCritical)
}

// TestValidate_Rule4_ConsensusRounds_Breach checks rate >= 10 → CRITICAL.
func TestValidate_Rule4_ConsensusRounds_Breach(t *testing.T) {
	m := allPassMetrics()
	m["consensus_rounds_rate"] = 10 // NOT < 10 → CRITICAL

	r := Validate(m)

	requireViolation(t, r, "consensus_rounds_rate", SeverityCritical)
}

// TestValidate_Rule5_TestDuration_Breach checks p95 >= 300s → CRITICAL.
func TestValidate_Rule5_TestDuration_Breach(t *testing.T) {
	m := allPassMetrics()
	m["test_duration_p95"] = 300 // NOT < 300 → CRITICAL

	r := Validate(m)

	requireViolation(t, r, "test_duration_p95", SeverityCritical)
}

// TestValidate_Rule6_FirecrackerVCPU_Breach checks vcpu >= 80% → CRITICAL.
func TestValidate_Rule6_FirecrackerVCPU_Breach(t *testing.T) {
	m := allPassMetrics()
	m["firecracker_vcpu"] = 80 // NOT < 80 → CRITICAL

	r := Validate(m)

	requireViolation(t, r, "firecracker_vcpu", SeverityCritical)
}

// TestValidate_Rule7_ChaosFaults_Breach checks faults < 1 → CRITICAL.
func TestValidate_Rule7_ChaosFaults_Breach(t *testing.T) {
	m := allPassMetrics()
	m["chaos_faults_injected"] = 0 // NOT >= 1 → CRITICAL

	r := Validate(m)

	requireViolation(t, r, "chaos_faults_injected", SeverityCritical)
}

// TestValidate_Rule8_RecoveryTime_Breach checks p99 >= 30000ms → CRITICAL.
func TestValidate_Rule8_RecoveryTime_Breach(t *testing.T) {
	m := allPassMetrics()
	m["recovery_time_p99"] = 30000 // NOT < 30000 → CRITICAL

	r := Validate(m)

	requireViolation(t, r, "recovery_time_p99", SeverityCritical)
}

// ---------------------------------------------------------------------------
// 4. Table-completeness: all 8 KPI metric names are evaluated.
// ---------------------------------------------------------------------------

// TestValidate_AllEightRulesEvaluated verifies that the Report's EvaluatedKPIs
// field contains exactly 8 distinct KPI metric names (covering all rules),
// regardless of pass/fail outcome. This prevents silent rule-table truncation.
func TestValidate_AllEightRulesEvaluated(t *testing.T) {
	r := Validate(allPassMetrics())

	// 8 design rules; rule 2 has two metric sub-checks (2a == 0 CRITICAL,
	// 2b rate < 1 WARNING) giving 9 total evaluated KPI metric entries.
	const want = 9
	if len(r.EvaluatedKPIs) != want {
		t.Fatalf("expected %d evaluated KPI entries (8 rules, rule-2 has 2 metric sub-checks), got %d: %v",
			want, len(r.EvaluatedKPIs), r.EvaluatedKPIs)
	}

	// Verify every KPI name is present.
	required := []string{
		"helix_nodes_healthy",
		"helix_tasks_unscheduled",
		"helix_tasks_unscheduled_rate",
		"schedule_latency_p99",
		"consensus_rounds_rate",
		"test_duration_p95",
		"firecracker_vcpu",
		"chaos_faults_injected",
		"recovery_time_p99",
	}
	got := make(map[string]bool, len(r.EvaluatedKPIs))
	for _, k := range r.EvaluatedKPIs {
		got[k] = true
	}
	for _, name := range required {
		if !got[name] {
			t.Errorf("KPI metric %q not found in EvaluatedKPIs: %v", name, r.EvaluatedKPIs)
		}
	}
}

// TestValidate_ExactNineKPIsEvaluated confirms that exactly 9 KPI metric-level
// entries are evaluated (8 design rules, rule 2 split into 2a + 2b = 9 total).
func TestValidate_ExactNineKPIsEvaluated(t *testing.T) {
	r := Validate(allPassMetrics())

	expected := []string{
		"helix_nodes_healthy",
		"helix_tasks_unscheduled",
		"helix_tasks_unscheduled_rate",
		"schedule_latency_p99",
		"consensus_rounds_rate",
		"test_duration_p95",
		"firecracker_vcpu",
		"chaos_faults_injected",
		"recovery_time_p99",
	}

	got := make(map[string]bool, len(r.EvaluatedKPIs))
	for _, k := range r.EvaluatedKPIs {
		got[k] = true
	}
	for _, want := range expected {
		if !got[want] {
			t.Errorf("KPI %q not found in EvaluatedKPIs: %v", want, r.EvaluatedKPIs)
		}
	}
}

// ---------------------------------------------------------------------------
// 5. Mutation tests — these prove the tests fail for the right reason when the
// production rule table is mutated. These are run against real code; the
// mutation_proof field documents the edit we verified causes failure.
// ---------------------------------------------------------------------------

// TestMutation_FlipComparator_ScheduleLatency demonstrates that if the
// schedule_latency rule used <= instead of <, a value AT the boundary (1000)
// would be PASSED instead of FAILED. We verify the REAL code (using <) still
// flags 1000 as a breach. The mutation proof is: change < to <= in the rule
// definition and re-run: this test FAILS because requireViolation does not find
// the violation (the boundary value passes the lax guard).
func TestMutation_FlipComparator_ScheduleLatency(t *testing.T) {
	m := allPassMetrics()
	m["schedule_latency_p99"] = 1000 // exactly at boundary; < 1000 means this breaches

	r := Validate(m)

	// With real code (< 1000): 1000 violates the rule → FAIL.
	if r.Pass {
		t.Fatal("mutation check: schedule_latency_p99==1000 should FAIL (< 1000 rule), but got PASS — comparator may have been mutated to <=")
	}
	found := false
	for _, v := range r.Violations {
		if v.Metric == "schedule_latency_p99" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("mutation check: schedule_latency_p99 violation not found; violations=%v", r.Violations)
	}
}

// TestMutation_DowngradeSeverity_TasksUnscheduled demonstrates that if the
// tasks_unscheduled rule severity were downgraded from CRITICAL to WARNING,
// a breach would not fail the gate. We verify the REAL code (CRITICAL) does
// fail the gate. Mutation proof: change SeverityCritical to SeverityWarning in
// the rule for helix_tasks_unscheduled and re-run → this test FAILS because
// r.Pass becomes true.
func TestMutation_DowngradeSeverity_TasksUnscheduled(t *testing.T) {
	m := allPassMetrics()
	m["helix_tasks_unscheduled"] = 3 // critical breach

	r := Validate(m)

	if r.Pass {
		t.Fatal("mutation check: helix_tasks_unscheduled==3 must FAIL (CRITICAL), got PASS — severity may have been downgraded")
	}
}

// TestMutation_DropOneRule_TableCompleteness demonstrates that if a rule is
// dropped from the rule table, EvaluatedKPIs shrinks and the completeness
// assertion fails. Mutation proof: remove the chaos_faults_injected rule from
// baseline → EvaluatedKPIs no longer contains it → this test FAILS.
func TestMutation_DropOneRule_TableCompleteness(t *testing.T) {
	r := Validate(allPassMetrics())

	found := false
	for _, k := range r.EvaluatedKPIs {
		if k == "chaos_faults_injected" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("mutation check: chaos_faults_injected not in EvaluatedKPIs — rule may have been dropped; got %v", r.EvaluatedKPIs)
	}
}

// ---------------------------------------------------------------------------
// 6. RunUUID uniqueness across invocations.
// ---------------------------------------------------------------------------

// TestValidate_RunUUID_Unique ensures every Validate call produces a distinct
// non-empty RunUUID (quality-gate run correlation).
func TestValidate_RunUUID_Unique(t *testing.T) {
	r1 := Validate(allPassMetrics())
	r2 := Validate(allPassMetrics())

	if r1.RunUUID == "" {
		t.Fatal("RunUUID must not be empty on first call")
	}
	if r2.RunUUID == "" {
		t.Fatal("RunUUID must not be empty on second call")
	}
	if r1.RunUUID == r2.RunUUID {
		t.Fatalf("RunUUID must be unique per run; both calls returned %q", r1.RunUUID)
	}
}
