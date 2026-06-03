package multiraft

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"

	"go.etcd.io/raft/v3"
	pb "go.etcd.io/raft/v3/raftpb"
)

// ShardID identifies an independent raft group (shard) within the cluster.
type ShardID string

// Common errors returned by the manager.
var (
	// ErrShardExists is returned by CreateShard when the id is already present.
	ErrShardExists = errors.New("multiraft: shard already exists")
	// ErrShardNotFound is returned when an operation references an unknown shard.
	ErrShardNotFound = errors.New("multiraft: shard not found")
	// ErrNoLeader is returned by Propose when the shard currently has no leader
	// to accept the write (e.g. mid-election). Callers should retry.
	ErrNoLeader = errors.New("multiraft: shard has no leader")
)

// node is a single raft peer within a shard. Each peer drives its own RawNode
// over its own ShardStorage; together the peers of a shard form one real raft
// group reaching consensus over the shared InProcTransport.
type node struct {
	id      uint64
	raw     *raft.RawNode
	storage shardPersister
	// committed holds the application payloads of committed normal entries, in
	// commit order — the sink-side evidence that a proposal was actually agreed
	// by the raft quorum (CLAUDE-1: prove the feature works, not just runs).
	committed [][]byte
	applied   uint64
	stopped   bool
	// pendingReady holds a Ready that was pulled from raft (via raw.Ready()) but
	// whose HardState/Entries could NOT be persisted yet because the storage
	// backend returned an error. Per the etcd raft v3.6 contract, calling Ready()
	// already "accepts" the unstable batch — raft will NOT re-present it on its own
	// and forbids pulling another Ready() before Advance(). So to honor raft's
	// durability invariant we PARK the un-persisted Ready here and, on each later
	// cycle, RE-attempt persistence of this exact Ready; only once it is durable do
	// we Advance(it). This is the no-data-loss retry that HXC-917 requires: Advance
	// (which Steps the storage-append-response that drives commit) never runs until
	// the data is actually on stable storage.
	pendingReady    *raft.Ready
	hasPendingReady bool
}

// park stashes a Ready whose persistence failed so the next driving cycle retries
// that exact batch instead of pulling a new one (raft forbids a second Ready()
// before Advance). It copies rd because raw.Ready() reuses backing buffers.
func (n *node) park(rd raft.Ready) {
	rdCopy := rd
	n.pendingReady = &rdCopy
	n.hasPendingReady = true
}

// clearParked drops any parked Ready once its data is durably persisted.
func (n *node) clearParked() {
	n.pendingReady = nil
	n.hasPendingReady = false
}

// shard is one independent raft group. Its own mutex means proposing to / ticking
// shard A never contends on shard B's lock, which is what lets throughput scale
// with shard count instead of serializing through a single shared structure.
type shard struct {
	id        ShardID
	mu        sync.Mutex
	nodes     map[uint64]*node
	peers     []uint64
	transport RaftTransport
	// pending accumulates cross-node outbound raft messages produced while sh.mu
	// is held in processReadyLocked. They are flushed by deliverPending AFTER sh.mu
	// is released, so transport.Send (and any handler it invokes synchronously) is
	// never called under sh.mu. This is what lets the receiving inbox handler take
	// sh.mu safely — including for an asynchronous transport (HXC-909).
	pending []pb.Message
}

// MultiRaftManager owns a set of independent shards and drives their raft groups.
type MultiRaftManager struct {
	mu        sync.RWMutex
	shards    map[ShardID]*shard
	transport RaftTransport
	// electionTick/heartbeatTick configure every shard's raft nodes.
	electionTick  int
	heartbeatTick int
	// leases tracks leaseholder-local read leases per shard (CockroachDB pattern);
	// nil until EnableLeaseholderReads is called. readEnabled records that the
	// transport's read handlers have been wired so Read may take its fast path.
	leases      *LeaseTracker
	readEnabled bool
	// newStorage builds the per-peer persistence backend. It defaults to the real
	// in-memory ShardStorage; tests inject a fault-injecting persister to exercise
	// the durability-error path (HXC-917). It is the single seam that keeps the
	// storage layer swappable for a real WAL/disk backend.
	newStorage func() shardPersister
}

