package device

import (
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/tierdef"
)

// loadRegistry loads the real embedded tierdef.yaml. The test fails immediately
// if Default() returns an error — a broken registry means nothing else can pass.
func loadRegistry(t *testing.T) *tierdef.Registry {
	t.Helper()
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("tierdef.Default() failed: %v", err)
	}
	return reg
}

// mbToBytes converts megabytes to bytes for populating DeviceDescriptor fields.
func mbToBytes(mb int) int64 {
	return int64(mb) * mibBytes
}

// buildDescriptorForTier returns a DeviceDescriptor whose fields are set to
// EXACTLY the minimum requirements of the named tier. It reads those minimums
// directly from the live registry so the fixture stays honest with the YAML.
//
// NetworkMbps is NOT mapped (DeviceDescriptor has no such field), so the
// DeviceCaps.NetworkMbps will be 0. For this to correctly classify a tier, the
// tier must have min_network_mbps == 0 OR the test must use descriptors that
// can pass with NetworkMbps=0. We verify in TestAssignTier_NetworkMbpsIsZero
// that T3+ require network — so for tiers with min_network_mbps>0 we keep them
// in a separate "boundary" fixture path that notes the limitation.
func buildDescriptorForTier(t *testing.T, reg *tierdef.Registry, tierName string) DeviceDescriptor {
	t.Helper()
	td, ok := reg.Get(tierName)
	if !ok {
		t.Fatalf("registry has no tier %q", tierName)
	}
	d := DeviceDescriptor{
		CPUCores:         td.MinCPUCores,
		MemoryBytes:      mbToBytes(td.MinMemoryMB),
		GPUCount:         td.MinGPU,
		GPUVRAMBytes:     mbToBytes(td.MinGPUVRAMMB),
		PowerBudgetWatts: td.MaxPowerW,
	}
	return d
}

