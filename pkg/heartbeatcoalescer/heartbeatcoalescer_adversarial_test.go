package heartbeatcoalescer

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// runUUIDAdv tags log lines so captured output is traceable to this adversarial
// audit wave (heartbeat coalescing receiver: latest-wins + threshold + race).
const runUUIDAdv = "hbc-adv-7c41e9a2-HXC-1649-AUDIT"

func advShard(i int) string { return fmt.Sprintf("ashard-%05d", i) }

// -----------------------------------------------------------------------------
// CENTERPIECE A: latest-wins coalescing at the PRODUCER.
//
// CONTRACT: TickCoalesced batches every shard heartbeat destined for a peer into
// ONE message, each entry carrying that shard's CURRENT (latest) Term/CommitIndex
// for the tick. Coalescing must keep the LATEST per (peer, shard) — never an
// older term — and must not DROP any shard's entry.
//
// We advance the producer several ticks so terms strictly grow, then assert that
// the coalesced message for every peer carries, for every shard, the latest term
// (== tick count) and the latest commit (== tick count). A stale entry surviving
// (older term winning) or a dropped entry both fail here.
//
// BITES: if TickCoalesced reused a stale per-shard heartbeat, or appended an old
// term, the term/commit assertion fails. If it dropped an entry, the per-peer
// entry-count assertion fails.
// -----------------------------------------------------------------------------
func TestAdv_LatestWinsCoalescing_ProducerKeepsLatestPerPeer(t *testing.T) {
	peers := []string{"pA", "pB", "pC"}
	const nShards = 50
	const nTicks = 7

	c := New()
	for i := 0; i < nShards; i++ {
		c.Register(advShard(i), peers)
	}

	var last TickResult
	for tk := 0; tk < nTicks; tk++ {
		last = c.TickCoalesced()
	}
	// After nTicks coalesced ticks, each shard's term has advanced exactly nTicks
	// times (term++ per tick), and commit == nTicks (commit++ per tick).
	wantTerm := uint64(nTicks)
	wantCommit := uint64(nTicks)

	t.Logf("run=%s test=LatestWins ticks=%d peers=%d shards=%d msgs=%d entries=%d want_term=%d want_commit=%d",
		runUUIDAdv, nTicks, len(peers), nShards, last.MsgCount, last.EntryCount, wantTerm, wantCommit)

	if last.MsgCount != len(peers) {
		t.Fatalf("coalesced msgs = %d, want one per peer (%d)", last.MsgCount, len(peers))
	}
	for _, m := range last.Messages {
		if len(m.Entries) != nShards {
			t.Fatalf("peer %s carries %d entries, want %d (a shard dropped)", m.DestPeer, len(m.Entries), nShards)
		}
		seen := make(map[string]bool, nShards)
		for _, e := range m.Entries {
			if seen[e.ShardID] {
				t.Fatalf("peer %s has duplicate entry for %s", m.DestPeer, e.ShardID)
			}
			seen[e.ShardID] = true
			// LATEST must win: term/commit are the most-recent tick's, not a stale one.
			if e.Term != wantTerm {
				t.Fatalf("peer %s shard %s term=%d, want latest %d (stale term survived coalescing)",
					m.DestPeer, e.ShardID, e.Term, wantTerm)
			}
			if e.CommitIndex != wantCommit {
				t.Fatalf("peer %s shard %s commit=%d, want latest %d (stale commit survived)",
					m.DestPeer, e.ShardID, e.CommitIndex, wantCommit)
			}
		}
		if len(seen) != nShards {
			t.Fatalf("peer %s distinct shards = %d, want %d", m.DestPeer, len(seen), nShards)
		}
	}
}

// CENTERPIECE A (burst): many heartbeats from one shard to one peer within a tick
// must coalesce to ONE entry carrying the latest — none lost, no stale duplicate.
// In this package a shard appears once per tick, so the "burst" is modelled by a
// shard registered with duplicate peers + repeated peers in its set: Register
// dedups peers, so the shard still yields exactly one entry per distinct peer
// with the latest term. This pins "duplicate fan-out coalesces, latest survives".
func TestAdv_BurstCoalescesToLatest_NoDuplicateNoLoss(t *testing.T) {
	c := New()
	// Same peer listed many times + duplicates: must dedup to one entry per peer.
	c.Register("burst-shard", []string{"pX", "pX", "pX", "pY", "pX", "pY"})

	co := c.TickCoalesced()
	t.Logf("run=%s test=Burst msgs=%d entries=%d", runUUIDAdv, co.MsgCount, co.EntryCount)

	// Exactly two distinct peers, each exactly one entry for burst-shard.
	if co.MsgCount != 2 {
		t.Fatalf("burst msgs = %d, want 2 distinct peers (dedup failed -> duplicate/lost)", co.MsgCount)
	}
	for _, m := range co.Messages {
		if len(m.Entries) != 1 {
			t.Fatalf("peer %s entries = %d, want exactly 1 (burst not coalesced to latest)", m.DestPeer, len(m.Entries))
		}
		if m.Entries[0].ShardID != "burst-shard" {
			t.Fatalf("peer %s carries wrong shard %q", m.DestPeer, m.Entries[0].ShardID)
		}
		// Single tick -> latest term is 1, commit 1.
		if m.Entries[0].Term != 1 || m.Entries[0].CommitIndex != 1 {
			t.Fatalf("peer %s entry term=%d commit=%d, want latest 1/1", m.DestPeer, m.Entries[0].Term, m.Entries[0].CommitIndex)
		}
	}
}

