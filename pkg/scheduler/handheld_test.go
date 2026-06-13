package scheduler

// handheld_test.go — HXC-1298
//
// ANTI-BLUFF contract: every test assertion must FAIL when the production code
// is mutated in a way that breaks the real behaviour.
//
//   Key mutations and the tests that catch them:
//   - Flip '<' to '>=' in the battery-min check   → TestHandheldFilter_LowBatteryRejected FAILS
//   - Flip '<' to '>=' in the charge-floor check  → TestHandheldFilter_NotChargingBelowFloor FAILS
//   - Flip '>' to '<=' in the thermal check       → TestHandheldFilter_OverThermalRejected FAILS
//   - Score: remove battery contribution          → TestHandheldScore_OrderingByBattery FAILS
//   - Score: remove charging bonus               → TestHandheldScore_ChargingBonus FAILS

import (
	"testing"
)

// helpers — build minimal Node/Job with only the labels relevant to this test.

func handheldJob() *Job {
	return &Job{ID: "hxc-1298-job", Labels: map[string]string{}}
}

// battPctStr converts an int to its decimal string representation (small helper
// that avoids importing strconv in the test file).
func battPctStr(n int) string {
	if n < 0 {
		return "-1"
	}
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for v := n; v > 0; v /= 10 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
	}
	return string(digits)
}

// nodeLabeled builds a Node with an explicit label map.
func nodeLabeled(labels map[string]string) *Node {
	return &Node{ID: "n1", Labels: labels}
}

// ─── Filter tests ─────────────────────────────────────────────────────────────

// TestHandheldFilter_HealthyChargingAdmitted verifies that a node which is
// charging, has ample battery, cool temperature, and available state passes.
func TestHandheldFilter_HealthyChargingAdmitted(t *testing.T) {
	h := &HandheldPolicy{}
	node := nodeLabeled(map[string]string{
		LabelBatteryPct:  "80",
		LabelCharging:    "true",
		LabelNodeTempC:   "42.0",
		LabelDeviceState: DeviceStateAvailable,
	})
	if !h.Filter(handheldJob(), node) {
		t.Fatal("expected healthy charging node to be admitted; Filter returned false")
	}
}

// TestHandheldFilter_LowBatteryRejected is the primary ANTI-BLUFF test.
// A node with battery_pct below BatteryMinPercent (default 20) must be
// rejected regardless of charging state.
//
// Mutation target: flip '<' to '>=' in the battery-min guard in Filter.
// With that mutation this test passes a 10 % node → Filter returns true → FAIL.
func TestHandheldFilter_LowBatteryRejected(t *testing.T) {
	h := &HandheldPolicy{} // uses default BatteryMinPercent = 20
	node := nodeLabeled(map[string]string{
		LabelBatteryPct:  "10", // below 20 → must be rejected
		LabelCharging:    "true",
		LabelNodeTempC:   "40.0",
		LabelDeviceState: DeviceStateAvailable,
	})
	if h.Filter(handheldJob(), node) {
		t.Fatalf("expected low-battery node (10%%) to be rejected; Filter returned true (BatteryMin=%d)", h.effectiveBatteryMin())
	}
}

// TestHandheldFilter_BatteryAtExactMinAdmitted confirms that a node at exactly
// BatteryMinPercent is admitted (boundary: reject only strictly less than).
func TestHandheldFilter_BatteryAtExactMinAdmitted(t *testing.T) {
	h := &HandheldPolicy{} // BatteryMin = 20
	node := nodeLabeled(map[string]string{
		LabelBatteryPct:  battPctStr(BatteryMinPercent), // exactly 20
		LabelCharging:    "true",
		LabelNodeTempC:   "40.0",
		LabelDeviceState: DeviceStateAvailable,
	})
	if !h.Filter(handheldJob(), node) {
		t.Fatalf("expected node at exact BatteryMin (%d%%) to be admitted; Filter returned false", BatteryMinPercent)
	}
}

// TestHandheldFilter_OverThermalRejected verifies thermal gate.
//
// Mutation target: flip '>' to '<=' in the thermal check.
// With that mutation a 90 °C node would be admitted → FAIL.
func TestHandheldFilter_OverThermalRejected(t *testing.T) {
	h := &HandheldPolicy{}
	node := nodeLabeled(map[string]string{
		LabelBatteryPct:  "90",
		LabelCharging:    "true",
		LabelNodeTempC:   "90.0", // above ThermalThresholdC (75)
		LabelDeviceState: DeviceStateAvailable,
	})
	if h.Filter(handheldJob(), node) {
		t.Fatalf("expected over-thermal node (90°C) to be rejected; Filter returned true (threshold=%.1f)", h.effectiveThermalThreshold())
	}
}

