package multiraft

import (
	"errors"
	"fmt"
	"sync"

	pb "go.etcd.io/raft/v3/raftpb"
)

// ErrNoReadHandler is returned by SendRead when the targeted leaseholder has no
// registered read handler (e.g. it was stopped or never wired). Callers treat
// this as "leaseholder unavailable, retry / re-resolve lease".
var ErrNoReadHandler = errors.New("multiraft: no read handler registered for leaseholder")

// ReadHandler serves a leaseholder-local read, returning the latest committed
// application value for the shard. It performs NO Raft proposal.
type ReadHandler func() ([]byte, error)

// RaftTransport abstracts how raft messages are delivered between the peers of a
// shard. It is an interface so the delivery mechanism can be swapped (in-process
// for tests/single-host, a network transport in production) without touching the
// raft driving logic.
//
// WHY scoped by shard: every raft message names a (To, From) node within a single
// raft group. Because the same numeric node IDs are reused across shards, routing
// MUST be qualified by ShardID — otherwise a MsgApp for shard A's node 2 could be
// mis-delivered to shard B's node 2. Send therefore carries the ShardID.
type RaftTransport interface {
	// Send delivers msg to msg.To within the given shard. Implementations must be
	// safe for concurrent use because multiple shards drive sends in parallel.
	//
	// SYNCHRONOUS-DELIVERY CONTRACT (load-bearing): Send MUST deliver msg to the
	// destination's registered MessageHandler synchronously, on the calling
	// goroutine, BEFORE Send returns. The manager invokes Send only from inside
	// processReadyLocked while holding the source shard's sh.mu, and the registered
	// handler steps the destination RawNode and reads dst.stopped WITHOUT taking a
	// lock — that is sound ONLY because synchronous in-band delivery means every
	// handler invocation for a shard is serialized under that same sh.mu. An
	// implementation that delivers asynchronously (a goroutine, a network round
	// trip, queued dispatch) would invoke the handler concurrently with the shard's
	// own driving and race on dst.stopped / RawNode.Step; such a transport MUST
	// instead serialize delivery per shard (e.g. step under the shard's lock on the
	// receiving side) before it can satisfy this interface. InProcTransport meets
	// the contract by calling the handler inline within Send.
	Send(shard ShardID, msg pb.Message) error
	// RegisterPeer wires a destination node's inbound message handler into the
	// transport so future Sends to that (shard, nodeID) are routed to it.
	RegisterPeer(shard ShardID, nodeID uint64, inbox MessageHandler)
	// UnregisterPeer removes a peer (e.g. when a node is stopped), after which
	// Sends to it are silently dropped (mirrors a real network where a downed
	// peer's messages are lost rather than erroring the sender).
	UnregisterPeer(shard ShardID, nodeID uint64)
	// SendRead forwards a follower-originated read request to the shard's current
	// leaseholder so the read is served from that node's local committed store
	// WITHOUT a Raft proposal (CockroachDB leaseholder-local-read pattern). It is
	// the routing half of the read path: a non-leaseholder cannot serve the read
	// locally, so it asks the leaseholder over the transport. The leaseholder's
	// read handler must have been wired via RegisterReadHandler. Returns the
	// committed value (or ErrNoReadHandler if no leaseholder handler is present).
	SendRead(shard ShardID, leaseholderID uint64) ([]byte, error)
}

// MessageHandler receives a raft message destined for a particular node.
type MessageHandler func(msg pb.Message)

// ReadRouter is the read-handler-registration extension of a transport. SendRead
// itself lives on RaftTransport (so the read can be routed through any transport),
// but wiring a leaseholder's local-read handler is delivery-mechanism specific, so
// it is kept on this opt-in interface. The manager type-asserts its transport to
// ReadRouter in EnableLeaseholderReads; a transport that does not implement it
// cannot serve leaseholder-local reads and the call returns an honest error.
type ReadRouter interface {
	// RegisterReadHandler wires a leaseholder's local-read handler.
	RegisterReadHandler(shard ShardID, nodeID uint64, h ReadHandler)
	// UnregisterReadHandler removes a leaseholder's read handler.
	UnregisterReadHandler(shard ShardID, nodeID uint64)
}

var _ ReadRouter = (*InProcTransport)(nil)

