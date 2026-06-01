package deviceprofile_test

import (
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/deviceprofile"
)

// fixture-baked oracle values for T6 from the supported YAML document below.
// These are hardcoded constants, NOT derived from the implementation or writer,
// ensuring we assert sink-side facts rather than round-trip identity.
const (
	fixtureT6CPUCores         = 32
	fixtureT6MemoryMB         = 65536 // 64 GiB
	fixtureT6GPUCount         = 2
	fixtureT6PowerBudgetWatts = 350.0
	fixtureT6GPUVRAMmb        = 16384 // 16 GiB
	fixtureT6NetworkMbps      = 5000
	fixtureSchemaVersion      = 1
)

// fullYAML is a well-formed, schema_version: 1 document covering T1-T8.
// T6's capability/limit values are the fixture-baked oracle.
const fullYAML = `
schema_version: 1
profiles:
  T1:
    tier: T1
    cpu_cores: 1
    memory_mb: 512
    gpu_count: 0
    gpu_vram_mb: 0
    power_budget_watts: 5.0
    network_mbps: 100
  T2:
    tier: T2
    cpu_cores: 2
    memory_mb: 1024
    gpu_count: 0
    gpu_vram_mb: 0
    power_budget_watts: 10.0
    network_mbps: 250
  T3:
    tier: T3
    cpu_cores: 4
    memory_mb: 4096
    gpu_count: 0
    gpu_vram_mb: 0
    power_budget_watts: 25.0
    network_mbps: 500
  T4:
    tier: T4
    cpu_cores: 8
    memory_mb: 16384
    gpu_count: 1
    gpu_vram_mb: 2048
    power_budget_watts: 65.0
    network_mbps: 1000
  T5:
    tier: T5
    cpu_cores: 16
    memory_mb: 32768
    gpu_count: 1
    gpu_vram_mb: 8192
    power_budget_watts: 150.0
    network_mbps: 2500
  T6:
    tier: T6
    cpu_cores: 32
    memory_mb: 65536
    gpu_count: 2
    gpu_vram_mb: 16384
    power_budget_watts: 350.0
    network_mbps: 5000
  T7:
    tier: T7
    cpu_cores: 64
    memory_mb: 131072
    gpu_count: 4
    gpu_vram_mb: 32768
    power_budget_watts: 700.0
    network_mbps: 10000
  T8:
    tier: T8
    cpu_cores: 128
    memory_mb: 524288
    gpu_count: 8
    gpu_vram_mb: 81920
    power_budget_watts: 1500.0
    network_mbps: 25000
`

// TestLoadYAML_SupportedVersion_T6MatchesOracle verifies that a YAML document
// with schema_version: 1 loads successfully and Get("T6") returns the exact
// fixture-baked capability/limit fields. This is a SINK-SIDE assertion: the
// values must match our oracle constants, not the writer's output.
func TestLoadYAML_SupportedVersion_T6MatchesOracle(t *testing.T) {
	reg, err := deviceprofile.LoadYAML([]byte(fullYAML))
	if err != nil {
		t.Fatalf("LoadYAML returned unexpected error: %v", err)
	}
	if reg == nil {
		t.Fatal("LoadYAML returned nil registry")
	}

	// Verify schema version stored in the registry.
	if reg.SchemaVersion != fixtureSchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", reg.SchemaVersion, fixtureSchemaVersion)
	}

	// Verify T6 profile — sink-side oracle values.
	p, ok := reg.Get("T6")
	if !ok {
		t.Fatal("Get(\"T6\") returned not-found; want T6 profile")
	}

	if p.CPUCores != fixtureT6CPUCores {
		t.Errorf("T6.CPUCores: got %d, want %d", p.CPUCores, fixtureT6CPUCores)
	}
	if p.MemoryMB != fixtureT6MemoryMB {
		t.Errorf("T6.MemoryMB: got %d, want %d", p.MemoryMB, fixtureT6MemoryMB)
	}
	if p.GPUCount != fixtureT6GPUCount {
		t.Errorf("T6.GPUCount: got %d, want %d", p.GPUCount, fixtureT6GPUCount)
	}
	if p.GPUVRAMmb != fixtureT6GPUVRAMmb {
		t.Errorf("T6.GPUVRAMmb: got %d, want %d", p.GPUVRAMmb, fixtureT6GPUVRAMmb)
	}
	if p.PowerBudgetWatts != fixtureT6PowerBudgetWatts {
		t.Errorf("T6.PowerBudgetWatts: got %g, want %g", p.PowerBudgetWatts, fixtureT6PowerBudgetWatts)
	}
	if p.NetworkMbps != fixtureT6NetworkMbps {
		t.Errorf("T6.NetworkMbps: got %d, want %d", p.NetworkMbps, fixtureT6NetworkMbps)
	}
	if p.Tier != "T6" {
		t.Errorf("T6.Tier: got %q, want \"T6\"", p.Tier)
	}
}

