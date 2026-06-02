// Package capability implements node capability manifest generation and
// control-plane negotiation for the Helix Cluster OS (HXC-1309).
//
// A CapabilityManifest describes what a node offers — CPU cores, memory,
// GPU/NPU/FPGA presence, free-form feature tags, and a tier label.
// A Requirement describes what a workload needs.
// Negotiate checks whether a manifest satisfies a requirement and, when it
// does not, reports every gap in deterministic order.
//
// Manifests are serialised to/from YAML via Render and ParseManifest so that
// they can be exchanged over the control plane without touching protobuf.
package capability

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// CapabilityManifest describes the hardware and software capabilities of a
// single cluster node.  All string fields are case-sensitive.
//
// Features must be non-nil after construction; use []string{} for "none".
// Render encodes the manifest as YAML; ParseManifest is its inverse.
type CapabilityManifest struct {
	// NodeID is the unique identifier for the node (non-empty after Validate).
	NodeID string `yaml:"node_id"`

	// Tier is a device-profile tier label, e.g. "T3" (non-empty after Validate).
	Tier string `yaml:"tier"`

	// Arch is the CPU architecture, e.g. "amd64" or "arm64".
	Arch string `yaml:"arch"`

	// CPUCores is the number of logical CPU cores (>0 after Validate).
	CPUCores int `yaml:"cpu_cores"`

	// MemoryMB is the total system memory in mebibytes (>0 after Validate).
	MemoryMB int64 `yaml:"memory_mb"`

	// GPUCount is the number of discrete GPUs (0 = none).
	GPUCount int `yaml:"gpu_count"`

	// GPUClass describes the GPU product family, e.g. "A100" (may be empty when
	// GPUCount is 0).
	GPUClass string `yaml:"gpu_class"`

	// NPUTOPS is the NPU/TPU peak throughput in tera-operations per second.
	// 0 means no NPU present.
	NPUTOPS float64 `yaml:"npu_tops"`

	// FPGAPresent is true when the node has at least one accessible FPGA.
	FPGAPresent bool `yaml:"fpga_present"`

	// Features is an ordered, deduplicated list of capability tags such as
	// "avx512", "nvlink", "rdma".  Validate ensures it is sorted and
	// deduplicated.
	Features []string `yaml:"features"`
}

// Validate checks structural invariants and normalises the Features slice.
//
// Returned errors are non-nil when:
//   - NodeID, Tier, or Arch is empty;
//   - CPUCores is ≤ 0;
//   - MemoryMB is ≤ 0.
//
// On success, Features is sorted and deduplicated in-place; duplicate entries
// are not an error.
func (m *CapabilityManifest) Validate() error {
	if m.NodeID == "" {
		return errors.New("capability: NodeID must not be empty")
	}
	if m.Tier == "" {
		return errors.New("capability: Tier must not be empty")
	}
	if m.Arch == "" {
		return errors.New("capability: Arch must not be empty")
	}
	if m.CPUCores <= 0 {
		return fmt.Errorf("capability: CPUCores must be > 0, got %d", m.CPUCores)
	}
	if m.MemoryMB <= 0 {
		return fmt.Errorf("capability: MemoryMB must be > 0, got %d", m.MemoryMB)
	}

	// Normalise Features: deduplicate and sort for canonical representation.
	m.Features = deduplicateSorted(m.Features)
	return nil
}

// deduplicateSorted sorts a string slice and removes duplicate entries,
// returning a new slice.  A nil input returns an empty (non-nil) slice.
func deduplicateSorted(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	cp := make([]string, len(in))
	copy(cp, in)
	sort.Strings(cp)

	out := cp[:1]
	for i := 1; i < len(cp); i++ {
		if cp[i] != cp[i-1] {
			out = append(out, cp[i])
		}
	}
	return out
}

// Render serialises m to YAML.  It returns an error only if yaml.Marshal
// itself fails (which cannot happen for this struct).
func (m CapabilityManifest) Render() ([]byte, error) {
	return yaml.Marshal(m)
}

// ParseManifest deserialises a YAML document produced by Render and returns
// the resulting CapabilityManifest.  It does NOT call Validate automatically —
// the caller may modify fields before validating.
//
// Returns an error if the YAML is malformed or contains fields that are not
// defined on CapabilityManifest (strict mode — unknown fields are rejected to
// prevent silent data loss from misspelled keys such as "cpu_core" instead of
// "cpu_cores").
func ParseManifest(b []byte) (CapabilityManifest, error) {
	var m CapabilityManifest
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return CapabilityManifest{}, fmt.Errorf("capability: parse manifest: %w", err)
	}
	// Guarantee a non-nil Features slice even when the YAML omits the field.
	if m.Features == nil {
		m.Features = []string{}
	}
	return m, nil
}