// NewMultiRaftManager constructs a manager using the supplied transport. The
// transport is REAL message delivery between shard peers (see InProcTransport).
func NewMultiRaftManager(transport RaftTransport) *MultiRaftManager {
	return NewMultiRaftManagerWithStorage(transport, func() shardPersister { return NewShardStorage() })
}

// NewMultiRaftManagerWithStorage is like NewMultiRaftManager but lets the caller
// supply the per-peer storage backend factory. This is the seam used to plug in a
// real WAL/disk persister in production deployments and a fault-injecting
// persister in tests that verify raft's durability invariant (HXC-917): on a
// persistence error the driver must NOT Advance the affected Ready. A nil factory
// falls back to the real in-memory ShardStorage.
func NewMultiRaftManagerWithStorage(transport RaftTransport, newStorage func() shardPersister) *MultiRaftManager {
	if newStorage == nil {
		newStorage = func() shardPersister { return NewShardStorage() }
	}
	return &MultiRaftManager{
		shards:        make(map[ShardID]*shard),
		transport:     transport,
		electionTick:  10,
		heartbeatTick: 1,
		newStorage:    newStorage,
	}
}

// CreateShard creates a new independent raft group identified by id with the
// given peer node IDs. Every peer gets its own RawNode + ShardStorage and is
// registered with the transport so the peers can exchange real raft messages.
func (m *MultiRaftManager) CreateShard(id ShardID, peers []uint64) error {
	if len(peers) == 0 {
		return fmt.Errorf("multiraft: shard %q needs at least one peer", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.shards[id]; ok {
		return fmt.Errorf("%w: %q", ErrShardExists, id)
	}

	sh := &shard{id: id, nodes: make(map[uint64]*node), peers: append([]uint64(nil), peers...), transport: m.transport}
	sort.Slice(sh.peers, func(i, j int) bool { return sh.peers[i] < sh.peers[j] })

	raftPeers := make([]raft.Peer, len(sh.peers))
	for i, p := range sh.peers {
		raftPeers[i] = raft.Peer{ID: p}
	}

	for _, pid := range sh.peers {
		st := m.newStorage()
		cfg := &raft.Config{
			ID:            pid,
			ElectionTick:  m.electionTick,
			HeartbeatTick: m.heartbeatTick,
			Storage:       st,
			MaxSizePerMsg: 1 << 20,
			// MaxInflightMsgs bounds in-flight appends; a small value is fine.
			MaxInflightMsgs: 256,
		}
		raw, err := raft.NewRawNode(cfg)
		if err != nil {
			return fmt.Errorf("multiraft: shard %q node %d: %w", id, pid, err)
		}
		// Bootstrap seeds the identical initial membership conf-change log into
		// every peer so they agree on the voter set without an external config.
		if err := raw.Bootstrap(raftPeers); err != nil {
			return fmt.Errorf("multiraft: shard %q node %d bootstrap: %w", id, pid, err)
		}
		n := &node{id: pid, raw: raw, storage: st}
		sh.nodes[pid] = n
	}

	// Register inboxes AFTER all nodes exist so a delivered message always finds
	// its destination node within this shard.
	//
	// ASYNC-DELIVERY SAFETY (HXC-909): the inbox handler takes sh.mu before it
	// reads sh.nodes / dst.stopped and calls dst.raw.Step. This makes the handler
	// safe BY CONSTRUCTION for ANY transport — including one that delivers from its
	// own goroutine or after a network hop — because every Step and every
	// processReadyLocked body for this shard is now serialized under the one lock.
	// To make this work without self-deadlock, processReadyLocked NEVER holds sh.mu
	// while it calls transport.Send: it drains Ready under the lock, then releases
	// the lock and delivers the collected outbound messages (see deliverPending).
	// So when synchronous InProcTransport.Send invokes this handler inline, sh.mu is
	// NOT held by the sender and this Lock() succeeds rather than deadlocking.
	for _, n := range sh.nodes {
		n := n
		m.transport.RegisterPeer(id, n.id, func(msg pb.Message) {
			sh.mu.Lock()
			defer sh.mu.Unlock()
			dst, ok := sh.nodes[msg.To]
			if !ok || dst.stopped {
				return
			}
			// Step feeds the inbound message into the raft state machine.
			_ = dst.raw.Step(msg)
		})
	}

	m.shards[id] = sh

	// If leaseholder reads were already enabled before this shard existed, wire its
	// nodes' read handlers now. Without this, a follower Read against this newly
	// created shard's leaseholder would fail with ErrNoReadHandler even though
	// leaseholder reads are "enabled" — the create-after-enable ordering gap.
	// registerShardReadHandlers takes sh.mu (not yet held here); we still hold m.mu,
	// which is the established lock order (manager lock outermost).
	if m.readEnabled {
		if rt, ok := m.transport.(ReadRouter); ok {
			registerShardReadHandlers(rt, sh)
		}
	}
	return nil
}

// Tick advances the logical clock of ALL shards by one tick, driving elections
// and heartbeats. After ticking each node we process its Ready so any resulting
// messages (e.g. campaign votes) are delivered immediately.
func (m *MultiRaftManager) Tick() {
	m.mu.RLock()
	shards := make([]*shard, 0, len(m.shards))
	for _, sh := range m.shards {
		shards = append(shards, sh)
	}
	m.mu.RUnlock()

	for _, sh := range shards {
		sh.mu.Lock()
		for _, n := range sh.nodes {
			if n.stopped {
				continue
			}
			n.raw.Tick()
		}
		sh.processReadyLocked()
		sh.mu.Unlock()
		// Flush outbound messages OUTSIDE sh.mu so a synchronous transport's inline
		// inbox handler (which takes sh.mu) cannot deadlock and an async transport
		// cannot race the next driving cycle.
		sh.deliverPending()
	}
}

// Propose submits data to the specified shard's leader. It is independent per
// shard: it takes only that shard's lock, so a proposal to shard A never blocks
// on shard B. Returns ErrNoLeader if no leader is currently elected.
//
// WHY route through the leader: only the raft leader may append proposals.
// Routing each proposal to its own shard's leader (rather than a single shared
// node) is exactly what removes the single-leader bottleneck.
func (m *MultiRaftManager) Propose(ctx context.Context, shardID ShardID, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	sh, ok := m.shards[shardID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrShardNotFound, shardID)
	}

	sh.mu.Lock()
	leader := sh.leaderLocked()
	if leader == nil {
		sh.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrNoLeader, shardID)
	}
	if err := leader.raw.Propose(data); err != nil {
		sh.mu.Unlock()
		return fmt.Errorf("multiraft: propose to shard %q: %w", shardID, err)
	}
	// Drive the proposal through one Ready cycle so the append is replicated
	// promptly; commitment completes over subsequent Run/Tick cycles.
	sh.processReadyLocked()
	sh.mu.Unlock()
	// Flush outbound replication messages outside sh.mu (see deliverPending) so an
	// async transport cannot race this proposal's driving and a sync transport's
	// inline inbox handler (which takes sh.mu) cannot deadlock.
	sh.deliverPending()
	return nil
}

