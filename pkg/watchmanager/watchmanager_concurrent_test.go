package watchmanager

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// runUUID for the concurrent suite (distinct anchor from the closure suite).
const concurrentRunUUID = "watchmanager-concurrent-HXC-1391-9d4c7f10-6a2b-4f8e-bc31-71e0a9c5d4af"

// ----------------------------------------------------------------------------
// CONTRACT UNDER TEST (read from watchmanager.go + its package doc)
//
// A *Manager is an etcd-style watch manager whose ENTIRE public surface takes
// m.mu: Watch (register), Put (notify/fan-out), Sync (replay), GroupOf, Counts,
// CurrentRevision. The shared mutable state is three maps (synced/unsynced/
// victim) keyed by watcher id, the append-only store slice, currentRev, and the
// per-Watcher fields nextRev/group/c — all mutated only under m.mu. The package
// doc explicitly models "a watcher registers ... as Put advances the revision",
// i.e. registration races live fan-out. So the manager IS concurrent-by-
// contract: many callers Put / Watch / Sync / inspect simultaneously while
// consumers drain w.C().
//
// NOTE on "unregister": this Manager has NO Unwatch/Remove/Close/Cancel method
// (verified by source inspection). A watcher is never deleted from all maps; it
// only MOVES between synced<->unsynced<->victim via demotion (Put overflow) and
// promotion (Sync catch-up), always tracked in exactly ONE group. The "clean
// removal / no send-to-removed / idempotent unregister" clauses are therefore
// tested against the real lifecycle the contract DOES expose: demotion must not
// lose the watcher, must not double-count it across maps, and must never send on
// a closed channel (the manager never closes w.c).
//
// The non-blocking trySend (select/default) is the liveness guarantee: a slow /
// never-draining watcher must NEVER wedge a notifier — Put must not block on a
// full buffer. The concurrent tests below assert that directly.
// ----------------------------------------------------------------------------