// TestLoadYAML_UnsupportedSchemaVersion_IsRejected verifies that a document with
// schema_version: 999 is REJECTED with a clear error and no registry is returned.
func TestLoadYAML_UnsupportedSchemaVersion_IsRejected(t *testing.T) {
	badVersionYAML := `
schema_version: 999
profiles:
  T1:
    tier: T1
    cpu_cores: 1
    memory_mb: 512
    gpu_count: 0
    gpu_vram_mb: 0
    power_budget_watts: 5.0
    network_mbps: 100
`
	reg, err := deviceprofile.LoadYAML([]byte(badVersionYAML))
	if err == nil {
		t.Fatal("LoadYAML accepted schema_version 999; want rejection error")
	}
	if reg != nil {
		t.Errorf("LoadYAML returned non-nil registry on rejection; want nil")
	}
	// The error must mention the version in a meaningful way.
	errStr := err.Error()
	if !strings.Contains(errStr, "999") {
		t.Errorf("rejection error %q does not mention the bad version 999", errStr)
	}
}

// TestLoadYAML_MalformedDoc_IsRejected verifies that a document missing required
// fields (e.g., completely empty profiles map) is rejected with a clear error.
func TestLoadYAML_MalformedDoc_IsRejected(t *testing.T) {
	malformedYAML := `
schema_version: 1
profiles: {}
`
	reg, err := deviceprofile.LoadYAML([]byte(malformedYAML))
	if err == nil {
		t.Fatal("LoadYAML accepted empty profiles map; want rejection error for missing tiers")
	}
	if reg != nil {
		t.Errorf("LoadYAML returned non-nil registry on rejection; want nil")
	}
}

// TestLoadYAML_MissingSchemaVersion_IsRejected verifies that a document without
// a schema_version field is rejected.
func TestLoadYAML_MissingSchemaVersion_IsRejected(t *testing.T) {
	noVersionYAML := `
profiles:
  T1:
    tier: T1
    cpu_cores: 1
    memory_mb: 512
    gpu_count: 0
    gpu_vram_mb: 0
    power_budget_watts: 5.0
    network_mbps: 100
`
	reg, err := deviceprofile.LoadYAML([]byte(noVersionYAML))
	if err == nil {
		t.Fatal("LoadYAML accepted doc with missing schema_version; want rejection error")
	}
	if reg != nil {
		t.Errorf("LoadYAML returned non-nil registry on rejection; want nil")
	}
}

// TestLoadYAML_InvalidTierName_IsRejected verifies that a document containing a
// profile keyed to an invalid tier name (not T1-T8) is rejected.
func TestLoadYAML_InvalidTierName_IsRejected(t *testing.T) {
	badTierYAML := `
schema_version: 1
profiles:
  INVALID:
    tier: INVALID
    cpu_cores: 4
    memory_mb: 4096
    gpu_count: 0
    gpu_vram_mb: 0
    power_budget_watts: 25.0
    network_mbps: 500
`
	reg, err := deviceprofile.LoadYAML([]byte(badTierYAML))
	if err == nil {
		t.Fatal("LoadYAML accepted invalid tier name; want rejection error")
	}
	if reg != nil {
		t.Errorf("LoadYAML returned non-nil registry on rejection; want nil")
	}
}

// TestRegistry_GetMiss verifies that Get on a missing tier returns false.
func TestRegistry_GetMiss(t *testing.T) {
	reg, err := deviceprofile.LoadYAML([]byte(fullYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, ok := reg.Get("T9")
	if ok {
		t.Error("Get(\"T9\") returned ok=true; want false for non-existent tier")
	}
}

// TestRegistry_ListAll verifies that List returns exactly 8 profiles (T1-T8).
func TestRegistry_ListAll(t *testing.T) {
	reg, err := deviceprofile.LoadYAML([]byte(fullYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	profiles := reg.List()
	if len(profiles) != 8 {
		t.Errorf("List() returned %d profiles; want 8", len(profiles))
	}
	// Verify the order is T1..T8.
	want := []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8"}
	for i, p := range profiles {
		if p.Tier != want[i] {
			t.Errorf("List()[%d].Tier = %q; want %q", i, p.Tier, want[i])
		}
	}
}

// TestSaveYAML_RoundTrip verifies that SaveYAML produces a YAML document that,
// when loaded back, reproduces the same T6 oracle values. This is an integration
// check of Save+Load consistency (not a round-trip identity claim — values are
// still asserted against oracle constants).
func TestSaveYAML_RoundTrip(t *testing.T) {
	reg, err := deviceprofile.LoadYAML([]byte(fullYAML))
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}

	saved, err := reg.SaveYAML()
	if err != nil {
		t.Fatalf("SaveYAML: %v", err)
	}

	reg2, err := deviceprofile.LoadYAML(saved)
	if err != nil {
		t.Fatalf("LoadYAML after SaveYAML: %v", err)
	}

	p, ok := reg2.Get("T6")
	if !ok {
		t.Fatal("T6 not found after round-trip")
	}
	if p.CPUCores != fixtureT6CPUCores {
		t.Errorf("round-trip T6.CPUCores: got %d, want %d", p.CPUCores, fixtureT6CPUCores)
	}
	if p.MemoryMB != fixtureT6MemoryMB {
		t.Errorf("round-trip T6.MemoryMB: got %d, want %d", p.MemoryMB, fixtureT6MemoryMB)
	}
}
