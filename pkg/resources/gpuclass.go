package resources

import (
	"fmt"
	"strings"
)

// GPU performance-class modeling.
//
// This file extends the raw DRM enumeration (drm.go) with scheduler-facing
// derived signals: a stable per-GPU FINGERPRINT, a relative compute MULTIPLIER
// catalog keyed by PCI "vendor:device", and helpers that fold attestation and
// thermal-throttle state into an *effective* multiplier. The intent is that a
// scheduler can weight a node's GPU by its real class (a datacenter accelerator
// is worth far more than an integrated iGPU) and by its current thermal
// headroom, rather than treating every "GPU" as one undifferentiated unit.
//
// All inputs are real PCI identities parsed from sysfs by ParseDRMTree, so the
// catalog lookups below operate on the same lossless "0x...." hex tokens the
// kernel reports.

// UnknownGPUMultiplier is the documented default relative compute multiplier
// returned by Multiplier for any GPU whose vendor:device identity is not present
// in the catalog. It is deliberately conservative (1.0 == "one baseline unit")
// so an unrecognized accelerator is never over-credited by the scheduler.
const UnknownGPUMultiplier = 1.0

// ThermalThrottleFactor is the fraction of nominal compute that remains usable
// while a GPU reports it is thermally throttling. A throttling GPU keeps doing
// work but at reduced sustained clocks, so its effective multiplier is scaled by
// this factor (strictly < 1 so throttling always lowers the weight).
const ThermalThrottleFactor = 0.5

// gpuClass is a catalog entry: the human-facing model name and its relative
// compute multiplier versus a 1.0 baseline (a mid-range consumer card).
type gpuClass struct {
	name       string
	multiplier float64
}

// gpuCatalog maps a canonical "vendor:device" PCI identity (lower-case "0x" hex,
// exactly as ParseDRMTree emits it) to a known GPU class. The multipliers encode
// a deliberate ordering: high-end datacenter accelerators > consumer discrete >
// integrated. These are relative weights for scheduling, not benchmark scores.
//
// Keeping this as data (not code branches) is what makes Multiplier's
// catalog-driven behavior testable: a known key MUST return its exact entry, and
// changing a value here MUST change the returned multiplier.
var gpuCatalog = map[string]gpuClass{
	// NVIDIA datacenter accelerators (vendor 0x10de).
	"0x10de:0x20b2": {name: "NVIDIA A100 80GB", multiplier: 10.0},
	"0x10de:0x20f1": {name: "NVIDIA A100", multiplier: 9.0},
	"0x10de:0x2330": {name: "NVIDIA H100", multiplier: 16.0},
	// NVIDIA consumer discrete.
	"0x10de:0x2204": {name: "NVIDIA RTX 3090", multiplier: 4.0},
	"0x10de:0x2684": {name: "NVIDIA RTX 4090", multiplier: 6.0},
	// AMD datacenter + consumer discrete (vendor 0x1002).
	"0x1002:0x740c": {name: "AMD Instinct MI250X", multiplier: 12.0},
	"0x1002:0x73bf": {name: "AMD Radeon RX 6900 XT", multiplier: 3.5},
	// Intel integrated (vendor 0x8086): low relative compute.
	"0x8086:0x9a60": {name: "Intel Iris Xe (integrated)", multiplier: 0.25},
}

// canonicalIdentity extracts the leading "vendor:device" token used as the
// catalog key from a GPUInfo.Model. GPUInfoFromDevices renders Model as one of:
//
//	"0x10de:0x2204"            (single card)
//	"0x10de:0x2204 x 4"        (N homogeneous cards)
//	"0x1002:0x73bf,0x10de:..." (heterogeneous cards, comma-joined)
//
// For all three shapes the FIRST whitespace/comma-delimited field is a
// "vendor:device" identity, which is what we key the catalog and fingerprint on.
// An empty or malformed Model yields "" (no identity), which routes Multiplier
// to the unknown default.
func canonicalIdentity(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	// Cut at the first comma (heterogeneous list) ...
	if i := strings.IndexByte(model, ','); i >= 0 {
		model = model[:i]
	}
	// ... then at the first space (the " x N" multiplicity suffix).
	if i := strings.IndexByte(model, ' '); i >= 0 {
		model = model[:i]
	}
	model = strings.TrimSpace(model)
	// A valid identity is exactly "vendor:device" (two 0x-prefixed tokens).
	v, d, ok := strings.Cut(model, ":")
	if !ok || !strings.HasPrefix(v, "0x") || !strings.HasPrefix(d, "0x") {
		return ""
	}
	return v + ":" + d
}

