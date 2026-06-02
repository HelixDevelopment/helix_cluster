package deviceplugin

import (
	"errors"
	"testing"
)

// healthyGPU is a helper building a healthy rtx3080 device for inventory tests.
func healthyGPU(id string) Device {
	return Device{
		ID:                 id,
		Model:              "rtx3080",
		MemoryBytes:        10 << 30,
		DriverVersion:      "550.00",
		PCIeBandwidth:      "16GT/s",
		Capabilities:       []string{"fp16", "tensor-cores"},
		Health:             HealthHealthy,
		UtilizationPercent: 0,
	}
}

// TestAllocate_OversubscriptionRejected is closure criterion #3 and the mutation
// guard for the oversubscription check.
//
// Inventory of 2 rtx3080 -> allocating 2 succeeds; a subsequent request for 1
// more must be REJECTED with ErrOversubscription, and allocatable inventory is
// captured before (2) and after (0). If the `len(free) < req.Count` guard in
// Allocate is removed or inverted, the second allocation would (wrongly) succeed
// and this test fails.
func TestAllocate_OversubscriptionRejected(t *testing.T) {
	reg := NewRegistry()
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "nvidia-gpu",
		ResourceKind: "gpu",
		Devices:      []Device{healthyGPU("gpu-0"), healthyGPU("gpu-1")},
	}); err != nil {
		t.Fatalf("ApplyFingerprint: %v", err)
	}

	// Allocatable BEFORE: 2.
	if got := reg.Allocatable("rtx3080"); got != 2 {
		t.Fatalf("allocatable before: want 2, got %d", got)
	}

	req := Resource{Kind: "gpu", Model: "rtx3080", Count: 2}
	alloc, err := reg.Allocate(req)
	if err != nil {
		t.Fatalf("first Allocate(2) failed: %v", err)
	}
	if len(alloc.DeviceIDs) != 2 {
		t.Fatalf("want 2 device IDs allocated, got %v", alloc.DeviceIDs)
	}

	// Allocatable AFTER full allocation: 0.
	if got := reg.Allocatable("rtx3080"); got != 0 {
		t.Fatalf("allocatable after: want 0, got %d", got)
	}

	// Now request 1 MORE -> must be rejected as oversubscription.
	_, err = reg.Allocate(Resource{Kind: "gpu", Model: "rtx3080", Count: 1})
	if err == nil {
		t.Fatal("oversubscribed Allocate(1) succeeded; want ErrOversubscription")
	}
	if !errors.Is(err, ErrOversubscription) {
		t.Fatalf("want ErrOversubscription, got %v", err)
	}

	// Inventory unchanged on rejection; still 0 allocatable, 2 total known.
	if got := reg.Allocatable("rtx3080"); got != 0 {
		t.Fatalf("allocatable after rejection: want 0, got %d", got)
	}
	if got := len(reg.Inventory()); got != 2 {
		t.Fatalf("total inventory: want 2, got %d", got)
	}
}

// TestAllocate_ReleaseRestoresInventory proves released devices become
// allocatable again, exercising the atomic reserve/release lifecycle.
func TestAllocate_ReleaseRestoresInventory(t *testing.T) {
	reg := NewRegistry()
	_ = reg.ApplyFingerprint(Fingerprint{
		PluginName: "nvidia-gpu",
		Devices:    []Device{healthyGPU("gpu-0"), healthyGPU("gpu-1")},
	})
	alloc, err := reg.Allocate(Resource{Kind: "gpu", Model: "rtx3080", Count: 2})
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if reg.Allocatable("rtx3080") != 0 {
		t.Fatal("expected 0 allocatable after full allocation")
	}
	if freed := reg.Release(alloc.DeviceIDs...); freed != 2 {
		t.Fatalf("Release reported %d freed, want 2", freed)
	}
	if got := reg.Allocatable("rtx3080"); got != 2 {
		t.Fatalf("after release want 2 allocatable, got %d", got)
	}
}

