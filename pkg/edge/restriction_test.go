package edge

import (
	"testing"
)

// TestAllowed_SensitiveDataOnSEMI_Denied verifies that SensitiveData workload
// is denied to a SEMI device. This is the primary sink-side assertion for
// HXC-1196.
//
// MUTATION target: change the SensitiveData case to "level >= SEMI" → this test fails.
func TestAllowed_SensitiveDataOnSEMI_Denied(t *testing.T) {
	dec := Allowed(SEMI, SensitiveData)
	if dec.Allowed {
		t.Errorf("Allowed(SEMI, SensitiveData) = true, want false")
	}
	if dec.PolicyReason == "" {
		t.Error("PolicyReason is empty, want a non-empty denial explanation")
	}
	// Verify sink-side: the decision records the correct workload and level.
	if dec.WorkloadType != SensitiveData {
		t.Errorf("dec.WorkloadType = %s, want SensitiveData", dec.WorkloadType)
	}
	if dec.TrustLevel != SEMI {
		t.Errorf("dec.TrustLevel = %s, want SEMI", dec.TrustLevel)
	}
}

// TestAllowed_InteractiveOnEDGE_DONOR_Denied verifies that InteractiveSession
// is denied to an EDGE_DONOR device.
//
// MUTATION target: open the InteractiveSession cell to EDGE_DONOR → fails.
func TestAllowed_InteractiveOnEDGE_DONOR_Denied(t *testing.T) {
	dec := Allowed(EDGE_DONOR, InteractiveSession)
	if dec.Allowed {
		t.Errorf("Allowed(EDGE_DONOR, InteractiveSession) = true, want false")
	}
	if dec.PolicyReason == "" {
		t.Error("PolicyReason is empty")
	}
	if dec.TrustLevel != EDGE_DONOR {
		t.Errorf("dec.TrustLevel = %s, want EDGE_DONOR", dec.TrustLevel)
	}
}

// TestAllowed_AIInferenceOnSEMI_Allowed verifies that AIInference is permitted
// to SEMI.
func TestAllowed_AIInferenceOnSEMI_Allowed(t *testing.T) {
	dec := Allowed(SEMI, AIInference)
	if !dec.Allowed {
		t.Errorf("Allowed(SEMI, AIInference) = false, want true; reason: %s", dec.PolicyReason)
	}
}

// TestAllowed_AIInferenceOnEDGE_DONOR_Allowed verifies that AIInference is
// permitted to EDGE_DONOR.
func TestAllowed_AIInferenceOnEDGE_DONOR_Allowed(t *testing.T) {
	dec := Allowed(EDGE_DONOR, AIInference)
	if !dec.Allowed {
		t.Errorf("Allowed(EDGE_DONOR, AIInference) = false, want true; reason: %s", dec.PolicyReason)
	}
}

// TestAllowed_SensitiveDataOnFULL_Allowed verifies that SensitiveData is
// permitted to FULL.
func TestAllowed_SensitiveDataOnFULL_Allowed(t *testing.T) {
	dec := Allowed(FULL, SensitiveData)
	if !dec.Allowed {
		t.Errorf("Allowed(FULL, SensitiveData) = false, want true; reason: %s", dec.PolicyReason)
	}
}

// TestAllowed_PersistentStorageOnSEMI_Denied verifies PersistentStorage is
// denied to SEMI.
func TestAllowed_PersistentStorageOnSEMI_Denied(t *testing.T) {
	dec := Allowed(SEMI, PersistentStorage)
	if dec.Allowed {
		t.Errorf("Allowed(SEMI, PersistentStorage) = true, want false")
	}
}

// TestAllowed_PersistentStorageOnEDGE_DONOR_Denied verifies PersistentStorage
// is denied to EDGE_DONOR.
func TestAllowed_PersistentStorageOnEDGE_DONOR_Denied(t *testing.T) {
	dec := Allowed(EDGE_DONOR, PersistentStorage)
	if dec.Allowed {
		t.Errorf("Allowed(EDGE_DONOR, PersistentStorage) = true, want false")
	}
}

// TestAllowed_NetworkRelayOnSEMI_Denied verifies NetworkRelay is denied to SEMI.
func TestAllowed_NetworkRelayOnSEMI_Denied(t *testing.T) {
	dec := Allowed(SEMI, NetworkRelay)
	if dec.Allowed {
		t.Errorf("Allowed(SEMI, NetworkRelay) = true, want false")
	}
}

// TestAllowed_NetworkRelayOnSTANDARD_Allowed verifies NetworkRelay is allowed
// for STANDARD.
func TestAllowed_NetworkRelayOnSTANDARD_Allowed(t *testing.T) {
	dec := Allowed(STANDARD, NetworkRelay)
	if !dec.Allowed {
		t.Errorf("Allowed(STANDARD, NetworkRelay) = false, want true; reason: %s", dec.PolicyReason)
	}
}

// TestAllowed_SmallBatchAllLevels verifies SmallBatch is allowed at every
// trust level.
func TestAllowed_SmallBatchAllLevels(t *testing.T) {
	levels := []TrustLevel{EDGE_DONOR, SEMI, STANDARD, FULL}
	for _, level := range levels {
		dec := Allowed(level, SmallBatch)
		if !dec.Allowed {
			t.Errorf("Allowed(%s, SmallBatch) = false, want true; reason: %s", level, dec.PolicyReason)
		}
	}
}

// TestAllowed_DataProcessingAllLevels verifies DataProcessing is allowed at
// every trust level.
func TestAllowed_DataProcessingAllLevels(t *testing.T) {
	levels := []TrustLevel{EDGE_DONOR, SEMI, STANDARD, FULL}
	for _, level := range levels {
		dec := Allowed(level, DataProcessing)
		if !dec.Allowed {
			t.Errorf("Allowed(%s, DataProcessing) = false, want true; reason: %s", level, dec.PolicyReason)
		}
	}
}

// TestAllowed_InteractiveOnSTANDARD_Allowed verifies InteractiveSession is
// allowed for STANDARD.
func TestAllowed_InteractiveOnSTANDARD_Allowed(t *testing.T) {
	dec := Allowed(STANDARD, InteractiveSession)
	if !dec.Allowed {
		t.Errorf("Allowed(STANDARD, InteractiveSession) = false, want true; reason: %s", dec.PolicyReason)
	}
}

// TestAllowed_DecisionRecordsWorkloadAndTrust verifies that the AdmissionDecision
// struct faithfully records the (workload, trust) pair for every cell.
// This is the per-(workload,trust) capture requirement from HXC-1196.
func TestAllowed_DecisionRecordsWorkloadAndTrust(t *testing.T) {
	cases := []struct {
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
	for _, tc := range cases {
		dec := Allowed(tc.level, tc.wt)
		if dec.WorkloadType != tc.wt {
			t.Errorf("(%s,%s): dec.WorkloadType = %s, want %s", tc.level, tc.wt, dec.WorkloadType, tc.wt)
		}
		if dec.TrustLevel != tc.level {
			t.Errorf("(%s,%s): dec.TrustLevel = %s, want %s", tc.level, tc.wt, dec.TrustLevel, tc.level)
		}
		if dec.Allowed != tc.allowed {
			t.Errorf("(%s,%s): dec.Allowed = %v, want %v; reason: %s", tc.level, tc.wt, dec.Allowed, tc.allowed, dec.PolicyReason)
		}
	}
}
