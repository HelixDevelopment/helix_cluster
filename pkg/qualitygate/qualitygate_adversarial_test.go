// Package qualitygate_test — ADVERSARIAL sink-side probes for the KPI quality
// gate. Every assertion targets the actual PASS/FAIL verdict (Report.Pass) and
// the recorded Violations — never "no panic". These tests hunt for the
// covgate-family fail-open: a gate that PASSES when it must FAIL on no-data,
// boundary, numeric-poison, or one-failing-criterion inputs (CLAUDE-1).
//
// Contract under test (from qualitygate.go doc + rule table):
//   - Validate returns Report.Pass=true iff NO CRITICAL rule is breached.
//   - Each cmpLT CRITICAL rule encodes an upper-bound safety promise:
//     "metric < threshold" (e.g. schedule_latency_p99 < 1000ms). A value that
//     does NOT satisfy "< threshold" is, by the rule's own semantics, a breach.
//   - Each cmpGTE CRITICAL rule encodes a lower-bound promise:
//     "metric >= threshold" (quorum / chaos faults).
//   - Missing metric is documented as "treat as zero".
package qualitygate_test

import (
	"math"
	"testing"

	. "github.com/HelixDevelopment/helix_cluster/pkg/qualitygate"
)

// adversarialAllPass mirrors the in-range baseline used by the unit tests so
// each probe perturbs exactly one metric.
func adversarialAllPass() map[string]float64 {
	return map[string]float64{
		"helix_nodes_healthy":          5,
		"helix_nodes_total":            6,
		"helix_tasks_unscheduled":      0,
		"helix_tasks_unscheduled_rate": 0.5,
		"schedule_latency_p99":         800,
		"consensus_rounds_rate":        7,
		"test_duration_p95":            250,
		"firecracker_vcpu":             60,
		"chaos_faults_injected":        1,
		"recovery_time_p99":            20000,
	}
}

// ---------------------------------------------------------------------------
// (a) FAIL-OPEN ON NO DATA — empty / nil metric map.
//
// With every metric missing → treated as zero. Rule 1 (helix_nodes_healthy >=
// floor(0*0.5)+1 = 1) and rule 7 (chaos_faults_injected >= 1) both breach at
// zero. So the gate MUST FAIL on no data. This pins that no-data is not a pass.
// ---------------------------------------------------------------------------

func TestAdversarial_NoData_EmptyMap_MustFail(t *testing.T) {
	r := Validate(map[string]float64{})
	if r.Pass {
		t.Fatalf("FAIL-OPEN: empty metric map produced PASS; a no-data quality gate must FAIL (CLAUDE-1). violations=%v", r.Violations)
	}
}

func TestAdversarial_NoData_NilMap_MustFail(t *testing.T) {
	r := Validate(nil)
	if r.Pass {
		t.Fatalf("FAIL-OPEN: nil metric map produced PASS; a no-data quality gate must FAIL (CLAUDE-1). violations=%v", r.Violations)
	}
}

// ---------------------------------------------------------------------------
// (b) THRESHOLD BOUNDARY — assert the documented comparators exactly.
// schedule_latency_p99 rule is "< 1000": value == 1000 must FAIL, 999.999...
// must PASS. recovery_time_p99 "< 30000": == 30000 must FAIL.
// chaos_faults_injected ">= 1": == 1 must PASS, epsilon-below (0.999..) FAIL.
// ---------------------------------------------------------------------------

func TestAdversarial_Boundary_ScheduleLatency_Exact(t *testing.T) {
	m := adversarialAllPass()
	m["schedule_latency_p99"] = 1000 // exactly == threshold; rule is "< 1000" → breach
	if Validate(m).Pass {
		t.Fatal("BOUNDARY: schedule_latency_p99==1000 must FAIL the '< 1000' rule, got PASS")
	}

	m["schedule_latency_p99"] = math.Nextafter(1000, 0) // epsilon below 1000 → pass
	if r := Validate(m); !r.Pass {
		t.Fatalf("BOUNDARY: schedule_latency_p99 just below 1000 must PASS, got FAIL; violations=%v", r.Violations)
	}
}

func TestAdversarial_Boundary_ChaosFaults_GTE(t *testing.T) {
	m := adversarialAllPass()
	m["chaos_faults_injected"] = 1 // == threshold; rule is ">= 1" → pass
	if r := Validate(m); !r.Pass {
		t.Fatalf("BOUNDARY: chaos_faults_injected==1 must PASS the '>= 1' rule, got FAIL; violations=%v", r.Violations)
	}

	m["chaos_faults_injected"] = math.Nextafter(1, 0) // just below 1 → breach
	if Validate(m).Pass {
		t.Fatal("BOUNDARY: chaos_faults_injected just below 1 must FAIL the '>= 1' rule, got PASS")
	}
}

// ---------------------------------------------------------------------------
// (c) NUMERIC POISON — the covgate-family fail-open.
//
// A cmpLT CRITICAL rule promises "metric < threshold". The implementation
// computes breach as "actual >= threshold". For a poisoned/undefined metric
// value (NaN — e.g. a 0/0 in metric collection, or an absent measurement that
// arrived as NaN rather than missing), NaN >= threshold is FALSE, so the gate
// records NO breach and the verdict is PASS. But NaN does NOT satisfy the rule
// "p99 < 1000" either — it is unordered. A safety gate that PASSES on a
// poisoned latency/recovery metric is a fail-open PASS-bluff (CLAUDE-1).
//
// This probe asserts the SINK: the verdict on a NaN-poisoned CRITICAL metric.
// It FAILS on unfixed prod (documenting the bug) and PASSES once the gate
// treats a non-satisfying (NaN) value as a breach.
// ---------------------------------------------------------------------------

