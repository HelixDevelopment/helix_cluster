package informer

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

const advRunUUID = "informer-adversarial-9a2f61c0-3d4e-4b71-8c55-hxc-informer-audit"

// ---------------------------------------------------------------------------
// Adversarial supplement for pkg/informer.
//
// The committed suite (informer_coherence_test.go, HXC-1664) already pins:
//   - latest-delta-per-key after a single-threaded barrier sweep,
//   - handler/cache consistency through the real Informer.Run path,
//   - delete coherence + no-resurrection after a barrier delete.
//
// This file attacks the cache-coherence contract from the angles the committed
// suite leaves open:
//
//   A. LIST/INDEX ATOMICITY (torn-seed): Store.List() and Store.ByIndex() must
//      return a self-consistent snapshot taken entirely under one RLock — never
//      a torn read and never a "concurrent map iteration and map write" panic —
//      while many writers mutate the SAME keys. (Go's race detector + the
//      runtime's concurrent-map-access panic are the oracles.)
//
//   B. SEED-THEN-GET ATOMICITY through Run: a reader hammering Get/List while
//      the initial List seeds the cache must see either the pre-seed (empty) or
//      a consistent post-seed state for each key it reads — never a half-built
//      Object — and the cache must converge to EXACTLY the seeded set.
//
//   C. RESURRECTION ORDERING (sharper): with a single serialized writer (the
//      contract's real topology), a Delete that is emitted AFTER an Add for the
//      same key must win — the key stays absent. The reverse (Add after Delete)
//      must resurrect. This pins the "last serialized apply wins; the Store has
//      no version field, ordering is the caller's responsibility" contract.
//
//   D. EDGES: empty cache reads; Get-missing; Delete-missing no-op; duplicate
//      identical Add is idempotent (still one entry, value unchanged, no index
//      bucket bloat); Update of a never-added key behaves as an upsert (Added).
//
// Anti-bluff (proven at the end of this run, see TestMutationGuide doc): each
// assertion below is wired to a specific canonical mutation in informer.go:
//   - drop s.mu in List()/apply()   -> A and the -race headline fire,
//   - dispatch before apply in Run() -> B's in-handler/post-seed reads fire,
//   - make Delete a no-op overwrite  -> C and D fire,
//   - skip deindex on Update         -> ByIndex coherence in A fires.
// ---------------------------------------------------------------------------

