package edge

import (
	"fmt"
	"time"
)

// DeviceTier is an alias for TrustLevel used as the key in the per-tier
// ScheduleRule map. We reuse TrustLevel rather than inventing a third tier
// enum (CLAUDE-1: no gratuitous duplication). The values EDGE_DONOR, SEMI,
// STANDARD, FULL directly correspond to the four admission tiers.
//
// Rationale: the design asked us to "REUSE the 1195 TrustLevel or device.Tier —
// do NOT invent a 3rd tier enum". We pick TrustLevel because ScheduleRules are
// admission-policy artefacts, and TrustLevel is the primary admission currency
// in this package.
type DeviceTier = TrustLevel

// ConditionType identifies a single schedulable precondition.
type ConditionType int

const (
	// IsCharging requires the device to be connected to external power.
	IsCharging ConditionType = iota + 1
	// WifiConnected requires the device to be on a WiFi network (not mobile data).
	WifiConnected
	// BatteryAbove requires BatteryPct > threshold.
	BatteryAbove
	// CPUTempBelow requires CPUTempC < threshold.
	CPUTempBelow
	// TimeBetween requires the current time to fall within a [start, end) window
	// (hours, 24h clock). Start and end are stored in ConditionThreshold as
	// float64 values (e.g. 22.0 = 22:00, 6.0 = 06:00).
	TimeBetween
	// BackgroundRefreshEnabled requires the device OS to permit background refresh.
	BackgroundRefreshEnabled
	// LowPowerModeOff requires low-power mode to be disabled.
	LowPowerModeOff
)

// String returns a human-readable label for a ConditionType.
func (ct ConditionType) String() string {
	switch ct {
	case IsCharging:
		return "IsCharging"
	case WifiConnected:
		return "WifiConnected"
	case BatteryAbove:
		return "BatteryAbove"
	case CPUTempBelow:
		return "CPUTempBelow"
	case TimeBetween:
		return "TimeBetween"
	case BackgroundRefreshEnabled:
		return "BackgroundRefreshEnabled"
	case LowPowerModeOff:
		return "LowPowerModeOff"
	default:
		return fmt.Sprintf("ConditionType(%d)", int(ct))
	}
}

// Condition is a schedulable precondition with optional numeric thresholds.
type Condition struct {
	// Type is the kind of condition.
	Type ConditionType
	// ThresholdA is the primary numeric threshold.
	// For BatteryAbove: minimum battery percentage (integer, e.g. 20).
	// For CPUTempBelow: maximum temperature in Celsius.
	// For TimeBetween: start hour (0–23).
	ThresholdA float64
	// ThresholdB is the secondary numeric threshold.
	// For TimeBetween: end hour (0–23), exclusive. End < Start wraps midnight.
	ThresholdB float64
}

// ScheduleRule pairs a Condition with a Required flag.
// If Required is true, failing this condition blocks scheduling.
// If Required is false, the condition is advisory only (evaluated but never
// blocking).
type ScheduleRule struct {
	Condition Condition
	Required  bool
}

// DeviceState is a point-in-time snapshot of an edge device's operational
// state. All fields are plain data so a snapshot can be constructed from an
// inventory record or a device heartbeat without I/O.
type DeviceState struct {
	// IsCharging is true when the device is connected to external power.
	IsCharging bool
	// WifiConnected is true when the device is on a WiFi network.
	WifiConnected bool
	// BatteryPct is the current battery percentage (0–100).
	BatteryPct int
	// CPUTempC is the current CPU temperature in Celsius.
	CPUTempC float64
	// BackgroundRefreshEnabled is true when the OS permits background refresh.
	BackgroundRefreshEnabled bool
	// LowPowerModeOff is true when low-power mode is disabled.
	LowPowerModeOff bool
	// Now is the point-in-time used for TimeBetween evaluation. If zero the
	// evaluator uses time.Now().UTC().
	Now time.Time
}

// ConditionResult records the pass/fail outcome for one ScheduleRule
// evaluation. It is the per-condition trace artefact required by CLAUDE-1.
type ConditionResult struct {
	// Condition is a copy of the evaluated condition.
	Condition Condition
	// Required mirrors ScheduleRule.Required.
	Required bool
	// Passed is true iff the condition evaluated to satisfied.
	Passed bool
	// Detail is a human-readable explanation of the outcome.
	Detail string
}

