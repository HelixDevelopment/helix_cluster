package fedtrust

import (
	"strings"
	"testing"
)

// ---- helpers ----------------------------------------------------------------

// assertDecision checks Allow, a required Reason substring, and the Rule name.
// It asserts concrete values — never len>0 / err==nil alone.
func assertDecision(t *testing.T, label string, got Decision, wantAllow bool, wantReasonSubstr, wantRule string) {
	t.Helper()
	if got.Allow != wantAllow {
		t.Errorf("%s: Allow = %v; want %v (Reason: %q, Rule: %q)", label, got.Allow, wantAllow, got.Reason, got.Rule)
	}
	if !strings.Contains(got.Reason, wantReasonSubstr) {
		t.Errorf("%s: Reason %q does not contain %q", label, got.Reason, wantReasonSubstr)
	}
	if got.Rule != wantRule {
		t.Errorf("%s: Rule = %q; want %q", label, got.Rule, wantRule)
	}
}

// ---- TestTrustLevelString ---------------------------------------------------
// Validates that TrustLevel.String() returns the exact labels used in Reason
// strings; concrete equality assertions, not len>0.
//
// Mutation proof: change the TrustHardened case to return "hardened" (lowercase)
// → the "Hardened" sub-case fails (still compiles).

func TestTrustLevelString(t *testing.T) {
	cases := []struct {
		level TrustLevel
		want  string
	}{
		{TrustUnknown, "Unknown"},
		{TrustProvisional, "Provisional"},
		{TrustStandard, "Standard"},
		{TrustHardened, "Hardened"},
		{TrustLevel(99), "TrustLevel(99)"},
	}
	for _, tc := range cases {
		got := tc.level.String()
		if got != tc.want {
			t.Errorf("TrustLevel(%d).String() = %q; want %q", int(tc.level), got, tc.want)
		}
	}
}

// ---- TestAllowed ------------------------------------------------------------
// A cluster in the allowlist, with sufficient trust, and CanBind=true must
// yield Allow=true, rule "allowed".
//
// Mutation proof: change the final return in Evaluate to return
// Decision{Allow: false, …} → this test fails (still compiles).

func TestAllowed(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"prod.example.com", "staging.example.com"},
		DeniedDomains:  []string{"bad.example.com"},
		MinTrustLevel:  TrustStandard,
		CanBind: func(from, to ClusterIdentity) bool {
			// Only allow binding within the same domain.
			return from.TrustDomain == to.TrustDomain
		},
	})

	from := ClusterIdentity{
		Name:        "cluster-a",
		TrustDomain: "prod.example.com",
		TrustLevel:  TrustStandard,
	}
	target := ClusterIdentity{
		Name:        "cluster-b",
		TrustDomain: "prod.example.com",
		TrustLevel:  TrustHardened,
	}

	d := policy.Evaluate(from, target)
	assertDecision(t, "allowed basic", d, true, "cluster-a", "allowed")
	// Reason must also name the target cluster — concrete traceability.
	if !strings.Contains(d.Reason, "cluster-b") {
		t.Errorf("allowed basic: Reason %q does not contain target cluster name %q", d.Reason, "cluster-b")
	}
}

// TestAllowedNilCanBind checks that a nil CanBind (no topology gate) still
// yields Allow when domain and trust-level are satisfied.
//
// Mutation proof: change Precedence 4 to always deny (regardless of
// CanBind nil check) → this test fails (still compiles).

func TestAllowedNilCanBind(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"trusted.example.com"},
		MinTrustLevel:  TrustProvisional,
		CanBind:        nil, // no topology gate
	})

	from := ClusterIdentity{
		Name:        "cluster-x",
		TrustDomain: "trusted.example.com",
		TrustLevel:  TrustProvisional,
	}
	target := ClusterIdentity{
		Name:        "cluster-y",
		TrustDomain: "trusted.example.com",
		TrustLevel:  TrustProvisional,
	}

	d := policy.Evaluate(from, target)
	assertDecision(t, "allowed nil CanBind", d, true, "cluster-x", "allowed")
}

// TestAllowedEmptyAllowedDomains checks that an empty AllowedDomains slice
// skips the domain-allowlist check, allowing any non-denied domain.
//
// Mutation proof: change len(p.allowedDomains) > 0 check to len(p.allowedDomains) >= 0
// (i.e. always apply the allowlist gate) → any non-listed domain is denied
// and this test fails (still compiles).

