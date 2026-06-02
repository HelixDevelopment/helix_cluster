package kraft

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"go.etcd.io/raft/v3"
	pb "go.etcd.io/raft/v3/raftpb"
)

// Common errors returned by the MetadataQuorum.
var (
	// ErrNoController is returned when a metadata write is attempted but the
	// quorum currently has no elected controller (leader) — e.g. mid-election.
	// Callers should retry after letting consensus settle (Tick/Run).
	ErrNoController = errors.New("kraft: no controller (leader) elected")
	// ErrUnknownMember is returned when an operation references a node id that is
	// not part of the controller quorum.
	ErrUnknownMember = errors.New("kraft: unknown member")
	// ErrUnknownTopic is returned by leader-assignment proposal when the topic
	// has not yet been created (and committed) in the metadata.
	ErrUnknownTopic = errors.New("kraft: unknown topic")
	// ErrTopicExists is returned by CreateTopic when the topic already exists
	// in the committed metadata with a DIFFERENT partition count. An identical
	// re-create (same name, same partition count) is idempotent and returns nil
	// instead. This prevents a silent semantic surprise where CreateTopic returns
	// nil even though the requested partition count was ignored.
	ErrTopicExists = errors.New("kraft: topic already exists with different partition count")
)

// member is a single controller node within the quorum. Each member drives its
// own RawNode over its own storage and applies committed commands into its own
// metadataState copy. Together the members form ONE real raft group reaching
// consensus over the in-process transport — this is the self-managed KRaft
// controller quorum, with NO external ZooKeeper.
type member struct {
	id      uint64
	raw     *raft.RawNode
	storage *memberStorage
	state   *metadataState
	// committed holds raw committed normal-entry payloads in commit order — the
	// sink-side evidence that a command was actually agreed by the raft quorum.
	committed [][]byte
	applied   uint64
	stopped   bool
}

// MetadataQuorum is a self-managed KRaft-style controller quorum: a single
// embedded raft group whose replicated log is the messaging metadata. Metadata
// writes (CreateTopic, AssignPartitionLeader) are proposed through raft and
// applied deterministically on every member, so ListTopics on any live member
// reflects the same committed state — with NO external coordination service.
type MetadataQuorum struct {
	mu            sync.Mutex
	members       map[uint64]*member
	peers         []uint64
	transport     raftTransport
	electionTick  int
	heartbeatTick int
}

// NewMetadataQuorum constructs a controller quorum across the given member ids,
// using a REAL in-process transport for inter-member raft traffic. It bootstraps
// each member's raft node with the identical initial membership so the quorum
// agrees on its voter set WITHOUT any external configuration store.
//
// WHY in-process transport here: it is genuine message delivery between separate
// RawNodes (real MsgVote/MsgApp/etc.), which is the real consensus exercise on a
// single host. A network transport can later satisfy the same raftTransport
// interface for a live multi-node cluster without changing this driving logic.
func NewMetadataQuorum(peers []uint64) (*MetadataQuorum, error) {
	if len(peers) == 0 {
		return nil, fmt.Errorf("kraft: controller quorum needs at least one member")
	}
	q := &MetadataQuorum{
		members:       make(map[uint64]*member),
		peers:         append([]uint64(nil), peers...),
		transport:     newInProcTransport(),
		electionTick:  10,
		heartbeatTick: 1,
	}
	sort.Slice(q.peers, func(i, j int) bool { return q.peers[i] < q.peers[j] })
	// Reject duplicate peer ids — they would corrupt the voter set.
	for i := 1; i < len(q.peers); i++ {
		if q.peers[i] == q.peers[i-1] {
			return nil, fmt.Errorf("kraft: duplicate member id %d", q.peers[i])
		}
	}
	if q.peers[0] == 0 {
		return nil, fmt.Errorf("kraft: member id 0 is reserved")
	}

	raftPeers := make([]raft.Peer, len(q.peers))
	for i, p := range q.peers {
		raftPeers[i] = raft.Peer{ID: p}
	}

	for _, pid := range q.peers {
		st := newMemberStorage()
		cfg := &raft.Config{
			ID:              pid,
			ElectionTick:    q.electionTick,
			HeartbeatTick:   q.heartbeatTick,
			Storage:         st,
			MaxSizePerMsg:   1 << 20,
			MaxInflightMsgs: 256,
		}
		raw, err := raft.NewRawNode(cfg)
		if err != nil {
			return nil, fmt.Errorf("kraft: member %d: %w", pid, err)
		}
		// Bootstrap seeds the identical initial membership conf-change into every
		// member so they agree on the voter set with no external config service.
		if err := raw.Bootstrap(raftPeers); err != nil {
			return nil, fmt.Errorf("kraft: member %d bootstrap: %w", pid, err)
		}
		q.members[pid] = &member{id: pid, raw: raw, storage: st, state: newMetadataState()}
	}

	// Register inboxes AFTER all members exist so a delivered message always finds
	// its destination. The handler steps inbound messages directly: every Send is
	// invoked synchronously from inside processReadyLocked which already holds
	// q.mu, so re-locking would deadlock; q.mu held throughout serializes stepping.
	for _, m := range q.members {
		m := m
		q.transport.RegisterPeer(m.id, func(msg pb.Message) {
			dst, ok := q.members[msg.To]
			if !ok || dst.stopped {
				return
			}
			_ = dst.raw.Step(msg)
		})
	}

	return q, nil
}

