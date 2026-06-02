// Package residency implements a pure-Go data-sovereignty / data-residency
// admission-policy evaluator (HXC-1359).
//
// # Evaluation Precedence
//
// For a given (DataClass, targetRegion) pair the evaluator walks the first
// rule whose DataClass matches and applies the following precedence (highest
// to lowest):
//
//  1. DeniedRegions match  → Deny  (deny always wins, even if AllowedRegions
//     also contains the region).
//  2. AllowedRegions non-empty AND targetRegion ∈ AllowedRegions → Allow.
//  3. AllowedRegions empty (meaning "allow anywhere") → Allow.
//  4. AllowedRegions non-empty AND targetRegion ∉ AllowedRegions → Deny.
//  5. No rule matches the DataClass → Allow iff ResidencyPolicy.defaultAllow,
//     else Deny.
//
// The targetCell parameter is carried through to the Decision for caller
// convenience but does not participate in region-level evaluation.
//
// The Jurisdiction field on Rule is informational metadata; if non-empty it
// is echoed in the Deny reason when the region is not in the AllowedRegions
// set, giving operators jurisdiction-aware diagnostics.
package residency

import (
	"fmt"
	"sort"
)

// DataClass identifies the sensitivity tier of a data object.
type DataClass string

const (
	// ClassPublic represents data with no residency constraints.
	ClassPublic DataClass = "public"
	// ClassPII represents Personally Identifiable Information subject to
	// regional data-protection legislation (e.g. GDPR).
	ClassPII DataClass = "pii"
	// ClassPHI represents Protected Health Information subject to healthcare
	// regulatory requirements (e.g. HIPAA).
	ClassPHI DataClass = "phi"
)

// Rule expresses the residency constraints for a single DataClass.
//
// Evaluation semantics follow the package-level precedence table. An empty
// AllowedRegions slice means "the class is permitted anywhere (no region
// allowlist)". DeniedRegions is evaluated before AllowedRegions; a region
// present in both is always denied.
type Rule struct {
	// DataClass is the sensitivity tier this rule governs.
	DataClass DataClass
	// AllowedRegions lists every region where this class may be stored or
	// processed. An empty slice means no region restriction applies.
	AllowedRegions []string
	// DeniedRegions lists regions that are explicitly prohibited regardless
	// of AllowedRegions. Evaluated before AllowedRegions (deny-precedence).
	DeniedRegions []string
	// Jurisdiction is an informational label (e.g. "EU", "HIPAA-US"). When
	// non-empty it is included in Deny reasons to aid operator diagnosis.
	// It does not affect the allow/deny outcome.
	Jurisdiction string
}

// Decision is the output of a single Evaluate call.
type Decision struct {
	// Allow is true when the placement is permitted.
	Allow bool
	// Reason is a human-readable explanation of the decision. It always
	// names the concrete trigger (region, class, rule name, etc.).
	Reason string
	// Rule identifies which policy clause fired:
	//   "denied-region"       – matched DeniedRegions
	//   "allowed-region"      – matched AllowedRegions
	//   "open-allowlist"      – AllowedRegions empty → allow everywhere
	//   "region-not-allowed"  – AllowedRegions non-empty, region absent
	//   "default-allow"       – no rule for class, defaultAllow=true
	//   "default-deny"        – no rule for class, defaultAllow=false
	Rule string
}

// ResidencyPolicy holds the ordered rule set and the default stance for
// unknown data classes.
//
// Rules are evaluated in the order supplied to NewResidencyPolicy; the first
// rule whose DataClass matches the evaluated class is applied.
type ResidencyPolicy struct {
	rules        []Rule
	defaultAllow bool
}

// NewResidencyPolicy constructs a ResidencyPolicy from the supplied rules and
// a default-allow stance used when no rule matches the requested DataClass.
//
// Rules are stored in the order provided; the first matching rule wins.
func NewResidencyPolicy(rules []Rule, defaultAllow bool) *ResidencyPolicy {
	// defensive copy so callers cannot mutate the policy after construction
	cp := make([]Rule, len(rules))
	for i, r := range rules {
		cp[i] = Rule{
			DataClass:    r.DataClass,
			Jurisdiction: r.Jurisdiction,
		}
		if len(r.AllowedRegions) > 0 {
			cp[i].AllowedRegions = append([]string(nil), r.AllowedRegions...)
		}
		if len(r.DeniedRegions) > 0 {
			cp[i].DeniedRegions = append([]string(nil), r.DeniedRegions...)
		}
	}
	return &ResidencyPolicy{rules: cp, defaultAllow: defaultAllow}
}

// Evaluate applies the policy to a (class, targetRegion, targetCell) triple
// and returns an admission Decision.
//
// targetCell is carried through to the Reason for traceability but does not
// influence the allow/deny outcome; region-level rules are the unit of
// evaluation.
func (p *ResidencyPolicy) Evaluate(class DataClass, targetRegion, targetCell string) Decision {
	for _, r := range p.rules {
		if r.DataClass != class {
			continue
		}
		// Precedence 1: denied-region check (deny wins over everything).
		if containsRegion(r.DeniedRegions, targetRegion) {
			reason := fmt.Sprintf(
				"region %q is explicitly denied for class %q (cell %q)",
				targetRegion, class, targetCell,
			)
			if r.Jurisdiction != "" {
				reason += fmt.Sprintf("; jurisdiction: %s", r.Jurisdiction)
			}
			return Decision{Allow: false, Reason: reason, Rule: "denied-region"}
		}

		// Precedence 2 & 3: allowed-region check.
		if len(r.AllowedRegions) == 0 {
			// Open allowlist — class is permitted anywhere.
			reason := fmt.Sprintf(
				"class %q has no region restriction; placement in %q (cell %q) is unrestricted",
				class, targetRegion, targetCell,
			)
			return Decision{Allow: true, Reason: reason, Rule: "open-allowlist"}
		}

		if containsRegion(r.AllowedRegions, targetRegion) {
			reason := fmt.Sprintf(
				"region %q is in the allowlist for class %q (cell %q)",
				targetRegion, class, targetCell,
			)
			if r.Jurisdiction != "" {
				reason += fmt.Sprintf("; jurisdiction: %s", r.Jurisdiction)
			}
			return Decision{Allow: true, Reason: reason, Rule: "allowed-region"}
		}

		// Precedence 4: region not in non-empty allowlist → deny.
		reason := fmt.Sprintf(
			"region %q is not in the allowed regions %v for class %q (cell %q)",
			targetRegion, sortedCopy(r.AllowedRegions), class, targetCell,
		)
		if r.Jurisdiction != "" {
			reason += fmt.Sprintf("; jurisdiction: %s", r.Jurisdiction)
		}
		return Decision{Allow: false, Reason: reason, Rule: "region-not-allowed"}
	}

	// Precedence 5: no matching rule.
	if p.defaultAllow {
		return Decision{
			Allow:  true,
			Reason: fmt.Sprintf("no rule for class %q; default policy is allow", class),
			Rule:   "default-allow",
		}
	}
	return Decision{
		Allow:  false,
		Reason: fmt.Sprintf("no rule for class %q; default policy is deny", class),
		Rule:   "default-deny",
	}
}

// containsRegion reports whether needle is present in haystack.
// Comparison is case-sensitive and O(n) — ruleset sizes are small.
func containsRegion(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// sortedCopy returns a sorted copy of ss for deterministic Reason strings.
func sortedCopy(ss []string) []string {
	out := append([]string(nil), ss...)
	sort.Strings(out)
	return out
}
