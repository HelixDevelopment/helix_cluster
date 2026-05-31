package scheduler

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/HelixDevelopment/helix_cluster/pkg/classads"
)

// ClassAdMatch wires the pkg/classads expression engine into the scheduler as a
// real matching/ranking Plugin (Phase 3 "ClassAd" concept).
//
// Filter rejects a node whose ClassAd attributes do not satisfy the job's
// Requirements expression. Score ranks satisfying nodes by the job's Rank
// expression (higher = better). Both expressions are evaluated by
// pkg/classads against attributes derived from the node.
//
// It is additive and backward-compatible: it implements the existing Plugin
// interface (Name/Filter/Score/Bind) and stores its inputs in the existing
// Job.Labels / Node.Labels maps under reserved keys, so no field on Job or Node
// changes shape. Like the other scoring plugins in this package it does not
// consume resources in Bind (NodeResourcesFit owns resource accounting).
//
// Node ClassAd attributes are derived from Node.Labels: every label becomes an
// attribute, with values that look like numbers coerced to float64 and the
// literals "true"/"false" coerced to bool, so a Requirements expression such as
// "GPU >= 2" or a Rank such as "GPU + CPU" evaluates numerically.
type ClassAdMatch struct{}

// Reserved Job.Label keys carrying ClassAd expressions. They are namespaced so
// they never collide with the require_/prefer_ keys used by CapabilityMatch or
// the cost_* keys used by CostAwareGPUPlacement.
const (
	// LabelClassAdRequirements holds a boolean ClassAd Requirements expression.
	// A node passes Filter only when this expression evaluates to true against
	// the node's attributes. Absent/empty => the job has no ClassAd constraint
	// and every node passes this plugin's Filter.
	LabelClassAdRequirements = "classad_requirements"
	// LabelClassAdRank holds a numeric ClassAd Rank expression. Higher is
	// better. Absent/empty => this plugin contributes 0 to the score.
	LabelClassAdRank = "classad_rank"
)

// rankScale converts a fractional Rank value into integer score resolution
// while preserving strict ordering for the ranges used in practice.
const rankScale = 1000

func (c *ClassAdMatch) Name() string { return "ClassAdMatch" }

// SetRequirements attaches a ClassAd Requirements expression to a job in a
// backward-compatible way (it writes a reserved Job.Labels key, allocating the
// map if needed). Existing jobs that never call this are unaffected.
func SetRequirements(job *Job, expr string) {
	if job.Labels == nil {
		job.Labels = make(map[string]string)
	}
	job.Labels[LabelClassAdRequirements] = expr
}

// SetRank attaches a ClassAd Rank expression to a job (reserved Job.Labels key).
func SetRank(job *Job, expr string) {
	if job.Labels == nil {
		job.Labels = make(map[string]string)
	}
	job.Labels[LabelClassAdRank] = expr
}

// SetNodeAttribute records a ClassAd attribute on a node (reserved via the
// existing Node.Labels map). It is a thin, explicit alias for setting a label
// and exists so callers express intent ("this is a ClassAd attribute") without
// a new field on Node.
func SetNodeAttribute(node *Node, key, value string) {
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	node.Labels[key] = value
}

// Filter returns true when the job carries no ClassAd Requirements, or when the
// Requirements expression evaluates to true against the node's attributes. A
// node that does NOT satisfy the requirement, OR a malformed/erroring
// expression, is rejected deterministically (Filter returns false).
func (c *ClassAdMatch) Filter(job *Job, node *Node) bool {
	if job == nil || node == nil {
		return false
	}
	req := requirementsExpr(job)
	if req == "" {
		return true
	}
	ok, err := classads.Match(req, nodeAttributes(node))
	if err != nil {
		// Deterministic reject: an unsatisfiable/undefined/malformed expression
		// must never silently admit a node.
		return false
	}
	return ok
}

// Score ranks satisfying nodes by the job's Rank expression (higher = better).
// Nodes that fail Filter score 0. A job with no Rank expression contributes 0.
// A Rank expression that errors yields 0 (it must never crash the pipeline nor
// invent a ranking).
func (c *ClassAdMatch) Score(job *Job, node *Node) int {
	if !c.Filter(job, node) {
		return 0
	}
	rank := rankExpr(job)
	if rank == "" {
		return 0
	}
	v, err := classads.Eval(rank, nodeAttributes(node))
	if err != nil {
		return 0
	}
	f, ok := toRankFloat(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int(math.Round(f * rankScale))
}

// Bind is a no-op success for this matching/scoring plugin; it does not consume
// resources and must not break the bind pipeline.
func (c *ClassAdMatch) Bind(job *Job, node *Node) bool { return true }

// ClassAdError reports why a job's Requirements expression cannot admit a node.
// It is returned by RequirementsError to give callers a clear, inspectable error
// (the Plugin.Filter contract is boolean, so this is an explicit side-channel).
type ClassAdError struct {
	Expr string
	Err  error
}

func (e *ClassAdError) Error() string {
	return fmt.Sprintf("classad requirements %q: %v", e.Expr, e.Err)
}

func (e *ClassAdError) Unwrap() error { return e.Err }

// RequirementsError evaluates the job's Requirements against the node and
// returns a non-nil *ClassAdError when the expression is malformed or otherwise
// fails to evaluate. It returns nil when the expression is absent or evaluates
// cleanly (whether to true or false). This lets callers distinguish "node does
// not match" (nil error, Filter==false) from "expression is broken".
func (c *ClassAdMatch) RequirementsError(job *Job, node *Node) error {
	if job == nil || node == nil {
		return nil
	}
	req := requirementsExpr(job)
	if req == "" {
		return nil
	}
	if _, err := classads.Match(req, nodeAttributes(node)); err != nil {
		return &ClassAdError{Expr: req, Err: err}
	}
	return nil
}

func requirementsExpr(job *Job) string {
	if job.Labels == nil {
		return ""
	}
	return strings.TrimSpace(job.Labels[LabelClassAdRequirements])
}

func rankExpr(job *Job) string {
	if job.Labels == nil {
		return ""
	}
	return strings.TrimSpace(job.Labels[LabelClassAdRank])
}

// nodeAttributes projects a node into a ClassAd attribute set the expression
// engine can evaluate. Every label is exposed; numeric- and bool-looking values
// are coerced so relational/arithmetic operators work, while everything else
// stays a string (usable with == and regexp()). The synthesised attributes
// CPU/Memory/GPU expose the node's available resources so requirements can
// reference real capacity even when no matching label is present. Explicit
// labels take precedence over the synthesised resource attributes.
func nodeAttributes(node *Node) map[string]interface{} {
	attrs := make(map[string]interface{}, len(node.Labels)+3)
	attrs["CPU"] = node.AvailableResources.CPU
	attrs["Memory"] = float64(node.AvailableResources.Memory)
	attrs["GPU"] = float64(node.AvailableResources.GPU)
	for k, raw := range node.Labels {
		// Reserved scheduler-internal keys are never exposed as ClassAd
		// attributes (they are control data, not advertised capabilities).
		if k == LabelClassAdRequirements || k == LabelClassAdRank {
			continue
		}
		attrs[k] = coerceAttr(raw)
	}
	return attrs
}

// coerceAttr converts a label string into the most specific ClassAd value:
// bool for "true"/"false", float64 for numeric strings, else the raw string.
func coerceAttr(raw string) interface{} {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true":
		return true
	case "false":
		return false
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
		return f
	}
	return raw
}

// toRankFloat coerces a Rank result to float64. Bool ranks map to 1/0 so a
// boolean Rank still orders true above false.
func toRankFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}
