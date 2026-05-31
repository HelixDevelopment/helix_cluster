package scheduler

import (
	"strconv"
	"strings"
)

// TierPowerAware is a tier- and power-aware scheduling Plugin (Phase 5
// "heterogeneous fabric" concept). It combines two real-world placement
// concerns:
//
//   - TIER/ARCH ADMISSION (Filter): nodes are organised into capability tiers
//     T0..Tn (e.g. baseline CPU box -> top-end multi-GPU accelerator). A job can
//     demand a minimum tier and/or a specific micro-architecture. A node whose
//     tier is below the job's minimum, or whose arch does not match the job's
//     required arch, cannot run the job and is filtered OUT.
//
//   - POWER-EFFICIENCY RANKING (Score): among nodes that admit the job, prefer
//     the one that satisfies it at the lowest power cost / best performance per
//     watt. For tier-demanding jobs it can prefer a higher tier, but it applies
//     an ANTI-FRAGMENTATION penalty so a tiny job is not wastefully placed on a
//     top-tier node that should be reserved for jobs that actually need it.
//
// It is additive and backward-compatible: it implements the existing Plugin
// interface (Name/Filter/Score/Bind) and stores all of its inputs in the
// existing Job.Labels / Node.Labels maps under reserved, namespaced keys
// (mirroring ClassAdMatch and CostAwareGPUPlacement). No field on Job or Node
// changes shape. Like the other scoring plugins it does not consume resources
// in Bind (NodeResourcesFit owns resource accounting).
//
// HONEST HARDWARE SEAM: this plugin does NOT probe real hardware. Device
// capabilities (tier, arch, watts, perf-per-watt) are taken as INPUT via labels
// and classified/scored deterministically. A real deployment would populate
// those labels from a node-agent that reads /sys, NVML, etc.; that probe is out
// of scope here and is represented purely by the label contract below.
type TierPowerAware struct{}

// Reserved Node.Labels keys describing a node's capability tier, arch and power.
const (
	// LabelNodeTier is the node's capability tier as an integer ("0".."n"),
	// optionally with a leading "T"/"t" (e.g. "T6"). Higher = more capable.
	// Absent/unparseable => tier 0 (the baseline tier).
	LabelNodeTier = "tier"
	// LabelNodeArch is the node's micro-architecture identifier, matched
	// case-insensitively against a job's required arch (e.g. "x86_64",
	// "arm64", "cuda_hopper"). Absent => the node matches any arch requirement
	// only if the job does not require a specific arch.
	LabelNodeArch = "arch"
	// LabelNodeWatts is the node's power draw in watts while running the job,
	// used directly as the power cost when present. Absent/unparseable =>
	// the plugin derives a power proxy from the node's resources.
	LabelNodeWatts = "watts"
	// LabelNodePerfPerWatt is an explicit performance-per-watt figure (higher =
	// more efficient). When present it dominates the power score: a node with a
	// higher perf-per-watt is preferred even at equal raw watts.
	LabelNodePerfPerWatt = "perf_per_watt"
)

// Reserved Job.Labels keys describing a job's tier/arch demands.
const (
	// LabelJobMinTier is the minimum node tier the job requires (integer,
	// optional leading "T"). A node below this tier is filtered OUT.
	// Absent/unparseable => 0 (no minimum tier; any tier admits the job).
	LabelJobMinTier = "min_tier"
	// LabelJobRequireArch is the arch the job requires. A node whose arch does
	// not match (case-insensitive) is filtered OUT. Absent/empty => the job has
	// no arch constraint and any arch admits it.
	LabelJobRequireArch = "require_arch"
)

// tierPowerScoreBase is the baseline from which power cost and the
// anti-fragmentation penalty are subtracted, so an efficient, well-tier-matched
// placement yields a strictly higher (and non-negative for realistic inputs)
// score. It mirrors costScoreBase's role in CostAwareGPUPlacement.
const tierPowerScoreBase = 1_000_000

// wattScale converts a fractional watt figure into integer score resolution.
const wattScale = 100

// perfPerWattScale rewards efficiency: a node's explicit perf-per-watt is
// multiplied by this and added to the score, so a more efficient node ranks
// strictly higher at equal raw watts.
const perfPerWattScale = 1000

// tierFragmentationPenalty is charged per tier-level of "overshoot" (node tier
// above the job's minimum tier). It discourages wasting a high tier on a job
// that does not need it (anti-fragmentation), without ever filtering such a
// node out — overshoot remains feasible, just less preferred.
const tierFragmentationPenalty = 5000

func (t *TierPowerAware) Name() string { return "TierPowerAware" }

// SetNodeTier records a node's capability tier (reserved Node.Labels key),
// allocating the map if needed. Backward-compatible: nodes that never call this
// default to tier 0.
func SetNodeTier(node *Node, tier int) {
	ensureNodeLabels(node)
	node.Labels[LabelNodeTier] = strconv.Itoa(tier)
}

// SetNodeArch records a node's micro-architecture (reserved Node.Labels key).
func SetNodeArch(node *Node, arch string) {
	ensureNodeLabels(node)
	node.Labels[LabelNodeArch] = arch
}

// SetNodePower records a node's power draw in watts (reserved Node.Labels key).
func SetNodePower(node *Node, watts float64) {
	ensureNodeLabels(node)
	node.Labels[LabelNodeWatts] = strconv.FormatFloat(watts, 'f', -1, 64)
}

