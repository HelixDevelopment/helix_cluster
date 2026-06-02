// Package watchmanager implements an etcd-style persistent watch manager with
// synced / unsynced / victim watcher groups.
//
// Model: every mutation advances a monotonic revision and is appended to an
// in-memory revision-ordered event store. A watcher registers on a key prefix
// from a starting revision R.
//
//   - SYNCED watchers are caught up to the current revision and receive new
//     matching events live as Put advances the revision.
//   - UNSYNCED watchers are behind the current revision (either because they
//     started from a historical R, or because a live fan-out could not be
//     delivered into their bounded buffer). They catch up by replaying
//     historical events during a Sync() pass.
//   - VICTIM watchers are unsynced watchers whose delivery buffer overflowed
//     mid-replay; they are still owed events and are retried on the next
//     Sync() pass.
//
// The package is pure Go and deterministic: it never calls time.Now in logic.
// All state (event store, watchers, delivery buffers) is modeled with in-memory
// structures. A Clock interface is provided for callers that want to stamp
// events with a logical tick, but the watch/sync logic is driven purely by
// revisions.
package watchmanager

import (
	"sort"
	"strings"
	"sync"
)

// Group classifies which watcher group a watcher currently belongs to.
type Group int

const (
	// GroupSynced means the watcher is caught up to the current revision and
	// receives new matching events live.
	GroupSynced Group = iota
	// GroupUnsynced means the watcher is behind and must replay historical
	// events to catch up.
	GroupUnsynced
	// GroupVictim means the watcher overflowed its buffer during catch-up and
	// is still owed events; it is retried on the next Sync pass.
	GroupVictim
)

func (g Group) String() string {
	switch g {
	case GroupSynced:
		return "synced"
	case GroupUnsynced:
		return "unsynced"
	case GroupVictim:
		return "victim"
	default:
		return "unknown"
	}
}

// Clock provides a logical tick. It exists so callers may stamp events without
// using wall-clock time. The watch/sync logic does not depend on it.
type Clock interface {
	Tick() int64
}

// LogicalClock is a deterministic monotonic logical clock.
type LogicalClock struct {
	mu  sync.Mutex
	now int64
}

// Tick advances and returns the logical time.
func (c *LogicalClock) Tick() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now++
	return c.now
}

// Event is a single revision in the store.
type Event struct {
	Revision int64
	Key      string
	Value    string
	Tick     int64 // logical timestamp, informational only
}

// Watcher is a registered watch on a key prefix. Events are delivered through
// the C channel; callers receive in revision order. A Watcher with a buffer
// that fills up will be moved out of the synced group.
type Watcher struct {
	ID     int64
	Prefix string
	// nextRev is the next revision this watcher still needs to receive. It is
	// strictly the revision after the last one successfully delivered (or the
	// requested fromRev if nothing has been delivered yet).
	nextRev int64
	bufSize int
	c       chan Event
	group   Group
}

// C returns the delivery channel for received events. Drain it to receive
// events in revision order.
func (w *Watcher) C() <-chan Event { return w.c }

// Manager is an etcd-style watch manager over an in-memory revision store.
type Manager struct {
	mu sync.Mutex

	clock Clock

	currentRev int64
	store      []Event // append-only, sorted by Revision ascending

	nextWatcherID int64
	synced        map[int64]*Watcher
	unsynced      map[int64]*Watcher
	victim        map[int64]*Watcher
}

// New constructs an empty Manager. If clk is nil a LogicalClock is used.
func New(clk Clock) *Manager {
	if clk == nil {
		clk = &LogicalClock{}
	}
	return &Manager{
		clock:    clk,
		synced:   make(map[int64]*Watcher),
		unsynced: make(map[int64]*Watcher),
		victim:   make(map[int64]*Watcher),
	}
}

// CurrentRevision returns the latest committed revision (0 if no writes yet).
func (m *Manager) CurrentRevision() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentRev
}

// Put advances the revision by one, appends the event to the store, and fans
// the event out live to every synced watcher whose prefix matches. If a synced
// watcher's bounded buffer is full, the delivery fails and the watcher is moved
// to the unsynced group (it will catch up via Sync); its nextRev is preserved
// so no event is lost.
func (m *Manager) Put(key, value string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.currentRev++
	rev := m.currentRev
	ev := Event{Revision: rev, Key: key, Value: value, Tick: m.clock.Tick()}
	m.store = append(m.store, ev)

	for id, w := range m.synced {
		if !strings.HasPrefix(key, w.Prefix) {
			// Prefix filter: non-matching keys are not delivered, but the
			// watcher remains caught up THROUGH this revision so it does not
			// needlessly re-replay. Advance its cursor past this rev.
			w.nextRev = rev + 1
			continue
		}
		// Watcher needs this revision.
		if !m.trySend(w, ev) {
			// Buffer full: demote to unsynced WITHOUT advancing nextRev, so the
			// event is replayed later. This is the overflow->unsynced move.
			w.group = GroupUnsynced
			delete(m.synced, id)
			m.unsynced[id] = w
			continue
		}
		w.nextRev = rev + 1
	}
	return rev
}

