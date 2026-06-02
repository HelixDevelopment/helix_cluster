// Package heartbeatcoalescer implements a deterministic, pure-Go heartbeat
// coalescer for Multi-Raft.
//
// Model: a node hosts many Raft shards. Each shard, per tick, would send a
// heartbeat to each of its peers. Uncoalesced cost is O(shards * peers)
// messages/tick. Coalesced: all heartbeats destined for the same peer in a
// tick are batched into ONE message carrying many shard-heartbeat entries,
// yielding O(peers) messages/tick. Liveness of every shard is preserved: the
// receiver demultiplexes the batched message and refreshes each contained
// shard's last-seen, so no shard is starved and no spurious election occurs.
//
// The package is deterministic (peers and entries are emitted in sorted
// order), uses only the standard library, and performs no I/O.
package heartbeatcoalescer

import "sort"

// ShardHeartbeat is a single shard's heartbeat entry destined for one peer.
type ShardHeartbeat struct {
	ShardID string
	// Term and CommitIndex are the per-shard Raft metadata a real heartbeat
	// carries. They participate in the byte accounting and are demultiplexed
	// by the receiver.
	Term        uint64
	CommitIndex uint64
}

// Message is one outbound heartbeat message destined for a single peer. In the
// uncoalesced mode each Message carries exactly one Entry; in the coalesced
// mode a Message carries every shard heartbeat destined for that peer in the
// tick.
type Message struct {
	DestPeer string
	Entries  []ShardHeartbeat
}

// Bytes returns a deterministic wire-size estimate for the message. The cost is
// a fixed per-message envelope (destination routing header) plus a per-entry
// payload. This models the real saving of coalescing: many entries amortize a
// single envelope/header instead of paying one envelope per shard.
func (m Message) Bytes() int {
	const (
		envelopeOverhead = 24 // dest peer routing header, msg framing
		perEntryBytes    = 18 // shardID handle + term + commitIndex
	)
	return envelopeOverhead + len(m.Entries)*perEntryBytes
}

type shard struct {
	id    string
	term  uint64
	peers []string // sorted, deduplicated
}

// Coalescer registers shards and produces per-tick outbound heartbeat messages.
// It is not safe for concurrent use; ticks are deterministic and sequential.
type Coalescer struct {
	shards []shard
	// commit advances each tick so heartbeats carry changing metadata.
	commit uint64
}

// New constructs an empty Coalescer.
func New() *Coalescer { return &Coalescer{} }

// Register adds a shard with the given id and peer set. Peers are deduplicated
// and stored sorted so output ordering is deterministic. A shard with an empty
// peer set is permitted (it simply contributes no messages).
func (c *Coalescer) Register(shardID string, peers []string) {
	uniq := make(map[string]struct{}, len(peers))
	for _, p := range peers {
		uniq[p] = struct{}{}
	}
	ps := make([]string, 0, len(uniq))
	for p := range uniq {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	c.shards = append(c.shards, shard{id: shardID, peers: ps})
}

// ShardCount returns the number of registered shards.
func (c *Coalescer) ShardCount() int { return len(c.shards) }

// TickResult bundles the produced messages with derived counters so callers
// (and tests) can read real observed counts and bytes.
type TickResult struct {
	Messages   []Message
	MsgCount   int
	ByteCount  int
	EntryCount int
}

func newTickResult(msgs []Message) TickResult {
	r := TickResult{Messages: msgs, MsgCount: len(msgs)}
	for _, m := range msgs {
		r.ByteCount += m.Bytes()
		r.EntryCount += len(m.Entries)
	}
	return r
}

// TickUncoalesced produces the naive output: one message per (shard, peer)
// pair. Cost is O(shards * peers) messages. Output is deterministic, ordered by
// shard registration order then by sorted peer.
func (c *Coalescer) TickUncoalesced() TickResult {
	c.commit++
	var msgs []Message
	for i := range c.shards {
		s := &c.shards[i]
		s.term++
		for _, peer := range s.peers {
			msgs = append(msgs, Message{
				DestPeer: peer,
				Entries: []ShardHeartbeat{{
					ShardID:     s.id,
					Term:        s.term,
					CommitIndex: c.commit,
				}},
			})
		}
	}
	return newTickResult(msgs)
}

// TickCoalesced produces the batched output: exactly one message per distinct
// destination peer, each carrying every shard heartbeat destined for that peer
// this tick. Cost is O(peers) messages. Entries within a message are ordered by
// shard id; messages are ordered by destination peer. Both orderings are
// deterministic.
func (c *Coalescer) TickCoalesced() TickResult {
	c.commit++
	byPeer := make(map[string][]ShardHeartbeat)
	for i := range c.shards {
		s := &c.shards[i]
		s.term++
		hb := ShardHeartbeat{ShardID: s.id, Term: s.term, CommitIndex: c.commit}
		for _, peer := range s.peers {
			byPeer[peer] = append(byPeer[peer], hb)
		}
	}

	peers := make([]string, 0, len(byPeer))
	for p := range byPeer {
		peers = append(peers, p)
	}
	sort.Strings(peers)

	msgs := make([]Message, 0, len(peers))
	for _, peer := range peers {
		entries := byPeer[peer]
		sort.Slice(entries, func(a, b int) bool { return entries[a].ShardID < entries[b].ShardID })
		msgs = append(msgs, Message{DestPeer: peer, Entries: entries})
	}
	return newTickResult(msgs)
}
