package scheduler

// EdgeAware is a scheduler Plugin that enforces edge-device admission rules
// (HXC-1189) and applies per-tier scoring bonuses with thermal headroom
// (HXC-1190).
//
// Label contract (Node.Labels):
//   - "tier"         — capability tier integer, parsed by parseTierLabel (reused from tier_power.go)
//   - "temp_c"       — current temperature in Celsius, parsed by nodeTemp (reused from thermal.go)
//   - "charging"     — "true" / "false" (battery charge state)
//   - "battery_pct"  — integer 0-100 (battery percentage)
//   - "device_state" — "available" | "sleeping" | "offline" | "unavailable"
//
// Label contract (Job.Labels):
//   - "workload_type" — "work_unit" | "session" | or an edge.WorkloadType name
//     (case-insensitive; "work_unit" and "work unit" both accepted)
//
// Filter semantics (HXC-1189):
//   - Tiers < 6: unconditionally admitted (Filter returns true).
//   - Tiers >= 6:
//     (a) Non-work-unit (session) workloads → rejected.
//     (b) device_state in {unavailable, sleeping, offline} → rejected.
//     (c) nodeTemp > ThermalThresholdC → rejected.
//     (d) T6 or T8 only: battery_pct < BatteryMinPercent OR not charging → rejected.
//
// Score semantics (HXC-1190):
//   Per-tier bonuses:
//     T3 (NPU/NVMe): +20
//     T4 (batch SBC): +10
//     T5 (headless accelerated): +15
//     T6 (small edge + battery_pct/5 bonus): +10 + battery_pct/5
//     T7 (server inference): +40 − 20 (intermittency penalty) = net +20
//     T8 (NPU server): +35
//   Thermal headroom:
//     temp < 50°C: +15
//     temp > 70°C: −30
//
// Exported side-channels (CLAUDE-1 sink-side evidence):
//   Evaluate(job, node) EdgeDecision  — documents Filter decision with Reasons + RunUUID
//   ScoreBreakdown(job, node) (int, map[string]int, string)  — documents per-part Score + RunUUID
import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// EdgeAware thresholds — exported so tests can assert against the exact values.
const (
	// ThermalThresholdC is the temperature above which edge tier>=6 nodes are rejected.
	ThermalThresholdC = 75.0
	// BatteryMinPercent is the minimum battery percentage for T6/T8 nodes to pass Filter.
	BatteryMinPercent = 20
)

// Label keys introduced by EdgeAware — reserved on Node.Labels.
const (
	LabelCharging     = "charging"
	LabelBatteryPct   = "battery_pct"
	LabelDeviceState  = "device_state"
	LabelWorkloadType = "workload_type"
)

// Device-state values that cause rejection on tier>=6.
const (
	DeviceStateAvailable   = "available"
	DeviceStateUnavailable = "unavailable"
	DeviceStateSleeping    = "sleeping"
	DeviceStateOffline     = "offline"
)

// EdgeDecision is the exported side-channel decision record returned by
// Evaluate. Filter calls Evaluate internally; the last decision is accessible
// via the returned struct.
type EdgeDecision struct {
	// Schedulable mirrors the Filter return value.
	Schedulable bool
	// Reasons contains human-readable rejection reasons. Empty when Schedulable==true.
	Reasons []string
	// RunUUID is a per-call universally-unique identifier for correlation.
	RunUUID string
}

// EdgeAware implements Plugin and exposes the side-channel methods Evaluate and
// ScoreBreakdown.
type EdgeAware struct {
	// ThermalThreshold overrides ThermalThresholdC when non-zero (default 0 means
	// use ThermalThresholdC = 75.0).
	ThermalThreshold float64
	// BatteryMin overrides BatteryMinPercent when non-zero (default 0 means
	// use BatteryMinPercent = 20).
	BatteryMin int
}

// effectiveThermalThreshold returns the configured or default threshold.
func (e *EdgeAware) effectiveThermalThreshold() float64 {
	if e.ThermalThreshold > 0 {
		return e.ThermalThreshold
	}
	return ThermalThresholdC
}

// effectiveBatteryMin returns the configured or default battery minimum.
func (e *EdgeAware) effectiveBatteryMin() int {
	if e.BatteryMin > 0 {
		return e.BatteryMin
	}
	return BatteryMinPercent
}

func (e *EdgeAware) Name() string { return "EdgeAware" }

// Filter returns true iff the node is acceptable for the job.
// It delegates to Evaluate internally.
func (e *EdgeAware) Filter(job *Job, node *Node) bool {
	dec := e.Evaluate(job, node)
	return dec.Schedulable
}

// Score returns the EdgeAware tier+thermal bonus for the node.
// It delegates to ScoreBreakdown internally.
func (e *EdgeAware) Score(job *Job, node *Node) int {
	total, _, _ := e.ScoreBreakdown(job, node)
	return total
}

// Bind is a no-op success; EdgeAware does not consume resources.
func (e *EdgeAware) Bind(job *Job, node *Node) bool { return true }

