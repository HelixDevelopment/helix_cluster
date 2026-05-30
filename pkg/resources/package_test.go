package resources

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---------- Mock Reader Tests ----------

func TestMockReader_Read(t *testing.T) {
	mr := NewMockReader()
	res, err := mr.Read("node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.NodeID != "node-1" {
		t.Errorf("NodeID = %q, want %q", res.NodeID, "node-1")
	}
	if res.CPU.Cores != 8 {
		t.Errorf("CPU.Cores = %d, want 8", res.CPU.Cores)
	}
	if res.Memory.TotalKB != 16*1024*1024 {
		t.Errorf("Memory.TotalKB = %d, want %d", res.Memory.TotalKB, 16*1024*1024)
	}
	if res.GPU.Count != 2 {
		t.Errorf("GPU.Count = %d, want 2", res.GPU.Count)
	}
}

func TestMockReader_Read_EmptyNodeID(t *testing.T) {
	mr := NewMockReader()
	_, err := mr.Read("")
	if err == nil {
		t.Fatal("expected error for empty nodeID")
	}
}

func TestMockReader_ReadMemInfo(t *testing.T) {
	mr := NewMockReader()
	mem, err := mr.ReadMemInfo()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mem.TotalKB != mr.TotalKB {
		t.Errorf("TotalKB = %d, want %d", mem.TotalKB, mr.TotalKB)
	}
	if mem.UsedKB != mr.TotalKB-mr.AvailableKB {
		t.Errorf("UsedKB = %d, want %d", mem.UsedKB, mr.TotalKB-mr.AvailableKB)
	}
}

// ---------- Aggregator Tests ----------

func TestNodeAggregator_Collect(t *testing.T) {
	agg := NewNodeAggregator(5 * time.Minute)
	agg.RegisterReader(NewMockReader())
	agg.RegisterNode("node-a")

	if err := agg.Collect(); err != nil {
		t.Fatalf("Collect error: %v", err)
	}

	snap, ok := agg.GetNode("node-a")
	if !ok {
		t.Fatal("expected node-a to exist")
	}
	if snap.NodeID != "node-a" {
		t.Errorf("NodeID = %q, want %q", snap.NodeID, "node-a")
	}
	if snap.Resources.CPU.Cores == 0 {
		t.Error("expected non-zero CPU cores")
	}
}

func TestNodeAggregator_GetNode_Missing(t *testing.T) {
	agg := NewNodeAggregator(5 * time.Minute)
	_, ok := agg.GetNode("missing")
	if ok {
		t.Error("expected ok=false for missing node")
	}
}

func TestNodeAggregator_ListNodes(t *testing.T) {
	agg := NewNodeAggregator(5 * time.Minute)
	agg.RegisterReader(NewMockReader())
	agg.RegisterNode("node-1")
	agg.RegisterNode("node-2")

	if err := agg.Collect(); err != nil {
		t.Fatalf("Collect error: %v", err)
	}

	nodes := agg.ListNodes()
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	ids := make(map[string]bool)
	for _, n := range nodes {
		ids[n.NodeID] = true
	}
	if !ids["node-1"] || !ids["node-2"] {
		t.Error("expected both nodes in list")
	}
}

func TestNodeAggregator_ListNodes_SkipsExpired(t *testing.T) {
	fixed := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	agg := NewNodeAggregator(1 * time.Second)
	agg.clock = func() time.Time { return fixed }
	agg.RegisterReader(NewMockReader())
	agg.RegisterNode("node-x")

	if err := agg.Collect(); err != nil {
		t.Fatalf("Collect error: %v", err)
	}

	// Advance clock past TTL.
	agg.clock = func() time.Time { return fixed.Add(2 * time.Second) }

	nodes := agg.ListNodes()
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes after expiry, got %d", len(nodes))
	}
}

func TestNodeAggregator_RegisterDeregister(t *testing.T) {
	agg := NewNodeAggregator(5 * time.Minute)
	agg.RegisterNode("node-a")
	if _, ok := agg.GetNode("node-a"); !ok {
		t.Fatal("expected node-a after registration")
	}
	agg.DeregisterNode("node-a")
	if _, ok := agg.GetNode("node-a"); ok {
		t.Error("expected node-a to be deregistered")
	}
}

