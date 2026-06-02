// Package fedtrust implements a pure-Go cross-cluster federated-trust
// admission-policy evaluator (HXC-1358).
//
// # Evaluation Precedence
//
// For a given (from ClusterIdentity, target ClusterIdentity) pair the
// evaluator applies the following precedence (highest to lowest):
//
//  1. from.TrustDomain ∈ DeniedDomains  → Deny  (rule "denied-domain").
//     Deny always wins, even when the domain also appears in AllowedDomains.
//  2. AllowedDomains non-empty AND from.TrustDomain ∉ AllowedDomains → Deny
//     (rule "domain-not-allowed").
//  3. from.TrustLevel < MinTrustLevel → Deny (rule "trust-level").
//  4. CanBind != nil AND !CanBind(from, target) → Deny (rule "topology").
//  5. else → Allow (rule "allowed").
//
// A nil CanBind field means no topology gate is applied.
//
// This evaluator does not depend on OPA/Rego or any external trust-federation
// library. Topology constraints are expressed as an injected predicate
// (CanBind) so callers retain full control without coupling this package to
// any concrete topology implementation.
package fedtrust

import (
	"fmt"
	"sort"
)

// TrustLevel is an ordered enumeration of cluster-to-cluster trust strengths.
// Higher numeric values indicate stronger trust.
type TrustLevel int

const (
	// TrustUnknown is the zero value and represents an unverified cluster.
	TrustUnknown TrustLevel = iota
	// TrustProvisional represents a cluster that has completed basic
	// bootstrapping but whose identity has not yet been fully attested.
	TrustProvisional
	// TrustStandard represents a fully attested cluster operating within
	// normal federation bounds.
	TrustStandard
	// TrustHardened represents a cluster that has undergone additional
	// security hardening and continuous attestation.
	TrustHardened
)

// String returns a human-readable label for the TrustLevel. It is used in
// Deny reasons so operators can see exactly which level was required vs.
// which level was presented.
func (t TrustLevel) String() string {
	switch t {
	case TrustUnknown:
		return "Unknown"
	case TrustProvisional:
		return "Provisional"
	case TrustStandard:
		return "Standard"
	case TrustHardened:
		return "Hardened"
	default:
		return fmt.Sprintf("TrustLevel(%d)", int(t))
	}
}

// ClusterIdentity carries the attributes of a federated cluster that are
// evaluated by the policy.
type ClusterIdentity struct {
	// Name is a human-readable cluster identifier used in Reason strings.
	Name string
	// TrustDomain is the SPIFFE-style trust domain (e.g. "prod.example.com")
	// that this cluster belongs to.
	TrustDomain string
	// TrustLevel is the attested trust strength of this cluster.
	TrustLevel TrustLevel
}

// TrustConfig is the configuration for a FederationTrustPolicy.
//
// AllowedDomains and DeniedDomains are matched against ClusterIdentity.TrustDomain
// by exact string comparison (case-sensitive).
//
// CanBind, if non-nil, is called only after all domain and trust-level checks
// pass. Returning false from CanBind causes a Deny with rule "topology".
type TrustConfig struct {
	// AllowedDomains is the explicit allowlist of trust domains permitted to
	// federate. When empty, domain-allowlist checking is skipped (any domain
	// not in DeniedDomains is accepted).
	AllowedDomains []string

	// DeniedDomains is the denylist of trust domains that are always refused,
	// even if the domain also appears in AllowedDomains (deny-precedence).
	DeniedDomains []string

	// MinTrustLevel is the minimum TrustLevel a requesting cluster must
	// present. A cluster whose TrustLevel is strictly less than MinTrustLevel
	// is denied with rule "trust-level".
	MinTrustLevel TrustLevel

	// CanBind is an optional topology predicate. When non-nil it is invoked
	// with (from, target) after all earlier checks pass. Returning false
	// produces a Deny with rule "topology". A nil CanBind skips this gate.
	CanBind func(from, to ClusterIdentity) bool
}

