package resources

import (
	"sync"
	"time"
)

// NodeAggregator collects resources from multiple readers and maintains node state.
type NodeAggregator struct {
	mu      sync.RWMutex
	readers []Reader
	nodes   map[string]*nodeState
	ttl     time.Duration
	clock   func() time.Time
}

// NewNodeAggregator creates an aggregator with the given TTL for stale data.
func NewNodeAggregator(ttl time.Duration) *NodeAggregator {
	return &NodeAggregator{
		readers: make([]Reader, 0),
		nodes:   make(map[string]*nodeState),
		ttl:     ttl,
		clock:   time.Now,
	}
}

// RegisterReader adds a resource reader to the aggregator.
func (a *NodeAggregator) RegisterReader(r Reader) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.readers = append(a.readers, r)
}

// RegisterNode adds a node to track. If the node already exists it is a no-op.
func (a *NodeAggregator) RegisterNode(nodeID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.nodes[nodeID]; ok {
		return
	}
	a.nodes[nodeID] = &nodeState{
		snap: ResourceSnapshot{
			NodeID:    nodeID,
			Timestamp: a.clock().Add(-a.ttl - time.Second),
		},
		ttl: a.ttl,
	}
}

// DeregisterNode removes a node from tracking.
func (a *NodeAggregator) DeregisterNode(nodeID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.nodes, nodeID)
}

// Collect gathers resources from all readers for all registered nodes.
func (a *NodeAggregator) Collect() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.clock()
	for nodeID, state := range a.nodes {
		var merged NodeResources
		merged.NodeID = nodeID

		for _, r := range a.readers {
			res, err := r.Read(nodeID)
			if err != nil {
				continue // best-effort: skip failing readers
			}
			merged = mergeResources(merged, res)
		}

		state.update(ResourceSnapshot{
			NodeID:    nodeID,
			Resources: merged,
			Timestamp: now,
		}, a.ttl)
	}
	return nil
}

// GetNode returns the latest snapshot for a node.
func (a *NodeAggregator) GetNode(id string) (ResourceSnapshot, bool) {
	a.mu.RLock()
	state, ok := a.nodes[id]
	a.mu.RUnlock()
	if !ok {
		return ResourceSnapshot{}, false
	}
	return state.get(), true
}

// ListNodes returns all known node snapshots, skipping expired entries.
func (a *NodeAggregator) ListNodes() []ResourceSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()

	now := a.clock()
	out := make([]ResourceSnapshot, 0, len(a.nodes))
	for _, state := range a.nodes {
		if state.isExpired(now) {
			continue
		}
		out = append(out, state.get())
	}
	return out
}

// mergeResources overlays src onto dst, preferring non-zero values from src.
func mergeResources(dst, src NodeResources) NodeResources {
	if src.NodeID != "" {
		dst.NodeID = src.NodeID
	}
	if src.CPU.Cores > 0 {
		dst.CPU = src.CPU
	}
	if src.Memory.TotalKB > 0 {
		dst.Memory = src.Memory
	}
	if src.GPU.Count > 0 {
		dst.GPU = src.GPU
	}
	// TFLOPS is a separable GPU signal: an accelerator-probe reader may report
	// only TFLOPS (Count==0) to enrich a GPU another reader already enumerated.
	// Overlay it when the source carries a value, without clobbering Count/Model.
	if src.GPU.TFLOPS > 0 {
		dst.GPU.TFLOPS = src.GPU.TFLOPS
	}
	if src.Disk.TotalKB > 0 {
		dst.Disk = src.Disk
	}
	if len(src.Network.Interfaces) > 0 {
		dst.Network = src.Network
	}
	// Accelerators overlay per-field so an override-label reader can declare an
	// NPU/FPGA/QPU independently of the GPU/CPU readers.
	if src.Accelerators.NPUTops > 0 {
		dst.Accelerators.NPUTops = src.Accelerators.NPUTops
	}
	if src.Accelerators.FPGALogicElements > 0 {
		dst.Accelerators.FPGALogicElements = src.Accelerators.FPGALogicElements
	}
	if src.Accelerators.QPUPresent {
		dst.Accelerators.QPUPresent = true
	}
	return dst
}

// StartBackgroundRefresh runs Collect at the given interval until the context is done.
func StartBackgroundRefresh(a *NodeAggregator, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = a.Collect()
		case <-stop:
			return
		}
	}
}

// Ensure NodeAggregator implements Aggregator.
var _ Aggregator = (*NodeAggregator)(nil)
