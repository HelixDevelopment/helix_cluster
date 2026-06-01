package scheduler

import (
	"math"

	"github.com/HelixDevelopment/helix_cluster/pkg/classads"
)

// ClassAdFilterPlugin is a named Plugin that implements the Requirements-only
// half of the ClassAd scheduling logic. It delegates Filter to the classads
// Requirements evaluation (reusing the helpers from classad_match.go) and
// returns a no-op Score=0 / Bind=true.
//
// Design rationale (HXC-1212): the existing ClassAdMatch fuses both Filter and
// Score into one closure. Operators that want separate, named plugins in the
// pipeline (e.g. to observe per-plugin metrics, to compose selectively, or to
// chain them with other plugins in between) use ClassAdFilterPlugin for
// requirements gating and ClassAdScorePlugin for ranking, without re-wiring
// any classads internals.
//
// Label contract (inherited from ClassAdMatch, no new labels):
//   - classad_requirements (LabelClassAdRequirements) on the Job
//   - node attributes derived via nodeAttributes() from Node.Labels + AvailableResources
type ClassAdFilterPlugin struct{}

func (c *ClassAdFilterPlugin) Name() string { return "ClassAdFilterPlugin" }

// Filter returns true when the node satisfies the job's Requirements expression,
// false otherwise. Semantics are identical to ClassAdMatch.Filter: absent/empty
// Requirements admits every node; a malformed/erroring expression deterministically
// rejects the node.
func (c *ClassAdFilterPlugin) Filter(job *Job, node *Node) bool {
	if node == nil {
		return false
	}
	if job == nil {
		// No job-side constraint: thermal/resource decisions still live with the node.
		// This plugin only gates on Requirements, so absent job == no constraint.
		return true
	}
	req := requirementsExpr(job)
	if req == "" {
		return true
	}
	ok, err := classads.Match(req, nodeAttributes(node))
	if err != nil {
		// Deterministic reject: a malformed expression must never silently admit a node.
		return false
	}
	return ok
}

// Score is a no-op for this plugin. ClassAdFilterPlugin is a pure admission gate;
// ranking is delegated to ClassAdScorePlugin.
func (c *ClassAdFilterPlugin) Score(job *Job, node *Node) int { return 0 }

// Bind is a no-op success; this plugin does not consume resources.
func (c *ClassAdFilterPlugin) Bind(job *Job, node *Node) bool { return true }

// ClassAdScorePlugin is a named Plugin that implements the Rank-only half of
// the ClassAd scheduling logic. It delegates Score to the classads Rank
// evaluation (reusing helpers from classad_match.go) and returns a pass-all
// Filter=true / Bind=true.
//
// Design rationale (HXC-1212): see ClassAdFilterPlugin. Operators use this
// plugin alongside ClassAdFilterPlugin (or standalone) to rank already-admitted
// nodes by a job's Rank expression without mixing in the admission gate.
//
// Label contract (inherited from ClassAdMatch):
//   - classad_rank (LabelClassAdRank) on the Job
//   - node attributes derived via nodeAttributes()
type ClassAdScorePlugin struct{}

func (c *ClassAdScorePlugin) Name() string { return "ClassAdScorePlugin" }

// Filter always returns true: ClassAdScorePlugin is a scorer, not a gate.
// Admission is the responsibility of ClassAdFilterPlugin or any other filter plugin.
func (c *ClassAdScorePlugin) Filter(job *Job, node *Node) bool { return true }

// Score ranks the node by the job's Rank expression (higher = better). Semantics
// are identical to ClassAdMatch.Score except it does not re-check Filter (since
// this plugin's Filter is always true). A missing/empty Rank contributes 0. A
// malformed or erroring Rank expression yields 0 (never crashes the pipeline).
func (c *ClassAdScorePlugin) Score(job *Job, node *Node) int {
	if job == nil || node == nil {
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

// Bind is a no-op success; this plugin does not consume resources.
func (c *ClassAdScorePlugin) Bind(job *Job, node *Node) bool { return true }
