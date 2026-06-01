package edge

import (
	"testing"
	"time"
)

// dischargingLowBatteryWifiState returns a DeviceState that is:
//   - NOT charging (IsCharging=false) — fails IsCharging rule
//   - WiFi connected (WifiConnected=true) — passes WifiConnected rule
//   - Battery at 15% (BatteryPct=15) — fails BatteryAbove(20) rule
//   - CPU temp 55°C — passes CPUTempBelow(75) rule
func dischargingLowBatteryWifiState() DeviceState {
	return DeviceState{
		IsCharging:               false,
		WifiConnected:            true,
		BatteryPct:               15,
		CPUTempC:                 55.0,
		BackgroundRefreshEnabled: true,
		LowPowerModeOff:          true,
		Now:                      time.Date(2026, 6, 1, 14, 0, 0, 0, time.UTC),
	}
}

// semiTierRules returns the SEMI rules from DefaultTierRules. This is the
// "T6/SEMI rules requiring is_charging + battery_above(20) + wifi" referred to
// in the HXC-1191 closure criteria. (T6 maps to SEMI in this package since we
// reuse TrustLevel as DeviceTier.)
func semiTierRules() []ScheduleRule {
	return DefaultTierRules()[SEMI]
}

// TestEvaluate_DischargingLowBattery_Blocked is the primary sink-side
// assertion for HXC-1191.
//
// The state: not charging, battery=15%, WiFi=yes.
// The SEMI rules require is_charging AND battery_above(20) AND wifi.
// Expected: eligible=false; trace contains IsCharging FAIL and BatteryAbove FAIL.
//
// MUTATION target: mark all Required=false → eligible=true → test FAILS.
func TestEvaluate_DischargingLowBattery_Blocked(t *testing.T) {
	rules := semiTierRules()
	state := dischargingLowBatteryWifiState()

	eligible, trace := Evaluate(rules, state)

	// Primary assertion: device must NOT be eligible.
	if eligible {
		t.Error("Evaluate(discharging+low-battery+wifi) = eligible=true, want false (should be blocked)")
	}

	// Trace must be non-empty.
	if len(trace) == 0 {
		t.Fatal("trace is empty, want per-condition results")
	}

	// Find IsCharging condition in the trace and assert it FAILED.
	chargingResult := findCondition(trace, IsCharging)
	if chargingResult == nil {
		t.Error("no IsCharging condition found in trace")
	} else {
		if chargingResult.Passed {
			t.Error("IsCharging condition Passed=true for a discharging device, want false")
		}
		if chargingResult.Detail == "" {
			t.Error("IsCharging condition Detail is empty, want a reason string")
		}
	}

	// Find BatteryAbove condition in the trace and assert it FAILED.
	batteryResult := findConditionByBatteryAbove(trace)
	if batteryResult == nil {
		t.Error("no BatteryAbove condition found in trace")
	} else {
		if batteryResult.Passed {
			t.Errorf("BatteryAbove condition Passed=true for battery=15%% (threshold=20), want false")
		}
		if batteryResult.Detail == "" {
			t.Error("BatteryAbove condition Detail is empty")
		}
	}

	// WifiConnected must PASS (device is on WiFi).
	wifiResult := findCondition(trace, WifiConnected)
	if wifiResult == nil {
		t.Error("no WifiConnected condition found in trace")
	} else if !wifiResult.Passed {
		t.Error("WifiConnected condition Passed=false for a WiFi-connected device, want true")
	}

	// Verify trace captures the run UUID context by checking detail strings
	// are non-empty for every condition.
	for i, cr := range trace {
		if cr.Detail == "" {
			t.Errorf("trace[%d] condition %s has empty Detail", i, cr.Condition.Type)
		}
	}
}

// TestEvaluate_ChargingFullBattery_Eligible verifies a fully-charged,
// charging device on WiFi is eligible under the SEMI rules.
func TestEvaluate_ChargingFullBattery_Eligible(t *testing.T) {
	rules := semiTierRules()
	state := DeviceState{
		IsCharging:               true,
		WifiConnected:            true,
		BatteryPct:               85,
		CPUTempC:                 45.0,
		BackgroundRefreshEnabled: true,
		LowPowerModeOff:          true,
		Now:                      time.Date(2026, 6, 1, 14, 0, 0, 0, time.UTC),
	}

	eligible, trace := Evaluate(rules, state)
	if !eligible {
		// Diagnose: report which required conditions failed.
		for _, cr := range trace {
			if cr.Required && !cr.Passed {
				t.Logf("failed required condition: %s — %s", cr.Condition.Type, cr.Detail)
			}
		}
		t.Error("Evaluate(charging+85%+wifi) = eligible=false, want true")
	}
	if len(trace) == 0 {
		t.Error("trace is empty, want per-condition results")
	}
}

