// Package informer provides an Informer-style list-watch local cache with
// indexers and resync, modeled after the Kubernetes informer/Store/Indexer
// pattern but kept pure-Go, deterministic, and self-contained.
//
// A local thread-safe Store (cache) is seeded by an initial List() from a
// DeltaSource and then kept in sync by a stream of Watch deltas (Add/Update/
// Delete events). An Indexer maintains secondary indexes via a user-supplied
// IndexFunc so callers can look objects up by index value. Resync re-delivers
// the current cache state to handlers.
//
// The package performs NO network or host I/O: all input arrives through the
// DeltaSource interface, which tests satisfy with an in-memory fake.
package informer

import (
	"sort"
	"sync"
)

// Object is a cached item with a stable identity given by Namespace/Name.
type Object struct {
	Namespace string
	Name      string
	// Value is an opaque payload the index functions and handlers inspect.
	// Two Objects with the same Key but different Value represent an update.
	Value string
	// Labels carry secondary attributes indexers may key on.
	Labels map[string]string
}

// Key returns the stable cache key "Namespace/Name". Empty namespace yields
// just the name so cluster-scoped objects key cleanly.
func (o Object) Key() string {
	if o.Namespace == "" {
		return o.Name
	}
	return o.Namespace + "/" + o.Name
}

// EventType enumerates the kinds of watch deltas.
type EventType int

const (
	// Added means the object newly appeared.
	Added EventType = iota
	// Updated means an existing object changed.
	Updated
	// Deleted means the object was removed.
	Deleted
)

func (e EventType) String() string {
	switch e {
	case Added:
		return "Added"
	case Updated:
		return "Updated"
	case Deleted:
		return "Deleted"
	default:
		return "Unknown"
	}
}

// Event is a single watch delta carrying the affected object.
type Event struct {
	Type   EventType
	Object Object
}

// DeltaSource supplies the initial snapshot via List and a continuous stream
// of deltas via Watch. Implementations are responsible for closing the Watch
// channel when no more events will arrive.
type DeltaSource interface {
	// List returns the full current state used to seed the cache.
	List() []Object
	// Watch returns a channel of deltas to apply after the initial List.
	Watch() <-chan Event
}

// IndexFunc maps an object to zero or more index values (buckets). An object
// is reachable via ByIndex(name, v) for every v this returns.
type IndexFunc func(o Object) []string

// ResourceEventHandler receives cache mutations. Old is the prior state on
// update (zero Object otherwise).
type ResourceEventHandler struct {
	OnAdd    func(o Object)
	OnUpdate func(old, new Object)
	OnDelete func(o Object)
}

// Store is the thread-safe local cache plus its secondary indexes.
type Store struct {
	mu      sync.RWMutex
	items   map[string]Object
	indexes map[string]IndexFunc
	// indexed[name][value] -> set of object keys
	indexed map[string]map[string]map[string]struct{}
}

// NewStore returns an empty Store ready for indexers and deltas.
func NewStore() *Store {
	return &Store{
		items:   make(map[string]Object),
		indexes: make(map[string]IndexFunc),
		indexed: make(map[string]map[string]map[string]struct{}),
	}
}

// AddIndexer registers a named index built from fn. It (re)indexes any objects
// already present. Registering the same name twice replaces the function and
// rebuilds that index.
func (s *Store) AddIndexer(name string, fn IndexFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexes[name] = fn
	s.indexed[name] = make(map[string]map[string]struct{})
	for key, obj := range s.items {
		s.addToIndexLocked(name, fn, key, obj)
	}
}

func (s *Store) addToIndexLocked(name string, fn IndexFunc, key string, obj Object) {
	for _, v := range fn(obj) {
		bucket, ok := s.indexed[name][v]
		if !ok {
			bucket = make(map[string]struct{})
			s.indexed[name][v] = bucket
		}
		bucket[key] = struct{}{}
	}
}

func (s *Store) removeFromIndexLocked(name string, fn IndexFunc, key string, obj Object) {
	for _, v := range fn(obj) {
		bucket, ok := s.indexed[name][v]
		if !ok {
			continue
		}
		delete(bucket, key)
		if len(bucket) == 0 {
			delete(s.indexed[name], v)
		}
	}
}

// indexAllLocked adds key/obj to every registered index.
func (s *Store) indexAllLocked(key string, obj Object) {
	for name, fn := range s.indexes {
		s.addToIndexLocked(name, fn, key, obj)
	}
}

// deindexAllLocked removes key/obj from every registered index using the
// object's CURRENT (pre-mutation) value so the right buckets are cleared.
func (s *Store) deindexAllLocked(key string, obj Object) {
	for name, fn := range s.indexes {
		s.removeFromIndexLocked(name, fn, key, obj)
	}
}

// GetByKey returns the cached object for key and whether it was present.
func (s *Store) GetByKey(key string) (Object, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.items[key]
	return o, ok
}

// List returns a snapshot of all cached objects in deterministic key order.
func (s *Store) List() []Object {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.items))
	for k := range s.items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Object, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.items[k])
	}
	return out
}