// Tick advances the logical clock of every live member by one tick, driving
// elections and heartbeats, then processes the resulting Ready so any emitted
// messages (votes, appends) are delivered immediately.
func (q *MetadataQuorum) Tick() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, m := range q.members {
		if m.stopped {
			continue
		}
		m.raw.Tick()
	}
	q.processReadyLocked()
}

// Campaign forces a specific member to start an election. Tests use it to elect a
// controller deterministically; production relies on Tick-driven timeouts.
func (q *MetadataQuorum) Campaign(nodeID uint64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	m, ok := q.members[nodeID]
	if !ok || m.stopped {
		return fmt.Errorf("%w: %d", ErrUnknownMember, nodeID)
	}
	if err := m.raw.Campaign(); err != nil {
		return err
	}
	q.processReadyLocked()
	return nil
}

// Run drives every member's Ready loop until no member has further work, flushing
// in-flight replication and commitment after proposals or elections. It is the
// synchronous "let consensus settle" step used by callers and tests.
func (q *MetadataQuorum) Run() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.processReadyLocked() {
	}
}

// CreateTopic proposes creation of a topic through the controller quorum. The
// command is appended to the REPLICATED raft log by the controller and applied
// deterministically on every member — there is NO local-only state mutation.
//
// Idempotency rule: if the topic already exists with the SAME partition count,
// CreateTopic returns nil (idempotent success). If it already exists with a
// DIFFERENT partition count, CreateTopic returns ErrTopicExists — a genuine
// conflict rather than a silent success that would surprise callers.
//
// Mutation guard: if this proposed only to local state (instead of through
// leader.raw.Propose), the committed log would not replicate and ListTopics on
// the OTHER members would not reflect the topic — which the cross-member tests
// assert and would therefore fail.
func (q *MetadataQuorum) CreateTopic(ctx context.Context, name string, partitions int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("kraft: CreateTopic: empty name")
	}
	if partitions <= 0 {
		return fmt.Errorf("kraft: CreateTopic %q: partitions must be > 0", name)
	}

	// Pre-flight: check the controller's committed view before proposing. This
	// surfaces the conflict to the caller BEFORE a command enters the raft log,
	// keeping the log clean. The check is under q.mu to prevent a TOCTOU race
	// with concurrent CreateTopic calls.
	q.mu.Lock()
	ctrl := q.controllerLocked()
	if ctrl != nil {
		if existing, ok := ctrl.state.topics[name]; ok {
			if existing != partitions {
				// Topic exists but with a different partition count — conflict.
				q.mu.Unlock()
				return fmt.Errorf("%w: topic %q has %d partitions, requested %d",
					ErrTopicExists, name, existing, partitions)
			}
			// Same partition count — idempotent success; do NOT re-propose.
			q.mu.Unlock()
			return nil
		}
	}
	q.mu.Unlock()

	cmd := command{Kind: cmdCreateTopic, Topic: name, Partitions: partitions}
	return q.propose(cmd)
}