// TestAllocate_UnhealthyNotAllocatable proves unhealthy devices are excluded
// from the allocatable pool — a mutation removing the health filter would let an
// allocation of 2 succeed against 1 healthy + 1 unhealthy device.
func TestAllocate_UnhealthyNotAllocatable(t *testing.T) {
	reg := NewRegistry()
	bad := healthyGPU("gpu-1")
	bad.Health = HealthUnhealthy
	_ = reg.ApplyFingerprint(Fingerprint{
		PluginName: "nvidia-gpu",
		Devices:    []Device{healthyGPU("gpu-0"), bad},
	})

	if got := reg.Allocatable("rtx3080"); got != 1 {
		t.Fatalf("allocatable with one unhealthy: want 1, got %d", got)
	}
	if _, err := reg.Allocate(Resource{Kind: "gpu", Model: "rtx3080", Count: 2}); !errors.Is(err, ErrOversubscription) {
		t.Fatalf("want oversubscription against single healthy device, got %v", err)
	}
}

// TestAllocate_Errors covers the typed-error paths.
func TestAllocate_Errors(t *testing.T) {
	reg := NewRegistry()
	_ = reg.ApplyFingerprint(Fingerprint{PluginName: "nvidia-gpu", Devices: []Device{healthyGPU("gpu-0")}})

	if _, err := reg.Allocate(Resource{Kind: "gpu", Model: "a100", Count: 1}); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("want ErrUnknownModel, got %v", err)
	}
	if _, err := reg.Allocate(Resource{Kind: "memory", Quantity: "10Gi"}); !errors.Is(err, ErrUnsupportedRequest) {
		t.Fatalf("want ErrUnsupportedRequest, got %v", err)
	}
	if _, err := reg.Allocate(Resource{Kind: "gpu", Model: "rtx3080", Count: 0}); err == nil {
		t.Fatal("want error for zero count")
	}
}

// TestApplyFingerprint_RejectsEmptyID guards the ID validation branch.
func TestApplyFingerprint_RejectsEmptyID(t *testing.T) {
	reg := NewRegistry()
	err := reg.ApplyFingerprint(Fingerprint{
		PluginName: "nvidia-gpu",
		Devices:    []Device{{Model: "rtx3080", Health: HealthHealthy}},
	})
	if err == nil {
		t.Fatal("want error for empty device ID")
	}
}

// TestApplyFingerprint_RejectsEmptyPluginName guards the owner-identity branch:
// without a plugin identity we cannot scope pruning, so an empty PluginName must
// be rejected. Inverting/removing that check would let an un-ownable fingerprint
// in and silently break reconciliation.
func TestApplyFingerprint_RejectsEmptyPluginName(t *testing.T) {
	reg := NewRegistry()
	err := reg.ApplyFingerprint(Fingerprint{
		Devices: []Device{healthyGPU("gpu-0")},
	})
	if err == nil {
		t.Fatal("want error for empty PluginName")
	}
	if reg.Count() != 0 {
		t.Fatalf("rejected fingerprint must not mutate inventory, got Count=%d", reg.Count())
	}
}

// TestApplyFingerprint_AtomicOnError is the partial-mutation guard.
//
// A fingerprint whose SECOND device has an empty ID must be rejected WHOLESALE:
// the first (valid) device must NOT have been written. This locks the
// validate-all-before-apply contract. If validation were per-device-as-you-go
// (the old behaviour), gpu-0 would already be in the registry when the error for
// the empty-ID device is returned, leaving the caller's "rejected" ack
// inconsistent with mutated inventory.
func TestApplyFingerprint_AtomicOnError(t *testing.T) {
	reg := NewRegistry()
	err := reg.ApplyFingerprint(Fingerprint{
		PluginName: "nvidia-gpu",
		Devices: []Device{
			healthyGPU("gpu-0"),                       // valid, appears first
			{Model: "rtx3080", Health: HealthHealthy}, // invalid: empty ID
		},
	})
	if err == nil {
		t.Fatal("want error for fingerprint containing an empty-ID device")
	}
	if _, ok := reg.Device("gpu-0"); ok {
		t.Fatal("partial mutation: gpu-0 was written despite the fingerprint being rejected")
	}
	if reg.Count() != 0 {
		t.Fatalf("registry must be unchanged after rejection, got Count=%d", reg.Count())
	}
}

