package heartbeatcoalescer

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// runUUIDConc tags every log line so the captured -race output is traceable to
// this adversarial wave.
const runUUIDConc = "hbc-conc-3b7d52e1-9a04-4c6f-bb18-HXC-RECV-RACE"

// shardName is the canonical id used throughout so the Coalescer (producer) and
// the Receiver (sink) agree on the universe under test.
func shardName(i int) string { return fmt.Sprintf("shard-%05d", i) }

// TestConcurrent_ReceiverNoRace_NoLostSource is the core adversarial test.
//
// CONTRACT ANCHORED: Receiver is "the peer side ... refreshes EVERY contained
// shard" (receiver.go). In the documented Multi-Raft model a single peer
// receives inbound heartbeat messages from MANY sources concurrently, so the
// last-seen map is hammered by overlapping Deliver calls. The unsynchronized
// map at receiver.go:28 is a real data race (proven below by removing the
// mutex). This test exercises that exact path.
//
// Note on semantics: Deliver is documented to refresh last-seen to the tick of
// THIS message; it carries no max/monotonic guarantee, so under a free-for-all
// the surviving value is whichever Deliver for that shard happened to write
// last. This test therefore asserts the properties the contract DOES promise,
// then pins latest-per-source deterministically with a post-barrier final round
// (see phase 2) so "newest information wins" is checked without inventing
// max-semantics the code never claims.
//
// What it proves, and why each assertion BITES a broken receiver:
//
//  1. -race clean: many goroutines Deliver concurrently. Without the r.mu guard
//     in Deliver, the Go race detector flags a WRITE/WRITE race on lastSeen and
//     this test FAILS — that is what makes the mutex fix load-bearing.
//
//  2. No source lost: every one of the nShards distinct shards must appear in
//     SeenShards() after the storm. A racing map can drop a concurrent insert
//     (lost update) -> distinct count < nShards -> Fatalf. Anti-bluff tooth: a
//     receiver that races its per-source map silently loses a shard's liveness
//     and would trigger a spurious election.
//
//  3. No torn write: every recorded last-seen must be a tick that was ACTUALLY
//     delivered for that shard (in the valid delivered set), never garbage from
//     a half-written map slot.
//
//  4. Latest-per-source preserved (phase 2): after the contended storm, a single
//     goroutine delivers a strictly-larger finalTick to every shard behind a
//     happens-before barrier (wg.Wait). With deliveries now serialized after the
//     storm, every shard MUST read back exactly finalTick. A lost/torn final
//     write -> stale tick -> Fatalf.
//
//  5. Counts conserved: the sum of entries reported delivered by every Deliver
//     call (atomic counter) must equal the number of heartbeats actually emitted.
func TestConcurrent_ReceiverNoRace_NoLostSource(t *testing.T) {
	const (
		nShards     = 600
		nGoroutines = 16
		nRounds     = 50
	)

	rx := NewReceiver(0)

	// validTick reports whether v is a tick some goroutine could have delivered
	// in the storm: tick = round*nGoroutines + g + 1 for round in [0,nRounds),
	// g in [0,nGoroutines). That is exactly the integer range [1, stormMax].
	stormMax := uint64((nRounds-1)*nGoroutines + (nGoroutines - 1) + 1)
	validStormTick := func(v uint64) bool { return v >= 1 && v <= stormMax }

	var deliveredEntries int64

	// Phase 1: contended storm. Every goroutine touches EVERY shard each round:
	// maximum contention on each map key, exactly where a lost update hides.
	var wg sync.WaitGroup
	for g := 0; g < nGoroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for round := 0; round < nRounds; round++ {
				tick := uint64(round*nGoroutines + g + 1)
				entries := make([]ShardHeartbeat, 0, nShards)
				for s := 0; s < nShards; s++ {
					entries = append(entries, ShardHeartbeat{
						ShardID:     shardName(s),
						Term:        tick,
						CommitIndex: tick,
					})
				}
				n := rx.Deliver(tick, Message{DestPeer: fmt.Sprintf("peer-%02d", g), Entries: entries})
				atomic.AddInt64(&deliveredEntries, int64(n))
			}
		}(g)
	}
	wg.Wait()

	seen := rx.SeenShards()
	t.Logf("run=%s test=ReceiverNoRace phase=storm goroutines=%d rounds=%d shards=%d distinct_seen=%d delivered_entries=%d storm_max_tick=%d",
		runUUIDConc, nGoroutines, nRounds, nShards, len(seen), atomic.LoadInt64(&deliveredEntries), stormMax)

	// (2) No source lost.
	if len(seen) != nShards {
		t.Fatalf("distinct shards seen = %d, want %d (a source was lost under concurrency)", len(seen), nShards)
	}
	// (3) No torn write: every surviving value is a real delivered tick.
	for s := 0; s < nShards; s++ {
		id := shardName(s)
		got, ok := rx.LastSeen(id)
		if !ok {
			t.Fatalf("shard %s never recorded (lost under concurrency)", id)
		}
		if !validStormTick(got) {
			t.Fatalf("shard %s last-seen=%d is not a delivered tick (torn/garbage write)", id, got)
		}
	}

	// Phase 2: latest-per-source. A strictly larger finalTick is delivered to
	// every shard by competing goroutines, but ALL of it happens-after the
	// phase-1 barrier and every phase-2 delivery uses the SAME finalTick, so the
	// deterministic outcome is: every shard reads back exactly finalTick. This
	// pins "newest information survives" without assuming max-semantics.
	finalTick := stormMax + 1
	var wg2 sync.WaitGroup
	for g := 0; g < nGoroutines; g++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			for s := 0; s < nShards; s++ {
				rx.Deliver(finalTick, Message{
					DestPeer: "final",
					Entries:  []ShardHeartbeat{{ShardID: shardName(s), Term: finalTick, CommitIndex: finalTick}},
				})
			}
		}()
	}
	wg2.Wait()

	mism := 0
	for s := 0; s < nShards; s++ {
		id := shardName(s)
		got, _ := rx.LastSeen(id)
		if got != finalTick {
			mism++
		}
	}
	t.Logf("run=%s test=ReceiverNoRace phase=final final_tick=%d mismatched_shards=%d", runUUIDConc, finalTick, mism)
	if mism != 0 {
		t.Fatalf("%d shards did not reflect final tick %d (lost/torn update under concurrency)", mism, finalTick)
	}

	// (5) Counts conserved (phase 1 only; the atomic counter is summed there).
	wantEntries := int64(nGoroutines) * int64(nRounds) * int64(nShards)
	if got := atomic.LoadInt64(&deliveredEntries); got != wantEntries {
		t.Fatalf("delivered entries = %d, want %d (heartbeats dropped)", got, wantEntries)
	}
}