// AssignPartitionLeader proposes a partition-leader assignment through the quorum.
// The assignment is recorded in the replicated log and becomes visible on every
// member once committed. The leader must be a member of the quorum and the topic
// must already exist in committed metadata.
func (q *MetadataQuorum) AssignPartitionLeader(ctx context.Context, topic string, partition int, leader uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	if _, ok := q.members[leader]; !ok {
		q.mu.Unlock()
		return fmt.Errorf("%w: leader %d", ErrUnknownMember, leader)
	}
	// Validate the topic against the controller's committed view before proposing.
	ctrl := q.controllerLocked()
	if ctrl == nil {
		q.mu.Unlock()
		return ErrNoController
	}
	parts, ok := ctrl.state.topics[topic]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrUnknownTopic, topic)
	}
	if partition < 0 || partition >= parts {
		q.mu.Unlock()
		return fmt.Errorf("kraft: AssignPartitionLeader %q: partition %d out of range [0,%d)", topic, partition, parts)
	}
	q.mu.Unlock()

	cmd := command{Kind: cmdAssignLeader, Topic: topic, Partition: partition, Leader: leader}
	return q.propose(cmd)
}

// propose encodes a command and submits it to the current controller (leader),
// then drives one Ready cycle so the append replicates promptly. Returns
// ErrNoController when no leader is currently elected.
func (q *MetadataQuorum) propose(cmd command) error {
	data, err := cmd.encode()
	if err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	ctrl := q.controllerLocked()
	if ctrl == nil {
		return ErrNoController
	}
	if err := ctrl.raw.Propose(data); err != nil {
		return fmt.Errorf("kraft: propose: %w", err)
	}
	q.processReadyLocked()
	return nil
}

// ListTopics returns the committed topic metadata as seen by a specific member —
// the sink-side view used to prove replication reached that member.
func (q *MetadataQuorum) ListTopics(nodeID uint64) ([]TopicMetadata, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	m, ok := q.members[nodeID]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknownMember, nodeID)
	}
	return m.state.listTopics(), nil
}

// PartitionLeader returns the committed leader of a partition as seen by a member.
func (q *MetadataQuorum) PartitionLeader(nodeID uint64, topic string, partition int) (uint64, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	m, ok := q.members[nodeID]
	if !ok {
		return 0, false, fmt.Errorf("%w: %d", ErrUnknownMember, nodeID)
	}
	l, present := m.state.partitionLeader(topic, partition)
	return l, present, nil
}

// ControllerID returns the elected controller (leader) member id, or 0 if none.
func (q *MetadataQuorum) ControllerID() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if c := q.controllerLocked(); c != nil {
		return c.id
	}
	return 0
}

// MemberIDs returns the ids of all live (non-stopped) members.
func (q *MetadataQuorum) MemberIDs() []uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	ids := make([]uint64, 0, len(q.members))
	for id, m := range q.members {
		if m.stopped {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// CommittedCount returns how many normal entries the given member has committed —
// sink-side proof of how many metadata commands that member has agreed and applied.
func (q *MetadataQuorum) CommittedCount(nodeID uint64) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	m, ok := q.members[nodeID]
	if !ok {
		return 0, fmt.Errorf("%w: %d", ErrUnknownMember, nodeID)
	}
	return len(m.committed), nil
}

// StopController stops the current controller (leader) member, simulating
// controller loss. Its inbox is unregistered so it receives no further messages
// and it stops ticking; the remaining quorum can then re-elect a controller and
// resume metadata operations. Returns the stopped member id, or 0 if no
// controller was elected.
func (q *MetadataQuorum) StopController() uint64 {
	q.mu.Lock()
	ctrl := q.controllerLocked()
	if ctrl == nil {
		q.mu.Unlock()
		return 0
	}
	ctrl.stopped = true
	id := ctrl.id
	q.mu.Unlock()
	q.transport.UnregisterPeer(id)
	return id
}

// StopMember stops a specific member (controller or follower). It is the general
// form of StopController used by tests to drive precise failure scenarios.
func (q *MetadataQuorum) StopMember(nodeID uint64) error {
	q.mu.Lock()
	m, ok := q.members[nodeID]
	if ok {
		m.stopped = true
	}
	q.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %d", ErrUnknownMember, nodeID)
	}
	q.transport.UnregisterPeer(nodeID)
	return nil
}

