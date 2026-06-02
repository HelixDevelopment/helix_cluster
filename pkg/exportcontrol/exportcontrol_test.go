package exportcontrol

import (
	"errors"
	"testing"
)

// TestVerify_RejectsTier3H100 is the load-bearing test for the country-tier
// guard. Onboarding {Tier3 country "IR", "H100"} MUST be rejected with
// ErrExportDenied. If the country check inside VerifyOnboarding is mutated to
// always return nil, this test FAILS.
func TestVerify_RejectsTier3H100(t *testing.T) {
	const runUUID = "exportcontrol-test-2f1a9c7e-rejects-tier3-h100"

	// Independently verify the precondition: IR is Tier3.
	tier, known := TierForCountry("IR")
	if !known || tier != Tier3 {
		t.Fatalf("[%s] precondition: expected IR=Tier3(%d), got tier=%d known=%v",
			runUUID, Tier3, tier, known)
	}

	n := NodeOnboarding{CountryCode: "IR", GPUModel: "NVIDIA H100 80GB"}
	err := VerifyOnboarding(n)

	// Hand-calculated expected: Tier3 + controlled GPU -> ErrExportDenied.
	if !errors.Is(err, ErrExportDenied) {
		t.Fatalf("[%s] expected ErrExportDenied for {IR,H100}, got err=%v", runUUID, err)
	}
	t.Logf("[%s] delta: onboarding {IR(Tier3), H100} before=permitted-by-mutant -> after=ErrExportDenied (correct)", runUUID)
}

// TestVerify_AllowsTier1H100 proves a Tier1 country with the same controlled
// GPU is allowed (nil). This pins the guard from the other side: a mutation
// that always denies would fail here.
func TestVerify_AllowsTier1H100(t *testing.T) {
	const runUUID = "exportcontrol-test-7b3d4e10-allows-tier1-h100"

	tier, known := TierForCountry("US")
	if !known || tier != Tier1 {
		t.Fatalf("[%s] precondition: expected US=Tier1(%d), got tier=%d known=%v",
			runUUID, Tier1, tier, known)
	}

	n := NodeOnboarding{CountryCode: "US", GPUModel: "NVIDIA H100 80GB"}
	err := VerifyOnboarding(n)

	// Hand-calculated expected: Tier1 + controlled GPU -> nil (allowed).
	if err != nil {
		t.Fatalf("[%s] expected nil (allowed) for {US,H100}, got err=%v", runUUID, err)
	}
	t.Logf("[%s] delta: onboarding {US(Tier1), H100} before=denied-by-mutant -> after=nil/allowed (correct)", runUUID)
}

// TestVerify_RejectsTier2A100 confirms Tier2 (restricted) also denies a
// controlled GPU, distinct from the Tier1 allow path.
func TestVerify_RejectsTier2A100(t *testing.T) {
	const runUUID = "exportcontrol-test-9c8f1a22-rejects-tier2-a100"

	tier, known := TierForCountry("CN")
	if !known || tier != Tier2 {
		t.Fatalf("[%s] precondition: expected CN=Tier2(%d), got tier=%d known=%v",
			runUUID, Tier2, tier, known)
	}

	err := VerifyOnboarding(NodeOnboarding{CountryCode: "CN", GPUModel: "A100"})
	if !errors.Is(err, ErrExportDenied) {
		t.Fatalf("[%s] expected ErrExportDenied for {CN,A100}, got err=%v", runUUID, err)
	}
	t.Logf("[%s] delta: onboarding {CN(Tier2), A100} -> ErrExportDenied (correct)", runUUID)
}

// TestVerify_UnknownCountryDeniesControlledGPU proves an unrecognized country
// code is treated as restricted for controlled GPUs.
func TestVerify_UnknownCountryDeniesControlledGPU(t *testing.T) {
	const runUUID = "exportcontrol-test-1e5b6c33-unknown-denies"

	if _, known := TierForCountry("ZZ"); known {
		t.Fatalf("[%s] precondition: expected ZZ to be unknown, but it is catalogued", runUUID)
	}

	err := VerifyOnboarding(NodeOnboarding{CountryCode: "ZZ", GPUModel: "H100"})
	if !errors.Is(err, ErrExportDenied) {
		t.Fatalf("[%s] expected ErrExportDenied for {ZZ(unknown),H100}, got err=%v", runUUID, err)
	}
	t.Logf("[%s] delta: onboarding {ZZ(unknown), H100} -> ErrExportDenied (correct)", runUUID)
}

// TestVerify_NonControlledGPUAllowedFromTier3 proves the policy is scoped to
// controlled GPUs only: a Tier3 node with a non-controlled GPU is allowed.
func TestVerify_NonControlledGPUAllowedFromTier3(t *testing.T) {
	const runUUID = "exportcontrol-test-4a2d7e44-noncontrolled-allowed"

	// Sanity: the model is genuinely not controlled.
	if IsControlledGPU("RTX 4090") {
		t.Fatalf("[%s] precondition: RTX 4090 should not be export-controlled", runUUID)
	}

	err := VerifyOnboarding(NodeOnboarding{CountryCode: "IR", GPUModel: "RTX 4090"})
	if err != nil {
		t.Fatalf("[%s] expected nil for {IR,RTX 4090} (non-controlled), got err=%v", runUUID, err)
	}
	t.Logf("[%s] delta: onboarding {IR(Tier3), RTX 4090(non-controlled)} -> nil/allowed (correct)", runUUID)
}

// TestIsControlledGPU verifies the single-source controlled-model catalog with
// hand-picked positive and negative cases (case-insensitive substring match).
func TestIsControlledGPU(t *testing.T) {
	const runUUID = "exportcontrol-test-6f9a0b55-iscontrolled"

	cases := []struct {
		model string
		want  bool
	}{
		{"NVIDIA H100 80GB", true},
		{"a100", true},
		{"H800", true},
		{"B200", true},
		{"RTX 4090", false},
		{"Tesla T4", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsControlledGPU(c.model); got != c.want {
			t.Fatalf("[%s] IsControlledGPU(%q)=%v, want %v", runUUID, c.model, got, c.want)
		}
	}
	t.Logf("[%s] delta: %d controlled/non-controlled classifications all matched hand-calculated values", runUUID, len(cases))
}

// TestTierForCountry verifies catalog lookups including case-insensitivity and
// the unknown-code signal.
func TestTierForCountry(t *testing.T) {
	const runUUID = "exportcontrol-test-8d1c2e66-tierforcountry"

	cases := []struct {
		code      string
		wantTier  CountryTier
		wantKnown bool
	}{
		{"US", Tier1, true},
		{"de", Tier1, true}, // case-insensitive
		{"CN", Tier2, true},
		{"IR", Tier3, true},
		{"ZZ", 0, false},
	}
	for _, c := range cases {
		gotTier, gotKnown := TierForCountry(c.code)
		if gotKnown != c.wantKnown || (c.wantKnown && gotTier != c.wantTier) {
			t.Fatalf("[%s] TierForCountry(%q)=(%d,%v), want (%d,%v)",
				runUUID, c.code, gotTier, gotKnown, c.wantTier, c.wantKnown)
		}
	}
	t.Logf("[%s] delta: %d catalog lookups matched hand-calculated tiers", runUUID, len(cases))
}
