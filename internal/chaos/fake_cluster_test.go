// Tests for package chaos — in-process fake cluster implementation + all
// closure criteria from HXC-1422 (CLAUDE-1 compliance).
package chaos

import (
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// FakeCluster — real state machine used by every test.
//
// This is NOT a no-op mock. Every method mutates observable state that the
// tests assert against (CLAUDE-1 sink-side requirement).
// ---------------------------------------------------------------------------

// fakeNode holds the observable state for a single cluster node.
type fakeNode struct {
	down            bool
	partitionedFrom map[string]bool
	writesStalled   bool
	clockSkewNanos  int64
}

// FakeCluster is a fully deterministic in-process cluster. Tests drive its
// state directly via the Target interface methods, then inspect it via
// NodeState().
type FakeCluster struct {
	mu    sync.Mutex
	nodes map[string]*fakeNode
	// healthOverride: when non-nil it is called to supply HealthScore().
	// Useful for tests that need to drive the health value externally.
	healthOverride func() float64
}

// NewFakeCluster builds a cluster with the given node IDs, all initially healthy.
func NewFakeCluster(nodeIDs ...string) *FakeCluster {
	fc := &FakeCluster{nodes: make(map[string]*fakeNode, len(nodeIDs))}
	for _, id := range nodeIDs {
		fc.nodes[id] = &fakeNode{partitionedFrom: make(map[string]bool)}
	}
	return fc
}

// --- Target interface ---

func (fc *FakeCluster) Nodes() []string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	out := make([]string, 0, len(fc.nodes))
	for id := range fc.nodes {
		out = append(out, id)
	}
	return out
}

func (fc *FakeCluster) NodeState(nodeID string) (NodeState, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	n, ok := fc.nodes[nodeID]
	if !ok {
		return NodeState{}, &nodeNotFoundError{nodeID}
	}
	// Return a copy so callers cannot mutate the internal state.
	partCopy := make(map[string]bool, len(n.partitionedFrom))
	for k, v := range n.partitionedFrom {
		partCopy[k] = v
	}
	return NodeState{
		Down:            n.down,
		PartitionedFrom: partCopy,
		WritesStalled:   n.writesStalled,
		ClockSkewNanos:  n.clockSkewNanos,
	}, nil
}

func (fc *FakeCluster) SetNodeDown(nodeID string, down bool) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	n, ok := fc.nodes[nodeID]
	if !ok {
		return &nodeNotFoundError{nodeID}
	}
	n.down = down
	return nil
}

func (fc *FakeCluster) SetPartition(src, dst string, partition bool) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	n, ok := fc.nodes[src]
	if !ok {
		return &nodeNotFoundError{src}
	}
	if _, ok2 := fc.nodes[dst]; !ok2 {
		return &nodeNotFoundError{dst}
	}
	n.partitionedFrom[dst] = partition
	if !partition {
		delete(n.partitionedFrom, dst)
	}
	return nil
}

func (fc *FakeCluster) SetWritesStalled(nodeID string, stalled bool) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	n, ok := fc.nodes[nodeID]
	if !ok {
		return &nodeNotFoundError{nodeID}
	}
	n.writesStalled = stalled
	return nil
}

func (fc *FakeCluster) SetClockSkew(nodeID string, skewNanos int64) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	n, ok := fc.nodes[nodeID]
	if !ok {
		return &nodeNotFoundError{nodeID}
	}
	n.clockSkewNanos = skewNanos
	return nil
}

// HealthScore returns 1.0 (all nodes healthy) unless an override is set or
// any node is marked down — in which case it returns the fraction that are up.
func (fc *FakeCluster) HealthScore() float64 {
	if fc.healthOverride != nil {
		return fc.healthOverride()
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.nodes) == 0 {
		return 1.0
	}
	up := 0
	for _, n := range fc.nodes {
		if !n.down {
			up++
		}
	}
	return float64(up) / float64(len(fc.nodes))
}

// SetHealthOverride installs a custom HealthScore provider.  Pass nil to
// revert to the default (fraction of nodes that are up).
func (fc *FakeCluster) SetHealthOverride(fn func() float64) {
	fc.healthOverride = fn
}

// nodeNotFoundError is a typed error for missing nodes (CLAUDE-1 honesty).
type nodeNotFoundError struct{ id string }

func (e *nodeNotFoundError) Error() string { return "chaos: node not found: " + e.id }

// ---------------------------------------------------------------------------
// FakeClock — injectable clock that makes ChaosRunner fully deterministic.
// ---------------------------------------------------------------------------

// FakeClock lets tests control time precisely without real sleeps.
//
// After() returns a channel that fires immediately unless the test installs a
// custom trigger via SetAfterFunc.  Sleep() is a no-op by default.
type FakeClock struct {
	mu      sync.Mutex
	current time.Time
	// afterFn is called each time After() is invoked; it receives the duration
	// and returns the channel.  Default: fires immediately.
	afterFn func(d time.Duration) <-chan time.Time
}

// NewFakeClock creates a FakeClock seeded at t.
// By default, each After(d) call advances the clock by d and fires
// immediately, so the runner loop makes progress without real sleeps.
func NewFakeClock(t time.Time) *FakeClock {
	fc := &FakeClock{current: t}
	fc.afterFn = func(d time.Duration) <-chan time.Time {
		fc.mu.Lock()
		fc.current = fc.current.Add(d)
		now := fc.current
		fc.mu.Unlock()
		ch := make(chan time.Time, 1)
		ch <- now
		return ch
	}
	return fc
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *FakeClock) Sleep(_ time.Duration) {} // no-op

func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	fn := c.afterFn
	c.mu.Unlock()
	return fn(d)
}

// Advance moves the fake clock forward by d and returns the new time.
func (c *FakeClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
	return c.current
}

// SetAfterFunc installs a custom After implementation.
func (c *FakeClock) SetAfterFunc(fn func(d time.Duration) <-chan time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.afterFn = fn
}
