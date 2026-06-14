package deviceprofile_test

import (
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/deviceprofile"
)

// This file is an adversarial audit of LoadYAML's parse + validation contract.
// It complements registry_test.go (which already covers the happy path, the
// unsupported-version, missing-version, empty-profiles, and invalid-tier paths)
// by attacking the parser itself: syntactically broken YAML, wrong-shaped
// documents (scalar/list), type mismatches, duplicate keys, and the silent
// edges (no value-range validation; unknown fields ignored).
//
// CONTRACT (as implemented, verified empirically against gopkg.in/yaml.v3):
//   - LoadYAML returns (nil, error) on ANY yaml.Unmarshal failure — the parse
//     error is wrapped, never swallowed into a zero profile.
//   - schema_version must equal SupportedSchemaVersion (1); 0/absent and any
//     other value are rejected. nil/empty input therefore rejects via version 0.
//   - profiles map must be non-empty and every key must be a valid tier T1..T8.
//   - The decoder is LAX: unknown fields are silently ignored (no KnownFields).
//   - There is NO value-range validation: negative/out-of-range numeric fields
//     are accepted as-is. Pinned below so any future tightening is a deliberate,
//     test-breaking decision rather than an accident.

// ---------------------------------------------------------------------------
// (a) VALID BASELINE — a minimal well-formed single-tier document loads and
// every field is exactly the hand-verified value. Distinct from registry_test's
// T6 oracle: this uses a tiny T4 doc so the baseline stands on its own.
// ---------------------------------------------------------------------------

const validT4YAML = `
schema_version: 1
profiles:
  T4:
    tier: T4
    cpu_cores: 8
    memory_mb: 16384
    gpu_count: 1
    gpu_vram_mb: 2048
    power_budget_watts: 65.5
    network_mbps: 1000
`

func TestAdversarial_ValidBaseline_AllFieldsExact(t *testing.T) {
	reg, err := deviceprofile.LoadYAML([]byte(validT4YAML))
	if err != nil {
		t.Fatalf("valid baseline rejected: %v", err)
	}
	if reg == nil {
		t.Fatal("valid baseline returned nil registry")
	}
	if reg.SchemaVersion != deviceprofile.SupportedSchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", reg.SchemaVersion, deviceprofile.SupportedSchemaVersion)
	}
	p, ok := reg.Get("T4")
	if !ok {
		t.Fatal("Get(\"T4\") not found in valid baseline")
	}
	// Hand-verified oracle values (independent of the writer).
	if p.Tier != "T4" {
		t.Errorf("Tier: got %q, want \"T4\"", p.Tier)
	}
	if p.CPUCores != 8 {
		t.Errorf("CPUCores: got %d, want 8", p.CPUCores)
	}
	if p.MemoryMB != 16384 {
		t.Errorf("MemoryMB: got %d, want 16384", p.MemoryMB)
	}
	if p.GPUCount != 1 {
		t.Errorf("GPUCount: got %d, want 1", p.GPUCount)
	}
	if p.GPUVRAMmb != 2048 {
		t.Errorf("GPUVRAMmb: got %d, want 2048", p.GPUVRAMmb)
	}
	if p.PowerBudgetWatts != 65.5 {
		t.Errorf("PowerBudgetWatts: got %g, want 65.5", p.PowerBudgetWatts)
	}
	if p.NetworkMbps != 1000 {
		t.Errorf("NetworkMbps: got %d, want 1000", p.NetworkMbps)
	}
}

// ---------------------------------------------------------------------------
// (b) MALFORMED YAML — the centerpiece. Syntactically broken input MUST return
// an error, NOT a zero/partial Registry. This is the exact bug class the audit
// targets: a swallowed parse error masquerading as an empty profile.
// ---------------------------------------------------------------------------