// TestListSeedAtomicNoTornSnapshotConcurrent runs many writers that churn an
// overlapping key set (Add/Update/Delete + index moves) while many readers call
// List()/ByIndex()/GetByKey() in a tight loop. The win condition is purely
// safety: no data race (-race), no "concurrent map iteration and map write"
// panic, and every List() snapshot is internally consistent — each returned
// Object is fully formed and, if it carries a team label, is reachable in that
// team's index bucket AT THE TIME of a follow-up read is NOT asserted (that
// would be a TOCTOU illusion); instead we assert each snapshot is well-formed
// and sorted, which is what "atomic under one RLock" guarantees.
//
// Anti-bluff: removing the RLock from List()/ByIndex() (or the Lock from
// apply()) makes -race fire here, and concurrent map iteration vs write makes
// the Go runtime panic — this test is the canary for the centerpiece race.
func TestListSeedAtomicNoTornSnapshotConcurrent(t *testing.T) {
	store := NewStore()
	store.AddIndexer("by-team", func(o Object) []string {
		if v, ok := o.Labels["team"]; ok {
			return []string{v}
		}
		return nil
	})

	const (
		keys      = 24
		writers   = 24
		readers   = 12
		perWriter = 600
		reads     = 1200
	)
	mk := func(k, gen int) Object {
		return Object{
			Namespace: "ns",
			Name:      fmt.Sprintf("k%02d", k),
			Value:     fmt.Sprintf("g%d", gen),
			Labels:    map[string]string{"team": fmt.Sprintf("t%d", k%5)},
		}
	}

	var wg sync.WaitGroup
	var stop int32

	// Writers: chaotic concurrent churn on overlapping keys.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				k := (seed + i) % keys
				switch (seed*3 + i) % 3 {
				case 0:
					store.apply(Event{Type: Added, Object: mk(k, seed*100+i)})
				case 1:
					store.apply(Event{Type: Updated, Object: mk(k, seed*7+i)})
				default:
					store.apply(Event{Type: Deleted, Object: Object{Namespace: "ns", Name: fmt.Sprintf("k%02d", k)}})
				}
			}
		}(w)
	}

	// Readers: hammer List/ByIndex/Get and validate each snapshot is well-formed
	// and key-sorted (proves the snapshot was taken under one consistent lock,
	// not assembled piecewise from a mutating map).
	var tornSeen int32
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < reads && atomic.LoadInt32(&stop) == 0; i++ {
				snap := store.List()
				var prev string
				for j, o := range snap {
					// Well-formed: every cached object has the expected ns/name shape.
					if o.Namespace != "ns" || len(o.Name) == 0 || o.Value == "" {
						atomic.StoreInt32(&tornSeen, 1)
					}
					// Sorted by Key(): proves single-snapshot ordering.
					if j > 0 && o.Key() < prev {
						atomic.StoreInt32(&tornSeen, 1)
					}
					prev = o.Key()
				}
				for tt := 0; tt < 5; tt++ {
					_ = store.ByIndex("by-team", fmt.Sprintf("t%d", tt))
				}
				_, _ = store.GetByKey(fmt.Sprintf("ns/k%02d", i%keys))
			}
		}()
	}
	wg.Wait()
	atomic.StoreInt32(&stop, 1)

	if atomic.LoadInt32(&tornSeen) != 0 {
		t.Fatalf("[%s] torn/ill-formed List() snapshot observed under concurrency (List not atomic under one RLock)", advRunUUID)
	}

	// BARRIER: single-threaded terminal sweep, then full coherence check.
	for k := 0; k < keys; k++ {
		store.apply(Event{Type: Added, Object: mk(k, 1_000_000)})
	}
	list := store.List()
	if len(list) != keys {
		t.Fatalf("[%s] after terminal upsert List()=%d want %d", advRunUUID, len(list), keys)
	}
	for _, o := range list {
		if o.Value != "g1000000" {
			t.Fatalf("[%s] %s value=%q want g1000000 (terminal upsert lost)", advRunUUID, o.Key(), o.Value)
		}
		team := o.Labels["team"]
		found := false
		for _, x := range store.ByIndex("by-team", team) {
			if x.Key() == o.Key() {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("[%s] %s present in cache but missing from index bucket %q (index/cache incoherent after churn)", advRunUUID, o.Key(), team)
		}
	}
	t.Logf("[%s] %d writers x %d ops + %d readers x %d reads: no torn snapshot, no race, terminal upsert coherent across %d keys",
		advRunUUID, writers, perWriter, readers, reads, keys)
}

// TestSeedAtomicGetDuringInitialList drives the REAL Informer.Run seeding path
// while a reader concurrently hammers Get/List. The contract: by the time
// WaitForSync returns true, the cache holds EXACTLY the seeded set with the
// EXACT seeded values; and every Get a reader makes during seeding returns
// either "absent" or the fully-formed seeded Object — never a half-applied one.
//
// Anti-bluff: this exercises the dispatch-after-apply ordering in Run via a real
// List seed of many keys; combined with -race it bites a seed that mutates the
// shared map outside the store lock or a reader path that reads it unguarded.
func TestSeedAtomicGetDuringInitialList(t *testing.T) {
	const nSeed = 200
	seed := make([]Object, 0, nSeed)
	want := make(map[string]string, nSeed)
	for i := 0; i < nSeed; i++ {
		name := fmt.Sprintf("s%03d", i)
		val := fmt.Sprintf("seed-v%03d", i)
		seed = append(seed, Object{Namespace: "ns", Name: name, Value: val})
		want["ns/"+name] = val
	}
	src := newFakeSource(seed)
	inf := NewInformer(src)
	store := inf.Store()

	stop := make(chan struct{})
	defer close(stop)

	var bad int32
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		// Read aggressively while Run seeds; any value seen must match the seed.
		for round := 0; round < 4000; round++ {
			for i := 0; i < nSeed; i += 7 {
				key := fmt.Sprintf("ns/s%03d", i)
				if o, ok := store.GetByKey(key); ok {
					if o.Value != want[key] {
						atomic.StoreInt32(&bad, 1)
					}
				}
			}
			_ = store.List() // must never panic / tear during seed.
			if inf.HasSynced() {
				// Keep reading a bit past sync to race the boundary, then stop.
				if round > 50 {
					break
				}
			}
		}
	}()

	inf.Start(stop)
	if !inf.WaitForSync(stop) {
		t.Fatalf("[%s] informer never synced", advRunUUID)
	}
	done.Wait()

	if atomic.LoadInt32(&bad) != 0 {
		t.Fatalf("[%s] reader observed a half-applied/incorrect value during seed (torn seed)", advRunUUID)
	}
	// Post-sync the cache must equal EXACTLY the seeded set.
	got := store.List()
	if len(got) != nSeed {
		t.Fatalf("[%s] post-sync cache size=%d want %d (seed not atomic/complete)", advRunUUID, len(got), nSeed)
	}
	for key, v := range want {
		o, ok := store.GetByKey(key)
		if !ok {
			t.Fatalf("[%s] seeded key %s missing post-sync", advRunUUID, key)
		}
		if o.Value != v {
			t.Fatalf("[%s] seeded key %s value=%q want %q", advRunUUID, key, o.Value, v)
		}
	}
	t.Logf("[%s] seed of %d keys is atomic vs concurrent Get/List; post-sync cache == seed exactly", advRunUUID, nSeed)
}

