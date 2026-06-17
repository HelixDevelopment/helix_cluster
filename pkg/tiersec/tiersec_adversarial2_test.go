package tiersec

// Second-pass adversarial sink-side probes for pkg/tiersec.
//
// The first pass (tiersec_adversarial_test.go) found + fixed the Sscanf
// clearance-parse fail-open in tierRank. This pass targets OTHER decision paths,
// chiefly the egress allowlist comparison in egressPermitted (enforce.go).
//
// REAL BUG FOUND + FIXED (egress over-match fail-open):
//
//   egressPermitted previously reduced a CIDR *target* to the single host/network
//   address of the parsed prefix and asked whether that ONE address fell inside
//   an allowlist CIDR. A deliberately wide target such as "10.0.0.0/4" — which Go
//   masks to 0.0.0.0/4 and which SPANS public Internet space (8.8.8.8, 11.x.x.x,
//   15.255.255.255) — has a network/host address (10.0.0.0) that lands inside the
//   RFC-1918 allowlist entry 10.0.0.0/8. So Enforce GRANTED egress to a range that
//   includes the public Internet under an allowlist tier (T5-T8): a high-severity
//   egress fail-open / over-match.
//
//   Fix (enforce.go): a CIDR target is permitted only when the ENTIRE prefix is a
//   subset of an allowlist CIDR (cidrSubset: allowlist contains target network AND
//   target prefix is no wider than the allowlist prefix). Bare-IP targets keep
//   point containment.
//
//   Mutation proof: TestADV2_Enforce_EgressBroadCIDRTargetDenied below FAILS on the
//   pre-fix code (broad CIDR ALLOWED) and PASSES on the fixed code.
//
// The remaining TestADV2_* tests PIN correct behavior (legitimate sub-prefixes
// still allowed; ordering total; no panic on hostile input).

import (
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/sandbox"
)

// ─────────────────────────────────────────────────────────────────────────────
// REAL BUG (egress over-match): a CIDR target broader than the allowlist that
// spans public space must be DENIED on an allowlist tier (T5-T8). This is the
// mutation-proving test for the egressPermitted full-subset fix.
// ─────────────────────────────────────────────────────────────────────────────

func TestADV2_Enforce_EgressBroadCIDRTargetDenied(t *testing.T) {
	// Each target is a CIDR whose network address falls inside an RFC-1918
	// allowlist entry, but whose range escapes that entry into public space.
	hostileTargets := []struct {
		target string
		why    string
	}{
		{"10.0.0.0/4", "masks to 0.0.0.0/4, spans 8.8.8.8 / 11.x / 15.255.255.255 (public)"},
		{"10.0.0.0/7", "spans 10.0.0.0-11.255.255.255; 11.x is public"},
		{"172.0.0.0/8", "spans 172.0.0.0-172.255.255.255; only 172.16/12 is private"},
		{"192.168.0.0/15", "spans 192.168-192.169; 192.169.x is public"},
	}
	for _, ht := range hostileTargets {
		dec, err := Enforce("T5", Operation{
			Capability:   sandbox.CapReadFS,
			DataClass:    Public,
			EgressTarget: ht.target,
		})
		if err != nil {
			t.Fatalf("Enforce(T5, egress %q): unexpected error: %v", ht.target, err)
		}
		if dec.Allowed {
			t.Errorf(
				"EGRESS OVER-MATCH FAIL-OPEN: Enforce(T5, egress %q) ALLOWED; the target "+
					"prefix escapes RFC-1918 into public space (%s). An allowlist tier must "+
					"only permit egress targets fully contained within the allowlist. Reason=%q",
				ht.target, ht.why, dec.Reason,
			)
		}
		if !strings.Contains(dec.Reason, "egress") {
			t.Errorf("Enforce(T5, %q) deny reason should cite 'egress', got %q", ht.target, dec.Reason)
		}
	}
}

