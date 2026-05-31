package console

import (
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/resources"
)

func TestNewRegistrar(t *testing.T) {
	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry)
	if r == nil {
		t.Fatal("expected non-nil registrar")
	}
}

func TestRegistrar_Register(t *testing.T) {
	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry)

	cap := &resources.NodeResources{CPU: resources.CPUInfo{Cores: 8}, Memory: resources.MemoryInfo{TotalKB: 16384 * 1024}}
	record, err := r.Register("console-1", TypePS5, "us-east-1", map[string]string{"rack": "A1"}, cap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record.ID != "console-1" {
		t.Errorf("expected ID console-1, got %s", record.ID)
	}
	if record.Trust != TrustSemi {
		t.Errorf("expected default trust SEMI, got %s", record.Trust)
	}
	if record.Type != TypePS5 {
		t.Errorf("expected TypePS5, got %s", record.Type)
	}
	if record.Capacity.CPU.Cores != 8 {
		t.Errorf("expected CPU cores 8, got %d", record.Capacity.CPU.Cores)
	}
}

func TestRegistrar_Register_MissingID(t *testing.T) {
	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry)

	_, err := r.Register("", TypeUnknown, "", nil, nil)
	if err == nil {
		t.Error("expected error for missing ID")
	}
}

func TestRegistrar_Register_NilRegistry(t *testing.T) {
	r := NewRegistrar(nil)
	record, err := r.Register("console-2", TypePS4, "eu-west-1", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record.ID != "console-2" {
		t.Errorf("expected ID console-2, got %s", record.ID)
	}
}

func TestRegistrar_SetTrust(t *testing.T) {
	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry)

	record, _ := r.Register("console-3", TypePS5, "ap-south-1", nil, nil)
	if err := r.SetTrust(record, TrustFull); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record.Trust != TrustFull {
		t.Errorf("expected trust FULL, got %s", record.Trust)
	}
}

func TestRegistrar_SetTrust_NilRecord(t *testing.T) {
	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry)

	if err := r.SetTrust(nil, TrustFull); err == nil {
		t.Error("expected error for nil record")
	}
}

func TestRegistrar_SetTrust_Invalid(t *testing.T) {
	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry)

	record, _ := r.Register("console-4", TypePS5, "", nil, nil)
	if err := r.SetTrust(record, TrustLevel("INVALID")); err == nil {
		t.Error("expected error for invalid trust level")
	}
}
