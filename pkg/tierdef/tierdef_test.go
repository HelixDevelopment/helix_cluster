package tierdef_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/tierdef"
)

// Fixture-baked oracle constants for T1 and T15. These are hardcoded here, NOT
// derived from the implementation, so a wrong value in tierdef.yaml causes a
// test FAILURE — not a silent pass-through.
const (
	// T1 oracle (tiny embedded)
	oracleT1CPUCores    = 1
	oracleT1MemoryMB    = 256
	oracleT1GPU         = 0
	oracleT1NetworkMbps = 10

	// T15 oracle (extreme multi-GPU server)
	oracleT15CPUCores    = 256
	oracleT15MemoryMB    = 2097152 // 2 TiB
	oracleT15GPU         = 8
	oracleT15GPUVRAMMB   = 80000
	oracleT15NetworkMbps = 400000
)

// ── 1. Default() / LoadTiers parses exactly 15 tiers T1..T15 ─────────────────

// TestDefault_Parses15Tiers verifies that the embedded tierdef.yaml is loaded
// successfully and contains exactly the 15 tiers T1..T15 in ascending order.
func TestDefault_Parses15Tiers(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default() returned unexpected error: %v", err)
	}
	if reg == nil {
		t.Fatal("Default() returned nil registry")
	}
	if len(reg.Tiers) != 15 {
		t.Fatalf("len(Tiers) = %d; want 15", len(reg.Tiers))
	}

	// Order must be strictly T1..T15.
	for i, td := range reg.Tiers {
		want := fmt.Sprintf("T%d", i+1)
		if td.Tier != want {
			t.Errorf("Tiers[%d].Tier = %q; want %q", i, td.Tier, want)
		}
	}
}

// TestDefault_T1OracleValues asserts exact min-requirement values for T1.
// A mutation that changes any field in tierdef.yaml will break this test.
func TestDefault_T1OracleValues(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	td, ok := reg.Get("T1")
	if !ok {
		t.Fatal("Get(\"T1\") returned not-found")
	}

	if td.MinCPUCores != oracleT1CPUCores {
		t.Errorf("T1.MinCPUCores = %d; want %d", td.MinCPUCores, oracleT1CPUCores)
	}
	if td.MinMemoryMB != oracleT1MemoryMB {
		t.Errorf("T1.MinMemoryMB = %d; want %d", td.MinMemoryMB, oracleT1MemoryMB)
	}
	if td.MinGPU != oracleT1GPU {
		t.Errorf("T1.MinGPU = %d; want %d", td.MinGPU, oracleT1GPU)
	}
	if td.MinNetworkMbps != oracleT1NetworkMbps {
		t.Errorf("T1.MinNetworkMbps = %d; want %d", td.MinNetworkMbps, oracleT1NetworkMbps)
	}
}

// TestDefault_T15OracleValues asserts exact min-requirement values for T15.
func TestDefault_T15OracleValues(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	td, ok := reg.Get("T15")
	if !ok {
		t.Fatal("Get(\"T15\") returned not-found")
	}

	if td.MinCPUCores != oracleT15CPUCores {
		t.Errorf("T15.MinCPUCores = %d; want %d", td.MinCPUCores, oracleT15CPUCores)
	}
	if td.MinMemoryMB != oracleT15MemoryMB {
		t.Errorf("T15.MinMemoryMB = %d; want %d", td.MinMemoryMB, oracleT15MemoryMB)
	}
	if td.MinGPU != oracleT15GPU {
		t.Errorf("T15.MinGPU = %d; want %d", td.MinGPU, oracleT15GPU)
	}
	if td.MinGPUVRAMMB != oracleT15GPUVRAMMB {
		t.Errorf("T15.MinGPUVRAMMB = %d; want %d", td.MinGPUVRAMMB, oracleT15GPUVRAMMB)
	}
	if td.MinNetworkMbps != oracleT15NetworkMbps {
		t.Errorf("T15.MinNetworkMbps = %d; want %d", td.MinNetworkMbps, oracleT15NetworkMbps)
	}
}

// TestRegistry_Get_Miss verifies Get returns ok=false for a non-existent tier.
func TestRegistry_Get_Miss(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	_, ok := reg.Get("T16")
	if ok {
		t.Error("Get(\"T16\") returned ok=true; want false")
	}
	_, ok = reg.Get("")
	if ok {
		t.Error("Get(\"\") returned ok=true; want false")
	}
}