// -----------------------------------------------------------------------------
// CENTERPIECE B: Receiver last-writer-wins refresh semantics (the DOCUMENTED
// contract). Deliver refreshes last-seen to the tick of THIS message. The package
// explicitly does NOT claim max/monotonic semantics: whatever tick a Deliver
// carries becomes the stored value. This test PINS that documented behaviour in
// BOTH directions so the contract cannot silently drift:
//
//   - a LATER tick overwrites an earlier one (forward refresh), AND
//   - a Deliver with an EARLIER tick also overwrites (last-writer-wins, no guard).
//
// This is the precise inverse of a "monotonic max" receiver. If someone later
// added a max-guard (turning Deliver into max(old,new)), the "earlier overwrites"
// assertion below would fail — surfacing the semantic change for review rather
// than letting it slip in unannounced. This is contract-pinning, not a bug claim.
// -----------------------------------------------------------------------------
func TestAdv_Receiver_LastWriterWins_DocumentedSemantics(t *testing.T) {
	rx := NewReceiver(0)
	const id = "lww-shard"

	msg := func(tick uint64) Message {
		return Message{DestPeer: "p", Entries: []ShardHeartbeat{{ShardID: id, Term: tick, CommitIndex: tick}}}
	}

	// Forward: a later tick refreshes.
	rx.Deliver(5, msg(5))
	if got, ok := rx.LastSeen(id); !ok || got != 5 {
		t.Fatalf("after Deliver(5): last-seen=%d ok=%v, want 5/true", got, ok)
	}
	rx.Deliver(9, msg(9))
	if got, _ := rx.LastSeen(id); got != 9 {
		t.Fatalf("after Deliver(9): last-seen=%d, want 9 (later tick must refresh)", got)
	}

	// Backward: an EARLIER tick ALSO overwrites (documented last-writer-wins).
	// A monotonic-max receiver would keep 9; this package keeps 3.
	rx.Deliver(3, msg(3))
	got, _ := rx.LastSeen(id)
	t.Logf("run=%s test=LastWriterWins after_seq[5,9,3] last_seen=%d", runUUIDAdv, got)
	if got != 3 {
		t.Fatalf("after Deliver(3) following Deliver(9): last-seen=%d, want 3 "+
			"(documented last-writer-wins; a max-guard would wrongly keep 9)", got)
	}
}

// -----------------------------------------------------------------------------
// CENTERPIECE C (HXC-1649 regression guard): concurrent Deliver storm vs. query
// accessors under -race. Many goroutines hammer Deliver across all shards while
// other goroutines concurrently call the read accessors (IsFailed / FailedShards
// / SeenShards / LastSeen). With the RWMutex present this is race-clean and every
// shard ends seen; remove the lock around lastSeen and -race flags a WRITE/READ
// (or WRITE/WRITE) race here.
//
// BITES: dropping r.mu in Deliver (or any accessor) -> `go test -race` reports a
// data race on lastSeen and this test fails.
// -----------------------------------------------------------------------------
func TestAdv_Concurrent_DeliverVsQuery_NoRace(t *testing.T) {
	const (
		nShards  = 400
		nWriters = 12
		nReaders = 8
		nRounds  = 40
	)
	rx := NewReceiver(2)

	universe := make([]string, nShards)
	for i := range universe {
		universe[i] = advShard(i)
	}

	var delivered int64
	var stop int32
	var wg sync.WaitGroup

	// Writers: hammer Deliver across every shard each round.
	for w := 0; w < nWriters; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for round := 0; round < nRounds; round++ {
				tick := uint64(round*nWriters + w + 1)
				entries := make([]ShardHeartbeat, 0, nShards)
				for s := 0; s < nShards; s++ {
					entries = append(entries, ShardHeartbeat{ShardID: advShard(s), Term: tick, CommitIndex: tick})
				}
				n := rx.Deliver(tick, Message{DestPeer: fmt.Sprintf("w-%02d", w), Entries: entries})
				atomic.AddInt64(&delivered, int64(n))
			}
		}(w)
	}

	// Readers: concurrently query liveness while the storm runs. This is the
	// receive-vs-query contention HXC-1649 guards. We don't assert on the racy
	// intermediate values (they're nondeterministic by contract) — the assertion
	// is "-race clean + no panic"; the race detector is the oracle.
	for r := 0; r < nReaders; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			now := uint64(1)
			for atomic.LoadInt32(&stop) == 0 {
				_ = rx.IsFailed(advShard(r%nShards), now)
				_ = rx.FailedShards(universe, now)
				_ = rx.SeenShards()
				_, _ = rx.LastSeen(advShard((r + 1) % nShards))
				now++
			}
		}(r)
	}

	// Coordinate writers vs. readers without joining them separately: spin until
	// the writers have delivered their full total (so the storm is complete), then
	// signal readers to stop. Readers keep racing the map the entire time.
	wantDelivered := int64(nWriters) * int64(nRounds) * int64(nShards)
	for atomic.LoadInt64(&delivered) < wantDelivered {
		runtime.Gosched()
	}
	atomic.StoreInt32(&stop, 1)
	wg.Wait()

	seen := rx.SeenShards()
	t.Logf("run=%s test=DeliverVsQuery writers=%d readers=%d rounds=%d shards=%d distinct_seen=%d delivered=%d",
		runUUIDAdv, nWriters, nReaders, nRounds, nShards, len(seen), atomic.LoadInt64(&delivered))

	if len(seen) != nShards {
		t.Fatalf("distinct shards seen = %d, want %d (a source lost under concurrent receive+query)", len(seen), nShards)
	}
	if got := atomic.LoadInt64(&delivered); got != wantDelivered {
		t.Fatalf("delivered entries = %d, want %d", got, wantDelivered)
	}
}

