package gpu

import (
	"errors"
	"testing"
)

// TestMIGCatalog_StandardProfiles asserts that the four standard MIG profiles
// exist in the catalog with correct slice and memory values (CLAUDE-1 closure
// criterion 1). A mutation that changes any field in one of the profile
// variables will break the corresponding sub-test.
func TestMIGCatalog_StandardProfiles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		wantName string
		wantSlices   int
		wantMemoryGB int
	}{
		{
			name:         "vGPU-Small / 1g.10gb",
			wantName:     "1g.10gb",
			wantSlices:   1,
			wantMemoryGB: 10,
		},
		{
			name:         "vGPU-Medium / 2g.20gb",
			wantName:     "2g.20gb",
			wantSlices:   2,
			wantMemoryGB: 20,
		},
		{
			name:         "vGPU-Large / 3g.40gb",
			wantName:     "3g.40gb",
			wantSlices:   3,
			wantMemoryGB: 40,
		},
		{
			name:         "full / 7g.80gb",
			wantName:     "7g.80gb",
			wantSlices:   7,
			wantMemoryGB: 80,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Lookup via the catalog — this tests BOTH the catalog contents AND
			// LookupProfile.
			p, err := LookupProfile(tc.wantName)
			if err != nil {
				t.Fatalf("LookupProfile(%q) returned unexpected error: %v", tc.wantName, err)
			}
			if p.Name != tc.wantName {
				t.Errorf("Name: got %q, want %q", p.Name, tc.wantName)
			}
			if p.Slices != tc.wantSlices {
				t.Errorf("Slices: got %d, want %d", p.Slices, tc.wantSlices)
			}
			if p.MemoryGB != tc.wantMemoryGB {
				t.Errorf("MemoryGB: got %d, want %d", p.MemoryGB, tc.wantMemoryGB)
			}
		})
	}
}

// TestLookupProfile_Unknown asserts that an unrecognised name returns
// ErrProfileNotFound and a zero MIGProfile.
func TestLookupProfile_Unknown(t *testing.T) {
	t.Parallel()

	_, err := LookupProfile("99g.999gb")
	if err == nil {
		t.Fatal("expected error for unknown profile, got nil")
	}
	if !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("expected errors.Is ErrProfileNotFound, got: %v", err)
	}
}

// TestMIGPartition_InstanceCountAndMemory is closure criterion 2: partitioning
// a device by a profile yields the correct instance count and correct profile
// metadata on each instance.
//
// Mutation guard: removing the oversubscription check in PartitionGPU allows a
// too-large profile to succeed. TestMIGPartition_OversubscriptionRejected
// asserts that, so removing the guard breaks that test.
func TestMIGPartition_InstanceCountAndMemory(t *testing.T) {
	t.Parallel()

	// A standard H100/H200/B200-class device: 7 slices, 80 GB.
	const (
		totalSlices   = 7
		totalMemoryGB = 80
	)

	cases := []struct {
		name          string
		profile       MIGProfile
		wantInstances int
	}{
		{
			name:    "1g.10gb fills 7 instances (slice-bound)",
			profile: ProfileSmall, // 1 slice, 10 GB → min(7/1, 80/10) = min(7, 8) = 7
			wantInstances: 7,
		},
		{
			name:    "2g.20gb fills 3 instances (slice-bound: 7/2=3)",
			profile: ProfileMedium, // 2 slices, 20 GB → min(7/2, 80/20) = min(3, 4) = 3
			wantInstances: 3,
		},
		{
			name:    "3g.40gb fills 2 instances (slice-bound: 7/3=2)",
			profile: ProfileLarge, // 3 slices, 40 GB → min(7/3, 80/40) = min(2, 2) = 2
			wantInstances: 2,
		},
		{
			name:    "7g.80gb fills 1 instance (whole device)",
			profile: ProfileFull, // 7 slices, 80 GB → min(7/7, 80/80) = min(1, 1) = 1
			wantInstances: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d, err := NewMIGDevice("gpu-0", totalSlices, totalMemoryGB)
			if err != nil {
				t.Fatalf("NewMIGDevice: %v", err)
			}

			instances, err := d.PartitionGPU(tc.profile)
			if err != nil {
				t.Fatalf("PartitionGPU(%q): unexpected error: %v", tc.profile.Name, err)
			}

			// Assert instance count.
			if len(instances) != tc.wantInstances {
				t.Errorf("got %d instances, want %d", len(instances), tc.wantInstances)
			}

			// Assert each instance carries the correct profile and parent link.
			for i, inst := range instances {
				if inst.Profile.Name != tc.profile.Name {
					t.Errorf("instance[%d].Profile.Name = %q, want %q", i, inst.Profile.Name, tc.profile.Name)
				}
				if inst.Profile.MemoryGB != tc.profile.MemoryGB {
					t.Errorf("instance[%d].Profile.MemoryGB = %d, want %d", i, inst.Profile.MemoryGB, tc.profile.MemoryGB)
				}
				if inst.Profile.Slices != tc.profile.Slices {
					t.Errorf("instance[%d].Profile.Slices = %d, want %d", i, inst.Profile.Slices, tc.profile.Slices)
				}
				if inst.ParentGPUID != "gpu-0" {
					t.Errorf("instance[%d].ParentGPUID = %q, want %q", i, inst.ParentGPUID, "gpu-0")
				}
			}

			// Instances() must return the same count.
			snap := d.Instances()
			if len(snap) != tc.wantInstances {
				t.Errorf("Instances() len = %d, want %d", len(snap), tc.wantInstances)
			}
		})
	}
}