func TestAdversarial_MalformedYAML_RejectedNotSwallowed(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "unterminated_flow_sequence",
			yaml: "schema_version: 1\nprofiles:\n  T1: [unterminated",
		},
		{
			name: "tab_indentation", // YAML forbids tabs for indentation
			yaml: "schema_version: 1\nprofiles:\n\tT1: {}",
		},
		{
			name: "bad_block_structure",
			yaml: "schema_version: 1\nprofiles:\n  T1:\n  cpu_cores 8\n: : :",
		},
		{
			name: "unclosed_quote",
			yaml: "schema_version: 1\nprofiles:\n  T1:\n    tier: \"T1\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, err := deviceprofile.LoadYAML([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("malformed YAML accepted (parse error swallowed); reg=%+v", reg)
			}
			if reg != nil {
				t.Errorf("malformed YAML returned non-nil registry %+v; want nil", reg)
			}
			// Must be surfaced as a parse error, not a downstream validation msg.
			if !strings.Contains(err.Error(), "YAML parse error") {
				t.Errorf("error %q is not the wrapped parse error; the failure may be masked", err.Error())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// (c) TYPE MISMATCH — a string where an int is expected MUST be rejected by the
// unmarshaler, not silently coerced to zero. (yaml.v3 does NOT coerce "8" → 8.)
// ---------------------------------------------------------------------------

func TestAdversarial_TypeMismatch_StringForInt_Rejected(t *testing.T) {
	cases := map[string]string{
		"non_numeric_string": "schema_version: 1\nprofiles:\n  T1:\n    tier: T1\n    cpu_cores: \"not a number\"\n",
		"quoted_numeric":     "schema_version: 1\nprofiles:\n  T1:\n    tier: T1\n    cpu_cores: \"8\"\n",
		"version_as_string":  "schema_version: \"1\"\nprofiles:\n  T1:\n    tier: T1\n    cpu_cores: 8\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			reg, err := deviceprofile.LoadYAML([]byte(doc))
			if err == nil {
				t.Fatalf("type-mismatched YAML accepted; reg=%+v", reg)
			}
			if reg != nil {
				t.Errorf("type mismatch returned non-nil registry; want nil")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// (d) DUPLICATE MAPPING KEY — yaml.v3 rejects duplicate keys; pin that it is
// surfaced as an error, not silently last-wins-merged.
// ---------------------------------------------------------------------------

func TestAdversarial_DuplicateKey_Rejected(t *testing.T) {
	doc := "schema_version: 1\nschema_version: 1\nprofiles:\n  T1:\n    tier: T1\n    cpu_cores: 8\n"
	reg, err := deviceprofile.LoadYAML([]byte(doc))
	if err == nil {
		t.Fatalf("duplicate key accepted; reg=%+v", reg)
	}
	if reg != nil {
		t.Errorf("duplicate key returned non-nil registry; want nil")
	}
	if !strings.Contains(err.Error(), "already defined") {
		t.Errorf("error %q does not indicate a duplicate-key rejection", err.Error())
	}
}

// ---------------------------------------------------------------------------
// (e) WRONG-SHAPED DOCUMENTS — a scalar or a list where a mapping is expected
// MUST reject cleanly (no panic). Guards against a top-level type confusion.
// ---------------------------------------------------------------------------

func TestAdversarial_WrongShape_ScalarOrList_RejectedNoPanic(t *testing.T) {
	cases := map[string]string{
		"scalar":          "just a string",
		"top_level_list":  "- a\n- b\n",
		"profiles_scalar": "schema_version: 1\nprofiles: not-a-map\n",
		"profiles_list":   "schema_version: 1\nprofiles:\n  - T1\n  - T2\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			reg, err := deviceprofile.LoadYAML([]byte(doc))
			if err == nil {
				t.Fatalf("wrong-shaped document accepted; reg=%+v", reg)
			}
			if reg != nil {
				t.Errorf("wrong-shaped document returned non-nil registry; want nil")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EDGES: empty input and nil bytes — no panic; rejected (via schema_version 0,
// since an empty doc unmarshals into a zero rawDoc). Pins the exact behavior.
// ---------------------------------------------------------------------------

func TestAdversarial_EmptyAndNilInput_RejectedNoPanic(t *testing.T) {
	for _, in := range [][]byte{nil, {}, []byte("   \n  \n")} {
		reg, err := deviceprofile.LoadYAML(in)
		if err == nil {
			t.Fatalf("empty/nil input accepted; reg=%+v", reg)
		}
		if reg != nil {
			t.Errorf("empty/nil input returned non-nil registry; want nil")
		}
		// Empty input has no profiles and version 0 → rejected on version first.
		if !strings.Contains(err.Error(), "schema_version") {
			t.Errorf("empty input error %q should cite schema_version", err.Error())
		}
	}
}

// ---------------------------------------------------------------------------
// CONTRACT PIN (lax): UNKNOWN fields are SILENTLY IGNORED. The decoder does not
// use KnownFields(true). If a future change enables strict decoding, THIS test
// must be updated deliberately — it documents the current accept-and-drop rule.
// ---------------------------------------------------------------------------

func TestAdversarial_UnknownFields_SilentlyIgnored_ContractPin(t *testing.T) {
	doc := `
schema_version: 1
totally_unknown_top_level: 42
profiles:
  T1:
    tier: T1
    cpu_cores: 8
    memory_mb: 1024
    gpu_count: 0
    gpu_vram_mb: 0
    power_budget_watts: 10.0
    network_mbps: 100
    bogus_field: 999
`
	reg, err := deviceprofile.LoadYAML([]byte(doc))
	if err != nil {
		t.Fatalf("CONTRACT CHANGE: unknown fields now rejected (%v). If strict "+
			"decoding was intentionally enabled, update this pin.", err)
	}
	if reg == nil {
		t.Fatal("unknown-field doc returned nil registry")
	}
	p, ok := reg.Get("T1")
	if !ok {
		t.Fatal("T1 not found despite valid (lax) document")
	}
	// Known fields still bind correctly; unknowns are dropped.
	if p.CPUCores != 8 || p.MemoryMB != 1024 {
		t.Errorf("known fields mis-bound under lax decode: cpu=%d mem=%d", p.CPUCores, p.MemoryMB)
	}
}

// ---------------------------------------------------------------------------
// CONTRACT PIN (no value validation): NEGATIVE / out-of-range numeric values
// are ACCEPTED AS-IS. There is no range check in LoadYAML. This is a GAP worth
// surfacing: a profile with cpu_cores: -5 loads successfully. If value
// validation is ever added, this pin will break and force a conscious update.
// ---------------------------------------------------------------------------

func TestAdversarial_NegativeValues_AcceptedAsIs_ContractPin(t *testing.T) {
	doc := `
schema_version: 1
profiles:
  T1:
    tier: T1
    cpu_cores: -5
    memory_mb: -100
    gpu_count: -1
    gpu_vram_mb: -2048
    power_budget_watts: -10.0
    network_mbps: -1
`
	reg, err := deviceprofile.LoadYAML([]byte(doc))
	if err != nil {
		t.Fatalf("CONTRACT CHANGE: negative values now rejected (%v). If value "+
			"validation was intentionally added, update this pin and add explicit "+
			"range-rejection tests.", err)
	}
	if reg == nil {
		t.Fatal("negative-value doc returned nil registry")
	}
	p, ok := reg.Get("T1")
	if !ok {
		t.Fatal("T1 not found")
	}
	// Pin the exact (unvalidated) values that flow straight through.
	if p.CPUCores != -5 {
		t.Errorf("CPUCores: got %d, want -5 (no validation)", p.CPUCores)
	}
	if p.MemoryMB != -100 {
		t.Errorf("MemoryMB: got %d, want -100 (no validation)", p.MemoryMB)
	}
	if p.PowerBudgetWatts != -10.0 {
		t.Errorf("PowerBudgetWatts: got %g, want -10.0 (no validation)", p.PowerBudgetWatts)
	}
}

// ---------------------------------------------------------------------------
// MISSING PER-PROFILE FIELDS — the package validates the document envelope
// (version, non-empty profiles, valid tier keys) but does NOT require any
// per-profile field. An omitted field defaults to the Go zero value and the
// load SUCCEEDS. Pinned so the "no required-field enforcement" contract is
// explicit and any future requirement is a deliberate change.
// ---------------------------------------------------------------------------

func TestAdversarial_MissingProfileFields_ZeroValued_ContractPin(t *testing.T) {
	// Only the tier key is present; every capability field is omitted.
	doc := `
schema_version: 1
profiles:
  T2: {}
`
	reg, err := deviceprofile.LoadYAML([]byte(doc))
	if err != nil {
		t.Fatalf("CONTRACT CHANGE: a profile with no fields is now rejected (%v). "+
			"If per-field required validation was added, update this pin.", err)
	}
	p, ok := reg.Get("T2")
	if !ok {
		t.Fatal("T2 not found")
	}
	// Everything defaults to zero; note even Tier is empty (the map KEY is "T2",
	// but the inner `tier:` field was never set).
	if p.Tier != "" || p.CPUCores != 0 || p.MemoryMB != 0 || p.GPUCount != 0 ||
		p.GPUVRAMmb != 0 || p.PowerBudgetWatts != 0 || p.NetworkMbps != 0 {
		t.Errorf("expected all-zero profile for empty T2, got %+v", p)
	}
}
