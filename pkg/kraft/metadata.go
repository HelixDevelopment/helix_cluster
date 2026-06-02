package kraft

import (
	"encoding/json"
	"fmt"
	"sort"
)

// commandKind enumerates the metadata mutations that may be proposed through the
// controller quorum. Every metadata change is one of these and is encoded into a
// raft log entry, so the ONLY way state changes is via a committed, replicated
// command (CLAUDE-1 mutation guard: there is no local-only mutation path).
type commandKind uint8

const (
	// cmdCreateTopic creates a topic with a fixed partition count.
	cmdCreateTopic commandKind = iota + 1
	// cmdAssignLeader records the elected/assigned leader for one partition.
	cmdAssignLeader
)

// command is the serialized unit of metadata change carried in a raft log entry.
// It is applied DETERMINISTICALLY by every member's state machine, so all members
// converge on byte-identical metadata.
type command struct {
	Kind       commandKind `json:"kind"`
	Topic      string      `json:"topic,omitempty"`
	Partitions int         `json:"partitions,omitempty"`
	// Partition + Leader are used by cmdAssignLeader.
	Partition int    `json:"partition,omitempty"`
	Leader    uint64 `json:"leader,omitempty"`
}

// encode serializes a command for proposal through raft.
func (c command) encode() ([]byte, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("kraft: encode command: %w", err)
	}
	return b, nil
}

// decodeCommand parses a raft log entry payload back into a command.
func decodeCommand(data []byte) (command, error) {
	var c command
	if err := json.Unmarshal(data, &c); err != nil {
		return command{}, fmt.Errorf("kraft: decode command: %w", err)
	}
	return c, nil
}

// TopicMetadata is a read-only snapshot of one topic's replicated metadata.
type TopicMetadata struct {
	Name       string
	Partitions int
	// Leaders maps partition index -> controller member id that leads it. A
	// partition with no assigned leader is absent from the map.
	Leaders map[int]uint64
}

// metadataState is the replicated state machine. Each controller member holds its
// own copy; identical committed commands applied in the same order yield identical
// state on every member. It is NOT safe for concurrent use on its own — the owning
// member serializes all apply/read access under the member lock.
type metadataState struct {
	// topics maps topic name -> partition count.
	topics map[string]int
	// leaders maps topic name -> (partition -> leader member id).
	leaders map[string]map[int]uint64
}

// newMetadataState builds an empty replicated metadata state.
func newMetadataState() *metadataState {
	return &metadataState{
		topics:  make(map[string]int),
		leaders: make(map[string]map[int]uint64),
	}
}

// apply mutates the state for one committed command. It is the deterministic
// transition function: given the same prior state and command, every member
// produces the same next state. Returns an error only for genuinely malformed
// commands (which indicate a programming bug, not normal operation).
func (s *metadataState) apply(c command) error {
	switch c.Kind {
	case cmdCreateTopic:
		if c.Topic == "" {
			return fmt.Errorf("kraft: apply create-topic: empty topic name")
		}
		if c.Partitions <= 0 {
			return fmt.Errorf("kraft: apply create-topic %q: partitions must be > 0", c.Topic)
		}
		// First-writer-wins: the state machine always keeps the first committed
		// definition. An identical re-create (same partition count) is a true
		// idempotent no-op and returns nil. A conflicting re-create (different
		// partition count) returns ErrTopicExists so the caller/test can detect
		// the semantic conflict — the committed log entry is still absorbed
		// deterministically on every member (state unchanged), ensuring convergence.
		if existing, ok := s.topics[c.Topic]; ok {
			if existing == c.Partitions {
				return nil // idempotent — same definition, no change needed
			}
			return fmt.Errorf("%w: topic %q has %d partitions, command requested %d",
				ErrTopicExists, c.Topic, existing, c.Partitions)
		}
		s.topics[c.Topic] = c.Partitions
		if _, ok := s.leaders[c.Topic]; !ok {
			s.leaders[c.Topic] = make(map[int]uint64)
		}
		return nil
	case cmdAssignLeader:
		if c.Topic == "" {
			return fmt.Errorf("kraft: apply assign-leader: empty topic name")
		}
		parts, ok := s.topics[c.Topic]
		if !ok {
			return fmt.Errorf("kraft: apply assign-leader: unknown topic %q", c.Topic)
		}
		if c.Partition < 0 || c.Partition >= parts {
			return fmt.Errorf("kraft: apply assign-leader %q: partition %d out of range [0,%d)", c.Topic, c.Partition, parts)
		}
		if _, ok := s.leaders[c.Topic]; !ok {
			s.leaders[c.Topic] = make(map[int]uint64)
		}
		s.leaders[c.Topic][c.Partition] = c.Leader
		return nil
	default:
		return fmt.Errorf("kraft: apply: unknown command kind %d", c.Kind)
	}
}

// listTopics returns a sorted snapshot of all topics with their leader maps. The
// deterministic ordering makes cross-member equality checks straightforward.
func (s *metadataState) listTopics() []TopicMetadata {
	names := make([]string, 0, len(s.topics))
	for name := range s.topics {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]TopicMetadata, 0, len(names))
	for _, name := range names {
		leaders := make(map[int]uint64, len(s.leaders[name]))
		for p, l := range s.leaders[name] {
			leaders[p] = l
		}
		out = append(out, TopicMetadata{Name: name, Partitions: s.topics[name], Leaders: leaders})
	}
	return out
}

// partitionLeader returns the leader assigned to a partition and whether one is set.
func (s *metadataState) partitionLeader(topic string, partition int) (uint64, bool) {
	pm, ok := s.leaders[topic]
	if !ok {
		return 0, false
	}
	l, ok := pm[partition]
	return l, ok
}
