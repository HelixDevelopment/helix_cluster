package heartbeatcoalescer

import "sync"

// Receiver is the peer side. It tracks the last tick at which each shard's
// liveness was refreshed by an inbound heartbeat. A shard is considered failed
// (election-eligible) if it has not been refreshed within FailureWindow ticks.
//
// Deliver demultiplexes a coalesced (or uncoalesced) message and refreshes
// EVERY contained shard. This is the property that preserves liveness under
// coalescing: batching changes the wire shape, not which shards get refreshed.
//
// A Receiver is safe for concurrent use: in the Multi-Raft model a peer
// receives inbound heartbeat messages from many sources at once, so Deliver and
// the liveness queries are guarded by mu. FailureWindow is read under the same
// lock; set it before sharing the Receiver across goroutines.
type Receiver struct {
	// mu guards lastSeen (and the FailureWindow read in IsFailed) so concurrent
	// Deliver/query calls from multiple inbound peers do not race the map.
	mu sync.RWMutex
	// lastSeen maps shardID -> last tick at which a heartbeat for it arrived.
	lastSeen map[string]uint64
	// FailureWindow is how many ticks a shard may go unrefreshed before it is
	// considered failed.
	FailureWindow uint64
}

// NewReceiver builds a Receiver with the given failure window (in ticks).
func NewReceiver(failureWindow uint64) *Receiver {
	return &Receiver{lastSeen: make(map[string]uint64), FailureWindow: failureWindow}
}

// Deliver applies one inbound message at logical time tick, refreshing the
// last-seen of every shard entry it carries. It returns the number of shard
// entries refreshed (real count derived from the message, not assumed).
func (r *Receiver) Deliver(tick uint64, m Message) int {
	r.mu.Lock()
	for _, e := range m.Entries {
		r.lastSeen[e.ShardID] = tick
	}
	r.mu.Unlock()
	return len(m.Entries)
}

// DeliverAll delivers every message in a tick result at the given tick and
// returns the total number of shard entries refreshed.
func (r *Receiver) DeliverAll(tick uint64, msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += r.Deliver(tick, m)
	}
	return n
}

// LastSeen returns the last tick a shard was refreshed and whether it has ever
// been seen.
func (r *Receiver) LastSeen(shardID string) (uint64, bool) {
	r.mu.RLock()
	t, ok := r.lastSeen[shardID]
	r.mu.RUnlock()
	return t, ok
}

// isFailedLocked is the unlocked core of IsFailed; the caller must hold r.mu
// (read or write). Keeping it separate lets FailedShards take the lock once for
// the whole batch instead of per shard, and avoids RWMutex re-entrancy.
func (r *Receiver) isFailedLocked(shardID string, now uint64) bool {
	t, ok := r.lastSeen[shardID]
	if !ok {
		return true
	}
	return now-t > r.FailureWindow
}

// IsFailed reports whether the shard is election-eligible at the current tick:
// either never seen, or last seen more than FailureWindow ticks ago.
func (r *Receiver) IsFailed(shardID string, now uint64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.isFailedLocked(shardID, now)
}

// FailedShards returns, for the provided shard universe, those considered
// failed at the current tick. Deterministic: input order is preserved.
func (r *Receiver) FailedShards(shardIDs []string, now uint64) []string {
	var failed []string
	r.mu.RLock()
	for _, id := range shardIDs {
		if r.isFailedLocked(id, now) {
			failed = append(failed, id)
		}
	}
	r.mu.RUnlock()
	return failed
}

// SeenShards returns the set of shard ids that have ever been refreshed.
func (r *Receiver) SeenShards() map[string]uint64 {
	r.mu.RLock()
	out := make(map[string]uint64, len(r.lastSeen))
	for k, v := range r.lastSeen {
		out[k] = v
	}
	r.mu.RUnlock()
	return out
}
