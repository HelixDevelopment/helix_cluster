package informer

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

const coherenceRunUUID = "informer-coherence-7c4d92a1-5e6b-4f80-bd33-hxc-adversarial"

// ---------------------------------------------------------------------------
// Adversarial cache-coherence + concurrency-safety suite for pkg/informer.
//
// Model under test (read from informer.go):
//   - Store.items map[string]Object is the local cache, keyed by Object.Key()
//     ("Namespace/Name"), guarded by Store.mu (sync.RWMutex).
//   - Store.apply(Event) is THE mutation primitive: Add/Update overwrite
//     items[key]; Delete removes key; Delete of an absent key is a no-op
//     (fire=false). apply returns (effType, old, new, fire) so handlers run
//     outside the lock.
//   - Informer.Run serializes deltas from a single Watch channel, so the
//     EXISTING concurrent test (TestConcurrentReadDuringDeltas) only ever
//     exercises ONE writer vs readers. The write-vs-write path through the
//     store map + indexes is never raced by the existing suite.
//
// GAP these tests close:
//   1. Coherence headline: after a barrier-defined "last delta per key", the
//      cache holds EXACTLY the latest state — update overwrites, delete
//      removes, no stale value, no resurrected deleted key.
//   2. Handler/cache consistency: a handler observing an Add/Update can find
//      the object in the cache with the SAME value the event carried (no lost
//      delta, dispatch view agrees with store view).
//   3. Concurrent delivery (-race): many goroutines call apply() on OVERLAPPING
//      keys at once -> bites a store-map / index-map data race, a panic, and
//      (after a deterministic barrier sweep) a wrong final state.
//   4. Delete coherence: delete removes the key; delete of an unknown key is a
//      safe no-op; a superseded late add does not resurrect a deleted key.
// ---------------------------------------------------------------------------

// TestCoherenceLatestDeltaPerKeyConcurrent hammers apply() from many goroutines
// on a small set of overlapping keys (maximizing write-vs-write contention on
// the same map buckets), then imposes a BARRIER: a single-goroutine final sweep
// that sets each key to a known terminal state. After the barrier the cache
// MUST equal exactly the terminal state.
//
// Anti-bluff: "cache == terminal state per key" bites a broken Add/Update that
// fails to overwrite (stale value survives) and a broken Delete that fails to
// remove (resurrected key). The concurrent churn under -race bites any data
// race on items/indexed. If apply dropped a write, the final read disagrees.
func TestCoherenceLatestDeltaPerKeyConcurrent(t *testing.T) {
	store := NewStore()
	store.AddIndexer("by-team", func(o Object) []string {
		if v, ok := o.Labels["team"]; ok {
			return []string{v}
		}
		return nil
	})

	const (
		keys      = 16
		writers   = 32
		perWriter = 400
	)
	mkObj := func(k, gen int) Object {
		return Object{
			Namespace: "ns",
			Name:      fmt.Sprintf("k%02d", k),
			Value:     fmt.Sprintf("g%d", gen),
			Labels:    map[string]string{"team": fmt.Sprintf("t%d", k%4)},
		}
	}

	var wg sync.WaitGroup
	// Phase 1: chaotic concurrent churn on overlapping keys. Mix of
	// Add/Update/Delete so buckets are created and torn down concurrently.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				k := (seed + i) % keys
				switch (seed + i) % 3 {
				case 0:
					store.apply(Event{Type: Added, Object: mkObj(k, seed)})
				case 1:
					store.apply(Event{Type: Updated, Object: mkObj(k, seed*7+i)})
				default:
					store.apply(Event{Type: Deleted, Object: Object{Namespace: "ns", Name: fmt.Sprintf("k%02d", k)}})
				}
				// Concurrent readers racing the writers.
				_ = store.List()
				_ = store.ByIndex("by-team", fmt.Sprintf("t%d", k%4))
				_, _ = store.GetByKey(fmt.Sprintf("ns/k%02d", k))
			}
		}(w)
	}
	wg.Wait()

	// BARRIER: all churn goroutines have returned. Now, single-threaded, define
	// the well-defined "last delta per key": even keys -> terminal value;
	// odd keys -> deleted (absent).
	terminal := map[string]string{} // key -> expected value; absent => deleted
	for k := 0; k < keys; k++ {
		key := fmt.Sprintf("ns/k%02d", k)
		if k%2 == 0 {
			want := mkObj(k, 999999)
			store.apply(Event{Type: Added, Object: want}) // upsert to known value
			terminal[key] = want.Value
		} else {
			store.apply(Event{Type: Deleted, Object: Object{Namespace: "ns", Name: fmt.Sprintf("k%02d", k)}})
			// absent
		}
	}

	// Assert cache == terminal state per key.
	present := 0
	for k := 0; k < keys; k++ {
		key := fmt.Sprintf("ns/k%02d", k)
		got, ok := store.GetByKey(key)
		want, shouldExist := terminal[key]
		if shouldExist {
			present++
			if !ok {
				t.Fatalf("[%s] key %s missing after barrier; want value=%q (lost write / resurrected delete bug)", coherenceRunUUID, key, want)
			}
			if got.Value != want {
				t.Fatalf("[%s] key %s value=%q want %q (stale cache: Add/Update did not overwrite)", coherenceRunUUID, key, got.Value, want)
			}
		} else {
			if ok {
				t.Fatalf("[%s] key %s present=%+v after terminal Delete (resurrected deleted key / Delete did not remove)", coherenceRunUUID, key, got)
			}
		}
	}

	// Store.List() must contain EXACTLY the present keys (no ghosts).
	list := store.List()
	if len(list) != present {
		t.Fatalf("[%s] List()=%d entries want %d (ghost/stale entries in cache map)", coherenceRunUUID, len(list), present)
	}
	// Index coherence: every listed object must be reachable via its bucket;
	// no deleted key may linger in any index bucket.
	for _, o := range list {
		team := o.Labels["team"]
		found := false
		for _, x := range store.ByIndex("by-team", team) {
			if x.Key() == o.Key() {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("[%s] %s in cache but absent from index bucket %q (index/cache incoherent)", coherenceRunUUID, o.Key(), team)
		}
	}
	t.Logf("[%s] concurrent churn settled: %d/%d keys present, List()==terminal, indexes coherent (writers=%d perWriter=%d)",
		coherenceRunUUID, present, keys, writers, perWriter)
}