// ByIndex returns all cached objects in bucket value of index name, in
// deterministic key order. Unknown index or empty bucket yields nil.
func (s *Store) ByIndex(name, value string) []Object {
	s.mu.RLock()
	defer s.mu.RUnlock()
	buckets, ok := s.indexed[name]
	if !ok {
		return nil
	}
	set, ok := buckets[value]
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Object, 0, len(keys))
	for _, k := range keys {
		if o, present := s.items[k]; present {
			out = append(out, o)
		}
	}
	return out
}

// apply mutates the cache and indexes for one delta and returns the handler
// notifications to fire (decoupled so handlers run outside the lock). It
// returns the resolved event type along with old/new objects.
func (s *Store) apply(ev Event) (effType EventType, old Object, newObj Object, fire bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := ev.Object.Key()
	switch ev.Type {
	case Added, Updated:
		prev, existed := s.items[key]
		if existed {
			// Remove stale index entries using the OLD value before reindexing.
			s.deindexAllLocked(key, prev)
		}
		s.items[key] = ev.Object
		s.indexAllLocked(key, ev.Object)
		if existed {
			return Updated, prev, ev.Object, true
		}
		return Added, Object{}, ev.Object, true
	case Deleted:
		prev, existed := s.items[key]
		if !existed {
			return Deleted, Object{}, Object{}, false
		}
		s.deindexAllLocked(key, prev)
		delete(s.items, key)
		return Deleted, prev, Object{}, true
	default:
		return ev.Type, Object{}, Object{}, false
	}
}

// Informer ties a DeltaSource to a Store and dispatches handler callbacks.
type Informer struct {
	source   DeltaSource
	store    *Store
	mu       sync.Mutex
	handlers []ResourceEventHandler
	syncedCh chan struct{}
	synced   bool
}

// NewInformer constructs an Informer over source backed by a fresh Store.
func NewInformer(source DeltaSource) *Informer {
	return &Informer{
		source:   source,
		store:    NewStore(),
		syncedCh: make(chan struct{}),
	}
}

// Store exposes the backing cache for reads and indexer registration.
func (inf *Informer) Store() *Store { return inf.store }

// AddEventHandler registers a handler. Handlers added before Run receive the
// initial-List adds; handlers added after still receive subsequent deltas and
// resyncs.
func (inf *Informer) AddEventHandler(h ResourceEventHandler) {
	inf.mu.Lock()
	defer inf.mu.Unlock()
	inf.handlers = append(inf.handlers, h)
}

func (inf *Informer) snapshotHandlers() []ResourceEventHandler {
	inf.mu.Lock()
	defer inf.mu.Unlock()
	out := make([]ResourceEventHandler, len(inf.handlers))
	copy(out, inf.handlers)
	return out
}

func (inf *Informer) dispatch(effType EventType, old, newObj Object) {
	for _, h := range inf.snapshotHandlers() {
		switch effType {
		case Added:
			if h.OnAdd != nil {
				h.OnAdd(newObj)
			}
		case Updated:
			if h.OnUpdate != nil {
				h.OnUpdate(old, newObj)
			}
		case Deleted:
			if h.OnDelete != nil {
				h.OnDelete(old)
			}
		}
	}
}

// Run seeds the cache from source.List, marks the informer synced, then
// consumes deltas from source.Watch until the watch channel closes or stop is
// closed. Run blocks; callers typically invoke it in a goroutine via Start.
func (inf *Informer) Run(stop <-chan struct{}) {
	// Initial List seeds the cache and fires OnAdd for each item.
	for _, o := range inf.source.List() {
		effType, old, newObj, fire := inf.store.apply(Event{Type: Added, Object: o})
		if fire {
			inf.dispatch(effType, old, newObj)
		}
	}
	inf.markSynced()

	watch := inf.source.Watch()
	for {
		select {
		case <-stop:
			return
		case ev, ok := <-watch:
			if !ok {
				return
			}
			effType, old, newObj, fire := inf.store.apply(ev)
			if fire {
				inf.dispatch(effType, old, newObj)
			}
		}
	}
}

// Start runs the informer in a background goroutine and returns immediately.
func (inf *Informer) Start(stop <-chan struct{}) {
	go inf.Run(stop)
}

func (inf *Informer) markSynced() {
	inf.mu.Lock()
	defer inf.mu.Unlock()
	if !inf.synced {
		inf.synced = true
		close(inf.syncedCh)
	}
}

// HasSynced reports whether the initial List has been applied.
func (inf *Informer) HasSynced() bool {
	inf.mu.Lock()
	defer inf.mu.Unlock()
	return inf.synced
}

// WaitForSync blocks until the initial List has been applied or stop fires.
// It returns true if synced, false if stop won the race.
func (inf *Informer) WaitForSync(stop <-chan struct{}) bool {
	select {
	case <-inf.syncedCh:
		return true
	case <-stop:
		return false
	}
}

// Resync re-delivers the current cache state to all handlers: OnUpdate(o, o)
// for each cached object (Kubernetes informers resync via update with the
// same object). It returns the number of objects re-emitted.
func (inf *Informer) Resync() int {
	current := inf.store.List()
	for _, o := range current {
		inf.dispatch(Updated, o, o)
	}
	return len(current)
}

// ResyncAsAdds re-delivers the current cache state as OnAdd for each cached
// object, for handlers that treat resync as a fresh add. Returns the count.
func (inf *Informer) ResyncAsAdds() int {
	current := inf.store.List()
	for _, o := range current {
		inf.dispatch(Added, Object{}, o)
	}
	return len(current)
}
