package console

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// TestRegistrar_ReadyGateBlocksRegistration is the sink-side integration test
// the reviewer required: it proves the boot-readiness gate is actually wired
// into the production registration path. A real *BootCoordinator over a
// not-yet-cluster-ready marker-set is attached via WithReadyGate; RegisterCtx
// must NOT publish the node to discovery while the node is below ClusterReady,
// and must succeed (and publish) once the cluster-ready marker appears.
//
// Mutation that kills it: drop the gate.WaitReady call in RegisterCtx -> the
// node is published immediately and the "still blocked + not yet in discovery"
// assertion fails.
func TestRegistrar_ReadyGateBlocksRegistration(t *testing.T) {
	dir := t.TempDir()
	vp := filepath.Join(dir, "version")
	cl := filepath.Join(dir, "cmdline")
	lg := filepath.Join(dir, "boot.log")

	// Kernel up, userspace ready, but NOT cluster-ready yet.
	if err := os.WriteFile(vp, []byte("Linux version 6.6.0"), 0o644); err != nil {
		t.Fatalf("write version: %v", err)
	}
	if err := os.WriteFile(cl, []byte("ro quiet helix.userspace=ready"), 0o644); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}

	bc := NewBootCoordinator(BootMarkers{VersionPath: vp, CmdlinePath: cl, BootLogPath: lg})
	bc.PollInterval = 5 * time.Millisecond

	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry).WithReadyGate(bc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		rec *NodeRecord
		err error
	}
	done := make(chan result, 1)
	go func() {
		rec, err := r.RegisterCtx(ctx, "gated-node", TypePS5, "us-east-1", nil, nil)
		done <- result{rec, err}
	}()

	// While the node is not ClusterReady, RegisterCtx must stay blocked AND the
	// node must not be present in discovery.
	select {
	case res := <-done:
		t.Fatalf("RegisterCtx returned early (err=%v) before ClusterReady", res.err)
	case <-time.After(80 * time.Millisecond):
	}
	if insts, err := registry.Lookup(context.Background(), "helix-console-node"); err != nil {
		t.Fatalf("discover: %v", err)
	} else if len(insts) != 0 {
		t.Fatalf("node was published to discovery before ClusterReady: %+v", insts)
	}

	// Flip the cluster-ready marker; registration must now complete and publish.
	if err := os.WriteFile(cl, []byte("ro quiet helix.userspace=ready helix.cluster=ready"), 0o644); err != nil {
		t.Fatalf("flip cmdline: %v", err)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("RegisterCtx after ClusterReady: %v", res.err)
		}
		if res.rec == nil || res.rec.ID != "gated-node" {
			t.Fatalf("unexpected record: %+v", res.rec)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("RegisterCtx did not complete after ClusterReady marker appeared")
	}

	insts, err := registry.Lookup(context.Background(), "helix-console-node")
	if err != nil {
		t.Fatalf("discover after ready: %v", err)
	}
	if len(insts) != 1 || insts[0].ID != "gated-node" {
		t.Fatalf("node not published to discovery after ClusterReady; got %+v", insts)
	}
}

// TestRegistrar_ReadyGateRefusesOnContextCancel proves a stuck (never
// ClusterReady) node does not register: RegisterCtx returns the gate error and
// publishes nothing once the context is cancelled.
//
// Mutation that kills it: ignore the gate error in RegisterCtx -> the node
// registers despite never being ready and this fails.
func TestRegistrar_ReadyGateRefusesOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	vp := filepath.Join(dir, "version")
	cl := filepath.Join(dir, "cmdline")
	lg := filepath.Join(dir, "boot.log")
	if err := os.WriteFile(vp, []byte("Linux version 6.6.0"), 0o644); err != nil {
		t.Fatalf("write version: %v", err)
	}
	if err := os.WriteFile(cl, []byte("ro quiet helix.userspace=ready"), 0o644); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}

	bc := NewBootCoordinator(BootMarkers{VersionPath: vp, CmdlinePath: cl, BootLogPath: lg})
	bc.PollInterval = 5 * time.Millisecond

	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry).WithReadyGate(bc)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	rec, err := r.RegisterCtx(ctx, "stuck-node", TypePS5, "us-east-1", nil, nil)
	if err == nil {
		t.Fatalf("RegisterCtx returned nil error for a node that never became ClusterReady")
	}
	if rec != nil {
		t.Fatalf("RegisterCtx returned a record %+v for an un-ready node", rec)
	}
	if insts, derr := registry.Lookup(context.Background(), "helix-console-node"); derr != nil {
		t.Fatalf("discover: %v", derr)
	} else if len(insts) != 0 {
		t.Fatalf("un-ready node was published to discovery: %+v", insts)
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
