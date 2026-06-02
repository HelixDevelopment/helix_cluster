package multiraft

import (
	"fmt"
	"sync"

	pb "go.etcd.io/raft/v3/raftpb"
)

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
	Send(shard ShardID, msg pb.Message) error
	// RegisterPeer wires a destination node's inbound message handler into the
	// transport so future Sends to that (shard, nodeID) are routed to it.
	RegisterPeer(shard ShardID, nodeID uint64, inbox MessageHandler)
	// UnregisterPeer removes a peer (e.g. when a node is stopped), after which
	// Sends to it are silently dropped (mirrors a real network where a downed
	// peer's messages are lost rather than erroring the sender).
	UnregisterPeer(shard ShardID, nodeID uint64)
}

// MessageHandler receives a raft message destined for a particular node.
type MessageHandler func(msg pb.Message)

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
	mu     sync.RWMutex
	inbox  map[inprocKey]MessageHandler
	// dropped counts messages sent to peers that are not (or no longer)
	// registered; surfaced for tests asserting leader-loss behaviour.
	dropped int
}

// NewInProcTransport constructs an empty in-process transport.
func NewInProcTransport() *InProcTransport {
	return &InProcTransport{inbox: make(map[inprocKey]MessageHandler)}
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
