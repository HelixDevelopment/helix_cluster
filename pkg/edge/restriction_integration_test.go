//go:build integration

package edge

import (
	"fmt"
	"testing"
	"time"
)

// TestIntegration_RestrictionMatrix_FullCoverage evaluates every cell in the
// restriction matrix and logs a per-run UUID with each decision for auditability.
//
// This test is gated by //go:build integration and is run by the orchestrator.
func TestIntegration_RestrictionMatrix_FullCoverage(t *testing.T) {
	runUUID := fmt.Sprintf("int-%d", time.Now().UnixNano())
	t.Logf("integration run_uuid=%s", runUUID)

	// Expected matrix: true = allowed.
	matrix := []struct {
		level   TrustLevel
		wt      WorkloadType
		allowed bool
	}{
		{EDGE_DONOR, InteractiveSession, false},
		{EDGE_DONOR, SensitiveData, false},
		{EDGE_DONOR, PersistentStorage, false},
		{EDGE_DONOR, NetworkRelay, false},
		{EDGE_DONOR, AIInference, true},
		{EDGE_DONOR, DataProcessing, true},
		{EDGE_DONOR, SmallBatch, true},
		{SEMI, InteractiveSession, false},
		{SEMI, SensitiveData, false},
		{SEMI, PersistentStorage, false},
		{SEMI, NetworkRelay, false},
		{SEMI, AIInference, true},
		{SEMI, DataProcessing, true},
		{SEMI, SmallBatch, true},
		{STANDARD, InteractiveSession, true},
		{STANDARD, SensitiveData, false},
		{STANDARD, PersistentStorage, true},
		{STANDARD, NetworkRelay, true},
		{STANDARD, AIInference, true},
		{STANDARD, DataProcessing, true},
		{STANDARD, SmallBatch, true},
		{FULL, InteractiveSession, true},
		{FULL, SensitiveData, true},
		{FULL, PersistentStorage, true},
		{FULL, NetworkRelay, true},
		{FULL, AIInference, true},
		{FULL, DataProcessing, true},
		{FULL, SmallBatch, true},
	}

	for _, tc := range matrix {
		dec := Allowed(tc.level, tc.wt)
		result := "ALLOW"
		if !dec.Allowed {
			result = "DENY"
		}
		t.Logf("(%s, %s) → %s | %s | run_uuid=%s",
			tc.level, tc.wt, result, dec.PolicyReason, runUUID)

		if dec.Allowed != tc.allowed {
			t.Errorf("(%s, %s): Allowed=%v, want %v; reason=%q; run_uuid=%s",
				tc.level, tc.wt, dec.Allowed, tc.allowed, dec.PolicyReason, runUUID)
		}
		// Sink-side: every decision must record the correct (workload, trust).
		if dec.WorkloadType != tc.wt {
			t.Errorf("dec.WorkloadType=%s, want %s (run_uuid=%s)", dec.WorkloadType, tc.wt, runUUID)
		}
		if dec.TrustLevel != tc.level {
			t.Errorf("dec.TrustLevel=%s, want %s (run_uuid=%s)", dec.TrustLevel, tc.level, runUUID)
		}
	}
}
