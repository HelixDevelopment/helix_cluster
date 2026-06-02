package kraft

import (
	"fmt"
	"sync"

	pb "go.etcd.io/raft/v3/raftpb"
)

// raftTransport abstracts how raft messages travel between the members of the
// single controller quorum. It is an interface so the delivery mechanism can be
// swapped (in-process for tests / single host, a network transport in
// production) without touching the raft driving logic.
//
// WHY no group/shard qualifier (unlike a multi-group manager): the KRaft
// controller is exactly ONE raft group, so a (To, From) node id pair is globally
// unambiguous and routing needs only the node id.
type raftTransport interface {
	// Send delivers msg to msg.To. Implementations must be safe for concurrent
	// use. A message to an unregistered (e.g. stopped) peer must NOT error the
	// sender — that mirrors a real network where a downed peer's messages are
	// simply lost, letting the surviving quorum keep making progress.
	Send(msg pb.Message) error
	// RegisterPeer wires a destination node's inbound handler into the transport.
	RegisterPeer(nodeID uint64, inbox messageHandler)
	// UnregisterPeer removes a peer; later Sends to it are dropped silently.
	UnregisterPeer(nodeID uint64)
}

// messageHandler receives a raft message destined for a particular member.
type messageHandler func(msg pb.Message)

// inProcTransport is a REAL in-process raftTransport that genuinely delivers raft
// messages between the registered controller members (not a mock or no-op). The
// separate RawNodes hand each other real MsgVote/MsgApp/MsgHeartbeat traffic
// through this transport, so the etcd raft consensus protocol runs in full.
// Delivery is in-memory and synchronous for deterministic tests.
type inProcTransport struct {
	mu    sync.RWMutex
	inbox map[uint64]messageHandler
	// dropped counts messages sent to peers that are not (or no longer)
	// registered; surfaced so tests can assert leader-loss behaviour.
	dropped int
}

// newInProcTransport constructs an empty in-process transport.
func newInProcTransport() *inProcTransport {
	return &inProcTransport{inbox: make(map[uint64]messageHandler)}
}

var _ raftTransport = (*inProcTransport)(nil)

// RegisterPeer wires nodeID's handler into the transport.
func (t *inProcTransport) RegisterPeer(nodeID uint64, inbox messageHandler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inbox[nodeID] = inbox
}

// UnregisterPeer removes nodeID's handler; later Sends to it are dropped.
func (t *inProcTransport) UnregisterPeer(nodeID uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.inbox, nodeID)
}

// Send routes msg to its destination member's handler. If the destination is not
// registered the message is counted as dropped and Send returns nil — a downed
// peer must not error the leader, modelling a real network and letting the
// remaining quorum keep committing.
func (t *inProcTransport) Send(msg pb.Message) error {
	if msg.To == 0 {
		return fmt.Errorf("kraft: transport refusing message with zero destination")
	}
	t.mu.RLock()
	handler, ok := t.inbox[msg.To]
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
func (t *inProcTransport) Dropped() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.dropped
}
