// Package kraft implements a KRaft-style, SELF-MANAGED Raft metadata quorum for
// the messaging layer. It replaces any external ZooKeeper / coordination service
// with a single embedded Raft group (the "controller quorum") that holds the
// authoritative, replicated messaging metadata (topics, partitions, partition
// leaders).
//
// WHY this exists: classic Kafka-style brokers leaned on an external ZooKeeper
// ensemble to elect a controller and store metadata. That is an extra system to
// run, secure, and keep in sync. KRaft folds metadata + controller election into
// the brokers themselves via a self-managed Raft quorum. This package provides
// that mechanism on top of the genuine etcd raft engine (go.etcd.io/raft/v3):
// metadata writes are PROPOSED through Raft and applied deterministically by the
// replicated state machine on every member, so all members converge on identical
// metadata WITHOUT any external coordination service.
//
// This file provides the per-member raft.Storage implementation. It is fully
// self-contained: pkg/kraft does NOT import pkg/multiraft.
package kraft

import (
	"go.etcd.io/raft/v3"
	pb "go.etcd.io/raft/v3/raftpb"
)

// memberStorage is a per-member, in-memory implementation of the etcd
// raft.Storage interface. It wraps raft.MemoryStorage — a REAL storage shipped
// by the etcd raft library, not a mock — so each controller member persists
// genuine log entries, HardState, and snapshots and the raft engine runs in full.
//
// WHY a named wrapper instead of using MemoryStorage directly: it makes the
// one-storage-per-member ownership boundary explicit and gives a place to hang
// member-scoped helpers, while still satisfying raft.Storage.
type memberStorage struct {
	mem *raft.MemoryStorage
}

// newMemberStorage builds an empty in-memory storage for a single controller member.
func newMemberStorage() *memberStorage {
	return &memberStorage{mem: raft.NewMemoryStorage()}
}

// Compile-time assertion that memberStorage satisfies raft.Storage. Dropping a
// required method would fail the build here rather than at runtime.
var _ raft.Storage = (*memberStorage)(nil)

// InitialState implements raft.Storage.
func (s *memberStorage) InitialState() (pb.HardState, pb.ConfState, error) {
	return s.mem.InitialState()
}

// Entries implements raft.Storage.
func (s *memberStorage) Entries(lo, hi, maxSize uint64) ([]pb.Entry, error) {
	return s.mem.Entries(lo, hi, maxSize)
}

// Term implements raft.Storage.
func (s *memberStorage) Term(i uint64) (uint64, error) {
	return s.mem.Term(i)
}

// LastIndex implements raft.Storage.
func (s *memberStorage) LastIndex() (uint64, error) {
	return s.mem.LastIndex()
}

// FirstIndex implements raft.Storage.
func (s *memberStorage) FirstIndex() (uint64, error) {
	return s.mem.FirstIndex()
}

// Snapshot implements raft.Storage.
func (s *memberStorage) Snapshot() (pb.Snapshot, error) {
	return s.mem.Snapshot()
}

// Append persists newly produced log entries. Raft requires entries be persisted
// BEFORE outbound messages referencing them are sent; the driver enforces that.
func (s *memberStorage) Append(entries []pb.Entry) error {
	return s.mem.Append(entries)
}

// SetHardState persists the member's current HardState (term, vote, commit).
func (s *memberStorage) SetHardState(st pb.HardState) error {
	return s.mem.SetHardState(st)
}