// Run drives every shard's Ready loop until no shard has further work, which
// flushes in-flight replication and commitment after proposals or elections.
// It is the synchronous "let consensus settle" step used by callers and tests.
func (m *MultiRaftManager) Run() {
	m.mu.RLock()
	shards := make([]*shard, 0, len(m.shards))
	for _, sh := range m.shards {
		shards = append(shards, sh)
	}
	m.mu.RUnlock()

	for {
		progressed := false
		for _, sh := range shards {
			sh.mu.Lock()
			if sh.processReadyLocked() {
				progressed = true
			}
			hadPending := len(sh.pending) > 0
			sh.mu.Unlock()
			// Flush queued outbound messages outside sh.mu (see deliverPending).
			// A flush that actually had messages counts as progress because a
			// synchronous transport will have Stepped them into peers inline,
			// producing new Ready work on the next iteration.
			sh.deliverPending()
			if hadPending {
				progressed = true
			}
		}
		if !progressed {
			return
		}
	}
}

// Campaign forces a specific node in a shard to start an election. Tests use it
// to elect a leader deterministically; production relies on Tick-driven timeouts.
func (m *MultiRaftManager) Campaign(shardID ShardID, nodeID uint64) error {
	m.mu.RLock()
	sh, ok := m.shards[shardID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrShardNotFound, shardID)
	}
	sh.mu.Lock()
	n, ok := sh.nodes[nodeID]
	if !ok || n.stopped {
		sh.mu.Unlock()
		return fmt.Errorf("multiraft: shard %q has no live node %d", shardID, nodeID)
	}
	if err := n.raw.Campaign(); err != nil {
		sh.mu.Unlock()
		return err
	}
	sh.processReadyLocked()
	sh.mu.Unlock()
	// Flush campaign vote requests outside sh.mu (see deliverPending).
	sh.deliverPending()
	return nil
}

