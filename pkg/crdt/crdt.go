// Package crdt provides state-based convergent replicated data types (CvRDTs)
// for Helix Cluster OS. Every CRDT exposes a Merge operation that is
// commutative, associative, and idempotent, guaranteeing that replicas which
// have observed the same set of updates converge to identical state regardless
// of message ordering or duplication.
//
// The package is stdlib-only. The provided types are GCounter, PNCounter,
// LWWRegister, and ORSet. A companion anti-entropy helper lives in
// pkg/crdt/merkle.
//
// Concurrency note: the CRDT types are NOT goroutine-safe. Callers that share a
// replica across goroutines must serialize access (e.g. with a sync.Mutex).
// This mirrors the house style of pkg/lru, which documents the same contract.
package crdt

// ReplicaID identifies the replica that authored an update. It participates in
// LWWRegister tiebreaks and namespaces per-replica counter slots.
type ReplicaID string

// GCounter is a grow-only counter. Each replica increments only its own slot;
// the logical value is the sum of all slots. Merge takes the per-replica
// maximum, which is the only rule that keeps the merge idempotent: re-merging
// an already-seen state must not double-count.
type GCounter struct {
	counts map[ReplicaID]uint64
}

// NewGCounter returns an empty grow-only counter.
func NewGCounter() *GCounter {
	return &GCounter{counts: make(map[ReplicaID]uint64)}
}

// Inc adds delta to the calling replica's own slot.
func (g *GCounter) Inc(id ReplicaID, delta uint64) {
	g.counts[id] += delta
}

// Value returns the logical counter value (sum of all per-replica slots).
func (g *GCounter) Value() uint64 {
	var sum uint64
	for _, v := range g.counts {
		sum += v
	}
	return sum
}

// Clone returns a deep copy so that mutating the result cannot affect the
// receiver. Merge relies on this to remain side-effect free with respect to its
// arguments.
func (g *GCounter) Clone() *GCounter {
	out := NewGCounter()
	for id, v := range g.counts {
		out.counts[id] = v
	}
	return out
}

// Merge returns a new GCounter holding the per-replica maximum of g and other.
// Per-replica max (not sum) is what makes Merge commutative, associative, and
// idempotent.
func (g *GCounter) Merge(other *GCounter) *GCounter {
	out := g.Clone()
	for id, v := range other.counts {
		if v > out.counts[id] {
			out.counts[id] = v
		}
	}
	return out
}

// Equal reports whether two counters carry identical per-replica state. Zero
// slots and absent slots are treated as equal.
func (g *GCounter) Equal(other *GCounter) bool {
	for id, v := range g.counts {
		if other.counts[id] != v {
			return false
		}
	}
	for id, v := range other.counts {
		if g.counts[id] != v {
			return false
		}
	}
	return true
}

// PNCounter is an increment/decrement counter built from two grow-only
// counters: p tracks increments, n tracks decrements. Value = p.Value -
// n.Value. Because both halves are GCounters, the combined Merge inherits
// commutativity, associativity, and idempotence.
type PNCounter struct {
	p *GCounter
	n *GCounter
}

// NewPNCounter returns an empty PN counter.
func NewPNCounter() *PNCounter {
	return &PNCounter{p: NewGCounter(), n: NewGCounter()}
}

// Inc adds delta to the increment half for the calling replica.
func (c *PNCounter) Inc(id ReplicaID, delta uint64) {
	c.p.Inc(id, delta)
}

// Dec adds delta to the decrement half for the calling replica.
func (c *PNCounter) Dec(id ReplicaID, delta uint64) {
	c.n.Inc(id, delta)
}

// Value returns the signed logical value (increments minus decrements).
func (c *PNCounter) Value() int64 {
	return int64(c.p.Value()) - int64(c.n.Value()) //gosec:disable G115 -- PN-counter semantics: GCounter totals are bounded well within int64; difference is the signed value

}

// Clone returns a deep copy.
func (c *PNCounter) Clone() *PNCounter {
	return &PNCounter{p: c.p.Clone(), n: c.n.Clone()}
}

// Merge merges both halves independently.
func (c *PNCounter) Merge(other *PNCounter) *PNCounter {
	return &PNCounter{p: c.p.Merge(other.p), n: c.n.Merge(other.n)}
}

// Equal reports whether both halves match.
func (c *PNCounter) Equal(other *PNCounter) bool {
	return c.p.Equal(other.p) && c.n.Equal(other.n)
}

// LWWRegister is a last-writer-wins register. The winning value is the one with
// the highest timestamp; ties are broken deterministically by the authoring
// ReplicaID so that Merge is commutative even under identical timestamps.
type LWWRegister struct {
	value     string
	timestamp uint64
	replica   ReplicaID
	set       bool
}

// NewLWWRegister returns an empty register that loses every merge against a set
// register.
func NewLWWRegister() *LWWRegister {
	return &LWWRegister{}
}

// Set assigns value with the given timestamp and authoring replica. A later Set
// on the same replica with a non-greater timestamp is ignored (writes never
// move time backward), preserving idempotence under replay.
func (r *LWWRegister) Set(value string, timestamp uint64, id ReplicaID) {
	if r.dominates(timestamp, id) {
		return
	}
	r.value = value
	r.timestamp = timestamp
	r.replica = id
	r.set = true
}

// Value returns the current value and whether the register has ever been set.
func (r *LWWRegister) Value() (string, bool) {
	return r.value, r.set
}