// Fingerprint returns a stable, distinct identity string for the GPU(s) in g.
// The fingerprint is "vendor:device" and, when a hardware UUID is known (read
// from the DRM unique_id node), "vendor:device:uuid". It is deterministic for a
// given GPUInfo and distinct for GPUs of different PCI identity or UUID, so a
// scheduler/placement layer can key on it stably across reads.
//
// When the model carries no parseable identity (empty/garbled Model) the
// fingerprint is "unknown" (plus any UUID), never a panic and never an empty
// string that would collide across distinct nodes.
func Fingerprint(g GPUInfo) string {
	id := canonicalIdentity(g.Model)
	if id == "" {
		id = "unknown"
	}
	if uuid := strings.TrimSpace(g.UUID); uuid != "" {
		return id + ":" + uuid
	}
	return id
}

// Multiplier returns the relative compute multiplier for g by looking up its
// canonical vendor:device identity in the catalog. A KNOWN identity returns its
// EXACT catalog multiplier; an unknown or unparseable identity returns the
// documented UnknownGPUMultiplier default.
//
// This is a pure catalog lookup — it does NOT apply thermal/attestation scaling
// (use EffectiveMultiplier for the scheduler-weighted value). Returning a
// constant here regardless of g.Model would fail the "known model -> exact
// multiplier" contract.
func Multiplier(g GPUInfo) float64 {
	id := canonicalIdentity(g.Model)
	if id == "" {
		return UnknownGPUMultiplier
	}
	if c, ok := gpuCatalog[id]; ok {
		return c.multiplier
	}
	return UnknownGPUMultiplier
}

// CatalogName returns the human-facing model name for g's identity if it is in
// the catalog, and ok=false otherwise. This is the read-side companion to the
// catalog used for diagnostics/inventory.
func CatalogName(g GPUInfo) (string, bool) {
	id := canonicalIdentity(g.Model)
	if id == "" {
		return "", false
	}
	c, ok := gpuCatalog[id]
	if !ok {
		return "", false
	}
	return c.name, true
}

// EffectiveMultiplier returns the scheduler-facing weight: the catalog
// Multiplier scaled DOWN by ThermalThrottleFactor when the GPU is currently
// thermally throttling. Attestation does not change the magnitude here (an
// attested vs. unattested GPU of the same class has the same raw compute); it is
// surfaced separately via IsAttested so a policy can refuse to schedule on
// unattested hardware. A throttling GPU's effective multiplier is therefore
// strictly less than its nominal Multiplier.
func EffectiveMultiplier(g GPUInfo) float64 {
	m := Multiplier(g)
	if g.ThermalThrottling {
		m *= ThermalThrottleFactor
	}
	return m
}

// SetAttested returns a copy of g with the Attested flag set to v. GPUInfo is a
// small value type, so we round-trip a copy rather than mutate through a pointer;
// this keeps the additive fields easy to thread through immutable snapshots.
func SetAttested(g GPUInfo, v bool) GPUInfo {
	g.Attested = v
	return g
}

// IsAttested reports whether g has been marked as remotely attested.
func IsAttested(g GPUInfo) bool {
	return g.Attested
}

// SetThermalThrottling returns a copy of g with the ThermalThrottling flag set
// to v.
func SetThermalThrottling(g GPUInfo, v bool) GPUInfo {
	g.ThermalThrottling = v
	return g
}

// IsThermalThrottling reports whether g is currently flagged as thermally
// throttling.
func IsThermalThrottling(g GPUInfo) bool {
	return g.ThermalThrottling
}

// DescribeClass renders a short human string for logs/inventory, e.g.
// "NVIDIA RTX 3090 (x4)" for a known model or "unknown (x1)" otherwise. It is a
// standalone helper rather than a method so it does not alter GPUInfo's existing
// JSON/zero-value behavior.
func DescribeClass(g GPUInfo) string {
	if name, ok := CatalogName(g); ok {
		return fmt.Sprintf("%s (x%.2g)", name, Multiplier(g))
	}
	return fmt.Sprintf("unknown (x%.2g)", Multiplier(g))
}
