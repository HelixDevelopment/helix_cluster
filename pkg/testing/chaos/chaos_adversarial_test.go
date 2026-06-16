package chaos

import (
	"math/rand"
	"testing"
	"time"
)

// TestActiveFaults_ReportsAllAppliedFaults is the load-bearing adversarial probe.
//
// CONTRACT (CLAUDE-1 sink-side): ChaosRunner.ActiveFaults() must report EXACTLY
// the set of faults that are currently applied and producing an observable
// effect. A fault whose Apply() succeeded — and which sink-side genuinely
// changes behaviour — MUST appear in ActiveFaults(). If it does not, a chaos
// "campaign" that injects, say, a NetworkPartition or an RPCError and then asks
// the runner "what is active?" gets a silent no-op answer: the framework claims
// nothing is active while the fault is in fact perturbing the system. That is a
// PASS-bluff.
//
// This test pairs each fault with INDEPENDENT sink-side evidence that the fault
// is really active (the fault's own observable method), then demands that
// ActiveFaults() agrees.
func TestActiveFaults_ReportsAllAppliedFaults(t *testing.T) {
	cr := NewChaosRunner()

	np := &NetworkPartition{Nodes: []string{"a", "b"}}
	pl := NewPacketLoss("a", "b", 100.0, rand.New(rand.NewSource(1))) // 100% => always drops
	rpcErr := &RPCError{Method: "Get"}
	skew := &ClockSkew{NodeID: "n1", Offset: time.Hour}
	nc := &NodeCrash{NodeID: "c"}

	cr.AddFault(np)
	cr.AddFault(pl)
	cr.AddFault(rpcErr)
	cr.AddFault(skew)
	cr.AddFault(nc)

	if err := cr.ApplyAll(); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}

	// SINK-SIDE EVIDENCE that every fault is genuinely active and effecting change:
	if !np.applied {
		t.Fatal("NetworkPartition.applied false after Apply — fault not really active")
	}
	if !pl.ShouldDrop() {
		t.Fatal("PacketLoss at 100% did not drop after Apply — fault not really active")
	}
	if rpcErr.Call("Get") == nil {
		t.Fatal("RPCError.Call returned nil after Apply — fault not really active")
	}
	base := time.Unix(0, 0)
	if skew.Now(base).Equal(base) {
		t.Fatal("ClockSkew.Now unchanged after Apply — fault not really active")
	}
	if !nc.IsCrashed() {
		t.Fatal("NodeCrash not crashed after Apply — fault not really active")
	}

	// Every one of the 5 faults is provably active. ActiveFaults() must list all 5.
	active := cr.ActiveFaults()
	if len(active) != 5 {
		names := make([]string, len(active))
		for i, f := range active {
			names[i] = f.Name()
		}
		t.Fatalf("ActiveFaults reported %d active (%v); want 5 — all applied faults "+
			"produce real sink-side effects but the runner hides non-NodeCrash faults", len(active), names)
	}
}

// TestActiveFaults_ClearedFaultDropsOut proves the lifecycle clears correctly:
// restoring a fault must remove it from ActiveFaults() AND stop its effect.
func TestActiveFaults_ClearedFaultDropsOut(t *testing.T) {
	cr := NewChaosRunner()
	rpcErr := &RPCError{Method: "Get"}
	nc := &NodeCrash{NodeID: "c"}
	cr.AddFault(rpcErr)
	cr.AddFault(nc)

	if err := cr.ApplyAll(); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}
	if got := len(cr.ActiveFaults()); got != 2 {
		t.Fatalf("after ApplyAll want 2 active, got %d", got)
	}

	// Clear just the RPC fault.
	if err := rpcErr.Restore(); err != nil {
		t.Fatalf("Restore rpcErr: %v", err)
	}

	// Sink-side: effect must be gone.
	if rpcErr.Call("Get") != nil {
		t.Fatal("RPCError.Call still injects after Restore — fault not cleared sink-side")
	}
	// And it must drop out of ActiveFaults while NodeCrash remains.
	active := cr.ActiveFaults()
	if len(active) != 1 {
		t.Fatalf("after clearing rpcErr want 1 active, got %d", len(active))
	}
	if active[0].Name() != "node-crash" {
		t.Fatalf("remaining active fault should be node-crash, got %s", active[0].Name())
	}
}

// TestActiveFaults_OrderingDeterministic asserts ActiveFaults preserves a
// stable, insertion-driven order across repeated calls (no map-iteration leak).
func TestActiveFaults_OrderingDeterministic(t *testing.T) {
	build := func() *ChaosRunner {
		cr := NewChaosRunner()
		cr.AddFault(&RPCError{Method: "A"})
		cr.AddFault(&NodeCrash{NodeID: "c"})
		cr.AddFault(&ClockSkew{NodeID: "n1", Offset: time.Second})
		cr.AddFault(&NetworkPartition{Nodes: []string{"x"}})
		if err := cr.ApplyAll(); err != nil {
			t.Fatalf("ApplyAll: %v", err)
		}
		return cr
	}
	cr := build()
	var prev []string
	for iter := 0; iter < 20; iter++ {
		af := cr.ActiveFaults()
		names := make([]string, len(af))
		for i, f := range af {
			names[i] = f.Name()
		}
		if prev != nil {
			if len(names) != len(prev) {
				t.Fatalf("ActiveFaults length unstable: %v vs %v", prev, names)
			}
			for i := range names {
				if names[i] != prev[i] {
					t.Fatalf("ActiveFaults order unstable across calls: %v vs %v", prev, names)
				}
			}
		}
		prev = names
	}
}

// TestActiveFaults_RaceClean exercises ActiveFaults concurrently with the
// runner's locking. Run with -race separately.
func TestActiveFaults_RaceClean(t *testing.T) {
	cr := NewChaosRunner()
	cr.AddFault(&RPCError{Method: "Get"})
	cr.AddFault(&NodeCrash{NodeID: "c"})
	if err := cr.ApplyAll(); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			_ = cr.ActiveFaults()
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = cr.ListFaults()
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent ActiveFaults")
	}
}
