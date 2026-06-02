// Package multiraft implements a MultiRaftManager that partitions cluster state
// into independent shards, each running its own etcd raft group (RawNode) with
// its own elected leader.
//
// WHY: A single Raft group funnels every write through one leader, which becomes
// the cluster's write bottleneck. By sharding state across many independent raft
// groups, proposals to different shards proceed in parallel and throughput scales
// with the shard count rather than serializing through a single leader. This file
// provides the per-shard raft.Storage implementation.
package multiraft

import (
	"go.etcd.io/raft/v3"
	pb "go.etcd.io/raft/v3/raftpb"
)

// ShardStorage is a per-shard, in-memory implementation of the etcd raft.Storage
// interface. It is backed by raft.MemoryStorage (a REAL implementation shipped by
// the etcd raft library, not a mock) so the underlying raft state machine reads
// and persists genuine log entries, HardState, and snapshots.
//
// WHY a thin wrapper instead of using MemoryStorage directly: keeping a named
// type per shard makes the ownership boundary explicit (each shard has exactly
// one storage), gives us a place to hang shard-scoped helpers, and lets the
// MultiRaftManager treat storage as a swappable component while still satisfying
// raft.Storage so we drive the genuine etcd raft engine.
type ShardStorage struct {
	mem *raft.MemoryStorage
}

// NewShardStorage builds an empty in-memory storage for a single shard.
func NewShardStorage() *ShardStorage {
	return &ShardStorage{mem: raft.NewMemoryStorage()}
}

// Compile-time assertion that ShardStorage satisfies the etcd raft.Storage
// interface. If a required method were dropped this would fail to build.
var _ raft.Storage = (*ShardStorage)(nil)

// InitialState implements raft.Storage.
func (s *ShardStorage) InitialState() (pb.HardState, pb.ConfState, error) {
	return s.mem.InitialState()
}

// Entries implements raft.Storage.
func (s *ShardStorage) Entries(lo, hi, maxSize uint64) ([]pb.Entry, error) {
	return s.mem.Entries(lo, hi, maxSize)
}

// Term implements raft.Storage.
func (s *ShardStorage) Term(i uint64) (uint64, error) {
	return s.mem.Term(i)
}

// LastIndex implements raft.Storage.
func (s *ShardStorage) LastIndex() (uint64, error) {
	return s.mem.LastIndex()
}

// FirstIndex implements raft.Storage.
func (s *ShardStorage) FirstIndex() (uint64, error) {
	return s.mem.FirstIndex()
}

// Snapshot implements raft.Storage.
func (s *ShardStorage) Snapshot() (pb.Snapshot, error) {
	return s.mem.Snapshot()
}

// Append persists newly produced log entries to the shard's stable storage.
// Raft requires entries be persisted BEFORE outbound messages referencing them
// are sent; the shard driver enforces that ordering.
func (s *ShardStorage) Append(entries []pb.Entry) error {
	return s.mem.Append(entries)
}

// SetHardState persists the shard's current HardState (term, vote, commit).
func (s *ShardStorage) SetHardState(st pb.HardState) error {
	return s.mem.SetHardState(st)
}
