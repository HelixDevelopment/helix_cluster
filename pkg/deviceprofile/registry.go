// Package deviceprofile provides a versioned-YAML device profile registry for
// T1-T8 tiers. Unlike pkg/testing/device (which provides simulation profiles
// without schema versioning), this package is a FIRST-CLASS capability/limits
// registry with schema version validation and strict rejection of mismatched or
// malformed documents.
package deviceprofile

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// SupportedSchemaVersion is the only schema version this package understands.
// A document with any other schema_version is rejected.
const SupportedSchemaVersion = 1

// validTiers is the set of recognised tier keys (T1..T8).
var validTiers = map[string]bool{
	"T1": true, "T2": true, "T3": true, "T4": true,
	"T5": true, "T6": true, "T7": true, "T8": true,
}

// Profile holds the capability and limit fields for a single device tier.
// Fields are deliberately flat (not nested) to keep the YAML schema simple and
// easy to validate without reflection.
type Profile struct {
	Tier             string  `yaml:"tier"`
	CPUCores         int     `yaml:"cpu_cores"`
	MemoryMB         int     `yaml:"memory_mb"`
	GPUCount         int     `yaml:"gpu_count"`
	GPUVRAMmb        int     `yaml:"gpu_vram_mb"`
	PowerBudgetWatts float64 `yaml:"power_budget_watts"`
	NetworkMbps      int     `yaml:"network_mbps"`
}

// Registry is a versioned collection of device profiles keyed by tier string.
// It is created exclusively via LoadYAML — there is no public constructor.
type Registry struct {
	SchemaVersion int
	profiles      map[string]Profile
}

// rawDoc is the internal intermediate structure used during YAML unmarshalling,
// before validation. It mirrors the on-disk format exactly.
type rawDoc struct {
	SchemaVersion int                `yaml:"schema_version"`
	Profiles      map[string]Profile `yaml:"profiles"`
}

// LoadYAML parses and validates a YAML document, returning a Registry on
// success. It rejects the document when:
//   - schema_version is absent (zero) or not equal to SupportedSchemaVersion;
//   - the profiles map is empty (at least one tier must be present);
//   - any profile key is not a valid tier name (T1..T8).
//
// On any validation failure, a descriptive error is returned and the returned
// *Registry is nil.
func LoadYAML(data []byte) (*Registry, error) {
	var raw rawDoc
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("deviceprofile: YAML parse error: %w", err)
	}

	// Validate schema version: must be exactly SupportedSchemaVersion.
	if raw.SchemaVersion != SupportedSchemaVersion {
		return nil, fmt.Errorf(
			"deviceprofile: unsupported schema_version %d; this build only accepts version %d",
			raw.SchemaVersion, SupportedSchemaVersion,
		)
	}

	// At least one profile must be present.
	if len(raw.Profiles) == 0 {
		return nil, fmt.Errorf("deviceprofile: document contains no profiles; at least one T1-T8 entry is required")
	}

	// Validate each profile key is a recognised tier.
	for key := range raw.Profiles {
		if !validTiers[key] {
			return nil, fmt.Errorf(
				"deviceprofile: profile key %q is not a valid tier; valid tiers are T1-T8",
				key,
			)
		}
	}

	return &Registry{
		SchemaVersion: raw.SchemaVersion,
		profiles:      raw.Profiles,
	}, nil
}

// SaveYAML serialises the registry back to a YAML document, including the
// schema_version header. The returned bytes can be passed to LoadYAML.
func (r *Registry) SaveYAML() ([]byte, error) {
	doc := rawDoc{
		SchemaVersion: r.SchemaVersion,
		Profiles:      r.profiles,
	}
	data, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("deviceprofile: YAML marshal error: %w", err)
	}
	return data, nil
}

// Get returns the Profile for the given tier key (e.g. "T6") and whether it was
// found. The tier key must be in the form "T1".."T8".
func (r *Registry) Get(tier string) (Profile, bool) {
	p, ok := r.profiles[tier]
	return p, ok
}

// List returns all profiles sorted by tier in ascending order (T1 first, T8 last).
func (r *Registry) List() []Profile {
	keys := make([]string, 0, len(r.profiles))
	for k := range r.profiles {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]Profile, 0, len(keys))
	for _, k := range keys {
		out = append(out, r.profiles[k])
	}
	return out
}
