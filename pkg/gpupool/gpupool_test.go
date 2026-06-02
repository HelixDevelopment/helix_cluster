package gpupool

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
)

// runUUID returns a random hex id for run correlation in logs.
func runUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("runUUID: %v", err)
	}
	return hex.EncodeToString(b)
}

// TestAllocate_TierWalkSkipsZeroCapacityTier1 covers clause 1 (HXC-1494):
// three providers in tiers 1/3/4 where the tier-1 provider reports ZERO free
// capacity and the tier-3 provider is healthy with capacity. Allocate must
// walk past the empty tier-1 provider and return the tier-3 provider.
//
// Mutation: if the tier walk does NOT skip the zero-capacity tier-1 provider
// (e.g. it returns the lowest-tier provider unconditionally, ignoring
// FreeCapacity), this test fails because the returned provider would be the
// tier-1 "local-empty" instead of "cloud-healthy".
func TestAllocate_TierWalkSkipsZeroCapacityTier1(t *testing.T) {
	id := runUUID(t)
	providers := []Provider{
		{ID: "local-empty", Tier: TierLocal, Capacity: 8, FreeCapacity: 0, Healthy: true},
		{ID: "cloud-healthy", Tier: TierCloud, Capacity: 16, FreeCapacity: 4, Healthy: true},
		{ID: "decentralized", Tier: TierDecentralized, Capacity: 32, FreeCapacity: 10, Healthy: true},
	}
	spec := WorkloadSpec{GPUCount: 2}

	t.Logf("run=%s BEFORE allocate: tier1=local-empty(free=0) tier3=cloud-healthy(free=4) tier4=decentralized(free=10) need=%d",
		id, spec.effectiveGPUCount())

	m := NewPoolManager(providers)
	got, err := m.Allocate(spec)
	if err != nil {
		t.Fatalf("run=%s Allocate returned error: %v", id, err)
	}

	t.Logf("run=%s AFTER allocate: selected provider=%s tier=%s free=%d", id, got.ID, got.Tier, got.FreeCapacity)

	// Derived expectation: tier-1 (free 0) is skipped, the next ascending tier
	// with free >= 2 is tier-3 "cloud-healthy".
	if got.ID != "cloud-healthy" {
		t.Fatalf("run=%s expected tier-3 provider cloud-healthy (tier-1 empty must be skipped), got %s (tier %s)",
			id, got.ID, got.Tier)
	}
	if got.Tier != TierCloud {
		t.Fatalf("run=%s expected selected tier=Cloud(3), got %s(%d)", id, got.Tier, got.Tier)
	}
	// Guard the skip explicitly: the empty tier-1 provider must never be returned.
	if got.FreeCapacity < spec.effectiveGPUCount() {
		t.Fatalf("run=%s selected provider has insufficient free capacity %d < %d",
			id, got.FreeCapacity, spec.effectiveGPUCount())
	}
}

// TestAllocate_NoProviderWhenAllExhausted proves the negative path: if no tier
// can satisfy the need, Allocate returns ErrNoProvider rather than a bogus
// provider. This also pins that a too-small (but nonzero) tier-1 is skipped.
func TestAllocate_NoProviderWhenAllExhausted(t *testing.T) {
	id := runUUID(t)
	providers := []Provider{
		{ID: "local-small", Tier: TierLocal, Capacity: 2, FreeCapacity: 1, Healthy: true},
		{ID: "cloud-down", Tier: TierCloud, Capacity: 8, FreeCapacity: 8, Healthy: false},
	}
	spec := WorkloadSpec{GPUCount: 4}

	t.Logf("run=%s BEFORE allocate: local-small(free=1,healthy) cloud-down(free=8,UNHEALTHY) need=4", id)

	m := NewPoolManager(providers)
	got, err := m.Allocate(spec)

	t.Logf("run=%s AFTER allocate: err=%v providerID=%q", id, err, got.ID)

	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("run=%s expected ErrNoProvider (local too small, cloud unhealthy), got err=%v provider=%s",
			id, err, got.ID)
	}
}

// TestFilter_ExcludesUnderVRAMAndUnlabeled covers clause 2 (HXC-1496):
// a WorkloadSpec requiring a TEE label + 40GiB VRAM filters a mixed device set
// so only TEE-labeled, sufficiently-large devices pass. An under-VRAM device
// (even if TEE-labeled) is excluded, and a large device lacking TEE is excluded.
//
// Mutation: if the VRAM filter is dropped, "tee-small" (TEE but only 24GiB)
// would incorrectly pass, failing this test. If the label filter is dropped,
// "big-no-tee" would pass, also failing.
func TestFilter_ExcludesUnderVRAMAndUnlabeled(t *testing.T) {
	id := runUUID(t)
	devices := []Device{
		{ID: "tee-big-a", Tier: TierLocal, VRAMGiB: 80, Labels: map[string]bool{"TEE": true}},
		{ID: "tee-small", Tier: TierLocal, VRAMGiB: 24, Labels: map[string]bool{"TEE": true}}, // under VRAM
		{ID: "big-no-tee", Tier: TierCloud, VRAMGiB: 48, Labels: map[string]bool{}},           // no TEE
		{ID: "tee-big-b", Tier: TierCloud, VRAMGiB: 40, Labels: map[string]bool{"TEE": true}}, // exactly at threshold
	}
	spec := WorkloadSpec{MinVRAMGiB: 40, RequireLabels: []string{"TEE"}}

	t.Logf("run=%s BEFORE filter: %d devices, require TEE + >=40GiB", id, len(devices))

	got := Filter(spec, devices)

	gotIDs := make([]string, len(got))
	for i, d := range got {
		gotIDs[i] = d.ID
	}
	t.Logf("run=%s AFTER filter: survivors=%v", id, gotIDs)

	// Derived expectation: only tee-big-a (80) and tee-big-b (40, at threshold) qualify.
	want := map[string]bool{"tee-big-a": true, "tee-big-b": true}
	if len(got) != len(want) {
		t.Fatalf("run=%s expected %d survivors %v, got %d %v", id, len(want), want, len(got), gotIDs)
	}
	for _, d := range got {
		if !want[d.ID] {
			t.Fatalf("run=%s unexpected survivor %s (VRAM=%d labels=%v) passed filter", id, d.ID, d.VRAMGiB, d.Labels)
		}
		if d.VRAMGiB < spec.MinVRAMGiB {
			t.Fatalf("run=%s survivor %s has VRAM %d < min %d — VRAM filter not applied", id, d.ID, d.VRAMGiB, spec.MinVRAMGiB)
		}
		if !d.Labels["TEE"] {
			t.Fatalf("run=%s survivor %s lacks required TEE label — label filter not applied", id, d.ID)
		}
	}
	// Explicit exclusion guards.
	for _, d := range got {
		if d.ID == "tee-small" {
			t.Fatalf("run=%s under-VRAM device tee-small(24GiB) must be excluded but passed", id)
		}
		if d.ID == "big-no-tee" {
			t.Fatalf("run=%s non-TEE device big-no-tee must be excluded but passed", id)
		}
	}
}

