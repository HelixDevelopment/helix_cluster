package scheduler

import (
	"strconv"
	"strings"

	"github.com/HelixDevelopment/helix_cluster/pkg/device"
)

// GPUTierFilter is a GPU-TIER-aware placement Plugin. Where CostAwareGPUPlacement
// (cost_gpu.go) filters strictly on GPU COUNT, this plugin filters and scores on
// the GPU/compute-class TIER of a node, using the canonical device.Tier ladder
// (pkg/device, T1..T15). It lets a workload:
//
//   - HARD-EXCLUDE disallowed tiers: a node whose GPU tier is in the workload's
//     disallowed set is filtered OUT (e.g. a job that must never land on a
//     low-end edge GPU, or one barred from a flagship tier it is not licensed
//     for). This is an admission decision and is enforced in Filter.
//
//   - SOFT-PREFER preferred tiers: among admitted nodes, a node whose GPU tier
//     is in the workload's preferred set scores strictly HIGHER than an equally
//     admitted non-preferred node, so the scheduler orders preferred-tier nodes
//     first WITHOUT excluding the others. This is a ranking decision and is
//     applied in Score.
//
// It is additive and backward-compatible: it implements the existing Plugin
// interface (Name/Filter/Score/Bind) and stores all inputs in the existing
// Job.Labels / Node.Labels maps under reserved, namespaced keys (mirroring
// CostAwareGPUPlacement and TierPowerAware). No field on Job or Node changes
// shape. It does NOT consume resources in Bind (NodeResourcesFit owns resource
// accounting), so it never breaks the bind pipeline.
//
// It deliberately REUSES the repo's device.Tier type rather than inventing a new
// tier enum, so a node's GPU tier here is the same value pkg/device classifies a
// device into. The tier is supplied as INPUT via a label (a real deployment
// populates it from a node-agent that runs device.ClassifyTier); this plugin
// does not probe hardware itself.
//
// COMPOSITION: this filter is orthogonal to the GPU-count filter. Use both in a
// plugin chain (every plugin's Filter must pass) so a node is admitted only when
// it has BOTH enough GPUs AND an allowed tier — see ComposeFilter, which makes
// the AND-composition explicit and testable.
type GPUTierFilter struct{}

// Reserved label keys. Tiers are encoded as canonical labels ("T5"), the same
// representation device.Tier.String() produces, and parsed case-insensitively
// with an optional leading "T". Sets are comma-separated lists of such tokens.
const (
	// LabelNodeGPUTier is the node's GPU/compute-class tier (e.g. "T7"), as
	// classified by pkg/device. Absent/unparseable => device.TierUnknown ("T0").
	LabelNodeGPUTier = "gpu_tier"
	// LabelJobDisallowedTiers is a comma-separated set of tiers the job must
	// NEVER run on (hard exclude). A node whose GPU tier is in this set is
	// filtered OUT. Absent/empty => no tier is disallowed.
	LabelJobDisallowedTiers = "disallowed_gpu_tiers"
	// LabelJobPreferredTiers is a comma-separated set of tiers the job PREFERS
	// (soft). A node whose GPU tier is in this set scores higher; a node not in
	// the set is still admitted. Absent/empty => no preference (all admitted
	// nodes score equally for this plugin).
	LabelJobPreferredTiers = "preferred_gpu_tiers"
)

// gpuTierScoreBase is the baseline score for an admitted node. Mirrors
// costScoreBase / tierPowerScoreBase so this plugin composes cleanly with the
// other scoring plugins in the same range.
const gpuTierScoreBase = 1_000_000

// gpuTierPreferenceBonus is added when an admitted node's tier is in the job's
// preferred set, so a preferred-tier node strictly outranks a non-preferred but
// still-admitted node. Large enough to dominate within this plugin's own score.
const gpuTierPreferenceBonus = 100_000

func (g *GPUTierFilter) Name() string { return "GPUTierFilter" }

// SetNodeGPUTier records a node's GPU tier (reserved Node.Labels key) using the
// canonical device.Tier label, allocating the map if needed. Backward-compatible:
// nodes that never call this default to device.TierUnknown.
func SetNodeGPUTier(node *Node, tier device.Tier) {
	ensureNodeLabels(node)
	node.Labels[LabelNodeGPUTier] = tier.String()
}

// SetJobDisallowedTiers records the job's hard-excluded GPU tiers (reserved
// Job.Labels key) as a canonical comma-separated set.
func SetJobDisallowedTiers(job *Job, tiers ...device.Tier) {
	ensureJobLabels(job)
	job.Labels[LabelJobDisallowedTiers] = joinTiers(tiers)
}

