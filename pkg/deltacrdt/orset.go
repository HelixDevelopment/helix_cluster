package deltacrdt

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

// ─────────────────────────────────────────────────────────────────────────────
// ORSet — observed-remove set (ORSWOT — observed-remove set without tombstone
// resurrection; here implemented with an explicit per-element tombstone set).
//
// Each Add generates a unique *tag* (a deterministic hash of nodeID + element +
// a monotonically-increasing per-node sequence number). The per-element state is
// TWO tag sets: the add-tags it has observed and the removed-tags (tombstones)
// it has observed. An element is PRESENT iff it has at least one add-tag that is
// NOT in the removed-tags set:  present(e) ⇔ (adds(e) \ removes(e)) ≠ ∅.
//
// Remove(e) records — as durable tombstones — the specific tags it observed for
// e. Crucially these tombstones are kept even if the corresponding Add has not
// yet been delivered to a peer, so a Remove delivered BEFORE its target Add does
// NOT become a silent no-op: when the Add later arrives its tag is already
// tombstoned and is therefore suppressed (it never re-introduces the element).
// New adds carry NEW, never-before-seen tags that are absent from the tombstone
// set and therefore survive — preserving add-wins on a genuinely concurrent
// add/remove, and preserving legitimate RE-ADD after remove (a fresh op yields a
// fresh tag).
//
// Convergence rule (a join on the semilattice of (adds, removes) tag-set pairs):
//   - Merge of Add delta:    adds(e) ← adds(e) ∪ delta.added(e)
//   - Merge of Remove delta: removes(e) ← removes(e) ∪ delta.removed(e)
//
// Both operations are set unions, so Merge is commutative, associative, and
// idempotent regardless of delivery order or duplicate delivery — including the
// remove-before-add case. (TestORSetRemoveBeforeAddConverges pins this.)
//
// TOMBSTONE GROWTH TRADEOFF: the removed-tags set is monotonically growing and
// is never garbage-collected here. That is the standard correctness-first
// tradeoff for a tombstone OR-Set; a production deployment would add causal-
// context compaction (e.g. an ORSWOT version vector that lets a dotted-version
// vector subsume individual tombstones once all replicas have observed them).
// We deliberately do NOT implement that compaction here — correctness first,
// without over-engineering — and call out the unbounded-growth caveat honestly.
// ─────────────────────────────────────────────────────────────────────────────

// tag is an opaque unique identifier for a single Add operation.
type tag string

// elementState holds the observed add-tags and the observed removed-tags
// (tombstones) for a single element.
type elementState struct {
	adds    map[tag]struct{}
	removes map[tag]struct{}
}

// live reports whether at least one add-tag is not tombstoned.
func (s *elementState) live() bool {
	for t := range s.adds {
		if _, removed := s.removes[t]; !removed {
			return true
		}
	}
	return false
}

// liveTags returns the set of add-tags not cancelled by a tombstone.
func (s *elementState) liveTags() map[tag]struct{} {
	out := make(map[tag]struct{}, len(s.adds))
	for t := range s.adds {
		if _, removed := s.removes[t]; !removed {
			out[t] = struct{}{}
		}
	}
	return out
}

// ORSet is an observed-remove set whose elements are strings.
type ORSet struct {
	nodeID string
	seq    uint64 // monotonic per-node sequence; deterministic, never time.Now()
	// elements maps element → its (add-tags, removed-tags) state.
	elements map[string]*elementState
}

// ORSetDelta represents a small delta produced by Add or Remove.
type ORSetDelta struct {
	// added maps element → tags that were added in this delta.
	added map[string]map[tag]struct{}
	// removed maps element → tags that were observed-removed in this delta.
	removed map[string]map[tag]struct{}
}

// NewORSet returns an empty ORSet for nodeID.
func NewORSet(nodeID string) *ORSet {
	return &ORSet{
		nodeID:   nodeID,
		elements: make(map[string]*elementState),
	}
}

// stateFor returns (creating if necessary) the elementState for elem.
func (o *ORSet) stateFor(elem string) *elementState {
	s := o.elements[elem]
	if s == nil {
		s = &elementState{
			adds:    make(map[tag]struct{}),
			removes: make(map[tag]struct{}),
		}
		o.elements[elem] = s
	}
	return s
}

// makeTag builds a deterministic tag from (nodeID, element, seq).
// Using sha256 keeps tags compact and collision-resistant. The payload is
// constructed with fmt.Sprintf (provably infallible for these types) rather
// than json.Marshal so there is no error to discard — a silent marshal failure
// would produce a constant hash and collide all tags.
func makeTag(nodeID, elem string, seq uint64) tag {
	h := sha256.New()
	// %q quotes strings (escapes special chars); %d is exact for uint64.
	// This format is injective: distinct (nodeID, elem, seq) triples produce
	// distinct payloads.
	h.Write([]byte(fmt.Sprintf("%q\x00%q\x00%d", nodeID, elem, seq)))
	return tag(fmt.Sprintf("%x", h.Sum(nil)))
}