// TestConcurrent_RegisterNotifySyncChurn is the headline -race test. Many
// goroutines concurrently:
//   - register watchers (Watch) — grows the synced/unsynced maps
//   - fire notifications (Put) — fans out + mutates watcher group/nextRev,
//     migrating watchers between the synced and unsynced maps
//   - run Sync passes — replays history, migrating unsynced/victim<->synced
//   - inspect state (GroupOf / Counts / CurrentRevision)
//   - drain watcher channels concurrently with the fan-out that fills them
//
// This is the classic churn that races an unsynchronized watcher map / an
// unsynchronized per-watcher field. With -race it directly catches the
// concurrent-map-read/write and the data-race-on-w.group/w.nextRev classes.
//
// Assertions (beyond -race + no panic):
//   - every revision Put returns is unique and gap-free 1..N (the store/
//     currentRev increment is serialized correctly under contention)
//   - every watcher ends in exactly ONE of the three maps (no leak, no double
//     accounting) and Counts() sums to the number registered
//   - no send-on-closed panic (manager never closes channels; drainers race
//     the fan-out)
func TestConcurrent_RegisterNotifySyncChurn(t *testing.T) {
	t.Logf("run-UUID=%s test=RegisterNotifySyncChurn (concurrent register/notify/sync churn, -race)", concurrentRunUUID)
	m := New(nil)

	const (
		putters      = 8
		putsEach     = 400
		registrars   = 6
		watchEach    = 40
		syncers      = 3
		syncRounds   = 200
		inspectors   = 4
		inspectLoops = 500
	)
	totalWatchers := registrars * watchEach
	totalPuts := putters * putsEach

	var (
		wg           sync.WaitGroup // producers: putters, registrars, syncers, inspectors
		drainWg      sync.WaitGroup // drainers (must outlive producers; joined separately)
		stopDrainers atomic.Bool

		watchersMu sync.Mutex
		watchers   []*Watcher

		revMu   sync.Mutex
		revSeen = make(map[int64]int) // revision -> times returned by Put
	)

	// Drainers: continuously, non-blockingly drain whatever watchers exist.
	// This races the in-lock trySend fan-out against channel receives — if the
	// manager ever sent on a closed channel, or mutated a watcher field racily
	// while a drainer touched w.C(), -race / a panic would fire.
	//
	// Drainers live on their OWN WaitGroup: they must keep running until ALL
	// producers finish, so they cannot be part of `wg` (which we Wait on to
	// detect producer completion) — otherwise wg.Wait would deadlock waiting on
	// drainers that only stop after wg.Wait returns.
	const drainers = 4
	for d := 0; d < drainers; d++ {
		drainWg.Add(1)
		go func() {
			defer drainWg.Done()
			for !stopDrainers.Load() {
				watchersMu.Lock()
				snap := make([]*Watcher, len(watchers))
				copy(snap, watchers)
				watchersMu.Unlock()
				for _, w := range snap {
					ch := w.C()
					for {
						select {
						case <-ch:
						default:
							goto next
						}
					}
				next:
				}
			}
		}()
	}

	// Registrars: concurrently register watchers from a mix of historical and
	// future revisions, with small buffers so overflow/demotion actually fires.
	for r := 0; r < registrars; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; i < watchEach; i++ {
				prefix := fmt.Sprintf("/p%d/", (r+i)%4)
				fromRev := int64((i % 5)) // some historical (<=cur), some =0->1
				buf := 1 + (i % 3)        // tiny buffers: 1..3, forces overflow
				w := m.Watch(prefix, fromRev, buf)
				watchersMu.Lock()
				watchers = append(watchers, w)
				watchersMu.Unlock()
			}
		}(r)
	}

	// Putters: concurrently fire notifications across the prefix space.
	for p := 0; p < putters; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < putsEach; i++ {
				key := fmt.Sprintf("/p%d/k%d", i%4, i)
				rev := m.Put(key, "v")
				revMu.Lock()
				revSeen[rev]++
				revMu.Unlock()
			}
		}(p)
	}

	// Syncers: concurrently run replay passes (migrates unsynced/victim).
	for s := 0; s < syncers; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < syncRounds; i++ {
				m.Sync()
			}
		}()
	}

	// Inspectors: concurrently read state across the locked accessors.
	for in := 0; in < inspectors; in++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < inspectLoops; i++ {
				_ = m.CurrentRevision()
				sy, un, vi := m.Counts()
				_ = sy + un + vi
				watchersMu.Lock()
				var w *Watcher
				if len(watchers) > 0 {
					w = watchers[i%len(watchers)]
				}
				watchersMu.Unlock()
				if w != nil {
					_ = m.GroupOf(w)
				}
			}
		}()
	}

	// Wait for all producers (putters/registrars/syncers/inspectors) to finish,
	// THEN signal drainers to stop and join them on their own WaitGroup.
	wg.Wait()
	stopDrainers.Store(true)
	drainWg.Wait()

	// ---- Correctness assertions after the storm settles ----

	// 1. currentRev equals total Puts; revisions returned are exactly 1..N once.
	if got := m.CurrentRevision(); got != int64(totalPuts) {
		t.Fatalf("currentRev=%d, expected exactly %d Puts (lost/double increment under contention)", got, totalPuts)
	}
	revMu.Lock()
	if len(revSeen) != totalPuts {
		t.Fatalf("Put returned %d distinct revisions, expected %d (duplicate/missing revision)", len(revSeen), totalPuts)
	}
	for r := int64(1); r <= int64(totalPuts); r++ {
		c, ok := revSeen[r]
		if !ok {
			t.Fatalf("revision %d was never returned by any Put (gap)", r)
		}
		if c != 1 {
			t.Fatalf("revision %d was returned by %d Puts (duplicate revision handed out)", r, c)
		}
	}
	revMu.Unlock()

	// 2. Every registered watcher is accounted for in EXACTLY one group (no
	//    leak, no double accounting). Counts() must sum to totalWatchers.
	sy, un, vi := m.Counts()
	if sy+un+vi != totalWatchers {
		t.Fatalf("Counts sum synced=%d+unsynced=%d+victim=%d=%d != registered %d (watcher leaked or double-counted across maps)",
			sy, un, vi, sy+un+vi, totalWatchers)
	}
	t.Logf("settled: currentRev=%d distinctRevs=%d watchers=%d (synced=%d unsynced=%d victim=%d)",
		m.CurrentRevision(), len(revSeen), totalWatchers, sy, un, vi)
}

// TestConcurrent_SlowWatcherDoesNotWedgeNotifiers proves the liveness contract:
// a watcher that NEVER drains (tiny buffer that fills immediately) must NOT
// block / wedge concurrent notifiers. Put uses a non-blocking trySend; if that
// guarantee regressed to a blocking send, the putter goroutines would deadlock
// and this test would hang (caught by the go test timeout / -timeout).
//
// We register a never-drained slow watcher, then fire many concurrent Puts and
// assert they ALL complete (return revisions) promptly. We also assert the slow
// watcher got demoted out of synced (it overflowed) and is still tracked — i.e.
// the manager shed it cleanly without losing it and without sending on a closed
// channel.
func TestConcurrent_SlowWatcherDoesNotWedgeNotifiers(t *testing.T) {
	t.Logf("run-UUID=%s test=SlowWatcherDoesNotWedgeNotifiers (liveness: slow watcher must not wedge notifiers)", concurrentRunUUID)
	m := New(nil)

	// A never-drained watcher with the smallest buffer; it will overflow on the
	// 2nd matching Put and must be demoted, not block anyone.
	slow := m.Watch("/wedge/", 1, 1)

	const putters = 6
	const putsEach = 300
	var wg sync.WaitGroup
	var completed atomic.Int64

	for p := 0; p < putters; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < putsEach; i++ {
				// Every Put matches the slow watcher's prefix, so the fan-out
				// targets its full buffer every time after the first.
				_ = m.Put("/wedge/k", "v")
				completed.Add(1)
			}
		}()
	}
	wg.Wait() // if Put blocked on the full buffer, this never returns -> -timeout

	if got := completed.Load(); got != int64(putters*putsEach) {
		t.Fatalf("only %d/%d Puts completed — a slow watcher wedged the notifiers (blocking send regression)",
			got, putters*putsEach)
	}
	if got := m.CurrentRevision(); got != int64(putters*putsEach) {
		t.Fatalf("currentRev=%d, expected %d", got, putters*putsEach)
	}

	// The slow watcher overflowed -> must have been demoted out of synced, and
	// must still be tracked (not lost, not on a closed channel).
	g := m.GroupOf(slow)
	if g == GroupSynced {
		t.Fatalf("never-drained slow watcher should have been demoted out of synced, got %v", g)
	}
	sy, un, vi := m.Counts()
	if sy+un+vi != 1 {
		t.Fatalf("the single registered watcher must be tracked in exactly one map, got synced=%d unsynced=%d victim=%d", sy, un, vi)
	}
	t.Logf("settled: all %d concurrent Puts completed without wedging; slow watcher group=%v (tracked in 1 map)",
		completed.Load(), g)
}

