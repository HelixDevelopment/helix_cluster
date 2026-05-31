package resources

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMultiplier_KnownModelExact is the headline anti-bluff test: a known
// vendor:device must map to its EXACT catalog multiplier, not a constant.
//
// Mutation that this kills:
//   - a Multiplier that ignores the catalog and returns a constant (e.g.
//     `return UnknownGPUMultiplier` or `return 1.0`) fails every non-default row
//     below, because A100/H100/iGPU all have distinct, non-1.0 multipliers.
//   - swapping the lookup key (e.g. keying on Model verbatim instead of the
//     canonical "vendor:device") fails the " x N" and heterogeneous rows.
func TestMultiplier_KnownModelExact(t *testing.T) {
	cases := []struct {
		name  string
		model string
		want  float64
	}{
		{"H100 datacenter", "0x10de:0x2330", 16.0},
		{"A100-80GB datacenter", "0x10de:0x20b2", 10.0},
		{"MI250X datacenter", "0x1002:0x740c", 12.0},
		{"RTX4090 consumer", "0x10de:0x2684", 6.0},
		{"RTX3090 consumer", "0x10de:0x2204", 4.0},
		{"RX6900XT consumer", "0x1002:0x73bf", 3.5},
		{"Intel iGPU integrated", "0x8086:0x9a60", 0.25},
		// The "x N" multiplicity suffix must not defeat the lookup.
		{"RTX3090 x4 still maps", "0x10de:0x2204 x 4", 4.0},
		// Heterogeneous list: identity is taken from the FIRST card.
		{"heterogeneous first wins", "0x10de:0x2330,0x8086:0x9a60", 16.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Multiplier(GPUInfo{Model: tc.model})
			if got != tc.want {
				t.Fatalf("Multiplier(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}

	// Class ordering invariant: datacenter > consumer > integrated. This is the
	// whole point of the catalog and would break if the table were flattened.
	dc := Multiplier(GPUInfo{Model: "0x10de:0x2330"})       // H100
	consumer := Multiplier(GPUInfo{Model: "0x10de:0x2204"}) // RTX 3090
	igpu := Multiplier(GPUInfo{Model: "0x8086:0x9a60"})     // Intel iGPU
	if !(dc > consumer && consumer > igpu) {
		t.Fatalf("class ordering violated: datacenter=%v consumer=%v integrated=%v", dc, consumer, igpu)
	}
}

// TestMultiplier_UnknownDefault: an unknown vendor:device returns the documented
// UnknownGPUMultiplier, and so does an empty/garbled Model.
//
// Mutation that this kills: returning a catalog hit's multiplier for an unknown
// key (e.g. map access without the comma-ok guard, defaulting to a zero-value
// gpuClass with multiplier 0) would return 0, not UnknownGPUMultiplier.
func TestMultiplier_UnknownDefault(t *testing.T) {
	cases := []string{
		"0xdead:0xbeef", // well-formed but not in catalog
		"",              // no GPU
		"garbage",       // not vendor:device shaped
		"0x10de",        // missing device half
	}
	for _, m := range cases {
		got := Multiplier(GPUInfo{Model: m})
		if got != UnknownGPUMultiplier {
			t.Errorf("Multiplier(%q) = %v, want default %v", m, got, UnknownGPUMultiplier)
		}
	}
}

// TestFingerprint_StableAndDistinct: the fingerprint is deterministic for a
// given GPUInfo, distinct for distinct PCI identities, and distinct for the same
// identity with different UUIDs.
//
// Mutation that this kills:
//   - a Fingerprint that returns a constant (or ignores Model) collapses the
//     three distinct GPUs into one value, failing the distinctness asserts.
//   - dropping the UUID from the fingerprint makes the two same-model/different-
//     UUID GPUs collide, failing the last assertion.
func TestFingerprint_StableAndDistinct(t *testing.T) {
	a := GPUInfo{Model: "0x10de:0x2330"}
	b := GPUInfo{Model: "0x1002:0x73bf"}

	// Deterministic: same input -> same output across calls.
	if Fingerprint(a) != Fingerprint(a) {
		t.Fatal("Fingerprint not stable for identical input")
	}
	// Exact expected value (anti-bluff: pin the format, not just "non-empty").
	if got := Fingerprint(a); got != "0x10de:0x2330" {
		t.Errorf("Fingerprint(a) = %q, want %q", got, "0x10de:0x2330")
	}
	// Distinct identities -> distinct fingerprints.
	if Fingerprint(a) == Fingerprint(b) {
		t.Errorf("distinct GPUs share fingerprint %q", Fingerprint(a))
	}

	// Same model, different hardware UUID -> distinct fingerprints.
	u1 := GPUInfo{Model: "0x10de:0x2330", UUID: "GPU-aaaa"}
	u2 := GPUInfo{Model: "0x10de:0x2330", UUID: "GPU-bbbb"}
	if got := Fingerprint(u1); got != "0x10de:0x2330:GPU-aaaa" {
		t.Errorf("Fingerprint(u1) = %q, want %q", got, "0x10de:0x2330:GPU-aaaa")
	}
	if Fingerprint(u1) == Fingerprint(u2) {
		t.Errorf("same-model different-UUID GPUs collided on %q", Fingerprint(u1))
	}
	// Distinct from the no-UUID variant too.
	if Fingerprint(u1) == Fingerprint(a) {
		t.Error("UUID-bearing fingerprint collided with bare identity")
	}

	// Empty model fingerprints to a non-empty sentinel, never "".
	if got := Fingerprint(GPUInfo{}); got != "unknown" {
		t.Errorf("Fingerprint(zero) = %q, want %q", got, "unknown")
	}
}

// TestFlags_RoundTrip: attested + thermalThrottling flags round-trip through the
// set/read helpers.
//
// Mutation that this kills: a SetAttested that ignores its argument (always sets
// true/false) or an IsAttested that returns a constant fails the paired
// true-then-false assertions.
func TestFlags_RoundTrip(t *testing.T) {
	g := GPUInfo{Model: "0x10de:0x2330"}

	if IsAttested(g) {
		t.Fatal("fresh GPUInfo should not be attested")
	}
	g = SetAttested(g, true)
	if !IsAttested(g) {
		t.Error("SetAttested(true) did not stick")
	}
	g = SetAttested(g, false)
	if IsAttested(g) {
		t.Error("SetAttested(false) did not clear")
	}

	if IsThermalThrottling(g) {
		t.Fatal("fresh GPUInfo should not be throttling")
	}
	g = SetThermalThrottling(g, true)
	if !IsThermalThrottling(g) {
		t.Error("SetThermalThrottling(true) did not stick")
	}
	g = SetThermalThrottling(g, false)
	if IsThermalThrottling(g) {
		t.Error("SetThermalThrottling(false) did not clear")
	}

	// Flags are independent: setting one must not move the other.
	g = SetAttested(SetThermalThrottling(GPUInfo{}, true), false)
	if IsAttested(g) || !IsThermalThrottling(g) {
		t.Errorf("flags not independent: attested=%v throttling=%v", IsAttested(g), IsThermalThrottling(g))
	}
}

// TestEffectiveMultiplier_ThrottlingReduces: a throttling GPU's effective
// multiplier is strictly reduced versus its nominal Multiplier, by exactly
// ThermalThrottleFactor.
//
// Mutation that this kills: an EffectiveMultiplier that ignores
// ThermalThrottling (returns Multiplier unchanged) fails the strict-less-than
// and the exact-product assertions.
func TestEffectiveMultiplier_ThrottlingReduces(t *testing.T) {
	cool := GPUInfo{Model: "0x10de:0x2330"} // H100, nominal 16.0
	nominal := Multiplier(cool)
	if EffectiveMultiplier(cool) != nominal {
		t.Errorf("cool EffectiveMultiplier = %v, want nominal %v", EffectiveMultiplier(cool), nominal)
	}

	hot := SetThermalThrottling(cool, true)
	eff := EffectiveMultiplier(hot)
	if eff >= nominal {
		t.Errorf("throttling EffectiveMultiplier = %v, want < nominal %v", eff, nominal)
	}
	if want := nominal * ThermalThrottleFactor; eff != want {
		t.Errorf("throttling EffectiveMultiplier = %v, want %v (nominal*%v)", eff, want, ThermalThrottleFactor)
	}

	// Attestation must NOT change magnitude (it gates policy, not compute).
	if EffectiveMultiplier(SetAttested(cool, true)) != nominal {
		t.Errorf("attestation changed EffectiveMultiplier: got %v, want %v",
			EffectiveMultiplier(SetAttested(cool, true)), nominal)
	}
}

// TestCatalogName_KnownAndUnknown: CatalogName resolves a known identity to its
// human name and reports ok=false for unknown ones.
//
// Mutation that this kills: a CatalogName that always returns ok=true (or a
// fixed name) fails the unknown row's ok==false assertion.
func TestCatalogName_KnownAndUnknown(t *testing.T) {
	name, ok := CatalogName(GPUInfo{Model: "0x10de:0x2330"})
	if !ok || name != "NVIDIA H100" {
		t.Errorf("CatalogName(H100) = (%q, %v), want (NVIDIA H100, true)", name, ok)
	}
	if _, ok := CatalogName(GPUInfo{Model: "0xdead:0xbeef"}); ok {
		t.Error("CatalogName(unknown) returned ok=true")
	}
	if got := DescribeClass(GPUInfo{Model: "0xdead:0xbeef"}); got != "unknown (x1)" {
		t.Errorf("DescribeClass(unknown) = %q, want %q", got, "unknown (x1)")
	}
}

// TestUniqueID_ParsedFromDRM proves the UUID seam is wired to REAL sysfs bytes:
// a fake card carrying a unique_id node surfaces that exact UUID in the
// aggregate GPUInfo and into the Fingerprint, end to end.
//
// Mutation that this kills: dropping the unique_id read in ParseDRMTree (or not
// propagating devs[0].UUID in GPUInfoFromDevices) leaves UUID empty and fails the
// exact fingerprint assertion.
func TestUniqueID_ParsedFromDRM(t *testing.T) {
	base := t.TempDir()
	dev := filepath.Join(base, DRMClassDir, "card0", "device")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dev, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("vendor", "0x10de\n")
	write("device", "0x2330\n")
	write("mem_info_vram_total", "85899345920\n")
	write("unique_id", "GPU-0123456789abcdef\n")

	devs, err := ParseDRMTree(base)
	if err != nil {
		t.Fatalf("ParseDRMTree: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(devs))
	}
	if devs[0].UUID != "GPU-0123456789abcdef" {
		t.Errorf("devs[0].UUID = %q, want GPU-0123456789abcdef", devs[0].UUID)
	}

	info := GPUInfoFromDevices(devs)
	if info.UUID != "GPU-0123456789abcdef" {
		t.Errorf("info.UUID = %q, want GPU-0123456789abcdef", info.UUID)
	}
	if got := Fingerprint(info); got != "0x10de:0x2330:GPU-0123456789abcdef" {
		t.Errorf("Fingerprint = %q, want 0x10de:0x2330:GPU-0123456789abcdef", got)
	}
	// And the class signal flows through: H100 multiplier == 16.0.
	if m := Multiplier(info); m != 16.0 {
		t.Errorf("Multiplier(H100 from DRM) = %v, want 16.0", m)
	}
}