// ── 2. Rejection tests ────────────────────────────────────────────────────────

// TestLoadTiers_WrongSchemaVersion_IsRejected confirms LoadTiers refuses a doc
// with schema_version != 1.
func TestLoadTiers_WrongSchemaVersion_IsRejected(t *testing.T) {
	cases := []struct {
		name    string
		version int
	}{
		{"zero", 0},
		{"two", 2},
		{"large", 999},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			doc := buildMinimalYAML(tc.version, 15, false)
			reg, err := tierdef.LoadTiers([]byte(doc))
			if err == nil {
				t.Fatalf("LoadTiers accepted schema_version=%d; want error", tc.version)
			}
			if reg != nil {
				t.Errorf("LoadTiers returned non-nil registry on schema rejection")
			}
		})
	}
}

// TestLoadTiers_WrongTierCount_IsRejected confirms that a doc with != 15 tiers
// is rejected with a non-nil error.
func TestLoadTiers_WrongTierCount_IsRejected(t *testing.T) {
	for _, count := range []int{0, 1, 8, 14, 16} {
		count := count
		t.Run(fmt.Sprintf("count_%d", count), func(t *testing.T) {
			doc := buildMinimalYAML(1, count, false)
			reg, err := tierdef.LoadTiers([]byte(doc))
			if err == nil {
				t.Fatalf("LoadTiers accepted %d tiers; want error (need exactly 15)", count)
			}
			if reg != nil {
				t.Errorf("LoadTiers returned non-nil registry on count rejection")
			}
		})
	}
}

// TestLoadTiers_OutOfOrder_IsRejected confirms that tiers in the wrong order
// (e.g. T2 before T1) are rejected.
func TestLoadTiers_OutOfOrder_IsRejected(t *testing.T) {
	// Swap T1 and T2 to produce T2, T1, T3..T15.
	doc := buildMinimalYAML(1, 15, true /* outOfOrder */)
	reg, err := tierdef.LoadTiers([]byte(doc))
	if err == nil {
		t.Fatal("LoadTiers accepted out-of-order tiers; want error")
	}
	if reg != nil {
		t.Errorf("LoadTiers returned non-nil registry on out-of-order rejection")
	}
}

// TestLoadTiers_MissingTier_IsRejected confirms that a doc with a gap in the
// tier sequence (e.g. T1, T2, T4, T5… skipping T3) is rejected.
func TestLoadTiers_MissingTier_IsRejected(t *testing.T) {
	// Build 15 entries but name them T1,T2,T4,T5..T16 (skip T3, rename rest).
	doc := buildYAMLWithGap()
	reg, err := tierdef.LoadTiers([]byte(doc))
	if err == nil {
		t.Fatal("LoadTiers accepted doc with missing T3 (gap); want error")
	}
	if reg != nil {
		t.Errorf("LoadTiers returned non-nil registry on gap rejection")
	}
}

// TestLoadTiers_BadYAML_IsRejected confirms that malformed YAML is rejected.
func TestLoadTiers_BadYAML_IsRejected(t *testing.T) {
	_, err := tierdef.LoadTiers([]byte(":::not yaml:::"))
	if err == nil {
		t.Fatal("LoadTiers accepted malformed YAML; want error")
	}
}

// TestLoadTiers_ErrorMentionsVersion verifies the schema_version error message
// includes the bad version number for debuggability.
func TestLoadTiers_ErrorMentionsVersion(t *testing.T) {
	doc := buildMinimalYAML(42, 15, false)
	_, err := tierdef.LoadTiers([]byte(doc))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "42") {
		t.Errorf("error %q does not mention the bad version 42", err.Error())
	}
}

// ── 3. Meets boundary tests ───────────────────────────────────────────────────

// TestMeets_ExactBoundary_True verifies that a DeviceCaps EXACTLY matching a
// TierDef's min requirements returns Meets=true (inclusive boundary).
// Anti-bluff: if >= were changed to > in Meets, this test would FAIL.
func TestMeets_ExactBoundary_True(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}

	// Test exact boundary for every tier.
	for _, td := range reg.Tiers {
		td := td
		t.Run(td.Tier+"_exact", func(t *testing.T) {
			c := tierdef.DeviceCaps{
				CPUCores:    td.MinCPUCores,
				MemoryMB:    td.MinMemoryMB,
				GPUCount:    td.MinGPU,
				GPUVRAMMB:   td.MinGPUVRAMMB,
				NetworkMbps: td.MinNetworkMbps,
			}
			if !td.Meets(c) {
				t.Errorf("%s: Meets(exact-minimum) = false; want true (boundary must be inclusive)", td.Tier)
			}
		})
	}
}