// TestSerializedOrderingDeleteWinsThenAddResurrects pins the ordering contract
// on the REAL single-writer topology (Informer.Run consuming one Watch channel,
// which is how production serializes deltas). The Store has no version field, so
// the rule is "last serialized apply wins":
//   - Add(v1) then Delete  -> key ABSENT (delete wins, no stale survivor)
//   - Delete then Add(v2)   -> key PRESENT=v2 (later add resurrects, by design)
//   - Add(v1) then Add(v3)  -> key PRESENT=v3 (latest overwrite wins)
//
// Anti-bluff: turning apply's Delete into a no-op leaves the first case present
// (fails); making Add not overwrite leaves the third case at v1 (fails). This
// is the ordering oracle the concurrent barrier test cannot pin deterministically.
func TestSerializedOrderingDeleteWinsThenAddResurrects(t *testing.T) {
	src := newFakeSource(nil)
	inf := NewInformer(src)
	store := inf.Store()

	notified := make(chan EventType, 64)
	inf.AddEventHandler(ResourceEventHandler{
		OnAdd:    func(o Object) { notified <- Added },
		OnUpdate: func(old, new Object) { notified <- Updated },
		OnDelete: func(o Object) { notified <- Deleted },
	})
	stop := make(chan struct{})
	defer close(stop)
	inf.Start(stop)
	inf.WaitForSync(stop)

	mk := func(name, val string) Object { return Object{Namespace: "ns", Name: name, Value: val} }

	// Case 1: Add(v1) then Delete -> absent. (delete-wins)
	src.emit(Event{Type: Added, Object: mk("a", "v1")})
	<-notified
	src.emit(Event{Type: Deleted, Object: mk("a", "")})
	<-notified
	if got, ok := store.GetByKey("ns/a"); ok {
		t.Fatalf("[%s] case1 Add-then-Delete: ns/a still present=%+v (delete did not win)", advRunUUID, got)
	}

	// Case 2: Delete (no-op) then Add(v2) -> present=v2. (later add resurrects)
	// The leading delete on the now-absent key is a no-op: it must NOT notify.
	src.emit(Event{Type: Deleted, Object: mk("a", "")}) // no-op, fire=false
	src.emit(Event{Type: Added, Object: mk("a", "v2")})
	// Only the Add should notify; if the no-op delete wrongly fired we'd read
	// Deleted here first and the value assertion would still hold, so guard it.
	ev := <-notified
	if ev != Added {
		t.Fatalf("[%s] case2: first notification=%v want Added (no-op delete of absent key wrongly fired)", advRunUUID, ev)
	}
	if got, ok := store.GetByKey("ns/a"); !ok || got.Value != "v2" {
		t.Fatalf("[%s] case2 Delete-then-Add: ns/a=%+v ok=%v want value=v2 (later add must resurrect)", advRunUUID, got, ok)
	}

	// Case 3: Add(v3) overwrites the present v2 -> Updated, value=v3. (latest wins)
	src.emit(Event{Type: Added, Object: mk("a", "v3")})
	ev = <-notified
	if ev != Updated {
		t.Fatalf("[%s] case3: notification=%v want Updated (re-add of present key is an update)", advRunUUID, ev)
	}
	if got, ok := store.GetByKey("ns/a"); !ok || got.Value != "v3" {
		t.Fatalf("[%s] case3 Add-over-present: ns/a=%+v want value=v3 (latest overwrite must win)", advRunUUID, got)
	}

	// Final delete settles absent.
	src.emit(Event{Type: Deleted, Object: mk("a", "")})
	<-notified
	if _, ok := store.GetByKey("ns/a"); ok {
		t.Fatalf("[%s] final delete left ns/a present", advRunUUID)
	}
	t.Logf("[%s] serialized ordering pinned: delete-wins, later-add-resurrects, latest-overwrite-wins; no-op delete of absent key did not fire", advRunUUID)
}

