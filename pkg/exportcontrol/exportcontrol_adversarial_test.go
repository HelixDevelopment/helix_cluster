package exportcontrol

import (
	"errors"
	"testing"
)

// This file contains ADVERSARIAL export-control tests (HXC compliance hardening).
//
// The package enforces a hard, fail-closed export-control policy: a node
// declaring an export-controlled GPU (H100/A100/... substring) may onboard ONLY
// from a Tier1 jurisdiction. Tier2 (restricted), Tier3 (embargoed), and UNKNOWN
// jurisdictions are denied. The compliance-critical property is the *deny side*:
//
//	An embargoed/restricted/unknown jurisdiction declaring a controlled GPU MUST
//	be blocked, and that block MUST NOT be bypassable by case, whitespace,
//	substring/superstring lookalikes, unicode confusables, aliases, or empty
//	input.
//
// The pre-existing tests cover the canonical IR/CN/US/ZZ paths. The GAP they
// leave — and what these tests close — is the *identity-confusion / match-bypass*
// class on the country code, plus fail-closed edge inputs and the controlled-GPU
// substring-match anti-bypass. Each block below is written so it FAILS if the
// load-bearing country-tier guard in VerifyOnboarding were weakened to
// default-allow, or if TierForCountry's exact-match lookup were replaced by a
// substring/case-folding-leak match.
//
// ANTI-BLUFF: every embargo assertion below would FLIP to a restricted-flow-
// ALLOWED failure under the documented mutations (see TestADV_MutationGuide).

const advControlledGPU = "NVIDIA H100 80GB" // genuinely export-controlled (H100)

// embargoedAndRestricted enumerates every catalogued non-Tier1 jurisdiction.
// A controlled GPU from ANY of these MUST be denied — this is the core embargo.
var embargoedAndRestricted = []struct {
	code string
	tier CountryTier
}{
	{"CN", Tier2}, {"RU", Tier2}, {"VE", Tier2}, {"BY", Tier2},
	{"IR", Tier3}, {"KP", Tier3}, {"SY", Tier3}, {"CU", Tier3},
}

// TestADV_AllInList_EmbargoIsHard is the core embargo-enforcement test: every
// restricted/embargoed jurisdiction in the catalog, declaring a controlled GPU,
// MUST be blocked. If even one is allowed, that is a SEVERE compliance bug
// (restricted flow permitted) and this test fails loudly naming the offender.
//
// BITES because: if VerifyOnboarding default-allowed (guard removed), every one
// of these would return nil -> restricted flow allowed -> fail.
func TestADV_AllInList_EmbargoIsHard(t *testing.T) {
	const runUUID = "exportcontrol-adv-d41e7a90-embargo-is-hard"

	for _, c := range embargoedAndRestricted {
		// Independent oracle: confirm the catalog really classes it non-Tier1.
		tier, known := TierForCountry(c.code)
		if !known || tier != c.tier {
			t.Fatalf("[%s] precondition: %s expected tier=%d known, got tier=%d known=%v",
				runUUID, c.code, c.tier, tier, known)
		}
		if tier == Tier1 {
			t.Fatalf("[%s] precondition: %s is Tier1, not a restricted/embargoed jurisdiction", runUUID, c.code)
		}

		err := VerifyOnboarding(NodeOnboarding{CountryCode: c.code, GPUModel: advControlledGPU})
		if !errors.Is(err, ErrExportDenied) {
			t.Fatalf("[%s] COMPLIANCE BUG: controlled GPU %q from EMBARGOED/RESTRICTED %s (Tier%d) "+
				"was ALLOWED (err=%v) — restricted export flow permitted",
				runUUID, advControlledGPU, c.code, c.tier, err)
		}
	}
	t.Logf("[%s] delta: all %d restricted/embargoed jurisdictions BLOCKED a controlled GPU (correct)",
		runUUID, len(embargoedAndRestricted))
}