// Decision is the output of a single Evaluate call.
type Decision struct {
	// Allow is true when the federation request is permitted.
	Allow bool
	// Reason is a human-readable, concrete explanation of the decision.
	// It always names the trigger values (domain name, trust levels, etc.)
	// so operators can diagnose denials without guessing.
	Reason string
	// Rule identifies which policy clause fired:
	//   "denied-domain"      – from.TrustDomain ∈ DeniedDomains
	//   "domain-not-allowed" – AllowedDomains non-empty, domain absent
	//   "trust-level"        – from.TrustLevel < MinTrustLevel
	//   "topology"           – CanBind(from, target) returned false
	//   "allowed"            – all checks passed
	Rule string
}

// FederationTrustPolicy evaluates cross-cluster federated-trust admission
// requests according to the configured TrustConfig.
//
// All fields are immutable after construction; FederationTrustPolicy is safe
// for concurrent use.
type FederationTrustPolicy struct {
	allowedDomains map[string]struct{}
	deniedDomains  map[string]struct{}
	minTrustLevel  TrustLevel
	canBind        func(from, to ClusterIdentity) bool
}

// NewFederationTrustPolicy constructs a FederationTrustPolicy from cfg.
//
// Slices in cfg are copied defensively; callers may mutate their originals
// without affecting the returned policy.
func NewFederationTrustPolicy(cfg TrustConfig) *FederationTrustPolicy {
	p := &FederationTrustPolicy{
		minTrustLevel: cfg.MinTrustLevel,
		canBind:       cfg.CanBind,
	}
	if len(cfg.DeniedDomains) > 0 {
		p.deniedDomains = make(map[string]struct{}, len(cfg.DeniedDomains))
		for _, d := range cfg.DeniedDomains {
			p.deniedDomains[d] = struct{}{}
		}
	}
	if len(cfg.AllowedDomains) > 0 {
		p.allowedDomains = make(map[string]struct{}, len(cfg.AllowedDomains))
		for _, d := range cfg.AllowedDomains {
			p.allowedDomains[d] = struct{}{}
		}
	}
	return p
}

// Evaluate applies the policy to a (from, target) ClusterIdentity pair and
// returns an admission Decision.
//
// Evaluation is purely in-memory and deterministic given identical inputs.
// No I/O, goroutines, or wall-clock reads are performed.
func (p *FederationTrustPolicy) Evaluate(from, target ClusterIdentity) Decision {
	// Precedence 1: denied-domain (wins over everything, including allowlist).
	if _, denied := p.deniedDomains[from.TrustDomain]; denied {
		return Decision{
			Allow: false,
			Reason: fmt.Sprintf(
				"trust domain %q (cluster %q) is explicitly denied",
				from.TrustDomain, from.Name,
			),
			Rule: "denied-domain",
		}
	}

	// Precedence 2: allowlist gate — only applies when AllowedDomains is non-empty.
	if len(p.allowedDomains) > 0 {
		if _, ok := p.allowedDomains[from.TrustDomain]; !ok {
			return Decision{
				Allow: false,
				Reason: fmt.Sprintf(
					"trust domain %q (cluster %q) is not in the allowed-domains list %v",
					from.TrustDomain, from.Name, sortedKeys(p.allowedDomains),
				),
				Rule: "domain-not-allowed",
			}
		}
	}

	// Precedence 3: trust-level floor.
	if from.TrustLevel < p.minTrustLevel {
		return Decision{
			Allow: false,
			Reason: fmt.Sprintf(
				"cluster %q trust level %s is below the minimum required level %s",
				from.Name, from.TrustLevel, p.minTrustLevel,
			),
			Rule: "trust-level",
		}
	}

	// Precedence 4: topology predicate.
	if p.canBind != nil && !p.canBind(from, target) {
		return Decision{
			Allow: false,
			Reason: fmt.Sprintf(
				"topology constraint rejected binding from cluster %q (domain %q) to cluster %q (domain %q)",
				from.Name, from.TrustDomain, target.Name, target.TrustDomain,
			),
			Rule: "topology",
		}
	}

	// Precedence 5: all checks passed.
	return Decision{
		Allow: true,
		Reason: fmt.Sprintf(
			"cluster %q (domain %q, level %s) is permitted to bind to cluster %q (domain %q)",
			from.Name, from.TrustDomain, from.TrustLevel,
			target.Name, target.TrustDomain,
		),
		Rule: "allowed",
	}
}

// sortedKeys returns the keys of m in sorted order for deterministic Reason
// strings (map iteration order is not guaranteed in Go).
func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
