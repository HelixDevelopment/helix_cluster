package console

import (
	"context"
	"errors"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/resources"
)

// failingBackend implements discovery.Backend and rejects Put, letting us
// drive the registry-error path of Registrar.Register with a real (not mock)
// registry wired to a deterministic failing store.
type failingBackend struct{}

var errPutBoom = errors.New("backend put failed")

func (failingBackend) Put(context.Context, string, *discovery.Instance) error { return errPutBoom }
func (failingBackend) Delete(context.Context, string) error                   { return nil }
func (failingBackend) List(context.Context, string) ([]*discovery.Instance, error) {
	return nil, nil
}
func (failingBackend) Watch(context.Context, string) (<-chan discovery.BackendEvent, error) {
	return nil, nil
}

// Sink-side proof: Register must not merely return a NodeRecord, it must
// actually publish a discoverable instance carrying the merged console
// metadata. We assert by looking the instance back up through the registry.

func TestRegister_PublishesDiscoverableInstance(t *testing.T) {
	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry)

	_, err := r.Register("console-sink", TypePS5, "us-west-2",
		map[string]string{"rack": "B7"},
		&resources.NodeResources{CPU: resources.CPUInfo{Cores: 8}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	insts, err := registry.Lookup(context.Background(), "helix-console-node")
	if err != nil {
		t.Fatalf("lookup error: %v", err)
	}
	// Mutation: delete the `r.registry.Register(...)` call in Register ->
	// nothing is published and this length check fails.
	if len(insts) != 1 {
		t.Fatalf("expected 1 published instance, got %d", len(insts))
	}
	got := insts[0]
	if got.ID != "console-sink" {
		t.Errorf("expected ID console-sink, got %s", got.ID)
	}
	// Mutation: drop out["console.type"] assignment in mergeLabels -> fails.
	if got.Metadata["console.type"] != "ps5" {
		t.Errorf("expected console.type=ps5 in published metadata, got %q", got.Metadata["console.type"])
	}
	// Mutation: drop out["console.trust"] assignment -> fails. Default trust
	// is SEMI and must survive into the registry.
	if got.Metadata["console.trust"] != "SEMI" {
		t.Errorf("expected console.trust=SEMI, got %q", got.Metadata["console.trust"])
	}
	// Mutation: skip copying base labels in mergeLabels -> "rack" missing.
	if got.Metadata["rack"] != "B7" {
		t.Errorf("expected base label rack=B7 to be merged, got %q", got.Metadata["rack"])
	}
}

func TestRegister_PropagatesRegistryError(t *testing.T) {
	registry := discovery.NewServiceRegistry(failingBackend{})
	r := NewRegistrar(registry)

	rec, err := r.Register("boom-id", TypePS4, "r", nil, nil)
	// Mutation: change `return nil, fmt.Errorf("discovery register: %w", err)`
	// to `return record, nil` (swallow) -> this error expectation fails.
	if err == nil {
		t.Fatal("expected error to propagate from failing registry backend")
	}
	if rec != nil {
		t.Errorf("expected nil record on registry failure, got %+v", rec)
	}
	if !errors.Is(err, errPutBoom) {
		t.Errorf("expected wrapped backend error, got %v", err)
	}
}

func TestMergeLabels_NilBase(t *testing.T) {
	out := mergeLabels(nil, TypePS4Pro, TrustUntrusted)
	if out["console.type"] != "ps4pro" {
		t.Errorf("expected ps4pro, got %q", out["console.type"])
	}
	if out["console.trust"] != "UNTRUSTED" {
		t.Errorf("expected UNTRUSTED, got %q", out["console.trust"])
	}
	// Mutation: change make(...,len(base)+2) handling so nil base panics ->
	// this test would panic/fail. Confirms nil-safe construction.
	if len(out) != 2 {
		t.Errorf("expected exactly 2 keys for nil base, got %d", len(out))
	}
}

func TestMergeLabels_DoesNotMutateBase(t *testing.T) {
	base := map[string]string{"zone": "z1"}
	out := mergeLabels(base, TypePS5, TrustFull)
	// Mutation: write directly into `base` instead of a fresh `out` map ->
	// base would gain console.* keys and this fails.
	if _, ok := base["console.type"]; ok {
		t.Error("mergeLabels must not mutate the caller's base map")
	}
	if out["zone"] != "z1" {
		t.Errorf("expected zone preserved, got %q", out["zone"])
	}
}
