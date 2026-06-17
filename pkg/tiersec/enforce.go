package tiersec

import (
	"fmt"
	"net"

	"github.com/HelixDevelopment/helix_cluster/pkg/sandbox"
)

// Operation describes a single action a workload wishes to perform.
type Operation struct {
	// Capability is the sandbox resource class the operation requires.
	Capability sandbox.Capability
	// DataClass is the sensitivity of data the operation will touch.
	DataClass DataClassification
	// EgressTarget is the destination address or CIDR the operation will
	// contact over the network. An empty string means no outbound network
	// contact is attempted and the egress check is skipped.
	EgressTarget string
}

// Decision is the outcome produced by Enforce for a (tier, operation) pair.
type Decision struct {
	// Tier is the tier name against which the decision was made.
	Tier string
	// Op is the operation that was evaluated.
	Op Operation
	// Allowed is true iff every policy dimension permitted the operation.
	Allowed bool
	// Reason is a human-readable explanation; non-empty on both allow and deny.
	Reason string
}

// Enforce evaluates op against the SecurityProfile for tier and returns a
// Decision. An error is returned only for configuration problems (unknown tier
// or malformed allowlist CIDR); a policy DENY is a normal outcome and is
// returned as (Decision{Allowed: false}, nil).
//
// Admission rules (all three must hold for ALLOW):
//
//  1. Capability: profile.AllowedCaps.Has(op.Capability) must be true.
//  2. Data class: op.DataClass <= profile.MaxDataClass (inclusive).
//  3. Egress: if op.EgressTarget != "" and profile.Egress.Mode == "deny",
//     the operation is denied. If Mode == "allowlist", op.EgressTarget must
//     fall inside one of the AllowedCIDRs (stdlib net.ParseCIDR / Contains).
//     Mode == "allow" always permits any target.
//
// Reason strings always identify the failing dimension so callers can act on
// them programmatically (they contain the literal words "capability", "data
// class", or "egress" as appropriate).
func Enforce(tier string, op Operation) (Decision, error) {
	profile, err := ProfileForTier(tier)
	if err != nil {
		return Decision{}, err
	}

	// ── 1. Capability check ──────────────────────────────────────────────────
	if !profile.AllowedCaps.Has(op.Capability) {
		return Decision{
			Tier:    tier,
			Op:      op,
			Allowed: false,
			Reason: fmt.Sprintf(
				"capability %q not granted to tier %s",
				op.Capability, tier,
			),
		}, nil
	}

	// ── 2. Data-class check (inclusive: op.DataClass == MaxDataClass is OK) ──
	if op.DataClass > profile.MaxDataClass {
		return Decision{
			Tier:    tier,
			Op:      op,
			Allowed: false,
			Reason: fmt.Sprintf(
				"data class %s exceeds maximum %s for tier %s",
				op.DataClass, profile.MaxDataClass, tier,
			),
		}, nil
	}

	// ── 3. Egress check ──────────────────────────────────────────────────────
	if op.EgressTarget != "" {
		switch profile.Egress.Mode {
		case "deny":
			return Decision{
				Tier:    tier,
				Op:      op,
				Allowed: false,
				Reason: fmt.Sprintf(
					"egress to %q denied: tier %s has no outbound network access",
					op.EgressTarget, tier,
				),
			}, nil

		case "allowlist":
			ok, checkErr := egressPermitted(op.EgressTarget, profile.Egress.AllowedCIDRs)
			if checkErr != nil {
				return Decision{}, checkErr
			}
			if !ok {
				return Decision{
					Tier:    tier,
					Op:      op,
					Allowed: false,
					Reason: fmt.Sprintf(
						"egress to %q denied: not in allowlist for tier %s",
						op.EgressTarget, tier,
					),
				}, nil
			}

		case "allow":
			// unrestricted — no further check needed

		default:
			return Decision{}, fmt.Errorf(
				"tiersec: tier %s has unknown egress mode %q", tier, profile.Egress.Mode,
			)
		}
	}

	// ── All dimensions passed ─────────────────────────────────────────────────
	return Decision{
		Tier:    tier,
		Op:      op,
		Allowed: true,
		Reason: fmt.Sprintf(
			"tier %s: capability %q granted, data class %s <= %s, egress OK",
			tier, op.Capability, op.DataClass, profile.MaxDataClass,
		),
	}, nil
}

// egressPermitted reports whether target (IP address or CIDR) is permitted by
// the allowlist. It uses stdlib net.ParseCIDR and (*net.IPNet).Contains.
//
// target may be:
//   - a bare IP address ("192.168.1.1") — permitted iff the address falls inside
//     one of the allowlist CIDRs.
//   - a CIDR prefix ("192.168.1.0/24") — permitted iff the ENTIRE prefix is a
//     subset of (fully contained in) one of the allowlist CIDRs. Probing only the
//     prefix's network address would FAIL OPEN: a deliberately wide target such
//     as "10.0.0.0/4" (which Go masks to 0.0.0.0/4 and spans public space like
//     8.8.8.8/11.x.x.x) would land its network address inside 10.0.0.0/8 and be
//     wrongly granted, exfiltrating to the public Internet under an allowlist
//     tier. Full-subset containment closes that hole.
//
// Returns an error only for genuinely malformed allowlist CIDR entries (caller
// misconfiguration, not a policy DENY).
func egressPermitted(target string, cidrs []string) (bool, error) {
	// Parse the allowlist once; surface malformed entries as a hard error.
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return false, fmt.Errorf(
				"tiersec: malformed allowlist CIDR %q: %w", cidr, err,
			)
		}
		nets = append(nets, network)
	}

	// Bare IP target: point containment.
	if ip := net.ParseIP(target); ip != nil {
		for _, network := range nets {
			if network.Contains(ip) {
				return true, nil
			}
		}
		return false, nil
	}

	// CIDR target: the entire prefix must be a subset of an allowlist CIDR.
	_, targetNet, parseErr := net.ParseCIDR(target)
	if parseErr != nil {
		// target is neither a valid IP nor a valid CIDR — treat as non-routable
		// (deny), but do NOT return a hard error (that would hide the deny reason).
		return false, nil
	}
	for _, network := range nets {
		if cidrSubset(targetNet, network) {
			return true, nil
		}
	}
	return false, nil
}

// cidrSubset reports whether the inner prefix is fully contained within the
// outer prefix: outer must contain inner's network address AND inner must be no
// wider than outer (inner prefix length >= outer prefix length). This prevents a
// broad target prefix from being admitted merely because its network address
// happens to fall inside a narrower allowlist entry.
func cidrSubset(inner, outer *net.IPNet) bool {
	if !outer.Contains(inner.IP) {
		return false
	}
	innerOnes, innerBits := inner.Mask.Size()
	outerOnes, outerBits := outer.Mask.Size()
	// Mismatched address families (e.g. IPv4 vs IPv6 mask) are not comparable.
	if innerBits != outerBits {
		return false
	}
	return innerOnes >= outerOnes
}