// TestEvaluate_AdvisoryConditionDoesNotBlock verifies that marking a failing
// condition Required=false keeps the device eligible.
//
// MUTATION target: changing a Required=false rule to Required=true → eligible
// becomes false and this test fails.
func TestEvaluate_AdvisoryConditionDoesNotBlock(t *testing.T) {
	// Single advisory rule that will fail (device not charging).
	rules := []ScheduleRule{
		{Condition: Condition{Type: IsCharging}, Required: false},
	}
	state := DeviceState{
		IsCharging:    false,
		WifiConnected: true,
		BatteryPct:    90,
	}

	eligible, trace := Evaluate(rules, state)
	if !eligible {
		t.Error("advisory-only failing rule should not block: eligible=false, want true")
	}
	if len(trace) != 1 {
		t.Fatalf("expected 1 trace entry, got %d", len(trace))
	}
	if trace[0].Passed {
		t.Error("expected IsCharging to be recorded as failed in trace")
	}
	if trace[0].Required {
		t.Error("trace[0].Required = true, want false (advisory)")
	}
}

// TestEvaluate_EmptyRules_AlwaysEligible verifies that an empty ruleset
// always returns eligible=true.
func TestEvaluate_EmptyRules_AlwaysEligible(t *testing.T) {
	eligible, trace := Evaluate([]ScheduleRule{}, DeviceState{})
	if !eligible {
		t.Error("empty ruleset: eligible=false, want true")
	}
	if len(trace) != 0 {
		t.Errorf("empty ruleset: trace len=%d, want 0", len(trace))
	}
}

// TestEvaluate_TimeBetween_InWindow verifies the TimeBetween condition passes
// when the current time is within the window.
func TestEvaluate_TimeBetween_InWindow(t *testing.T) {
	rules := []ScheduleRule{
		{
			Condition: Condition{Type: TimeBetween, ThresholdA: 22.0, ThresholdB: 6.0}, // 22:00–06:00 wraps midnight
			Required:  true,
		},
	}
	state := DeviceState{
		Now: time.Date(2026, 6, 1, 23, 30, 0, 0, time.UTC), // 23:30 — in window
	}
	eligible, trace := Evaluate(rules, state)
	if !eligible {
		t.Errorf("TimeBetween 22–06 at 23:30 should be eligible; trace: %v", trace)
	}
}

// TestEvaluate_TimeBetween_OutOfWindow verifies the TimeBetween condition
// fails (and blocks) when outside the window.
func TestEvaluate_TimeBetween_OutOfWindow(t *testing.T) {
	rules := []ScheduleRule{
		{
			Condition: Condition{Type: TimeBetween, ThresholdA: 22.0, ThresholdB: 6.0},
			Required:  true,
		},
	}
	state := DeviceState{
		Now: time.Date(2026, 6, 1, 14, 0, 0, 0, time.UTC), // 14:00 — out of window
	}
	eligible, _ := Evaluate(rules, state)
	if eligible {
		t.Error("TimeBetween 22–06 at 14:00 should NOT be eligible")
	}
}

// TestDefaultTierRules_ContainsAllLevels verifies that DefaultTierRules
// returns entries for all four trust levels.
func TestDefaultTierRules_ContainsAllLevels(t *testing.T) {
	rules := DefaultTierRules()
	for _, level := range []DeviceTier{EDGE_DONOR, SEMI, STANDARD, FULL} {
		if _, ok := rules[level]; !ok {
			t.Errorf("DefaultTierRules() missing entry for level %s", level)
		}
	}
}

// TestEvaluate_TracePreservesRequiredFlag verifies that ConditionResult.Required
// mirrors ScheduleRule.Required.
func TestEvaluate_TracePreservesRequiredFlag(t *testing.T) {
	rules := []ScheduleRule{
		{Condition: Condition{Type: IsCharging}, Required: true},
		{Condition: Condition{Type: WifiConnected}, Required: false},
	}
	state := DeviceState{IsCharging: true, WifiConnected: false}
	_, trace := Evaluate(rules, state)

	if len(trace) != 2 {
		t.Fatalf("expected 2 trace entries, got %d", len(trace))
	}
	if !trace[0].Required {
		t.Error("trace[0].Required = false, want true")
	}
	if trace[1].Required {
		t.Error("trace[1].Required = true, want false")
	}
}

// ---- helpers ----------------------------------------------------------------

// findCondition finds the first ConditionResult for the given ConditionType.
func findCondition(trace []ConditionResult, ct ConditionType) *ConditionResult {
	for i := range trace {
		if trace[i].Condition.Type == ct {
			return &trace[i]
		}
	}
	return nil
}

// findConditionByBatteryAbove finds the first BatteryAbove ConditionResult.
func findConditionByBatteryAbove(trace []ConditionResult) *ConditionResult {
	for i := range trace {
		if trace[i].Condition.Type == BatteryAbove {
			return &trace[i]
		}
	}
	return nil
}
