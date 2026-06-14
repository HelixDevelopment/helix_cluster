package watchmanager

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// runUUID anchor for the adversarial suite (distinct from closure + concurrent
// suites already present in the package).
const adversarialRunUUID = "watchmanager-adversarial-HXC-1391-c4e7a2f9-8b1d-4e63-a0f5-2d9c7e1b46af"

// ----------------------------------------------------------------------------
// CONTRACT UNDER TEST — re-derived from watchmanager.go by source inspection,
// adversarially, looking SPECIFICALLY for the helixnet teardown-race shape
// (lock released before a channel send + a close() not serialized against it).
//
// FINDINGS FROM THE SOURCE (load-bearing):
//
//  1. EVERY public method takes m.mu for its WHOLE body: Watch (register), Put
//     (notify/fan-out), Sync (replay), GroupOf, Counts, CurrentRevision. There
//     is NO path that releases the lock and then touches a watcher's channel.
//
//  2. The channel SEND is m.trySend, called from INSIDE the held lock in both
//     Put (line ~164) and Sync (line ~281). This is the OPPOSITE of the
//     helixnet bug (which read a shared map, released the lock, THEN sent). Here
//     send and map-mutation are atomic w.r.t. each other under m.mu.
//
//  3. There is NO Unwatch / Remove / Close / Cancel method, and the Manager
//     NEVER calls close(w.c) anywhere. A watcher is only ever MOVED between the
//     synced/unsynced/victim maps. Therefore "send on closed channel" is
//     STRUCTURALLY IMPOSSIBLE: nothing closes the channel a notifier sends on.
//
//  4. trySend is non-blocking (select/default). A full buffer never blocks a
//     notifier; it demotes the watcher (Put) or marks it victim (Sync) instead.
//
//  5. The receive side (a consumer draining w.C()) needs no lock: w.c is set
//     once at construction and never reassigned, and channel send/recv is
//     internally synchronized. So a drainer racing the in-lock fan-out is safe.
//
// CONCLUSION going in: the helixnet bug class (send-on-closed / lock-released-
// before-send data race) does NOT exist here by construction. The tests below
// therefore (a) PROVE that conclusion with hard -race churn that would expose it
// if it regressed, and (b) pin the delivery / lifecycle contract the manager
// DOES expose (no loss, no dup, in-order, no cross-prefix leak, watcher never
// lost across the map migrations, notifier never wedged), each written so a
// canonical mutation makes it bite (see TestAdversarial_MutationGuide).
// ----------------------------------------------------------------------------