// TestConcurrent_RegisteredWatcherReceivesPostRegistrationEvents proves the
// notification-correctness clause under concurrency: a watcher registered for
// FUTURE events (fromRev past currentRev) receives every matching event fired
// AFTER it registered, in strictly increasing revision order with no gaps and
// no duplicates, while OTHER concurrent watchers and non-matching Puts churn the
// manager. A watcher registered on a different prefix receives NONE of this
// watcher's events (no cross-watcher leak).
//
// This bites a manager that races the fan-out loop (delivering to the wrong
// watcher, dropping a matching event, or double-delivering) — none of which a
// single-threaded test would surface.
func TestConcurrent_RegisteredWatcherReceivesPostRegistrationEvents(t *testing.T) {
	t.Logf("run-UUID=%s test=RegisteredWatcherReceivesPostRegistrationEvents (delivery correctness under churn)", concurrentRunUUID)
	m := New(nil)

	// Lay down some history first so currentRev > 0; our target registers for
	// the FUTURE (synced), so it must NOT see any of this history.
	for i := 0; i < 50; i++ {
		m.Put("/target/old", "old")
		m.Put("/noise/old", "old")
	}
	startRev := m.CurrentRevision()

	// Register the target for future events only, with a buffer large enough to
	// hold every matching event we will fire, so live delivery never overflows.
	const matching = 500
	target := m.Watch("/target/", startRev+1, matching+16)
	if g := m.GroupOf(target); g != GroupSynced {
		t.Fatalf("future watcher must start synced, got %v", g)
	}
	// A second watcher on a disjoint prefix: must receive none of /target/.
	other := m.Watch("/disjoint/", startRev+1, 256)

	var wg sync.WaitGroup

	// Goroutine A: fire the matching events for target.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < matching; i++ {
			m.Put("/target/k", "v")
		}
	}()
	// Goroutine B: concurrent non-matching noise (must not reach target).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < matching; i++ {
			m.Put("/noise/k", "n")
		}
	}()
	// Goroutine C: events for the disjoint watcher (must not reach target).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			m.Put("/disjoint/k", "d")
		}
	}()
	wg.Wait()

	// Drain the target. Because its buffer holds all matching events and it was
	// synced for every matching Put, it must have received exactly `matching`
	// events, all with key /target/k, strictly increasing, all with rev>startRev.
	got := drain(target)
	if len(got) != matching {
		t.Fatalf("target received %d events, expected exactly %d matching post-registration events (race dropped/duplicated delivery?)", len(got), matching)
	}
	var prev int64
	seen := make(map[int64]bool)
	for i, ev := range got {
		if ev.Key != "/target/k" {
			t.Fatalf("event %d had key %q — cross-prefix leak into target", i, ev.Key)
		}
		if ev.Revision <= startRev {
			t.Fatalf("event %d revision %d <= startRev %d — target received a pre-registration (historical) event", i, ev.Revision, startRev)
		}
		if ev.Revision <= prev {
			t.Fatalf("event %d revision %d not strictly increasing (prev %d) — ordering/gap race", i, ev.Revision, prev)
		}
		if seen[ev.Revision] {
			t.Fatalf("revision %d delivered twice — duplicate fan-out race", ev.Revision)
		}
		seen[ev.Revision] = true
		prev = ev.Revision
	}

	// The disjoint watcher must contain ONLY /disjoint/k events (no /target/k).
	od := drain(other)
	for _, ev := range od {
		if ev.Key == "/target/k" {
			t.Fatalf("disjoint watcher received target's event rev %d — cross-watcher delivery race", ev.Revision)
		}
	}
	t.Logf("settled: target got exactly %d in-order unique /target/ events (>startRev=%d); disjoint watcher saw %d events, none from /target/",
		len(got), startRev, len(od))
}