// TestADV_EmbargoCaseAndWhitespaceStillBlocked proves the embargo deny survives
// the case/whitespace normalization path. The documented contract is that
// TierForCountry normalizes via ToUpper(TrimSpace(...)) — so "ir", " IR ", and
// "Ir\t" MUST all still resolve to the SAME embargoed tier and be BLOCKED. The
// danger this guards: a normalization that *leaked* (e.g. exact-only match that
// missed "ir") would let an embargoed node slip through as "unknown-but-treated-
// -as-Tier1" under a weaker policy. Here it must stay denied either way.
//
// BITES because: these are real embargoed identities; allowing any is a
// restricted flow permitted.
func TestADV_EmbargoCaseAndWhitespaceStillBlocked(t *testing.T) {
	const runUUID = "exportcontrol-adv-5b9c0f12-embargo-normalized"

	// All of these are the SAME embargoed country IR (Iran, Tier3).
	variants := []string{"ir", "iR", "Ir", " IR", "IR ", "  IR  ", "IR\t", "\tIR\n"}
	for _, v := range variants {
		tier, known := TierForCountry(v)
		if !known || tier != Tier3 {
			t.Fatalf("[%s] precondition: %q must normalize to IR=Tier3, got tier=%d known=%v",
				runUUID, v, tier, known)
		}
		err := VerifyOnboarding(NodeOnboarding{CountryCode: v, GPUModel: advControlledGPU})
		if !errors.Is(err, ErrExportDenied) {
			t.Fatalf("[%s] COMPLIANCE BUG: embargoed IR spelled %q ALLOWED a controlled GPU (err=%v)",
				runUUID, v, err)
		}
	}
	t.Logf("[%s] delta: %d case/whitespace spellings of embargoed IR all BLOCKED (correct)",
		runUUID, len(variants))
}

// TestADV_CountryLookalikeNoBypass is the identity-confusion anti-bypass for the
// country code. The catalog lookup MUST be EXACT (after normalization): a code
// that merely CONTAINS, is a SUPERSTRING of, or visually resembles an allowed
// Tier1 code MUST NOT inherit Tier1's permission. Each of these is NOT a real
// Tier1 code, so it is unknown -> fail-closed -> a controlled GPU MUST be denied.
//
// Critically, an attacker's goal here is the REVERSE of the embargo test: take a
// permitted Tier1 code ("US") and smuggle a controlled GPU in under a lookalike
// ("USA", "US ", "XUS", "uss"). The exact-match + fail-closed policy must reject
// all of them.
//
// BITES because: if TierForCountry used substring/Contains or prefix matching
// against the Tier1 set, "USA"/"XUS"/"USX" would resolve to US=Tier1 and the
// controlled GPU would be ALLOWED -> restricted flow permitted -> fail.
func TestADV_CountryLookalikeNoBypass(t *testing.T) {
	const runUUID = "exportcontrol-adv-7a2e3b34-country-lookalike"

	// None of these are catalogued. They are lookalikes/superstrings/substrings
	// of real codes (US/RS/CN/IR), unicode confusables, or alias attempts.
	lookalikes := []string{
		"USA",  // superstring of allowed US
		"XUS",  // US embedded (prefix junk)
		"USX",  // US embedded (suffix junk)
		"US1",  // US + digit
		"U.S",  // punctuation
		"U S",  // embedded space (not trimmed internally)
		"RSX",  // superstring of allowed RS (Serbia)
		"XRS",  // RS embedded
		"USSR", // historical alias, not a current ISO code, contains "US"
		"İR",   // Turkish dotted capital I + R — unicode confusable of IR
		"ＩＲ",   // fullwidth I R — unicode confusable of IR
		"Iran", // full country name (alias), not the ISO code
		"PRC",  // alias for China, not the ISO code CN
		"USA ", // superstring + trailing space
	}
	for _, code := range lookalikes {
		tier, known := TierForCountry(code)
		if known {
			t.Fatalf("[%s] BYPASS: lookalike %q resolved to a KNOWN tier=%d — exact-match broken; "+
				"an attacker could smuggle a controlled GPU via this code",
				runUUID, code, tier)
		}
		err := VerifyOnboarding(NodeOnboarding{CountryCode: code, GPUModel: advControlledGPU})
		if !errors.Is(err, ErrExportDenied) {
			t.Fatalf("[%s] COMPLIANCE BUG: controlled GPU via lookalike country %q was ALLOWED (err=%v) "+
				"— country identity-confusion bypass", runUUID, code, err)
		}
	}
	t.Logf("[%s] delta: %d country lookalikes/confusables/aliases all unknown -> controlled GPU BLOCKED (correct)",
		runUUID, len(lookalikes))
}