// TestSelect_TierThenUtilizationThenCost covers clause 3 (HXC-1498):
// candidates span two tiers with varying utilization. The local-tier
// LEAST-UTILIZED device must be chosen first; the ordering is tier > util > cost.
//
// Mutation: if ordering is done by cost before tier, the cheap cloud device
// "cloud-cheap" (cost 0.10) would be selected instead of the local device,
// failing this test. If utilization is ignored within the local tier, the
// more-utilized "local-busy" could win over "local-idle".
func TestSelect_TierThenUtilizationThenCost(t *testing.T) {
	id := runUUID(t)
	candidates := []Device{
		// Cloud tier: cheapest of all but a WORSE tier — must lose to local.
		{ID: "cloud-cheap", Tier: TierCloud, Utilization: 0.05, CostPerHour: 0.10},
		// Local tier, more utilized + more expensive.
		{ID: "local-busy", Tier: TierLocal, Utilization: 0.80, CostPerHour: 5.00},
		// Local tier, least utilized — expected winner despite higher cost than cloud.
		{ID: "local-idle", Tier: TierLocal, Utilization: 0.10, CostPerHour: 3.00},
	}

	t.Logf("run=%s BEFORE select: cloud-cheap(tier=Cloud,util=0.05,cost=0.10) local-busy(tier=Local,util=0.80,cost=5.00) local-idle(tier=Local,util=0.10,cost=3.00)", id)

	s := NewPriorityScheduler()
	got, err := s.Select(candidates)
	if err != nil {
		t.Fatalf("run=%s Select returned error: %v", id, err)
	}

	t.Logf("run=%s AFTER select: chosen=%s tier=%s util=%.2f cost=%.2f", id, got.ID, got.Tier, got.Utilization, got.CostPerHour)

	// Derived expectation: tier dominates — local beats cloud; within local,
	// least utilization wins => local-idle.
	if got.ID != "local-idle" {
		t.Fatalf("run=%s expected local-idle (local tier + least util), got %s (tier %s, util %.2f, cost %.2f) — ordering must be tier>util>cost",
			id, got.ID, got.Tier, got.Utilization, got.CostPerHour)
	}
	if got.Tier != TierLocal {
		t.Fatalf("run=%s tier ordering violated: chosen tier=%s, expected Local (cheaper cloud must NOT win)", id, got.Tier)
	}
}

// TestSelect_CostBreaksTieWithinSameTierAndUtil pins the third-level tie-break:
// when two same-tier devices have identical utilization, the cheaper one wins.
// This proves cost is actually consulted (not dead code).
//
// Mutation: if cost is ignored, the stable-sort would keep input order and pick
// the more expensive "same-pricey" first, failing this test.
func TestSelect_CostBreaksTieWithinSameTierAndUtil(t *testing.T) {
	id := runUUID(t)
	candidates := []Device{
		{ID: "same-pricey", Tier: TierLocal, Utilization: 0.30, CostPerHour: 9.00},
		{ID: "same-cheap", Tier: TierLocal, Utilization: 0.30, CostPerHour: 1.00},
	}

	t.Logf("run=%s BEFORE select: same-pricey(cost=9.00) same-cheap(cost=1.00), equal tier+util", id)

	s := NewPriorityScheduler()
	got, err := s.Select(candidates)
	if err != nil {
		t.Fatalf("run=%s Select error: %v", id, err)
	}

	t.Logf("run=%s AFTER select: chosen=%s cost=%.2f", id, got.ID, got.CostPerHour)

	if got.ID != "same-cheap" {
		t.Fatalf("run=%s expected same-cheap (cost tie-break), got %s cost=%.2f", id, got.ID, got.CostPerHour)
	}
}

// TestSelect_EmptyCandidates proves the empty-set negative path.
func TestSelect_EmptyCandidates(t *testing.T) {
	id := runUUID(t)
	t.Logf("run=%s BEFORE select: 0 candidates", id)
	s := NewPriorityScheduler()
	_, err := s.Select(nil)
	t.Logf("run=%s AFTER select: err=%v", id, err)
	if !errors.Is(err, ErrNoDevice) {
		t.Fatalf("run=%s expected ErrNoDevice for empty candidates, got %v", id, err)
	}
}
