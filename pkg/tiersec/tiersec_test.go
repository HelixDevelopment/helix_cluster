package tiersec

import (
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/sandbox"
)

// ── ProfileForTier: basic contract ───────────────────────────────────────────

func TestProfileForTier_KnownTiers(t *testing.T) {
	for _, tier := range []string{
		"T1", "T2", "T3", "T4",
		"T5", "T6", "T7", "T8",
		"T9", "T10", "T11", "T12",
		"T13", "T14", "T15",
	} {
		p, err := ProfileForTier(tier)
		if err != nil {
			t.Errorf("ProfileForTier(%q) unexpected error: %v", tier, err)
			continue
		}
		if p.Tier != tier {
			t.Errorf("ProfileForTier(%q).Tier = %q; want %q", tier, p.Tier, tier)
		}
		if p.AllowedCaps == nil {
			t.Errorf("ProfileForTier(%q).AllowedCaps is nil", tier)
		}
	}
}

func TestProfileForTier_UnknownTier(t *testing.T) {
	for _, bad := range []string{"T0", "T16", "T100", "X1", "", "t3"} {
		_, err := ProfileForTier(bad)
		if err == nil {
			t.Errorf("ProfileForTier(%q): expected error, got nil", bad)
		}
	}
}

// ANTI-BLUFF: T1 and T15 must produce strictly different profiles.
func TestProfileForTier_LowVsHighDiffer(t *testing.T) {
	low, err := ProfileForTier("T1")
	if err != nil {
		t.Fatalf("ProfileForTier(T1): %v", err)
	}
	high, err := ProfileForTier("T15")
	if err != nil {
		t.Fatalf("ProfileForTier(T15): %v", err)
	}

	// MaxDataClass must differ.
	if low.MaxDataClass == high.MaxDataClass {
		t.Errorf("T1 and T15 have the same MaxDataClass (%v) — constant profile is a bluff", low.MaxDataClass)
	}
	// Egress mode must differ.
	if low.Egress.Mode == high.Egress.Mode {
		t.Errorf("T1 and T15 have the same Egress.Mode (%q) — constant profile is a bluff", low.Egress.Mode)
	}
	// T1 must NOT have CapExec; T15 must have it.
	if low.AllowedCaps.Has(sandbox.CapExec) {
		t.Error("T1 profile should NOT have CapExec")
	}
	if !high.AllowedCaps.Has(sandbox.CapExec) {
		t.Error("T15 profile should have CapExec")
	}
	// T1 must NOT have CapNetwork; T15 must have it.
	if low.AllowedCaps.Has(sandbox.CapNetwork) {
		t.Error("T1 profile should NOT have CapNetwork")
	}
	if !high.AllowedCaps.Has(sandbox.CapNetwork) {
		t.Error("T15 profile should have CapNetwork")
	}
}

// ── Enforce: capability dimension (both directions) ───────────────────────────

