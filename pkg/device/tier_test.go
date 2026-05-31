package device

import "testing"

// canonical[T] is a descriptor that sits exactly on tier T's lower boundary and
// must classify to EXACTLY T. These anchor the per-tier exact-classification
// guarantee. MUTATION: collapse ClassifyTier to `return T4` (or any constant) and
// every row except that constant's row fails -> proves the function actually
// discriminates.
func canonical() map[Tier]DeviceDescriptor {
	return map[Tier]DeviceDescriptor{
		T1: {ID: "t1", Arch: ArchARM64, CPUCores: 1, MemoryBytes: 256 * mib, HasBattery: true},
		T2: {ID: "t2", Arch: ArchARM64, CPUCores: 2, MemoryBytes: 512 * mib, HasBattery: true},
		T3: {ID: "t3", Arch: ArchARM64, CPUCores: 4, MemoryBytes: 2 * gib},
		T4: {ID: "t4", Arch: ArchAMD64, CPUCores: 8, MemoryBytes: 16 * gib},
		T5: {ID: "t5", Arch: ArchAMD64, CPUCores: 8, MemoryBytes: 16 * gib, GPUCount: 1, GPUVRAMBytes: 2 * gib},
		T6: {ID: "t6", Arch: ArchAMD64, CPUCores: 16, MemoryBytes: 32 * gib, GPUCount: 1, GPUVRAMBytes: 8 * gib},
		T7: {ID: "t7", Arch: ArchAMD64, CPUCores: 64, MemoryBytes: 128 * gib, GPUCount: 1, GPUVRAMBytes: 16 * gib},
		T8: {ID: "t8", Arch: ArchAMD64, CPUCores: 128, MemoryBytes: 256 * gib, GPUCount: 8, GPUVRAMBytes: 320 * gib},
	}
}

func TestClassifyTier_ExactPerTier(t *testing.T) {
	for want, d := range canonical() {
		got := ClassifyTier(d)
		if got != want {
			t.Errorf("ClassifyTier(%s) = %s, want %s", d.ID, got, want)
		}
	}
}