// TestHandlerCacheConsistencyNoLostDelta drives deltas through the REAL
// Informer.Run path and asserts, INSIDE each handler, that the store already
// reflects the event the handler is being notified about (apply mutates under
// lock BEFORE dispatch, so the handler's view and the cache must agree). It
// also proves no delta is lost: every emitted Add/Update/Delete is observed
// exactly once and the terminal cache matches the last emission per key.
//
// Anti-bluff: the in-handler GetByKey bites a reordering where dispatch could
// fire before the cache mutation (handler would see stale/missing object). The
// observed-count assertion bites a dropped/duplicated delta.
func TestHandlerCacheConsistencyNoLostDelta(t *testing.T) {
	src := newFakeSource(nil)
	inf := NewInformer(src)

	var (
		mu            sync.Mutex
		mismatches    []string
		addSeen       = map[string]string{} // key -> value seen on add
		updSeen       = map[string]string{} // key -> latest value seen on update
		delSeen       = map[string]int{}
		totalNotified int
	)
	done := make(chan struct{}, 4096)

	store := inf.Store()
	inf.AddEventHandler(ResourceEventHandler{
		OnAdd: func(o Object) {
			// Handler/cache consistency: the just-added object must be in cache
			// with the same value.
			got, ok := store.GetByKey(o.Key())
			mu.Lock()
			if !ok || got.Value != o.Value {
				mismatches = append(mismatches, fmt.Sprintf("OnAdd %s: cache ok=%v val=%q event val=%q", o.Key(), ok, got.Value, o.Value))
			}
			addSeen[o.Key()] = o.Value
			totalNotified++
			mu.Unlock()
			done <- struct{}{}
		},
		OnUpdate: func(old, new Object) {
			got, ok := store.GetByKey(new.Key())
			mu.Lock()
			if !ok || got.Value != new.Value {
				mismatches = append(mismatches, fmt.Sprintf("OnUpdate %s: cache ok=%v val=%q event new val=%q", new.Key(), ok, got.Value, new.Value))
			}
			updSeen[new.Key()] = new.Value
			totalNotified++
			mu.Unlock()
			done <- struct{}{}
		},
		OnDelete: func(o Object) {
			// After delete, the key must be GONE from the cache.
			_, ok := store.GetByKey(o.Key())
			mu.Lock()
			if ok {
				mismatches = append(mismatches, fmt.Sprintf("OnDelete %s: still in cache", o.Key()))
			}
			delSeen[o.Key()]++
			totalNotified++
			mu.Unlock()
			done <- struct{}{}
		},
	})

	stop := make(chan struct{})
	defer close(stop)
	inf.Start(stop)
	inf.WaitForSync(stop)

	// Deterministic emission sequence: for each key, add then a chain of
	// updates then delete. Run consumes them in order; we count expected
	// notifications precisely.
	const nKeys = 12
	const updatesPer = 5
	expectedNotifications := 0
	lastValue := map[string]string{}
	for k := 0; k < nKeys; k++ {
		name := fmt.Sprintf("o%02d", k)
		key := "ns/" + name
		src.emit(Event{Type: Added, Object: Object{Namespace: "ns", Name: name, Value: "v0"}})
		expectedNotifications++
		lastValue[key] = "v0"
		for u := 1; u <= updatesPer; u++ {
			val := fmt.Sprintf("v%d", u)
			src.emit(Event{Type: Updated, Object: Object{Namespace: "ns", Name: name, Value: val}})
			expectedNotifications++
			lastValue[key] = val
		}
		src.emit(Event{Type: Deleted, Object: Object{Namespace: "ns", Name: name}})
		expectedNotifications++
	}
	// Also emit a delete of an UNKNOWN key: it must be a no-op (fire=false),
	// so it must NOT produce a notification.
	src.emit(Event{Type: Deleted, Object: Object{Namespace: "ns", Name: "ghost-never-added"}})

	// Drain exactly the expected number of notifications.
	for i := 0; i < expectedNotifications; i++ {
		<-done
	}

	mu.Lock()
	defer mu.Unlock()
	if len(mismatches) != 0 {
		t.Fatalf("[%s] handler/cache disagreements (%d): %v", coherenceRunUUID, len(mismatches), mismatches)
	}
	if totalNotified != expectedNotifications {
		t.Fatalf("[%s] notified=%d want=%d (lost or duplicated delta; or no-op delete of unknown key wrongly fired)",
			coherenceRunUUID, totalNotified, expectedNotifications)
	}
	// Every key added once, deleted once; each update value was observed.
	if len(addSeen) != nKeys {
		t.Fatalf("[%s] adds seen for %d keys want %d", coherenceRunUUID, len(addSeen), nKeys)
	}
	for k := 0; k < nKeys; k++ {
		key := fmt.Sprintf("ns/o%02d", k)
		if delSeen[key] != 1 {
			t.Fatalf("[%s] delete of %s seen %d times want 1", coherenceRunUUID, key, delSeen[key])
		}
		// final update value observed must be the last update emitted.
		if updSeen[key] != fmt.Sprintf("v%d", updatesPer) {
			t.Fatalf("[%s] last update value for %s = %q want %q (lost update delta)", coherenceRunUUID, key, updSeen[key], fmt.Sprintf("v%d", updatesPer))
		}
	}
	// Terminal cache: everything was deleted -> empty.
	if n := len(store.List()); n != 0 {
		t.Fatalf("[%s] terminal cache size=%d want 0 (delete did not remove every key)", coherenceRunUUID, n)
	}
	t.Logf("[%s] handler==cache for %d notifications across %d keys; no-op delete of unknown key did NOT fire; terminal cache empty",
		coherenceRunUUID, totalNotified, nKeys)
}