// TestConcurrent_PerSourceIsolation proves clause 2 (per-source isolation) under
// concurrency: N goroutines each OWN one distinct shard and deliver only their
// own shard concurrently. Afterwards there must be exactly N distinct entries,
// each at its own independently tracked tick. No goroutine's shard may leak into
// another's slot, and no shard may be missing.
//
// BITES: a receiver that shared state incorrectly across keys (or raced inserts)
// would yield distinct_seen != N or a wrong per-shard tick. With the map guarded
// this is exact and -race clean.
func TestConcurrent_PerSourceIsolation(t *testing.T) {
	const nSources = 256

	rx := NewReceiver(0)

	var wg sync.WaitGroup
	for s := 0; s < nSources; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			id := shardName(s)
			// Each source's final tick is unique and == s+1.
			for tick := uint64(1); tick <= uint64(s+1); tick++ {
				rx.Deliver(tick, Message{
					DestPeer: "single-peer",
					Entries:  []ShardHeartbeat{{ShardID: id, Term: tick, CommitIndex: tick}},
				})
			}
		}(s)
	}
	wg.Wait()

	seen := rx.SeenShards()
	t.Logf("run=%s test=PerSourceIsolation sources=%d distinct_seen=%d", runUUIDConc, nSources, len(seen))

	if len(seen) != nSources {
		t.Fatalf("distinct sources tracked = %d, want %d (isolation broken / source lost)", len(seen), nSources)
	}
	// Each source must hold ITS OWN final tick (s+1), proving no cross-source
	// contamination of the per-shard slot.
	for s := 0; s < nSources; s++ {
		id := shardName(s)
		got, ok := seen[id]
		if !ok {
			t.Fatalf("source %s missing (lost)", id)
		}
		if want := uint64(s + 1); got != want {
			t.Fatalf("source %s tracked tick=%d, want its own %d (cross-source contamination)", id, got, want)
		}
	}
}

