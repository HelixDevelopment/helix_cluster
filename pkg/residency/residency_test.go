package residency

import (
	"strings"
	"testing"
)

// ---- helpers ----------------------------------------------------------------

// assertDecision checks Allow, a required Reason substring, and the Rule name.
// It does NOT use len>0 / err==nil alone — it asserts concrete values.
func assertDecision(t *testing.T, label string, got Decision, wantAllow bool, wantReasonSubstr, wantRule string) {
	t.Helper()
	if got.Allow != wantAllow {
		t.Errorf("%s: Allow = %v; want %v (Reason: %q)", label, got.Allow, wantAllow, got.Reason)
	}
	if !strings.Contains(got.Reason, wantReasonSubstr) {
		t.Errorf("%s: Reason %q does not contain %q", label, got.Reason, wantReasonSubstr)
	}
	if got.Rule != wantRule {
		t.Errorf("%s: Rule = %q; want %q", label, got.Rule, wantRule)
	}
}

// ---- TestPIIAllowedRegions --------------------------------------------------
// Mutation proof: change `return Decision{Allow: false, …}` in the
// "region-not-allowed" branch to `return Decision{Allow: true, …}` →
// the us-east deny sub-case fails (still compiles).

func TestPIIAllowedRegions(t *testing.T) {
	policy := NewResidencyPolicy([]Rule{
		{
			DataClass:      ClassPII,
			AllowedRegions: []string{"eu-west", "eu-central"},
		},
	}, false)

	t.Run("pii_eu-west_allow", func(t *testing.T) {
		// PII placed in eu-west — region is in AllowedRegions → Allow.
		d := policy.Evaluate(ClassPII, "eu-west", "cell-1")
		assertDecision(t, "pii eu-west", d, true, "eu-west", "allowed-region")
		// Traceability contract: targetCell MUST appear in Reason.
		// Mutation proof: remove targetCell from the allowed-region fmt.Sprintf in
		// residency.go → this assertion fails (still compiles).
		if !strings.Contains(d.Reason, "cell-1") {
			t.Errorf("pii eu-west: targetCell %q missing from Reason %q", "cell-1", d.Reason)
		}
	})

	t.Run("pii_us-east_deny", func(t *testing.T) {
		// PII placed in us-east — not in AllowedRegions=[eu-west,eu-central] → Deny.
		// The Reason MUST name the region that was not allowed.
		d := policy.Evaluate(ClassPII, "us-east", "cell-2")
		assertDecision(t, "pii us-east", d, false, "us-east", "region-not-allowed")
		// Traceability contract: targetCell MUST appear in Reason.
		// Mutation proof: remove targetCell from the region-not-allowed fmt.Sprintf in
		// residency.go → this assertion fails (still compiles).
		if !strings.Contains(d.Reason, "cell-2") {
			t.Errorf("pii us-east: targetCell %q missing from Reason %q", "cell-2", d.Reason)
		}
	})
}

// ---- TestPublicOpenAllowlist ------------------------------------------------
// Mutation proof: remove the `len(r.AllowedRegions) == 0` branch (or add a
// non-empty AllowedRegions to the public rule) → Allow becomes false for
// unrecognized regions (still compiles).

func TestPublicOpenAllowlist(t *testing.T) {
	// public rule: AllowedRegions empty → allow anywhere.
	policy := NewResidencyPolicy([]Rule{
		{DataClass: ClassPublic, AllowedRegions: []string{}},
	}, false)

	regions := []string{"eu-west", "us-east", "ap-southeast", "sa-east"}
	for _, region := range regions {
		d := policy.Evaluate(ClassPublic, region, "cell-x")
		assertDecision(t, "public "+region, d, true, "no region restriction", "open-allowlist")
		// Traceability contract: targetCell MUST appear in Reason.
		// Mutation proof: remove targetCell from the open-allowlist fmt.Sprintf in
		// residency.go → this assertion fails (still compiles).
		if !strings.Contains(d.Reason, "cell-x") {
			t.Errorf("public %s: targetCell %q missing from Reason %q", region, "cell-x", d.Reason)
		}
	}
}

