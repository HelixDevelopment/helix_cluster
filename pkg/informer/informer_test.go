package informer

import (
	"sort"
	"sync"
	"testing"
)

const runUUID = "informer-test-3f7a1c2e-9b04-4d6a-8e21-hxc1411"

// fakeSource is an in-memory DeltaSource: no network, fully deterministic.
// The watch channel is fed by the test and closed via Close().
type fakeSource struct {
	initial []Object
	ch      chan Event
}

func newFakeSource(initial []Object) *fakeSource {
	return &fakeSource{initial: initial, ch: make(chan Event, 64)}
}

func (f *fakeSource) List() []Object        { return f.initial }
func (f *fakeSource) Watch() <-chan Event   { return f.ch }
func (f *fakeSource) emit(ev Event)         { f.ch <- ev }
func (f *fakeSource) Close()                { close(f.ch) }

func keysOf(objs []Object) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Key())
	}
	sort.Strings(out)
	return out
}

// Clause 1: List seeds the cache.
// Mutation: if Run skipped applying source.List(), Store.List() would be empty
// and GetByKey would miss every seeded key — this test fails.
func TestListSeedsCache(t *testing.T) {
	seed := []Object{
		{Namespace: "ns1", Name: "a", Value: "v-a"},
		{Namespace: "ns1", Name: "b", Value: "v-b"},
		{Namespace: "ns2", Name: "c", Value: "v-c"},
	}
	src := newFakeSource(seed)
	inf := NewInformer(src)

	var mu sync.Mutex
	adds := 0
	inf.AddEventHandler(ResourceEventHandler{OnAdd: func(o Object) {
		mu.Lock()
		adds++
		mu.Unlock()
	}})

	stop := make(chan struct{})
	defer close(stop)
	inf.Start(stop)
	if !inf.WaitForSync(stop) {
		t.Fatalf("[%s] informer never synced", runUUID)
	}

	got := inf.Store().List()
	t.Logf("[%s] after List: cache size before=0 after=%d keys=%v", runUUID, len(got), keysOf(got))
	if len(got) != len(seed) {
		t.Fatalf("[%s] want %d cached, got %d", runUUID, len(seed), len(got))
	}
	for _, s := range seed {
		o, ok := inf.Store().GetByKey(s.Key())
		if !ok {
			t.Fatalf("[%s] GetByKey(%s) missing", runUUID, s.Key())
		}
		if o.Value != s.Value {
			t.Fatalf("[%s] GetByKey(%s) value=%q want %q", runUUID, s.Key(), o.Value, s.Value)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if adds != len(seed) {
		t.Fatalf("[%s] OnAdd fired %d times, want %d", runUUID, adds, len(seed))
	}
}

// Clause 2: Watch deltas mutate cache (Add/Update/Delete) and fire handlers.
// Mutation: if apply() skipped `delete(s.items, key)` on Deleted, the final
// cache would still contain "ns/x" and this test fails. If Update did not
// overwrite s.items[key], the value-change assertion fails.
func TestWatchDeltasMutateCache(t *testing.T) {
	src := newFakeSource(nil)
	inf := NewInformer(src)

	var mu sync.Mutex
	var addKeys, delKeys []string
	type upd struct{ old, new string }
	var updates []upd
	done := make(chan struct{}, 16)

	inf.AddEventHandler(ResourceEventHandler{
		OnAdd: func(o Object) {
			mu.Lock()
			addKeys = append(addKeys, o.Key())
			mu.Unlock()
			done <- struct{}{}
		},
		OnUpdate: func(old, new Object) {
			mu.Lock()
			updates = append(updates, upd{old.Value, new.Value})
			mu.Unlock()
			done <- struct{}{}
		},
		OnDelete: func(o Object) {
			mu.Lock()
			delKeys = append(delKeys, o.Key())
			mu.Unlock()
			done <- struct{}{}
		},
	})

	stop := make(chan struct{})
	defer close(stop)
	inf.Start(stop)
	inf.WaitForSync(stop)

	obj := Object{Namespace: "ns", Name: "x", Value: "first"}

	// Add
	src.emit(Event{Type: Added, Object: obj})
	<-done
	if o, ok := inf.Store().GetByKey("ns/x"); !ok || o.Value != "first" {
		t.Fatalf("[%s] after Add: got=%+v ok=%v want value=first", runUUID, o, ok)
	}
	t.Logf("[%s] after Add: key ns/x present value=first", runUUID)

	// Update
	src.emit(Event{Type: Updated, Object: Object{Namespace: "ns", Name: "x", Value: "second"}})
	<-done
	o, ok := inf.Store().GetByKey("ns/x")
	if !ok || o.Value != "second" {
		t.Fatalf("[%s] after Update: got=%+v ok=%v want value=second", runUUID, o, ok)
	}
	t.Logf("[%s] after Update: value changed first->second", runUUID)

	// Delete
	src.emit(Event{Type: Deleted, Object: Object{Namespace: "ns", Name: "x"}})
	<-done
	if _, ok := inf.Store().GetByKey("ns/x"); ok {
		t.Fatalf("[%s] after Delete: key ns/x still present", runUUID)
	}
	if n := len(inf.Store().List()); n != 0 {
		t.Fatalf("[%s] after Delete: cache size=%d want 0", runUUID, n)
	}
	t.Logf("[%s] after Delete: cache size before=1 after=0", runUUID)

	mu.Lock()
	defer mu.Unlock()
	if len(addKeys) != 1 || addKeys[0] != "ns/x" {
		t.Fatalf("[%s] OnAdd=%v want [ns/x]", runUUID, addKeys)
	}
	if len(updates) != 1 || updates[0].old != "first" || updates[0].new != "second" {
		t.Fatalf("[%s] OnUpdate=%v want old=first new=second", runUUID, updates)
	}
	if len(delKeys) != 1 || delKeys[0] != "ns/x" {
		t.Fatalf("[%s] OnDelete=%v want [ns/x]", runUUID, delKeys)
	}
}

// Clause 3: Indexer correctness before AND after an update that moves an
// object between buckets.
// Mutation: if apply() did NOT deindex the old value before reindexing on
// Update (i.e. skipped removeFromIndex with the prior object), "ns/p" would
// remain in bucket "team-a" and the post-update assertion (team-a empty,
// team-b has p) fails.
func TestIndexerBeforeAndAfterUpdate(t *testing.T) {
	byTeam := func(o Object) []string {
		if v, ok := o.Labels["team"]; ok {
			return []string{v}
		}
		return nil
	}
	seed := []Object{
		{Namespace: "ns", Name: "p", Value: "1", Labels: map[string]string{"team": "team-a"}},
		{Namespace: "ns", Name: "q", Value: "1", Labels: map[string]string{"team": "team-a"}},
		{Namespace: "ns", Name: "r", Value: "1", Labels: map[string]string{"team": "team-b"}},
	}
	src := newFakeSource(seed)
	inf := NewInformer(src)
	inf.Store().AddIndexer("by-team", byTeam)

	stop := make(chan struct{})
	defer close(stop)
	inf.Start(stop)
	inf.WaitForSync(stop)

	before := keysOf(inf.Store().ByIndex("by-team", "team-a"))
	t.Logf("[%s] before update: by-team/team-a=%v", runUUID, before)
	if len(before) != 2 || before[0] != "ns/p" || before[1] != "ns/q" {
		t.Fatalf("[%s] team-a before=%v want [ns/p ns/q]", runUUID, before)
	}

	// Move p from team-a to team-b.
	done := make(chan struct{}, 1)
	inf.AddEventHandler(ResourceEventHandler{OnUpdate: func(old, new Object) { done <- struct{}{} }})
	src.emit(Event{Type: Updated, Object: Object{
		Namespace: "ns", Name: "p", Value: "2", Labels: map[string]string{"team": "team-b"},
	}})
	<-done

	afterA := keysOf(inf.Store().ByIndex("by-team", "team-a"))
	afterB := keysOf(inf.Store().ByIndex("by-team", "team-b"))
	t.Logf("[%s] after update: by-team/team-a=%v by-team/team-b=%v", runUUID, afterA, afterB)
	if len(afterA) != 1 || afterA[0] != "ns/q" {
		t.Fatalf("[%s] team-a after=%v want [ns/q] (p must have left)", runUUID, afterA)
	}
	if len(afterB) != 2 || afterB[0] != "ns/p" || afterB[1] != "ns/r" {
		t.Fatalf("[%s] team-b after=%v want [ns/p ns/r]", runUUID, afterB)
	}
}

// Indexer registered AFTER seeding must back-index existing objects.
// Mutation: if AddIndexer skipped re-indexing s.items, ByIndex would be empty.
func TestAddIndexerBackfills(t *testing.T) {
	seed := []Object{
		{Namespace: "ns", Name: "a", Labels: map[string]string{"zone": "z1"}},
		{Namespace: "ns", Name: "b", Labels: map[string]string{"zone": "z1"}},
	}
	src := newFakeSource(seed)
	inf := NewInformer(src)
	stop := make(chan struct{})
	defer close(stop)
	inf.Start(stop)
	inf.WaitForSync(stop)

	inf.Store().AddIndexer("by-zone", func(o Object) []string { return []string{o.Labels["zone"]} })
	got := keysOf(inf.Store().ByIndex("by-zone", "z1"))
	t.Logf("[%s] backfilled by-zone/z1=%v", runUUID, got)
	if len(got) != 2 {
		t.Fatalf("[%s] backfill got=%v want 2", runUUID, got)
	}
}

// Clause 4: Resync re-emits current state to handlers.
// Mutation: if Resync did not iterate the cache (returned without dispatch),
// the handler counters stay 0 and this test fails.
func TestResyncReEmitsState(t *testing.T) {
	seed := []Object{
		{Namespace: "ns", Name: "a", Value: "1"},
		{Namespace: "ns", Name: "b", Value: "2"},
		{Namespace: "ns", Name: "c", Value: "3"},
	}
	src := newFakeSource(seed)
	inf := NewInformer(src)
	stop := make(chan struct{})
	defer close(stop)
	inf.Start(stop)
	inf.WaitForSync(stop)

	var mu sync.Mutex
	updates := map[string]int{}
	addsViaResync := map[string]int{}
	inf.AddEventHandler(ResourceEventHandler{
		OnUpdate: func(old, new Object) {
			mu.Lock()
			updates[new.Key()]++
			mu.Unlock()
		},
		OnAdd: func(o Object) {
			mu.Lock()
			addsViaResync[o.Key()]++
			mu.Unlock()
		},
	})

	n := inf.Resync()
	t.Logf("[%s] Resync re-emitted %d objects as OnUpdate", runUUID, n)
	if n != 3 {
		t.Fatalf("[%s] Resync count=%d want 3", runUUID, n)
	}
	mu.Lock()
	for _, k := range []string{"ns/a", "ns/b", "ns/c"} {
		if updates[k] != 1 {
			mu.Unlock()
			t.Fatalf("[%s] OnUpdate for %s fired %d times want 1; updates=%v", runUUID, k, updates[k], updates)
		}
	}
	mu.Unlock()

	n2 := inf.ResyncAsAdds()
	t.Logf("[%s] ResyncAsAdds re-emitted %d objects as OnAdd", runUUID, n2)
	mu.Lock()
	defer mu.Unlock()
	for _, k := range []string{"ns/a", "ns/b", "ns/c"} {
		if addsViaResync[k] != 1 {
			t.Fatalf("[%s] OnAdd(resync) for %s fired %d times want 1", runUUID, k, addsViaResync[k])
		}
	}
}

// Concurrency: readers calling List/ByIndex while deltas apply must be
// race-clean under -race.
// Mutation: removing the RWMutex guards in Store would trip the race detector.
func TestConcurrentReadDuringDeltas(t *testing.T) {
	src := newFakeSource(nil)
	inf := NewInformer(src)
	inf.Store().AddIndexer("by-team", func(o Object) []string { return []string{o.Labels["team"]} })

	stop := make(chan struct{})
	defer close(stop)
	inf.Start(stop)
	inf.WaitForSync(stop)

	applied := make(chan struct{}, 256)
	inf.AddEventHandler(ResourceEventHandler{
		OnAdd:    func(o Object) { applied <- struct{}{} },
		OnUpdate: func(old, new Object) { applied <- struct{}{} },
		OnDelete: func(o Object) { applied <- struct{}{} },
	})

	const N = 200
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			_ = inf.Store().List()
			_ = inf.Store().ByIndex("by-team", "t1")
			if _, ok := inf.Store().GetByKey("ns/k"); ok {
				_ = ok
			}
		}
	}()

	for i := 0; i < N; i++ {
		src.emit(Event{Type: Added, Object: Object{
			Namespace: "ns", Name: "k", Value: "v", Labels: map[string]string{"team": "t1"},
		}})
		<-applied // Add or Update (k repeats -> updates)
	}
	wg.Wait()

	o, ok := inf.Store().GetByKey("ns/k")
	t.Logf("[%s] after %d concurrent deltas: ns/k present=%v value=%q", runUUID, N, ok, o.Value)
	if !ok {
		t.Fatalf("[%s] ns/k missing after concurrent run", runUUID)
	}
}
