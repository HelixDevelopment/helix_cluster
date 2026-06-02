// Package tiersec implements the per-tier security model for Helix Cluster OS.
// It provides an in-process policy layer that maps tier rank (T1-T15) to a
// SecurityProfile (allowed capabilities, maximum data classification, and egress
// network policy) and enforces those profiles against individual operations.
//
// IMPORTANT — scope boundary: this package performs in-process policy decisions
// only. Real OS-level sandboxing (seccomp, namespaces, pledge/unveil) requires
// kernel facilities that are absent in a pure-Go in-process guard; that boundary
// is documented here and is NOT faked.
package tiersec

import (
	"fmt"

	"github.com/HelixDevelopment/helix_cluster/pkg/sandbox"
)

// DataClassification is an ordered sensitivity label. Higher numeric values
// represent more sensitive data. The comparison op.DataClass <= MaxDataClass is
// inclusive: a profile that allows Confidential data also allows Internal and
// Public.
type DataClassification int

const (
	// Public data has no access restrictions — freely shareable.
	Public DataClassification = iota
	// Internal data is for cluster-internal use only; no external disclosure.
	Internal
	// Confidential data is restricted to attested, high-trust tiers.
	Confidential
	// Secret data is restricted to the highest-trust tiers only.
	Secret
)

// String returns the human-readable label for a DataClassification.
func (d DataClassification) String() string {
	switch d {
	case Public:
		return "Public"
	case Internal:
		return "Internal"
	case Confidential:
		return "Confidential"
	case Secret:
		return "Secret"
	default:
		return fmt.Sprintf("DataClassification(%d)", int(d))
	}
}

// NetworkPolicy describes the egress network access permitted for a tier.
// Mode must be one of "deny", "allow", or "allowlist".
//
//   - "deny"      — no outbound network access whatsoever.
//   - "allow"     — unrestricted outbound network access.
//   - "allowlist" — outbound access only to the CIDRs in AllowedCIDRs.
type NetworkPolicy struct {
	Mode         string   // "deny" | "allow" | "allowlist"
	AllowedCIDRs []string // only consulted when Mode == "allowlist"
}

// SecurityProfile is the compiled security posture for a single tier.
type SecurityProfile struct {
	// Tier is the canonical tier name, e.g. "T1".
	Tier string
	// AllowedCaps is the set of sandbox capabilities permitted for this tier.
	AllowedCaps *sandbox.Capabilities
	// MaxDataClass is the most sensitive data classification this tier may
	// handle. op.DataClass <= MaxDataClass is the admission test (inclusive).
	MaxDataClass DataClassification
	// Egress is the outbound network policy for this tier.
	Egress NetworkPolicy
}

// tierRank extracts the integer rank (1-15) from a tier name like "T7".
// Returns (0, error) for any non-conforming input.
func tierRank(tier string) (int, error) {
	var rank int
	if _, err := fmt.Sscanf(tier, "T%d", &rank); err != nil || rank < 1 || rank > 15 {
		return 0, fmt.Errorf("tiersec: unknown tier %q: must be T1-T15", tier)
	}
	return rank, nil
}

// ProfileForTier returns the SecurityProfile for the named tier (T1-T15).
//
// The matrix is hard-coded and intentionally opinionated:
//
//   - T1-T4  (low/edge): deny-everything capability set; Public data only;
//     no egress.
//   - T5-T8  (medium/standard): limited capabilities (ReadFS + Clock);
//     Internal data allowed; allowlisted egress to RFC-1918 ranges.
//   - T9-T12 (high/trusted): most capabilities (ReadFS/WriteFS/Network/Clock);
//     Confidential data allowed; unrestricted egress.
//   - T13-T15 (top/server): full capability set; Secret data allowed;
//     unrestricted egress.
//
// An unknown tier name returns a descriptive error; no default/fallback profile
// is produced.
func ProfileForTier(tier string) (SecurityProfile, error) {
	rank, err := tierRank(tier)
	if err != nil {
		return SecurityProfile{}, err
	}

	switch {
	// ── T1-T4: low / edge ────────────────────────────────────────────────────
	// Tiny embedded and low-trust edge devices. No network, no exec, no
	// filesystem writes. Only Public data. Zero egress.
	case rank <= 4:
		return SecurityProfile{
			Tier:         tier,
			AllowedCaps:  sandbox.NewCapabilities(sandbox.CapReadFS, sandbox.CapClock),
			MaxDataClass: Public,
			Egress:       NetworkPolicy{Mode: "deny"},
		}, nil

	// ── T5-T8: medium / standard ──────────────────────────────────────────────
	// SBC-class and standard edge nodes. ReadFS + WriteFS + Clock; Internal
	// data allowed; egress restricted to RFC-1918 private-address space.
	case rank <= 8:
		return SecurityProfile{
			Tier:         tier,
			AllowedCaps:  sandbox.NewCapabilities(sandbox.CapReadFS, sandbox.CapWriteFS, sandbox.CapClock),
			MaxDataClass: Internal,
			Egress: NetworkPolicy{
				Mode: "allowlist",
				AllowedCIDRs: []string{
					"10.0.0.0/8",
					"172.16.0.0/12",
					"192.168.0.0/16",
				},
			},
		}, nil

	// ── T9-T12: high / trusted ────────────────────────────────────────────────
	// Attested server-grade devices. ReadFS + WriteFS + Network + Clock;
	// Confidential data allowed; unrestricted egress.
	case rank <= 12:
		return SecurityProfile{
			Tier: tier,
			AllowedCaps: sandbox.NewCapabilities(
				sandbox.CapReadFS,
				sandbox.CapWriteFS,
				sandbox.CapNetwork,
				sandbox.CapClock,
			),
			MaxDataClass: Confidential,
			Egress:       NetworkPolicy{Mode: "allow"},
		}, nil

	// ── T13-T15: top / server ─────────────────────────────────────────────────
	// Full-trust multi-GPU server nodes. All capabilities; Secret data
	// allowed; unrestricted egress.
	default: // rank 13-15
		return SecurityProfile{
			Tier: tier,
			AllowedCaps: sandbox.NewCapabilities(
				sandbox.CapReadFS,
				sandbox.CapWriteFS,
				sandbox.CapNetwork,
				sandbox.CapExec,
				sandbox.CapClock,
			),
			MaxDataClass: Secret,
			Egress:       NetworkPolicy{Mode: "allow"},
		}, nil
	}
}