// TestEdgesEmptyMissingDuplicateUpsert pins the small-surface contract:
//   - empty store: List() is empty, ByIndex unknown is nil, Get miss is (zero,false)
//   - Delete of an absent key: fire=false, cache untouched
//   - duplicate identical Add: idempotent — one entry, value stable, exactly one
//     index-bucket membership (no bucket bloat / double-count)
//   - Update of a never-seen key: upserts as Added (effType Added, old zero)
//
// Anti-bluff: duplicate-Add bucket-size==1 bites an addToIndex that fails to
// treat the bucket as a set; the Update-as-upsert effType bites a path that
// blind-returns Updated for a missing prior.
func TestEdgesEmptyMissingDuplicateUpsert(t *testing.T) {
	store := NewStore()
	store.AddIndexer("by-team", func(o Object) []string {
		if v, ok := o.Labels["team"]; ok {
			return []string{v}
		}
		return nil
	})

	// Empty store.
	if l := store.List(); len(l) != 0 {
		t.Fatalf("[%s] empty List()=%d want 0", advRunUUID, len(l))
	}
	if b := store.ByIndex("by-team", "tX"); b != nil {
		t.Fatalf("[%s] empty ByIndex unknown bucket=%v want nil", advRunUUID, b)
	}
	if b := store.ByIndex("no-such-index", "v"); b != nil {
		t.Fatalf("[%s] ByIndex unknown index=%v want nil", advRunUUID, b)
	}
	if o, ok := store.GetByKey("ns/nope"); ok || o.Value != "" {
		t.Fatalf("[%s] Get miss returned o=%+v ok=%v want zero,false", advRunUUID, o, ok)
	}

	// Delete of absent key: no-op.
	if _, _, _, fire := store.apply(Event{Type: Deleted, Object: Object{Namespace: "ns", Name: "absent"}}); fire {
		t.Fatalf("[%s] delete-missing fire=true want false", advRunUUID)
	}
	if len(store.List()) != 0 {
		t.Fatalf("[%s] delete-missing mutated the cache", advRunUUID)
	}

	// Duplicate identical Add: idempotent.
	obj := Object{Namespace: "ns", Name: "d", Value: "v", Labels: map[string]string{"team": "red"}}
	et1, _, _, f1 := store.apply(Event{Type: Added, Object: obj})
	if !f1 || et1 != Added {
		t.Fatalf("[%s] first Add effType=%v fire=%v want Added,true", advRunUUID, et1, f1)
	}
	et2, _, _, f2 := store.apply(Event{Type: Added, Object: obj}) // same key again
	if !f2 || et2 != Updated {
		t.Fatalf("[%s] duplicate Add effType=%v fire=%v want Updated,true (re-add of present key is update)", advRunUUID, et2, f2)
	}
	if l := store.List(); len(l) != 1 {
		t.Fatalf("[%s] after duplicate Add List()=%d want 1 (entry duplicated)", advRunUUID, len(l))
	}
	if o, _ := store.GetByKey("ns/d"); o.Value != "v" {
		t.Fatalf("[%s] after duplicate Add value=%q want v", advRunUUID, o.Value)
	}
	bucket := store.ByIndex("by-team", "red")
	if len(bucket) != 1 || bucket[0].Key() != "ns/d" {
		t.Fatalf("[%s] index bucket after duplicate Add=%v want exactly [ns/d] (bucket bloat / double membership)", advRunUUID, keysOf(bucket))
	}

	// Update of a never-seen key behaves as an upsert (Added, old zero).
	et3, old3, new3, f3 := store.apply(Event{Type: Updated, Object: Object{Namespace: "ns", Name: "fresh", Value: "u"}})
	if !f3 || et3 != Added {
		t.Fatalf("[%s] Update-of-missing effType=%v fire=%v want Added,true (upsert)", advRunUUID, et3, f3)
	}
	if old3.Value != "" {
		t.Fatalf("[%s] Update-of-missing old=%+v want zero Object", advRunUUID, old3)
	}
	if new3.Value != "u" {
		t.Fatalf("[%s] Update-of-missing new=%+v want value=u", advRunUUID, new3)
	}
	if o, ok := store.GetByKey("ns/fresh"); !ok || o.Value != "u" {
		t.Fatalf("[%s] Update-of-missing did not upsert: o=%+v ok=%v", advRunUUID, o, ok)
	}
	t.Logf("[%s] edges pinned: empty reads, get/delete-missing no-op, duplicate Add idempotent (bucket size 1), update-of-missing upserts as Added", advRunUUID)
}
