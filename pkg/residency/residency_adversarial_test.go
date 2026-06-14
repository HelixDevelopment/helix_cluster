package residency

import (
	"strings"
	"testing"
)

// This file is an adversarial, compliance-gate audit of the residency policy
// evaluator. It pins the "negative space" of the contract — the cases where a
// bug would let data land in a region its classification forbids.
//
// Centerpieces:
//   - DENY PRECEDENCE is order-independent (deny ALWAYS beats allow, no matter
//     how the Rule's slices or the rule set are declared).
//   - EXACT region/class matching (no substring / case-fold lookalike bypass).
//   - PER-CLASS ISOLATION (an allow for one class never leaks to another).
//   - UNKNOWN class/region fail to the documented stance (fail-closed when
//     defaultAllow=false; a non-allowlisted region under a constrained class is
//     denied).
//
// Each test documents the canonical PRODUCTION mutation that makes it bite; the
// anti-bluff harness (separate run) applies one and confirms the failure.

// adv is a local assertion helper that pins concrete (Allow, Rule) and a
// required Reason substring — never len>0 / err==nil bluffs.
func adv(t *testing.T, label string, got Decision, wantAllow bool, wantRule, wantReasonSubstr string) {
	t.Helper()
	if got.Allow != wantAllow {
		t.Errorf("%s: Allow = %v; want %v (Rule=%q Reason=%q)", label, got.Allow, wantAllow, got.Rule, got.Reason)
	}
	if got.Rule != wantRule {
		t.Errorf("%s: Rule = %q; want %q (Reason=%q)", label, got.Rule, wantRule, got.Reason)
	}
	if !strings.Contains(got.Reason, wantReasonSubstr) {
		t.Errorf("%s: Reason %q does not contain %q", label, got.Reason, wantReasonSubstr)
	}
}

// ---- TestDenyPrecedenceOrderIndependent (CENTERPIECE) ----------------------
//
// A region that is in BOTH AllowedRegions and DeniedRegions MUST be denied,
// and that outcome MUST NOT depend on:
//   - the order the regions appear within their slices, nor
//   - whether the allow or the deny list is declared "first".
//
// Mutation proof: move the DeniedRegions check below the AllowedRegions check
// (invert precedence so allow wins) in residency.go → every sub-case here
// flips to Allow=true / Rule="allowed-region" and FAILS, proving data would be
// admitted into an explicitly-denied region.
func TestDenyPrecedenceOrderIndependent(t *testing.T) {
	// Same conflicting region "eu-west" expressed four different ways. All must DENY.
	variants := []struct {
		name string
		rule Rule
	}{
		{
			name: "allow_then_deny_single",
			rule: Rule{DataClass: ClassPII, AllowedRegions: []string{"eu-west"}, DeniedRegions: []string{"eu-west"}},
		},
		{
			name: "deny_region_first_in_multi_allow",
			rule: Rule{DataClass: ClassPII, AllowedRegions: []string{"eu-west", "eu-central", "us-east"}, DeniedRegions: []string{"eu-west"}},
		},
		{
			name: "deny_region_last_in_multi_allow",
			rule: Rule{DataClass: ClassPII, AllowedRegions: []string{"eu-central", "us-east", "eu-west"}, DeniedRegions: []string{"ap-south", "eu-west"}},
		},
		{
			name: "deny_region_amid_multi_deny",
			rule: Rule{DataClass: ClassPII, AllowedRegions: []string{"eu-west"}, DeniedRegions: []string{"cn-north", "eu-west", "ru-central"}},
		},
	}
	for _, v := range variants {
		v := v
		t.Run(v.name, func(t *testing.T) {
			policy := NewResidencyPolicy([]Rule{v.rule}, false)
			d := policy.Evaluate(ClassPII, "eu-west", "cell-dp")
			adv(t, v.name, d, false, "denied-region", `"eu-west" is explicitly denied`)
			if !strings.Contains(d.Reason, "cell-dp") {
				t.Errorf("%s: targetCell missing from Reason %q", v.name, d.Reason)
			}
		})
	}
}

