// Package mvcc implements a multi-version concurrency control (MVCC) revision
// store with a real in-memory B-tree key index and time-travel reads.
//
// The store keeps a monotonically increasing global revision counter. Every Put
// produces a new global revision and appends a version (modRev, value) to the
// targeted key's history. Reads are time-travel reads: Get(key, atRev) returns
// the value of key as of revision atRev (the latest version whose modRev is
// <= atRev). The primary key index is an ordered B-tree (not a Go map), so keys
// can be iterated in sorted order via Range. Watch replays all change events in
// revision order, and Compact prunes superseded historical versions.
//
// The package is pure Go, deterministic, and uses no clock, network, or host
// facilities; the global revision counter is the only notion of "time".
package mvcc

import "sort"

// Version is one historical value of a key, stamped with the global revision at
// which the Put that created it occurred.
type Version struct {
	ModRev int64
	Value  []byte
}

// Event is a recorded change in the revision log. Watch replays these.
type Event struct {
	Rev   int64
	Key   string
	Value []byte
}

// keyHistory holds the full ordered version history for a single key. Versions
// are kept sorted ascending by ModRev (which is naturally the case since Put
// assigns strictly increasing revisions).
type keyHistory struct {
	versions []Version
}

// get returns the value as of atRev: the latest version whose ModRev <= atRev.
// If atRev <= 0 the latest version is returned. found is false when no version
// qualifies (e.g. the key did not yet exist at atRev, or all qualifying
// versions were compacted away).
func (h *keyHistory) get(atRev int64) ([]byte, bool) {
	if len(h.versions) == 0 {
		return nil, false
	}
	if atRev <= 0 {
		v := h.versions[len(h.versions)-1]
		return v.Value, true
	}
	// Find rightmost version with ModRev <= atRev via binary search.
	// sort.Search finds the first index where ModRev > atRev; the one before
	// it is our answer.
	idx := sort.Search(len(h.versions), func(i int) bool {
		return h.versions[i].ModRev > atRev
	})
	if idx == 0 {
		return nil, false
	}
	v := h.versions[idx-1]
	return v.Value, true
}

// compact drops superseded historical versions with ModRev < rev. A version is
// "superseded" if a later version (higher ModRev) exists; the most recent
// version is always retained regardless of rev so the latest value survives.
func (h *keyHistory) compact(rev int64) {
	if len(h.versions) <= 1 {
		return
	}
	kept := h.versions[:0:0] // new backing array; do not alias old slice
	for i, v := range h.versions {
		isLatest := i == len(h.versions)-1
		superseded := !isLatest
		if superseded && v.ModRev < rev {
			continue // prune
		}
		kept = append(kept, v)
	}
	h.versions = kept
}

// Store is an MVCC revision store. It is not safe for concurrent use; callers
// requiring concurrency should serialize access externally. Determinism (the
// requirement here) is preserved by avoiding any wall-clock or map-iteration
// dependence in observable output.
type Store struct {
	rev   int64
	index *btree
	log   []Event
}

// New creates an empty Store with global revision 0.
func New() *Store {
	return &Store{
		index: newBtree(defaultDegree),
	}
}

// Rev returns the current (most recent) global revision.
func (s *Store) Rev() int64 {
	return s.rev
}

// Put records a new version of key with val and returns the new global
// revision. The first Put returns revision 1.
func (s *Store) Put(key string, val []byte) int64 {
	s.rev++
	rev := s.rev
	// Copy val so later caller mutation cannot corrupt stored history.
	stored := make([]byte, len(val))
	copy(stored, val)

	h := s.index.get(key)
	if h == nil {
		h = &keyHistory{}
		s.index.insert(key, h)
	}
	h.versions = append(h.versions, Version{ModRev: rev, Value: stored})

	s.log = append(s.log, Event{Rev: rev, Key: key, Value: stored})
	return rev
}

// Get returns the value of key as of revision atRev. atRev <= 0 means latest.
func (s *Store) Get(key string, atRev int64) ([]byte, bool) {
	h := s.index.get(key)
	if h == nil {
		return nil, false
	}
	return h.get(atRev)
}

// Range returns the keys in the closed-open interval [start, end) in sorted
// order. If end is the empty string, the range is unbounded above and all keys
// >= start are returned.
func (s *Store) Range(start, end string) []string {
	return s.index.rangeKeys(start, end)
}

// Watch replays every change event with Rev > fromRev, in ascending revision
// order, so a watcher resuming from an old revision sees all intervening Puts.
func (s *Store) Watch(fromRev int64) []Event {
	out := make([]Event, 0)
	for _, e := range s.log {
		if e.Rev > fromRev {
			out = append(out, e)
		}
	}
	return out
}

// Compact drops superseded historical versions with ModRev < rev across all
// keys, so pre-compaction revisions are no longer retrievable via Get for those
// superseded versions, while each key's latest value and any post-compaction
// history remain intact.
func (s *Store) Compact(rev int64) {
	for _, h := range s.index.allHistories() {
		h.compact(rev)
	}
}