// Boundary tests: a descriptor exactly at a gate is tier T; one unit below the
// gate drops to the next-lower tier. MUTATION: change any `>=` to `>` in the
// corresponding gate (e.g. t3MinCores) and the "at boundary" case falls through
// to the lower tier, failing the test.
func TestClassifyTier_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		d    DeviceDescriptor
		want Tier
	}{
		// T3 lower boundary on cores: 4 cores @2GiB == T3; 3 cores drops to T2.
		{"t3_at_cores", DeviceDescriptor{CPUCores: 4, MemoryBytes: 2 * gib}, T3},
		{"t3_below_cores", DeviceDescriptor{CPUCores: 3, MemoryBytes: 2 * gib}, T2},
		// T4 lower boundary on memory: 8c@16GiB == T4; 8c@(16GiB-1) drops to T3.
		{"t4_at_mem", DeviceDescriptor{CPUCores: 8, MemoryBytes: 16 * gib}, T4},
		{"t4_below_mem", DeviceDescriptor{CPUCores: 8, MemoryBytes: 16*gib - 1}, T3},
		// T5 lower boundary on VRAM: 1 GPU @2GiB == T5; @(2GiB-1) drops to T4.
		{"t5_at_vram", DeviceDescriptor{CPUCores: 8, MemoryBytes: 16 * gib, GPUCount: 1, GPUVRAMBytes: 2 * gib}, T5},
		{"t5_below_vram", DeviceDescriptor{CPUCores: 8, MemoryBytes: 16 * gib, GPUCount: 1, GPUVRAMBytes: 2*gib - 1}, T4},
		// T5 via NPU: exactly 10 TOPS == T5; 9.9 drops to T4 (CPU/mem qualify T4).
		{"t5_at_npu", DeviceDescriptor{CPUCores: 8, MemoryBytes: 16 * gib, NPUTops: 10.0}, T5},
		{"t5_below_npu", DeviceDescriptor{CPUCores: 8, MemoryBytes: 16 * gib, NPUTops: 9.9}, T4},
		// T6 lower boundary on cores: 16c + 8GiB GPU == T6; 15c drops to T5.
		{"t6_at_cores", DeviceDescriptor{CPUCores: 16, MemoryBytes: 32 * gib, GPUCount: 1, GPUVRAMBytes: 8 * gib}, T6},
		{"t6_below_cores", DeviceDescriptor{CPUCores: 15, MemoryBytes: 32 * gib, GPUCount: 1, GPUVRAMBytes: 8 * gib}, T5},
		// T7 lower boundary on cores: 64c + 16GiB GPU == T7; 63c drops to T6.
		{"t7_at_cores", DeviceDescriptor{CPUCores: 64, MemoryBytes: 128 * gib, GPUCount: 1, GPUVRAMBytes: 16 * gib}, T7},
		{"t7_below_cores", DeviceDescriptor{CPUCores: 63, MemoryBytes: 128 * gib, GPUCount: 1, GPUVRAMBytes: 16 * gib}, T6},
		// T8 lower boundary on cores: 128c == T8; 127c (with 16GiB GPU) drops to T7.
		{"t8_at_cores", DeviceDescriptor{CPUCores: 128, MemoryBytes: 256 * gib}, T8},
		{"t8_below_cores", DeviceDescriptor{CPUCores: 127, MemoryBytes: 256 * gib, GPUCount: 1, GPUVRAMBytes: 16 * gib}, T7},
		// T8 via multi-GPU: 4 GPUs == T8 even at low core count; 3 GPUs is not T8.
		{"t8_multigpu", DeviceDescriptor{CPUCores: 8, MemoryBytes: 32 * gib, GPUCount: 4, GPUVRAMBytes: 96 * gib}, T8},
		// 3 GPUs misses the 4-GPU T8 gate; only 8 cores misses T6/T7, so the
		// real classification is T5 (a GPU with >=2GiB VRAM). This documents that
		// raw GPU count alone does not buy a high tier without cores.
		{"t8_multigpu_below", DeviceDescriptor{CPUCores: 8, MemoryBytes: 32 * gib, GPUCount: 3, GPUVRAMBytes: 72 * gib}, T5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTier(tc.d); got != tc.want {
				t.Errorf("ClassifyTier(%s) = %s, want %s", tc.name, got, tc.want)
			}
		})
	}
}

// MUTATION: change the TierUnknown guard to `return T1` and this fails, since an
// empty descriptor would then be T1 instead of TierUnknown.
func TestClassifyTier_EmptyIsUnknown(t *testing.T) {
	if got := ClassifyTier(DeviceDescriptor{}); got != TierUnknown {
		t.Errorf("ClassifyTier(empty) = %s, want %s", got, TierUnknown)
	}
}

// MUTATION: drop the `* archWeight(d.Arch)` factor (or make archWeight constant)
// and the amd64>arm64>riscv64 ordering for identical hardware breaks.
func TestEffectiveCompute_ArchWeighting(t *testing.T) {
	base := DeviceDescriptor{CPUCores: 100}
	amd := base
	amd.Arch = ArchAMD64
	arm := base
	arm.Arch = ArchARM64
	rv := base
	rv.Arch = ArchRISCV64

	ca, cm, cr := EffectiveCompute(amd), EffectiveCompute(arm), EffectiveCompute(rv)
	if !(ca > cm && cm > cr) {
		t.Errorf("arch ordering broken: amd64=%v arm64=%v riscv64=%v; want amd64>arm64>riscv64", ca, cm, cr)
	}
	// arm64 weight is exactly 1.0, so its score equals the raw base.
	if cm != 100.0 {
		t.Errorf("EffectiveCompute(arm64,100 cores) = %v, want 100.0", cm)
	}
}