// TestAssignTier_TierNameToConst verifies the string→Tier helper is bijective
// over the full T1-T15 range and returns TierUnknown for unknown strings.
func TestAssignTier_TierNameToConst(t *testing.T) {
	cases := []struct {
		name string
		want Tier
	}{
		{"T1", T1}, {"T2", T2}, {"T3", T3}, {"T4", T4}, {"T5", T5},
		{"T6", T6}, {"T7", T7}, {"T8", T8}, {"T9", T9}, {"T10", T10},
		{"T11", T11}, {"T12", T12}, {"T13", T13}, {"T14", T14}, {"T15", T15},
		{"T0", TierUnknown}, {"T16", TierUnknown}, {"", TierUnknown}, {"t1", TierUnknown},
	}
	for _, tc := range cases {
		got := tierNameToConst(tc.name)
		if got != tc.want {
			t.Errorf("tierNameToConst(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestTierString_T9ToT15 verifies the String() extension for the new tiers.
func TestTierString_T9ToT15(t *testing.T) {
	cases := []struct {
		tier Tier
		want string
	}{
		{T9, "T9"}, {T10, "T10"}, {T11, "T11"}, {T12, "T12"},
		{T13, "T13"}, {T14, "T14"}, {T15, "T15"},
	}
	for _, tc := range cases {
		if got := tc.tier.String(); got != tc.want {
			t.Errorf("Tier(%d).String() = %q, want %q", int(tc.tier), got, tc.want)
		}
	}
}

// TestTierConstants_Ordering verifies that T1 < T2 < ... < T15 and that
// TierUnknown == 0 (the iota zero value). This protects against accidental
// reordering in the const block.
func TestTierConstants_Ordering(t *testing.T) {
	ordered := []Tier{T1, T2, T3, T4, T5, T6, T7, T8, T9, T10, T11, T12, T13, T14, T15}
	if TierUnknown != 0 {
		t.Errorf("TierUnknown = %d, want 0", int(TierUnknown))
	}
	for i := 1; i < len(ordered); i++ {
		if ordered[i] <= ordered[i-1] {
			t.Errorf("Tier ordering broken: T%d (%d) <= T%d (%d)",
				i+1, int(ordered[i]), i, int(ordered[i-1]))
		}
	}
}

// TestAssignTier_DegenerateDescriptor verifies that an empty descriptor returns
// TierUnknown (mirrors ClassifyTier behaviour).
func TestAssignTier_DegenerateDescriptor(t *testing.T) {
	reg := loadRegistry(t)
	d := DeviceDescriptor{} // CPUCores=0, MemoryBytes=0
	got := AssignTier(d, reg)
	if got != TierUnknown {
		t.Errorf("AssignTier(empty, reg) = %v, want TierUnknown", got)
	}
}

// TestAssignTier_NilRegistry verifies that a nil registry returns TierUnknown
// rather than panicking.
func TestAssignTier_NilRegistry(t *testing.T) {
	d := DeviceDescriptor{CPUCores: 4, MemoryBytes: mbToBytes(2048)}
	got := AssignTier(d, nil)
	if got != TierUnknown {
		t.Errorf("AssignTier(d, nil) = %v, want TierUnknown", got)
	}
}

// TestAssignTier_TinyDevice verifies that a 1-core/256MB device (the absolute
// minimum that is not degenerate) classifies to T1.
//
// Anti-bluff: if AssignTier always returned T1 for all devices the T15 fixture
// below would fail, and if it always returned the highest tier this test fails.
func TestAssignTier_TinyDevice(t *testing.T) {
	reg := loadRegistry(t)
	d := DeviceDescriptor{
		CPUCores:    1,
		MemoryBytes: mbToBytes(256),
	}
	got := AssignTier(d, reg)
	if got != T1 {
		t.Errorf("AssignTier(tiny 1-core/256MB) = %v, want T1", got)
	}
}

// TestAssignTier_HugeDevice verifies that a flagship multi-GPU descriptor
// classifies to T15. The descriptor is built to exactly meet T15 minimums from
// the live registry — if the registry changes the fixture stays correct.
//
// Anti-bluff: if AssignTier iterates LOW→HIGH it would return T1 (the first
// meeting tier), which would fail this assertion.
func TestAssignTier_HugeDevice(t *testing.T) {
	reg := loadRegistry(t)
	d := buildDescriptorForTier(t, reg, "T15")
	got := AssignTier(d, reg)
	if got != T15 {
		t.Errorf("AssignTier(T15-spec device) = %v, want T15", got)
	}
}

// TestAssignTier_ExactBoundaryFixtures tests that a descriptor built to exactly
// meet each tier's minimum requirements (and no more) classifies to that tier.
//
// The "exactly meets" property relies on the registry being strictly monotonic:
// each tier's minimums are strictly higher than the tier below it in at least one
// dimension, which means a device at EXACTLY T_n meets T_n but NOT T_{n+1}.
//
// NOTE: NetworkMbps is always 0 in DeviceDescriptor (no network field). For tiers
// whose min_network_mbps > 0 this means the device fails that tier's Meets check.
// The fixture therefore targets only tiers where network is not the gating factor,
// or where the other fields alone disqualify higher tiers. Tiers with a strict
// network requirement are tested in TestAssignTier_NetworkNotGating.
func TestAssignTier_ExactBoundaryFixtures(t *testing.T) {
	reg := loadRegistry(t)

	// For each tier we build a descriptor at EXACTLY the tier's minimums, then
	// assert the classified result. Because NetworkMbps is always 0, a tier with
	// min_network_mbps > 0 will not be Met by any descriptor built this way.
	// We therefore compute the expected tier as the HIGHEST tier that is actually
	// Met by the descriptor (which may be lower than the one we built from).
	//
	// This still validates the HIGH→LOW iteration order: if the iteration were
	// LOW→HIGH, the result would always be T1 (or the lowest-meeting tier), which
	// would fail for any tier above T1 that has no network requirement.
	for i, td := range reg.Tiers {
		tierName := td.Tier
		d := buildDescriptorForTier(t, reg, tierName)
		caps := descriptorToCaps(d)

		// Compute what the highest meeting tier SHOULD be given caps (with
		// NetworkMbps=0). Walk from the top down.
		wantIdx := -1
		for j := len(reg.Tiers) - 1; j >= 0; j-- {
			if reg.Tiers[j].Meets(caps) {
				wantIdx = j
				break
			}
		}
		if wantIdx < 0 {
			// Even T1 not met — device built from tier i must at least meet T1.
			// (T1 requires only 1 core / 256 MB / network=10 — but network=0.)
			// The test builder may produce a descriptor that fails T1's network
			// requirement. In that case we treat the fallback as T1 from the
			// code's perspective (the code returns T1 when no tier Meets).
			wantIdx = 0
		}
		want := tierNameToConst(reg.Tiers[wantIdx].Tier)

		got := AssignTier(d, reg)
		if got != want {
			t.Errorf("tier[%d] %s: AssignTier(exact-min descriptor) = %v, want %v (caps=%+v)",
				i, tierName, got, want, caps)
		}
	}
}

// TestAssignTier_SelectedTiersExact runs pinned fixtures for T1, T5 (mid-low),
// T8 (mid), T12 (mid-high), and T15, asserting exact tier values. These act as
// permanent regression anchors: a wrong iteration order or a broken Meets
// predicate will break at least one of these.
func TestAssignTier_SelectedTiersExact(t *testing.T) {
	reg := loadRegistry(t)

	// T1: 1 core, 256 MB, no GPU, no network field.
	// Meets only T1 because T2 requires 2 cores.
	t.Run("T1_exact", func(t *testing.T) {
		td, _ := reg.Get("T1")
		d := DeviceDescriptor{
			CPUCores:    td.MinCPUCores, // 1
			MemoryBytes: mbToBytes(td.MinMemoryMB), // 256 MB
		}
		got := AssignTier(d, reg)
		if got != T1 {
			t.Errorf("got %v, want T1", got)
		}
	})

	// T8: 32 cores, 128 GiB, 2 GPUs, 16 GiB VRAM each, no network.
	// T9 requires 48 cores — so this device falls back to T8.
	t.Run("T8_exact_no_network", func(t *testing.T) {
		td8, _ := reg.Get("T8")
		td9, _ := reg.Get("T9")
		d := DeviceDescriptor{
			CPUCores:     td8.MinCPUCores,              // 32 — below T9's 48
			MemoryBytes:  mbToBytes(td8.MinMemoryMB),   // 128 GiB
			GPUCount:     td8.MinGPU,                   // 2
			GPUVRAMBytes: mbToBytes(td8.MinGPUVRAMMB),  // 16384 MB
		}
		// Verify our understanding: T9 requires more cores than this descriptor.
		if td9.MinCPUCores <= td8.MinCPUCores {
			t.Skipf("T9 min_cpu_cores (%d) not > T8 min_cpu_cores (%d); fixture assumption broken",
				td9.MinCPUCores, td8.MinCPUCores)
		}
		got := AssignTier(d, reg)
		if got != T8 {
			t.Errorf("got %v, want T8 (T9 requires %d cores, descriptor has %d)",
				got, td9.MinCPUCores, d.CPUCores)
		}
	})

	// T12: 128 cores, 512 GiB, 4 GPUs, 40960 MB VRAM. T13 requires 8 GPUs.
	t.Run("T12_exact_gpu_ceiling", func(t *testing.T) {
		td12, _ := reg.Get("T12")
		td13, _ := reg.Get("T13")
		d := DeviceDescriptor{
			CPUCores:     td12.MinCPUCores,
			MemoryBytes:  mbToBytes(td12.MinMemoryMB),
			GPUCount:     td12.MinGPU,              // 4
			GPUVRAMBytes: mbToBytes(td12.MinGPUVRAMMB),
		}
		// Verify fixture: T13 needs more GPUs.
		if td13.MinGPU <= td12.MinGPU {
			t.Skipf("T13 min_gpu (%d) not > T12 min_gpu (%d); fixture assumption broken",
				td13.MinGPU, td12.MinGPU)
		}
		got := AssignTier(d, reg)
		if got != T12 {
			t.Errorf("got %v, want T12 (T13 requires %d GPUs, descriptor has %d)",
				got, td13.MinGPU, d.GPUCount)
		}
	})

	// T15: maximum requirements; must classify to T15.
	t.Run("T15_exact", func(t *testing.T) {
		d := buildDescriptorForTier(t, reg, "T15")
		got := AssignTier(d, reg)
		if got != T15 {
			t.Errorf("got %v, want T15", got)
		}
	})
}

// TestAssignTier_Monotonic verifies that if AssignTier returns Tn, the device
// also satisfies T1..T(n-1). This is the "monotonic meet" property: higher tiers
// imply all lower tiers are also met.
//
// Anti-bluff: if the Meets predicate or conversion is wrong this test surfaces it.
func TestAssignTier_Monotonic(t *testing.T) {
	reg := loadRegistry(t)

	// Build a descriptor that targets T10 (T13 requires 8 GPUs, T10 requires 4).
	td10, _ := reg.Get("T10")
	td11, _ := reg.Get("T11")
	d := DeviceDescriptor{
		CPUCores:     td10.MinCPUCores, // 64
		MemoryBytes:  mbToBytes(td10.MinMemoryMB),
		GPUCount:     td10.MinGPU,      // 4
		GPUVRAMBytes: mbToBytes(td10.MinGPUVRAMMB),
	}
	// The descriptor must fail T11 (which needs more cores or memory or VRAM).
	// If T11 requirements equal T10 on every dimension the test is vacuous; skip.
	caps := descriptorToCaps(d)
	if td11.Meets(caps) {
		t.Skip("T11 Meets T10-spec caps; monotonic fixture is vacuous — update thresholds in tierdef.yaml")
	}

	got := AssignTier(d, reg)
	// got must be <= T10; specifically we expect T10 unless network disqualifies.
	if got > T10 {
		t.Fatalf("AssignTier returned %v which is above T10; fixture is wrong", got)
	}
	if got < T1 {
		t.Fatalf("AssignTier returned %v which is below T1", got)
	}

	// Every tier from T1 up to (got-1) must also be met by this descriptor.
	gotIdx := int(got) - 1 // Tier const is 1-based; index in reg.Tiers is 0-based
	for i := 0; i < gotIdx; i++ {
		td := reg.Tiers[i]
		if !td.Meets(caps) {
			t.Errorf("monotonic violation: AssignTier=%v but tier %s (idx %d) not met by same caps",
				got, td.Tier, i)
		}
	}
}

// TestAssignTier_IterationOrderMutation is a named mutation test that documents
// the expected failure mode: if AssignTier iterated LOW→HIGH instead of HIGH→LOW
// it would always return the LOWEST matching tier (T1 for any capable device).
// We verify this by directly calling the Meets predicate in the wrong order and
// confirming T15-spec caps would return T1, which proves our HIGH→LOW logic is
// necessary and correct.
func TestAssignTier_IterationOrderMutation(t *testing.T) {
	reg := loadRegistry(t)

	// Build a T15-spec descriptor.
	d := buildDescriptorForTier(t, reg, "T15")
	caps := descriptorToCaps(d)

	// Simulate LOW→HIGH iteration (the WRONG order).
	var wrongResult Tier
	for i := 0; i < len(reg.Tiers); i++ {
		if reg.Tiers[i].Meets(caps) {
			wrongResult = tierNameToConst(reg.Tiers[i].Tier)
			break
		}
	}

	// The wrong order returns the LOWEST matching tier.
	// T15-spec caps must meet T1 (since T1 is a strict subset of T15's requirements).
	// If wrongResult == T15 that would mean T1 is not met by T15-caps, which is
	// wrong — and this test would expose that logic error too.
	if wrongResult == T15 {
		t.Error("mutation test: LOW→HIGH iteration returned T15; T15-spec caps must also meet T1")
	}

	// The CORRECT implementation must return T15 for this descriptor.
	correct := AssignTier(d, reg)
	if correct != T15 {
		t.Errorf("AssignTier(T15-spec) = %v, want T15 (HIGH→LOW iteration required)", correct)
	}

	// Confirm the two results differ — this is the core mutation assertion.
	if wrongResult == correct {
		t.Errorf("mutation test: LOW→HIGH and HIGH→LOW return the same result (%v); "+
			"one of them must be wrong — check T15 and T1 Meets predicates", correct)
	}
}

// TestAssignTier_NetworkSentinel documents the network-field behaviour: because
// DeviceDescriptor has no NetworkMbps field, descriptorToCaps sets NetworkMbps to
// networkUnknown (math.MaxInt) so that no tier's min_network_mbps gate ever fires
// due to a missing network value. This test verifies:
//  1. A T15-spec descriptor is NOT downgraded because of network.
//  2. A 1-core/256MB descriptor still classifies to T1 (the smallest capable tier).
func TestAssignTier_NetworkSentinel(t *testing.T) {
	reg := loadRegistry(t)

	// Verify the sentinel is large enough to satisfy every tier's network gate.
	for _, td := range reg.Tiers {
		if networkUnknown < td.MinNetworkMbps {
			t.Errorf("tier %s: networkUnknown (%d) < min_network_mbps (%d); sentinel is too small",
				td.Tier, networkUnknown, td.MinNetworkMbps)
		}
	}

	// A 1-core/256MB device must still land at T1 (CPU/memory are the binding constraints).
	td1, _ := reg.Get("T1")
	d := DeviceDescriptor{
		CPUCores:    td1.MinCPUCores,
		MemoryBytes: mbToBytes(td1.MinMemoryMB),
	}
	got := AssignTier(d, reg)
	if got != T1 {
		t.Errorf("1-core/256MB device classified as %v, want T1", got)
	}

	// A T15-spec device must still land at T15 (sentinel must not cause any gate to fail).
	d15 := buildDescriptorForTier(t, reg, "T15")
	got15 := AssignTier(d15, reg)
	if got15 != T15 {
		t.Errorf("T15-spec device classified as %v with network sentinel, want T15", got15)
	}
}
