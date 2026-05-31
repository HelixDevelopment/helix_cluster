// Package admissioncontrol implements N+K capacity-reserve admission control.
//
// The controller admits a new job ONLY if, after placing it, the cluster would
// STILL have enough free capacity to absorb the loss of any K nodes. The reserve
// headroom that must remain free is the combined capacity of the K LARGEST live
// nodes — losing any K nodes can free at most that much running work, so keeping
// that much capacity unallocated guarantees the displaced work can be re-placed.
//
// All capacities are expressed as integer capacity units (e.g. millicores, MiB);
// the package is unit-agnostic.
package admissioncontrol

// Node describes a single cluster node's capacity and current allocation.
//
// Capacity is the node's total schedulable capacity. Allocated is the portion
// currently consumed by running work. Allocated is clamped to [0, Capacity] when
// the node is admitted to the controller; an over-allocated node contributes zero
// free capacity rather than negative free capacity.
type Node struct {
	// ID uniquely identifies the node within the cluster.
	ID string
	// Capacity is the node's total schedulable capacity in capacity units.
	Capacity int64
	// Allocated is the capacity currently consumed by running work.
	Allocated int64
	// Failed marks the node as lost. Failed nodes contribute neither free
	// capacity nor reserve headroom and are excluded from the K-largest set.
	Failed bool
}

// free returns the node's unallocated capacity, never negative.
func (n Node) free() int64 {
	if n.Failed {
		return 0
	}
	f := n.Capacity - n.Allocated
	if f < 0 {
		return 0
	}
	return f
}

// Decision is the outcome of an admission request.
type Decision struct {
	// Admitted is true iff the request preserves the N+K invariant.
	Admitted bool
	// Reason explains a rejection; empty when Admitted is true.
	Reason string
	// RequestedCapacity echoes the capacity the caller asked for.
	RequestedCapacity int64
	// AvailableForAdmission is the free capacity that may be handed out while
	// still preserving the reserve, computed at decision time.
	AvailableForAdmission int64
	// ReservedHeadroom is the capacity that must remain free to tolerate K
	// failures, computed at decision time.
	ReservedHeadroom int64
}

// Rejection reasons.
const (
	// ReasonInsufficientHeadroom indicates admitting the job would leave less
	// free capacity than the N+K reserve requires.
	ReasonInsufficientHeadroom = "rejected: insufficient headroom to preserve N+K failure tolerance"
	// ReasonNegativeRequest indicates the request asked for a negative amount.
	ReasonNegativeRequest = "rejected: negative requested capacity"
)