// Add adds elem to the set, returning the delta. The tag is deterministic given
// (nodeID, elem, seq) so tests with a fixed seq are fully reproducible.
//
// Because every Add increments the per-node seq, each Add produces a FRESH tag
// that has never appeared in any tombstone set. This is what makes legitimate
// RE-ADD after a remove work: re-adding an element generates a new tag not in
// removes(e), so present(e) becomes true again (add-wins re-add).
func (o *ORSet) Add(elem string) ORSetDelta {
	o.seq++
	t := makeTag(o.nodeID, elem, o.seq)
	s := o.stateFor(elem)
	s.adds[t] = struct{}{}
	return ORSetDelta{
		added:   map[string]map[tag]struct{}{elem: {t: {}}},
		removed: nil,
	}
}

// Remove removes elem from the set by tombstoning all currently-LIVE tags for
// elem on this replica. Returns the delta. If elem has no live tags the delta is
// empty (no-op). The observed live tags are recorded durably as tombstones both
// locally and in the delta, so that the removal converges even when delivered
// before the add it cancels.
func (o *ORSet) Remove(elem string) ORSetDelta {
	s := o.elements[elem]
	if s == nil {
		return ORSetDelta{}
	}
	live := s.liveTags()
	if len(live) == 0 {
		return ORSetDelta{}
	}
	// Tombstone the observed live tags durably (do NOT delete the add-tags;
	// they must stay so idempotent re-delivery and structural convergence hold).
	for t := range live {
		s.removes[t] = struct{}{}
	}
	return ORSetDelta{
		added:   nil,
		removed: map[string]map[tag]struct{}{elem: live},
	}
}

// Merge integrates a delta into the receiver.
//
// Both halves are pure set unions on the per-element (adds, removes) tag sets,
// making Merge commutative, associative, and idempotent. A Remove delta's tags
// are unioned into removes(e) UNCONDITIONALLY — even if the corresponding add is
// not yet present — so a remove delivered before its add is durably recorded and
// suppresses that add when it later arrives (no resurrection). This is the fix
// for the former remove-before-add divergence (the old code deleted tags only if
// locally present, silently dropping early removes).
func (o *ORSet) Merge(d ORSetDelta) {
	// Integrate additions: union add-tags. A tag already tombstoned in
	// removes(e) stays suppressed (present() filters it out), so an add whose
	// tag was previously removed does NOT resurrect the element.
	for elem, newTags := range d.added {
		if len(newTags) == 0 {
			continue
		}
		s := o.stateFor(elem)
		for t := range newTags {
			s.adds[t] = struct{}{}
		}
	}

	// Integrate removals: union tombstones unconditionally (observed-remove,
	// durable — the SEC-critical change from the previous intersection guard).
	for elem, removedTags := range d.removed {
		if len(removedTags) == 0 {
			continue
		}
		s := o.stateFor(elem)
		for t := range removedTags {
			s.removes[t] = struct{}{}
		}
	}
}

// Contains reports whether elem is in the set (has ≥1 non-tombstoned add-tag).
func (o *ORSet) Contains(elem string) bool {
	s := o.elements[elem]
	return s != nil && s.live()
}

// Elements returns a sorted slice of all elements currently in the set.
func (o *ORSet) Elements() []string {
	out := make([]string, 0, len(o.elements))
	for elem, s := range o.elements {
		if s.live() {
			out = append(out, elem)
		}
	}
	sort.Strings(out)
	return out
}

// State returns a full-state delta for convergence tests. It materialises the
// LIVE tag set per present element (add-tags minus tombstones). Two replicas
// that have observed the same delta multiset therefore have structurally equal
// State() regardless of delivery order — the strong-eventual-consistency
// guarantee that orsetStateEqual checks.
func (o *ORSet) State() ORSetDelta {
	added := make(map[string]map[tag]struct{}, len(o.elements))
	for elem, s := range o.elements {
		live := s.liveTags()
		if len(live) == 0 {
			continue
		}
		added[elem] = live
	}
	return ORSetDelta{added: added}
}

// orsetStateEqual reports whether two full-state ORSetDeltas represent the same set.
func orsetStateEqual(a, b ORSetDelta) bool {
	if len(a.added) != len(b.added) {
		return false
	}
	for elem, aTags := range a.added {
		bTags, ok := b.added[elem]
		if !ok {
			return false
		}
		if len(aTags) != len(bTags) {
			return false
		}
		for t := range aTags {
			if _, ok := bTags[t]; !ok {
				return false
			}
		}
	}
	return true
}
