package stonith

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeEC2 is a real in-process EC2 power state machine (NOT a call-count mock):
// instances start "running" and StopInstance genuinely flips them to "stopped",
// so a test can assert sink-side state.
type fakeEC2 struct {
	mu      sync.Mutex
	states  map[string]string
	stopErr error
}

func newFakeEC2() *fakeEC2 { return &fakeEC2{states: map[string]string{}} }

func (f *fakeEC2) StopInstance(_ context.Context, id string) error {
	if f.stopErr != nil {
		return f.stopErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[id] = "stopped"
	return nil
}

func (f *fakeEC2) InstanceState(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.states[id]; ok {
		return s, nil
	}
	return "running", nil
}

func TestEC2AgentFencesRealState(t *testing.T) {
	ctx := context.Background()
	cloud := newFakeEC2()
	agent := NewEC2Agent("ec2-1", cloud)
	const id = "i-0abc"

	if fenced, _ := agent.IsFenced(ctx, id); fenced {
		t.Fatal("pre-condition: instance should be running")
	}
	conf, err := agent.Fence(ctx, id)
	if err != nil {
		t.Fatalf("Fence: %v", err)
	}
	if conf.Method != "ec2 stop" {
		t.Errorf("method=%q", conf.Method)
	}
	// Sink-side oracle: read the cloud state machine directly.
	if st, _ := cloud.InstanceState(ctx, id); st != "stopped" {
		t.Fatalf("instance state=%q, want stopped", st)
	}
	if fenced, err := agent.IsFenced(ctx, id); err != nil || !fenced {
		t.Fatalf("IsFenced: got (%v,%v), want true", fenced, err)
	}
}

func TestEC2AgentStopError(t *testing.T) {
	cloud := newFakeEC2()
	cloud.stopErr = errors.New("throttled")
	agent := NewEC2Agent("ec2-1", cloud)
	_, err := agent.Fence(context.Background(), "i-x")
	if err == nil || !errors.Is(err, cloud.stopErr) {
		t.Fatalf("got %v, want wrapped throttled error", err)
	}
}

// fakeAzure is a real in-process Azure VM power state machine.
type fakeAzure struct {
	mu     sync.Mutex
	states map[string]string
	offErr error
}

func newFakeAzure() *fakeAzure { return &fakeAzure{states: map[string]string{}} }

func key(rg, vm string) string { return rg + "/" + vm }

func (f *fakeAzure) PowerOffVM(_ context.Context, rg, vm string) error {
	if f.offErr != nil {
		return f.offErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[key(rg, vm)] = "deallocated"
	return nil
}

func (f *fakeAzure) VMPowerState(_ context.Context, rg, vm string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.states[key(rg, vm)]; ok {
		return s, nil
	}
	return "running", nil
}

func TestAzureAgentFencesRealState(t *testing.T) {
	ctx := context.Background()
	cloud := newFakeAzure()
	agent := NewAzureAgent("az-1", "rg-prod", cloud)
	const vm = "vm-7"

	conf, err := agent.Fence(ctx, vm)
	if err != nil {
		t.Fatalf("Fence: %v", err)
	}
	if conf.Method != "azure power off" {
		t.Errorf("method=%q", conf.Method)
	}
	if st, _ := cloud.VMPowerState(ctx, "rg-prod", vm); st != "deallocated" {
		t.Fatalf("vm state=%q, want deallocated", st)
	}
	if fenced, err := agent.IsFenced(ctx, vm); err != nil || !fenced {
		t.Fatalf("IsFenced: got (%v,%v), want true", fenced, err)
	}
}

func TestAzureAgentPowerOffError(t *testing.T) {
	cloud := newFakeAzure()
	cloud.offErr = errors.New("subscription disabled")
	agent := NewAzureAgent("az-1", "rg", cloud)
	_, err := agent.Fence(context.Background(), "vm-x")
	if err == nil || !errors.Is(err, cloud.offErr) {
		t.Fatalf("got %v, want wrapped error", err)
	}
}

// TestHeterogeneousFenceChain proves IPMI->EC2->SBD style heterogeneous
// fallback: a failing EC2 primary falls through to a succeeding Azure secondary.
func TestHeterogeneousFenceChain(t *testing.T) {
	ctx := context.Background()
	ec2 := newFakeEC2()
	ec2.stopErr = errors.New("instance not found")
	az := newFakeAzure()

	fencer, _ := NewMultiLevelFencer(
		NewEC2Agent("ec2", ec2),
		NewAzureAgent("azure", "rg", az),
	)
	conf, err := fencer.Fence(ctx, "vm-9")
	if err != nil {
		t.Fatalf("Fence: %v", err)
	}
	if conf.Agent != "azure" {
		t.Fatalf("won by %q, want azure", conf.Agent)
	}
	if st, _ := az.VMPowerState(ctx, "rg", "vm-9"); st != "deallocated" {
		t.Fatalf("azure vm not stopped: %q", st)
	}
}