// dominates reports whether the receiver's current state wins against an
// incoming (timestamp,id) pair. Higher timestamp wins; equal timestamp is
// broken by the larger ReplicaID.
func (r *LWWRegister) dominates(timestamp uint64, id ReplicaID) bool {
	if !r.set {
		return false
	}
	if r.timestamp != timestamp {
		return r.timestamp > timestamp
	}
	return r.replica >= id
}

// Clone returns a copy.
func (r *LWWRegister) Clone() *LWWRegister {
	cp := *r
	return &cp
}

// Merge returns the winner of r and other under the LWW ordering.
func (r *LWWRegister) Merge(other *LWWRegister) *LWWRegister {
	out := r.Clone()
	if other.set {
		out.Set(other.value, other.timestamp, other.replica)
	}
	return out
}

// Equal reports whether two registers hold identical state.
func (r *LWWRegister) Equal(other *LWWRegister) bool {
	if r.set != other.set {
		return false
	}
	if !r.set {
		return true
	}
	return r.value == other.value && r.timestamp == other.timestamp && r.replica == other.replica
}

// tag uniquely identifies a single add operation in an ORSet.
type tag struct {
	Replica ReplicaID
	Seq     uint64
}

// ORSet is an observed-remove set. Each Add stamps the element with a unique
// tag; Remove tombstones exactly the tags the removing replica has observed. An
// element is present iff it has at least one live (non-tombstoned) tag. This
// yields add-wins semantics: an Add concurrent with a Remove survives because
// its fresh tag was not observed by the Remove.
type ORSet struct {
	adds   map[string]map[tag]struct{}
	tombs  map[string]map[tag]struct{}
	clocks map[ReplicaID]uint64
}

// NewORSet returns an empty observed-remove set.
func NewORSet() *ORSet {
	return &ORSet{
		adds:   make(map[string]map[tag]struct{}),
		tombs:  make(map[string]map[tag]struct{}),
		clocks: make(map[ReplicaID]uint64),
	}
}

// Add inserts element on behalf of id, minting a fresh unique tag.
func (s *ORSet) Add(element string, id ReplicaID) {
	s.clocks[id]++
	t := tag{Replica: id, Seq: s.clocks[id]}
	if s.adds[element] == nil {
		s.adds[element] = make(map[tag]struct{})
	}
	s.adds[element][t] = struct{}{}
}

// Remove tombstones every add-tag for element that this replica has currently
// observed. Tags added concurrently elsewhere (and not yet merged in) are not
// observed, so they survive — the defining OR-Set behavior.
func (s *ORSet) Remove(element string) {
	live := s.adds[element]
	if len(live) == 0 {
		return
	}
	if s.tombs[element] == nil {
		s.tombs[element] = make(map[tag]struct{})
	}
	for t := range live {
		s.tombs[element][t] = struct{}{}
	}
}

// Contains reports whether element has at least one live tag.
func (s *ORSet) Contains(element string) bool {
	live := s.adds[element]
	if len(live) == 0 {
		return false
	}
	tombs := s.tombs[element]
	for t := range live {
		if _, dead := tombs[t]; !dead {
			return true
		}
	}
	return false
}

// Elements returns the sorted-free slice of currently present elements.
func (s *ORSet) Elements() []string {
	var out []string
	for el := range s.adds {
		if s.Contains(el) {
			out = append(out, el)
		}
	}
	return out
}

// Clone returns a deep copy.
func (s *ORSet) Clone() *ORSet {
	out := NewORSet()
	for el, tags := range s.adds {
		cp := make(map[tag]struct{}, len(tags))
		for t := range tags {
			cp[t] = struct{}{}
		}
		out.adds[el] = cp
	}
	for el, tags := range s.tombs {
		cp := make(map[tag]struct{}, len(tags))
		for t := range tags {
			cp[t] = struct{}{}
		}
		out.tombs[el] = cp
	}
	for id, c := range s.clocks {
		out.clocks[id] = c
	}
	return out
}

// Merge unions add-tags and tombstone-tags element-wise and takes the
// per-replica max of the observed clocks. Set union is commutative,
// associative, and idempotent, so the merged set is a valid CvRDT.
func (s *ORSet) Merge(other *ORSet) *ORSet {
	out := s.Clone()
	for el, tags := range other.adds {
		if out.adds[el] == nil {
			out.adds[el] = make(map[tag]struct{})
		}
		for t := range tags {
			out.adds[el][t] = struct{}{}
		}
	}
	for el, tags := range other.tombs {
		if out.tombs[el] == nil {
			out.tombs[el] = make(map[tag]struct{})
		}
		for t := range tags {
			out.tombs[el][t] = struct{}{}
		}
	}
	for id, c := range other.clocks {
		if c > out.clocks[id] {
			out.clocks[id] = c
		}
	}
	return out
}

// Equal reports whether two sets carry identical add/tombstone tag state.
func (s *ORSet) Equal(other *ORSet) bool {
	if !sameTagMap(s.adds, other.adds) || !sameTagMap(s.tombs, other.tombs) {
		return false
	}
	return true
}

func sameTagMap(a, b map[string]map[tag]struct{}) bool {
	live := func(m map[string]map[tag]struct{}) map[string]map[tag]struct{} {
		out := make(map[string]map[tag]struct{})
		for el, tags := range m {
			if len(tags) > 0 {
				out[el] = tags
			}
		}
		return out
	}
	a, b = live(a), live(b)
	if len(a) != len(b) {
		return false
	}
	for el, at := range a {
		bt, ok := b[el]
		if !ok || len(at) != len(bt) {
			return false
		}
		for t := range at {
			if _, ok := bt[t]; !ok {
				return false
			}
		}
	}
	return true
}