// Evaluate is the exported side-channel for HXC-1189.
// It computes the Filter decision and returns the full EdgeDecision trace including RunUUID.
func (e *EdgeAware) Evaluate(job *Job, node *Node) EdgeDecision {
	runUUID := edgeRunUUID()
	if node == nil {
		return EdgeDecision{
			Schedulable: false,
			Reasons:     []string{"nil node"},
			RunUUID:     runUUID,
		}
	}

	tier := nodeTier(node)

	// Tiers < 6: unconditionally admitted.
	// CRITICAL GUARD: must be >=6, not >6 (mutation target).
	if tier < 6 {
		return EdgeDecision{Schedulable: true, RunUUID: runUUID}
	}

	// Tier >= 6 checks follow.
	var reasons []string

	// (a) Non-work-unit (session) workloads are rejected on edge devices.
	if !isWorkUnit(job) {
		reasons = append(reasons, "Edge devices only accept work units")
	}

	// (b) device_state in {unavailable, sleeping, offline}.
	if ds := deviceState(node); ds == DeviceStateUnavailable || ds == DeviceStateSleeping || ds == DeviceStateOffline {
		reasons = append(reasons, fmt.Sprintf("device_state=%q is not available", ds))
	}

	// (c) Temperature check.
	if temp, ok := nodeTemp(node); ok && temp > e.effectiveThermalThreshold() {
		reasons = append(reasons, fmt.Sprintf("node temperature %.1f°C exceeds threshold %.1f°C", temp, e.effectiveThermalThreshold()))
	}

	// (d) T6 and T8 only: battery check.
	// CRITICAL GUARD: check is specifically for T6 and T8 (mutation target).
	if tier == 6 || tier == 8 {
		pct := batteryPct(node)
		charging := isCharging(node)
		if pct < e.effectiveBatteryMin() || !charging {
			reasons = append(reasons, "Not charging")
		}
	}

	if len(reasons) > 0 {
		return EdgeDecision{Schedulable: false, Reasons: reasons, RunUUID: runUUID}
	}
	return EdgeDecision{Schedulable: true, RunUUID: runUUID}
}

// ScoreBreakdown is the exported side-channel for HXC-1190.
// It returns (total score, per-part breakdown map, runUUID).
// Parts keys: "tier_bonus", "battery_bonus" (T6 only), "thermal_hot", "thermal_cool".
func (e *EdgeAware) ScoreBreakdown(job *Job, node *Node) (int, map[string]int, string) {
	runUUID := edgeRunUUID()
	parts := make(map[string]int)

	if node == nil {
		return 0, parts, runUUID
	}

	tier := nodeTier(node)

	// Per-tier bonus.
	tierBonus := edgeTierBonus(tier)
	if tierBonus != 0 {
		parts["tier_bonus"] = tierBonus
	}

	// T6 battery percentage bonus: battery_pct / 5.
	battBonus := 0
	if tier == 6 {
		battBonus = batteryPct(node) / 5
		if battBonus != 0 {
			parts["battery_bonus"] = battBonus
		}
	}

	// Thermal headroom.
	thermalBonus := 0
	if temp, ok := nodeTemp(node); ok {
		if temp < 50.0 {
			thermalBonus = +15
			parts["thermal_cool"] = thermalBonus
		} else if temp > 70.0 {
			thermalBonus = -30
			parts["thermal_hot"] = thermalBonus
		}
	}

	total := tierBonus + battBonus + thermalBonus
	return total, parts, runUUID
}

// edgeTierBonus returns the base bonus for the given tier (without battery or thermal).
// T3: +20 (NPU/NVMe); T4: +10 (batch SBC); T5: +15 (headless);
// T6: +10 base (battery_pct/5 added separately); T7: +40 - 20 (intermittency) = +20;
// T8: +35 (NPU server).
func edgeTierBonus(tier int) int {
	switch tier {
	case 3:
		return 20
	case 4:
		return 10
	case 5:
		return 15
	case 6:
		return 10
	case 7:
		return 20 // +40 inference - 20 intermittency
	case 8:
		return 35
	default:
		return 0
	}
}

// isWorkUnit returns true iff the job is labelled as a work unit (non-session).
// A job is considered a work unit unless its workload_type is a known session type.
// Recognised session types: "session", "interactive_session", "InteractiveSession".
// All other values (including absent) are treated as work units.
func isWorkUnit(job *Job) bool {
	if job == nil || job.Labels == nil {
		return true // absent label → treat as work unit (safe default)
	}
	wt := strings.ToLower(strings.TrimSpace(job.Labels[LabelWorkloadType]))
	switch wt {
	case "session", "interactive_session", "interactivesession":
		return false
	}
	return true
}

// deviceState returns the node's device_state label value (lowercased), or
// DeviceStateAvailable if absent.
func deviceState(node *Node) string {
	if node.Labels == nil {
		return DeviceStateAvailable
	}
	v := strings.ToLower(strings.TrimSpace(node.Labels[LabelDeviceState]))
	if v == "" {
		return DeviceStateAvailable
	}
	return v
}

// batteryPct returns the node's battery_pct label as int (0 if absent/unparseable).
func batteryPct(node *Node) int {
	if node.Labels == nil {
		return 0
	}
	raw, ok := node.Labels[LabelBatteryPct]
	if !ok {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// isCharging returns true iff the node's "charging" label is "true".
func isCharging(node *Node) bool {
	if node.Labels == nil {
		return false
	}
	return strings.ToLower(strings.TrimSpace(node.Labels[LabelCharging])) == "true"
}

// edgeRunUUID returns a per-call unique string suitable for log correlation.
// Uses time + goroutine count — same lightweight scheme as workunit.go.
func edgeRunUUID() string {
	return fmt.Sprintf("edge-%016x-%04x", time.Now().UnixNano(), runtime.NumGoroutine())
}