func TestAllowedEmptyAllowedDomains(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{}, // empty: skip allowlist gate
		DeniedDomains:  []string{"blocked.example.com"},
		MinTrustLevel:  TrustUnknown,
	})

	from := ClusterIdentity{
		Name:        "open-cluster",
		TrustDomain: "any.example.com",
		TrustLevel:  TrustUnknown,
	}
	target := ClusterIdentity{
		Name:        "target-cluster",
		TrustDomain: "any.example.com",
		TrustLevel:  TrustUnknown,
	}

	d := policy.Evaluate(from, target)
	assertDecision(t, "empty allowed domains", d, true, "open-cluster", "allowed")
}

// ---- TestDeniedDomainPrecedence ---------------------------------------------
// A domain present in BOTH AllowedDomains and DeniedDomains must be DENIED
// because denied-domain check (precedence 1) fires before the allowlist check
// (precedence 2).
//
// Mutation proof: swap the denied-domain check to run AFTER the allowlist check
// (i.e. check AllowedDomains first) → the deny-precedence sub-case returns
// Allow=true because the domain IS in the allowlist (still compiles).

func TestDeniedDomainPrecedence(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"overlap.example.com"}, // also in denied
		DeniedDomains:  []string{"overlap.example.com"}, // deny must win
		MinTrustLevel:  TrustUnknown,
	})

	from := ClusterIdentity{
		Name:        "cluster-overlap",
		TrustDomain: "overlap.example.com",
		TrustLevel:  TrustHardened,
	}
	target := ClusterIdentity{
		Name:        "target",
		TrustDomain: "other.example.com",
		TrustLevel:  TrustHardened,
	}

	d := policy.Evaluate(from, target)
	assertDecision(t, "denied-domain wins over allowlist", d, false, "overlap.example.com", "denied-domain")
	// Reason must name the explicit "denied" trigger.
	if !strings.Contains(d.Reason, "explicitly denied") {
		t.Errorf("denied-domain: Reason %q does not contain %q", d.Reason, "explicitly denied")
	}
}

// TestDeniedDomainOnlyInDenied checks the simple case where a domain is only
// in DeniedDomains (not in AllowedDomains).
//
// Mutation proof: remove the denied-domain check entirely from Evaluate →
// the domain passes through to precedence 2 where it is also not in
// AllowedDomains, returning rule "domain-not-allowed" not "denied-domain",
// so the Rule assertion fails (still compiles).

func TestDeniedDomainOnlyInDenied(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"good.example.com"},
		DeniedDomains:  []string{"evil.example.com"},
		MinTrustLevel:  TrustUnknown,
	})

	from := ClusterIdentity{
		Name:        "evil-cluster",
		TrustDomain: "evil.example.com",
		TrustLevel:  TrustHardened,
	}
	target := ClusterIdentity{
		Name:        "good-target",
		TrustDomain: "good.example.com",
		TrustLevel:  TrustHardened,
	}

	d := policy.Evaluate(from, target)
	assertDecision(t, "denied domain simple", d, false, "evil.example.com", "denied-domain")
}

// ---- TestDomainNotAllowed ---------------------------------------------------
// A domain that is absent from a non-empty AllowedDomains must be denied with
// rule "domain-not-allowed".
//
// Mutation proof: change the allowlist check to return Allow=true instead of
// Deny when the domain is not found → this test fails (still compiles).

func TestDomainNotAllowed(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"trusted-a.example.com", "trusted-b.example.com"},
		DeniedDomains:  []string{},
		MinTrustLevel:  TrustUnknown,
	})

	from := ClusterIdentity{
		Name:        "outsider",
		TrustDomain: "untrusted.example.com",
		TrustLevel:  TrustHardened,
	}
	target := ClusterIdentity{
		Name:        "inside",
		TrustDomain: "trusted-a.example.com",
		TrustLevel:  TrustHardened,
	}

	d := policy.Evaluate(from, target)
	assertDecision(t, "domain not allowed", d, false, "untrusted.example.com", "domain-not-allowed")
	// Reason must name the rejected domain so operators know which domain was blocked.
	if !strings.Contains(d.Reason, "untrusted.example.com") {
		t.Errorf("domain-not-allowed: Reason %q does not contain rejected domain", d.Reason)
	}
}