// TestHandheldFilter_ThermalAtThresholdAdmitted confirms that a node at exactly
// ThermalThresholdC is admitted (strict greater-than comparison).
func TestHandheldFilter_ThermalAtThresholdAdmitted(t *testing.T) {
	h := &HandheldPolicy{}
	// Use a custom threshold so the assertion is independent of the constant value.
	h.ThermalThreshold = 75.0
	node := nodeLabeled(map[string]string{
		LabelBatteryPct:  "90",
		LabelCharging:    "true",
		LabelNodeTempC:   "75.0",
		LabelDeviceState: DeviceStateAvailable,
	})
	if !h.Filter(handheldJob(), node) {
		t.Fatal("expected node at exact thermal threshold (75.0°C) to be admitted; Filter returned false")
	}
}

// TestHandheldFilter_NotChargingBelowFloor verifies the charge-floor gate.
// A node that is not charging and has battery below the charge floor must be
// rejected, even when its absolute battery_pct is >= BatteryMin.
//
// We set BatteryMin=10 so the absolute gate passes, but ChargeFloor=30 so the
// "not charging below floor" gate rejects it.
//
// Mutation target: flip '<' to '>=' in the charge-floor guard.
// With that mutation a 25 % not-charging node would be admitted → FAIL.
func TestHandheldFilter_NotChargingBelowFloor(t *testing.T) {
	h := &HandheldPolicy{
		BatteryMin:  10, // absolute gate: 25 % passes
		ChargeFloor: 30, // floor gate: 25 % NOT charging → reject
	}
	node := nodeLabeled(map[string]string{
		LabelBatteryPct:  "25",
		LabelCharging:    "false", // not charging
		LabelNodeTempC:   "40.0",
		LabelDeviceState: DeviceStateAvailable,
	})
	if h.Filter(handheldJob(), node) {
		t.Fatal("expected not-charging node below charge floor to be rejected; Filter returned true")
	}
}

// TestHandheldFilter_NotChargingAboveFloorAdmitted verifies that a node above
// the charge floor is admitted even when not charging.
func TestHandheldFilter_NotChargingAboveFloorAdmitted(t *testing.T) {
	h := &HandheldPolicy{
		BatteryMin:  10,
		ChargeFloor: 30,
	}
	node := nodeLabeled(map[string]string{
		LabelBatteryPct:  "60", // above charge floor (30)
		LabelCharging:    "false",
		LabelNodeTempC:   "40.0",
		LabelDeviceState: DeviceStateAvailable,
	})
	if !h.Filter(handheldJob(), node) {
		t.Fatal("expected not-charging node above charge floor to be admitted; Filter returned false")
	}
}

// TestHandheldFilter_UnavailableStateRejected verifies device_state gate.
func TestHandheldFilter_UnavailableStateRejected(t *testing.T) {
	for _, state := range []string{DeviceStateUnavailable, DeviceStateSleeping, DeviceStateOffline} {
		state := state
		t.Run(state, func(t *testing.T) {
			h := &HandheldPolicy{}
			node := nodeLabeled(map[string]string{
				LabelBatteryPct:  "80",
				LabelCharging:    "true",
				LabelNodeTempC:   "40.0",
				LabelDeviceState: state,
			})
			if h.Filter(handheldJob(), node) {
				t.Fatalf("expected node with device_state=%q to be rejected; Filter returned true", state)
			}
		})
	}
}

// TestHandheldFilter_AbsentBatteryLabelAssumesSafe is the critical absent-label
// test mandated by the spec: a node with NO battery_pct label must NOT be
// rejected on battery grounds. The absent label is treated as safe.
func TestHandheldFilter_AbsentBatteryLabelAssumesSafe(t *testing.T) {
	h := &HandheldPolicy{}
	node := nodeLabeled(map[string]string{
		// LabelBatteryPct intentionally absent
		LabelCharging:    "true",
		LabelNodeTempC:   "40.0",
		LabelDeviceState: DeviceStateAvailable,
	})
	if !h.Filter(handheldJob(), node) {
		t.Fatal("expected node with absent battery_pct label to be admitted (assume-safe); Filter returned false")
	}
}

// TestHandheldFilter_AbsentTempLabelAssumesSafe verifies that a node with no
// temp_c label is admitted (thermal assume-safe).
func TestHandheldFilter_AbsentTempLabelAssumesSafe(t *testing.T) {
	h := &HandheldPolicy{}
	node := nodeLabeled(map[string]string{
		LabelBatteryPct: "80",
		LabelCharging:   "true",
		// LabelNodeTempC intentionally absent
		LabelDeviceState: DeviceStateAvailable,
	})
	if !h.Filter(handheldJob(), node) {
		t.Fatal("expected node with absent temp_c label to be admitted (assume-safe); Filter returned false")
	}
}