// ---- TestDeniedRegionPrecedence --------------------------------------------
// Mutation proof: remove the `containsRegion(r.DeniedRegions, …)` check
// (skip deny-region evaluation) → eu-west returns Allow=true (still compiles).

func TestDeniedRegionPrecedence(t *testing.T) {
	// PII: Allowed=[eu-west], Denied=[eu-west] → eu-west must be DENIED because
	// deny-region wins over allow-region.
	policy := NewResidencyPolicy([]Rule{
		{
			DataClass:      ClassPII,
			AllowedRegions: []string{"eu-west"},
			DeniedRegions:  []string{"eu-west"},
		},
	}, false)

	d := policy.Evaluate(ClassPII, "eu-west", "cell-3")
	// Must deny AND the reason must name the denied region.
	assertDecision(t, "deny-precedence eu-west", d, false, `"eu-west" is explicitly denied`, "denied-region")
	// Traceability contract: targetCell MUST appear in Reason.
	// Mutation proof: remove targetCell from the denied-region fmt.Sprintf in
	// residency.go → this assertion fails (still compiles).
	if !strings.Contains(d.Reason, "cell-3") {
		t.Errorf("deny-precedence: targetCell %q missing from Reason %q", "cell-3", d.Reason)
	}
}

// ---- TestUnknownClassDefault -----------------------------------------------
// Mutation proof: flip `if p.defaultAllow` to `if !p.defaultAllow` →
// defaultAllow=false returns Allow=true and the deny sub-case fails (compiles).

func TestUnknownClassDefault(t *testing.T) {
	const unknownClass DataClass = "top-secret"

	t.Run("unknown_class_default_deny", func(t *testing.T) {
		policy := NewResidencyPolicy(nil, false) // no rules, defaultAllow=false
		d := policy.Evaluate(unknownClass, "eu-west", "cell-4")
		// Allow MUST be false; Reason MUST contain "no rule" (or "unknown").
		assertDecision(t, "unknown deny", d, false, "no rule", "default-deny")
	})

	t.Run("unknown_class_default_allow", func(t *testing.T) {
		policy := NewResidencyPolicy(nil, true) // no rules, defaultAllow=true
		d := policy.Evaluate(unknownClass, "eu-west", "cell-5")
		assertDecision(t, "unknown allow", d, true, "no rule", "default-allow")
	})
}

// ---- TestJurisdictionInReason ----------------------------------------------
// Mutation proof: remove the `if r.Jurisdiction != ""` block from the
// region-not-allowed branch → Reason no longer contains the jurisdiction
// string and this test fails (still compiles).

func TestJurisdictionInReason(t *testing.T) {
	policy := NewResidencyPolicy([]Rule{
		{
			DataClass:      ClassPHI,
			AllowedRegions: []string{"us-east", "us-west"},
			Jurisdiction:   "HIPAA-US",
		},
	}, false)

	t.Run("phi_outside_jurisdiction_deny", func(t *testing.T) {
		// ap-southeast is not in the US-jurisdiction allowlist.
		d := policy.Evaluate(ClassPHI, "ap-southeast", "cell-6")
		assertDecision(t, "phi outside US", d, false, "ap-southeast", "region-not-allowed")
		// Jurisdiction label must appear in the denial reason.
		if !strings.Contains(d.Reason, "HIPAA-US") {
			t.Errorf("jurisdiction label missing from Reason: %q", d.Reason)
		}
		// Traceability contract: targetCell MUST appear in Reason.
		// Mutation proof: remove targetCell from the region-not-allowed fmt.Sprintf in
		// residency.go → this assertion fails (still compiles).
		if !strings.Contains(d.Reason, "cell-6") {
			t.Errorf("phi outside US: targetCell %q missing from Reason %q", "cell-6", d.Reason)
		}
	})

	t.Run("phi_inside_jurisdiction_allow", func(t *testing.T) {
		d := policy.Evaluate(ClassPHI, "us-east", "cell-7")
		assertDecision(t, "phi us-east", d, true, "us-east", "allowed-region")
		if !strings.Contains(d.Reason, "HIPAA-US") {
			t.Errorf("jurisdiction label missing from allow Reason: %q", d.Reason)
		}
		// Traceability contract: targetCell MUST appear in Reason.
		// Mutation proof: remove targetCell from the allowed-region fmt.Sprintf in
		// residency.go → this assertion fails (still compiles).
		if !strings.Contains(d.Reason, "cell-7") {
			t.Errorf("phi us-east: targetCell %q missing from Reason %q", "cell-7", d.Reason)
		}
	})
}

