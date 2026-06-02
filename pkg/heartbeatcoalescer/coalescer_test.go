package heartbeatcoalescer

import (
	"fmt"
	"testing"
)

const runUUID = "hbc-9f2c1a7e-4d83-4b6a-8e21-HXC1388"

// build a coalescer with nShards spread round-robin across the given peers.
func buildEven(nShards int, peers []string) *Coalescer {
	c := New()
	for i := 0; i < nShards; i++ {
		// every shard talks to every peer (full mesh) -> exercises max fan-out
		c.Register(fmt.Sprintf("shard-%04d", i), peers)
	}
	return c
}

// TestClosure1_MessageAndByteCounts proves clause 1: with 1000 shards across 5
// peers, uncoalesced yields shards*peers messages while coalesced yields
// exactly one message per peer (5), and coalesced bytes < uncoalesced bytes.
// All counts are derived from the REAL produced messages, never hardcoded.
//
// Mutation: if TickCoalesced emitted per-shard messages instead of one per
// peer, co.MsgCount would be 5000 and the "exactly 5" assertion fails. If the
// byte model gave per-entry messages no envelope amortization, the bytes-less
// assertion fails.
func TestClosure1_MessageAndByteCounts(t *testing.T) {
	peers := []string{"peerA", "peerB", "peerC", "peerD", "peerE"}
	const nShards = 1000

	// Count derived from real messages, independent of any constant.
	un := buildEven(nShards, peers).TickUncoalesced()
	co := buildEven(nShards, peers).TickCoalesced()

	wantUn := nShards * len(peers) // 5000
	wantCo := len(peers)           // 5

	t.Logf("run=%s clause=1 BEFORE(uncoalesced): msgs=%d bytes=%d entries=%d",
		runUUID, un.MsgCount, un.ByteCount, un.EntryCount)
	t.Logf("run=%s clause=1 AFTER(coalesced):    msgs=%d bytes=%d entries=%d",
		runUUID, co.MsgCount, co.ByteCount, co.EntryCount)

	if un.MsgCount != wantUn {
		t.Fatalf("uncoalesced msg count = %d, want %d", un.MsgCount, wantUn)
	}
	if co.MsgCount != wantCo {
		t.Fatalf("coalesced msg count = %d, want exactly %d (one per peer)", co.MsgCount, wantCo)
	}
	// Coalescing must not drop heartbeats: same total entries either way.
	if co.EntryCount != un.EntryCount {
		t.Fatalf("coalesced entries=%d != uncoalesced entries=%d (heartbeats lost)",
			co.EntryCount, un.EntryCount)
	}
	if co.EntryCount != nShards*len(peers) {
		t.Fatalf("total heartbeat entries = %d, want %d", co.EntryCount, nShards*len(peers))
	}
	// Real win: fewer envelopes -> fewer bytes.
	if !(co.ByteCount < un.ByteCount) {
		t.Fatalf("coalesced bytes %d not < uncoalesced bytes %d", co.ByteCount, un.ByteCount)
	}
	// Each coalesced message must actually carry many shards (proves batching).
	for _, m := range co.Messages {
		if len(m.Entries) != nShards {
			t.Fatalf("peer %s carries %d entries, want %d", m.DestPeer, len(m.Entries), nShards)
		}
	}
}