// ---- TestTrustLevel ---------------------------------------------------------
// A cluster whose TrustLevel is below MinTrustLevel must be denied with rule
// "trust-level", and the Reason must name both the presented and required
// levels.
//
// Mutation proof: change `from.TrustLevel < p.minTrustLevel` to
// `from.TrustLevel > p.minTrustLevel` → a below-minimum cluster is allowed
// and this test fails (still compiles).

func TestTrustLevel(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"ok.example.com"},
		MinTrustLevel:  TrustStandard,
	})

	cases := []struct {
		label      string
		level      TrustLevel
		wantAllow  bool
		reasonSubs []string
		wantRule   string
	}{
		{
			// TrustUnknown (0) < TrustStandard (2) → Deny.
			label:      "unknown below minimum",
			level:      TrustUnknown,
			wantAllow:  false,
			reasonSubs: []string{"Unknown", "Standard"},
			wantRule:   "trust-level",
		},
		{
			// TrustProvisional (1) < TrustStandard (2) → Deny.
			label:      "provisional below minimum",
			level:      TrustProvisional,
			wantAllow:  false,
			reasonSubs: []string{"Provisional", "Standard"},
			wantRule:   "trust-level",
		},
		{
			// TrustStandard (2) == TrustStandard (2) → Allow (at the floor).
			label:      "standard meets minimum",
			level:      TrustStandard,
			wantAllow:  true,
			reasonSubs: []string{"cluster-ok"},
			wantRule:   "allowed",
		},
		{
			// TrustHardened (3) > TrustStandard (2) → Allow.
			label:      "hardened above minimum",
			level:      TrustHardened,
			wantAllow:  true,
			reasonSubs: []string{"cluster-ok"},
			wantRule:   "allowed",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.label, func(t *testing.T) {
			from := ClusterIdentity{
				Name:        "cluster-ok",
				TrustDomain: "ok.example.com",
				TrustLevel:  tc.level,
			}
			target := ClusterIdentity{
				Name:        "target",
				TrustDomain: "ok.example.com",
				TrustLevel:  TrustHardened,
			}
			d := policy.Evaluate(from, target)
			assertDecision(t, tc.label, d, tc.wantAllow, tc.reasonSubs[0], tc.wantRule)
			for _, sub := range tc.reasonSubs[1:] {
				if !strings.Contains(d.Reason, sub) {
					t.Errorf("%s: Reason %q does not contain %q", tc.label, d.Reason, sub)
				}
			}
		})
	}
}

// ---- TestTopology -----------------------------------------------------------
// When CanBind returns false, Evaluate must return Deny with rule "topology".
//
// Mutation proof: change Precedence 4 to skip the CanBind call entirely
// (remove the `p.canBind != nil && !p.canBind(from, target)` branch) →
// the topology deny is bypassed and Allow=true, causing this test to fail
// (still compiles).

func TestTopology(t *testing.T) {
	// Only allow binding from "zone-a" clusters to other "zone-a" clusters.
	zoneAOnly := func(from, to ClusterIdentity) bool {
		return strings.HasPrefix(from.Name, "zone-a-") && strings.HasPrefix(to.Name, "zone-a-")
	}

	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"example.com"},
		MinTrustLevel:  TrustUnknown,
		CanBind:        zoneAOnly,
	})

	from := ClusterIdentity{
		Name:        "zone-a-01",
		TrustDomain: "example.com",
		TrustLevel:  TrustHardened,
	}
	targetSameZone := ClusterIdentity{
		Name:        "zone-a-02",
		TrustDomain: "example.com",
		TrustLevel:  TrustHardened,
	}
	targetOtherZone := ClusterIdentity{
		Name:        "zone-b-01",
		TrustDomain: "example.com",
		TrustLevel:  TrustHardened,
	}

	t.Run("topology_allow_same_zone", func(t *testing.T) {
		d := policy.Evaluate(from, targetSameZone)
		assertDecision(t, "same zone allowed", d, true, "zone-a-01", "allowed")
	})

	t.Run("topology_deny_cross_zone", func(t *testing.T) {
		d := policy.Evaluate(from, targetOtherZone)
		assertDecision(t, "cross zone denied", d, false, "zone-b-01", "topology")
		// Reason must name both clusters for operator diagnostics.
		if !strings.Contains(d.Reason, "zone-a-01") {
			t.Errorf("topology deny: Reason %q does not contain from cluster %q", d.Reason, "zone-a-01")
		}
	})
}