// ---- TestDeniedRegionAloneWithoutAllowed -----------------------------------
// Verifies deny fires for a rule that has DeniedRegions but an empty
// AllowedRegions (open allowlist + explicit deny).
// Mutation proof: swap the deny-region check to after the open-allowlist
// check → eu-west returns Allow=true (still compiles).

func TestDeniedRegionAloneWithoutAllowed(t *testing.T) {
	policy := NewResidencyPolicy([]Rule{
		{
			DataClass:     ClassPII,
			DeniedRegions: []string{"cn-north"},
			// AllowedRegions intentionally empty → open allowlist except for denials
		},
	}, false)

	t.Run("denied_explicit_region", func(t *testing.T) {
		d := policy.Evaluate(ClassPII, "cn-north", "cell-8")
		assertDecision(t, "pii cn-north", d, false, `"cn-north" is explicitly denied`, "denied-region")
		// Traceability contract: targetCell MUST appear in Reason.
		// Mutation proof: remove targetCell from the denied-region fmt.Sprintf in
		// residency.go → this assertion fails (still compiles).
		if !strings.Contains(d.Reason, "cell-8") {
			t.Errorf("pii cn-north: targetCell %q missing from Reason %q", "cell-8", d.Reason)
		}
	})

	t.Run("open_non_denied_region", func(t *testing.T) {
		d := policy.Evaluate(ClassPII, "eu-west", "cell-9")
		assertDecision(t, "pii eu-west open", d, true, "no region restriction", "open-allowlist")
		// Traceability contract: targetCell MUST appear in Reason.
		// Mutation proof: remove targetCell from the open-allowlist fmt.Sprintf in
		// residency.go → this assertion fails (still compiles).
		if !strings.Contains(d.Reason, "cell-9") {
			t.Errorf("pii eu-west open: targetCell %q missing from Reason %q", "cell-9", d.Reason)
		}
	})
}

// ---- TestFirstMatchingRuleWins ---------------------------------------------
// Verifies that when two rules share a DataClass the first one wins.
// Mutation proof: change the first-match loop to last-match → the wrong rule
// fires and the Rule name assertion fails (still compiles).

func TestFirstMatchingRuleWins(t *testing.T) {
	policy := NewResidencyPolicy([]Rule{
		{DataClass: ClassPII, AllowedRegions: []string{"eu-west"}},
		{DataClass: ClassPII, AllowedRegions: []string{"us-east"}},
	}, false)

	// First rule matches: eu-west allowed, us-east NOT allowed by rule 0.
	d := policy.Evaluate(ClassPII, "us-east", "cell-10")
	assertDecision(t, "first-rule wins", d, false, "us-east", "region-not-allowed")
}

// ---- TestNewResidencyPolicyDefensiveCopy -----------------------------------
// Verifies that mutating the original slice after construction does not alter
// the stored policy (defensive copy in NewResidencyPolicy).
// Mutation proof: remove the defensive copy in NewResidencyPolicy →
// the post-mutation Evaluate returns Allow=true (still compiles).

