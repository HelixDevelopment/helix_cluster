package multiraft

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// This file adds leaseholder-local reads (the CockroachDB pattern) on top of the
// per-shard raft groups in multiraft.go.
//
// WHY a lease at all: serving a linearizable read through Raft requires a round
// trip (a read-index or a no-op proposal) so the reader knows it has seen all
// committed writes. That defeats read throughput. CockroachDB instead grants the
// current leader a time-bounded *lease*; while the lease is valid the leaseholder
// may answer reads directly from its local committed store with NO new Raft
// proposal, because the lease guarantees no other node served a conflicting write
// in that interval. When the lease expires the fast path MUST stop — otherwise a
// stale node could serve a value after another node took over. Expiry is checked
// against an INJECTED clock so the decision is deterministic and testable (never
// time.Now in the decision path).

// Lease-related errors.
var (
	// ErrNoLease is returned when a shard has no lease recorded at all.
	ErrNoLease = errors.New("multiraft: shard has no lease")
	// ErrLeaseExpired is returned when the recorded lease has expired relative to
	// the supplied clock — the local fast path is refused and the read re-routes.
	ErrLeaseExpired = errors.New("multiraft: lease expired")
)

// Clock supplies the current time to the lease decision path. It is injected so
// tests can advance time deterministically; production passes a real wall clock.
type Clock interface {
	// Now returns the current time as judged by this clock.
	Now() time.Time
}

// systemClock is the production Clock backed by the real monotonic wall clock.
type systemClock struct{}

// Now implements Clock using time.Now. This is the ONLY place time.Now is read;
// the lease decision logic never calls it directly.
func (systemClock) Now() time.Time { return time.Now() }

// SystemClock returns a Clock backed by the real wall clock for production use.
func SystemClock() Clock { return systemClock{} }

// lease records a single shard's current leaseholder and the instant the lease
// stops being valid. A read may take the local fast path only on holder, and
// only while now < expiresAt.
type lease struct {
	holder    uint64
	expiresAt time.Time
}

// valid reports whether the lease is held by node id and has not expired as of
// now. Both conditions are load-bearing: dropping the holder check would let any
// node serve a local read; dropping the expiry check would let a stale node serve
// reads forever (the mutation guard in the tests pins exactly this).
func (l lease) valid(id uint64, now time.Time) bool {
	if l.holder != id {
		return false
	}
	return now.Before(l.expiresAt)
}

// LeaseTracker records, per shard, which node currently holds the read lease and
// when it expires. It is the decision component for leaseholder-local reads. It
// is concurrency-safe because reads from many shards consult it in parallel.
//
// LeaseTracker is intentionally storage-agnostic: it answers "may THIS node serve
// a local read for THIS shard right now?" and "who is the leaseholder?". The
// actual value comes from the manager's committed store; the routing comes from
// the transport. Keeping the policy here isolates the time-based decision so it
// can be unit-tested without spinning up raft.
type LeaseTracker struct {
	clock    Clock
	duration time.Duration
	mu       sync.RWMutex
	leases   map[ShardID]lease
}

// NewLeaseTracker builds a tracker that grants leases of the given duration and
// reads time from clock. A nil clock falls back to the real wall clock; a
// non-positive duration is rejected so a lease can never be born already expired.
func NewLeaseTracker(clock Clock, duration time.Duration) (*LeaseTracker, error) {
	if clock == nil {
		clock = SystemClock()
	}
	if duration <= 0 {
		return nil, fmt.Errorf("multiraft: lease duration must be positive, got %v", duration)
	}
	return &LeaseTracker{
		clock:    clock,
		duration: duration,
		leases:   make(map[ShardID]lease),
	}, nil
}

// Grant records that holder holds the lease for shard, expiring duration after
// the current clock time. It is called when a node becomes (and proves itself)
// leaseholder. Re-granting extends/replaces the existing lease.
func (lt *LeaseTracker) Grant(shard ShardID, holder uint64) time.Time {
	exp := lt.clock.Now().Add(lt.duration)
	lt.mu.Lock()
	lt.leases[shard] = lease{holder: holder, expiresAt: exp}
	lt.mu.Unlock()
	return exp
}

// Revoke drops any lease for shard (e.g. on leadership/lease loss).
func (lt *LeaseTracker) Revoke(shard ShardID) {
	lt.mu.Lock()
	delete(lt.leases, shard)
	lt.mu.Unlock()
}

