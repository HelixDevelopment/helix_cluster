package edge

import (
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/device"
)

// androidDescriptor returns a descriptor that matches the Android-class
// heuristic: battery-powered ARM64 device with no real GPU.
func androidDescriptor() device.DeviceDescriptor {
	return device.DeviceDescriptor{
		ID:           "android-test-01",
		Arch:         device.ArchARM64,
		CPUCores:     8,
		MemoryBytes:  6 * (1 << 30), // 6 GiB
		GPUCount:     1,
		GPUVRAMBytes: 128 * (1 << 20), // 128 MiB integrated GPU — below androidMaxVRAMBytes
		NPUTops:      0,
		HasBattery:   true,
		Trust:        device.TrustBasic,
	}
}

// iphoneDescriptor returns a descriptor that matches the iOS-class heuristic:
// battery-powered ARM64 device with no discrete GPU but a non-zero NPU.
func iphoneDescriptor() device.DeviceDescriptor {
	return device.DeviceDescriptor{
		ID:          "iphone-test-01",
		Arch:        device.ArchARM64,
		CPUCores:    6,
		MemoryBytes: 8 * (1 << 30), // 8 GiB
		GPUCount:    0,
		NPUTops:     17.0, // Apple Neural Engine equivalent
		HasBattery:  true,
		Trust:       device.TrustBasic,
	}
}

// TestAssignTrustLevel_Android verifies that an Android-class descriptor maps
// to SEMI. This is the primary sink-side assertion for HXC-1195.
//
// MUTATION target: swap the Android rule to return EDGE_DONOR → this test fails.
func TestAssignTrustLevel_Android(t *testing.T) {
	d := androidDescriptor()
	level, binding := AssignTrustLevel(d)

	if level != SEMI {
		t.Errorf("AssignTrustLevel(android) = %s, want SEMI", level)
	}
	// Verify the binding audit record is populated.
	if binding.DeviceID != d.ID {
		t.Errorf("binding.DeviceID = %q, want %q", binding.DeviceID, d.ID)
	}
	if binding.Level != SEMI {
		t.Errorf("binding.Level = %s, want SEMI", binding.Level)
	}
	if binding.AssignedAt.IsZero() {
		t.Error("binding.AssignedAt is zero, want a non-zero timestamp")
	}
	if binding.Rationale == "" {
		t.Error("binding.Rationale is empty, want a non-empty rationale")
	}
}

// TestAssignTrustLevel_iPhone verifies that an iOS-class descriptor maps to
// EDGE_DONOR. This is the primary sink-side assertion for HXC-1195.
//
// MUTATION target: swap the iOS rule to return SEMI → this test fails.
func TestAssignTrustLevel_iPhone(t *testing.T) {
	d := iphoneDescriptor()
	level, binding := AssignTrustLevel(d)

	if level != EDGE_DONOR {
		t.Errorf("AssignTrustLevel(iphone) = %s, want EDGE_DONOR", level)
	}
	if binding.Level != EDGE_DONOR {
		t.Errorf("binding.Level = %s, want EDGE_DONOR", binding.Level)
	}
	if binding.DeviceID != d.ID {
		t.Errorf("binding.DeviceID = %q, want %q", binding.DeviceID, d.ID)
	}
	if binding.AssignedAt.IsZero() {
		t.Error("binding.AssignedAt is zero")
	}
	if binding.Rationale == "" {
		t.Error("binding.Rationale is empty")
	}
}

// TestAssignTrustLevel_AttestedDeviceIsFull verifies that hardware attestation
// always yields FULL regardless of form factor.
func TestAssignTrustLevel_AttestedDeviceIsFull(t *testing.T) {
	d := androidDescriptor()
	d.Trust = device.TrustAttested

	level, binding := AssignTrustLevel(d)
	if level != FULL {
		t.Errorf("TrustAttested android → %s, want FULL", level)
	}
	if binding.Level != FULL {
		t.Errorf("binding.Level = %s, want FULL", binding.Level)
	}
}

// TestAssignTrustLevel_ConfidentialIsFull verifies that confidential-compute
// trust yields FULL.
func TestAssignTrustLevel_ConfidentialIsFull(t *testing.T) {
	d := device.DeviceDescriptor{
		ID:          "tee-server-01",
		Arch:        device.ArchAMD64,
		CPUCores:    64,
		MemoryBytes: 256 * (1 << 30),
		Trust:       device.TrustConfidential,
	}
	level, _ := AssignTrustLevel(d)
	if level != FULL {
		t.Errorf("TrustConfidential server → %s, want FULL", level)
	}
}

// TestAssignTrustLevel_SBCIsStandard verifies that a TrustBasic SBC (T3)
// is classified STANDARD.
func TestAssignTrustLevel_SBCIsStandard(t *testing.T) {
	d := device.DeviceDescriptor{
		ID:          "rpi-01",
		Arch:        device.ArchARM64,
		CPUCores:    4,
		MemoryBytes: 4 * (1 << 30),
		Trust:       device.TrustBasic,
		HasBattery:  false,
	}
	level, binding := AssignTrustLevel(d)
	if level != STANDARD {
		t.Errorf("SBC TrustBasic → %s, want STANDARD", level)
	}
	if binding.Level != STANDARD {
		t.Errorf("binding.Level = %s, want STANDARD", binding.Level)
	}
}

// TestTrustLevelStringRoundTrip verifies that String() and ParseTrustLevel()
// are inverses for every named level.
func TestTrustLevelStringRoundTrip(t *testing.T) {
	levels := []TrustLevel{EDGE_DONOR, SEMI, STANDARD, FULL}
	for _, want := range levels {
		s := want.String()
		got, err := ParseTrustLevel(s)
		if err != nil {
			t.Errorf("ParseTrustLevel(%q) error: %v", s, err)
			continue
		}
		if got != want {
			t.Errorf("ParseTrustLevel(String(%s)) = %s, want %s", want, got, want)
		}
	}
}

// TestTrustLevelIsValid verifies IsValid() for known levels.
func TestTrustLevelIsValid(t *testing.T) {
	if !FULL.IsValid() {
		t.Error("FULL.IsValid() = false, want true")
	}
	if !SEMI.IsValid() {
		t.Error("SEMI.IsValid() = false, want true")
	}
	if TrustLevelUnknown.IsValid() {
		t.Error("TrustLevelUnknown.IsValid() = true, want false")
	}
}

// TestParseUnknownTrustLevel verifies that an unrecognised label returns an error.
func TestParseUnknownTrustLevel(t *testing.T) {
	_, err := ParseTrustLevel("SUPERTRUSTLEVEL")
	if err == nil {
		t.Error("expected error for unknown trust level label, got nil")
	}
}