// ---- TestAlwaysAllowMutation ------------------------------------------------
// This test group verifies that changing Evaluate to always return Allow
// causes EVERY deny test to fail — proving the deny branches are load-bearing.
//
// The structure records expected deny outcomes; if the policy were replaced
// with a trivial always-allow stub, all wantAllow=false assertions would
// trigger. We drive the real policy to confirm each concrete deny rule fires.
//
// Mutation proof: replace the entire body of Evaluate with
// `return Decision{Allow: true, Reason: "stub", Rule: "allowed"}` →
// all sub-tests below that expect Allow=false fail (still compiles).

func TestAlwaysAllowMutationDetector(t *testing.T) {
	// Shared policy that covers all four deny paths.
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"ok.example.com"},
		DeniedDomains:  []string{"bad.example.com"},
		MinTrustLevel:  TrustStandard,
		CanBind: func(from, to ClusterIdentity) bool {
			return from.Name != "forbidden-cluster"
		},
	})

	goodTarget := ClusterIdentity{
		Name:        "target",
		TrustDomain: "ok.example.com",
		TrustLevel:  TrustHardened,
	}

	cases := []struct {
		label     string
		from      ClusterIdentity
		wantAllow bool
		reasonSub string
		wantRule  string
	}{
		{
			label: "denied-domain fires",
			from: ClusterIdentity{
				Name:        "c1",
				TrustDomain: "bad.example.com",
				TrustLevel:  TrustHardened,
			},
			wantAllow: false,
			reasonSub: "bad.example.com",
			wantRule:  "denied-domain",
		},
		{
			label: "domain-not-allowed fires",
			from: ClusterIdentity{
				Name:        "c2",
				TrustDomain: "stranger.example.com",
				TrustLevel:  TrustHardened,
			},
			wantAllow: false,
			reasonSub: "stranger.example.com",
			wantRule:  "domain-not-allowed",
		},
		{
			label: "trust-level fires",
			from: ClusterIdentity{
				Name:        "c3",
				TrustDomain: "ok.example.com",
				TrustLevel:  TrustUnknown,
			},
			wantAllow: false,
			reasonSub: "Unknown",
			wantRule:  "trust-level",
		},
		{
			label: "topology fires",
			from: ClusterIdentity{
				Name:        "forbidden-cluster",
				TrustDomain: "ok.example.com",
				TrustLevel:  TrustHardened,
			},
			wantAllow: false,
			reasonSub: "forbidden-cluster",
			wantRule:  "topology",
		},
		{
			label: "allowed fires",
			from: ClusterIdentity{
				Name:        "good-cluster",
				TrustDomain: "ok.example.com",
				TrustLevel:  TrustStandard,
			},
			wantAllow: true,
			reasonSub: "good-cluster",
			wantRule:  "allowed",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.label, func(t *testing.T) {
			d := policy.Evaluate(tc.from, goodTarget)
			assertDecision(t, tc.label, d, tc.wantAllow, tc.reasonSub, tc.wantRule)
		})
	}
}

// ---- TestMutationProofs (explicit per-rule proofs) --------------------------
// Each sub-test names, in a comment, the EXACT one-line source mutation that
// makes it fail and still compiles.

// TestDenyPrecedenceOverAllow verifies rule 1 beats rule 2 when a domain is
// in both lists.
//
// Mutation proof: in Evaluate, move the `p.deniedDomains` lookup AFTER the
// `p.allowedDomains` lookup → "denied-overlap" cluster passes the allowlist
// check first (it IS in allowedDomains) and returns Allow=true; this test
// fails (still compiles).

func TestDenyPrecedenceOverAllow(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"overlap.net"},
		DeniedDomains:  []string{"overlap.net"},
		MinTrustLevel:  TrustUnknown,
	})
	from := ClusterIdentity{Name: "c", TrustDomain: "overlap.net", TrustLevel: TrustHardened}
	to := ClusterIdentity{Name: "t", TrustDomain: "overlap.net", TrustLevel: TrustHardened}
	d := policy.Evaluate(from, to)
	if d.Allow {
		t.Fatalf("deny-precedence: expected Allow=false, got Allow=true (Rule=%q, Reason=%q)", d.Rule, d.Reason)
	}
	if d.Rule != "denied-domain" {
		t.Errorf("deny-precedence: Rule = %q; want %q", d.Rule, "denied-domain")
	}
}

