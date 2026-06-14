package tiersec

// Adversarial sink-side probes for pkg/tiersec.
//
// Family context: pkg/edge admitted invalid trust levels (fail-open) and
// pkg/security admitted a malformed SPIFFE domain. tiersec makes a tier /
// clearance decision. These tests probe the ACTUAL grant/deny verdict (and the
// actual privilege a malformed tier name resolves to) on hostile input — never
// merely "did not panic".
//
// Each test states the documented contract it is checking and asserts the
// sink-side outcome. Tests that pin CORRECT behavior guard against regression.
//
// !!! LIVE REPRODUCER — NOT env-gated (per protocol). On UNFIXED prod, exactly
// two tests FAIL, demonstrating a REAL high-severity fail-open:
//
//   - TestADV_ProfileForTier_RejectsMalformed
//   - TestADV_Enforce_RejectsMalformedTier
//
// Root cause: tierRank() in profile.go uses fmt.Sscanf(tier, "T%d", &rank),
// which silently accepts trailing/embedded junk and sign/zero-pad forms
// ("T13extra", "T15rm -rf", "T07", "T+7", "T 7", ...). The contract (tierRank,
// ProfileForTier and Enforce docs) promises that any non-conforming tier name
// is REJECTED with an error and no fallback profile. Instead a malformed name
// like "T13extra" / "T15rm -rf" is GRANTED a real Secret / CapExec /
// unrestricted-egress clearance band (sink-side: Enforce returns
// Decision{Allowed:true}).
//
// Minimal fix (verified, then reverted byte-identical — NOT applied): after the
// Sscanf parse in tierRank, require a byte-identical round-trip:
//     if tier != fmt.Sprintf("T%d", rank) {
//         return 0, fmt.Errorf("tiersec: unknown tier %q: must be T1-T15", tier)
//     }
//
// All other TestADV_* tests below PIN correct behavior and pass on unfixed prod.

import (
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/sandbox"
)

// ─────────────────────────────────────────────────────────────────────────────
// (a) FAIL-OPEN: malformed / unknown tier names.
//
// Contract (profile.go ProfileForTier doc + tierRank doc + enforce.go doc):
//   "An unknown tier name returns a descriptive error; no default/fallback
//    profile is produced."  tierRank: "Returns (0, error) for any non-conforming
//    input."
//
// A tier name is "conforming" iff it is exactly one of T1..T15. Anything else
// MUST yield an error from ProfileForTier and from Enforce — NOT a granted
// profile.
// ─────────────────────────────────────────────────────────────────────────────

// malformedTiers are strings that are NOT the canonical name of any tier
// (T1..T15) and therefore, per the documented contract, MUST be rejected.
var malformedTiers = []string{
	"T7xyz",     // trailing alpha junk after a valid rank
	"T13extra",  // trailing alpha junk that resolves to the Secret band
	"T9garbage", // trailing junk resolving into the Confidential band
	"T15rm -rf", // trailing junk resolving to the top tier
	"T07",       // zero-padded — not the canonical "T7"
	"T+7",       // explicit plus sign
	"T 7",       // embedded space
	"T7.5",      // fractional
}

// TestADV_ProfileForTier_RejectsMalformed asserts the documented contract:
// every malformed tier name must produce an ERROR and a zero profile — never a
// usable SecurityProfile. A returned non-error here is a fail-open admission of
// an invalid tier (cf. edge invalid TrustLevel).
func TestADV_ProfileForTier_RejectsMalformed(t *testing.T) {
	for _, bad := range malformedTiers {
		p, err := ProfileForTier(bad)
		if err == nil {
			t.Errorf(
				"FAIL-OPEN: ProfileForTier(%q) returned no error; granted profile "+
					"{Tier:%q MaxDataClass:%s Egress:%q Caps:%v}. Contract: malformed "+
					"tier names must be rejected with an error.",
				bad, p.Tier, p.MaxDataClass, p.Egress.Mode, p.AllowedCaps.Granted(),
			)
		}
	}
}