// Leaseholder returns the currently recorded leaseholder for shard and whether a
// (possibly expired) lease exists. Used to decide where to route a non-local read.
func (lt *LeaseTracker) Leaseholder(shard ShardID) (uint64, bool) {
	lt.mu.RLock()
	defer lt.mu.RUnlock()
	l, ok := lt.leases[shard]
	if !ok {
		return 0, false
	}
	return l.holder, true
}

// IsLocalLeaseholder reports whether nodeID may serve a local fast-path read for
// shard as of now: it must be the recorded leaseholder AND the lease must not be
// expired. This is the single decision point for the read fast path.
//
// MUTATION GUARD: if this ignored expiry and returned true whenever nodeID is the
// holder, TestLeaseExpiryRefusesLocalFastPath fails — after advancing the clock
// past expiry it would still report true and serve a stale local read.
func (lt *LeaseTracker) IsLocalLeaseholder(shard ShardID, nodeID uint64, now time.Time) bool {
	lt.mu.RLock()
	defer lt.mu.RUnlock()
	l, ok := lt.leases[shard]
	if !ok {
		return false
	}
	return l.valid(nodeID, now)
}

// ReadResult reports how a read was served, so callers (and tests) can prove the
// fast path vs. the routed path was actually taken — not merely that a value came
// back. This is the sink-side evidence CLAUDE-1 requires for the read feature.
type ReadResult struct {
	// Value is the latest committed application value for the shard.
	Value []byte
	// Local is true when the read was served from the asking node's own committed
	// store under a valid lease (no Raft proposal, no transport hop).
	Local bool
	// Routed is true when the read was forwarded to the leaseholder over the
	// transport because the asking node could not serve it locally.
	Routed bool
	// Leaseholder is the node that ultimately served the read.
	Leaseholder uint64
}

// latestCommittedLocked returns a copy of the most recent committed application
// payload for the node, or (nil,false) if the node has committed nothing. Caller
// holds sh.mu. This is the value a leaseholder-local read returns — straight from
// the already-agreed committed log, with NO new proposal.
func (n *node) latestCommittedLocked() ([]byte, bool) {
	if len(n.committed) == 0 {
		return nil, false
	}
	last := n.committed[len(n.committed)-1]
	cp := make([]byte, len(last))
	copy(cp, last)
	return cp, true
}

// EnableLeaseholderReads attaches a LeaseTracker to the manager and registers a
// read handler for every node of every existing shard with the transport, so a
// follower's SendRead reaches the leaseholder's local committed store. Call after
// shards are created. It is idempotent for already-registered nodes.
//
// WHY register a handler per node (not only the current leader): the leaseholder
// can change; whichever node currently holds the lease answers, and the others'
// handlers simply sit unused until they hold the lease. The handler itself never
// proposes — it reads the local committed store under sh.mu.
func (m *MultiRaftManager) EnableLeaseholderReads(lt *LeaseTracker) error {
	if lt == nil {
		return errors.New("multiraft: nil LeaseTracker")
	}
	rt, ok := m.transport.(ReadRouter)
	if !ok {
		return errors.New("multiraft: transport does not support read-handler registration (ReadRouter)")
	}
	m.mu.Lock()
	m.leases = lt
	m.readEnabled = true
	shards := make([]*shard, 0, len(m.shards))
	for _, sh := range m.shards {
		shards = append(shards, sh)
	}
	m.mu.Unlock()

	for _, sh := range shards {
		registerShardReadHandlers(rt, sh)
	}
	return nil
}

// registerShardReadHandlers wires a local-read handler for every node of sh into
// the ReadRouter. It is the single place this wiring is done so it is identical
// whether a shard existed at EnableLeaseholderReads time or was created later via
// CreateShard — fixing the "shard created after enable gets no read handler" gap
// (a follower Read against a later-created shard's leaseholder previously failed
// with ErrNoReadHandler even though leaseholder reads were enabled). It takes
// sh.mu itself, so callers MUST NOT already hold it.
func registerShardReadHandlers(rt ReadRouter, sh *shard) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	for _, n := range sh.nodes {
		node := n
		rt.RegisterReadHandler(sh.id, node.id, func() ([]byte, error) {
			sh.mu.Lock()
			defer sh.mu.Unlock()
			v, ok := node.latestCommittedLocked()
			if !ok {
				return nil, nil
			}
			return v, nil
		})
	}
}