// TestLowTrustDenied verifies that a TrustLevel below MinTrustLevel is denied.
//
// Mutation proof: change `from.TrustLevel < p.minTrustLevel` to
// `from.TrustLevel <= p.minTrustLevel` → TrustStandard (equal to min) is
// ALSO denied, but the existing TestTrustLevel "standard meets minimum" sub-test
// catches that. For this test specifically: change to `from.TrustLevel != p.minTrustLevel`
// → TrustHardened (3 != 2) is wrongly denied; but more directly, dropping the
// `<` check entirely makes TrustProvisional (1 < 2) pass through to Allow=true
// and this test fails (still compiles).

func TestLowTrustDenied(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"x.net"},
		MinTrustLevel:  TrustStandard, // requires >= Standard
	})
	from := ClusterIdentity{Name: "low", TrustDomain: "x.net", TrustLevel: TrustProvisional}
	to := ClusterIdentity{Name: "hi", TrustDomain: "x.net", TrustLevel: TrustHardened}
	d := policy.Evaluate(from, to)
	assertDecision(t, "low trust denied", d, false, "Provisional", "trust-level")
	if !strings.Contains(d.Reason, "Standard") {
		t.Errorf("low trust denied: Reason %q missing required level name %q", d.Reason, "Standard")
	}
}

// TestCanBindFalseIsDenied verifies that CanBind=false triggers rule "topology"
// and that the Reason names both concrete cluster identities so operators can
// trace which pairing was rejected.
//
// Mutation proof: change `!p.canBind(from, target)` to `p.canBind(from, target)`
// (invert the condition) → when CanBind returns false the condition is now true
// but we return Allow, so the test's wantAllow=false assertion fails (still
// compiles — the variable is used in the return).

func TestCanBindFalseIsDenied(t *testing.T) {
	neverBind := func(_, _ ClusterIdentity) bool { return false }
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"a.net"},
		MinTrustLevel:  TrustUnknown,
		CanBind:        neverBind,
	})
	from := ClusterIdentity{Name: "src", TrustDomain: "a.net", TrustLevel: TrustHardened}
	to := ClusterIdentity{Name: "dst", TrustDomain: "a.net", TrustLevel: TrustHardened}
	d := policy.Evaluate(from, to)
	// wantReasonSubstr uses the input-derived from-cluster name "src" so the
	// check is load-bearing on actual cluster identity, not on the literal word
	// "topology" that is structurally guaranteed to appear in every topology Reason.
	assertDecision(t, "CanBind=false", d, false, "src", "topology")
	// Reason must also name the target cluster for full operator traceability.
	if !strings.Contains(d.Reason, "dst") {
		t.Errorf("CanBind=false: Reason %q does not contain target cluster name %q", d.Reason, "dst")
	}
}

// ---- TestImmutabilityOfConfig -----------------------------------------------
// Mutating the slices passed to NewFederationTrustPolicy after construction
// must NOT affect the policy's behavior (defensive copy).
//
// Mutation proof: remove the defensive copy in NewFederationTrustPolicy
// (store the slice pointer directly) → mutating allowedDomains after
// construction corrupts the internal map; the allow case fails (still compiles).

func TestImmutabilityOfConfig(t *testing.T) {
	allowed := []string{"safe.net"}
	denied := []string{"bad.net"}
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: allowed,
		DeniedDomains:  denied,
		MinTrustLevel:  TrustUnknown,
	})

	// Mutate the original slices post-construction.
	allowed[0] = "poisoned.net"
	denied[0] = "innocent.net"

	// "safe.net" must still be allowed (the policy copied it).
	from := ClusterIdentity{Name: "c", TrustDomain: "safe.net", TrustLevel: TrustUnknown}
	to := ClusterIdentity{Name: "t", TrustDomain: "safe.net", TrustLevel: TrustUnknown}
	d := policy.Evaluate(from, to)
	assertDecision(t, "after slice mutation safe.net still allowed", d, true, "safe.net", "allowed")

	// "bad.net" must still be denied.
	fromBad := ClusterIdentity{Name: "cb", TrustDomain: "bad.net", TrustLevel: TrustUnknown}
	d2 := policy.Evaluate(fromBad, to)
	assertDecision(t, "after slice mutation bad.net still denied", d2, false, "bad.net", "denied-domain")
}

// ---- TestNilCanBindSkipsTopologyGate ----------------------------------------
// When CanBind is nil, no topology gate is applied — the evaluation proceeds to
// "allowed" even for clusters that would be rejected by a real predicate.
//
// Mutation proof: remove the `p.canBind != nil &&` guard, so the nil function
// is called unconditionally → nil function dereference panic causes test failure
// (still compiles, fails at runtime).

