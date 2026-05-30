// Package leader provides leader election utilities for Helix Cluster OS.
package leader

import "sync/atomic"

// Election is a simple leader election placeholder.
type Election struct {
	isLeader int32
}

// NewElection creates a new Election.
func NewElection() *Election {
	return &Election{}
}

// IsLeader returns true if this instance is the leader.
func (e *Election) IsLeader() bool {
	return atomic.LoadInt32(&e.isLeader) == 1
}

// BecomeLeader marks this instance as leader.
func (e *Election) BecomeLeader() {
	atomic.StoreInt32(&e.isLeader, 1)
}

// Resign resigns leadership.
func (e *Election) Resign() {
	atomic.StoreInt32(&e.isLeader, 0)
}