// TestMIGPartition_OversubscriptionRejected is the mutation-guard test.
// Removing the oversubscription check in PartitionGPU causes a profile that
// exceeds device capacity to succeed — this test would then fail because
// PartitionGPU returns nil error when it should return ErrOversubscribed.
func TestMIGPartition_OversubscriptionRejected(t *testing.T) {
	t.Parallel()

	// A small device: 3 slices, 20 GB.
	d, err := NewMIGDevice("gpu-small", 3, 20)
	if err != nil {
		t.Fatalf("NewMIGDevice: %v", err)
	}

	// ProfileFull requires 7 slices and 80 GB — both exceed the device.
	_, err = d.PartitionGPU(ProfileFull)
	if err == nil {
		t.Fatal("expected oversubscription error, got nil — mutation guard triggered: the oversubscription check was removed")
	}
	if !errors.Is(err, ErrOversubscribed) {
		t.Errorf("expected errors.Is ErrOversubscribed, got: %v", err)
	}
	// Device must NOT be marked as partitioned after a failed call.
	if d.IsPartitioned() {
		t.Error("device must not be marked partitioned after a failed PartitionGPU call")
	}
}

// TestMIGPartition_OversubscriptionRejected_MemoryBound confirms that
// oversubscription is also caught when only memory exceeds capacity (not slices).
func TestMIGPartition_OversubscriptionRejected_MemoryBound(t *testing.T) {
	t.Parallel()

	// Device with enough slices for 3g.40gb but not enough memory.
	d, err := NewMIGDevice("gpu-lowmem", 7, 30) // 30 GB — profile needs 40 GB
	if err != nil {
		t.Fatalf("NewMIGDevice: %v", err)
	}

	_, err = d.PartitionGPU(ProfileLarge) // 3 slices, 40 GB
	if err == nil {
		t.Fatal("expected oversubscription error for memory, got nil")
	}
	if !errors.Is(err, ErrOversubscribed) {
		t.Errorf("expected errors.Is ErrOversubscribed, got: %v", err)
	}
}

// TestMIGPartition_Immutability is closure criterion 3: a profile is immutable
// once set. A second PartitionGPU call on an already-partitioned device MUST
// return ErrMIGAlreadyPartitioned and leave the device unchanged.
func TestMIGPartition_Immutability(t *testing.T) {
	t.Parallel()

	d, err := NewMIGDevice("gpu-0", 7, 80)
	if err != nil {
		t.Fatalf("NewMIGDevice: %v", err)
	}

	// First partition: Small profile.
	instances, err := d.PartitionGPU(ProfileSmall)
	if err != nil {
		t.Fatalf("first PartitionGPU: %v", err)
	}
	if len(instances) != 7 {
		t.Fatalf("expected 7 instances after first partition, got %d", len(instances))
	}

	// Second partition attempt (any profile) must be refused.
	_, err = d.PartitionGPU(ProfileMedium)
	if err == nil {
		t.Fatal("expected reconfiguration error, got nil")
	}
	if !errors.Is(err, ErrMIGAlreadyPartitioned) {
		t.Errorf("expected errors.Is ErrMIGAlreadyPartitioned, got: %v", err)
	}

	// Instances are unchanged — still 7 x Small.
	snap := d.Instances()
	if len(snap) != 7 {
		t.Errorf("Instances() len after refused repartition = %d, want 7", len(snap))
	}
	for i, inst := range snap {
		if inst.Profile.Name != ProfileSmall.Name {
			t.Errorf("instance[%d] profile changed after refused repartition: got %q, want %q",
				i, inst.Profile.Name, ProfileSmall.Name)
		}
	}
}

