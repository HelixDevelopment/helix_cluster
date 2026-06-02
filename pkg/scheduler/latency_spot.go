package scheduler

import (
	"strconv"
	"strings"
)

// LatencySpotPolicy is a latency-aware and spot/preemptible-aware scheduling
// Plugin (HXC-1365).
//
// It addresses two interrelated real-world placement concerns:
//
//  1. NETWORK LATENCY: jobs that are sensitive to round-trip latency to the
//     job origin should prefer low-latency nodes. Jobs that are not
//     latency-sensitive (batch workloads) are indifferent to latency.
//
//  2. SPOT/PREEMPTIBLE CAPACITY: spot and preemptible nodes are cheaper but
//     can be reclaimed by the cloud provider at any time. A latency-sensitive
//     interactive job should avoid them (risk of mid-session preemption). A
//     batch job can embrace them for lower cost.
//
// # Label contract — Node.Labels
//
//   - LabelLatencyMs      ("latency_ms")     — measured or estimated network
//     latency from this node to the job origin, in milliseconds, as a
//     non-negative float/int string (e.g. "12", "5.5"). Lower is better.
//     Absent/unparseable => assume-safe neutral (no latency penalty applied).
//
//   - LabelPreemptible    ("preemptible")    — "true" if the node is a spot or
//     preemptible instance; "false" or absent => non-preemptible.
//
//   - LabelSpotDiscount   ("spot_discount")  — optional cost factor in the range
//     (0, 1], representing the discounted price relative to on-demand (e.g.
//     "0.3" = 70 % cheaper). Currently reserved for future use; parsed but not
//     yet incorporated into Score to keep scoring deterministic and testable.
//
// # Label contract — Job.Labels
//
//   - LabelLatencySensitive ("latency_sensitive") — "true" if the job cares
//     about network latency (interactive workloads, real-time pipelines).
//     "false" or absent => batch / latency-indifferent.
//
// # Score semantics
//
// Base score: latencySpotScoreBase (a large positive constant so realistic
// penalties never push the final score below zero).
//
//	Latency term (always applied when latency_ms is present):
//	  score -= int(latency_ms * latencyPenaltyPerMs)
//	  Lower latency_ms => smaller penalty => strictly higher score.
//	  CRITICAL invariant (mutation target): the sign is subtraction.
//	  Flipping to addition would invert the ordering — the tests catch this.
//
//	Preemptible term for latency-sensitive jobs:
//	  score -= preemptiblePenalty   (if node is preemptible AND job is sensitive)
//	  CRITICAL invariant (mutation target): sensitive jobs are penalised on
//	  preemptible nodes. Removing this penalty breaks the sensitive-job
//	  ordering assertion in the tests.
//
//	Preemptible term for non-latency-sensitive (batch) jobs:
//	  score += spotBonus            (if node is preemptible AND job is NOT sensitive)
//	  Batch jobs get a small bonus for choosing cheap spot capacity.
//
//	Absent latency_ms: no latency penalty is applied (assume-safe).
//	Absent preemptible label (or "false"): no preemptible adjustment.
//	Absent latency_sensitive label: treated as non-sensitive (batch path).
//
// # Filter semantics
//
// By default Filter is conservative and always returns true (no node is
// rejected on latency alone). An optional MaxLatencyMs threshold may be set:
// when MaxLatencyMs > 0 AND the node's latency_ms label is present and
// parseable AND latency_ms > MaxLatencyMs, the node is rejected. Absent or
// unparseable latency_ms is treated as safe (node admitted).
//
// # Plugin identity
//
// Name() returns "LatencySpotPolicy".
// Bind() is a no-op success (resource accounting is owned by NodeResourcesFit).
type LatencySpotPolicy struct {
	// MaxLatencyMs is an optional hard latency cap. When > 0, nodes whose
	// latency_ms label is present and exceeds this value are rejected by
	// Filter. Zero (default) disables the cap — Filter always returns true.
	MaxLatencyMs float64
}

// Label keys introduced by LatencySpotPolicy — non-clashing with all existing
// label constants in this package.
const (
	// LabelLatencyMs is the node label key for measured/estimated network
	// latency in milliseconds from this node to the job origin.
	// Value: non-negative float/int string, e.g. "12" or "5.5".
	// Absent/unparseable: assume-safe (no latency penalty).
	LabelLatencyMs = "latency_ms"

	// LabelPreemptible is the node label key indicating spot/preemptible
	// status. Value: "true" or "false". Absent => treated as "false".
	LabelPreemptible = "preemptible"

	// LabelSpotDiscount is the node label key for the cost discount factor
	// in the range (0, 1] relative to on-demand pricing (e.g. "0.3").
	// Currently reserved for future scoring extensions; parsed but not yet
	// incorporated into Score.
	LabelSpotDiscount = "spot_discount"

	// LabelLatencySensitive is the job label key flagging a workload as
	// latency-sensitive. Value: "true" or "false". Absent => "false".
	LabelLatencySensitive = "latency_sensitive"
)