// ---- TestDenyPrecedenceMultiRuleNotShadowed --------------------------------
//
// Even when an EARLIER rule for the same class would allow the region, the
// FIRST matching rule is the one that governs — and within it deny wins. This
// guards the interaction of first-match-wins with deny-precedence: a permissive
// rule placed first must not be able to launder a region that its own deny list
// forbids.
//
// Mutation proof: invert deny precedence (allow-before-deny) → this denies-case
// flips to Allow and FAILS.
func TestDenyPrecedenceFirstRuleDenies(t *testing.T) {
	policy := NewResidencyPolicy([]Rule{
		// First (governing) rule: allows eu-* broadly but explicitly denies eu-west.
		{DataClass: ClassPII, AllowedRegions: []string{"eu-west", "eu-central"}, DeniedRegions: []string{"eu-west"}},
		// A later, more permissive rule that would allow eu-west if it ever fired.
		{DataClass: ClassPII, AllowedRegions: []string{"eu-west"}},
	}, false)

	d := policy.Evaluate(ClassPII, "eu-west", "cell-fr")
	adv(t, "first-rule-denies", d, false, "denied-region", `"eu-west" is explicitly denied`)
}

// ---- TestExactRegionMatchNoLookalikeBypass (CENTERPIECE) -------------------
//
// Region codes are matched EXACTLY. A lookalike (substring, prefix/suffix, or
// case-variant) of an allowed region MUST NOT be admitted, and a lookalike of a
// denied region MUST NOT escape the deny.
//
// Mutation proof: change containsRegion from `h == needle` to
// `strings.Contains(h, needle)` (or strings.HasPrefix) → the substring/prefix
// lookalikes below get admitted and these sub-cases FAIL; a case-fold
// (EqualFold) mutation makes the case-variant sub-cases FAIL.
func TestExactRegionMatchNoLookalikeBypass(t *testing.T) {
	policy := NewResidencyPolicy([]Rule{
		{DataClass: ClassPII, AllowedRegions: []string{"eu-west"}, DeniedRegions: []string{"cn-north"}},
	}, false)

	// Lookalikes of the ALLOWED region "eu-west" — none may be admitted.
	allowLookalikes := []string{
		"eu-wes",    // prefix substring of allowed
		"eu-west-1", // allowed is a prefix of this
		"xeu-west",  // allowed is a suffix substring of this
		"EU-WEST",   // upper-case variant
		"Eu-West",   // mixed-case variant
		"eu_west",   // separator variant
		" eu-west",  // leading whitespace
		"eu-west ",  // trailing whitespace
	}
	for _, region := range allowLookalikes {
		region := region
		t.Run("allow_lookalike_denied/"+region, func(t *testing.T) {
			d := policy.Evaluate(ClassPII, region, "cell-x")
			// Not in the (exact) allowlist and not denied-listed → region-not-allowed.
			adv(t, "lookalike "+region, d, false, "region-not-allowed", region)
		})
	}

	// Lookalikes of the DENIED region "cn-north" — none may escape the deny via
	// an exact-match-that-isn't. They simply aren't the denied string, so they
	// fall through to region-not-allowed (still DENIED, just a different rule).
	denyLookalikes := []string{"cn-nort", "cn-north-2", "CN-NORTH"}
	for _, region := range denyLookalikes {
		region := region
		t.Run("deny_lookalike_not_admitted/"+region, func(t *testing.T) {
			d := policy.Evaluate(ClassPII, region, "cell-y")
			if d.Allow {
				t.Errorf("deny lookalike %q was ADMITTED (Rule=%q Reason=%q)", region, d.Rule, d.Reason)
			}
		})
	}

	// Positive control: the EXACT allowed region IS admitted (guards an
	// always-deny mutation of containsRegion).
	t.Run("exact_allow_admitted", func(t *testing.T) {
		d := policy.Evaluate(ClassPII, "eu-west", "cell-ok")
		adv(t, "exact allow", d, true, "allowed-region", "eu-west")
	})
	// Positive control: the EXACT denied region IS denied with the deny rule.
	t.Run("exact_deny_blocked", func(t *testing.T) {
		d := policy.Evaluate(ClassPII, "cn-north", "cell-no")
		adv(t, "exact deny", d, false, "denied-region", `"cn-north" is explicitly denied`)
	})
}