// TestClosure2_LivenessPreserved proves clause 2: after a single coalesced tick
// is delivered to the receiver, EVERY one of the 1000 shards has an updated
// last-seen, so none is considered failed -> no spurious election.
//
// Mutation: if Deliver dropped a shard entry (e.g. ranged over m.Entries[1:]),
// at least one shard would be missing and FailedShards would be non-empty,
// failing this test.
func TestClosure2_LivenessPreserved(t *testing.T) {
	peers := []string{"peerA", "peerB", "peerC", "peerD", "peerE"}
	const nShards = 1000

	c := buildEven(nShards, peers)
	allShards := make([]string, nShards)
	for i := range allShards {
		allShards[i] = fmt.Sprintf("shard-%04d", i)
	}

	rx := NewReceiver(0) // window 0: must be refreshed THIS tick or it's failed

	// Before delivery: nothing seen, everything failed.
	failedBefore := rx.FailedShards(allShards, 1)
	t.Logf("run=%s clause=2 BEFORE: seen=%d failed=%d", runUUID, len(rx.SeenShards()), len(failedBefore))
	if len(failedBefore) != nShards {
		t.Fatalf("before delivery, failed=%d, want all %d", len(failedBefore), nShards)
	}

	co := c.TickCoalesced()
	refreshed := rx.DeliverAll(1, co.Messages)

	failedAfter := rx.FailedShards(allShards, 1)
	t.Logf("run=%s clause=2 AFTER: refreshed_entries=%d distinct_seen=%d failed=%d",
		runUUID, refreshed, len(rx.SeenShards()), len(failedAfter))

	if len(rx.SeenShards()) != nShards {
		t.Fatalf("distinct shards refreshed = %d, want %d (a shard was missed)",
			len(rx.SeenShards()), nShards)
	}
	if len(failedAfter) != 0 {
		t.Fatalf("after coalesced delivery, %d shards still failed (spurious election): %v...",
			len(failedAfter), failedAfter[:min(5, len(failedAfter))])
	}
}

// TestClosure3_NoCrossPeerDelivery proves clause 3: a shard whose peer set
// differs from others routes ONLY to its own peers; coalescing never
// cross-delivers a heartbeat to a peer that the shard does not target.
//
// Mutation: if TickCoalesced fanned every shard to every peer (cross-delivery),
// peerX would receive sLonely's heartbeat and the "must NOT contain" assertion
// fails.
func TestClosure3_NoCrossPeerDelivery(t *testing.T) {
	c := New()
	c.Register("shard-common-1", []string{"peerX", "peerY"})
	c.Register("shard-common-2", []string{"peerX", "peerY"})
	c.Register("shard-lonely", []string{"peerZ"}) // disjoint peer set

	co := c.TickCoalesced()

	byPeer := map[string][]string{}
	for _, m := range co.Messages {
		for _, e := range m.Entries {
			byPeer[m.DestPeer] = append(byPeer[m.DestPeer], e.ShardID)
		}
	}
	t.Logf("run=%s clause=3 routing: peerX=%v peerY=%v peerZ=%v",
		runUUID, byPeer["peerX"], byPeer["peerY"], byPeer["peerZ"])

	// peerZ must receive ONLY the lonely shard.
	if got := byPeer["peerZ"]; len(got) != 1 || got[0] != "shard-lonely" {
		t.Fatalf("peerZ entries = %v, want exactly [shard-lonely]", got)
	}
	// peerX and peerY must NOT receive the lonely shard.
	for _, p := range []string{"peerX", "peerY"} {
		if contains(byPeer[p], "shard-lonely") {
			t.Fatalf("peer %s wrongly received shard-lonely (cross-delivery): %v", p, byPeer[p])
		}
		// And they must receive both common shards.
		if !contains(byPeer[p], "shard-common-1") || !contains(byPeer[p], "shard-common-2") {
			t.Fatalf("peer %s missing a common shard: %v", p, byPeer[p])
		}
	}
	// The lonely shard must NOT be routed to peerX/peerY at all.
	if contains(byPeer["peerX"], "shard-lonely") || contains(byPeer["peerY"], "shard-lonely") {
		t.Fatalf("shard-lonely leaked to peerX/peerY")
	}

	// Receiver side cross-check: deliver and confirm peerZ-only liveness path.
	rx := NewReceiver(0)
	rx.DeliverAll(1, co.Messages)
	if _, ok := rx.LastSeen("shard-lonely"); !ok {
		t.Fatalf("shard-lonely never refreshed despite being routed to peerZ")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