// TestMeets_OneBelowCPU_False verifies that a caps with CPUCores one unit below
// the minimum returns Meets=false.
// Anti-bluff: if the CPUCores check were removed from Meets, this FAILS.
func TestMeets_OneBelowCPU_False(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}

	for _, td := range reg.Tiers {
		if td.MinCPUCores <= 0 {
			continue // can't go below 0
		}
		td := td
		t.Run(td.Tier+"_cpu_below", func(t *testing.T) {
			c := tierdef.DeviceCaps{
				CPUCores:    td.MinCPUCores - 1, // one below
				MemoryMB:    td.MinMemoryMB,
				GPUCount:    td.MinGPU,
				GPUVRAMMB:   td.MinGPUVRAMMB,
				NetworkMbps: td.MinNetworkMbps,
			}
			if td.Meets(c) {
				t.Errorf("%s: Meets(cpu=%d) = true; want false (cpu < min=%d)",
					td.Tier, c.CPUCores, td.MinCPUCores)
			}
		})
	}
}

// TestMeets_OneBelowMemory_False verifies that MemoryMB one below minimum
// returns Meets=false.
// Anti-bluff: if the MemoryMB check were removed from Meets, this FAILS.
func TestMeets_OneBelowMemory_False(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}

	for _, td := range reg.Tiers {
		if td.MinMemoryMB <= 0 {
			continue
		}
		td := td
		t.Run(td.Tier+"_mem_below", func(t *testing.T) {
			c := tierdef.DeviceCaps{
				CPUCores:    td.MinCPUCores,
				MemoryMB:    td.MinMemoryMB - 1, // one below
				GPUCount:    td.MinGPU,
				GPUVRAMMB:   td.MinGPUVRAMMB,
				NetworkMbps: td.MinNetworkMbps,
			}
			if td.Meets(c) {
				t.Errorf("%s: Meets(mem=%d) = true; want false (mem < min=%d)",
					td.Tier, c.MemoryMB, td.MinMemoryMB)
			}
		})
	}
}

// TestMeets_OneBelowGPU_False verifies that GPUCount one below minimum returns
// Meets=false.
// Anti-bluff: if the GPUCount check were removed from Meets, this FAILS.
func TestMeets_OneBelowGPU_False(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}

	for _, td := range reg.Tiers {
		if td.MinGPU <= 0 {
			continue // MinGPU=0 means "no GPU required", can't go below
		}
		td := td
		t.Run(td.Tier+"_gpu_below", func(t *testing.T) {
			c := tierdef.DeviceCaps{
				CPUCores:    td.MinCPUCores,
				MemoryMB:    td.MinMemoryMB,
				GPUCount:    td.MinGPU - 1, // one below
				GPUVRAMMB:   td.MinGPUVRAMMB,
				NetworkMbps: td.MinNetworkMbps,
			}
			if td.Meets(c) {
				t.Errorf("%s: Meets(gpu=%d) = true; want false (gpu < min=%d)",
					td.Tier, c.GPUCount, td.MinGPU)
			}
		})
	}
}

// TestMeets_OneBelowVRAM_False verifies that GPUVRAMMB one below minimum
// returns Meets=false (only for tiers that require a GPU).
// Anti-bluff: if the GPUVRAMMB check were removed from Meets, this FAILS.
func TestMeets_OneBelowVRAM_False(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}

	for _, td := range reg.Tiers {
		if td.MinGPU <= 0 || td.MinGPUVRAMMB <= 0 {
			continue // VRAM check only active when GPU required
		}
		td := td
		t.Run(td.Tier+"_vram_below", func(t *testing.T) {
			c := tierdef.DeviceCaps{
				CPUCores:    td.MinCPUCores,
				MemoryMB:    td.MinMemoryMB,
				GPUCount:    td.MinGPU,
				GPUVRAMMB:   td.MinGPUVRAMMB - 1, // one below
				NetworkMbps: td.MinNetworkMbps,
			}
			if td.Meets(c) {
				t.Errorf("%s: Meets(vram=%d) = true; want false (vram < min=%d)",
					td.Tier, c.GPUVRAMMB, td.MinGPUVRAMMB)
			}
		})
	}
}