// TestADV_Enforce_RejectsMalformedTier is the SINK-SIDE version: it asks the
// real authorization entrypoint Enforce to authorize a privileged operation
// (CapExec on Secret data with unrestricted egress) under a malformed tier
// name. The contract says Enforce returns an error for an unknown tier. A
// (Decision{Allowed:true}, nil) here is a high-severity fail-open: a malformed
// tier string was GRANTED top-tier clearance.
func TestADV_Enforce_RejectsMalformedTier(t *testing.T) {
	for _, bad := range malformedTiers {
		dec, err := Enforce(bad, Operation{
			Capability:   sandbox.CapExec,
			DataClass:    Secret,
			EgressTarget: "8.8.8.8",
		})
		if err == nil {
			t.Errorf(
				"FAIL-OPEN: Enforce(%q, CapExec/Secret/egress) returned no error; "+
					"Decision{Allowed:%v Tier:%q Reason:%q}. A malformed tier name must "+
					"be rejected, not resolved to a real clearance band.",
				bad, dec.Allowed, dec.Tier, dec.Reason,
			)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (a) FAIL-OPEN: zero-value / unknown capability and empty operation.
//
// Contract (capability.go): Capabilities are deny-by-default; the zero value
// and unknown capabilities grant nothing. So an Operation with a zero-value
// ("") or unknown capability must be DENIED on the capability dimension at any
// tier that does not literally grant that exact capability string.
// ─────────────────────────────────────────────────────────────────────────────

// TestADV_Enforce_ZeroValueCapabilityDenied: a zero-value Operation{} carries
// Capability == "" and DataClass == Public. The empty capability is not granted
// to ANY tier, so even the top tier T15 must DENY on the capability dimension.
// A grant here would be a fail-open on an empty/uninitialised request.
func TestADV_Enforce_ZeroValueCapabilityDenied(t *testing.T) {
	for _, tier := range []string{"T1", "T8", "T15"} {
		dec, err := Enforce(tier, Operation{}) // zero value: Capability=="" DataClass==Public EgressTarget==""
		if err != nil {
			t.Fatalf("Enforce(%q, zero Operation): unexpected error: %v", tier, err)
		}
		if dec.Allowed {
			t.Errorf(
				"FAIL-OPEN: Enforce(%q, zero Operation{}) ALLOWED; an empty/uninitialised "+
					"operation (Capability==\"\") must be denied on the capability dimension. "+
					"Reason=%q",
				tier, dec.Reason,
			)
		}
		if !strings.Contains(dec.Reason, "capability") {
			t.Errorf("Enforce(%q, zero Operation): deny reason should cite 'capability', got %q", tier, dec.Reason)
		}
	}
}

// TestADV_Enforce_UnknownCapabilityDenied: an unknown capability string is not
// in any profile's allow-list → deny-by-default → DENY at every tier.
func TestADV_Enforce_UnknownCapabilityDenied(t *testing.T) {
	const bogus sandbox.Capability = "frobnicate_the_gpu"
	for _, tier := range []string{"T1", "T8", "T15"} {
		dec, err := Enforce(tier, Operation{Capability: bogus, DataClass: Public})
		if err != nil {
			t.Fatalf("Enforce(%q, bogus cap): unexpected error: %v", tier, err)
		}
		if dec.Allowed {
			t.Errorf("FAIL-OPEN: Enforce(%q, %q) ALLOWED an unknown capability; deny-by-default violated. Reason=%q",
				tier, bogus, dec.Reason)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (b) TOTAL-ORDER: data-class comparison must be a correct monotone order.
//
// Contract (enforce.go): op.DataClass <= profile.MaxDataClass (INCLUSIVE). A
// lower-clearance tier must NOT be granted higher-classification data.
// ─────────────────────────────────────────────────────────────────────────────

// TestADV_Enforce_DataClassMonotone walks every tier and asserts:
//   - data exactly at the tier's max is ALLOWED (inclusive boundary),
//   - data one step above the max is DENIED (no off-by-one fail-open),
// using a capability the tier genuinely holds so the data-class dimension is
// the deciding one.
func TestADV_Enforce_DataClassMonotone(t *testing.T) {
	type tc struct {
		tier string
		max  DataClassification
	}
	cases := []tc{
		{"T1", Public}, {"T4", Public},
		{"T5", Internal}, {"T8", Internal},
		{"T9", Confidential}, {"T12", Confidential},
		{"T13", Secret}, {"T15", Secret},
	}
	for _, c := range cases {
		// At-max: must ALLOW.
		atMax, err := Enforce(c.tier, Operation{Capability: sandbox.CapReadFS, DataClass: c.max})
		if err != nil {
			t.Fatalf("Enforce(%q, atMax): %v", c.tier, err)
		}
		if !atMax.Allowed {
			t.Errorf("TOTAL-ORDER: Enforce(%q, %s==max) should ALLOW (inclusive boundary); got DENY %q",
				c.tier, c.max, atMax.Reason)
		}
		// One above max (only meaningful when max < Secret): must DENY.
		if c.max < Secret {
			above, err := Enforce(c.tier, Operation{Capability: sandbox.CapReadFS, DataClass: c.max + 1})
			if err != nil {
				t.Fatalf("Enforce(%q, above): %v", c.tier, err)
			}
			if above.Allowed {
				t.Errorf("FAIL-OPEN/off-by-one: Enforce(%q, %s>max) was ALLOWED; lower tier granted higher-class data. Reason=%q",
					c.tier, c.max+1, above.Reason)
			}
		}
	}
}

// TestADV_Enforce_LowTierNeverGetsSecret cross-checks the order globally: NO
// tier below the Secret band (T1..T12) may ever be granted Secret data, no
// matter the capability. This is the headline total-order invariant.
func TestADV_Enforce_LowTierNeverGetsSecret(t *testing.T) {
	for _, tier := range []string{"T1", "T4", "T5", "T8", "T9", "T12"} {
		dec, err := Enforce(tier, Operation{Capability: sandbox.CapReadFS, DataClass: Secret})
		if err != nil {
			t.Fatalf("Enforce(%q, Secret): %v", tier, err)
		}
		if dec.Allowed {
			t.Errorf("TOTAL-ORDER FAIL-OPEN: Enforce(%q, Secret) ALLOWED; only T13-T15 may touch Secret. Reason=%q",
				tier, dec.Reason)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (c) NUMERIC: out-of-enum-range and extreme DataClassification values.
//
// DataClassification is an int. Probe both directions:
//   - A value far ABOVE Secret requested at a high tier must be DENIED (it is a
//     more-sensitive-than-known class; granting it would be a fail-open).
//   - A NEGATIVE value is strictly below Public in the order; per the documented
//     inclusive "<=" order it compares as least-sensitive and is admitted. This
//     is the documented total order, so we PIN it (no fix) — but we assert it
//     never lets a class ABOVE the tier max through.
// ─────────────────────────────────────────────────────────────────────────────

// TestADV_Enforce_AboveSecretDeniedEverywhere: a DataClass value above the
// Secret enum (e.g. Secret+5, or a huge int) must be denied even at T15, whose
// max is Secret. Granting it would admit an unknown, more-sensitive class.
func TestADV_Enforce_AboveSecretDeniedEverywhere(t *testing.T) {
	hostile := []DataClassification{
		Secret + 1,
		Secret + 5,
		DataClassification(1 << 30),
		DataClassification(math.MaxInt32),
	}
	for _, dc := range hostile {
		dec, err := Enforce("T15", Operation{Capability: sandbox.CapReadFS, DataClass: dc})
		if err != nil {
			t.Fatalf("Enforce(T15, %d): %v", int(dc), err)
		}
		if dec.Allowed {
			t.Errorf("FAIL-OPEN: Enforce(T15, DataClass(%d)) ALLOWED a class above Secret; unknown high class admitted. Reason=%q",
				int(dc), dec.Reason)
		}
		if !strings.Contains(dec.Reason, "data class") {
			t.Errorf("Enforce(T15, %d) deny reason should cite 'data class', got %q", int(dc), dec.Reason)
		}
	}
}

// TestADV_Enforce_NegativeDataClassIsOrderedLeast PINS the documented inclusive
// total order: a negative DataClass is below Public, so "<= max" admits it at
// any tier (it is strictly LESS sensitive than anything the tier may handle).
// This is contract-consistent, not a fail-open, and is pinned to detect any
// future order inversion.
func TestADV_Enforce_NegativeDataClassIsOrderedLeast(t *testing.T) {
	dec, err := Enforce("T1", Operation{Capability: sandbox.CapReadFS, DataClass: DataClassification(-7)})
	if err != nil {
		t.Fatalf("Enforce(T1, -7): %v", err)
	}
	if !dec.Allowed {
		t.Errorf("ORDER PIN: Enforce(T1, DataClass(-7)) should ALLOW (a class below Public is least-sensitive under the inclusive order); got DENY %q", dec.Reason)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (b/egress) TOTAL-ORDER on the egress dimension: allowlist must not fail open.
// ─────────────────────────────────────────────────────────────────────────────

// TestADV_Enforce_EgressAllowlist_MalformedTargetDenied: a malformed egress
// target on an allowlist tier (T5-T8) is neither a valid IP nor CIDR; the
// documented behavior is to treat it as non-routable → DENY. A grant here would
// be a fail-open on a malformed destination.
func TestADV_Enforce_EgressAllowlist_MalformedTargetDenied(t *testing.T) {
	for _, target := range []string{"not-an-ip", "999.999.999.999", "10.0.0.0/999", "::garbage::"} {
		dec, err := Enforce("T5", Operation{
			Capability:   sandbox.CapReadFS,
			DataClass:    Public,
			EgressTarget: target,
		})
		if err != nil {
			t.Fatalf("Enforce(T5, egress %q): unexpected error: %v", target, err)
		}
		if dec.Allowed {
			t.Errorf("FAIL-OPEN: Enforce(T5, egress %q) ALLOWED a malformed/unroutable target. Reason=%q",
				target, dec.Reason)
		}
	}
}

// TestADV_Enforce_EgressAllowlist_PublicCIDRDenied: a public CIDR prefix whose
// network address is outside RFC-1918 must be DENIED on an allowlist tier.
func TestADV_Enforce_EgressAllowlist_PublicCIDRDenied(t *testing.T) {
	dec, err := Enforce("T7", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Public,
		EgressTarget: "8.8.8.0/24",
	})
	if err != nil {
		t.Fatalf("Enforce(T7, 8.8.8.0/24): %v", err)
	}
	if dec.Allowed {
		t.Errorf("FAIL-OPEN: Enforce(T7, egress 8.8.8.0/24) ALLOWED a public CIDR outside RFC-1918. Reason=%q", dec.Reason)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (d) UNGUARDED SHARED STATE: concurrent decisions must be race-free.
//
// Enforce builds a fresh profile per call (ProfileForTier returns by value with
// fresh *Capabilities), so there should be no shared mutable state. Run under
// -race to prove it; assert verdicts stay correct under concurrency (sink-side,
// not just "no race").
// ─────────────────────────────────────────────────────────────────────────────

func TestADV_Enforce_ConcurrentDecisionsRaceFree(t *testing.T) {
	const workers = 64
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				// Must DENY: T1 cannot exec.
				dec, err := Enforce("T1", Operation{Capability: sandbox.CapExec, DataClass: Public})
				if err != nil || dec.Allowed {
					t.Errorf("concurrent T1+CapExec: want DENY,nil-err; got allowed=%v err=%v", dec.Allowed, err)
				}
			} else {
				// Must ALLOW: T15 can exec on Secret.
				dec, err := Enforce("T15", Operation{Capability: sandbox.CapExec, DataClass: Secret})
				if err != nil || !dec.Allowed {
					t.Errorf("concurrent T15+CapExec/Secret: want ALLOW,nil-err; got allowed=%v err=%v", dec.Allowed, err)
				}
			}
		}(i)
	}
	wg.Wait()
}