func TestNewResidencyPolicyDefensiveCopy(t *testing.T) {
	regions := []string{"eu-west"}
	rules := []Rule{{DataClass: ClassPII, AllowedRegions: regions}}
	policy := NewResidencyPolicy(rules, false)

	// Mutate original slices.
	regions[0] = "us-east"
	rules[0].AllowedRegions = []string{"us-east"}

	// Policy should still deny us-east (was not in the original allowlist).
	d := policy.Evaluate(ClassPII, "us-east", "cell-11")
	assertDecision(t, "defensive-copy", d, false, "us-east", "region-not-allowed")
}

// ---- TestTableDriven -------------------------------------------------------
// Additional table-driven coverage of exact (Allow, Rule) pairs.
// For every non-default branch (allowed-region, denied-region, open-allowlist,
// region-not-allowed) the targetCell token MUST appear in the Reason string.
// Mutation proof: removing targetCell from any of those fmt.Sprintf calls in
// residency.go breaks the wantCellInReason assertion for that branch (still
// compiles).

func TestTableDriven(t *testing.T) {
	policy := NewResidencyPolicy([]Rule{
		{
			DataClass:      ClassPII,
			AllowedRegions: []string{"eu-west", "eu-central"},
			DeniedRegions:  []string{"eu-central"}, // eu-central is allowed but also denied → deny wins
		},
		{DataClass: ClassPublic},               // open allowlist
		{DataClass: ClassPHI, AllowedRegions: []string{"us-east"}},
	}, false)

	type tc struct {
		name             string
		class            DataClass
		region           string
		cell             string
		wantAllow        bool
		wantRuleID       string
		wantInReason     string
		wantCellInReason bool // true for every branch that embeds targetCell in Reason
	}

	cases := []tc{
		{
			name: "pii_eu-west_allow",
			class: ClassPII, region: "eu-west", cell: "c1",
			wantAllow: true, wantRuleID: "allowed-region", wantInReason: "eu-west",
			wantCellInReason: true,
		},
		{
			// eu-central is in AllowedRegions but also in DeniedRegions → deny wins
			name: "pii_eu-central_denied",
			class: ClassPII, region: "eu-central", cell: "c2",
			wantAllow: false, wantRuleID: "denied-region", wantInReason: "eu-central",
			wantCellInReason: true,
		},
		{
			name: "pii_us-east_not_allowed",
			class: ClassPII, region: "us-east", cell: "c3",
			wantAllow: false, wantRuleID: "region-not-allowed", wantInReason: "us-east",
			wantCellInReason: true,
		},
		{
			name: "public_anywhere",
			class: ClassPublic, region: "ap-southeast", cell: "c4",
			wantAllow: true, wantRuleID: "open-allowlist", wantInReason: "no region restriction",
			wantCellInReason: true,
		},
		{
			name: "phi_us-east_allow",
			class: ClassPHI, region: "us-east", cell: "c5",
			wantAllow: true, wantRuleID: "allowed-region", wantInReason: "us-east",
			wantCellInReason: true,
		},
		{
			name: "phi_eu-west_deny",
			class: ClassPHI, region: "eu-west", cell: "c6",
			wantAllow: false, wantRuleID: "region-not-allowed", wantInReason: "eu-west",
			wantCellInReason: true,
		},
		{
			// default-deny branch: no rule matches, so targetCell is NOT embedded
			// in the Reason (design choice — class and policy stance are reported).
			name: "unknown_default_deny",
			class: "classified", region: "eu-west", cell: "c7",
			wantAllow: false, wantRuleID: "default-deny", wantInReason: "no rule",
			wantCellInReason: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := policy.Evaluate(tc.class, tc.region, tc.cell)
			assertDecision(t, tc.name, d, tc.wantAllow, tc.wantInReason, tc.wantRuleID)
			if tc.wantCellInReason && !strings.Contains(d.Reason, tc.cell) {
				t.Errorf("%s: targetCell %q missing from Reason %q", tc.name, tc.cell, d.Reason)
			}
		})
	}
}