// -----------------------------------------------------------------------------
// THRESHOLD: exact failure-window boundary (off-by-one guard). Failed iff
// now-lastSeen > FailureWindow. Probe just-before / at / just-after.
//
// BITES: flipping > to >= (or shifting the window by one) changes the at-boundary
// row from alive to failed.
// -----------------------------------------------------------------------------
func TestAdv_FailureThreshold_ExactBoundary(t *testing.T) {
	const window = 4
	rx := NewReceiver(window)
	const id = "thr-shard"
	const seenAt = uint64(20)
	rx.Deliver(seenAt, Message{DestPeer: "p", Entries: []ShardHeartbeat{{ShardID: id, Term: 1, CommitIndex: 1}}})

	type row struct {
		now      uint64
		wantFail bool
		why      string
	}
	rows := []row{
		{seenAt, false, "now-last=0 < window: alive"},
		{seenAt + window - 1, false, "now-last=window-1 < window: alive"},
		{seenAt + window, false, "now-last==window: NOT > window -> alive (boundary)"},
		{seenAt + window + 1, true, "now-last==window+1 > window -> failed"},
		{seenAt + window + 50, true, "far past -> failed"},
	}
	for _, r := range rows {
		got := rx.IsFailed(id, r.now)
		t.Logf("run=%s test=Threshold now=%d last=%d window=%d failed=%v (%s)",
			runUUIDAdv, r.now, seenAt, window, got, r.why)
		if got != r.wantFail {
			t.Fatalf("IsFailed(now=%d) = %v, want %v (%s)", r.now, got, r.wantFail, r.why)
		}
	}
}

// EDGES: unknown peer/shard, first heartbeat, duplicate (idempotent), empty
// receiver — none may panic; an unseen shard is failed.
func TestAdv_Edges_UnknownFirstDuplicateEmpty(t *testing.T) {
	// Empty receiver: query must not panic; unknown shard is failed and not seen.
	rx := NewReceiver(3)
	if _, ok := rx.LastSeen("nope"); ok {
		t.Fatalf("unknown shard reported seen on empty receiver")
	}
	if !rx.IsFailed("nope", 100) {
		t.Fatalf("unknown shard must be failed (never seen)")
	}
	if got := rx.FailedShards([]string{"a", "b"}, 5); len(got) != 2 {
		t.Fatalf("FailedShards on empty receiver = %v, want both failed", got)
	}
	if got := rx.SeenShards(); len(got) != 0 {
		t.Fatalf("SeenShards on empty receiver = %v, want empty", got)
	}

	// First heartbeat: becomes seen and alive within window.
	rx.Deliver(10, Message{DestPeer: "p", Entries: []ShardHeartbeat{{ShardID: "first", Term: 1, CommitIndex: 1}}})
	if _, ok := rx.LastSeen("first"); !ok {
		t.Fatalf("first heartbeat not recorded")
	}
	if rx.IsFailed("first", 10) {
		t.Fatalf("shard alive on its first heartbeat tick must not be failed")
	}

	// Duplicate heartbeat at same tick: idempotent (still one shard, same tick).
	rx.Deliver(10, Message{DestPeer: "p", Entries: []ShardHeartbeat{{ShardID: "first", Term: 1, CommitIndex: 1}}})
	if got, _ := rx.LastSeen("first"); got != 10 {
		t.Fatalf("duplicate same-tick deliver changed last-seen to %d, want 10", got)
	}
	if n := len(rx.SeenShards()); n != 1 {
		t.Fatalf("after duplicate deliver, distinct seen=%d, want 1 (not idempotent)", n)
	}

	// Empty-message Deliver: zero entries refreshed, no panic, no new shards.
	if n := rx.Deliver(11, Message{DestPeer: "p"}); n != 0 {
		t.Fatalf("empty-message Deliver refreshed %d entries, want 0", n)
	}
	if n := len(rx.SeenShards()); n != 1 {
		t.Fatalf("empty Deliver added shards: distinct seen=%d, want 1", n)
	}
}
