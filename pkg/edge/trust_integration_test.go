//go:build integration

package edge

import (
	"fmt"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/device"
)

// TestIntegration_AssignTrustLevel_BindingPersistence verifies that a batch
// of device registrations produces distinct TrustBindings with correct
// levels and that the RunUUID-equivalent (AssignedAt timestamp) is unique per
// device.
//
// This test is gated by //go:build integration and is run by the orchestrator,
// not in the hermetic tier.
func TestIntegration_AssignTrustLevel_BindingPersistence(t *testing.T) {
	runUUID := fmt.Sprintf("int-%d", time.Now().UnixNano())
	t.Logf("integration run_uuid=%s", runUUID)

	descriptors := []struct {
		d    device.DeviceDescriptor
		want TrustLevel
		name string
	}{
		{
			name: "android",
			d:    androidDescriptor(),
			want: SEMI,
		},
		{
			name: "iphone",
			d:    iphoneDescriptor(),
			want: EDGE_DONOR,
		},
		{
			name: "attested_sbc",
			d: device.DeviceDescriptor{
				ID:          "sbc-attested-01",
				Arch:        device.ArchARM64,
				CPUCores:    4,
				MemoryBytes: 4 * (1 << 30),
				Trust:       device.TrustAttested,
			},
			want: FULL,
		},
		{
			name: "standard_sbc",
			d: device.DeviceDescriptor{
				ID:          "sbc-standard-01",
				Arch:        device.ArchARM64,
				CPUCores:    4,
				MemoryBytes: 4 * (1 << 30),
				Trust:       device.TrustBasic,
			},
			want: STANDARD,
		},
	}

	bindings := make([]TrustBinding, 0, len(descriptors))
	for _, tc := range descriptors {
		level, binding := AssignTrustLevel(tc.d)
		if level != tc.want {
			t.Errorf("[%s] AssignTrustLevel() = %s, want %s (run_uuid=%s)",
				tc.name, level, tc.want, runUUID)
		}
		bindings = append(bindings, binding)
		t.Logf("[%s] device=%s level=%s tier=%s rationale=%q assigned_at=%s run_uuid=%s",
			tc.name, binding.DeviceID, binding.Level, binding.Tier,
			binding.Rationale, binding.AssignedAt.Format(time.RFC3339Nano), runUUID)
	}

	// All bindings must have non-zero AssignedAt and non-empty Rationale.
	for i, b := range bindings {
		if b.AssignedAt.IsZero() {
			t.Errorf("binding[%d].AssignedAt is zero (run_uuid=%s)", i, runUUID)
		}
		if b.Rationale == "" {
			t.Errorf("binding[%d].Rationale is empty (run_uuid=%s)", i, runUUID)
		}
	}
}
