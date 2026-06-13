// Package raft provides a TRUE embedded Raft consensus implementation for
// Helix Cluster OS, built on top of the battle-tested github.com/hashicorp/raft
// library. It supplies leader election, log replication, and a replicated
// finite state machine (FSM) for cluster coordination.
//
// Unlike pkg/leader (which derives a leader deterministically from SWIM
// membership) and the etcd-backed locks, this package runs a real Raft
// consensus group: entries are only applied to the FSM once committed by a
// majority of the cluster, and a new leader is automatically elected when the
// current leader fails. This satisfies Roadmap §2.1 (real embedded Raft).
package raft

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	hraft "github.com/hashicorp/raft"
)

// CommandType enumerates the mutations the replicated state machine understands.
type CommandType string

const (
	// CommandSet stores a key/value pair.
	CommandSet CommandType = "set"
	// CommandDelete removes a key.
	CommandDelete CommandType = "delete"
)

// Command is a single replicated log entry. It is serialized to bytes, appended
// to the Raft log, replicated to a majority, and only then applied to every
// replica's FSM in identical order — giving a linearizable, fault-tolerant
// key/value store usable for cluster coordination (locks, config, leases…).
type Command struct {
	Op    CommandType `json:"op"`
	Key   string      `json:"key"`
	Value string      `json:"value,omitempty"`
}

// Encode serializes a Command for submission to Raft.
func (c Command) Encode() ([]byte, error) {
	return json.Marshal(c)
}

// KVFSM is a replicated key/value state machine implementing hashicorp/raft.FSM.
// Every committed log entry is applied here, deterministically and in the same
// order on every node, so all replicas converge to identical state.
type KVFSM struct {
	mu    sync.RWMutex
	store map[string]string
	// applied counts the number of log entries applied to this FSM. It is a
	// convenient sink-side signal for tests/observers to confirm replication.
	applied uint64
}

// NewKVFSM constructs an empty replicated key/value FSM.
func NewKVFSM() *KVFSM {
	return &KVFSM{store: make(map[string]string)}
}

// Apply is invoked by Raft once a log entry has been committed by a majority of
// the cluster. It mutates the FSM deterministically. The returned value becomes
// the ApplyFuture.Response on the node that proposed the command.
func (f *KVFSM) Apply(log *hraft.Log) interface{} {
	var cmd Command
	if err := json.Unmarshal(log.Data, &cmd); err != nil {
		return fmt.Errorf("raft fsm: decode command: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied++

	switch cmd.Op {
	case CommandSet:
		f.store[cmd.Key] = cmd.Value
		return nil
	case CommandDelete:
		delete(f.store, cmd.Key)
		return nil
	default:
		return fmt.Errorf("raft fsm: unknown op %q", cmd.Op)
	}
}

// Get returns the value for a key and whether it exists. Safe for concurrent use.
func (f *KVFSM) Get(key string) (string, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	v, ok := f.store[key]
	return v, ok
}

// Len returns the number of keys currently in the FSM.
func (f *KVFSM) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.store)
}

// AppliedCount returns the number of log entries applied to this FSM. This is a
// sink-side replication signal: on a follower it reflects entries replicated &
// committed, independent of which node proposed them.
func (f *KVFSM) AppliedCount() uint64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.applied
}

// Snapshot returns a point-in-time copy of the FSM for log compaction.
func (f *KVFSM) Snapshot() (hraft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	clone := make(map[string]string, len(f.store))
	for k, v := range f.store {
		clone[k] = v
	}
	return &kvSnapshot{state: clone}, nil
}

// Restore replaces the FSM state from a snapshot stream.
func (f *KVFSM) Restore(rc io.ReadCloser) error {
	defer func() { _ = rc.Close() }()
	var state map[string]string
	if err := json.NewDecoder(rc).Decode(&state); err != nil {
		return fmt.Errorf("raft fsm: restore decode: %w", err)
	}
	if state == nil {
		state = make(map[string]string)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store = state
	return nil
}

// kvSnapshot is an immutable copy of the FSM persisted during log compaction.
type kvSnapshot struct {
	state map[string]string
}

// Persist writes the snapshot to the sink as JSON.
func (s *kvSnapshot) Persist(sink hraft.SnapshotSink) error {
	err := func() error {
		data, err := json.Marshal(s.state)
		if err != nil {
			return err
		}
		if _, err := sink.Write(data); err != nil {
			return err
		}
		return sink.Close()
	}()
	if err != nil {
		_ = sink.Cancel()
		return fmt.Errorf("raft fsm: persist snapshot: %w", err)
	}
	return nil
}

// Release is a no-op for the in-memory snapshot.
func (s *kvSnapshot) Release() {}