// SetJobPreferredTiers records the job's soft-preferred GPU tiers (reserved
// Job.Labels key) as a canonical comma-separated set.
func SetJobPreferredTiers(job *Job, tiers ...device.Tier) {
	ensureJobLabels(job)
	job.Labels[LabelJobPreferredTiers] = joinTiers(tiers)
}

// Filter admits a node unless its GPU tier is in the job's DISALLOWED set, in
// which case the node is rejected (hard exclude). A job with no disallowed set
// is admitted on every (non-nil) node by this plugin. This is the load-bearing
// admission branch: removing the disallowed-set check would wrongly admit
// barred-tier nodes.
func (g *GPUTierFilter) Filter(job *Job, node *Node) bool {
	if job == nil || node == nil {
		return false
	}
	disallowed := parseTierSet(job.Labels, LabelJobDisallowedTiers)
	if len(disallowed) == 0 {
		return true
	}
	if disallowed[nodeGPUTier(node)] {
		return false // hard exclude: node's GPU tier is barred for this job.
	}
	return true
}

// Score returns a higher value for admitted nodes whose GPU tier the job
// prefers. Nodes that fail Filter (disallowed tier) score 0. Among admitted
// nodes, a preferred-tier node scores gpuTierScoreBase+bonus while a
// non-preferred admitted node scores gpuTierScoreBase, so preferred nodes are
// ordered strictly first. The score is deterministic (no map-iteration
// ordering leaks into the result).
func (g *GPUTierFilter) Score(job *Job, node *Node) int {
	if !g.Filter(job, node) {
		return 0
	}
	score := gpuTierScoreBase
	preferred := parseTierSet(job.Labels, LabelJobPreferredTiers)
	if len(preferred) > 0 && preferred[nodeGPUTier(node)] {
		score += gpuTierPreferenceBonus
	}
	return score
}

// Bind is a no-op success for this matching/scoring plugin; it does not consume
// resources and must not break the bind pipeline.
func (g *GPUTierFilter) Bind(job *Job, node *Node) bool { return true }

// ComposeFilter returns true only when EVERY supplied predicate admits the
// (job, node) pair. It makes the AND-composition of independent filter
// predicates explicit and testable: a node passes only when it satisfies them
// all (e.g. GPU-count adequacy AND allowed GPU tier). A nil predicate is
// treated as a rejection so a mis-wired chain fails closed rather than silently
// admitting everything. With no predicates it admits (vacuous truth).
func ComposeFilter(job *Job, node *Node, predicates ...func(*Job, *Node) bool) bool {
	for _, p := range predicates {
		if p == nil {
			return false
		}
		if !p(job, node) {
			return false
		}
	}
	return true
}

// nodeGPUTier parses the node's GPU tier label into a device.Tier; absent or
// unparseable => device.TierUnknown.
func nodeGPUTier(node *Node) device.Tier {
	if node.Labels == nil {
		return device.TierUnknown
	}
	return parseTierToken(node.Labels[LabelNodeGPUTier])
}

// parseTierSet reads a comma-separated set of tier tokens from a label map into
// a set keyed by device.Tier. Absent/empty => an empty (nil) set. Unparseable
// tokens are skipped (they cannot match a real node tier). The token
// "T0"/"unknown" maps to device.TierUnknown.
func parseTierSet(labels map[string]string, key string) map[device.Tier]bool {
	if labels == nil {
		return nil
	}
	raw := strings.TrimSpace(labels[key])
	if raw == "" {
		return nil
	}
	set := make(map[device.Tier]bool)
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		set[parseTierToken(tok)] = true
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// parseTierToken parses a single tier token (e.g. "T7", "t7", "7", "0") into a
// device.Tier. An optional leading "T"/"t" is tolerated. Out-of-range or
// unparseable tokens map to device.TierUnknown. "0" maps to TierUnknown.
func parseTierToken(tok string) device.Tier {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return device.TierUnknown
	}
	tok = strings.TrimPrefix(tok, "T")
	tok = strings.TrimPrefix(tok, "t")
	n, err := strconv.Atoi(strings.TrimSpace(tok))
	if err != nil || n < int(device.TierUnknown) || n > int(device.T15) {
		return device.TierUnknown
	}
	return device.Tier(n)
}

// joinTiers renders a slice of device.Tier as a canonical comma-separated label
// (e.g. "T5,T7"), the inverse of parseTierSet.
func joinTiers(tiers []device.Tier) string {
	if len(tiers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tiers))
	for _, t := range tiers {
		parts = append(parts, t.String())
	}
	return strings.Join(parts, ",")
}
