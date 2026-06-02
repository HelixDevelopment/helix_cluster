package scheduler

// HandheldPolicy is a power-aware scheduling Plugin (HXC-1298) for
// handheld/battery-class workloads.
//
// Label contract (Node.Labels) — all reused from edgeaware.go / thermal.go:
//
//   - LabelBatteryPct  ("battery_pct")  — integer 0-100; absent => assume-safe (not rejected).
//   - LabelCharging    ("charging")     — "true"/"false"; absent => treated as not-charging.
//   - LabelNodeTempC   ("temp_c")       — float64 degrees Celsius; absent => assume-safe.
//   - LabelDeviceState ("device_state") — "available"|"sleeping"|"offline"|"unavailable";
//     absent => DeviceStateAvailable (assume-safe).
//
// Filter semantics (all thresholds are strict inequalities):
//
//  1. battery_pct < BatteryMin      => reject  (critically low battery).
//  2. temp_c > ThermalThreshold     => reject  (thermal throttle risk).
//  3. device_state != available     => reject  (node not usable).
//  4. battery_pct < ChargeFloor AND
//     NOT charging                  => reject  (discharging below floor).
//     ChargeFloor and BatteryMin are INDEPENDENT thresholds: ChargeFloor may be
//     set higher than BatteryMin to impose a stricter floor on discharging nodes.
//
// Absent labels follow the assume-safe principle: a node with NO battery_pct
// label is NOT rejected on battery grounds. This prevents a temporary telemetry
// gap from taking a healthy node offline.
//
// Score semantics (deterministic, no randomness):
//
//	+battery_pct/5          — reward more charge (0..20 points).
//	+15 if temp_c < 50      — cool node bonus.
//	−30 if temp_c > 70      — hot node penalty.
//	+25 if charging         — charging node bonus.
//
// BatteryMin and ThermalThreshold default to package constants BatteryMinPercent
// and ThermalThresholdC when set to 0.
//
// ChargeFloor is the minimum battery percentage below which a not-charging
// node is also rejected. It defaults to BatteryMinPercent (same as BatteryMin)
// when set to 0.  It is intentionally separate so callers can tune the
// "discharging below floor" gate independently from the absolute minimum.
type HandheldPolicy struct {
	// BatteryMin is the minimum battery percentage required for admission.
	// 0 means use the package default BatteryMinPercent.
	BatteryMin int
	// ThermalThreshold is the maximum node temperature in °C allowed for
	// admission. 0 means use the package default ThermalThresholdC.
	ThermalThreshold float64
	// ChargeFloor is the battery percentage below which a not-charging node
	// is rejected. 0 means use BatteryMinPercent.
	ChargeFloor int
}

// effectiveBatteryMin returns the configured or package-default battery minimum.
func (h *HandheldPolicy) effectiveBatteryMin() int {
	if h.BatteryMin > 0 {
		return h.BatteryMin
	}
	return BatteryMinPercent
}

// effectiveThermalThreshold returns the configured or package-default thermal
// threshold.
func (h *HandheldPolicy) effectiveThermalThreshold() float64 {
	if h.ThermalThreshold > 0 {
		return h.ThermalThreshold
	}
	return ThermalThresholdC
}

// effectiveChargeFloor returns the configured or package-default charge floor.
func (h *HandheldPolicy) effectiveChargeFloor() int {
	if h.ChargeFloor > 0 {
		return h.ChargeFloor
	}
	return BatteryMinPercent
}

// Name returns the plugin identifier.
func (h *HandheldPolicy) Name() string { return "HandheldPolicy" }

// Filter returns true iff the node passes all handheld power/thermal/state
// admission checks. Missing labels are treated as safe (assume-safe principle).
//
// Rejection conditions (any one is sufficient to reject):
//  1. battery_pct is present AND battery_pct < BatteryMin.
//  2. temp_c is present AND temp_c > ThermalThreshold.
//  3. device_state is present and non-available.
//  4. battery_pct is present AND battery_pct < ChargeFloor AND not charging.
func (h *HandheldPolicy) Filter(job *Job, node *Node) bool {
	if node == nil {
		return false
	}

	// (1) Battery percentage floor: reject if present and critically low.
	// CRITICAL GUARD: strictly less-than. Mutation: changing '<' to '>='
	// would admit critically-low-battery nodes — the test MUST catch this.
	if _, hasBatt := batteryLabel(node); hasBatt {
		pct := batteryPct(node)
		if pct < h.effectiveBatteryMin() {
			return false
		}
	}

	// (2) Thermal: reject if present and over threshold.
	if temp, ok := nodeTemp(node); ok {
		if temp > h.effectiveThermalThreshold() {
			return false
		}
	}

	// (3) Device state: reject any non-available state that is explicitly set.
	ds := deviceState(node) // returns DeviceStateAvailable when absent
	if ds != DeviceStateAvailable {
		return false
	}

	// (4) Charge floor: reject if discharging below the floor.
	// Applies only when battery_pct is present.
	if _, hasBatt := batteryLabel(node); hasBatt {
		pct := batteryPct(node)
		if pct < h.effectiveChargeFloor() && !isCharging(node) {
			return false
		}
	}

	return true
}

// Score returns a deterministic placement preference score.
// Higher is better. A nil node scores 0.
//
// Scoring rules:
//   - +battery_pct/5         (0..20 for 0..100 %)
//   - +15 if temp_c < 50°C
//   - −30 if temp_c > 70°C
//   - +25 if charging
func (h *HandheldPolicy) Score(_ *Job, node *Node) int {
	if node == nil {
		return 0
	}

	score := 0

	// Battery charge reward: each 5 % of charge = +1 point (0..20).
	if _, hasBatt := batteryLabel(node); hasBatt {
		score += batteryPct(node) / 5
	}

	// Thermal reward / penalty.
	if temp, ok := nodeTemp(node); ok {
		if temp < 50.0 {
			score += 15
		} else if temp > 70.0 {
			score -= 30
		}
	}

	// Charging bonus: plugged-in nodes are strongly preferred.
	if isCharging(node) {
		score += 25
	}

	return score
}

// Bind is a no-op success; HandheldPolicy does not consume resources.
func (h *HandheldPolicy) Bind(_ *Job, _ *Node) bool { return true }

// batteryLabel is a helper that reads the raw battery_pct label and returns
// (rawValue, true) when the key is present in the label map, (nil, false)
// otherwise. This allows Filter to distinguish "absent" (assume-safe) from
// "present but zero/low".
func batteryLabel(node *Node) (string, bool) {
	if node.Labels == nil {
		return "", false
	}
	v, ok := node.Labels[LabelBatteryPct]
	return v, ok
}
