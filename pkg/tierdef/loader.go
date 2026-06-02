package tierdef

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed tierdef.yaml
var tierdefYAML []byte

// Default loads and validates the built-in tierdef.yaml that is compiled into
// the binary. It is the normal entry-point for callers that want the canonical
// T1-T15 registry.
func Default() (*Registry, error) {
	return LoadTiers(tierdefYAML)
}

// rawDoc is the intermediate structure used during YAML unmarshalling, before
// any validation. It mirrors the on-disk schema exactly.
type rawDoc struct {
	SchemaVersion int       `yaml:"schema_version"`
	Tiers         []TierDef `yaml:"tiers"`
}

// expectedTierCount is the exact number of tiers that must be present.
const expectedTierCount = 15

// LoadTiers parses and validates a YAML document, returning a Registry on
// success. It returns a non-nil error when:
//
//   - schema_version is absent (zero) or not exactly 1;
//   - the tiers list does not contain exactly 15 entries;
//   - the tiers are not named T1..T15 in strictly ascending order;
//   - any min_* field is negative.
func LoadTiers(data []byte) (*Registry, error) {
	var raw rawDoc
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("tierdef: YAML parse error: %w", err)
	}

	// Validate schema version.
	if raw.SchemaVersion != 1 {
		return nil, fmt.Errorf(
			"tierdef: unsupported schema_version %d; only schema_version 1 is accepted",
			raw.SchemaVersion,
		)
	}

	// Validate tier count.
	if len(raw.Tiers) != expectedTierCount {
		return nil, fmt.Errorf(
			"tierdef: document contains %d tier(s); exactly %d (T1..T15) are required",
			len(raw.Tiers), expectedTierCount,
		)
	}

	// Validate names, ordering, and field non-negativity.
	seen := make(map[string]bool, expectedTierCount)
	for i, td := range raw.Tiers {
		wantName := fmt.Sprintf("T%d", i+1)
		if td.Tier != wantName {
			return nil, fmt.Errorf(
				"tierdef: tier[%d] has name %q; want %q (tiers must be T1..T15 in ascending order)",
				i, td.Tier, wantName,
			)
		}
		if seen[td.Tier] {
			return nil, fmt.Errorf("tierdef: duplicate tier %q in document", td.Tier)
		}
		seen[td.Tier] = true

		// Each min field must be non-negative.
		if td.MinCPUCores < 0 {
			return nil, fmt.Errorf("tierdef: %s: min_cpu_cores must be >= 0, got %d", td.Tier, td.MinCPUCores)
		}
		if td.MinMemoryMB < 0 {
			return nil, fmt.Errorf("tierdef: %s: min_memory_mb must be >= 0, got %d", td.Tier, td.MinMemoryMB)
		}
		if td.MinGPU < 0 {
			return nil, fmt.Errorf("tierdef: %s: min_gpu must be >= 0, got %d", td.Tier, td.MinGPU)
		}
		if td.MinGPUVRAMMB < 0 {
			return nil, fmt.Errorf("tierdef: %s: min_gpu_vram_mb must be >= 0, got %d", td.Tier, td.MinGPUVRAMMB)
		}
		if td.MinNetworkMbps < 0 {
			return nil, fmt.Errorf("tierdef: %s: min_network_mbps must be >= 0, got %d", td.Tier, td.MinNetworkMbps)
		}
	}

	// Build index.
	idx := make(map[string]int, expectedTierCount)
	for i, td := range raw.Tiers {
		idx[td.Tier] = i
	}

	return &Registry{
		SchemaVersion: raw.SchemaVersion,
		Tiers:         raw.Tiers,
		index:         idx,
	}, nil
}