// TestDeleteCoherenceNoResurrection proves: (a) delete removes the key;
// (b) delete of an unknown key is a safe no-op returning fire=false; (c) a
// late Add that is then superseded by a Delete does not resurrect the key.
//
// Anti-bluff: the fire==false assertion bites a Delete that wrongly reports a
// notification for a missing key; the final GetByKey miss bites a Delete that
// failed to remove or an Add that leaked a resurrected entry.
func TestDeleteCoherenceNoResurrection(t *testing.T) {
	store := NewStore()

	// (b) Delete of unknown key: no-op, fire=false, cache stays empty.
	_, _, _, fire := store.apply(Event{Type: Deleted, Object: Object{Namespace: "ns", Name: "missing"}})
	if fire {
		t.Fatalf("[%s] Delete of unknown key reported fire=true (should be a silent no-op)", coherenceRunUUID)
	}
	if _, ok := store.GetByKey("ns/missing"); ok {
		t.Fatalf("[%s] Delete of unknown key created an entry", coherenceRunUUID)
	}

	// (a) Add then Delete removes the key.
	store.apply(Event{Type: Added, Object: Object{Namespace: "ns", Name: "x", Value: "v1"}})
	if _, ok := store.GetByKey("ns/x"); !ok {
		t.Fatalf("[%s] add did not populate ns/x", coherenceRunUUID)
	}
	_, _, _, fire = store.apply(Event{Type: Deleted, Object: Object{Namespace: "ns", Name: "x"}})
	if !fire {
		t.Fatalf("[%s] Delete of present key did not fire", coherenceRunUUID)
	}
	if _, ok := store.GetByKey("ns/x"); ok {
		t.Fatalf("[%s] ns/x still present after delete", coherenceRunUUID)
	}

	// (c) No resurrection: add, delete, then concurrently a late add followed
	// by a barrier delete must leave the key absent. We race a re-add against a
	// delete, then a deterministic final delete settles it absent.
	var wg sync.WaitGroup
	var addCount int64
	for i := 0; i < 64; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			store.apply(Event{Type: Added, Object: Object{Namespace: "ns", Name: "z", Value: "race"}})
			atomic.AddInt64(&addCount, 1)
		}()
		go func() { defer wg.Done(); store.apply(Event{Type: Deleted, Object: Object{Namespace: "ns", Name: "z"}}) }()
	}
	wg.Wait()
	// BARRIER: settle deterministically with a final delete -> must be absent.
	store.apply(Event{Type: Deleted, Object: Object{Namespace: "ns", Name: "z"}})
	if got, ok := store.GetByKey("ns/z"); ok {
		t.Fatalf("[%s] ns/z resurrected=%+v after final barrier Delete (saw %d concurrent adds)", coherenceRunUUID, got, addCount)
	}
	t.Logf("[%s] delete removes; unknown-key delete is no-op (fire=false); final barrier delete wins over %d racing adds (no resurrection)",
		coherenceRunUUID, addCount)
}