// AcquireLease grants the read lease for shard to nodeID (which must currently be
// the raft leader of that shard — only the leader has the up-to-date committed
// log to back local reads). Returns the lease expiry time. This is how a node
// becomes the active leaseholder; afterwards IsLocalLeaseholder(shard,nodeID,now)
// is true until expiry.
func (m *MultiRaftManager) AcquireLease(shard ShardID, nodeID uint64) (time.Time, error) {
	m.mu.RLock()
	sh, ok := m.shards[shard]
	lt := m.leases
	m.mu.RUnlock()
	if !ok {
		return time.Time{}, fmt.Errorf("%w: %q", ErrShardNotFound, shard)
	}
	if lt == nil {
		return time.Time{}, errors.New("multiraft: leaseholder reads not enabled (call EnableLeaseholderReads)")
	}
	sh.mu.Lock()
	leader := sh.leaderLocked()
	sh.mu.Unlock()
	if leader == nil || leader.id != nodeID {
		return time.Time{}, fmt.Errorf("multiraft: node %d is not the leader of shard %q; only the leader may hold the lease", nodeID, shard)
	}
	return lt.Grant(shard, nodeID), nil
}

// Read serves a read of shard's latest committed value as observed by askingNode,
// using the leaseholder-local fast path when askingNode holds a valid lease, and
// otherwise routing to the current leaseholder via the transport. now is supplied
// by the caller (from an injected clock) so lease validity is deterministic.
//
// CLAUDE-1 sink-side guarantees proven by tests:
//   - On the leaseholder: NO new Raft proposal (committed-entry count unchanged
//     across the read) and the returned value is the latest committed value.
//   - On a follower: the read is forwarded via transport.SendRead (RoutedReads
//     increments) and ReadResult.Routed is true.
//   - After expiry: the fast path is refused; the read re-routes (Local=false).
func (m *MultiRaftManager) Read(shard ShardID, askingNode uint64, now time.Time) (ReadResult, error) {
	m.mu.RLock()
	sh, ok := m.shards[shard]
	lt := m.leases
	enabled := m.readEnabled
	m.mu.RUnlock()
	if !ok {
		return ReadResult{}, fmt.Errorf("%w: %q", ErrShardNotFound, shard)
	}
	if lt == nil || !enabled {
		return ReadResult{}, errors.New("multiraft: leaseholder reads not enabled (call EnableLeaseholderReads)")
	}

	// Fast path: asking node holds a valid (unexpired) lease -> serve locally.
	//
	// LEASE-VS-COMMITTED CONSISTENCY NOTE: lease validity is sampled under the
	// LeaseTracker's mu and the committed value under sh.mu — two distinct locks,
	// not one combined snapshot. This is correct for the current model because the
	// decision time `now` is supplied by the caller's injected clock (it does not
	// move between the two samples) and delivery is synchronous/in-process, so no
	// concurrent leadership change can interleave a Revoke + new-leader commit
	// between the two locks within a single Read. A future transport with truly
	// concurrent commit + lease churn would need a combined (lease,committed)
	// snapshot (e.g. read the committed value under sh.mu and re-validate the lease
	// epoch under the same critical section) to preserve linearizability. The
	// stale-node window itself is closed eagerly by revokeReadAccess on node/leader
	// loss (see StopNode/RemoveShard), not left to pure time expiry.
	if lt.IsLocalLeaseholder(shard, askingNode, now) {
		sh.mu.Lock()
		n, ok := sh.nodes[askingNode]
		if !ok || n.stopped {
			sh.mu.Unlock()
			return ReadResult{}, fmt.Errorf("multiraft: shard %q has no live node %d", shard, askingNode)
		}
		v, _ := n.latestCommittedLocked()
		sh.mu.Unlock()
		return ReadResult{Value: v, Local: true, Leaseholder: askingNode}, nil
	}

	// Slow path: route to the current leaseholder over the transport. This covers
	// both "asking node is a follower" and "asking node's lease has expired".
	holder, has := lt.Leaseholder(shard)
	if !has {
		return ReadResult{}, fmt.Errorf("%w: %q", ErrNoLease, shard)
	}
	// If the recorded lease has itself expired, the leaseholder must re-acquire
	// before it can serve — surface that rather than serving a stale value.
	if !lt.IsLocalLeaseholder(shard, holder, now) {
		return ReadResult{}, fmt.Errorf("%w: %q (holder %d)", ErrLeaseExpired, shard, holder)
	}
	v, err := m.transport.SendRead(shard, holder)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{Value: v, Routed: true, Leaseholder: holder}, nil
}
