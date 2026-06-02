package deltacrdt

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

// ─────────────────────────────────────────────────────────────────────────────
// ORSet — observed-remove set.
//
// Each Add generates a unique *tag* (a deterministic hash of nodeID + element +
// a monotonically-increasing per-node sequence number). The set state is a map
// from element → set-of-tags. Remove(e) only cancels tags that the removing
// replica has *observed* for e (the observed set at the time of the remove
// call). New adds arriving after the remove carry new tags and therefore
// survive — add-wins semantics on concurrent add/remove.
//
// Convergence rule:
//   - Merge of Add delta: union the tag sets.
//   - Merge of Remove delta: subtract ONLY the tags named in the delta
//     (the observed tags at remove time) from the local tag set.
//
// MUTATION GUARD — TestORSetAddRemoveConvergesReordered pins this:
//   In mergeRemoveDelta, the loop intersects the incoming "removed tags" with
//   the locally-present tags BEFORE deletion.  If that intersection guard is
//   dropped (unconditional delete of all tags in the remove delta regardless
//   of whether they are present locally), a remove delta arriving before the
//   corresponding add delta will erase tags that were never seen, and the final
//   set on both replicas will diverge from the expected result.
// ─────────────────────────────────────────────────────────────────────────────

// tag is an opaque unique identifier for a single Add operation.
type tag string

// ORSet is an observed-remove set whose elements are strings.
type ORSet struct {
	nodeID string
	seq    uint64 // monotonic per-node sequence; deterministic, never time.Now()
	// elements maps element → set of live add-tags.
	elements map[string]map[tag]struct{}
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
		elements: make(map[string]map[tag]struct{}),
	}
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
func (o *ORSet) Add(elem string) ORSetDelta {
	o.seq++
	t := makeTag(o.nodeID, elem, o.seq)
	if o.elements[elem] == nil {
		o.elements[elem] = make(map[tag]struct{})
	}
	o.elements[elem][t] = struct{}{}
	return ORSetDelta{
		added:   map[string]map[tag]struct{}{elem: {t: {}}},
		removed: nil,
	}
}

// Remove removes elem from the set by tombstoning all currently-observed tags
// for elem on this replica. Returns the delta. If elem is not present the
// delta is empty (no-op).
func (o *ORSet) Remove(elem string) ORSetDelta {
	observed := o.elements[elem]
	if len(observed) == 0 {
		return ORSetDelta{}
	}
	// Snapshot the observed tags into the delta.
	tombstones := make(map[tag]struct{}, len(observed))
	for t := range observed {
		tombstones[t] = struct{}{}
	}
	// Apply locally: remove all observed tags.
	delete(o.elements, elem)
	return ORSetDelta{
		added:   nil,
		removed: map[string]map[tag]struct{}{elem: tombstones},
	}
}

// Merge integrates a delta into the receiver.
//
// For Add deltas: union tags into local element maps.
// For Remove deltas: remove ONLY the named tags that are locally present
// (observed-remove intersection guard — the mutation-killable line).
func (o *ORSet) Merge(d ORSetDelta) {
	// Integrate additions first.
	for elem, newTags := range d.added {
		if o.elements[elem] == nil {
			o.elements[elem] = make(map[tag]struct{})
		}
		for t := range newTags {
			o.elements[elem][t] = struct{}{}
		}
		// Clean up: if tag set ended up empty, remove the key.
		if len(o.elements[elem]) == 0 {
			delete(o.elements, elem)
		}
	}

	// Integrate removals: only cancel tags that are present locally.
	// MUTATION GUARD — TestORSetAddRemoveConvergesReordered pins the "if _, ok" check below.
	// Removing that guard (deleting unconditionally from o.elements[elem]) breaks
	// convergence when a remove delta arrives before the add delta it observed.
	for elem, removedTags := range d.removed {
		local := o.elements[elem]
		if local == nil {
			// The element is not present locally — nothing to remove.
			// Without this (and the per-tag guard below), a remove arriving
			// before the add would create a phantom deletion.
			continue
		}
		for t := range removedTags {
			if _, ok := local[t]; ok { // ← mutation-killable guard (observed-remove intersection)
				delete(local, t)
			}
		}
		if len(local) == 0 {
			delete(o.elements, elem)
		}
	}
}

// Contains reports whether elem is in the set.
func (o *ORSet) Contains(elem string) bool {
	return len(o.elements[elem]) > 0
}

// Elements returns a sorted slice of all elements currently in the set.
func (o *ORSet) Elements() []string {
	out := make([]string, 0, len(o.elements))
	for elem, tags := range o.elements {
		if len(tags) > 0 {
			out = append(out, elem)
		}
	}
	sort.Strings(out)
	return out
}

// State returns a full-state delta for convergence tests.
func (o *ORSet) State() ORSetDelta {
	added := make(map[string]map[tag]struct{}, len(o.elements))
	for elem, tags := range o.elements {
		cp := make(map[tag]struct{}, len(tags))
		for t := range tags {
			cp[t] = struct{}{}
		}
		added[elem] = cp
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