func TestNodeAggregator_MultipleReaders(t *testing.T) {
	agg := NewNodeAggregator(5 * time.Minute)
	agg.RegisterReader(NewMockReader())
	agg.RegisterReader(NewMockReader())
	agg.RegisterNode("node-a")

	if err := agg.Collect(); err != nil {
		t.Fatalf("Collect error: %v", err)
	}

	snap, ok := agg.GetNode("node-a")
	if !ok {
		t.Fatal("expected node-a")
	}
	if snap.Resources.CPU.Cores != 8 {
		t.Errorf("CPU.Cores = %d, want 8", snap.Resources.CPU.Cores)
	}
}

func TestNodeAggregator_ConcurrentAccess(t *testing.T) {
	agg := NewNodeAggregator(5 * time.Minute)
	agg.RegisterReader(NewMockReader())
	for i := 0; i < 10; i++ {
		agg.RegisterNode(fmt.Sprintf("node-%d", i))
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = agg.Collect()
			_ = agg.ListNodes()
			_, _ = agg.GetNode("node-0")
		}()
	}
	wg.Wait()
}

// ---------- Mutation Tests ----------

// Mutation: change MockReader default cores to 0.
func TestMockReader_MutateZeroCores(t *testing.T) {
	mr := NewMockReader()
	mr.Cores = 0
	res, err := mr.Read("node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.CPU.Cores != 0 {
		t.Errorf("mutation not detected: Cores = %d, want 0", res.CPU.Cores)
	}
}

// Mutation: change MockReader total memory to 0.
func TestMockReader_MutateZeroMemory(t *testing.T) {
	mr := NewMockReader()
	mr.TotalKB = 0
	res, err := mr.Read("node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Memory.TotalKB != 0 {
		t.Errorf("mutation not detected: TotalKB = %d, want 0", res.Memory.TotalKB)
	}
}

// Mutation: skip Collect and expect stale data.
func TestNodeAggregator_MutateSkipCollect(t *testing.T) {
	fixed := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	agg := NewNodeAggregator(1 * time.Minute)
	agg.clock = func() time.Time { return fixed }
	agg.RegisterNode("node-a")
	// Do NOT call Collect.

	// Node exists but snapshot is older than TTL because default is zero time.
	agg.clock = func() time.Time { return fixed.Add(2 * time.Minute) }
	nodes := agg.ListNodes()
	if len(nodes) != 0 {
		t.Fatalf("mutation not detected: expected 0 nodes, got %d", len(nodes))
	}
}

// Mutation: change TTL to very short and verify expiry.
func TestNodeAggregator_MutateShortTTL(t *testing.T) {
	fixed := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	agg := NewNodeAggregator(1 * time.Nanosecond)
	agg.clock = func() time.Time { return fixed }
	agg.RegisterReader(NewMockReader())
	agg.RegisterNode("node-a")
	if err := agg.Collect(); err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	agg.clock = func() time.Time { return fixed.Add(time.Second) }
	nodes := agg.ListNodes()
	if len(nodes) != 0 {
		t.Fatalf("mutation not detected: expected expiry with short TTL, got %d nodes", len(nodes))
	}
}

// Mutation: deregister a node and ensure it disappears.
func TestNodeAggregator_MutateDeregister(t *testing.T) {
	agg := NewNodeAggregator(5 * time.Minute)
	agg.RegisterReader(NewMockReader())
	agg.RegisterNode("node-a")
	if err := agg.Collect(); err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	agg.DeregisterNode("node-a")
	if _, ok := agg.GetNode("node-a"); ok {
		t.Fatal("mutation not detected: expected node-a to be gone")
	}
	nodes := agg.ListNodes()
	if len(nodes) != 0 {
		t.Fatalf("mutation not detected: expected 0 nodes, got %d", len(nodes))
	}
}

// Mutation: register duplicate node should not duplicate entries.
func TestNodeAggregator_MutateDuplicateRegistration(t *testing.T) {
	agg := NewNodeAggregator(5 * time.Minute)
	agg.RegisterReader(NewMockReader())
	agg.RegisterNode("node-a")
	agg.RegisterNode("node-a")
	if err := agg.Collect(); err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	nodes := agg.ListNodes()
	if len(nodes) != 1 {
		t.Fatalf("mutation not detected: expected 1 node, got %d", len(nodes))
	}
}