func TestNilCanBindSkipsTopologyGate(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"any.net"},
		MinTrustLevel:  TrustUnknown,
		CanBind:        nil,
	})
	from := ClusterIdentity{Name: "f", TrustDomain: "any.net", TrustLevel: TrustUnknown}
	to := ClusterIdentity{Name: "t", TrustDomain: "any.net", TrustLevel: TrustUnknown}
	d := policy.Evaluate(from, to)
	assertDecision(t, "nil canBind allows", d, true, "f", "allowed")
}

// ---- TestReasonContainsIdentifiers ------------------------------------------
// All Reason strings must carry enough concrete identifiers (cluster names,
// domain names, trust-level names) for operators to act on them without reading
// source code.
//
// Mutation proof: strip the cluster name / domain from the Reason strings in
// Evaluate → the Contains checks below fail (still compiles).

func TestReasonContainsIdentifiers(t *testing.T) {
	canBind := func(from, _ ClusterIdentity) bool { return from.Name != "blocked" }
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"ok.net"},
		DeniedDomains:  []string{"denied.net"},
		MinTrustLevel:  TrustStandard,
		CanBind:        canBind,
	})
	target := ClusterIdentity{Name: "tgt", TrustDomain: "ok.net", TrustLevel: TrustHardened}

	t.Run("denied_domain_reason", func(t *testing.T) {
		from := ClusterIdentity{Name: "src-denied", TrustDomain: "denied.net", TrustLevel: TrustHardened}
		d := policy.Evaluate(from, target)
		if !strings.Contains(d.Reason, "denied.net") {
			t.Errorf("denied_domain_reason: Reason %q missing domain", d.Reason)
		}
		if !strings.Contains(d.Reason, "src-denied") {
			t.Errorf("denied_domain_reason: Reason %q missing cluster name", d.Reason)
		}
	})

	t.Run("domain_not_allowed_reason", func(t *testing.T) {
		from := ClusterIdentity{Name: "src-stranger", TrustDomain: "stranger.net", TrustLevel: TrustHardened}
		d := policy.Evaluate(from, target)
		if !strings.Contains(d.Reason, "stranger.net") {
			t.Errorf("domain_not_allowed_reason: Reason %q missing domain", d.Reason)
		}
		if !strings.Contains(d.Reason, "src-stranger") {
			t.Errorf("domain_not_allowed_reason: Reason %q missing cluster name", d.Reason)
		}
	})

	t.Run("trust_level_reason", func(t *testing.T) {
		from := ClusterIdentity{Name: "src-low", TrustDomain: "ok.net", TrustLevel: TrustUnknown}
		d := policy.Evaluate(from, target)
		if !strings.Contains(d.Reason, "Unknown") {
			t.Errorf("trust_level_reason: Reason %q missing presented level", d.Reason)
		}
		if !strings.Contains(d.Reason, "Standard") {
			t.Errorf("trust_level_reason: Reason %q missing required level", d.Reason)
		}
		if !strings.Contains(d.Reason, "src-low") {
			t.Errorf("trust_level_reason: Reason %q missing cluster name", d.Reason)
		}
	})

	t.Run("topology_reason", func(t *testing.T) {
		from := ClusterIdentity{Name: "blocked", TrustDomain: "ok.net", TrustLevel: TrustHardened}
		d := policy.Evaluate(from, target)
		if !strings.Contains(d.Reason, "blocked") {
			t.Errorf("topology_reason: Reason %q missing from cluster name", d.Reason)
		}
		if !strings.Contains(d.Reason, "tgt") {
			t.Errorf("topology_reason: Reason %q missing target cluster name", d.Reason)
		}
	})

	t.Run("allowed_reason", func(t *testing.T) {
		from := ClusterIdentity{Name: "src-good", TrustDomain: "ok.net", TrustLevel: TrustStandard}
		d := policy.Evaluate(from, target)
		if !strings.Contains(d.Reason, "src-good") {
			t.Errorf("allowed_reason: Reason %q missing from cluster name", d.Reason)
		}
		if !strings.Contains(d.Reason, "ok.net") {
			t.Errorf("allowed_reason: Reason %q missing domain", d.Reason)
		}
		if !strings.Contains(d.Reason, "Standard") {
			t.Errorf("allowed_reason: Reason %q missing trust level", d.Reason)
		}
	})
}