func TestAdversarial_NumericPoison_NaNLatency_MustFail(t *testing.T) {
	m := adversarialAllPass()
	m["schedule_latency_p99"] = math.NaN() // poisoned: does NOT satisfy "< 1000"

	r := Validate(m)

	if r.Pass {
		t.Fatalf("FAIL-OPEN (numeric poison): schedule_latency_p99=NaN does not satisfy the '< 1000' CRITICAL rule, yet gate returned PASS. A poisoned safety metric must FAIL the gate (CLAUDE-1). violations=%v", r.Violations)
	}
}

func TestAdversarial_NumericPoison_NaNRecoveryTime_MustFail(t *testing.T) {
	m := adversarialAllPass()
	m["recovery_time_p99"] = math.NaN() // does NOT satisfy "< 30000"

	r := Validate(m)

	if r.Pass {
		t.Fatalf("FAIL-OPEN (numeric poison): recovery_time_p99=NaN does not satisfy the '< 30000' CRITICAL rule, yet gate returned PASS. violations=%v", r.Violations)
	}
}

func TestAdversarial_NumericPoison_NaNVCPU_MustFail(t *testing.T) {
	m := adversarialAllPass()
	m["firecracker_vcpu"] = math.NaN() // does NOT satisfy "< 80"

	r := Validate(m)

	if r.Pass {
		t.Fatalf("FAIL-OPEN (numeric poison): firecracker_vcpu=NaN does not satisfy the '< 80' CRITICAL rule, yet gate returned PASS. violations=%v", r.Violations)
	}
}

// +Inf on a cmpLT rule DOES satisfy actual>=threshold → breach → FAIL. This is
// the correct direction already; pin it so a future "guard NaN/Inf" change does
// not accidentally pass +Inf.
func TestAdversarial_NumericPoison_PosInfLatency_MustFail(t *testing.T) {
	m := adversarialAllPass()
	m["schedule_latency_p99"] = math.Inf(1) // +Inf >= 1000 → breach
	if Validate(m).Pass {
		t.Fatal("schedule_latency_p99=+Inf must FAIL the '< 1000' rule, got PASS")
	}
}

// NaN on the cmpEQ CRITICAL rule (helix_tasks_unscheduled == 0): NaN != 0 is
// true → breach → FAIL. Pin this correct direction.
func TestAdversarial_NumericPoison_NaNTasksUnscheduled_MustFail(t *testing.T) {
	m := adversarialAllPass()
	m["helix_tasks_unscheduled"] = math.NaN() // NaN != 0 → breach
	if Validate(m).Pass {
		t.Fatal("helix_tasks_unscheduled=NaN must FAIL the '== 0' rule, got PASS")
	}
}

// NaN feeding the DYNAMIC threshold fn (helix_nodes_total=NaN →
// floor(NaN*0.5)+1 = NaN). Rule 1 is cmpGTE: breach = actual < NaN → false.
// helix_nodes_healthy stays "not breached" regardless of how few nodes are
// healthy, because the threshold itself is poisoned. That is a quorum gate
// going blind. Assert the SINK verdict.
func TestAdversarial_NumericPoison_NaNNodesTotal_PoisonsQuorumThreshold(t *testing.T) {
	m := adversarialAllPass()
	m["helix_nodes_total"] = math.NaN()
	m["helix_nodes_healthy"] = 0 // zero healthy nodes — quorum is obviously lost

	r := Validate(m)

	if r.Pass {
		t.Fatalf("FAIL-OPEN (numeric poison): helix_nodes_total=NaN poisons the quorum threshold so 0 healthy nodes PASSES the quorum gate. A blind quorum gate must FAIL (CLAUDE-1). violations=%v", r.Violations)
	}
}

// ---------------------------------------------------------------------------
// (d) AGGREGATION / TOTAL-ORDER — one failing CRITICAL criterion must fail the
// whole gate (AND semantics), and must not be lost among passing rules.
// ---------------------------------------------------------------------------

func TestAdversarial_Aggregation_OneCriticalFailsWholeGate(t *testing.T) {
	m := adversarialAllPass()
	// Exactly one CRITICAL breach amid 8 passing criteria.
	m["consensus_rounds_rate"] = 999

	r := Validate(m)
	if r.Pass {
		t.Fatalf("AGGREGATION: a single CRITICAL breach must FAIL the whole gate (AND semantics), got PASS; violations=%v", r.Violations)
	}
	// The failing criterion must be the recorded one (not averaged away).
	found := false
	for _, v := range r.Violations {
		if v.Metric == "consensus_rounds_rate" && v.Severity == SeverityCritical {
			found = true
		}
	}
	if !found {
		t.Fatalf("AGGREGATION: the one failing criterion must be recorded; violations=%v", r.Violations)
	}
}

// Many WARNING-only breaches must never sum into a FAIL, and one CRITICAL among
// many warnings must still FAIL — proving severity is not folded into a score.
func TestAdversarial_Aggregation_WarningsNeverFail_ButCriticalDoes(t *testing.T) {
	m := adversarialAllPass()
	m["helix_tasks_unscheduled_rate"] = 1000 // WARNING breach only
	if r := Validate(m); !r.Pass {
		t.Fatalf("AGGREGATION: warning-only breach must not FAIL the gate; violations=%v", r.Violations)
	}

	m["test_duration_p95"] = 100000 // add a CRITICAL breach
	if Validate(m).Pass {
		t.Fatal("AGGREGATION: a CRITICAL breach alongside a WARNING must FAIL the gate, got PASS")
	}
}
