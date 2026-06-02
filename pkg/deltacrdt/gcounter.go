// Package deltacrdt implements four delta-state CRDTs: GCounter, PNCounter,
// ORSet (observed-remove set), and LWWMap (last-writer-wins map).
//
// Delta-state CRDTs differ from full-state CRDTs in that every mutation
// operation returns a *delta* — the smallest sub-state that describes the
// change — rather than broadcasting the entire replica state. A receiver
// integrates deltas via Merge, which is commutative, associative, and
// idempotent, so replicas converge regardless of delivery order or duplicate
// delivery.
//
// No clocks, no math/rand, no cgo, no network, no external packages.
package deltacrdt

import "sort"

// ─────────────────────────────────────────────────────────────────────────────
// GCounter — grow-only counter.
//
// State: map[nodeID]uint64 (per-node increment counters).
// Delta: same shape — only the node(s) that incremented are populated.
// Value: sum of all per-node counters.
//
// Mutation guard pinned by TestMergeCommutativeAssociativeIdempotent:
//   the join rule "take the max per node" in gcounterJoin.
// ─────────────────────────────────────────────────────────────────────────────

// GCounter is a grow-only counter.
type GCounter struct {
	// nodeID identifies this replica.
	nodeID string
	// counts holds the per-node contribution.
	counts map[string]uint64
}

// GCounterDelta is the delta emitted by an Increment operation.
type GCounterDelta struct {
	// counts carries only the changed node(s).
	counts map[string]uint64
}

// NewGCounter returns an empty GCounter for the given nodeID.
func NewGCounter(nodeID string) *GCounter {
	return &GCounter{nodeID: nodeID, counts: make(map[string]uint64)}
}

// Increment increments this replica's counter by n and returns the delta.
func (g *GCounter) Increment(n uint64) GCounterDelta {
	g.counts[g.nodeID] += n
	return GCounterDelta{counts: map[string]uint64{g.nodeID: g.counts[g.nodeID]}}
}

// Merge integrates a delta (or another full state expressed as a delta) into g.
// The join rule is: for each node take the maximum of the two values.
//
// MUTATION GUARD — TestMergeCommutativeAssociativeIdempotent pins this:
// if the max is replaced with an unconditional overwrite, merging in any order
// will no longer be idempotent and the test will fail.
func (g *GCounter) Merge(d GCounterDelta) {
	for node, v := range d.counts {
		if v > g.counts[node] { // ← mutation-killable guard
			g.counts[node] = v
		}
	}
}

// Value returns the current counter total.
func (g *GCounter) Value() uint64 {
	var sum uint64
	for _, v := range g.counts {
		sum += v
	}
	return sum
}

// State returns a full-state delta (used for testing convergence comparisons).
func (g *GCounter) State() GCounterDelta {
	cp := make(map[string]uint64, len(g.counts))
	for k, v := range g.counts {
		cp[k] = v
	}
	return GCounterDelta{counts: cp}
}

// gcounterEqual reports whether two full states are identical (for test assertions).
func gcounterEqual(a, b GCounterDelta) bool {
	if len(a.counts) != len(b.counts) {
		return false
	}
	for k, v := range a.counts {
		if b.counts[k] != v {
			return false
		}
	}
	return true
}

// sortedNodes returns node IDs in deterministic order (used in serialisation).
func sortedNodes(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