// ---- TestClassMatchNoLookalikeBypass ---------------------------------------
//
// DataClass is matched EXACTLY too. A lookalike class string (case/substring of
// a known class) does NOT inherit that class's rule — it falls through to the
// default stance. With defaultAllow=false that is a DENY (fail-closed), so a
// "PII"/"pii-extra" spoof cannot pick up ClassPII's allowlist.
//
// Mutation proof: change the rule-match `r.DataClass != class` to a
// case-insensitive / substring compare → "PII" picks up the ClassPII rule and
// the default-deny assertions FAIL.
func TestClassMatchNoLookalikeBypass(t *testing.T) {
	policy := NewResidencyPolicy([]Rule{
		{DataClass: ClassPII, AllowedRegions: []string{"eu-west"}},
	}, false) // fail-closed default

	lookalikeClasses := []DataClass{"PII", "Pii", "pii-extra", "xpii", "p"}
	for _, c := range lookalikeClasses {
		c := c
		t.Run("class_lookalike_default_deny/"+string(c), func(t *testing.T) {
			// Even targeting eu-west (which the real ClassPII rule allows), a
			// lookalike class must NOT inherit that allowance.
			d := policy.Evaluate(c, "eu-west", "cell-c")
			adv(t, "class lookalike "+string(c), d, false, "default-deny", "no rule")
		})
	}

	// Positive control: the exact class DOES match its rule.
	t.Run("exact_class_matches", func(t *testing.T) {
		d := policy.Evaluate(ClassPII, "eu-west", "cell-c-ok")
		adv(t, "exact class", d, true, "allowed-region", "eu-west")
	})
}

// ---- TestPerClassIsolation (CENTERPIECE) -----------------------------------
//
// Each class's allow/deny lists are independent. A region allowed for one class
// MUST NOT thereby be allowed for another. Here ap-southeast is open for Public
// but absent from PII's allowlist and is in PHI's deny list.
//
// Mutation proof: make the rule loop ignore DataClass (match the first rule
// unconditionally) → PII/PHI start honoring Public's open allowlist and the
// deny/region-not-allowed assertions FAIL.
func TestPerClassIsolation(t *testing.T) {
	policy := NewResidencyPolicy([]Rule{
		{DataClass: ClassPublic}, // open allowlist: Public allowed anywhere
		{DataClass: ClassPII, AllowedRegions: []string{"eu-west"}},
		{DataClass: ClassPHI, AllowedRegions: []string{"us-east"}, DeniedRegions: []string{"ap-southeast"}},
	}, false)

	t.Run("public_apse_allowed", func(t *testing.T) {
		d := policy.Evaluate(ClassPublic, "ap-southeast", "cell-pi1")
		adv(t, "public apse", d, true, "open-allowlist", "no region restriction")
	})
	t.Run("pii_apse_not_allowed", func(t *testing.T) {
		// ap-southeast is allowed for Public but NOT for PII.
		d := policy.Evaluate(ClassPII, "ap-southeast", "cell-pi2")
		adv(t, "pii apse", d, false, "region-not-allowed", "ap-southeast")
	})
	t.Run("phi_apse_denied", func(t *testing.T) {
		// ap-southeast is allowed for Public but DENIED for PHI.
		d := policy.Evaluate(ClassPHI, "ap-southeast", "cell-pi3")
		adv(t, "phi apse", d, false, "denied-region", `"ap-southeast" is explicitly denied`)
	})
	t.Run("pii_eu-west_allowed_independent", func(t *testing.T) {
		// eu-west allowed for PII must not be affected by other classes' lists.
		d := policy.Evaluate(ClassPII, "eu-west", "cell-pi4")
		adv(t, "pii eu-west", d, true, "allowed-region", "eu-west")
	})
}