// StopNode removes a node from a shard (simulating leader/peer loss). Its inbox
// is unregistered so it receives no further messages and it stops ticking. The
// remaining quorum can then re-elect and continue committing.
func (m *MultiRaftManager) StopNode(shardID ShardID, nodeID uint64) error {
	m.mu.RLock()
	sh, ok := m.shards[shardID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrShardNotFound, shardID)
	}
	sh.mu.Lock()
	n, ok := sh.nodes[nodeID]
	if ok {
		n.stopped = true
	}
	sh.mu.Unlock()
	if !ok {
		return fmt.Errorf("multiraft: shard %q has no node %d", shardID, nodeID)
	}
	m.transport.UnregisterPeer(shardID, nodeID)
	// Stale-leaseholder safety: stopping a node that still holds a clock-valid
	// lease MUST immediately revoke that lease and tear down its read handler.
	// Otherwise a follower's slow-path Read would find the recorded lease still
	// "valid" and route SendRead to this stopped node's still-registered handler,
	// returning a frozen committed value until pure time expiry — exactly the
	// invariant lease.go forbids ("a stale node could serve a value after another
	// node took over"). Revoke is wired to the node-loss event here.
	m.revokeReadAccess(shardID, nodeID)
	return nil
}

// revokeReadAccess drops any lease held by (shardID,nodeID) and unregisters its
// transport read handler, so neither the local fast path nor a routed SendRead
// can serve a read from a node that has lost membership/leadership. It is the
// single wiring point between a node-loss/leadership-loss event and the lease +
// read-handler teardown. Safe to call even when leaseholder reads are not
// enabled (it then does nothing). It MUST NOT be called while holding sh.mu —
// it takes only the manager lock and the LeaseTracker/transport locks.
func (m *MultiRaftManager) revokeReadAccess(shardID ShardID, nodeID uint64) {
	m.mu.RLock()
	lt := m.leases
	enabled := m.readEnabled
	m.mu.RUnlock()
	if lt != nil {
		// Only revoke if THIS node is the recorded holder — revoking a lease held
		// by a still-live peer would needlessly force a re-acquire.
		if holder, ok := lt.Leaseholder(shardID); ok && holder == nodeID {
			lt.Revoke(shardID)
		}
	}
	if enabled {
		if rt, ok := m.transport.(ReadRouter); ok {
			rt.UnregisterReadHandler(shardID, nodeID)
		}
	}
}

// RemoveShard tears down a shard and unregisters all its peers from transport.
func (m *MultiRaftManager) RemoveShard(shardID ShardID) error {
	m.mu.Lock()
	sh, ok := m.shards[shardID]
	if ok {
		delete(m.shards, shardID)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrShardNotFound, shardID)
	}
	sh.mu.Lock()
	ids := make([]uint64, 0, len(sh.nodes))
	for id := range sh.nodes {
		ids = append(ids, id)
		m.transport.UnregisterPeer(shardID, id)
	}
	sh.nodes = nil
	sh.mu.Unlock()
	// Stale-leaseholder safety (same rationale as StopNode): a removed shard's
	// leaseholder must not keep serving routed reads. Revoke the shard's lease and
	// unregister every node's read handler so SendRead to any of them now returns
	// ErrNoReadHandler instead of a stale value.
	m.mu.RLock()
	lt := m.leases
	enabled := m.readEnabled
	rr, isRouter := m.transport.(ReadRouter)
	m.mu.RUnlock()
	if lt != nil {
		lt.Revoke(shardID)
	}
	if enabled && isRouter {
		for _, id := range ids {
			rr.UnregisterReadHandler(shardID, id)
		}
	}
	return nil
}