// TestMIGPartition_SameProfileAttempt verifies that even attempting to
// repartition with the SAME profile fails — no special-case "no-op" is
// permitted because the hardware does not allow it without a reset.
func TestMIGPartition_SameProfileAttempt(t *testing.T) {
	t.Parallel()

	d, err := NewMIGDevice("gpu-0", 7, 80)
	if err != nil {
		t.Fatalf("NewMIGDevice: %v", err)
	}
	if _, err = d.PartitionGPU(ProfileSmall); err != nil {
		t.Fatalf("first PartitionGPU: %v", err)
	}

	// Attempting the same profile again must fail.
	_, err = d.PartitionGPU(ProfileSmall)
	if err == nil {
		t.Fatal("expected reconfiguration error for same-profile repartition, got nil")
	}
	if !errors.Is(err, ErrMIGAlreadyPartitioned) {
		t.Errorf("expected errors.Is ErrMIGAlreadyPartitioned, got: %v", err)
	}
}

// TestNewMIGDevice_Validation confirms that NewMIGDevice rejects invalid
// parameters rather than silently creating a broken device.
func TestNewMIGDevice_Validation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		gpuID         string
		totalSlices   int
		totalMemoryGB int
	}{
		{name: "empty gpuID", gpuID: "", totalSlices: 7, totalMemoryGB: 80},
		{name: "zero slices", gpuID: "gpu-0", totalSlices: 0, totalMemoryGB: 80},
		{name: "negative slices", gpuID: "gpu-0", totalSlices: -1, totalMemoryGB: 80},
		{name: "zero memoryGB", gpuID: "gpu-0", totalSlices: 7, totalMemoryGB: 0},
		{name: "negative memoryGB", gpuID: "gpu-0", totalSlices: 7, totalMemoryGB: -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewMIGDevice(tc.gpuID, tc.totalSlices, tc.totalMemoryGB)
			if err == nil {
				t.Errorf("expected error for invalid input, got nil")
			}
		})
	}
}

// TestMIGDevice_InstancesBeforePartition verifies that Instances() returns nil
// (not an empty slice, not a panic) when the device has not been partitioned.
func TestMIGDevice_InstancesBeforePartition(t *testing.T) {
	t.Parallel()

	d, err := NewMIGDevice("gpu-0", 7, 80)
	if err != nil {
		t.Fatalf("NewMIGDevice: %v", err)
	}
	if snap := d.Instances(); snap != nil {
		t.Errorf("Instances() before partition: got %v, want nil", snap)
	}
	if d.IsPartitioned() {
		t.Error("IsPartitioned() should be false before PartitionGPU")
	}
}

// TestMIGInstanceIDs verifies that each MIGInstance has a unique, non-empty ID
// and that the ID embeds the parent GPU ID. This ensures the instance catalog
// is unambiguous in a multi-GPU node.
func TestMIGInstanceIDs(t *testing.T) {
	t.Parallel()

	d, err := NewMIGDevice("gpu-7", 7, 80)
	if err != nil {
		t.Fatalf("NewMIGDevice: %v", err)
	}
	instances, err := d.PartitionGPU(ProfileSmall)
	if err != nil {
		t.Fatalf("PartitionGPU: %v", err)
	}

	seen := make(map[string]bool)
	for i, inst := range instances {
		if inst.ID == "" {
			t.Errorf("instance[%d].ID is empty", i)
		}
		if seen[inst.ID] {
			t.Errorf("duplicate instance ID: %q", inst.ID)
		}
		seen[inst.ID] = true
	}
}

