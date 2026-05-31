package device

import "testing"

// StaticProber is the honest software reference for the hardware-probe seam. It
// must echo its configured descriptor verbatim so callers/tests get
// deterministic input without real hardware.
//
// MUTATION: have Probe() return a different/zero descriptor and the round-trip
// equality (and the derived tier) fails.
func TestStaticProber_ReturnsFixedDescriptor(t *testing.T) {
	want := DeviceDescriptor{
		ID: "ref", Arch: ArchAMD64, CPUCores: 16, MemoryBytes: 32 * gib,
		GPUCount: 1, GPUVRAMBytes: 8 * gib, Trust: TrustAttested,
	}
	var p Prober = StaticProber{Descriptor: want}
	got, err := p.Probe()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("Probe() = %+v, want %+v", got, want)
	}
	// The probed descriptor must classify deterministically (T6 by thresholds).
	if tier := ClassifyTier(got); tier != T6 {
		t.Errorf("ClassifyTier(probed) = %s, want T6", tier)
	}
}