// trySend performs a non-blocking send. Returns false if the buffer is full.
func (m *Manager) trySend(w *Watcher, ev Event) bool {
	select {
	case w.c <- ev:
		return true
	default:
		return false
	}
}

// Watch registers a watcher on the given key prefix starting from fromRev
// (inclusive). bufSize sets the delivery channel capacity (minimum 1).
//
// If fromRev is at or beyond the next revision (i.e. the watcher only wants
// future events), it starts in the synced group. Otherwise it has historical
// events to replay and starts unsynced; call Sync to replay them.
func (m *Manager) Watch(prefix string, fromRev int64, bufSize int) *Watcher {
	m.mu.Lock()
	defer m.mu.Unlock()

	if bufSize < 1 {
		bufSize = 1
	}
	if fromRev < 1 {
		fromRev = 1
	}
	m.nextWatcherID++
	w := &Watcher{
		ID:      m.nextWatcherID,
		Prefix:  prefix,
		nextRev: fromRev,
		bufSize: bufSize,
		c:       make(chan Event, bufSize),
	}

	// A watcher whose starting revision is already past the current revision
	// has nothing historical to replay -> synced. Otherwise -> unsynced.
	if fromRev > m.currentRev {
		w.group = GroupSynced
		m.synced[w.ID] = w
	} else {
		w.group = GroupUnsynced
		m.unsynced[w.ID] = w
	}
	return w
}

// GroupOf reports which group the watcher currently belongs to.
func (m *Manager) GroupOf(w *Watcher) Group {
	m.mu.Lock()
	defer m.mu.Unlock()
	return w.group
}

// Counts returns the number of watchers in each group (synced, unsynced, victim).
func (m *Manager) Counts() (synced, unsynced, victim int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.synced), len(m.unsynced), len(m.victim)
}

// Sync performs one syncing pass: for every unsynced and victim watcher it
// replays the historical events it still needs (revision >= nextRev, matching
// prefix) into its buffer, in revision order. A watcher that fully catches up
// to the current revision is promoted to synced. A watcher whose buffer fills
// mid-replay is classified as a victim (still owed events) and remains for the
// next pass; whatever it managed to receive is retained (no loss, no
// duplication) because nextRev only advances on successful delivery.
//
// Sync returns the total number of events delivered during this pass.
func (m *Manager) Sync() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	delivered := 0

	// Process unsynced and victim together; collect ids deterministically.
	ids := make([]int64, 0, len(m.unsynced)+len(m.victim))
	for id := range m.unsynced {
		ids = append(ids, id)
	}
	for id := range m.victim {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for _, id := range ids {
		w := m.unsynced[id]
		if w == nil {
			w = m.victim[id]
		}
		if w == nil {
			continue
		}

		overflowed := false
		// Replay store events from nextRev forward in revision order.
		for i := m.indexAtOrAfter(w.nextRev); i < len(m.store); i++ {
			ev := m.store[i]
			if !strings.HasPrefix(ev.Key, w.Prefix) {
				// Skip non-matching; advance cursor so we do not revisit it.
				w.nextRev = ev.Revision + 1
				continue
			}
			if !m.trySend(w, ev) {
				overflowed = true
				break
			}
			delivered++
			w.nextRev = ev.Revision + 1
		}

		// Remove from both maps; re-classify below.
		delete(m.unsynced, id)
		delete(m.victim, id)

		switch {
		case overflowed:
			w.group = GroupVictim
			m.victim[id] = w
		case w.nextRev > m.currentRev:
			w.group = GroupSynced
			m.synced[id] = w
		default:
			// Caught some but the store grew concurrently is impossible under
			// the lock; if still behind, keep as unsynced for next pass.
			w.group = GroupUnsynced
			m.unsynced[id] = w
		}
	}
	return delivered
}

// indexAtOrAfter returns the index of the first store entry whose Revision is
// >= rev. The store is append-only and sorted by revision, so this is a binary
// search. Returns len(store) if no such entry exists.
func (m *Manager) indexAtOrAfter(rev int64) int {
	return sort.Search(len(m.store), func(i int) bool {
		return m.store[i].Revision >= rev
	})
}