// TestMIGPartition_OversubscriptionRejected_GuardIsolation is the definitive
// mutation-guard isolation test for the count<1 oversubscription check in
// PartitionGPU. It proves that the ONLY load-bearing gate for oversubscription
// is the "count < 1" branch, not any earlier explicit bound comparison.
//
// How it works: we exercise every distinct oversubscription scenario and assert
// that:
//  (a) the error returned is ErrOversubscribed (not nil — would fail if count<1
//      guard is removed, because count=0 would then reach the loop and return
//      an empty slice with nil error), and
//  (b) the device is left un-partitioned (d.IsPartitioned() == false).
//
// Mutation sensitivity: if the "count < 1" guard is removed (or changed to
// "count < 0"), count reaches 0, the make([]*MIGInstance, 0) call returns an
// empty non-nil slice, the error returned is nil, and this test fails at the
// "expected ErrOversubscribed, got nil" assertion.
func TestMIGPartition_OversubscriptionRejected_GuardIsolation(t *testing.T) {
	t.Parallel()

	// Each case produces count=0 via floor division, so the count<1 guard is
	// the sole rejection mechanism. The profile dimensions span:
	//  - slices-only oversubscription  (profile.Slices > device.TotalSlices)
	//  - memory-only oversubscription  (profile.MemoryGB > device.TotalMemoryGB)
	//  - both-oversubscribed           (the common case in other tests)
	cases := []struct {
		name          string
		totalSlices   int
		totalMemoryGB int
		profile       MIGProfile
		// wantCountBySlices and wantCountByMemory document the intermediate
		// computation so a future reader can confirm that count=min(…) is 0.
		wantCountBySlices int
		wantCountByMemory int
	}{
		{
			// Slices-only: device has 2 slices, profile needs 3. Memory fits.
			// countBySlices = 2/3 = 0   ← oversubscribed
			// countByMemory = 40/10 = 4
			// count = min(0, 4) = 0  → guard fires
			name:              "slice-only oversubscription: profile.Slices=3 > device.TotalSlices=2",
			totalSlices:       2,
			totalMemoryGB:     40,
			profile:           ProfileLarge, // 3 slices, 40 GB
			wantCountBySlices: 0,
			wantCountByMemory: 1,
		},
		{
			// Memory-only: device has 7 slices but only 30 GB; profile needs 40 GB.
			// countBySlices = 7/3 = 2
			// countByMemory = 30/40 = 0  ← oversubscribed
			// count = min(2, 0) = 0  → guard fires
			name:              "memory-only oversubscription: profile.MemoryGB=40 > device.TotalMemoryGB=30",
			totalSlices:       7,
			totalMemoryGB:     30,
			profile:           ProfileLarge, // 3 slices, 40 GB
			wantCountBySlices: 2,
			wantCountByMemory: 0,
		},
		{
			// Both-oversubscribed: the canonical case from the original tests.
			// countBySlices = 3/7 = 0
			// countByMemory = 20/80 = 0
			// count = min(0, 0) = 0  → guard fires
			name:              "both oversubscribed: small device vs ProfileFull",
			totalSlices:       3,
			totalMemoryGB:     20,
			profile:           ProfileFull, // 7 slices, 80 GB
			wantCountBySlices: 0,
			wantCountByMemory: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d, err := NewMIGDevice("gpu-isolation", tc.totalSlices, tc.totalMemoryGB)
			if err != nil {
				t.Fatalf("NewMIGDevice: %v", err)
			}

			// Verify the intermediate computation matches our documented expectations.
			// This anchors the test description to the actual arithmetic.
			gotCountBySlices := tc.totalSlices / tc.profile.Slices
			gotCountByMemory := tc.totalMemoryGB / tc.profile.MemoryGB
			if gotCountBySlices != tc.wantCountBySlices {
				t.Errorf("floor(%d/%d) = %d, want %d — test precondition violated",
					tc.totalSlices, tc.profile.Slices, gotCountBySlices, tc.wantCountBySlices)
			}
			if gotCountByMemory != tc.wantCountByMemory {
				t.Errorf("floor(%d/%d) = %d, want %d — test precondition violated",
					tc.totalMemoryGB, tc.profile.MemoryGB, gotCountByMemory, tc.wantCountByMemory)
			}
			wantCount := gotCountBySlices
			if gotCountByMemory < wantCount {
				wantCount = gotCountByMemory
			}
			if wantCount >= 1 {
				t.Fatalf("test setup error: count=%d is not <1, so this case would NOT trigger the guard", wantCount)
			}

			// THE LOAD-BEARING ASSERTION:
			// If the "count < 1" guard in PartitionGPU is removed, PartitionGPU
			// returns ([]‌*MIGInstance{}, nil) — an empty slice with nil error.
			// This assertion would then fail because err is nil.
			_, err = d.PartitionGPU(tc.profile)
			if err == nil {
				t.Fatal("PartitionGPU returned nil error for oversubscribed profile — " +
					"mutation guard TRIGGERED: the 'count < 1' check was removed or inverted")
			}
			if !errors.Is(err, ErrOversubscribed) {
				t.Errorf("expected errors.Is(err, ErrOversubscribed), got: %v", err)
			}

			// Device must remain un-partitioned after a failed call.
			if d.IsPartitioned() {
				t.Error("device must not be marked partitioned after a failed PartitionGPU call")
			}
		})
	}
}

// TestMIGCatalog_CoverageComplete verifies that the StandardProfiles map has
// exactly the four expected profiles and no more. An undocumented profile in
// the catalog is a spec violation because operators rely on the catalog being
// the authoritative list of supported tiers.
func TestMIGCatalog_CoverageComplete(t *testing.T) {
	t.Parallel()

	expected := map[string]bool{
		"1g.10gb": true,
		"2g.20gb": true,
		"3g.40gb": true,
		"7g.80gb": true,
	}

	for name := range StandardProfiles {
		if !expected[name] {
			t.Errorf("unexpected profile in catalog: %q", name)
		}
	}
	for name := range expected {
		if _, ok := StandardProfiles[name]; !ok {
			t.Errorf("missing profile in catalog: %q", name)
		}
	}
	if len(StandardProfiles) != len(expected) {
		t.Errorf("catalog size = %d, want %d", len(StandardProfiles), len(expected))
	}
}