// TestADV_EmbargoedCodeSubstringInTier1NoLeak guards the opposite confusion: an
// embargoed code that is a substring of an allowed code, or vice versa, must not
// let the allowed code's permission leak to the embargoed one (or the reverse).
// "CU" (Cuba, Tier3) is a substring of nothing allowed here, but we assert the
// principle directly: the embargoed exact code stays denied, and a Tier1 code
// that happens to share characters stays allowed — proving membership is by the
// whole normalized key, not by character overlap.
func TestADV_EmbargoedCodeSubstringInTier1NoLeak(t *testing.T) {
	const runUUID = "exportcontrol-adv-9f4a1c56-substring-no-leak"

	// Embargoed exact code -> blocked.
	if err := VerifyOnboarding(NodeOnboarding{CountryCode: "CU", GPUModel: advControlledGPU}); !errors.Is(err, ErrExportDenied) {
		t.Fatalf("[%s] COMPLIANCE BUG: embargoed CU(Tier3) ALLOWED controlled GPU (err=%v)", runUUID, err)
	}
	// A genuine Tier1 code is allowed — confirms we are not blanket-denying.
	if err := VerifyOnboarding(NodeOnboarding{CountryCode: "CA", GPUModel: advControlledGPU}); err != nil {
		t.Fatalf("[%s] over-block: Tier1 CA should allow controlled GPU, got err=%v", runUUID, err)
	}
	// "CUX" / "XCU" must NOT inherit CU's classification (or any) — unknown, denied.
	for _, code := range []string{"CUX", "XCU", "CUB", "CUBA"} {
		if _, known := TierForCountry(code); known {
			t.Fatalf("[%s] BYPASS: %q resolved KNOWN — substring leak from CU", runUUID, code)
		}
		if err := VerifyOnboarding(NodeOnboarding{CountryCode: code, GPUModel: advControlledGPU}); !errors.Is(err, ErrExportDenied) {
			t.Fatalf("[%s] COMPLIANCE BUG: controlled GPU via %q ALLOWED (err=%v)", runUUID, code, err)
		}
	}
	t.Logf("[%s] delta: embargoed CU blocked, Tier1 CA allowed, CU-lookalikes unknown->blocked (no substring leak)", runUUID)
}

// TestADV_ControlledGPUSubstringMatchHolds is the GPU-side anti-bypass. The
// documented matching for IsControlledGPU is case-insensitive SUBSTRING — so a
// controlled core ("H100") embedded in vendor noise MUST still be classed
// controlled and, from an embargoed country, BLOCKED. This is the dangerous
// direction: an attacker must not be able to dress up an H100 so it reads as
// non-controlled and slip it into an embargoed node.
//
// BITES because: if IsControlledGPU were weakened to exact-match (==) instead of
// Contains, "NVIDIA H100 80GB" would read as NON-controlled, VerifyOnboarding
// would short-circuit to nil, and an H100 would onboard in IRAN -> the single
// worst compliance failure. This test catches exactly that.
func TestADV_ControlledGPUSubstringMatchHolds(t *testing.T) {
	const runUUID = "exportcontrol-adv-2c6d8e78-gpu-substring"

	controlledDressedUp := []string{
		"NVIDIA H100 80GB",
		"nvidia h100 sxm5",        // lowercase
		"PNY-A100-PCIE-40GB",      // A100 buried
		"server-node-7x-H800-pkg", // H800 buried
		"GH200 Grace Hopper",      // GH200
		"  B200  ",                // padded B200
	}
	for _, model := range controlledDressedUp {
		if !IsControlledGPU(model) {
			t.Fatalf("[%s] precondition: %q must be classed CONTROLLED (substring match)", runUUID, model)
		}
		// From the most embargoed jurisdiction (IR, Tier3) this MUST be denied.
		err := VerifyOnboarding(NodeOnboarding{CountryCode: "IR", GPUModel: model})
		if !errors.Is(err, ErrExportDenied) {
			t.Fatalf("[%s] COMPLIANCE BUG: controlled GPU %q onboarded from EMBARGOED IR (err=%v) "+
				"— controlled accelerator slipped into an embargoed node", runUUID, model, err)
		}
	}
	t.Logf("[%s] delta: %d dressed-up controlled GPUs all detected and BLOCKED from embargoed IR (correct)",
		runUUID, len(controlledDressedUp))
}

// TestADV_NonControlledNotOverBlocked pins the allow side of the GPU classifier:
// genuinely non-controlled GPUs (no catalogued substring) must be ALLOWED even
// from an embargoed country, per the documented scope. This proves the policy is
// not a blanket country ban — and that the embargo blocks above are caused by
// the controlled-GPU classification, not by denying everything (anti-bluff: if
// the package just denied all IR traffic, this test would fail).
func TestADV_NonControlledNotOverBlocked(t *testing.T) {
	const runUUID = "exportcontrol-adv-3e7f9a8a-noncontrolled-allowed"

	nonControlled := []string{
		"RTX 4090",
		"Tesla T4",
		"L40S",
		"A10",       // NOT A100 — must not false-positive on substring "A10"
		"H10",       // NOT H100
		"h 100",     // space breaks the H100 substring
		"AMD MI300", // different vendor, not catalogued
	}
	for _, model := range nonControlled {
		if IsControlledGPU(model) {
			t.Fatalf("[%s] FALSE-POSITIVE: %q classed controlled — over-block risk", runUUID, model)
		}
		if err := VerifyOnboarding(NodeOnboarding{CountryCode: "IR", GPUModel: model}); err != nil {
			t.Fatalf("[%s] over-block: non-controlled %q from IR should be allowed, got err=%v", runUUID, model, err)
		}
	}
	t.Logf("[%s] delta: %d non-controlled GPUs allowed even from IR (policy scoped to controlled GPUs, not a blanket ban)",
		runUUID, len(nonControlled))
}