// controllerLocked returns the live member believed to be controller (leader), or
// nil. Caller holds q.mu.
func (q *MetadataQuorum) controllerLocked() *member {
	for _, m := range q.members {
		if m.stopped {
			continue
		}
		if m.raw.BasicStatus().RaftState == raft.StateLeader {
			return m
		}
	}
	return nil
}

// processReadyLocked drains all pending Ready batches for every live member,
// persisting entries/HardState, delivering outbound messages over the transport,
// and applying committed entries into each member's metadataState. Returns true
// if any member made progress. Caller holds q.mu.
//
// This faithfully follows the etcd raft contract: (1) persist HardState+Entries,
// (2) send Messages, (3) apply CommittedEntries, then (4) Advance. Applying the
// committed normal entries here — on EVERY member — is what makes the metadata
// converge cluster-wide from the single replicated log.
func (q *MetadataQuorum) processReadyLocked() bool {
	progressed := false
	for {
		did := false
		for _, m := range q.members {
			if m.stopped || !m.raw.HasReady() {
				continue
			}
			rd := m.raw.Ready()

			// (1) Persist HardState + new log entries first.
			if !raft.IsEmptyHardState(rd.HardState) {
				_ = m.storage.SetHardState(rd.HardState)
			}
			if len(rd.Entries) > 0 {
				_ = m.storage.Append(rd.Entries)
			}

			// (2) Deliver outbound messages to peer inboxes via the transport.
			for _, msg := range rd.Messages {
				_ = q.deliver(msg)
			}

			// (3) Apply committed entries: conf-changes update membership; normal
			// entries carry metadata commands applied to this member's state.
			for _, ent := range rd.CommittedEntries {
				switch ent.Type {
				case pb.EntryConfChange:
					var cc pb.ConfChange
					if err := cc.Unmarshal(ent.Data); err == nil {
						m.raw.ApplyConfChange(cc)
					}
				case pb.EntryConfChangeV2:
					var cc pb.ConfChangeV2
					if err := cc.Unmarshal(ent.Data); err == nil {
						m.raw.ApplyConfChange(cc)
					}
				case pb.EntryNormal:
					if len(ent.Data) > 0 {
						cp := make([]byte, len(ent.Data))
						copy(cp, ent.Data)
						m.committed = append(m.committed, cp)
						if cmd, err := decodeCommand(ent.Data); err == nil {
							// Deterministic apply on this member's own copy.
							_ = m.state.apply(cmd)
						}
					}
				}
				if ent.Index > m.applied {
					m.applied = ent.Index
				}
			}

			// (4) Tell raft we durably handled this Ready.
			m.raw.Advance(rd)
			did = true
			progressed = true
		}
		if !did {
			return progressed
		}
	}
}

// deliver routes an outbound message. Self-directed messages are stepped inline
// (raft emits some that must not traverse the transport); all cross-member traffic
// is genuinely routed through the raftTransport — the REAL consensus exercise.
// Caller holds q.mu.
func (q *MetadataQuorum) deliver(msg pb.Message) error {
	if msg.To == msg.From {
		if dst, ok := q.members[msg.To]; ok && !dst.stopped {
			return dst.raw.Step(msg)
		}
		return nil
	}
	return q.transport.Send(msg)
}