// TestApplyFingerprint_RejectsDuplicateID guards the duplicate-detection branch
// of the pre-apply validation (a full device set must not list one ID twice).
func TestApplyFingerprint_RejectsDuplicateID(t *testing.T) {
	reg := NewRegistry()
	err := reg.ApplyFingerprint(Fingerprint{
		PluginName: "nvidia-gpu",
		Devices:    []Device{healthyGPU("gpu-0"), healthyGPU("gpu-0")},
	})
	if err == nil {
		t.Fatal("want error for duplicate device ID in one fingerprint")
	}
	if reg.Count() != 0 {
		t.Fatalf("registry must be unchanged after rejection, got Count=%d", reg.Count())
	}
}

// TestApplyFingerprint_PrunesVanishedDevice is the stale-device guard and the
// core CLAUDE-1 correctness fix: Fingerprint.Devices is the plugin's FULL
// current set, so a device dropped from a re-fingerprint (e.g. a GPU unplugged)
// must be pruned and must STOP being allocatable. If pruning is removed, gpu-1
// would linger forever and Allocate could hand out a device that no longer
// exists.
func TestApplyFingerprint_PrunesVanishedDevice(t *testing.T) {
	reg := NewRegistry()

	// Initial full set: two GPUs.
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName: "nvidia-gpu",
		Devices:    []Device{healthyGPU("gpu-0"), healthyGPU("gpu-1")},
	}); err != nil {
		t.Fatalf("initial ApplyFingerprint: %v", err)
	}
	if got := reg.Allocatable("rtx3080"); got != 2 {
		t.Fatalf("allocatable after initial fingerprint: want 2, got %d", got)
	}

	// gpu-1 is unplugged: the plugin's next FULL fingerprint reports only gpu-0.
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName: "nvidia-gpu",
		Devices:    []Device{healthyGPU("gpu-0")},
	}); err != nil {
		t.Fatalf("shrinking ApplyFingerprint: %v", err)
	}

	if got := reg.Count(); got != 1 {
		t.Fatalf("after pruning: want 1 known device, got %d", got)
	}
	if _, ok := reg.Device("gpu-1"); ok {
		t.Fatal("gpu-1 was not pruned after vanishing from the plugin's full fingerprint")
	}
	if got := reg.Allocatable("rtx3080"); got != 1 {
		t.Fatalf("allocatable after pruning: want 1, got %d", got)
	}

	// And a pruned device must never be allocatable: requesting 2 must now be
	// oversubscription against the single surviving device.
	if _, err := reg.Allocate(Resource{Kind: "gpu", Model: "rtx3080", Count: 2}); !errors.Is(err, ErrOversubscription) {
		t.Fatalf("want oversubscription after a device was pruned, got %v", err)
	}
}

// TestApplyFingerprint_PruneReleasesAllocation proves that pruning a device that
// was currently ALLOCATED also frees its allocation slot, so a later device
// re-appearing under the same ID is not wrongly considered still-allocated.
func TestApplyFingerprint_PruneReleasesAllocation(t *testing.T) {
	reg := NewRegistry()
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName: "nvidia-gpu",
		Devices:    []Device{healthyGPU("gpu-0"), healthyGPU("gpu-1")},
	}); err != nil {
		t.Fatalf("ApplyFingerprint: %v", err)
	}
	if _, err := reg.Allocate(Resource{Kind: "gpu", Model: "rtx3080", Count: 2}); err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	// Both unplugged; plugin reports an empty full set.
	if err := reg.ApplyFingerprint(Fingerprint{PluginName: "nvidia-gpu", Devices: nil}); err != nil {
		t.Fatalf("empty ApplyFingerprint: %v", err)
	}
	if reg.Count() != 0 {
		t.Fatalf("want 0 devices after both pruned, got %d", reg.Count())
	}

	// Re-plug both: they must be fully allocatable again (their old allocation
	// state was released by the prune, not left dangling).
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName: "nvidia-gpu",
		Devices:    []Device{healthyGPU("gpu-0"), healthyGPU("gpu-1")},
	}); err != nil {
		t.Fatalf("re-plug ApplyFingerprint: %v", err)
	}
	if got := reg.Allocatable("rtx3080"); got != 2 {
		t.Fatalf("re-plugged devices must be allocatable: want 2, got %d", got)
	}
}