// TestMeets_OneBelowNetwork_False verifies that NetworkMbps one below minimum
// returns Meets=false.
// Anti-bluff: if the NetworkMbps check were removed from Meets, this FAILS.
func TestMeets_OneBelowNetwork_False(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}

	for _, td := range reg.Tiers {
		if td.MinNetworkMbps <= 0 {
			continue
		}
		td := td
		t.Run(td.Tier+"_net_below", func(t *testing.T) {
			c := tierdef.DeviceCaps{
				CPUCores:    td.MinCPUCores,
				MemoryMB:    td.MinMemoryMB,
				GPUCount:    td.MinGPU,
				GPUVRAMMB:   td.MinGPUVRAMMB,
				NetworkMbps: td.MinNetworkMbps - 1, // one below
			}
			if td.Meets(c) {
				t.Errorf("%s: Meets(net=%d) = true; want false (net < min=%d)",
					td.Tier, c.NetworkMbps, td.MinNetworkMbps)
			}
		})
	}
}

// TestMeets_WeakDeviceDoesNotMeetT15 asserts that a tiny T1-class device does
// NOT satisfy T15 requirements.
// Anti-bluff: if Meets always returned true this test would FAIL.
func TestMeets_WeakDeviceDoesNotMeetT15(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	t15, ok := reg.Get("T15")
	if !ok {
		t.Fatal("T15 not found in registry")
	}

	weakDevice := tierdef.DeviceCaps{
		CPUCores:    oracleT1CPUCores,  // 1
		MemoryMB:    oracleT1MemoryMB,  // 256
		GPUCount:    0,
		GPUVRAMMB:   0,
		NetworkMbps: oracleT1NetworkMbps, // 10
	}
	if t15.Meets(weakDevice) {
		t.Errorf("T15.Meets(T1-class device) = true; want false")
	}
}

// TestMeets_VRAMIgnoredWhenNoGPURequired verifies that when MinGPU==0, the
// GPUVRAMMB check is NOT applied (a device with zero VRAM still Meets).
func TestMeets_VRAMIgnoredWhenNoGPURequired(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	// T1 has MinGPU=0; a device with zero VRAM should still satisfy it.
	t1, ok := reg.Get("T1")
	if !ok {
		t.Fatal("T1 not found")
	}
	if t1.MinGPU != 0 {
		t.Skipf("T1.MinGPU changed to %d; test only applies to MinGPU==0", t1.MinGPU)
	}

	c := tierdef.DeviceCaps{
		CPUCores:    t1.MinCPUCores,
		MemoryMB:    t1.MinMemoryMB,
		GPUCount:    0,
		GPUVRAMMB:   0, // zero VRAM is fine when GPU not required
		NetworkMbps: t1.MinNetworkMbps,
	}
	if !t1.Meets(c) {
		t.Errorf("T1.Meets(no-GPU device) = false; want true (MinGPU==0 means VRAM ignored)")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// buildMinimalYAML creates a synthetic YAML document with the given
// schema_version and n tiers named T1..Tn (or swapped if outOfOrder is true).
func buildMinimalYAML(version, n int, outOfOrder bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("schema_version: %d\ntiers:\n", version))
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("T%d", i)
		if outOfOrder && i == 1 {
			name = "T2"
		} else if outOfOrder && i == 2 {
			name = "T1"
		}
		sb.WriteString(fmt.Sprintf(
			"  - tier: %s\n    min_cpu_cores: %d\n    min_memory_mb: %d\n    min_gpu: 0\n    min_gpu_vram_mb: 0\n    min_network_mbps: 10\n    max_power_w: 5.0\n",
			name, i, 256*i,
		))
	}
	return sb.String()
}

// buildYAMLWithGap builds a 15-entry YAML where T3 is replaced by T16 (creates
// a gap — T3 is missing and the name sequence is broken).
func buildYAMLWithGap() string {
	var sb strings.Builder
	sb.WriteString("schema_version: 1\ntiers:\n")
	for i := 1; i <= 15; i++ {
		name := fmt.Sprintf("T%d", i)
		if i == 3 {
			name = "T16" // breaks the sequence at position 3
		}
		sb.WriteString(fmt.Sprintf(
			"  - tier: %s\n    min_cpu_cores: %d\n    min_memory_mb: %d\n    min_gpu: 0\n    min_gpu_vram_mb: 0\n    min_network_mbps: 10\n    max_power_w: 5.0\n",
			name, i, 256*i,
		))
	}
	return sb.String()
}
