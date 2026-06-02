// Package exportcontrol implements export-control tier verification and
// country-code KYC checks applied at node onboarding (HXC-1482).
//
// At onboarding, a node declares its country of operation (ISO-3166 alpha-2)
// and the GPU model it intends to expose to the cluster. Nodes located in
// embargoed/denied (Tier3) jurisdictions — and nodes in jurisdictions we do
// not recognize — are prohibited from contributing export-controlled high-end
// accelerators (e.g. NVIDIA H100/A100 class) per U.S. export-control rules.
package exportcontrol

import (
	"errors"
	"strings"
)

// CountryTier classifies a jurisdiction for export-control purposes.
type CountryTier int

const (
	// Tier1 is an allowed/allied jurisdiction. Controlled GPUs are permitted.
	Tier1 CountryTier = iota + 1
	// Tier2 is a restricted jurisdiction. Controlled GPUs are denied.
	Tier2
	// Tier3 is an embargoed/denied jurisdiction. Controlled GPUs are denied.
	Tier3
)

// ErrExportDenied is returned by VerifyOnboarding when a node may not onboard
// with the declared export-controlled GPU from its declared jurisdiction.
var ErrExportDenied = errors.New("exportcontrol: onboarding denied by export-control policy")

// countryTiers maps ISO-3166 alpha-2 country codes to their CountryTier.
// This is a deliberately curated, realistic (not exhaustive) catalog.
var countryTiers = map[string]CountryTier{
	// Tier1 — allied / allowed.
	"US": Tier1, // United States
	"DE": Tier1, // Germany
	"GB": Tier1, // United Kingdom
	"FR": Tier1, // France
	"CA": Tier1, // Canada
	"JP": Tier1, // Japan
	"AU": Tier1, // Australia
	"NL": Tier1, // Netherlands
	"RS": Tier1, // Serbia

	// Tier2 — restricted (controlled GPUs denied, broad compute restrictions).
	"CN": Tier2, // China
	"RU": Tier2, // Russia
	"VE": Tier2, // Venezuela
	"BY": Tier2, // Belarus

	// Tier3 — embargoed / denied.
	"IR": Tier3, // Iran
	"KP": Tier3, // North Korea
	"SY": Tier3, // Syria
	"CU": Tier3, // Cuba
}

// controlledGPUModels lists the substrings that mark a GPU model as
// export-controlled high-end. Defined in ONE place so the policy is testable.
// Matching is case-insensitive and substring-based (e.g. "NVIDIA H100 80GB").
var controlledGPUModels = []string{
	"H100",
	"A100",
	"H200",
	"H800",
	"A800",
	"B200",
	"GH200",
}

// TierForCountry returns the CountryTier for an ISO-3166 alpha-2 country code.
// Lookup is case-insensitive. The boolean is false when the code is unknown.
func TierForCountry(code string) (CountryTier, bool) {
	t, ok := countryTiers[strings.ToUpper(strings.TrimSpace(code))]
	return t, ok
}

// IsControlledGPU reports whether the given GPU model string designates an
// export-controlled high-end accelerator.
func IsControlledGPU(model string) bool {
	m := strings.ToUpper(model)
	for _, c := range controlledGPUModels {
		if strings.Contains(m, c) {
			return true
		}
	}
	return false
}

// NodeOnboarding is the declaration a node provides when joining the cluster.
type NodeOnboarding struct {
	CountryCode string // ISO-3166 alpha-2 (e.g. "US", "IR")
	GPUModel    string // declared GPU model (e.g. "NVIDIA H100 80GB")
}

// VerifyOnboarding applies export-control policy to a node onboarding request.
//
// Policy: a node declaring an export-controlled GPU may onboard only from a
// Tier1 jurisdiction. Nodes from Tier2/Tier3 jurisdictions, and nodes whose
// country code is unknown (treated as restricted), are rejected with
// ErrExportDenied. Nodes declaring a non-controlled GPU are always allowed.
//
// Returns nil when onboarding is permitted.
func VerifyOnboarding(n NodeOnboarding) error {
	if !IsControlledGPU(n.GPUModel) {
		// Not an export-controlled accelerator; no restriction applies.
		return nil
	}

	// LOAD-BEARING GUARD: the country-tier check. A controlled GPU is allowed
	// only from a Tier1 jurisdiction. Unknown codes are treated as restricted.
	tier, known := TierForCountry(n.CountryCode)
	if !known || tier != Tier1 {
		return ErrExportDenied
	}
	return nil
}