// Scoring constants — exported so tests can assert against exact values.
const (
	// latencySpotScoreBase is the baseline score before penalties/bonuses.
	// Large enough that realistic latency penalties (up to ~1000 ms) and
	// preemptible penalties never push the total below zero.
	latencySpotScoreBase = 500_000

	// latencyPenaltyPerMs is subtracted from the score per millisecond of
	// reported latency. Lower latency => smaller penalty => higher score.
	// CRITICAL: sign is subtraction. Mutation target.
	latencyPenaltyPerMs = 100

	// preemptiblePenalty is subtracted from the score when a latency-sensitive
	// job is placed on a preemptible node. CRITICAL: must be subtracted, not
	// added. Mutation target.
	preemptiblePenalty = 50_000

	// spotBonus is added to the score when a non-latency-sensitive (batch) job
	// is placed on a preemptible (spot) node — reward for using cheap capacity.
	spotBonus = 10_000
)

// Name returns the plugin's identifier.
func (p *LatencySpotPolicy) Name() string { return "LatencySpotPolicy" }

// Filter returns true (admit) for all nodes by default. When MaxLatencyMs > 0
// it additionally rejects nodes whose latency_ms label is present, parseable,
// and strictly greater than MaxLatencyMs. Absent or unparseable latency_ms is
// treated as safe (assume-safe principle).
func (p *LatencySpotPolicy) Filter(job *Job, node *Node) bool {
	if node == nil {
		return false
	}
	// When no hard cap is configured, every node is admitted.
	if p.MaxLatencyMs <= 0 {
		return true
	}
	// Hard cap: reject only if latency_ms is present AND exceeds the cap.
	// Absent/unparseable => assume-safe, admit.
	if latMs, ok := parseLatencyLabel(node); ok {
		if latMs > p.MaxLatencyMs {
			return false
		}
	}
	return true
}

// Score returns a deterministic placement preference score. Higher is better.
// A nil node scores 0.
//
// The score is:
//
//	latencySpotScoreBase
//	  − latency_ms * latencyPenaltyPerMs          (if latency_ms is present)
//	  − preemptiblePenalty                         (if job is latency-sensitive AND node is preemptible)
//	  + spotBonus                                  (if job is NOT latency-sensitive AND node is preemptible)
func (p *LatencySpotPolicy) Score(job *Job, node *Node) int {
	if node == nil {
		return 0
	}

	score := latencySpotScoreBase

	// Latency penalty: subtract latency_ms * k so that lower latency yields
	// a strictly higher score. Absent label => no penalty (assume-safe).
	// CRITICAL SIGN: subtraction. Mutation (addition) inverts lower<->higher.
	if latMs, ok := parseLatencyLabel(node); ok {
		score -= int(latMs * latencyPenaltyPerMs)
	}

	// Preemptible adjustment depends on whether the job is latency-sensitive.
	preemptible := isPreemptible(node)
	sensitive := isLatencySensitive(job)

	if preemptible {
		if sensitive {
			// Latency-sensitive jobs avoid preemptible nodes: they risk
			// mid-job reclamation which is especially harmful for interactive
			// or real-time workloads.
			// CRITICAL: subtract, not add. Mutation target.
			score -= preemptiblePenalty
		} else {
			// Batch / non-sensitive jobs benefit from cheap spot capacity.
			score += spotBonus
		}
	}

	return score
}

// Bind is a no-op success. LatencySpotPolicy does not consume resources;
// resource accounting is owned by NodeResourcesFit.
func (p *LatencySpotPolicy) Bind(_ *Job, _ *Node) bool { return true }

// parseLatencyLabel reads the node's latency_ms label as a non-negative
// float64. Returns (value, true) on success; (0, false) when the label is
// absent, unparseable, or negative.
func parseLatencyLabel(node *Node) (float64, bool) {
	if node.Labels == nil {
		return 0, false
	}
	raw, ok := node.Labels[LabelLatencyMs]
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// isPreemptible returns true iff the node's "preemptible" label is "true"
// (case-insensitive). Absent or any other value => false (non-preemptible).
func isPreemptible(node *Node) bool {
	if node.Labels == nil {
		return false
	}
	return strings.ToLower(strings.TrimSpace(node.Labels[LabelPreemptible])) == "true"
}

// isLatencySensitive returns true iff the job's "latency_sensitive" label is
// "true" (case-insensitive). Absent or any other value => false (batch path).
func isLatencySensitive(job *Job) bool {
	if job == nil || job.Labels == nil {
		return false
	}
	return strings.ToLower(strings.TrimSpace(job.Labels[LabelLatencySensitive])) == "true"
}
