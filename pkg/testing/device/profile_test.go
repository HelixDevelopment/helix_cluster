package device

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewRegistry(t *testing.T) {
	reg := NewRegistry()
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}
	if len(reg.List()) != 8 {
		t.Errorf("expected 8 default profiles, got %d", len(reg.List()))
	}
}

func TestRegistry_Get(t *testing.T) {
	reg := NewRegistry()
	p := reg.Get("T1")
	if p == nil {
		t.Fatal("expected T1 profile")
	}
	if p.Tier != "T1" {
		t.Errorf("expected tier T1, got %s", p.Tier)
	}
	if p.CPU.Cores != 1 {
		t.Errorf("expected 1 core, got %d", p.CPU.Cores)
	}
	if reg.Get("T99") != nil {
		t.Error("expected nil for unknown tier")
	}
}

func TestRegistry_List(t *testing.T) {
	reg := NewRegistry()
	list := reg.List()
	if len(list) != 8 {
		t.Fatalf("expected 8 profiles, got %d", len(list))
	}
	// Verify natural ordering T1..T8.
	for i, p := range list {
		want := "T" + string(rune('1'+i))
		if p.Tier != want {
			t.Errorf("position %d: expected %s, got %s", i, want, p.Tier)
		}
	}
}

func TestRegistry_RegisterOverwrite(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&Profile{ID: "custom", Tier: "T1", CPU: CPU{Cores: 99}})
	p := reg.Get("T1")
	if p.CPU.Cores != 99 {
		t.Errorf("expected overwritten cores 99, got %d", p.CPU.Cores)
	}
}

func TestRegistry_SaveAndLoadYAML(t *testing.T) {
	reg := NewRegistry()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "profiles.yaml")

	if err := reg.SaveYAML(path); err != nil {
		t.Fatalf("unexpected error saving: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty YAML file")
	}

	reg2 := NewRegistry()
	if err := reg2.LoadYAML(path); err != nil {
		t.Fatalf("unexpected error loading: %v", err)
	}

	// After load, the defaults are overwritten by the same defaults.
	if len(reg2.List()) != 8 {
		t.Errorf("expected 8 profiles after load, got %d", len(reg2.List()))
	}
	p := reg2.Get("T8")
	if p == nil {
		t.Fatal("expected T8 profile after load")
	}
	if p.CPU.Cores != 128 {
		t.Errorf("expected 128 cores for T8, got %d", p.CPU.Cores)
	}
}

func TestRegistry_LoadYAML_NotFound(t *testing.T) {
	reg := NewRegistry()
	if err := reg.LoadYAML("/nonexistent/path/profiles.yaml"); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestProfile_Struct(t *testing.T) {
	p := &Profile{
		ID:   "test", Tier: "TX",
		CPU:     CPU{Cores: 4, FrequencyGHz: 2.5, Architecture: "arm64"},
		RAM:     RAM{TotalGB: 8, Type: "DDR4"},
		GPU:     GPU{Count: 1, Model: "test-gpu", MemoryGB: 4, Vulkan: true},
		Network: Network{BandwidthMbps: 1000, LatencyMs: 5, JitterMs: 1},
	}
	if p.GPU.Vulkan != true {
		t.Error("expected Vulkan true")
	}
	if p.Network.BandwidthMbps != 1000 {
		t.Errorf("expected bandwidth 1000, got %d", p.Network.BandwidthMbps)
	}
}