// TestHandheldFilter_AllLabelsAbsent verifies the fully-absent case: a node
// with NO labels at all is admitted (everything assume-safe).
func TestHandheldFilter_AllLabelsAbsent(t *testing.T) {
	h := &HandheldPolicy{}
	node := &Node{ID: "bare", Labels: nil}
	if !h.Filter(handheldJob(), node) {
		t.Fatal("expected node with nil Labels to be admitted (all assume-safe); Filter returned false")
	}
}

// TestHandheldFilter_NilNodeRejected confirms nil node is safely rejected.
func TestHandheldFilter_NilNodeRejected(t *testing.T) {
	h := &HandheldPolicy{}
	if h.Filter(handheldJob(), nil) {
		t.Fatal("expected nil node to be rejected; Filter returned true")
	}
}

// ─── Score tests ──────────────────────────────────────────────────────────────

// TestHandheldScore_OrderingByBattery is a primary ANTI-BLUFF test.
// Score MUST decrease as battery drops (all other labels equal).
//
// Mutation target: remove battery_pct contribution from Score.
// With that mutation all three nodes score the same → ordering assertion FAILS.
func TestHandheldScore_OrderingByBattery(t *testing.T) {
	h := &HandheldPolicy{}
	job := handheldJob()

	nodeHigh := nodeLabeled(map[string]string{
		LabelBatteryPct:  "90",
		LabelCharging:    "true",
		LabelNodeTempC:   "45.0",
		LabelDeviceState: DeviceStateAvailable,
	})
	nodeMid := nodeLabeled(map[string]string{
		LabelBatteryPct:  "50",
		LabelCharging:    "true",
		LabelNodeTempC:   "45.0",
		LabelDeviceState: DeviceStateAvailable,
	})
	nodeLow := nodeLabeled(map[string]string{
		LabelBatteryPct:  "25",
		LabelCharging:    "true",
		LabelNodeTempC:   "45.0",
		LabelDeviceState: DeviceStateAvailable,
	})

	sHigh := h.Score(job, nodeHigh)
	sMid := h.Score(job, nodeMid)
	sLow := h.Score(job, nodeLow)

	if !(sHigh > sMid) {
		t.Fatalf("expected Score(90%%) > Score(50%%); got %d vs %d", sHigh, sMid)
	}
	if !(sMid > sLow) {
		t.Fatalf("expected Score(50%%) > Score(25%%); got %d vs %d", sMid, sLow)
	}
}

// TestHandheldScore_ChargingBonus verifies that a charging node scores strictly
// higher than an identical non-charging node.
//
// Mutation target: remove the +25 charging bonus in Score.
// With that mutation both nodes score equally → FAIL.
func TestHandheldScore_ChargingBonus(t *testing.T) {
	h := &HandheldPolicy{}
	job := handheldJob()

	baseLabels := map[string]string{
		LabelBatteryPct:  "60",
		LabelNodeTempC:   "45.0",
		LabelDeviceState: DeviceStateAvailable,
	}

	chargingLabels := make(map[string]string)
	for k, v := range baseLabels {
		chargingLabels[k] = v
	}
	chargingLabels[LabelCharging] = "true"

	notChargingLabels := make(map[string]string)
	for k, v := range baseLabels {
		notChargingLabels[k] = v
	}
	notChargingLabels[LabelCharging] = "false"

	sCharging := h.Score(job, nodeLabeled(chargingLabels))
	sNotCharging := h.Score(job, nodeLabeled(notChargingLabels))

	if !(sCharging > sNotCharging) {
		t.Fatalf("expected charging node to score higher than not-charging; got charging=%d, not-charging=%d", sCharging, sNotCharging)
	}
}

// TestHandheldScore_CoolBonus verifies thermal contribution: a cool node
// (<50°C) scores higher than a warm node (50–70°C range), which scores higher
// than a hot node (>70°C).
func TestHandheldScore_CoolBonus(t *testing.T) {
	h := &HandheldPolicy{}
	job := handheldJob()

	base := map[string]string{
		LabelBatteryPct:  "60",
		LabelCharging:    "false",
		LabelDeviceState: DeviceStateAvailable,
	}

	cool := make(map[string]string)
	for k, v := range base {
		cool[k] = v
	}
	cool[LabelNodeTempC] = "40.0"

	warm := make(map[string]string)
	for k, v := range base {
		warm[k] = v
	}
	warm[LabelNodeTempC] = "60.0"

	hot := make(map[string]string)
	for k, v := range base {
		hot[k] = v
	}
	hot[LabelNodeTempC] = "80.0"

	sCool := h.Score(job, nodeLabeled(cool))
	sWarm := h.Score(job, nodeLabeled(warm))
	sHot := h.Score(job, nodeLabeled(hot))

	if !(sCool > sWarm) {
		t.Fatalf("expected cool (40°C) to score higher than warm (60°C); got cool=%d warm=%d", sCool, sWarm)
	}
	if !(sWarm > sHot) {
		t.Fatalf("expected warm (60°C) to score higher than hot (80°C); got warm=%d hot=%d", sWarm, sHot)
	}
}