// Requirement describes the minimum capability a workload needs from a node.
// Zero values mean "no constraint" for numeric fields and false for booleans.
type Requirement struct {
	// AcceptedTiers is the set of tier labels that satisfy the requirement.
	// An empty slice means any tier is accepted.
	AcceptedTiers []string `yaml:"accepted_tiers"`

	// MinCPUCores is the minimum number of CPU cores required (0 = no minimum).
	MinCPUCores int `yaml:"min_cpu_cores"`

	// MinMemoryMB is the minimum memory in MiB required (0 = no minimum).
	MinMemoryMB int64 `yaml:"min_memory_mb"`

	// MinGPUCount is the minimum number of GPUs required (0 = no minimum).
	MinGPUCount int `yaml:"min_gpu_count"`

	// MinNPUTOPS is the minimum NPU throughput required in TOPS (0 = no minimum).
	MinNPUTOPS float64 `yaml:"min_npu_tops"`

	// RequireFPGA mandates that FPGAPresent == true on the candidate node.
	RequireFPGA bool `yaml:"require_fpga"`

	// RequiredFeatures is a list of feature tags that must all appear in the
	// manifest's Features slice.  Order is irrelevant for matching.
	RequiredFeatures []string `yaml:"required_features"`
}

// MatchResult reports the outcome of a Negotiate call.
type MatchResult struct {
	// OK is true when every dimension of the Requirement is satisfied.
	OK bool

	// Unmet contains one entry per failed dimension, in deterministic order:
	//
	//  1. "tier-not-accepted"
	//  2. "cpu"
	//  3. "memory"
	//  4. "gpu"
	//  5. "npu"
	//  6. "fpga"
	//  7. "missing-feature:<name>" — one per missing feature, sorted by name
	//
	// Unmet is nil when OK is true.
	Unmet []string
}

// Negotiate checks whether manifest m satisfies requirement r and returns a
// MatchResult.
//
// Gap names are produced in the order documented on MatchResult.Unmet so that
// callers can rely on deterministic output for logging and assertions.
//
//   - tier-not-accepted: m.Tier is not in r.AcceptedTiers (when AcceptedTiers
//     is non-empty).
//   - cpu:              m.CPUCores < r.MinCPUCores.
//   - memory:           m.MemoryMB < r.MinMemoryMB.
//   - gpu:              m.GPUCount  < r.MinGPUCount.
//   - npu:              m.NPUTOPS   < r.MinNPUTOPS.
//   - fpga:             r.RequireFPGA && !m.FPGAPresent.
//   - missing-feature:<name>: for each entry in r.RequiredFeatures not present
//     in m.Features; names are sorted for determinism.
func Negotiate(m CapabilityManifest, r Requirement) MatchResult {
	var unmet []string

	// 1. Tier check.
	if len(r.AcceptedTiers) > 0 {
		found := false
		for _, t := range r.AcceptedTiers {
			if t == m.Tier {
				found = true
				break
			}
		}
		if !found {
			unmet = append(unmet, "tier-not-accepted")
		}
	}

	// 2. CPU check.
	if r.MinCPUCores > 0 && m.CPUCores < r.MinCPUCores {
		unmet = append(unmet, "cpu")
	}

	// 3. Memory check.
	if r.MinMemoryMB > 0 && m.MemoryMB < r.MinMemoryMB {
		unmet = append(unmet, "memory")
	}

	// 4. GPU check.
	if r.MinGPUCount > 0 && m.GPUCount < r.MinGPUCount {
		unmet = append(unmet, "gpu")
	}

	// 5. NPU check.
	if r.MinNPUTOPS > 0 && m.NPUTOPS < r.MinNPUTOPS {
		unmet = append(unmet, "npu")
	}

	// 6. FPGA check.
	if r.RequireFPGA && !m.FPGAPresent {
		unmet = append(unmet, "fpga")
	}

	// 7. Feature checks — build a presence set from the manifest.
	featureSet := make(map[string]struct{}, len(m.Features))
	for _, f := range m.Features {
		featureSet[f] = struct{}{}
	}
	// Collect missing features and sort for determinism.
	var missingFeatures []string
	for _, req := range r.RequiredFeatures {
		if _, ok := featureSet[req]; !ok {
			missingFeatures = append(missingFeatures, req)
		}
	}
	sort.Strings(missingFeatures)
	for _, f := range missingFeatures {
		unmet = append(unmet, "missing-feature:"+f)
	}

	if len(unmet) == 0 {
		return MatchResult{OK: true}
	}
	return MatchResult{OK: false, Unmet: unmet}
}

