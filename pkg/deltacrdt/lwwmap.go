package deltacrdt

import "sort"

// ─────────────────────────────────────────────────────────────────────────────
// LWWMap — last-writer-wins map.
//
// Each key is associated with a value and a logical timestamp (uint64).
// The replica with the higher timestamp wins on merge; ties are broken by
// nodeID (lexicographically larger nodeID wins) for determinism.
// Deletes are represented as tombstones (value = "", tombstone = true) with a
// timestamp that must be greater than the last write timestamp to take effect.
//
// Delta: only the modified key(s) are included.
// ─────────────────────────────────────────────────────────────────────────────

// lwwEntry is a single map entry with its logical timestamp and writer identity.
type lwwEntry struct {
	value     string
	ts        uint64
	nodeID    string
	tombstone bool
}

// LWWMap is a last-writer-wins map with string keys and string values.
type LWWMap struct {
	nodeID  string
	entries map[string]lwwEntry
}

// LWWMapDelta is the delta emitted by Put or Delete.
type LWWMapDelta struct {
	entries map[string]lwwEntry
}

// NewLWWMap returns an empty LWWMap for nodeID.
func NewLWWMap(nodeID string) *LWWMap {
	return &LWWMap{nodeID: nodeID, entries: make(map[string]lwwEntry)}
}

// Put sets key → value at the given logical timestamp ts and returns the delta.
// The caller controls the clock (injected ts) — no time.Now().
func (m *LWWMap) Put(key, value string, ts uint64) LWWMapDelta {
	e := lwwEntry{value: value, ts: ts, nodeID: m.nodeID}
	m.entries[key] = e
	return LWWMapDelta{entries: map[string]lwwEntry{key: e}}
}

// Delete removes key at logical timestamp ts and returns the delta.
func (m *LWWMap) Delete(key string, ts uint64) LWWMapDelta {
	e := lwwEntry{value: "", ts: ts, nodeID: m.nodeID, tombstone: true}
	m.entries[key] = e
	return LWWMapDelta{entries: map[string]lwwEntry{key: e}}
}

// lwwWins reports whether candidate beats incumbent.
// Precedence (highest first):
//  1. Higher timestamp wins.
//  2. Equal timestamp + equal nodeID: tombstone beats a live entry (a node deleting
//     its own key at the same ts it wrote it must converge to "deleted" on all peers).
//  3. Equal timestamp + different nodeID: lexicographically larger nodeID wins.
//
// MUTATION GUARD — TestLWWMapSameTsDeleteConverges pins rule (2):
// removing the tombstone-priority branch causes a divergence where the originating
// replica sees the key absent but peers that received both deltas see it present.
func lwwWins(candidate, incumbent lwwEntry) bool {
	if candidate.ts != incumbent.ts {
		return candidate.ts > incumbent.ts
	}
	if candidate.nodeID == incumbent.nodeID {
		// Same node, same timestamp: tombstone wins over live entry. ← mutation-killable
		return candidate.tombstone && !incumbent.tombstone
	}
	return candidate.nodeID > incumbent.nodeID
}

// Merge integrates a LWWMapDelta into m. For each key the winning entry
// (higher timestamp; tie broken by nodeID) is kept.
func (m *LWWMap) Merge(d LWWMapDelta) {
	for key, incoming := range d.entries {
		existing, ok := m.entries[key]
		if !ok || lwwWins(incoming, existing) {
			m.entries[key] = incoming
		}
	}
}

// Get returns the value and whether the key is present (and not tombstoned).
func (m *LWWMap) Get(key string) (string, bool) {
	e, ok := m.entries[key]
	if !ok || e.tombstone {
		return "", false
	}
	return e.value, true
}

// Keys returns a sorted slice of all live (non-tombstoned) keys.
func (m *LWWMap) Keys() []string {
	out := make([]string, 0, len(m.entries))
	for k, e := range m.entries {
		if !e.tombstone {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// State returns a full-state delta for convergence tests.
func (m *LWWMap) State() LWWMapDelta {
	cp := make(map[string]lwwEntry, len(m.entries))
	for k, v := range m.entries {
		cp[k] = v
	}
	return LWWMapDelta{entries: cp}
}

// lwwmapEqual reports whether two full-state LWWMapDeltas are identical.
func lwwmapEqual(a, b LWWMapDelta) bool {
	if len(a.entries) != len(b.entries) {
		return false
	}
	for k, av := range a.entries {
		bv, ok := b.entries[k]
		if !ok {
			return false
		}
		if av != bv {
			return false
		}
	}
	return true
}