// TestHandheldScore_AbsentBatteryNoContribution verifies that an absent
// battery_pct label contributes 0 to the score (not a negative value).
func TestHandheldScore_AbsentBatteryNoContribution(t *testing.T) {
	h := &HandheldPolicy{}
	job := handheldJob()

	withBattery := nodeLabeled(map[string]string{
		LabelBatteryPct:  "0",
		LabelCharging:    "false",
		LabelNodeTempC:   "60.0",
		LabelDeviceState: DeviceStateAvailable,
	})
	withoutBattery := nodeLabeled(map[string]string{
		// LabelBatteryPct absent
		LabelCharging:    "false",
		LabelNodeTempC:   "60.0",
		LabelDeviceState: DeviceStateAvailable,
	})

	// Both should score the same: 0 (no battery contribution from either,
	// no thermal bonus at 60°C, no charging bonus).
	sWithBattery := h.Score(job, withBattery)
	sWithout := h.Score(job, withoutBattery)

	if sWithBattery != sWithout {
		t.Fatalf("expected absent battery_pct and 0%% battery to score equally; got with=%d without=%d", sWithBattery, sWithout)
	}
}

// TestHandheldScore_NilNodeZero confirms Score does not panic and returns 0 for
// a nil node.
func TestHandheldScore_NilNodeZero(t *testing.T) {
	h := &HandheldPolicy{}
	if s := h.Score(handheldJob(), nil); s != 0 {
		t.Fatalf("expected Score(nil node) == 0; got %d", s)
	}
}

// TestHandheldScore_DeterministicRepeat confirms that calling Score twice on the
// same node returns the same value (no hidden randomness).
func TestHandheldScore_DeterministicRepeat(t *testing.T) {
	h := &HandheldPolicy{}
	job := handheldJob()
	node := nodeLabeled(map[string]string{
		LabelBatteryPct:  "70",
		LabelCharging:    "true",
		LabelNodeTempC:   "45.0",
		LabelDeviceState: DeviceStateAvailable,
	})
	s1 := h.Score(job, node)
	s2 := h.Score(job, node)
	if s1 != s2 {
		t.Fatalf("Score is not deterministic: first call=%d, second call=%d", s1, s2)
	}
}

// TestHandheldScore_ExactValues verifies the exact scoring arithmetic so any
// formula change (even one that preserves ordering) is detected.
//
// Node: battery=80%, charging=true, temp=40°C
//
//	Expected: 80/5 + 15 (temp<50) + 25 (charging) = 16 + 15 + 25 = 56
func TestHandheldScore_ExactValues(t *testing.T) {
	h := &HandheldPolicy{}
	node := nodeLabeled(map[string]string{
		LabelBatteryPct:  "80",
		LabelCharging:    "true",
		LabelNodeTempC:   "40.0",
		LabelDeviceState: DeviceStateAvailable,
	})
	got := h.Score(handheldJob(), node)
	const want = 56 // 16 + 15 + 25
	if got != want {
		t.Fatalf("Score mismatch: got %d, want %d (battery=80%%: +16, temp<50: +15, charging: +25)", got, want)
	}
}

// ─── Bind / Name tests ────────────────────────────────────────────────────────

func TestHandheldPolicy_Name(t *testing.T) {
	h := &HandheldPolicy{}
	if h.Name() != "HandheldPolicy" {
		t.Fatalf("expected Name() == \"HandheldPolicy\"; got %q", h.Name())
	}
}

func TestHandheldPolicy_BindAlwaysTrue(t *testing.T) {
	h := &HandheldPolicy{}
	node := nodeLabeled(map[string]string{LabelDeviceState: DeviceStateAvailable})
	if !h.Bind(handheldJob(), node) {
		t.Fatal("expected Bind to return true; got false")
	}
	if !h.Bind(handheldJob(), nil) {
		t.Fatal("expected Bind(nil) to return true; got false")
	}
}

// ─── Plugin interface conformance ────────────────────────────────────────────

// TestHandheldPolicy_ImplementsPlugin is a compile-time interface check.
// If HandheldPolicy stops implementing Plugin this file will not compile.
var _ Plugin = (*HandheldPolicy)(nil)