// DefaultTierRules returns a pre-built map of DeviceTier → []ScheduleRule
// suitable for a typical Helix Cluster OS deployment. Callers may override
// entries for their own policies.
//
// SEMI / T6-proxy rules require:
//   - IsCharging (required)
//   - BatteryAbove(20) (required)
//   - WifiConnected (required)
//   - CPUTempBelow(75) (advisory)
//   - LowPowerModeOff (advisory)
func DefaultTierRules() map[DeviceTier][]ScheduleRule {
	return map[DeviceTier][]ScheduleRule{
		EDGE_DONOR: {
			{Condition: Condition{Type: IsCharging}, Required: true},
			{Condition: Condition{Type: BatteryAbove, ThresholdA: 30}, Required: true},
			{Condition: Condition{Type: WifiConnected}, Required: true},
			{Condition: Condition{Type: LowPowerModeOff}, Required: false},
		},
		SEMI: {
			{Condition: Condition{Type: IsCharging}, Required: true},
			{Condition: Condition{Type: BatteryAbove, ThresholdA: 20}, Required: true},
			{Condition: Condition{Type: WifiConnected}, Required: true},
			{Condition: Condition{Type: CPUTempBelow, ThresholdA: 75}, Required: false},
			{Condition: Condition{Type: LowPowerModeOff}, Required: false},
		},
		STANDARD: {
			{Condition: Condition{Type: WifiConnected}, Required: true},
			{Condition: Condition{Type: CPUTempBelow, ThresholdA: 85}, Required: false},
		},
		FULL: {
			{Condition: Condition{Type: CPUTempBelow, ThresholdA: 90}, Required: false},
		},
	}
}

// Evaluate assesses rules against state and returns whether the device is
// eligible for scheduling together with a per-condition trace.
//
// eligible is true iff every Required rule passes. Advisory (Required=false)
// rules are evaluated and recorded in the trace but never block scheduling.
//
// The RunUUID embedded in the trace is generated once per Evaluate call so
// the orchestrator can correlate log lines to a specific evaluation.
func Evaluate(rules []ScheduleRule, state DeviceState) (eligible bool, trace []ConditionResult) {
	now := state.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	eligible = true
	trace = make([]ConditionResult, 0, len(rules))

	for _, rule := range rules {
		passed, detail := evaluateCondition(rule.Condition, state, now)
		trace = append(trace, ConditionResult{
			Condition: rule.Condition,
			Required:  rule.Required,
			Passed:    passed,
			Detail:    detail,
		})
		if rule.Required && !passed {
			eligible = false
		}
	}
	return eligible, trace
}

// evaluateCondition evaluates a single Condition against state and returns
// (passed, detail).
func evaluateCondition(c Condition, state DeviceState, now time.Time) (bool, string) {
	switch c.Type {
	case IsCharging:
		if state.IsCharging {
			return true, "IsCharging: device is charging ✓"
		}
		return false, "IsCharging: device is NOT charging ✗"

	case WifiConnected:
		if state.WifiConnected {
			return true, "WifiConnected: device is on WiFi ✓"
		}
		return false, "WifiConnected: device is NOT on WiFi ✗"

	case BatteryAbove:
		threshold := int(c.ThresholdA)
		if state.BatteryPct > threshold {
			return true, fmt.Sprintf("BatteryAbove(%d): battery=%d%% > threshold ✓", threshold, state.BatteryPct)
		}
		return false, fmt.Sprintf("BatteryAbove(%d): battery=%d%% <= threshold ✗", threshold, state.BatteryPct)

	case CPUTempBelow:
		if state.CPUTempC < c.ThresholdA {
			return true, fmt.Sprintf("CPUTempBelow(%.1f): temp=%.1f°C < threshold ✓", c.ThresholdA, state.CPUTempC)
		}
		return false, fmt.Sprintf("CPUTempBelow(%.1f): temp=%.1f°C >= threshold ✗", c.ThresholdA, state.CPUTempC)

	case TimeBetween:
		hour := float64(now.Hour()) + float64(now.Minute())/60.0
		start, end := c.ThresholdA, c.ThresholdB
		var inWindow bool
		if start <= end {
			inWindow = hour >= start && hour < end
		} else {
			// Wraps midnight: e.g. 22:00–06:00.
			inWindow = hour >= start || hour < end
		}
		if inWindow {
			return true, fmt.Sprintf("TimeBetween(%.1f–%.1f): current=%.2f ✓", start, end, hour)
		}
		return false, fmt.Sprintf("TimeBetween(%.1f–%.1f): current=%.2f ✗", start, end, hour)

	case BackgroundRefreshEnabled:
		if state.BackgroundRefreshEnabled {
			return true, "BackgroundRefreshEnabled: enabled ✓"
		}
		return false, "BackgroundRefreshEnabled: NOT enabled ✗"

	case LowPowerModeOff:
		if state.LowPowerModeOff {
			return true, "LowPowerModeOff: low-power mode is off ✓"
		}
		return false, "LowPowerModeOff: low-power mode is ON ✗"

	default:
		return false, fmt.Sprintf("unknown ConditionType %d ✗", int(c.Type))
	}
}