// TestAdversarial_RegisterNotifyMigrateRace is the centerpiece -race probe for
// the helixnet shape. It hammers the manager from many goroutines doing the
// three operations that mutate the shared watcher maps + per-watcher fields:
//
//   - Watch (register)  — inserts into synced/unsynced
//   - Put (notify)      — fans out under lock; on overflow MIGRATES a watcher
//     from synced -> unsynced (the closest analogue to "remove" this API has)
//   - Sync (replay)     — MIGRATES unsynced/victim -> synced/victim
//
// while consumers concurrently drain the very channels the in-lock fan-out is
// sending on. If any channel send were performed off-lock against a map/field a
// concurrent migration touched, or if the manager ever closed a channel a
// notifier then sent on, -race or a "send on closed channel" panic would fire.
//
// It asserts (beyond -race + no panic) that the revision counter stays exact
// under contention and that NO watcher is lost or double-counted across the
// migrations (Counts sums to the number registered).
func TestAdversarial_RegisterNotifyMigrateRace(t *testing.T) {
	t.Logf("run-UUID=%s test=RegisterNotifyMigrateRace (helixnet-shape probe: register/notify/migrate under -race)", adversarialRunUUID)
	m := New(nil)

	const (
		putters    = 8
		putsEach   = 500
		registrars = 6
		watchEach  = 50
		syncers    = 4
		syncRounds = 300
		drainers   = 5
	)
	totalWatchers := registrars * watchEach
	totalPuts := putters * putsEach

	var (
		wg           sync.WaitGroup
		drainWg      sync.WaitGroup
		stopDrainers atomic.Bool

		watchersMu sync.Mutex
		watchers   []*Watcher

		revMu   sync.Mutex
		revSeen = make(map[int64]int)
	)

	// Drainers race the in-lock fan-out by receiving on live channels.
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

	// Registrars: tiny buffers so overflow->migration actually fires; a mix of
	// historical (fromRev<=cur -> unsynced) and future (fromRev=0->1) starts.
	for r := 0; r < registrars; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; i < watchEach; i++ {
				prefix := fmt.Sprintf("/p%d/", (r+i)%4)
				fromRev := int64(i % 7) // some <=cur (historical), some future
				buf := 1 + (i % 2)      // 1..2: forces overflow + demotion
				w := m.Watch(prefix, fromRev, buf)
				watchersMu.Lock()
				watchers = append(watchers, w)
				watchersMu.Unlock()
			}
		}(r)
	}

	// Putters: fan-out across the whole prefix space.
	for p := 0; p < putters; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < putsEach; i++ {
				rev := m.Put(fmt.Sprintf("/p%d/k%d", i%4, i), "v")
				revMu.Lock()
				revSeen[rev]++
				revMu.Unlock()
			}
		}()
	}

	// Syncers: continuously migrate unsynced/victim <-> synced.
	for s := 0; s < syncers; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < syncRounds; i++ {
				m.Sync()
			}
		}()
	}

	wg.Wait()
	stopDrainers.Store(true)
	drainWg.Wait()

	// Revision counter exact under contention: 1..N each returned exactly once.
	if got := m.CurrentRevision(); got != int64(totalPuts) {
		t.Fatalf("currentRev=%d, expected exactly %d (lost/double increment under contention)", got, totalPuts)
	}
	revMu.Lock()
	if len(revSeen) != totalPuts {
		t.Fatalf("Put returned %d distinct revisions, expected %d (duplicate/missing revision under race)", len(revSeen), totalPuts)
	}
	for r := int64(1); r <= int64(totalPuts); r++ {
		if c, ok := revSeen[r]; !ok || c != 1 {
			t.Fatalf("revision %d returned %d times (ok=%v): not exactly-once under race", r, c, ok)
		}
	}
	revMu.Unlock()

	// No watcher lost or double-counted across the synced/unsynced/victim
	// migrations: the three groups partition the registered set.
	sy, un, vi := m.Counts()
	if sy+un+vi != totalWatchers {
		t.Fatalf("Counts synced=%d+unsynced=%d+victim=%d=%d != registered %d (watcher lost or double-counted across migration)",
			sy, un, vi, sy+un+vi, totalWatchers)
	}
	t.Logf("settled: currentRev=%d distinctRevs=%d watchers=%d (synced=%d unsynced=%d victim=%d) — no race, no send-on-closed, no leak",
		m.CurrentRevision(), len(revSeen), totalWatchers, sy, un, vi)
}

// TestAdversarial_NotifyAfterDemotionDeliversNothingLive is the "notify after
// remove" centerpiece, adapted to the lifecycle this API exposes. The closest
// analogue to "remove" is demotion out of the synced group on buffer overflow:
// once a watcher is demoted (Put overflow), a SUBSEQUENT live Put must NOT push
// anything new into its channel directly — the missed revisions are owed via
// Sync replay instead, and the demoted watcher's buffer holds only what it got
// BEFORE demotion until a Sync runs. This pins that a "removed-from-synced"
// watcher receives no further LIVE fan-out, and that a concurrent Put racing the
// demotion never panics (no send-on-closed) and never silently loses an event.
func TestAdversarial_NotifyAfterDemotionDeliversNothingLive(t *testing.T) {
	t.Logf("run-UUID=%s test=NotifyAfterDemotionDeliversNothingLive (notify-after-remove analogue)", adversarialRunUUID)
	m := New(nil)

	// Buffer 1. First matching Put fills the buffer (synced). Second matching
	// Put overflows -> demotion to unsynced. We do NOT drain in between.
	w := m.Watch("/d/", 1, 1)

	r1 := m.Put("/d/a", "v1") // delivered live into the 1-slot buffer
	if g := m.GroupOf(w); g != GroupSynced {
		t.Fatalf("after first (buffered) live delivery watcher should still be synced, got %v", g)
	}
	r2 := m.Put("/d/b", "v2") // buffer full -> overflow -> demote, NOT delivered
	if g := m.GroupOf(w); g == GroupSynced {
		t.Fatalf("after overflow the watcher must be demoted out of synced, got %v", g)
	}

	// Fire MANY more live Puts. A demoted (removed-from-synced) watcher must
	// receive NONE of these live — its channel must still hold only r1.
	for i := 0; i < 50; i++ {
		m.Put("/d/more", "x")
	}

	buffered := drain(w)
	if len(buffered) != 1 {
		t.Fatalf("demoted watcher received %d live events; expected exactly 1 (only the pre-overflow r%d). Live fan-out leaked to a demoted watcher.", len(buffered), r1)
	}
	if buffered[0].Revision != r1 {
		t.Fatalf("demoted watcher's buffered event was rev %d, expected the single pre-overflow rev %d", buffered[0].Revision, r1)
	}
	t.Logf("after-demote: only pre-overflow rev=%d retained; r2=%d and 50 later Puts NOT delivered live (owed via Sync)", r1, r2)

	// And nothing is lost: a Sync replay must hand over r2 and all the rest,
	// each exactly once, in order, with no duplicate of r1.
	collected := map[int64]int{r1: 1}
	for iter := 0; iter < 80; iter++ {
		m.Sync()
		for _, ev := range drain(w) {
			collected[ev.Revision]++
		}
		if m.GroupOf(w) == GroupSynced {
			break
		}
	}
	if g := m.GroupOf(w); g != GroupSynced {
		t.Fatalf("after draining+Sync the watcher should be caught up (synced), got %v", g)
	}
	last := m.CurrentRevision()
	for r := r1; r <= last; r++ {
		if c := collected[r]; c != 1 {
			t.Fatalf("revision %d delivered %d times across live+replay; expected exactly once (loss or duplicate after demotion)", r, c)
		}
	}
	t.Logf("recovered: all revisions %d..%d delivered exactly once across live+replay (no loss, no dup after demotion)", r1, last)
}