// PIN: legitimate sub-prefixes fully inside RFC-1918 must STILL be allowed after
// the fix (guards against an over-strict regression that breaks real callers).
func TestADV2_Enforce_EgressContainedCIDRTargetAllowed(t *testing.T) {
	goodTargets := []string{
		"10.0.0.0/8",      // exactly the allowlist entry
		"10.1.2.0/24",     // a /24 inside 10/8
		"10.0.0.0/16",     // a /16 inside 10/8
		"172.16.0.0/12",   // exactly the allowlist entry
		"172.16.5.0/24",   // inside 172.16/12
		"192.168.0.0/16",  // exactly the allowlist entry
		"192.168.1.0/24",  // inside 192.168/16
	}
	for _, tgt := range goodTargets {
		dec, err := Enforce("T6", Operation{
			Capability:   sandbox.CapReadFS,
			DataClass:    Public,
			EgressTarget: tgt,
		})
		if err != nil {
			t.Fatalf("Enforce(T6, egress %q): unexpected error: %v", tgt, err)
		}
		if !dec.Allowed {
			t.Errorf(
				"REGRESSION: Enforce(T6, egress %q) DENIED a CIDR fully contained in the "+
					"RFC-1918 allowlist; legitimate sub-prefixes must remain allowed. Reason=%q",
				tgt, dec.Reason,
			)
		}
	}
}

