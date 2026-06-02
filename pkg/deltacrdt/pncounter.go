package deltacrdt

// ─────────────────────────────────────────────────────────────────────────────
// PNCounter — positive-negative counter (increment + decrement).
//
// State: two GCounter maps: P (increments) and N (decrements).
// Value: sum(P) − sum(N).
// Delta: a pair of GCounter deltas — only the mutated side is populated.
// ─────────────────────────────────────────────────────────────────────────────

// PNCounter is an increment/decrement counter built from two GCounters.
type PNCounter struct {
	nodeID string
	pos    map[string]uint64 // increment side
	neg    map[string]uint64 // decrement side
}

// PNCounterDelta is the delta emitted by Increment or Decrement.
type PNCounterDelta struct {
	pos map[string]uint64
	neg map[string]uint64
}

// NewPNCounter returns an empty PNCounter for nodeID.
func NewPNCounter(nodeID string) *PNCounter {
	return &PNCounter{
		nodeID: nodeID,
		pos:    make(map[string]uint64),
		neg:    make(map[string]uint64),
	}
}

// Increment adds n to the positive side and returns the delta.
func (p *PNCounter) Increment(n uint64) PNCounterDelta {
	p.pos[p.nodeID] += n
	return PNCounterDelta{
		pos: map[string]uint64{p.nodeID: p.pos[p.nodeID]},
		neg: nil,
	}
}

// Decrement adds n to the negative side and returns the delta.
func (p *PNCounter) Decrement(n uint64) PNCounterDelta {
	p.neg[p.nodeID] += n
	return PNCounterDelta{
		pos: nil,
		neg: map[string]uint64{p.nodeID: p.neg[p.nodeID]},
	}
}

// Merge integrates a PNCounterDelta. Both sides use the same max-join rule.
func (p *PNCounter) Merge(d PNCounterDelta) {
	for node, v := range d.pos {
		if v > p.pos[node] {
			p.pos[node] = v
		}
	}
	for node, v := range d.neg {
		if v > p.neg[node] {
			p.neg[node] = v
		}
	}
}

// Value returns the net counter value (positive minus negative).
// The result is int64; it can be negative.
func (p *PNCounter) Value() int64 {
	var pos, neg uint64
	for _, v := range p.pos {
		pos += v
	}
	for _, v := range p.neg {
		neg += v
	}
	return int64(pos) - int64(neg)
}

// State returns a full-state delta for convergence tests.
func (p *PNCounter) State() PNCounterDelta {
	cp := func(m map[string]uint64) map[string]uint64 {
		out := make(map[string]uint64, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	return PNCounterDelta{pos: cp(p.pos), neg: cp(p.neg)}
}

// pncounterEqual reports whether two full states are identical.
func pncounterEqual(a, b PNCounterDelta) bool {
	eqMap := func(x, y map[string]uint64) bool {
		if len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if y[k] != v {
				return false
			}
		}
		return true
	}
	return eqMap(a.pos, b.pos) && eqMap(a.neg, b.neg)
}
