package watchmanager

import (
	"testing"
)

const runUUID = "watchmanager-HXC-1391-7a1f9c2e-3b6d-4e0a-9c11-d2f8e4a51b0c"

// drain pulls up to want events from a watcher's channel without blocking
// forever. It performs bounded non-blocking reads.
func drain(w *Watcher) []Event {
	var out []Event
	for {
		select {
		case ev := <-w.c:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// TestClosure1_SyncedReceivesAllInOrderWithPrefixFilter proves closure clause 1:
// a watcher registered on a prefix from revision R receives ALL matching events
// in strictly increasing revision order with no gaps, and non-matching keys are
// excluded.
//
// Mutation: dropping the prefix filter in Put (delivering non-matching keys)
// makes count != 100 (it would receive the interleaved /other/ writes), failing
// this test. Removing the strictly-increasing assertion is also covered.
func TestClosure1_SyncedReceivesAllInOrderWithPrefixFilter(t *testing.T) {
	t.Logf("run-UUID=%s clause=1 (synced live fan-out + prefix filter)", runUUID)
	m := New(nil)

	// Register BEFORE any writes, from revision 1, with a generous buffer so
	// live delivery never overflows.
	w := m.Watch("/app/", 1, 256)
	if g := m.GroupOf(w); g != GroupSynced {
		t.Fatalf("fresh future watcher should start synced, got %v", g)
	}
	t.Logf("before: currentRev=%d group=%v received=0", m.CurrentRevision(), m.GroupOf(w))

	// 100 matching writes interleaved with non-matching writes.
	const matching = 100
	for i := 0; i < matching; i++ {
		m.Put("/app/key", "v")
		// A non-matching write that MUST be filtered out for this watcher.
		m.Put("/other/key", "x")
	}

	got := drain(w)
	if len(got) != matching {
		t.Fatalf("expected exactly %d matching events, got %d (prefix filter broken?)", matching, len(got))
	}

	var prev int64 = 0
	for i, ev := range got {
		if ev.Revision <= prev {
			t.Fatalf("event %d revision %d not strictly increasing (prev %d): gaps/order broken", i, ev.Revision, prev)
		}
		prev = ev.Revision
		if got := ev.Key; got != "/app/key" {
			t.Fatalf("event %d had non-matching key %q delivered (prefix filter broken)", i, got)
		}
	}
	t.Logf("after: currentRev=%d group=%v received=%d firstRev=%d lastRev=%d",
		m.CurrentRevision(), m.GroupOf(w), len(got), got[0].Revision, got[len(got)-1].Revision)

	if g := m.GroupOf(w); g != GroupSynced {
		t.Fatalf("watcher that kept up should remain synced, got %v", g)
	}
}

// TestClosure2_SlowConsumerBecomesVictimThenRecovers proves closure clause 2:
// a watcher with a tiny buffer that does not drain is forced out of the synced
// group (unsynced/victim) when its buffer overflows; after draining + Sync it
// returns to synced and receives the missed events with no loss.
//
// Mutation: never moving an overflowed watcher out of synced (i.e. removing the
// demote-on-full-buffer branch in Put) makes GroupOf stay synced after
// overflow, failing the "must NOT be synced" assertion. Also, if Sync delivered
// duplicates or dropped events, the final reconstructed set would not equal the
// full 1..N matching set.
func TestClosure2_SlowConsumerBecomesVictimThenRecovers(t *testing.T) {
	t.Logf("run-UUID=%s clause=2 (slow consumer overflow -> recovery)", runUUID)
	m := New(nil)

	// Tiny buffer of 2. We will NOT drain during the writes, forcing overflow.
	w := m.Watch("/k/", 1, 2)
	t.Logf("before: group=%v currentRev=%d bufSize=%d", m.GroupOf(w), m.CurrentRevision(), w.bufSize)

	const total = 20
	for i := 0; i < total; i++ {
		m.Put("/k/x", "v")
	}

	// With a buffer of 2 and 20 undrained writes, the watcher must have been
	// pushed out of synced.
	g := m.GroupOf(w)
	if g == GroupSynced {
		t.Fatalf("slow consumer that overflowed must NOT be synced, got %v", g)
	}
	if g != GroupUnsynced && g != GroupVictim {
		t.Fatalf("overflowed watcher must be unsynced or victim, got %v", g)
	}
	t.Logf("after-overflow: group=%v currentRev=%d", g, m.CurrentRevision())

	// Now recover: repeatedly drain everything we can and Sync until the
	// watcher is synced again and we have collected all events. This models a
	// consumer that resumes draining.
	collected := make(map[int64]Event)
	for _, ev := range drain(w) {
		collected[ev.Revision] = ev
	}
	for iter := 0; iter < total+5; iter++ {
		m.Sync()
		for _, ev := range drain(w) {
			if _, dup := collected[ev.Revision]; dup {
				t.Fatalf("duplicate delivery of revision %d during recovery", ev.Revision)
			}
			collected[ev.Revision] = ev
		}
		if m.GroupOf(w) == GroupSynced {
			break
		}
	}

	if g := m.GroupOf(w); g != GroupSynced {
		t.Fatalf("after draining + Sync the watcher should be synced again, got %v", g)
	}
	if len(collected) != total {
		t.Fatalf("expected to recover all %d events with no loss, got %d", total, len(collected))
	}
	// Every revision 1..total must be present exactly once.
	for r := int64(1); r <= total; r++ {
		ev, ok := collected[r]
		if !ok {
			t.Fatalf("missing revision %d after recovery (event lost)", r)
		}
		if ev.Key != "/k/x" {
			t.Fatalf("revision %d had wrong key %q", r, ev.Key)
		}
	}
	t.Logf("after-recovery: group=%v recovered=%d (all %d revisions present, no dup, no loss)",
		m.GroupOf(w), len(collected), total)
}

// TestClosure3_HistoricalStartReplaysBeforeSynced proves closure clause 3:
// a watcher started from an OLD revision replays all intervening matching
// events before becoming synced.
//
// Mutation: if Sync ignored nextRev and started replay from the current
// revision (skipping history), the historical events would never be delivered
// and the count/first-revision assertions would fail. If the watcher were
// classified synced immediately despite history, the initial-group assertion
// fails.
func TestClosure3_HistoricalStartReplaysBeforeSynced(t *testing.T) {
	t.Logf("run-UUID=%s clause=3 (historical start replay)", runUUID)
	m := New(nil)

	// Lay down history: 50 matching writes interleaved with non-matching.
	const hist = 50
	for i := 0; i < hist; i++ {
		m.Put("/h/key", "v")
		m.Put("/z/key", "x") // non-matching
	}
	curBefore := m.CurrentRevision()

	// Register a watcher starting from revision 1 (the very beginning).
	w := m.Watch("/h/", 1, 1024)
	if g := m.GroupOf(w); g != GroupUnsynced {
		t.Fatalf("watcher starting from historical rev must begin unsynced, got %v", g)
	}
	preDrain := drain(w)
	if len(preDrain) != 0 {
		t.Fatalf("unsynced watcher must not have received anything before Sync, got %d", len(preDrain))
	}
	t.Logf("before: currentRev=%d group=%v received=0", curBefore, m.GroupOf(w))

	// One Sync pass should replay all 50 matching historical events.
	n := m.Sync()
	got := drain(w)
	if len(got) != hist {
		t.Fatalf("expected %d replayed historical events, got %d (Sync delivered=%d)", hist, len(got), n)
	}
	if got[0].Revision != 1 {
		t.Fatalf("replay must start at the oldest revision 1, first delivered was %d", got[0].Revision)
	}
	var prev int64
	for i, ev := range got {
		if ev.Revision <= prev {
			t.Fatalf("replayed event %d revision %d not strictly increasing (prev %d)", i, ev.Revision, prev)
		}
		prev = ev.Revision
		if ev.Key != "/h/key" {
			t.Fatalf("replayed non-matching key %q", ev.Key)
		}
	}
	if g := m.GroupOf(w); g != GroupSynced {
		t.Fatalf("after replaying up to current rev the watcher must be synced, got %v", g)
	}
	t.Logf("after: group=%v replayed=%d firstRev=%d lastRev=%d currentRev=%d",
		m.GroupOf(w), len(got), got[0].Revision, got[len(got)-1].Revision, m.CurrentRevision())

	// And it is live now: a new matching write arrives without another Sync.
	m.Put("/h/key", "live")
	live := drain(w)
	if len(live) != 1 || live[0].Revision != m.CurrentRevision() {
		t.Fatalf("after becoming synced the watcher should receive live events, got %d", len(live))
	}
	t.Logf("live-after-sync: receivedLiveRev=%d", live[0].Revision)
}