// SetJobMinTier attaches a minimum-tier requirement to a job (reserved
// Job.Labels key), allocating the map if needed.
func SetJobMinTier(job *Job, tier int) {
	ensureJobLabels(job)
	job.Labels[LabelJobMinTier] = strconv.Itoa(tier)
}

// SetJobRequireArch attaches a required arch to a job (reserved Job.Labels key).
func SetJobRequireArch(job *Job, arch string) {
	ensureJobLabels(job)
	job.Labels[LabelJobRequireArch] = arch
}

// Filter admits a node only when it satisfies BOTH the job's minimum-tier and
// its required-arch constraints. A node below the required tier, or with a
// mismatched arch, is rejected deterministically. A job with no tier/arch
// constraint is admitted on every (non-nil) node by this plugin.
func (t *TierPowerAware) Filter(job *Job, node *Node) bool {
	if job == nil || node == nil {
		return false
	}
	// Tier admission: node tier must be >= the job's required minimum tier.
	if nodeTier(node) < jobMinTier(job) {
		return false
	}
	// Arch admission: if the job requires a specific arch, the node must match.
	if want := jobRequireArch(job); want != "" {
		if !strings.EqualFold(nodeArch(node), want) {
			return false
		}
	}
	return true
}

// Score returns a higher value for nodes that admit the job at a lower power
// cost / better perf-per-watt, with an anti-fragmentation penalty for using a
// tier far above what the job needs. Nodes that fail Filter score 0. The score
// is deterministic and contains no randomness or map-iteration ordering.
func (t *TierPowerAware) Score(job *Job, node *Node) int {
	if !t.Filter(job, node) {
		return 0
	}

	score := tierPowerScoreBase

	// Power cost: cheaper (fewer watts) ranks higher.
	score -= int(nodePowerWatts(node) * wattScale)

	// Efficiency bonus: an explicit perf-per-watt figure lifts the score, so a
	// more efficient node wins at equal raw watts.
	if ppw, ok := parsePositiveLabel(node, LabelNodePerfPerWatt); ok {
		score += int(ppw * perfPerWattScale)
	}

	// Anti-fragmentation: penalise tier overshoot so a top-tier node is not
	// wasted on a low-tier job. Overshoot is feasible (not filtered), just less
	// preferred than an exact/near tier match.
	overshoot := nodeTier(node) - jobMinTier(job)
	if overshoot > 0 {
		score -= overshoot * tierFragmentationPenalty
	}

	if score < 0 {
		score = 0
	}
	return score
}

// Bind is a no-op success for this matching/scoring plugin; it does not consume
// resources (NodeResourcesFit owns resource accounting) and must not break the
// bind pipeline.
func (t *TierPowerAware) Bind(job *Job, node *Node) bool { return true }

// nodePowerWatts returns the node's power cost used for ranking. An explicit
// watts label is used directly; otherwise a deterministic proxy is derived from
// the node's resources (richer nodes draw more power), so power ranking still
// works when no probe-supplied watts label is present.
func nodePowerWatts(node *Node) float64 {
	if w, ok := parsePositiveLabel(node, LabelNodeWatts); ok {
		return w
	}
	// Derived proxy: a coarse model of draw rising with provisioned capacity.
	// Weights mirror the relative power-hunger used elsewhere (GPU >> CPU >>
	// Memory). Kept small so a real watts label always dominates.
	return node.AvailableResources.CPU*5.0 +
		float64(node.AvailableResources.Memory)/1024.0 +
		float64(node.AvailableResources.GPU)*250.0
}

// nodeTier parses the node's capability tier; absent/unparseable => 0.
func nodeTier(node *Node) int { return parseTierLabel(node.Labels, LabelNodeTier) }

// jobMinTier parses the job's minimum tier requirement; absent/unparseable => 0.
func jobMinTier(job *Job) int { return parseTierLabel(job.Labels, LabelJobMinTier) }

// nodeArch returns the node's arch label (trimmed), or "" if absent.
func nodeArch(node *Node) string { return labelValue(node.Labels, LabelNodeArch) }

// jobRequireArch returns the job's required arch (trimmed), or "" if absent.
func jobRequireArch(job *Job) string { return labelValue(job.Labels, LabelJobRequireArch) }

// parseTierLabel reads a tier integer from a label map, tolerating an optional
// leading "T"/"t". Absent, empty, or unparseable values => 0 (baseline tier).
func parseTierLabel(labels map[string]string, key string) int {
	if labels == nil {
		return 0
	}
	raw := strings.TrimSpace(labels[key])
	if raw == "" {
		return 0
	}
	raw = strings.TrimPrefix(raw, "T")
	raw = strings.TrimPrefix(raw, "t")
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// labelValue returns a trimmed label value, or "" when the map/key is absent.
func labelValue(labels map[string]string, key string) string {
	if labels == nil {
		return ""
	}
	return strings.TrimSpace(labels[key])
}

// parsePositiveLabel reads a strictly-positive float label from a node, returning
// (0, false) when the label is absent, unparseable, zero, or negative.
func parsePositiveLabel(node *Node, key string) (float64, bool) {
	if node.Labels == nil {
		return 0, false
	}
	raw, ok := node.Labels[key]
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

func ensureNodeLabels(node *Node) {
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
}

func ensureJobLabels(job *Job) {
	if job.Labels == nil {
		job.Labels = make(map[string]string)
	}
}