// TestConcurrent_CoalescedFanoutToManyPeers is the realistic end-to-end scenario:
// ONE coalesced producer tick yields O(peers) per-peer messages (the documented
// coalescing win), and those per-peer messages are delivered CONCURRENTLY by one
// goroutine per peer link — modelling a node whose inbound links from each peer
// run in parallel. Despite the concurrent multi-message delivery, the liveness
// invariant must hold exactly: EVERY shard refreshed at the tick, none failed.
//
// This binds the Coalescer's "one message per peer, every shard's heartbeat
// preserved" contract to the Receiver's "refresh every contained shard" contract
// under -race. A racing receiver would drop a shard from one of the concurrent
// per-peer deliveries -> a shard ends up failed -> spurious election -> Fatalf.
func TestConcurrent_CoalescedFanoutToManyPeers(t *testing.T) {
	peers := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		peers = append(peers, fmt.Sprintf("peer-%02d", i))
	}
	const nShards = 800

	c := New()
	allShards := make([]string, nShards)
	for i := 0; i < nShards; i++ {
		allShards[i] = shardName(i)
		c.Register(shardName(i), peers) // full mesh: every shard -> every peer
	}

	co := c.TickCoalesced()

	// Documented coalescing shape: exactly one message per distinct peer.
	if co.MsgCount != len(peers) {
		t.Fatalf("coalesced msg count = %d, want one per peer (%d)", co.MsgCount, len(peers))
	}
	// No heartbeat dropped at produce time: entries == shards*peers.
	if co.EntryCount != nShards*len(peers) {
		t.Fatalf("coalesced entries = %d, want %d", co.EntryCount, nShards*len(peers))
	}

	rx := NewReceiver(0)
	const tick = uint64(1)

	// Deliver each per-peer message on its own goroutine: concurrent inbound
	// links converging on one receiver map.
	var refreshed int64
	var wg sync.WaitGroup
	for _, m := range co.Messages {
		wg.Add(1)
		go func(m Message) {
			defer wg.Done()
			atomic.AddInt64(&refreshed, int64(rx.Deliver(tick, m)))
		}(m)
	}
	wg.Wait()

	failed := rx.FailedShards(allShards, tick)
	t.Logf("run=%s test=CoalescedFanout peers=%d shards=%d refreshed_entries=%d distinct_seen=%d failed=%d",
		runUUIDConc, len(peers), nShards, atomic.LoadInt64(&refreshed), len(rx.SeenShards()), len(failed))

	if got := len(rx.SeenShards()); got != nShards {
		t.Fatalf("distinct shards refreshed = %d, want %d (a shard lost across concurrent per-peer deliveries)", got, nShards)
	}
	if len(failed) != 0 {
		bound := failed
		if len(bound) > 5 {
			bound = bound[:5]
		}
		t.Fatalf("%d shards failed after concurrent coalesced delivery (spurious election): %v...", len(failed), bound)
	}
	if want := int64(nShards * len(peers)); atomic.LoadInt64(&refreshed) != want {
		t.Fatalf("refreshed entries = %d, want %d", refreshed, want)
	}
}

// TestWindowBoundary_FailureWindow anchors clause 4 to the documented failure
// window. The Coalescer carries no injectable clock; the LOGICAL clock is the
// `now` tick passed to the Receiver (IsFailed: failed when now-lastSeen >
// FailureWindow). This test asserts the boundary PRECISELY around that window,
// and that a fresh delivery re-coalesces a new live interval.
//
// BITES: an off-by-one in the window comparison (>= vs >) flips the boundary
// rows below; a delivery that fails to refresh leaves the shard failed past the
// window. Run as part of the same wave; it is single-threaded by design (it
// pins the exact boundary, which concurrency would blur).
func TestWindowBoundary_FailureWindow(t *testing.T) {
	const window = 3
	rx := NewReceiver(window)

	c := New()
	c.Register("shard-W", []string{"peerW"})
	co := c.TickCoalesced() // tick logically delivered at now=10 below
	const seenAt = uint64(10)
	rx.DeliverAll(seenAt, co.Messages)

	type row struct {
		now      uint64
		wantFail bool
		why      string
	}
	rows := []row{
		{seenAt, false, "same tick: now-last=0 <= window"},
		{seenAt + window, false, "now-last==window: boundary still alive (> not >=)"},
		{seenAt + window + 1, true, "now-last==window+1: just past window -> failed"},
		{seenAt + 100, true, "far past window -> failed"},
	}
	for _, r := range rows {
		got := rx.IsFailed("shard-W", r.now)
		t.Logf("run=%s test=WindowBoundary now=%d last=%d window=%d failed=%v (%s)",
			runUUIDConc, r.now, seenAt, window, got, r.why)
		if got != r.wantFail {
			t.Fatalf("IsFailed(now=%d) = %v, want %v (%s)", r.now, got, r.wantFail, r.why)
		}
	}

	// A fresh delivery at a later tick re-arms liveness: the shard that was
	// failed at now=seenAt+100 becomes alive again, proving coalescing starts a
	// new live interval rather than carrying stale state.
	const refreshAt = uint64(200)
	rx.DeliverAll(refreshAt, c.TickCoalesced().Messages)
	if rx.IsFailed("shard-W", refreshAt) {
		t.Fatalf("shard-W still failed at now=%d immediately after fresh delivery", refreshAt)
	}
	if !rx.IsFailed("shard-W", refreshAt+window+1) {
		t.Fatalf("shard-W not failed at now=%d (window not re-armed correctly)", refreshAt+window+1)
	}
}