// PIN: a host /32 written as a CIDR inside the allowlist is allowed; a host /32
// just outside is denied (boundary check on the contained-IP path).
func TestADV2_Enforce_EgressHostCIDRBoundary(t *testing.T) {
	in, err := Enforce("T7", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Public,
		EgressTarget: "10.255.255.255/32",
	})
	if err != nil {
		t.Fatalf("Enforce(T7, 10.255.255.255/32): %v", err)
	}
	if !in.Allowed {
		t.Errorf("PIN: a /32 inside 10.0.0.0/8 must be ALLOWED; got DENY %q", in.Reason)
	}

	// 11.0.0.0/32 is one address past the end of 10.0.0.0/8 → must DENY.
	out, err := Enforce("T7", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Public,
		EgressTarget: "11.0.0.0/32",
	})
	if err != nil {
		t.Fatalf("Enforce(T7, 11.0.0.0/32): %v", err)
	}
	if out.Allowed {
		t.Errorf("FAIL-OPEN: a /32 outside RFC-1918 (11.0.0.0/32) must be DENIED; got ALLOW %q", out.Reason)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// IPv4-mapped IPv6 egress targets: an IPv4-mapped public address (::ffff:8.8.8.8)
// must NOT be admitted on an allowlist tier, and an IPv4-mapped private address
// (::ffff:10.0.0.1) is the same host as 10.0.0.1 and is allowed. PIN both so a
// future change to the parse path cannot fail open via address-family confusion.
// ─────────────────────────────────────────────────────────────────────────────

func TestADV2_Enforce_EgressIPv4MappedTargets(t *testing.T) {
	pub, err := Enforce("T5", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Public,
		EgressTarget: "::ffff:8.8.8.8",
	})
	if err != nil {
		t.Fatalf("Enforce(T5, ::ffff:8.8.8.8): %v", err)
	}
	if pub.Allowed {
		t.Errorf("FAIL-OPEN: IPv4-mapped public target ::ffff:8.8.8.8 ALLOWED on allowlist tier. Reason=%q", pub.Reason)
	}

	priv, err := Enforce("T5", Operation{
		Capability:   sandbox.CapReadFS,
		DataClass:    Public,
		EgressTarget: "::ffff:10.0.0.1",
	})
	if err != nil {
		t.Fatalf("Enforce(T5, ::ffff:10.0.0.1): %v", err)
	}
	if !priv.Allowed {
		t.Errorf("PIN: IPv4-mapped private target ::ffff:10.0.0.1 (== 10.0.0.1) should be ALLOWED; got DENY %q", priv.Reason)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TOTAL-ORDER ACROSS TIERS: a strictly lower tier must NEVER satisfy a
// requirement that a strictly higher tier defines. Cross-check the capability
// AND data-class dimensions band-by-band so an ordering inversion anywhere is
// caught (e.g. T4 must not get WriteFS that begins at T5; T8 must not get
// Network that begins at T9; T12 must not get Exec that begins at T13).
// ─────────────────────────────────────────────────────────────────────────────

func TestADV2_Enforce_LowerTierNeverSatisfiesHigherRequirement(t *testing.T) {
	// Capability that first appears at the NEXT band must be denied at the top of
	// the current band.
	capCases := []struct {
		tier string
		cap  sandbox.Capability
		band string
	}{
		{"T4", sandbox.CapWriteFS, "WriteFS begins at T5"},
		{"T4", sandbox.CapNetwork, "Network begins at T9"},
		{"T4", sandbox.CapExec, "Exec begins at T13"},
		{"T8", sandbox.CapNetwork, "Network begins at T9"},
		{"T8", sandbox.CapExec, "Exec begins at T13"},
		{"T12", sandbox.CapExec, "Exec begins at T13"},
	}
	for _, c := range capCases {
		dec, err := Enforce(c.tier, Operation{Capability: c.cap, DataClass: Public})
		if err != nil {
			t.Fatalf("Enforce(%q, %q): %v", c.tier, c.cap, err)
		}
		if dec.Allowed {
			t.Errorf(
				"ORDER INVERSION: Enforce(%q, %q) ALLOWED a capability that only a higher "+
					"band grants (%s). Reason=%q",
				c.tier, c.cap, c.band, dec.Reason,
			)
		}
		if !strings.Contains(dec.Reason, "capability") {
			t.Errorf("Enforce(%q, %q) deny reason should cite 'capability', got %q", c.tier, c.cap, dec.Reason)
		}
	}

	// Data class that first appears at the NEXT band must be denied at the top of
	// the current band (using a capability the tier genuinely holds).
	dataCases := []struct {
		tier string
		dc   DataClassification
		band string
	}{
		{"T4", Internal, "Internal begins at T5"},
		{"T8", Confidential, "Confidential begins at T9"},
		{"T12", Secret, "Secret begins at T13"},
	}
	for _, c := range dataCases {
		dec, err := Enforce(c.tier, Operation{Capability: sandbox.CapReadFS, DataClass: c.dc})
		if err != nil {
			t.Fatalf("Enforce(%q, %s): %v", c.tier, c.dc, err)
		}
		if dec.Allowed {
			t.Errorf(
				"ORDER INVERSION: Enforce(%q, %s) ALLOWED a data class that only a higher "+
					"band grants (%s). Reason=%q",
				c.tier, c.dc, c.band, dec.Reason,
			)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// EGRESS-MODE band ordering: the "deny" mode (T1-T4) must beat even a target
// that an allowlist tier would accept; a lower tier never inherits a higher
// tier's looser egress posture.
// ─────────────────────────────────────────────────────────────────────────────

func TestADV2_Enforce_DenyEgressTierNeverInheritsAllowlist(t *testing.T) {
	// 10.0.0.1 is allowed on T5 (allowlist) but T1-T4 are mode "deny": must DENY.
	for _, tier := range []string{"T1", "T2", "T3", "T4"} {
		dec, err := Enforce(tier, Operation{
			Capability:   sandbox.CapReadFS,
			DataClass:    Public,
			EgressTarget: "10.0.0.1",
		})
		if err != nil {
			t.Fatalf("Enforce(%q, 10.0.0.1): %v", tier, err)
		}
		if dec.Allowed {
			t.Errorf("FAIL-OPEN: Enforce(%q, egress 10.0.0.1) ALLOWED; deny-egress tier must not "+
				"accept an RFC-1918 target. Reason=%q", tier, dec.Reason)
		}
		if !strings.Contains(dec.Reason, "egress") {
			t.Errorf("Enforce(%q) deny reason should cite 'egress', got %q", tier, dec.Reason)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// NO PANIC on hostile egress targets (IPv6 zones, partial CIDRs, junk masks).
// egressPermitted must classify every input as allow/deny without panicking.
// ─────────────────────────────────────────────────────────────────────────────

func TestADV2_Enforce_HostileEgressTargetsNoPanicAllDeny(t *testing.T) {
	hostile := []string{
		"10.0.0.1%eth0",     // IPv6-style zone on an IPv4 literal
		"10.0.0.0/",         // trailing slash, no prefix len
		"/8",                // prefix only
		"10.0.0.0/8/8",      // double prefix
		"10.0.0.256",        // octet out of range
		"0x0a.0.0.1",        // hex octet
		"10.0.0.1 ",         // trailing space
		" 10.0.0.1",         // leading space
		"localhost",         // hostname, not an IP
		strings.Repeat("9", 40), // long numeric junk
	}
	for _, tgt := range hostile {
		dec, err := Enforce("T5", Operation{
			Capability:   sandbox.CapReadFS,
			DataClass:    Public,
			EgressTarget: tgt,
		})
		if err != nil {
			// A config error is acceptable (it is not a silent allow); just must not panic.
			continue
		}
		if dec.Allowed {
			t.Errorf("FAIL-OPEN: Enforce(T5, egress %q) ALLOWED a malformed/unroutable target. Reason=%q",
				tgt, dec.Reason)
		}
	}
}
