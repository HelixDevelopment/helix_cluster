package admissioncontrol

import (
	"fmt"
	"math"
	"sort"
	"sync"
)

// Controller implements N+K capacity-reserve admission control over a set of
// nodes. It is safe for concurrent use.
type Controller struct {
	mu sync.RWMutex
	// k is the number of simultaneous node failures the cluster must tolerate.
	k int
	// nodes maps node ID -> node state, in insertion-independent storage.
	nodes map[string]Node
}

// NewController creates a controller that preserves tolerance to k node failures.
//
// k must be >= 0. A k of 0 disables the reserve entirely (admits up to raw free
// capacity). Nodes may be supplied up front and/or added later via UpsertNode.
// Each node's Allocated is clamped into [0, Capacity].
func NewController(k int, nodes ...Node) (*Controller, error) {
	if k < 0 {
		return nil, fmt.Errorf("admissioncontrol: k must be >= 0, got %d", k)
	}
	c := &Controller{
		k:     k,
		nodes: make(map[string]Node, len(nodes)),
	}
	for _, n := range nodes {
		c.upsertLocked(n)
	}
	return c, nil
}

// upsertLocked inserts/updates a node with Allocated clamped to [0, Capacity].
// Caller must hold c.mu (or be in a constructor before publication).
func (c *Controller) upsertLocked(n Node) {
	if n.Capacity < 0 {
		n.Capacity = 0
	}
	if n.Allocated < 0 {
		n.Allocated = 0
	}
	if n.Allocated > n.Capacity {
		n.Allocated = n.Capacity
	}
	c.nodes[n.ID] = n
}

// UpsertNode adds or replaces a node by ID. Allocated is clamped to [0, Capacity].
func (c *Controller) UpsertNode(n Node) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.upsertLocked(n)
}

// MarkFailed marks a node as failed. It returns false if the node is unknown.
// A failed node stops contributing free capacity and reserve headroom, which can
// turn previously-admissible jobs into rejections on the next Admit.
func (c *Controller) MarkFailed(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.nodes[id]
	if !ok {
		return false
	}
	n.Failed = true
	c.nodes[id] = n
	return true
}

// liveNodesLocked returns a copy of the non-failed nodes. Caller holds c.mu.
func (c *Controller) liveNodesLocked() []Node {
	live := make([]Node, 0, len(c.nodes))
	for _, n := range c.nodes {
		if n.Failed {
			continue
		}
		live = append(live, n)
	}
	return live
}

// totalFreeLocked sums free capacity across live nodes. Caller holds c.mu.
func (c *Controller) totalFreeLocked() int64 {
	var sum int64
	for _, n := range c.nodes {
		sum += n.free() // free() already returns 0 for failed nodes
	}
	return sum
}

// reservedHeadroomLocked returns the combined Capacity of the K LARGEST live
// nodes — the amount of free capacity that must be preserved to tolerate the
// loss of any K nodes. Caller holds c.mu.
//
// If there are fewer than K live nodes, the reserve is the combined capacity of
// ALL live nodes (the worst case loses every node), which makes the cluster
// admit nothing until enough nodes return.
func (c *Controller) reservedHeadroomLocked() int64 {
	live := c.liveNodesLocked()
	// Sort by Capacity DESCENDING so the K largest are the first K entries.
	sort.Slice(live, func(i, j int) bool {
		return live[i].Capacity > live[j].Capacity
	})
	k := c.k
	if k > len(live) {
		k = len(live)
	}
	var reserve int64
	for i := 0; i < k; i++ {
		// Saturate instead of overflowing: an int64 wrap would make the reserve
		// NEGATIVE, which the admission invariant (free-req >= reserve) reads as
		// "infinite headroom" and admits beyond capacity — a total reserve
		// bypass. Clamping to MaxInt64 keeps the gate closed (admits nothing)
		// in the only regime where the K-largest capacities can't be summed.
		if reserve > math.MaxInt64-live[i].Capacity {
			return math.MaxInt64
		}
		reserve += live[i].Capacity
	}
	return reserve
}

// ReservedHeadroom returns the current N+K reserve: the combined capacity of the
// K largest live nodes.
func (c *Controller) ReservedHeadroom() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.reservedHeadroomLocked()
}

// AvailableForAdmission returns how much capacity may still be admitted while
// preserving the N+K reserve: total free capacity minus the reserved headroom,
// floored at zero.
func (c *Controller) AvailableForAdmission() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.availableForAdmissionLocked()
}

// availableForAdmissionLocked computes free - reserve, floored at 0.
func (c *Controller) availableForAdmissionLocked() int64 {
	avail := c.totalFreeLocked() - c.reservedHeadroomLocked()
	if avail < 0 {
		return 0
	}
	return avail
}

// Admit decides whether a job requesting the given capacity may be placed while
// preserving the N+K failure-tolerance invariant.
//
// The job is admitted iff, after subtracting the request from total free
// capacity, the remaining free capacity is STILL at least the reserved headroom
// (the K largest live nodes' capacity). Equivalently: request <=
// AvailableForAdmission. A zero-capacity request is always admitted (it consumes
// nothing). Admit does NOT place the job; the caller must record placement via
// UpsertNode to reflect the new allocation.
func (c *Controller) Admit(requestedCapacity int64) Decision {
	c.mu.RLock()
	defer c.mu.RUnlock()

	reserve := c.reservedHeadroomLocked()
	free := c.totalFreeLocked()
	available := free - reserve
	if available < 0 {
		available = 0
	}

	d := Decision{
		RequestedCapacity:     requestedCapacity,
		ReservedHeadroom:      reserve,
		AvailableForAdmission: available,
	}

	if requestedCapacity < 0 {
		d.Reason = ReasonNegativeRequest
		return d
	}

	// A zero-capacity request consumes nothing, so it cannot erode the reserve and
	// is always admitted (per the contract above). Short-circuit BEFORE the
	// headroom check: when the cluster is already over-reserved (reserve > free,
	// reachable after node failures), the `free - 0 >= reserve` test would be
	// false and wrongly REJECT a no-cost job that changes nothing.
	if requestedCapacity == 0 {
		d.Admitted = true
		return d
	}

	// Invariant: free capacity AFTER placing the job must still cover the reserve.
	if free-requestedCapacity >= reserve {
		d.Admitted = true
		return d
	}
	d.Reason = ReasonInsufficientHeadroom
	return d
}
