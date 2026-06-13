// Package raftleader adapts the proven embedded-Raft package (pkg/raft, built on
// github.com/hashicorp/raft) to the leader-election contract that the rest of
// Helix Cluster OS expects from pkg/leader.
//
// HXC-1216 delivered pkg/raft (real Raft consensus) and noted that the existing
// pkg/leader is "not real Raft" — it derives a leader deterministically from
// SWIM membership and renews via a TTL. This package provides a drop-in
// REAL-RAFT backend with a pkg/leader-shaped surface:
//
//	leader.Election           raftleader.RaftElection
//	  IsLeader() bool           IsLeader() bool
//	  LeaderID() string         LeaderID() string
//	  Resign()                  Resign()  (transfers leadership via real Raft)
//	  ErrNotLeader              ErrNotLeader (re-exported from pkg/raft)
//
// plus genuine, Raft-driven extras that the deterministic backend cannot offer:
//
//   - Campaign(): block until this node observes a leader (real election, not a
//     hash computation).
//   - LeadershipChanges(): a channel of acquire/lose events sourced directly
//     from hashicorp/raft's LeaderCh — true leadership-change observation.
//   - Apply(): propose a replicated command; rejected with ErrNotLeader on a
//     follower exactly like pkg/leader gates leader-only work.
//
// Unlike pkg/leader, leadership here is decided by a real Raft consensus group:
// a leader holds office only while it commands a quorum, and a NEW leader is
// automatically elected by the survivors when the current leader fails. This is
// not a mock or stub (CLAUDE-1 / CLAUDE-2): every assertion in the integration
// test is backed by actual replicated state and a real failover.
package raftleader

import (
	"context"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/raft"
)

// ErrNotLeader is returned when a leader-only operation (e.g. Apply) is invoked
// on a node that is not currently the Raft leader. It is the same sentinel used
// by pkg/raft (and mirrors pkg/leader.ErrNotLeader in spirit) so callers can
// treat the two election backends uniformly.
var ErrNotLeader = raft.ErrNotLeader

// LeadershipState is the observable leadership status delivered on the
// LeadershipChanges channel whenever this node acquires or loses leadership.
type LeadershipState struct {
	// IsLeader is true when this node has just become the Raft leader, false
	// when it has just lost leadership.
	IsLeader bool
	// LeaderID is the node believed to be the leader at the moment of the event
	// (this node's own ID on acquisition; possibly another node, or "" if
	// unknown, on loss).
	LeaderID string
	// At is the wall-clock time the transition was observed.
	At time.Time
}