// LOW tier (T1): CapExec is not in the T1 profile → DENIED.
func TestEnforce_CapabilityDenied_LowTier(t *testing.T) {
	dec, err := Enforce("T1", Operation{
		Capability:   sandbox.CapExec,
		DataClass:    Public,
		EgressTarget: "",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Error("T1+CapExec: expected DENY, got ALLOW")
	}
	if !strings.Contains(dec.Reason, "capability") {
		t.Errorf("DENY reason should mention 'capability', got: %q", dec.Reason)
	}
}

// HIGH tier (T15): CapExec IS in the T15 profile → ALLOWED (all else benign).
func TestEnforce_CapabilityAllowed_HighTier(t *testing.T) {
	dec, err := Enforce("T15", Operation{
		Capability:   sandbox.CapExec,
		DataClass:    Public,
		EgressTarget: "",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("T15+CapExec: expected ALLOW, got DENY (%s)", dec.Reason)
	}
}

// LOW tier (T1): CapNetwork is not in the T1 profile → DENIED.
func TestEnforce_NetworkCapDenied_LowTier(t *testing.T) {
	dec, err := Enforce("T1", Operation{
		Capability:   sandbox.CapNetwork,
		DataClass:    Public,
		EgressTarget: "",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Errorf("T1+CapNetwork: expected DENY, got ALLOW")
	}
	if !strings.Contains(dec.Reason, "capability") {
		t.Errorf("DENY reason should mention 'capability', got: %q", dec.Reason)
	}
}

// HIGH tier (T15): CapNetwork IS in the T15 profile → ALLOWED.
func TestEnforce_NetworkCapAllowed_HighTier(t *testing.T) {
	dec, err := Enforce("T15", Operation{
		Capability:   sandbox.CapNetwork,
		DataClass:    Public,
		EgressTarget: "",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("T15+CapNetwork: expected ALLOW, got DENY (%s)", dec.Reason)
	}
}

// ── Enforce: data-class dimension (both directions) ───────────────────────────

// T1 MaxDataClass == Public; Secret > Public → DENIED.
func TestEnforce_DataClassDenied_LowTier(t *testing.T) {
	dec, err := Enforce("T1", Operation{
		Capability:   sandbox.CapReadFS, // CapReadFS IS allowed on T1
		DataClass:    Secret,
		EgressTarget: "",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Error("T1+Secret: expected DENY, got ALLOW")
	}
	if !strings.Contains(dec.Reason, "data class") {
		t.Errorf("DENY reason should mention 'data class', got: %q", dec.Reason)
	}
}

// T15 MaxDataClass == Secret; Secret == Secret → ALLOWED (boundary is inclusive).
func TestEnforce_DataClassAllowed_HighTier(t *testing.T) {
	dec, err := Enforce("T15", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Secret,
		EgressTarget: "",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("T15+Secret: expected ALLOW, got DENY (%s)", dec.Reason)
	}
}

// Boundary test: op.DataClass == profile.MaxDataClass is ALLOWED (inclusive).
func TestEnforce_DataClassBoundaryInclusive(t *testing.T) {
	// T1 MaxDataClass == Public; Public == Public → ALLOW.
	dec, err := Enforce("T1", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Public,
		EgressTarget: "",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("T1+Public (boundary): expected ALLOW, got DENY (%s)", dec.Reason)
	}

	// T8 MaxDataClass == Internal; Internal == Internal → ALLOW.
	dec2, err2 := Enforce("T8", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Internal,
		EgressTarget: "",
	})
	if err2 != nil {
		t.Fatalf("Enforce: unexpected error: %v", err2)
	}
	if !dec2.Allowed {
		t.Errorf("T8+Internal (boundary): expected ALLOW, got DENY (%s)", dec2.Reason)
	}
}

// One step above boundary → DENIED.
func TestEnforce_DataClassBoundaryExclusive(t *testing.T) {
	// T1 MaxDataClass == Public; Internal (1) > Public (0) → DENY.
	dec, err := Enforce("T1", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Internal,
		EgressTarget: "",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Errorf("T1+Internal (above boundary): expected DENY, got ALLOW")
	}
	if !strings.Contains(dec.Reason, "data class") {
		t.Errorf("DENY reason should mention 'data class', got: %q", dec.Reason)
	}
}

// ── Enforce: egress dimension (both directions) ───────────────────────────────

// T1 Egress.Mode == "deny"; any EgressTarget → DENIED.
func TestEnforce_EgressDenied_LowTier(t *testing.T) {
	dec, err := Enforce("T1", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Public,
		EgressTarget: "8.8.8.8",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Error("T1+egress: expected DENY, got ALLOW")
	}
	if !strings.Contains(dec.Reason, "egress") {
		t.Errorf("DENY reason should mention 'egress', got: %q", dec.Reason)
	}
}

// T15 Egress.Mode == "allow"; any EgressTarget → ALLOWED.
func TestEnforce_EgressAllowed_HighTier(t *testing.T) {
	dec, err := Enforce("T15", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Public,
		EgressTarget: "8.8.8.8",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("T15+egress: expected ALLOW, got DENY (%s)", dec.Reason)
	}
}

// T5-T8: Mode == "allowlist"; RFC-1918 address → ALLOWED.
func TestEnforce_EgressAllowlist_PrivateAllowed(t *testing.T) {
	dec, err := Enforce("T5", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Public,
		EgressTarget: "10.0.1.50",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("T5+egress(RFC-1918): expected ALLOW, got DENY (%s)", dec.Reason)
	}
}

// T5-T8: Mode == "allowlist"; public address outside RFC-1918 → DENIED,
// reason must mention "egress".
func TestEnforce_EgressAllowlist_PublicDenied(t *testing.T) {
	dec, err := Enforce("T5", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Public,
		EgressTarget: "203.0.113.10", // TEST-NET-3, not in RFC-1918
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Error("T5+egress(public): expected DENY, got ALLOW")
	}
	if !strings.Contains(dec.Reason, "egress") {
		t.Errorf("DENY reason should mention 'egress', got: %q", dec.Reason)
	}
}

// Additional RFC-1918 sub-ranges that must be permitted on T5-T8.
func TestEnforce_EgressAllowlist_172and192(t *testing.T) {
	for _, ip := range []string{"172.16.0.1", "172.31.255.254", "192.168.100.200"} {
		dec, err := Enforce("T6", Operation{
			Capability:   sandbox.CapReadFS,
			DataClass:    Public,
			EgressTarget: ip,
		})
		if err != nil {
			t.Fatalf("Enforce(T6, %s): unexpected error: %v", ip, err)
		}
		if !dec.Allowed {
			t.Errorf("T6+egress(%s): expected ALLOW, got DENY (%s)", ip, dec.Reason)
		}
	}
}

// T1 with empty EgressTarget → egress check is skipped → ALLOW (cap+data OK).
func TestEnforce_NoEgressTarget_Allowed(t *testing.T) {
	dec, err := Enforce("T1", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Public,
		EgressTarget: "",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("T1+no-egress: expected ALLOW, got DENY (%s)", dec.Reason)
	}
}

// ── Enforce: unknown tier returns an error ────────────────────────────────────

func TestEnforce_UnknownTierError(t *testing.T) {
	_, err := Enforce("T99", Operation{Capability: sandbox.CapReadFS})
	if err == nil {
		t.Error("Enforce(T99): expected error, got nil")
	}
}

// ── Decision fields are populated correctly ───────────────────────────────────

func TestEnforce_DecisionFieldsOnDeny(t *testing.T) {
	dec, err := Enforce("T2", Operation{
		Capability:   sandbox.CapExec,
		DataClass:    Public,
		EgressTarget: "",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if dec.Tier != "T2" {
		t.Errorf("Decision.Tier = %q; want %q", dec.Tier, "T2")
	}
	if dec.Op.Capability != sandbox.CapExec {
		t.Errorf("Decision.Op.Capability = %q; want CapExec", dec.Op.Capability)
	}
	if dec.Allowed {
		t.Error("expected Allowed=false")
	}
	if dec.Reason == "" {
		t.Error("Decision.Reason must not be empty on DENY")
	}
}

func TestEnforce_DecisionFieldsOnAllow(t *testing.T) {
	dec, err := Enforce("T15", Operation{
		Capability:   sandbox.CapExec,
		DataClass:    Secret,
		EgressTarget: "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("Enforce: unexpected error: %v", err)
	}
	if dec.Tier != "T15" {
		t.Errorf("Decision.Tier = %q; want T15", dec.Tier)
	}
	if !dec.Allowed {
		t.Errorf("T15+all: expected ALLOW, got DENY (%s)", dec.Reason)
	}
	if dec.Reason == "" {
		t.Error("Decision.Reason must not be empty on ALLOW")
	}
}

// ── DataClassification.String ─────────────────────────────────────────────────

func TestDataClassificationString(t *testing.T) {
	cases := []struct {
		dc   DataClassification
		want string
	}{
		{Public, "Public"},
		{Internal, "Internal"},
		{Confidential, "Confidential"},
		{Secret, "Secret"},
	}
	for _, c := range cases {
		if got := c.dc.String(); got != c.want {
			t.Errorf("DataClassification(%d).String() = %q; want %q", int(c.dc), got, c.want)
		}
	}
}

// ── Mutation targets ──────────────────────────────────────────────────────────
//
// These tests are explicitly designed to catch the two documented mutations:
//
//  1. Flip <= to < in the data-class check → boundary-inclusive test fails.
//  2. Drop a deny branch (e.g. skip capability check) → low-tier wrongly
//     permitted CapExec/CapNetwork.

// Mutation 1: boundary must be INCLUSIVE (== MaxDataClass is ALLOWED).
// If someone changes <= to <, this test fails.
func TestEnforce_MutationGuard_DataClassBoundaryMustBeInclusive(t *testing.T) {
	// T1 allows Public (0). If < is used instead of <=, Public == 0 fails.
	dec, err := Enforce("T1", Operation{Capability: sandbox.CapReadFS, DataClass: Public})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Allowed {
		t.Error("MUTATION GUARD: T1+Public must be ALLOWED (boundary inclusive); failing means <= was changed to <")
	}

	// Same for T8+Internal.
	dec2, err2 := Enforce("T8", Operation{Capability: sandbox.CapReadFS, DataClass: Internal})
	if err2 != nil {
		t.Fatal(err2)
	}
	if !dec2.Allowed {
		t.Error("MUTATION GUARD: T8+Internal must be ALLOWED (boundary inclusive)")
	}
}

// Mutation 2: low-tier DENY branches must be present.
// If the capability deny branch is dropped, T1 would wrongly allow CapExec.
func TestEnforce_MutationGuard_CapDenyMustExist(t *testing.T) {
	dec, err := Enforce("T1", Operation{Capability: sandbox.CapExec, DataClass: Public})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed {
		t.Error("MUTATION GUARD: T1+CapExec must be DENIED; failing means the capability deny branch was dropped")
	}
}

// Mutation 2 (egress variant): if the egress deny branch is dropped,
// T1 would wrongly allow external egress.
func TestEnforce_MutationGuard_EgressDenyMustExist(t *testing.T) {
	dec, err := Enforce("T1", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Public,
		EgressTarget: "8.8.8.8",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed {
		t.Error("MUTATION GUARD: T1+egress must be DENIED; failing means the egress deny branch was dropped")
	}
}
