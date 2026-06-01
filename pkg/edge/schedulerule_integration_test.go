//go:build integration

package edge

import (
	"fmt"
	"testing"
	"time"
)

// TestIntegration_ScheduleRule_DischargingBlocked verifies the per-tier rule
// engine under integration conditions with a per-run UUID in the trace.
//
// This test is gated by //go:build integration and is run by the orchestrator.
func TestIntegration_ScheduleRule_DischargingBlocked(t *testing.T) {
	runUUID := fmt.Sprintf("int-%d", time.Now().UnixNano())
	t.Logf("integration run_uuid=%s", runUUID)

	rules := DefaultTierRules()[SEMI]
	state := dischargingLowBatteryWifiState()

	eligible, trace := Evaluate(rules, state)

	if eligible {
		t.Errorf("device should be blocked (discharging+battery=15%%); eligible=true (run_uuid=%s)", runUUID)
	}

	// Log the full trace with run UUID.
	for i, cr := range trace {
		status := "PASS"
		if !cr.Passed {
			status = "FAIL"
		}
		req := "advisory"
		if cr.Required {
			req = "required"
		}
		t.Logf("trace[%d] condition=%s required=%s result=%s detail=%q run_uuid=%s",
			i, cr.Condition.Type, req, status, cr.Detail, runUUID)
	}

	// Assert IsCharging FAIL in trace.
	cr := findCondition(trace, IsCharging)
	if cr == nil {
		t.Errorf("IsCharging not in trace (run_uuid=%s)", runUUID)
	} else if cr.Passed {
		t.Errorf("IsCharging passed for discharging device (run_uuid=%s)", runUUID)
	}

	// Assert BatteryAbove FAIL in trace.
	br := findConditionByBatteryAbove(trace)
	if br == nil {
		t.Errorf("BatteryAbove not in trace (run_uuid=%s)", runUUID)
	} else if br.Passed {
		t.Errorf("BatteryAbove passed for battery=15%% (run_uuid=%s)", runUUID)
	}

	// Assert WifiConnected PASS in trace.
	wr := findCondition(trace, WifiConnected)
	if wr == nil {
		t.Errorf("WifiConnected not in trace (run_uuid=%s)", runUUID)
	} else if !wr.Passed {
		t.Errorf("WifiConnected failed for connected device (run_uuid=%s)", runUUID)
	}
}

// TestIntegration_ScheduleRule_AllTiers exercises DefaultTierRules for all
// tiers against a happy-path state that should be eligible everywhere.
func TestIntegration_ScheduleRule_AllTiers(t *testing.T) {
	runUUID := fmt.Sprintf("int-%d", time.Now().UnixNano())
	t.Logf("integration run_uuid=%s", runUUID)

	happyState := DeviceState{
		IsCharging:               true,
		WifiConnected:            true,
		BatteryPct:               90,
		CPUTempC:                 45.0,
		BackgroundRefreshEnabled: true,
		LowPowerModeOff:          true,
		Now:                      time.Date(2026, 6, 1, 23, 30, 0, 0, time.UTC),
	}

	tierRules := DefaultTierRules()
	for _, tier := range []DeviceTier{EDGE_DONOR, SEMI, STANDARD, FULL} {
		rules := tierRules[tier]
		eligible, trace := Evaluate(rules, happyState)
		t.Logf("tier=%s eligible=%v rules=%d trace=%d run_uuid=%s",
			tier, eligible, len(rules), len(trace), runUUID)
		if !eligible {
			for _, cr := range trace {
				if cr.Required && !cr.Passed {
					t.Logf("  FAIL required: %s — %s", cr.Condition.Type, cr.Detail)
				}
			}
			t.Errorf("tier=%s: happy-path device should be eligible (run_uuid=%s)", tier, runUUID)
		}
	}
}