// LeaderID returns the elected leader node id of a shard, or 0 if none.
func (m *MultiRaftManager) LeaderID(shardID ShardID) uint64 {
	m.mu.RLock()
	sh, ok := m.shards[shardID]
	m.mu.RUnlock()
	if !ok {
		return 0
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if l := sh.leaderLocked(); l != nil {
		return l.id
	}
	return 0
}

// CommittedEntries returns a copy of the committed application payloads observed
// by the given node of a shard — the sink-side log proving consensus.
func (m *MultiRaftManager) CommittedEntries(shardID ShardID, nodeID uint64) ([][]byte, error) {
	m.mu.RLock()
	sh, ok := m.shards[shardID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrShardNotFound, shardID)
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	n, ok := sh.nodes[nodeID]
	if !ok {
		return nil, fmt.Errorf("multiraft: shard %q has no node %d", shardID, nodeID)
	}
	out := make([][]byte, len(n.committed))
	for i, e := range n.committed {
		cp := make([]byte, len(e))
		copy(cp, e)
		out[i] = cp
	}
	return out, nil
}

// CommittedCount returns how many normal entries the given node has committed.
func (m *MultiRaftManager) CommittedCount(shardID ShardID, nodeID uint64) (int, error) {
	entries, err := m.CommittedEntries(shardID, nodeID)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

// ShardIDs returns the ids of all live shards.
func (m *MultiRaftManager) ShardIDs() []ShardID {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]ShardID, 0, len(m.shards))
	for id := range m.shards {
		ids = append(ids, id)
	}
	return ids
}

// leaderLocked returns the live node believed to be leader, or nil. Caller holds sh.mu.
func (s *shard) leaderLocked() *node {
	for _, n := range s.nodes {
		if n.stopped {
			continue
		}
		st := n.raw.BasicStatus()
		if st.RaftState == raft.StateLeader {
			return n
		}
	}
	return nil
}

// processReadyLocked drains all pending Ready batches for every live node in the
// shard, persisting entries/HardState, delivering outbound messages over the
// transport, and recording committed normal-entry payloads. Returns true if any
// node made progress. Caller holds sh.mu.
//
// This is the heart of correctly driving the REAL etcd raft engine: per the raft
// contract we (1) persist HardState+Entries, (2) send Messages, (3) apply
// CommittedEntries, then (4) Advance.
func (s *shard) processReadyLocked() bool {
	progressed, err := s.processReadyLockedErr()
	if err != nil {
		// A persistence backend (real WAL/disk) failed. We deliberately did NOT
		// Advance the affected node's Ready, so raft will re-present it on the next
		// cycle once storage recovers — no data loss, no log divergence. Surface it
		// to the operator log; the bool/void-returning driver entrypoints (Tick,
		// Propose, Run, Campaign) keep progressing the other nodes/shards.
		log.Printf("multiraft: shard %q: deferring Advance after persistence error (Ready will be retried): %v", s.id, err)
	}
	return progressed
}