// RaftElection adapts a single pkg/raft.Node to the pkg/leader-style election
// contract, backing leader election with REAL embedded Raft instead of a
// deterministic SWIM hash. One RaftElection instance corresponds to one node's
// participation in a Raft consensus group.
type RaftElection struct {
	node *raft.Node

	// changes carries leadership-acquire/lose events derived from the Raft
	// node's LeaderCh. It is buffered so a slow consumer (or no consumer) never
	// blocks the Raft internals; the watcher coalesces if the buffer fills.
	changes chan LeadershipState

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// New wraps an already-constructed pkg/raft.Node in a RaftElection and starts a
// background watcher that translates the node's Raft LeaderCh signals into
// LeadershipState events. Call Close to stop the watcher and release resources.
//
// The node must already be part of a bootstrapped Raft cluster (see
// NewInmemElectionCluster, which wires this up over pkg/raft's in-mem cluster).
func New(node *raft.Node) *RaftElection {
	re := &RaftElection{
		node:    node,
		changes: make(chan LeadershipState, 16),
		stopCh:  make(chan struct{}),
	}
	re.wg.Add(1)
	go re.watchLeadership()
	return re
}

// ID returns this node's Raft server ID.
func (e *RaftElection) ID() string { return e.node.ID() }

// IsLeader reports whether this node is currently the Raft leader. Unlike
// pkg/leader.IsLeader (which checks a TTL against a wall clock), this reflects
// the live Raft state machine: it is true only while the node actually holds
// leadership in the consensus group.
func (e *RaftElection) IsLeader() bool { return e.node.IsLeader() }

// LeaderID returns the server ID of the cluster leader as currently known by
// this node, or "" if no leader is known yet. Mirrors pkg/leader.LeaderID.
func (e *RaftElection) LeaderID() string { return e.node.Leader() }

// Node exposes the underlying pkg/raft.Node for advanced/read-only use (e.g.
// inspecting the replicated FSM). Most callers should not need this.
func (e *RaftElection) Node() *raft.Node { return e.node }

// LeadershipChanges returns the channel of leadership-acquire/lose events for
// this node. The channel is closed when Close is called. This is the real
// "leadership-change observation" hook backed by hashicorp/raft's LeaderCh.
func (e *RaftElection) LeadershipChanges() <-chan LeadershipState { return e.changes }

// Campaign blocks until this node observes a leader in the cluster (its own
// election or another node's), or the context is cancelled. It does NOT force
// this node to win — Raft decides the leader. It returns the observed leader's
// ID, or the context error on cancellation.
//
// This is the real-Raft analogue of pkg/leader.BecomeLeader: instead of
// computing a winner from a hash, it waits for the consensus group to elect one.
func (e *RaftElection) Campaign(ctx context.Context) (string, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if id := e.node.Leader(); id != "" {
			return id, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// WaitForLeadership blocks until THIS node becomes the leader or the context is
// cancelled. Returns nil on success, ctx.Err() otherwise.
func (e *RaftElection) WaitForLeadership(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if e.node.IsLeader() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Apply proposes a replicated command to the Raft log. It succeeds only on the
// leader; on a follower it returns ErrNotLeader. On success the command has been
// committed by a majority and applied to every replica's FSM. This is the
// leader-gated write path: it is exactly how pkg/leader callers expect
// leader-only work to be rejected on non-leaders.
func (e *RaftElection) Apply(cmd raft.Command, timeout time.Duration) error {
	return e.node.Apply(cmd, timeout)
}

// Resign voluntarily gives up leadership by asking Raft to transfer it to
// another voter. On a follower it is a no-op (returns nil). This is the
// real-Raft analogue of pkg/leader.Resign — but instead of merely flipping a
// local flag, it triggers an actual leadership transfer in the consensus group.
func (e *RaftElection) Resign() error {
	if !e.node.IsLeader() {
		return nil
	}
	// LeadershipTransfer hands off to the most up-to-date voter and blocks until
	// the transfer completes (or errors). We surface the error so callers can
	// distinguish a genuine failure from a clean handoff.
	if err := e.node.Raft().LeadershipTransfer().Error(); err != nil {
		return err
	}
	return nil
}

// Close stops the leadership watcher and closes the LeadershipChanges channel.
// It does NOT shut down the underlying Raft node (that is the cluster's
// responsibility) so the same node can be observed by a fresh RaftElection if
// desired. Close is idempotent.
func (e *RaftElection) Close() {
	e.stopOnce.Do(func() {
		close(e.stopCh)
	})
	e.wg.Wait()
}

// watchLeadership translates the Raft node's LeaderCh (acquire/lose booleans)
// into LeadershipState events on e.changes. It runs until Close. The send is
// non-blocking: if no consumer is draining e.changes, the latest state is kept
// fresh by dropping the oldest queued event rather than stalling Raft.
func (e *RaftElection) watchLeadership() {
	defer e.wg.Done()
	defer close(e.changes)

	leaderCh := e.node.Raft().LeaderCh()
	for {
		select {
		case <-e.stopCh:
			return
		case isLeader, ok := <-leaderCh:
			if !ok {
				// Raft node shut down; stop observing.
				return
			}
			ev := LeadershipState{
				IsLeader: isLeader,
				LeaderID: e.node.Leader(),
				At:       time.Now(),
			}
			e.deliver(ev)
		}
	}
}

// deliver pushes an event without ever blocking the caller. If the buffer is
// full it drops the oldest event to make room, preserving the most recent
// leadership state (which is what observers care about).
func (e *RaftElection) deliver(ev LeadershipState) {
	for {
		select {
		case e.changes <- ev:
			return
		default:
			// Buffer full: discard the oldest queued event, then retry.
			select {
			case <-e.changes:
			default:
			}
		}
	}
}
