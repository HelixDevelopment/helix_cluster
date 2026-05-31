package chaos

import (
	"math/rand"
	"testing"
	"time"
)

func TestNetworkPartition(t *testing.T) {
	np := &NetworkPartition{Nodes: []string{"a", "b"}}
	if np.Name() != "network-partition" {
		t.Errorf("expected name network-partition, got %s", np.Name())
	}
	if err := np.Apply(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := np.Apply(); err == nil {
		t.Error("expected error on double apply")
	}
	if err := np.Restore(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := np.Restore(); err == nil {
		t.Error("expected error on double restore")
	}
}

func TestPacketLoss(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	pl := NewPacketLoss("a", "b", 50.0, rng)
	if pl.Name() != "packet-loss" {
		t.Errorf("expected name packet-loss, got %s", pl.Name())
	}
	if pl.ShouldDrop() {
		t.Error("expected no drop before apply")
	}
	if err := pl.Apply(); err != nil {
		t.Fatal(err)
	}
	// With 50% loss and deterministic seed, we should see some drops over many trials.
	drops := 0
	for i := 0; i < 1000; i++ {
		if pl.ShouldDrop() {
			drops++
		}
	}
	if drops == 0 || drops == 1000 {
		t.Errorf("expected mixed drops, got %d/1000", drops)
	}
	if err := pl.Restore(); err != nil {
		t.Fatal(err)
	}
	if pl.ShouldDrop() {
		t.Error("expected no drop after restore")
	}
}

func TestLatencyInjection(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	li := NewLatencyInjection("a", "b", 100*time.Millisecond, 10*time.Millisecond, rng)
	if li.Name() != "latency-injection" {
		t.Errorf("expected name latency-injection, got %s", li.Name())
	}
	if li.NextDelay() != 0 {
		t.Error("expected zero delay before apply")
	}
	if err := li.Apply(); err != nil {
		t.Fatal(err)
	}
	d := li.NextDelay()
	if d < 100*time.Millisecond || d > 110*time.Millisecond {
		t.Errorf("expected delay in [100ms,110ms], got %v", d)
	}
	if err := li.Restore(); err != nil {
		t.Fatal(err)
	}
	if li.NextDelay() != 0 {
		t.Error("expected zero delay after restore")
	}
}

func TestNodeCrash(t *testing.T) {
	nc := &NodeCrash{NodeID: "n1"}
	if nc.Name() != "node-crash" {
		t.Errorf("expected name node-crash, got %s", nc.Name())
	}
	if nc.IsCrashed() {
		t.Error("expected not crashed initially")
	}
	if err := nc.Apply(); err != nil {
		t.Fatal(err)
	}
	if !nc.IsCrashed() {
		t.Error("expected crashed after apply")
	}
	if err := nc.Restore(); err != nil {
		t.Fatal(err)
	}
	if nc.IsCrashed() {
		t.Error("expected not crashed after restore")
	}
}

func TestResourceExhaustion(t *testing.T) {
	re := &ResourceExhaustion{NodeID: "n1", Resource: "cpu", Percent: 95.0}
	if re.Name() != "resource-exhaustion" {
		t.Errorf("expected name resource-exhaustion, got %s", re.Name())
	}
	if err := re.Apply(); err != nil {
		t.Fatal(err)
	}
	if err := re.Apply(); err == nil {
		t.Error("expected error on double apply")
	}
	if err := re.Restore(); err != nil {
		t.Fatal(err)
	}
	if err := re.Restore(); err == nil {
		t.Error("expected error on double restore")
	}
}

func TestChaosRunner(t *testing.T) {
	cr := NewChaosRunner()
	cr.AddFault(&NetworkPartition{Nodes: []string{"a", "b"}})
	cr.AddFault(&NodeCrash{NodeID: "c"})

	faults := cr.ListFaults()
	if len(faults) != 2 {
		t.Fatalf("expected 2 faults, got %d", len(faults))
	}

	if err := cr.ApplyAll(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	active := cr.ActiveFaults()
	if len(active) != 1 {
		t.Errorf("expected 1 active fault (NodeCrash), got %d", len(active))
	}

	if err := cr.RestoreAll(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestChaosRunner_Empty(t *testing.T) {
	cr := NewChaosRunner()
	if err := cr.ApplyAll(); err != nil {
		t.Fatalf("unexpected error on empty apply: %v", err)
	}
	if err := cr.RestoreAll(); err != nil {
		t.Fatalf("unexpected error on empty restore: %v", err)
	}
	if len(cr.ListFaults()) != 0 {
		t.Error("expected zero faults")
	}
}