// processReadyLockedErr is the durability-correct core of the Ready loop. It
// drains pending Ready batches for every live node, persisting entries/HardState
// BEFORE delivering messages / applying / advancing. Returns whether any node made
// progress and the FIRST persistence error encountered (if any). Caller holds sh.mu.
//
// HXC-917 durability invariant: raft guarantees safety only if a Ready is persisted
// to stable storage BEFORE Advance reports it durably handled. ShardStorage is a
// swappable seam for a real WAL/disk backend, so SetHardState/Append CAN fail. If
// EITHER fails for a node, we MUST NOT call Advance for that node's Ready: doing so
// would tell raft the data is durable when it is not, risking acknowledged writes
// that vanish on restart and log divergence across peers. Instead we record the
// error and SKIP advancing that node — parking the un-persisted Ready so raft
// re-presents the identical Ready on the next cycle (idempotent retry). A faulting
// node does NOT count as progress, so the outer loop terminates instead of spinning
// when every remaining node is parked, while non-faulting nodes in the same pass
// still progress and other shards are wholly unaffected.
func (s *shard) processReadyLockedErr() (bool, error) {
	progressed := false
	var firstErr error
	for {
		did := false
		for _, n := range s.nodes {
			if n.stopped {
				continue
			}

			// If this node has a PARKED Ready whose persistence previously failed, we
			// MUST retry that exact Ready before pulling a new one (raft forbids a
			// second Ready() before Advance, and the parked batch is already accepted).
			var rd raft.Ready
			if n.hasPendingReady {
				rd = *n.pendingReady
			} else {
				if !n.raw.HasReady() {
					continue
				}
				rd = n.raw.Ready()
			}

			// (1) Persist HardState + new log entries to stable storage FIRST, and
			// CHECK the result. A persistence failure means this Ready is NOT durable,
			// so we PARK it (do NOT Advance) and retry it on a later cycle. Advancing
			// here would Step the storage-append-response and let raft mark data
			// durable/committed that never actually reached stable storage.
			if !raft.IsEmptyHardState(rd.HardState) {
				if err := n.storage.SetHardState(rd.HardState); err != nil {
					n.park(rd)
					if firstErr == nil {
						firstErr = fmt.Errorf("node %d SetHardState: %w", n.id, err)
					}
					continue
				}
			}
			if len(rd.Entries) > 0 {
				if err := n.storage.Append(rd.Entries); err != nil {
					n.park(rd)
					if firstErr == nil {
						firstErr = fmt.Errorf("node %d Append: %w", n.id, err)
					}
					continue
				}
			}

			// Persistence succeeded — clear any parked state for this node.
			n.clearParked()

			// (2) Queue outbound messages; cross-node ones are sent by deliverPending
			// AFTER sh.mu is released (self-directed ones are stepped inline here).
			for _, msg := range rd.Messages {
				s.deliver(msg)
			}

			// (3) Apply committed entries: conf-changes update membership;
			// normal entries with a payload are the agreed application writes.
			for _, ent := range rd.CommittedEntries {
				switch ent.Type {
				case pb.EntryConfChange:
					var cc pb.ConfChange
					if err := cc.Unmarshal(ent.Data); err == nil {
						n.raw.ApplyConfChange(cc)
					}
				case pb.EntryConfChangeV2:
					var cc pb.ConfChangeV2
					if err := cc.Unmarshal(ent.Data); err == nil {
						n.raw.ApplyConfChange(cc)
					}
				case pb.EntryNormal:
					if len(ent.Data) > 0 {
						cp := make([]byte, len(ent.Data))
						copy(cp, ent.Data)
						n.committed = append(n.committed, cp)
					}
				}
				if ent.Index > n.applied {
					n.applied = ent.Index
				}
			}

			// (4) Persistence succeeded: NOW we may tell raft this Ready is durably handled.
			n.raw.Advance(rd)
			did = true
			progressed = true
		}
		if !did {
			return progressed, firstErr
		}
	}
}

// deliver handles an outbound message produced under sh.mu. Self-directed messages
// (raft sometimes emits messages addressed to the sender that must not traverse the
// network) are stepped inline while the lock is held. Cross-node traffic is NOT sent
// here — sending under sh.mu would let a synchronous transport invoke the receiver's
// inbox handler (which now takes sh.mu) on this same goroutine and deadlock, and an
// async transport could also deliver concurrently with this critical section. Instead
// such messages are queued in s.pending and flushed by deliverPending after sh.mu is
// released. Caller holds sh.mu.
func (s *shard) deliver(msg pb.Message) {
	if msg.To == msg.From {
		if dst, ok := s.nodes[msg.To]; ok && !dst.stopped {
			_ = dst.raw.Step(msg)
		}
		return
	}
	s.pending = append(s.pending, msg)
}

// deliverPending sends every queued cross-node message over the transport. It MUST
// be called WITHOUT holding sh.mu: transport.Send may (for a synchronous transport)
// invoke the destination's inbox handler inline, and that handler takes sh.mu. By
// flushing outside the lock, both synchronous and asynchronous delivery are race-free
// and deadlock-free — the receiving handler always serializes its Step under sh.mu.
func (s *shard) deliverPending() {
	s.mu.Lock()
	msgs := s.pending
	s.pending = nil
	s.mu.Unlock()
	for _, msg := range msgs {
		_ = s.transport.Send(s.id, msg)
	}
}
