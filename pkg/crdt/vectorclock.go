package crdt

// CausalityRelation describes the happens-before ordering between two vector
// clocks as defined by Lamport (1978) and extended to partial orders by
// Mattern/Fidge (1988).
type CausalityRelation int

const (
	// Before means vc happened before other: vc[i] <= other[i] for all i, and
	// strictly less for at least one i.
	Before CausalityRelation = iota
	// After means other happened before vc (i.e. other.Compare(vc) == Before).
	After
	// Equal means both clocks carry identical values for every replica slot.
	Equal
	// Concurrent means neither clock dominates the other — they diverged from a
	// common ancestor without observing each other.
	Concurrent
)

// String returns a human-readable label for the relation.
func (r CausalityRelation) String() string {
	switch r {
	case Before:
		return "Before"
	case After:
		return "After"
	case Equal:
		return "Equal"
	case Concurrent:
		return "Concurrent"
	default:
		return "Unknown"
	}
}

// VectorClock tracks a per-replica logical timestamp following the Fidge/Mattern
// vector clock protocol. Missing replica slots are treated as zero throughout the
// API. The type is a plain map so callers can construct clocks with a map literal;
// Clone must be used before mutating a clock that is shared or stored.
type VectorClock map[ReplicaID]uint64

// Increment increments the clock entry for the given replica by one, modelling
// a local event at that replica. If the replica was not previously present in the
// clock its entry is initialised to 1.
func (vc VectorClock) Increment(id ReplicaID) {
	vc[id]++
}

// Merge returns a NEW VectorClock that is the component-wise maximum of vc and
// other over the union of all replica keys. The operation is commutative and
// idempotent: Merge(a,b) == Merge(b,a) and Merge(a,a) == a.
//
// Neither vc nor other is modified.
func (vc VectorClock) Merge(other VectorClock) VectorClock {
	out := make(VectorClock, len(vc))
	for id, v := range vc {
		out[id] = v
	}
	for id, v := range other {
		if v > out[id] {
			out[id] = v
		}
	}
	return out
}

// Compare returns the CausalityRelation between vc and other:
//
//   - Equal      — every slot (including zero-absent slots) is identical.
//   - Before     — vc[i] <= other[i] for all i AND vc[j] < other[j] for some j.
//   - After      — other.Compare(vc) == Before (symmetric inverse).
//   - Concurrent — neither clock dominates the other; they diverged.
//
// Missing entries are treated as 0 per the standard vector clock convention.
func (vc VectorClock) Compare(other VectorClock) CausalityRelation {
	// We sweep over the union of keys and track whether we observed any slot
	// where vc is strictly less than other (vcLess) and any where vc is strictly
	// greater than other (vcGreater).  Equal means neither flag is set after the
	// sweep; Before means vcLess && !vcGreater; After means vcGreater && !vcLess;
	// Concurrent means both flags are set.
	var vcLess, vcGreater bool

	// Keys present in vc.
	for id, v := range vc {
		w := other[id]
		if v < w {
			vcLess = true
		} else if v > w {
			vcGreater = true
		}
		// Early exit — Concurrent is the maximal uncertainty state.
		if vcLess && vcGreater {
			return Concurrent
		}
	}

	// Keys present in other but absent in vc (treated as vc[id]==0).
	for id, w := range other {
		if _, exists := vc[id]; exists {
			// Already evaluated above.
			continue
		}
		if w > 0 {
			// vc[id] is implicitly 0 < w.
			vcLess = true
		}
		if vcLess && vcGreater {
			return Concurrent
		}
	}

	switch {
	case !vcLess && !vcGreater:
		return Equal
	case vcLess && !vcGreater:
		return Before
	case vcGreater && !vcLess:
		return After
	default:
		return Concurrent
	}
}

// HappensBefore reports whether vc causally precedes other, i.e.
// vc.Compare(other) == Before.
func (vc VectorClock) HappensBefore(other VectorClock) bool {
	return vc.Compare(other) == Before
}

// Clone returns a deep copy of the clock. Use Clone before storing or sharing a
// clock that will subsequently be mutated via Increment.
func (vc VectorClock) Clone() VectorClock {
	out := make(VectorClock, len(vc))
	for id, v := range vc {
		out[id] = v
	}
	return out
}
