package scheduler

import (
	"strconv"
)

// CostAwareGPUPlacement is a cost-aware GPU placement scoring plugin
// (Phase 8C "Gepetto" concept). It prefers nodes that satisfy a job's
// GPU/resource requirement at the LOWEST cost.
//
// It is additive and backward-compatible: it implements the existing Plugin
// interface (Name/Filter/Score/Bind) and does not mutate node/job state in
// Bind (binding of resources remains the responsibility of NodeResourcesFit).
//
// Cost model (no external data sources are invented):
//   - If the node carries an explicit per-GPU-hour price label
//     ("cost_per_gpu_hour"), that value is used directly as the unit cost.
//   - Else if the node carries a whole-node hourly price label
//     ("cost_per_hour"), that value is amortised across the node's GPUs
//     (or the node as a whole when it has no GPUs).
//   - Else a sensible cost proxy is derived purely from existing Node fields:
//     richer nodes (more CPU/Memory/GPU available) are treated as more
//     expensive, so a job is steered onto the cheapest node that is still
//     adequate. This avoids "wasting" a large/expensive node on a small job.
//
// Score is HIGHER for cheaper adequate placements and is monotonically
// non-increasing in the cost proxy.
type CostAwareGPUPlacement struct{}

// Label keys consulted for explicit pricing, in priority order.
const (
	// LabelCostPerGPUHour is the preferred explicit price signal: the hourly
	// dollar (or arbitrary currency) cost of a single GPU on this node.
	LabelCostPerGPUHour = "cost_per_gpu_hour"
	// LabelCostPerHour is the whole-node hourly cost, amortised across GPUs.
	LabelCostPerHour = "cost_per_hour"
)

// costScoreBase is the baseline from which cost is subtracted so that cheaper
// placements yield a strictly higher (and non-negative for realistic inputs)
// score. It is large enough that realistic costs never push the score negative.
const costScoreBase = 1_000_000

// costScale converts a fractional unit cost into integer score resolution.
const costScale = 1000

func (c *CostAwareGPUPlacement) Name() string { return "CostAwareGPUPlacement" }

// Filter removes nodes that cannot satisfy the job's GPU requirement.
// A job requesting N GPUs is only placeable on a node with at least N GPUs
// available. Non-GPU jobs are never filtered out by this plugin.
func (c *CostAwareGPUPlacement) Filter(job *Job, node *Node) bool {
	if node == nil || job == nil {
		return false
	}
	if job.Resources.GPU <= 0 {
		return true
	}
	return node.AvailableResources.GPU >= job.Resources.GPU
}

// Score returns a higher value for nodes that can host the job's GPU need at a
// lower cost. Nodes that fail Filter score 0. The returned score equals
// costScoreBase minus the (scaled) unit cost of satisfying the job, so it is
// strictly monotonically decreasing in the cost proxy and deterministic.
func (c *CostAwareGPUPlacement) Score(job *Job, node *Node) int {
	if !c.Filter(job, node) {
		return 0
	}
	cost := c.unitCost(job, node)
	score := costScoreBase - int(cost*costScale)
	if score < 0 {
		score = 0
	}
	return score
}

// Bind is a no-op success for this scoring-only plugin; it does not consume
// resources (NodeResourcesFit owns resource accounting) and must not break the
// bind pipeline.
func (c *CostAwareGPUPlacement) Bind(job *Job, node *Node) bool { return true }

// unitCost computes the cost proxy for placing job on node. Lower is cheaper.
// The value is the cost attributable to the GPUs the job will occupy (or to
// the node as a whole for non-GPU jobs), so two nodes that both satisfy the
// job are ranked purely by how expensive that satisfaction is.
func (c *CostAwareGPUPlacement) unitCost(job *Job, node *Node) float64 {
	gpusNeeded := job.Resources.GPU
	if gpusNeeded < 0 {
		gpusNeeded = 0
	}

	// 1. Explicit per-GPU-hour price.
	if v, ok := parseCostLabel(node, LabelCostPerGPUHour); ok {
		if gpusNeeded == 0 {
			return v // non-GPU job: charge one GPU-equivalent unit.
		}
		return v * float64(gpusNeeded)
	}

	// 2. Whole-node hourly price, amortised across GPUs.
	if v, ok := parseCostLabel(node, LabelCostPerHour); ok {
		gpusOnNode := node.AvailableResources.GPU
		if gpusOnNode <= 0 {
			return v // no GPUs to amortise over: whole-node cost.
		}
		perGPU := v / float64(gpusOnNode)
		if gpusNeeded == 0 {
			return perGPU
		}
		return perGPU * float64(gpusNeeded)
	}

	// 3. Derived proxy from existing resource fields. Richer nodes cost more,
	// so a job is steered onto the cheapest adequate node. Weights mirror the
	// relative scarcity used elsewhere in this package (GPU >> CPU >> Memory).
	proxy := node.AvailableResources.CPU +
		float64(node.AvailableResources.Memory)/1024.0 +
		float64(node.AvailableResources.GPU)*10.0
	return proxy
}

// parseCostLabel reads and parses a non-negative float cost label from a node.
// It returns (0, false) when the label is absent, unparseable, or negative.
func parseCostLabel(node *Node, key string) (float64, bool) {
	if node.Labels == nil {
		return 0, false
	}
	raw, ok := node.Labels[key]
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}