// inprocKey identifies a single destination peer within the transport.
type inprocKey struct {
	shard  ShardID
	nodeID uint64
}

// InProcTransport is a REAL in-process RaftTransport that actually delivers raft
// messages between the registered peers (not a mock or no-op). It is the genuine
// multi-node exercise: separate RawNodes hand each other real MsgVote/MsgApp/etc.
// traffic through this transport, so the etcd raft consensus protocol runs in
// full. Delivery is in-memory and synchronous for determinism.
type InProcTransport struct {
	mu    sync.RWMutex
	inbox map[inprocKey]MessageHandler
	// readHandlers maps a (shard, nodeID) leaseholder to its local-read handler,
	// wired by the manager when a node acquires a lease. SendRead routes here.
	readHandlers map[inprocKey]ReadHandler
	// dropped counts messages sent to peers that are not (or no longer)
	// registered; surfaced for tests asserting leader-loss behaviour.
	dropped int
	// routedReads counts reads forwarded over the transport to a leaseholder —
	// the sink-side evidence that a follower's read was genuinely routed rather
	// than served locally (CLAUDE-1: assert the feature actually happened).
	routedReads int
}

// NewInProcTransport constructs an empty in-process transport.
func NewInProcTransport() *InProcTransport {
	return &InProcTransport{
		inbox:        make(map[inprocKey]MessageHandler),
		readHandlers: make(map[inprocKey]ReadHandler),
	}
}

var _ RaftTransport = (*InProcTransport)(nil)

// RegisterPeer wires nodeID's handler into the transport for the given shard.
func (t *InProcTransport) RegisterPeer(shard ShardID, nodeID uint64, inbox MessageHandler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inbox[inprocKey{shard: shard, nodeID: nodeID}] = inbox
}

// UnregisterPeer removes nodeID's handler; later Sends to it are dropped.
func (t *InProcTransport) UnregisterPeer(shard ShardID, nodeID uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.inbox, inprocKey{shard: shard, nodeID: nodeID})
}

// Send routes msg to its destination peer's handler within shard. If the
// destination is not registered the message is counted as dropped and Send
// returns nil — a downed peer must not error the leader, which models a real
// network and lets the remaining quorum keep making progress.
func (t *InProcTransport) Send(shard ShardID, msg pb.Message) error {
	if msg.To == 0 {
		return fmt.Errorf("multiraft: transport refusing message with zero destination in shard %v", shard)
	}
	t.mu.RLock()
	handler, ok := t.inbox[inprocKey{shard: shard, nodeID: msg.To}]
	t.mu.RUnlock()
	if !ok {
		t.mu.Lock()
		t.dropped++
		t.mu.Unlock()
		return nil
	}
	handler(msg)
	return nil
}

// Dropped returns how many messages were sent to unregistered peers.
func (t *InProcTransport) Dropped() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.dropped
}

// RegisterReadHandler wires a leaseholder's local-read handler so SendRead can
// route follower reads to it. Called by the manager when a node acquires a lease.
func (t *InProcTransport) RegisterReadHandler(shard ShardID, nodeID uint64, h ReadHandler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.readHandlers[inprocKey{shard: shard, nodeID: nodeID}] = h
}

// UnregisterReadHandler removes a leaseholder's read handler (e.g. on lease loss
// or node stop). Later SendReads to it return ErrNoReadHandler.
func (t *InProcTransport) UnregisterReadHandler(shard ShardID, nodeID uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.readHandlers, inprocKey{shard: shard, nodeID: nodeID})
}

// SendRead routes a follower-originated read to the shard's leaseholder. It
// genuinely invokes the leaseholder's registered read handler (no Raft proposal)
// and returns the committed value. It increments routedReads so tests can prove
// the read was forwarded rather than served locally.
func (t *InProcTransport) SendRead(shard ShardID, leaseholderID uint64) ([]byte, error) {
	t.mu.Lock()
	h, ok := t.readHandlers[inprocKey{shard: shard, nodeID: leaseholderID}]
	if ok {
		t.routedReads++
	}
	t.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: shard %v node %d", ErrNoReadHandler, shard, leaseholderID)
	}
	return h()
}

// RoutedReads returns how many reads were forwarded to a leaseholder over the
// transport — sink-side evidence that follower reads were genuinely routed.
func (t *InProcTransport) RoutedReads() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.routedReads
}