// TestApplyFingerprint_PruneScopedToOwningPlugin proves reconciliation is scoped
// per plugin: a fingerprint from plugin B must never prune devices owned by
// plugin A.
func TestApplyFingerprint_PruneScopedToOwningPlugin(t *testing.T) {
	reg := NewRegistry()
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName: "plugin-a",
		Devices:    []Device{healthyGPU("a-gpu-0")},
	}); err != nil {
		t.Fatalf("plugin-a fingerprint: %v", err)
	}
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName: "plugin-b",
		Devices:    []Device{healthyGPU("b-gpu-0")},
	}); err != nil {
		t.Fatalf("plugin-b fingerprint: %v", err)
	}

	// plugin-b re-fingerprints with a different device; its old device is pruned,
	// but plugin-a's device must survive.
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName: "plugin-b",
		Devices:    []Device{healthyGPU("b-gpu-1")},
	}); err != nil {
		t.Fatalf("plugin-b re-fingerprint: %v", err)
	}

	if _, ok := reg.Device("a-gpu-0"); !ok {
		t.Fatal("plugin-a's device was wrongly pruned by a plugin-b fingerprint")
	}
	if _, ok := reg.Device("b-gpu-0"); ok {
		t.Fatal("plugin-b's old device should have been pruned")
	}
	if _, ok := reg.Device("b-gpu-1"); !ok {
		t.Fatal("plugin-b's new device should be present")
	}
}

// TestRelease_CountsOnlyOutstanding locks the documented Release semantics:
// only IDs that were actually allocated are counted; unknown / already-free IDs
// are ignored (idempotent) but NOT counted, so a caller can detect a wrong ID.
func TestRelease_CountsOnlyOutstanding(t *testing.T) {
	reg := NewRegistry()
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName: "nvidia-gpu",
		Devices:    []Device{healthyGPU("gpu-0"), healthyGPU("gpu-1")},
	}); err != nil {
		t.Fatalf("ApplyFingerprint: %v", err)
	}
	alloc, err := reg.Allocate(Resource{Kind: "gpu", Model: "rtx3080", Count: 1})
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	// Release the one real allocation plus a bogus ID: count must be 1, not 2.
	got := reg.Release(alloc.DeviceIDs[0], "does-not-exist")
	if got != 1 {
		t.Fatalf("Release count: want 1 (only the real allocation), got %d", got)
	}
	// Releasing the same ID again is a no-op and counts 0 (double-free visible).
	if got := reg.Release(alloc.DeviceIDs[0]); got != 0 {
		t.Fatalf("double Release count: want 0, got %d", got)
	}
}

// TestRegistry_Count guards the cheap Count() accessor against Inventory drift.
func TestRegistry_Count(t *testing.T) {
	reg := NewRegistry()
	if reg.Count() != 0 {
		t.Fatalf("empty registry Count: want 0, got %d", reg.Count())
	}
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName: "nvidia-gpu",
		Devices:    []Device{healthyGPU("gpu-0"), healthyGPU("gpu-1")},
	}); err != nil {
		t.Fatalf("ApplyFingerprint: %v", err)
	}
	if reg.Count() != len(reg.Inventory()) {
		t.Fatalf("Count (%d) must equal len(Inventory) (%d)", reg.Count(), len(reg.Inventory()))
	}
	if reg.Count() != 2 {
		t.Fatalf("Count after 2 devices: want 2, got %d", reg.Count())
	}
}
