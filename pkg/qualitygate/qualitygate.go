// Package qualitygate implements the Helix Cluster OS KPI baseline validation
// and CI quality gate. It evaluates a metric snapshot against a fixed 8-rule
// baseline table and produces a Report with overall PASS/FAIL verdict plus
// per-rule Violation details.
//
// A breach at or above CRITICAL severity causes the overall Report to be FAIL,
// which drives the CI quality gate to reject the build. WARNING violations are
// recorded but do not change the overall verdict.
//
// Every Report is tagged with a per-run UUID (crypto/rand) so CI log
// correlation is unambiguous.
package qualitygate

import (
	"crypto/rand"
	"fmt"
	"math"
)

// Severity constants for KPI rule violations.
const (
	// SeverityCritical means a breach causes overall FAIL.
	SeverityCritical = "critical"
	// SeverityWarning means a breach is recorded but does NOT cause overall FAIL.
	SeverityWarning = "warning"
)

// Comparator strings used in Violation output.
const (
	CmpGTE = ">="
	CmpEQ  = "=="
	CmpLT  = "<"
)

// Violation records a single KPI rule breach.
type Violation struct {
	// Metric is the metric key that breached its rule.
	Metric string
	// Severity is SeverityCritical or SeverityWarning.
	Severity string
	// Actual is the observed metric value.
	Actual float64
	// Threshold is the rule threshold value.
	// For the helix_nodes_healthy rule the threshold is computed from
	// helix_nodes_total and is stored here as the computed value.
	Threshold float64
	// Comparator is the rule comparator string (>=, ==, <).
	Comparator string
}

// Report is the output of Validate.
type Report struct {
	// Pass is true when no CRITICAL rule was breached.
	Pass bool
	// Violations lists every breached rule (CRITICAL and WARNING).
	Violations []Violation
	// EvaluatedKPIs lists the metric keys that were evaluated, in rule-table
	// order. Its length equals the number of rule entries evaluated (9 for
	// 8 design rules, because rule 2 has two metric sub-checks).
	EvaluatedKPIs []string
	// RunUUID is a per-invocation UUID for CI log correlation.
	RunUUID string
}

// ---------------------------------------------------------------------------
// Rule table
// ---------------------------------------------------------------------------

// comparatorKind enumerates how a rule is evaluated.
type comparatorKind int

const (
	cmpGTE comparatorKind = iota // value >= threshold
	cmpEQ                        // value == threshold
	cmpLT                        // value < threshold
)

// kpiRule defines a single KPI gate rule.
type kpiRule struct {
	// metric is the primary metric key in the input map.
	metric string
	// comparator is the comparison direction.
	comparator comparatorKind
	// threshold is the static threshold.
	// For dynamic rules (helix_nodes_healthy) threshold is 0 and
	// thresholdFn is set.
	threshold float64
	// thresholdFn computes the threshold from the metric map.
	// When nil, threshold is used directly.
	thresholdFn func(metrics map[string]float64) float64
	// severity is SeverityCritical or SeverityWarning.
	severity string
}

// comparatorString returns the human-readable comparator symbol.
func (r kpiRule) comparatorString() string {
	switch r.comparator {
	case cmpGTE:
		return CmpGTE
	case cmpEQ:
		return CmpEQ
	case cmpLT:
		return CmpLT
	default:
		return "?"
	}
}

// evalThreshold returns the effective threshold value for a given metric map.
func (r kpiRule) evalThreshold(metrics map[string]float64) float64 {
	if r.thresholdFn != nil {
		return r.thresholdFn(metrics)
	}
	return r.threshold
}

// baseline is the fixed 8-rule (9 metric-level) KPI table.
//
// Rule index → design description:
//  1. helix_nodes_healthy >= floor(helix_nodes_total*0.5)+1  CRITICAL
//  2a. helix_tasks_unscheduled == 0                           CRITICAL
//  2b. helix_tasks_unscheduled rate < 1                       WARNING
//  3. schedule_latency p99 < 1000 ms                          CRITICAL
//  4. consensus_rounds rate < 10                              CRITICAL
//  5. test_duration p95 < 300 s                               CRITICAL
//  6. firecracker_vcpu < 80 %                                 CRITICAL
//  7. chaos_faults_injected >= 1                              CRITICAL
//  8. recovery_time p99 < 30000 ms                            CRITICAL
var baseline = []kpiRule{
	{
		metric:     "helix_nodes_healthy",
		comparator: cmpGTE,
		thresholdFn: func(metrics map[string]float64) float64 {
			total := metrics["helix_nodes_total"]
			return math.Floor(total*0.5) + 1
		},
		severity: SeverityCritical,
	},
	{
		metric:     "helix_tasks_unscheduled",
		comparator: cmpEQ,
		threshold:  0,
		severity:   SeverityCritical,
	},
	{
		metric:     "helix_tasks_unscheduled_rate",
		comparator: cmpLT,
		threshold:  1,
		severity:   SeverityWarning,
	},
	{
		metric:     "schedule_latency_p99",
		comparator: cmpLT,
		threshold:  1000,
		severity:   SeverityCritical,
	},
	{
		metric:     "consensus_rounds_rate",
		comparator: cmpLT,
		threshold:  10,
		severity:   SeverityCritical,
	},
	{
		metric:     "test_duration_p95",
		comparator: cmpLT,
		threshold:  300,
		severity:   SeverityCritical,
	},
	{
		metric:     "firecracker_vcpu",
		comparator: cmpLT,
		threshold:  80,
		severity:   SeverityCritical,
	},
	{
		metric:     "chaos_faults_injected",
		comparator: cmpGTE,
		threshold:  1,
		severity:   SeverityCritical,
	},
	{
		metric:     "recovery_time_p99",
		comparator: cmpLT,
		threshold:  30000,
		severity:   SeverityCritical,
	},
}

// ---------------------------------------------------------------------------
// Validate
// ---------------------------------------------------------------------------

// Validate evaluates metrics against the baseline KPI rule table and returns a
// Report. A CRITICAL breach causes Report.Pass=false. WARNING breaches are
// recorded but do not change the overall verdict.
func Validate(metrics map[string]float64) Report {
	r := Report{
		Pass:          true,
		EvaluatedKPIs: make([]string, 0, len(baseline)),
		RunUUID:       newUUID(),
	}

	for _, rule := range baseline {
		r.EvaluatedKPIs = append(r.EvaluatedKPIs, rule.metric)

		actual, ok := metrics[rule.metric]
		if !ok {
			// Missing metric: treat as zero.
			actual = 0
		}

		threshold := rule.evalThreshold(metrics)

		breached := false
		switch rule.comparator {
		case cmpGTE:
			breached = actual < threshold
		case cmpEQ:
			breached = actual != threshold
		case cmpLT:
			breached = actual >= threshold
		}

		if breached {
			v := Violation{
				Metric:     rule.metric,
				Severity:   rule.severity,
				Actual:     actual,
				Threshold:  threshold,
				Comparator: rule.comparatorString(),
			}
			r.Violations = append(r.Violations, v)
			if rule.severity == SeverityCritical {
				r.Pass = false
			}
		}
	}

	return r
}

// ---------------------------------------------------------------------------
// UUID helper (stdlib only)
// ---------------------------------------------------------------------------

// newUUID returns a random UUID string (version 4, RFC 4122).
func newUUID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback: use a fixed sentinel so callers can detect entropy failure.
		return "00000000-0000-0000-0000-000000000000"
	}
	// Set version 4 and variant bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