// ---- TestUnknownRegionFailsClosedUnderConstrainedClass ---------------------
//
// An UNKNOWN/unrecognized region under a class that carries a non-empty
// allowlist must be DENIED (the allowlist is a closed set). This is the
// data-residency fail-closed guarantee for regions: only enumerated regions are
// admitted; everything else — including never-before-seen region strings — is
// refused.
//
// Mutation proof: in the region-not-allowed branch flip Allow:false→true (or
// treat a non-empty allowlist as open) → these unknown regions get admitted and
// the test FAILS, demonstrating PII data placed in an unrecognized region.
func TestUnknownRegionFailsClosedUnderConstrainedClass(t *testing.T) {
	policy := NewResidencyPolicy([]Rule{
		{DataClass: ClassPII, AllowedRegions: []string{"eu-west", "eu-central"}, Jurisdiction: "EU"},
	}, false)

	unknownRegions := []string{"", "mars-1", "us-east", "unknown", "eu", "global"}
	for _, region := range unknownRegions {
		region := region
		t.Run("unknown_region_denied/"+region, func(t *testing.T) {
			d := policy.Evaluate(ClassPII, region, "cell-ur")
			if d.Allow {
				t.Errorf("unknown region %q ADMITTED for PII (Rule=%q Reason=%q)", region, d.Rule, d.Reason)
			}
			if d.Rule != "region-not-allowed" {
				t.Errorf("unknown region %q: Rule = %q; want region-not-allowed", region, d.Rule)
			}
			// Jurisdiction-aware diagnostics must survive on the deny path.
			if !strings.Contains(d.Reason, "EU") {
				t.Errorf("unknown region %q: jurisdiction label missing from Reason %q", region, d.Reason)
			}
		})
	}
}

// ---- TestUnknownClassFailsClosed (CENTERPIECE) -----------------------------
//
// An unrecognized DataClass under a fail-closed policy (defaultAllow=false) MUST
// be DENIED — including for an otherwise-permissive region. This is the
// fail-closed default for classification: data whose class the policy does not
// understand is never admitted by accident.
//
// Mutation proof: flip `if p.defaultAllow` to `if !p.defaultAllow` (or hardcode
// the no-rule fallthrough to Allow:true) → the unknown class is admitted and
// this test FAILS.
func TestUnknownClassFailsClosed(t *testing.T) {
	policy := NewResidencyPolicy([]Rule{
		{DataClass: ClassPII, AllowedRegions: []string{"eu-west"}},
	}, false)

	unknownClasses := []DataClass{"", "secret", "restricted", "PUBLIC", "Public", "phi-2"}
	for _, c := range unknownClasses {
		c := c
		t.Run("unknown_class_denied/"+string(c), func(t *testing.T) {
			// eu-west is permissive for the real ClassPII rule; an unknown class
			// must not borrow that and must hit fail-closed default-deny.
			d := policy.Evaluate(c, "eu-west", "cell-uc")
			adv(t, "unknown class "+string(c), d, false, "default-deny", "no rule")
		})
	}
}

// ---- TestGenuineAllowBaseline ----------------------------------------------
//
// Clean positive baseline across every class: a permitted (class, region) IS
// allowed. This is the canary for an always-deny mutation — if someone makes
// containsRegion always return false, or hardcodes Allow:false, these fail.
func TestGenuineAllowBaseline(t *testing.T) {
	policy := NewResidencyPolicy([]Rule{
		{DataClass: ClassPublic},
		{DataClass: ClassPII, AllowedRegions: []string{"eu-west", "eu-central"}},
		{DataClass: ClassPHI, AllowedRegions: []string{"us-east"}, Jurisdiction: "HIPAA-US"},
	}, false)

	cases := []struct {
		class  DataClass
		region string
		rule   string
		reason string
	}{
		{ClassPublic, "ap-southeast", "open-allowlist", "no region restriction"},
		{ClassPII, "eu-west", "allowed-region", "eu-west"},
		{ClassPII, "eu-central", "allowed-region", "eu-central"},
		{ClassPHI, "us-east", "allowed-region", "us-east"},
	}
	for _, c := range cases {
		c := c
		t.Run(string(c.class)+"_"+c.region, func(t *testing.T) {
			d := policy.Evaluate(c.class, c.region, "cell-ok")
			adv(t, "baseline allow", d, true, c.rule, c.reason)
		})
	}
}