// TestADV_FailClosedEdges covers empty/garbage/unknown inputs. The documented
// policy is fail-closed: a controlled GPU with an empty or unknown country code
// MUST be denied (not allowed by default), and no input may panic.
//
// BITES because: empty/unknown country with a controlled GPU is the classic
// "missing field defaults to permit" hole; the test asserts it is denied.
func TestADV_FailClosedEdges(t *testing.T) {
	const runUUID = "exportcontrol-adv-6a8b0c9c-fail-closed"

	// Controlled GPU + empty/unknown/garbage country -> MUST deny (fail-closed).
	denyCountries := []string{"", "   ", "\t", "ZZ", "??", "1", "Tier1", "TIER1", "ALL"}
	for _, cc := range denyCountries {
		err := VerifyOnboarding(NodeOnboarding{CountryCode: cc, GPUModel: advControlledGPU})
		if !errors.Is(err, ErrExportDenied) {
			t.Fatalf("[%s] FAIL-OPEN: controlled GPU with country %q was ALLOWED (err=%v) — not fail-closed",
				runUUID, cc, err)
		}
	}

	// Fully empty onboarding (zero value): GPUModel "" is non-controlled, so the
	// documented policy ALLOWS it (no controlled accelerator involved). Assert no
	// panic and the documented nil result.
	if err := VerifyOnboarding(NodeOnboarding{}); err != nil {
		t.Fatalf("[%s] zero-value onboarding (empty GPU = non-controlled) should be nil per policy, got err=%v",
			runUUID, err)
	}

	// Empty GPU from an embargoed country: still non-controlled -> allowed, no panic.
	if err := VerifyOnboarding(NodeOnboarding{CountryCode: "IR", GPUModel: ""}); err != nil {
		t.Fatalf("[%s] empty GPU from IR is non-controlled -> allowed per policy, got err=%v", runUUID, err)
	}
	t.Logf("[%s] delta: %d empty/unknown/garbage countries with a controlled GPU all DENIED (fail-closed); empty GPU non-controlled -> allowed, no panic",
		runUUID, len(denyCountries))
}

// TestADV_MutationGuide documents (and self-checks) WHY the embargo assertions
// bite. It re-asserts the two highest-value invariants in one place so the
// anti-bluff reasoning is executable, not just prose:
//
//  1. default-allow mutation in VerifyOnboarding (drop the country guard) =>
//     {IR, H100} returns nil. TestADV_AllInList_EmbargoIsHard and this test fail.
//  2. exact-match -> substring/case mutation in TierForCountry's spirit: a
//     lookalike "USA" must NOT be Tier1. TestADV_CountryLookalikeNoBypass fails
//     if that mutation is introduced.
//  3. Contains -> == mutation in IsControlledGPU => "NVIDIA H100 80GB" reads
//     non-controlled => {IR, "NVIDIA H100 80GB"} allowed.
//     TestADV_ControlledGPUSubstringMatchHolds fails.
func TestADV_MutationGuide(t *testing.T) {
	const runUUID = "exportcontrol-adv-8c0d2eae-mutation-guide"

	// (1) Embargoed + controlled must deny.
	if err := VerifyOnboarding(NodeOnboarding{CountryCode: "IR", GPUModel: advControlledGPU}); !errors.Is(err, ErrExportDenied) {
		t.Fatalf("[%s] guard(1): {IR,H100} not denied (err=%v) — default-allow mutation present", runUUID, err)
	}
	// (2) Lookalike of an allowed code must not be Tier1.
	if tier, known := TierForCountry("USA"); known && tier == Tier1 {
		t.Fatalf("[%s] guard(2): lookalike USA resolved Tier1 — substring/prefix match mutation present", runUUID)
	}
	// (3) Buried controlled core must be detected.
	if !IsControlledGPU("NVIDIA H100 80GB") {
		t.Fatalf("[%s] guard(3): buried H100 not detected — Contains->== mutation present", runUUID)
	}
	t.Logf("[%s] delta: all three load-bearing invariants hold; documented mutations would each flip a restricted flow to ALLOWED",
		runUUID)
}
