package scheduler

import (
	"strconv"
	"strings"
)

// LabelNodeTempC is the reserved Node.Labels key carrying the node's current
// temperature in degrees Celsius as a float64 string (e.g. "72.5"). It is
// populated by a node-agent from a real sensor (IPMI, ACPI, sysfs thermals)
// and read by ThermalAware as the plugin's honest-hardware seam.
// Absent or unparseable: the plugin treats the node as cool (assume-safe).
const LabelNodeTempC = "temp_c"

// ThermalAware is a thermal-aware scheduling Plugin that filters out and
// lowers the score of nodes whose reported temperature exceeds a configurable
// threshold.
//
// FILTER: a node is rejected (Filter returns false) when its temp_c label is
// present, parseable as float64, and strictly greater than ThresholdC. All
// other cases (absent label, unparseable value, temp at or below the threshold)
// are admitted — the plugin follows the assume-safe principle so a temporary
// telemetry gap never brings a healthy node offline.
//
// SCORE: among admitted nodes, cooler nodes score strictly higher than warmer
// ones. The score decreases linearly as temperature rises toward the threshold,
// providing soft preference for cooler nodes without requiring a secondary
// tiebreaker. Filtered-out nodes score 0 (consistent with all other plugins).
//
// HONEST HARDWARE SEAM (CLAUDE-2): this plugin does NOT probe sensors. The
// temperature is taken as INPUT via the Node.Labels[LabelNodeTempC] key. A
// real deployment populates that label from a node-agent reading
// /sys/class/thermal (Linux) or IOKit (macOS) etc. That per-OS probe is out
// of scope here; the plugin's contract is with the label.
type ThermalAware struct {
	// ThresholdC is the temperature threshold in degrees Celsius.
	// A node whose temp_c label is strictly GREATER than this value is
	// filtered out. The comparison is '>' so a node AT exactly the threshold
	// is still admitted. Set to a sensible default (e.g. 80.0) at creation.
	ThresholdC float64
}

// thermalScoreBase is the baseline score when a node is at 0°C (or no temp).
// It mirrors the tier_power.go convention of a large positive baseline so
// score differences are always positive for realistic temperatures.
const thermalScoreBase = 100_000

// thermalScorePerDegree is subtracted from the score for every degree of
// temperature. A cooler node therefore scores strictly higher than a warmer one.
const thermalScorePerDegree = 1000

func (t *ThermalAware) Name() string { return "ThermalAware" }

// Filter returns false when the node's reported temperature is strictly greater
// than ThresholdC, filtering out overheating nodes. It returns true (admit) in
// all other cases: absent/unparseable temp label, nil job, or temp <= threshold.
// A nil node is always rejected.
func (t *ThermalAware) Filter(job *Job, node *Node) bool {
	if node == nil {
		return false
	}
	temp, ok := nodeTemp(node)
	if !ok {
		// No readable temperature: assume safe (cannot confirm overheating).
		return true
	}
	// CRITICAL guard: strictly greater-than so the exact threshold is admitted.
	// Mutation: inverting '>' to '<=' breaks this (admits overheating, rejects cool).
	return !(temp > t.ThresholdC)
}

// Score returns a higher value for cooler nodes. Overheating nodes (those that
// fail Filter) receive a score of 0. Nodes without a temp_c label are scored
// at thermalScoreBase (maximally preferred among thermal-unknown nodes, since
// the plugin cannot penalise what it cannot measure).
func (t *ThermalAware) Score(job *Job, node *Node) int {
	if !t.Filter(job, node) {
		return 0
	}
	temp, ok := nodeTemp(node)
	if !ok {
		// No temperature data: score at base (neutral, cannot prefer/penalise).
		return thermalScoreBase
	}
	score := thermalScoreBase - int(temp*thermalScorePerDegree)
	if score < 0 {
		score = 0
	}
	return score
}

// Bind is a no-op success for this scoring plugin; it does not consume resources
// and must not break the bind pipeline.
func (t *ThermalAware) Bind(job *Job, node *Node) bool { return true }

// nodeTemp reads the node's temp_c label and returns (celsius, true) when
// present and parseable as float64, (0, false) otherwise.
func nodeTemp(node *Node) (float64, bool) {
	if node.Labels == nil {
		return 0, false
	}
	raw, ok := node.Labels[LabelNodeTempC]
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