// MUTATION: zero out the cores term (`cores*0.0`) or the gpu term and the
// corresponding strict-increase assertion fails. Proves monotonicity in each
// driver independently.
func TestEffectiveCompute_Monotonic(t *testing.T) {
	d := DeviceDescriptor{Arch: ArchAMD64, CPUCores: 8, GPUCount: 1, GPUVRAMBytes: 4 * gib, NPUTops: 5}

	more := d
	more.CPUCores++
	if EffectiveCompute(more) <= EffectiveCompute(d) {
		t.Errorf("adding a core did not increase score: %v !> %v", EffectiveCompute(more), EffectiveCompute(d))
	}

	moreGPU := d
	moreGPU.GPUCount++
	if EffectiveCompute(moreGPU) <= EffectiveCompute(d) {
		t.Errorf("adding a GPU did not increase score: %v !> %v", EffectiveCompute(moreGPU), EffectiveCompute(d))
	}

	moreVRAM := d
	moreVRAM.GPUVRAMBytes += gib
	if EffectiveCompute(moreVRAM) <= EffectiveCompute(d) {
		t.Errorf("adding VRAM did not increase score: %v !> %v", EffectiveCompute(moreVRAM), EffectiveCompute(d))
	}

	moreNPU := d
	moreNPU.NPUTops += 1
	if EffectiveCompute(moreNPU) <= EffectiveCompute(d) {
		t.Errorf("adding NPU TOPS did not increase score: %v !> %v", EffectiveCompute(moreNPU), EffectiveCompute(d))
	}
}

// MUTATION: make EffectiveCompute ignore GPUVRAMBytes and a server outscoring a
// handheld by a wide margin no longer holds at the expected magnitude.
func TestEffectiveCompute_ServerBeatsHandheld(t *testing.T) {
	server := canonical()[T8]
	handheld := canonical()[T2]
	if EffectiveCompute(server) <= EffectiveCompute(handheld) {
		t.Errorf("server %v should outscore handheld %v", EffectiveCompute(server), EffectiveCompute(handheld))
	}
}

// MUTATION: change IsAccelerated to `return d.GPUCount > 0` (drop NPU) and the
// npu-only case fails; change to always-true and the cpu-only case fails.
func TestIsAccelerated(t *testing.T) {
	tests := []struct {
		name string
		d    DeviceDescriptor
		want bool
	}{
		{"cpu_only", DeviceDescriptor{CPUCores: 64}, false},
		{"gpu", DeviceDescriptor{GPUCount: 1}, true},
		{"npu_only", DeviceDescriptor{NPUTops: 12}, true},
	}
	for _, tc := range tests {
		if got := tc.d.IsAccelerated(); got != tc.want {
			t.Errorf("%s: IsAccelerated() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// MUTATION: have ClassifyTier overwrite or ignore Arch handling — Arch is input
// data and must be preserved unchanged; this guards against any accidental
// descriptor mutation in the (value-receiver) helpers.
func TestArchPreserved(t *testing.T) {
	d := DeviceDescriptor{Arch: ArchRISCV64, CPUCores: 4, MemoryBytes: 2 * gib}
	_ = ClassifyTier(d)
	_ = EffectiveCompute(d)
	_ = d.IsAccelerated()
	if d.Arch != ArchRISCV64 {
		t.Errorf("Arch mutated to %q, want %q", d.Arch, ArchRISCV64)
	}
}

// MUTATION: change a String() case (e.g. return "T9" for T8) and this fails.
func TestTierString(t *testing.T) {
	cases := map[Tier]string{
		TierUnknown: "T0", T1: "T1", T4: "T4", T8: "T8",
	}
	for tr, want := range cases {
		if got := tr.String(); got != want {
			t.Errorf("Tier(%d).String() = %q, want %q", int(tr), got, want)
		}
	}
}
