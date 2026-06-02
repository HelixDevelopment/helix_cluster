// Package devicecatalog provides a machine-readable device taxonomy catalog
// with lookup by CPU model string (HXC-1331).
//
// The catalog maps well-known compute devices (x86 servers, ARM SBCs, mobile
// SoCs, etc.) to a tier/trust/compute-class taxonomy so the cluster can resolve
// a freshly-discovered node's CPU model to its placement and trust posture.
package devicecatalog

import "strings"

// ComputeClass classifies the broad compute capability of a device.
type ComputeClass string

const (
	// ComputeServer is a datacenter-grade multi-socket / high-core host.
	ComputeServer ComputeClass = "server"
	// ComputeWorkstation is a high-end desktop / workstation host.
	ComputeWorkstation ComputeClass = "workstation"
	// ComputeEdge is a small board / single-board computer at the edge.
	ComputeEdge ComputeClass = "edge"
	// ComputeMobile is a phone / tablet class SoC.
	ComputeMobile ComputeClass = "mobile"
	// ComputeAccelerator is a device whose primary role is acceleration (GPU/NPU host).
	ComputeAccelerator ComputeClass = "accelerator"
)

// TrustLevel classifies how much the cluster trusts a device for placement.
type TrustLevel string

const (
	// TrustHardened denotes attested datacenter hardware (TPM/secure boot, controlled).
	TrustHardened TrustLevel = "hardened"
	// TrustTrusted denotes known-good managed hardware.
	TrustTrusted TrustLevel = "trusted"
	// TrustStandard denotes ordinary managed devices with no special attestation.
	TrustStandard TrustLevel = "standard"
	// TrustUntrusted denotes consumer / unattested / BYO devices.
	TrustUntrusted TrustLevel = "untrusted"
)

// CatalogEntry is one taxonomy record. MatchModel is matched (case-insensitive
// substring) against a discovered CPU model string.
type CatalogEntry struct {
	MatchModel   string
	Arch         string
	Tier         string
	Trust        TrustLevel
	ComputeClass ComputeClass
}

// Discovery is one record of live-discovery output describing a node.
type Discovery struct {
	CPUModel string
}

// catalog is the authored taxonomy of well-known devices. Order matters: more
// specific entries appear before broader ones so the first substring match wins.
var catalog = []CatalogEntry{
	// --- x86 server ---
	{MatchModel: "AMD EPYC 9654", Arch: "x86_64", Tier: "server-flagship", Trust: TrustHardened, ComputeClass: ComputeServer},
	{MatchModel: "AMD EPYC", Arch: "x86_64", Tier: "server", Trust: TrustHardened, ComputeClass: ComputeServer},
	{MatchModel: "Intel Xeon Platinum", Arch: "x86_64", Tier: "server-flagship", Trust: TrustHardened, ComputeClass: ComputeServer},
	{MatchModel: "Intel Xeon", Arch: "x86_64", Tier: "server", Trust: TrustHardened, ComputeClass: ComputeServer},
	// --- ARM server ---
	{MatchModel: "Ampere Altra", Arch: "aarch64", Tier: "server-arm", Trust: TrustHardened, ComputeClass: ComputeServer},
	{MatchModel: "AWS Graviton", Arch: "aarch64", Tier: "server-arm", Trust: TrustHardened, ComputeClass: ComputeServer},
	// --- x86 workstation ---
	{MatchModel: "AMD Ryzen Threadripper", Arch: "x86_64", Tier: "workstation-hedt", Trust: TrustTrusted, ComputeClass: ComputeWorkstation},
	{MatchModel: "Intel Core i9", Arch: "x86_64", Tier: "workstation", Trust: TrustStandard, ComputeClass: ComputeWorkstation},
	// --- ARM SBC / edge ---
	// Load-bearing entry for HXC-1331: Rockchip RK3588 ARM SBC.
	{MatchModel: "Rockchip RK3588", Arch: "aarch64", Tier: "sbc-arm", Trust: TrustUntrusted, ComputeClass: ComputeEdge},
	{MatchModel: "Broadcom BCM2712", Arch: "aarch64", Tier: "sbc-arm", Trust: TrustUntrusted, ComputeClass: ComputeEdge},
	{MatchModel: "NVIDIA Jetson Orin", Arch: "aarch64", Tier: "sbc-accelerated", Trust: TrustUntrusted, ComputeClass: ComputeAccelerator},
	// --- mobile ---
	{MatchModel: "Apple M2", Arch: "arm64", Tier: "mobile-laptop", Trust: TrustStandard, ComputeClass: ComputeWorkstation},
	{MatchModel: "Qualcomm Snapdragon", Arch: "aarch64", Tier: "mobile-soc", Trust: TrustUntrusted, ComputeClass: ComputeMobile},
	{MatchModel: "MediaTek Dimensity", Arch: "aarch64", Tier: "mobile-soc", Trust: TrustUntrusted, ComputeClass: ComputeMobile},
}

// Lookup resolves a Discovery to a CatalogEntry by case-insensitive substring
// match of d.CPUModel against each entry's MatchModel. The first matching entry
// (in catalog order) is returned with ok=true; an empty entry and false when no
// entry matches.
func Lookup(d Discovery) (CatalogEntry, bool) {
	model := strings.TrimSpace(d.CPUModel)
	if model == "" {
		return CatalogEntry{}, false
	}
	lower := strings.ToLower(model)
	for _, e := range catalog {
		if strings.Contains(lower, strings.ToLower(e.MatchModel)) {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// Entries returns a copy of the full catalog (read-only view for callers).
func Entries() []CatalogEntry {
	out := make([]CatalogEntry, len(catalog))
	copy(out, catalog)
	return out
}