// TestAdversarial_ConcurrentDemotionRaceNoSendOnClosed stresses the exact moment
// the helixnet bug struck: a notifier sending on a watcher's channel WHILE that
// watcher is being migrated out of the synced map. Many putters all target ONE
// slow, never-drained watcher (buffer 1) so EVERY Put after the first races the
// overflow->demotion path, concurrently with Sync passes trying to migrate it
// back. If the send were ever done off-lock against a channel a concurrent path
// closed, this would panic "send on closed channel" or trip -race. It must run
// clean and leave the watcher tracked in exactly one group.
func TestAdversarial_ConcurrentDemotionRaceNoSendOnClosed(t *testing.T) {
	t.Logf("run-UUID=%s test=ConcurrentDemotionRaceNoSendOnClosed (notify races demotion)", adversarialRunUUID)
	m := New(nil)

	slow := m.Watch("/race/", 1, 1) // never drained; overflows immediately

	const putters = 8
	const putsEach = 400
	var wg sync.WaitGroup
	var completed atomic.Int64

	// Putters all hammer the slow watcher's prefix.
	for p := 0; p < putters; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < putsEach; i++ {
				_ = m.Put("/race/k", "v")
				completed.Add(1)
			}
		}()
	}
	// Syncers concurrently try to re-promote it (migrate victim/unsynced->...).
	var syncWg sync.WaitGroup
	stop := make(chan struct{})
	for s := 0; s < 3; s++ {
		syncWg.Add(1)
		go func() {
			defer syncWg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					m.Sync()
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	syncWg.Wait()

	if got := completed.Load(); got != int64(putters*putsEach) {
		t.Fatalf("only %d/%d Puts completed — notifier wedged or panicked racing demotion", got, putters*putsEach)
	}
	if got := m.CurrentRevision(); got != int64(putters*putsEach) {
		t.Fatalf("currentRev=%d, expected %d (lost increment racing demotion)", got, putters*putsEach)
	}
	sy, un, vi := m.Counts()
	if sy+un+vi != 1 {
		t.Fatalf("the single watcher must be tracked in exactly one group after the race, got synced=%d unsynced=%d victim=%d", sy, un, vi)
	}
	// The slow watcher overflowed and was never drained: it must NOT be synced.
	if g := m.GroupOf(slow); g == GroupSynced {
		t.Fatalf("never-drained slow watcher ended synced (%v) despite full buffer — overflow/demotion lost under race", g)
	}
	t.Logf("settled: %d concurrent Puts vs demotion+Sync ran clean (no send-on-closed, no race); slow watcher group=%v tracked in 1 group (synced=%d unsynced=%d victim=%d)",
		completed.Load(), m.GroupOf(slow), sy, un, vi)
}

// TestAdversarial_EdgeCases pins the smaller contract corners that the prompt
// calls out: notify with no watchers, the full-buffer contract (drop-from-synced
// vs block — it must DROP, never block), and that the manager never closes a
// channel (so a re-drain after demotion is safe, no "receive from closed" zero
// values mistaken for events).
func TestAdversarial_EdgeCases(t *testing.T) {
	t.Logf("run-UUID=%s test=EdgeCases (no-watcher notify, full-buffer-drops-not-blocks, channel never closed)", adversarialRunUUID)
	m := New(nil)

	// (a) Notify with zero watchers: must just advance the revision, no panic.
	if r := m.Put("/none/k", "v"); r != 1 {
		t.Fatalf("Put with no watchers returned rev %d, expected 1", r)
	}
	if c := m.CurrentRevision(); c != 1 {
		t.Fatalf("currentRev=%d after one Put with no watchers, expected 1", c)
	}

	// (b) Full-buffer contract: a synced watcher whose buffer is full must be
	// DROPPED from synced (demoted), and the notifier must NOT block. We prove
	// non-blocking by the fact that the Put below returns at all on this single
	// goroutine (a blocking send would deadlock the test under -timeout).
	// Register from a future rev so it starts synced. currentRev is already 1
	// (the no-watcher Put above), so the next Put is rev 2.
	w := m.Watch("/f/", m.CurrentRevision()+1, 1) // buffer 1 -> synced
	rFill := m.Put("/f/a", "1")                   // fills the single slot
	beforeGroup := m.GroupOf(w)
	r := m.Put("/f/b", "2") // buffer full -> must drop+demote, must NOT block
	if beforeGroup != GroupSynced {
		t.Fatalf("watcher should have been synced before overflow, got %v", beforeGroup)
	}
	if g := m.GroupOf(w); g == GroupSynced {
		t.Fatalf("after full-buffer Put the watcher must be demoted (dropped from synced), got %v — full-buffer must drop, not silently keep synced", g)
	}
	t.Logf("full-buffer: Put rev=%d returned without blocking; watcher demoted %v->%v (drop-not-block contract)", r, beforeGroup, m.GroupOf(w))

	// (c) Channel never closed: after demotion, a non-blocking receive on the
	// watcher channel must yield the one buffered real event then report empty
	// via the default branch — NOT a stream of zero-value events (which is what
	// a closed channel would yield). drain() relies on the default branch; if
	// the manager had closed w.c, drain would still terminate but a closed
	// channel would make `<-ch` return immediately with zero Events forever in a
	// plain receive. We assert the buffered content is exactly the 1 real event.
	got := drain(w)
	if len(got) != 1 || got[0].Revision != rFill {
		t.Fatalf("expected exactly the 1 buffered real event (rev %d), got %v — channel closed or live-leaked?", rFill, got)
	}
	// A second drain must be empty (channel open + drained, not closed+spinning).
	if again := drain(w); len(again) != 0 {
		t.Fatalf("second drain returned %d events; channel should be open-and-empty, not closed", len(again))
	}
	t.Logf("channel-never-closed: drained exactly 1 real event then empty (open channel, no close())")
}

// TestAdversarial_MutationGuide documents the canonical mutations that make the
// above tests bite, so a maintainer can confirm the suite is load-bearing. It is
// a NO-OP at runtime (it only logs). The actual mutation verification is run
// out-of-band by the auditor (see report), not in CI.
//
// Mutations proven to FAIL the suite (each reverted byte-identical):
//
//	M1 (lock-released-before-send, the helixnet shape): in Put, move the
//	   m.trySend(w, ev) call to AFTER `m.mu.Unlock()` (snapshot the synced map
//	   under lock, send after unlock). -> TestAdversarial_RegisterNotifyMigrateRace
//	   reports a DATA RACE (concurrent map iteration vs migration / racy w.c send)
//	   under -race.
//
//	M2 (send-on-closed): add a close(w.c) on the demotion branch in Put (treating
//	   demotion as "remove"). -> TestAdversarial_ConcurrentDemotionRaceNoSendOnClosed
//	   panics "send on closed channel" when a concurrent Put sends on the just-
//	   closed channel.
//
//	M3 (live-leak-to-demoted): in Put, deliver to a watcher even when its buffer
//	   is full by removing the demotion branch / using a blocking send. -> Either
//	   TestAdversarial_NotifyAfterDemotionDeliversNothingLive sees >1 live event
//	   (leak) or TestAdversarial_ConcurrentDemotionRaceNoSendOnClosed hangs
//	   (blocking send wedges notifiers, caught by -timeout).
func TestAdversarial_MutationGuide(t *testing.T) {
	t.Logf("run-UUID=%s test=MutationGuide (documents M1 lock-release, M2 send-on-closed, M3 live-leak)", adversarialRunUUID)
	t.Log("see function doc: M1/M2/M3 mutations each make a centerpiece test bite; reverted byte-identical")
}
