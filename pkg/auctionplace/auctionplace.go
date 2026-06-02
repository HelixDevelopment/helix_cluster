// Package auctionplace implements a bounty/auction-based economic placement
// mechanism with a single-writer claim lock.
//
// Model: an under-served workload posts a Bounty (id + reward). Multiple nodes
// attempt to CLAIM it. A single-writer lock guarantees EXACTLY ONE node wins
// the claim (no double-claim) even under concurrency; the winning node DEPLOYS
// the workload; losers get a typed ErrAlreadyClaimed.
//
// The package is self-contained, pure Go, deterministic and race-safe. It does
// not depend on any other in-repo package and performs no network, host or cgo
// operations.
package auctionplace

import (
	"errors"
	"fmt"
	"sync"
)

// Sentinel errors returned by Board operations.
var (
	// ErrAlreadyClaimed is returned by Claim when the bounty has already been
	// claimed by a (different or the same) node. This is the typed loser error.
	ErrAlreadyClaimed = errors.New("auctionplace: bounty already claimed")
	// ErrUnknownBounty is returned by Claim/Winner/Deployed when the bounty id
	// was never posted.
	ErrUnknownBounty = errors.New("auctionplace: unknown bounty")
	// ErrDuplicatePost is returned by Post when a bounty id is posted twice.
	ErrDuplicatePost = errors.New("auctionplace: duplicate bounty id")
)

// Bounty is a posted reward for placing an under-served workload.
type Bounty struct {
	ID     string
	Reward int64
}

// Claim is the receipt of a successful, winning claim on a bounty.
type Claim struct {
	BountyID string
	NodeID   string
	// Reward carries the bounty reward at the moment of winning, so the winner
	// has an immutable record independent of later board mutations.
	Reward int64
}

// entry is the internal, mutex-guarded state for a single bounty.
type entry struct {
	bounty   Bounty
	claimed  bool
	winner   string
	deployed bool
}

// Board is a concurrency-safe bounty board. The zero value is not usable; use
// NewBoard.
type Board struct {
	mu      sync.Mutex
	entries map[string]*entry
}

// NewBoard returns an empty Board ready for use.
func NewBoard() *Board {
	return &Board{entries: make(map[string]*entry)}
}

// Post publishes a bounty. Re-posting an existing id is rejected with
// ErrDuplicatePost so that a claimed bounty can never be silently reset.
func (b *Board) Post(bounty Bounty) error {
	if bounty.ID == "" {
		return fmt.Errorf("auctionplace: empty bounty id")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.entries[bounty.ID]; ok {
		return ErrDuplicatePost
	}
	b.entries[bounty.ID] = &entry{bounty: bounty}
	return nil
}

// Claim attempts to claim the bounty for nodeID. The FIRST/winning caller
// succeeds, is recorded as the Winner, the workload is marked Deployed, and a
// Claim receipt is returned. Every subsequent caller (any node, including the
// winner re-claiming) receives ErrAlreadyClaimed. An unknown bounty id returns
// ErrUnknownBounty.
//
// The single-writer guarantee is enforced by holding b.mu across the
// test-and-set of e.claimed, so that under arbitrary concurrency exactly one
// goroutine observes claimed==false and transitions it to true.
func (b *Board) Claim(bountyID, nodeID string) (Claim, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.entries[bountyID]
	if !ok {
		return Claim{}, ErrUnknownBounty
	}
	if e.claimed {
		return Claim{}, ErrAlreadyClaimed
	}
	// Single-writer critical section: claim + deploy happen atomically.
	e.claimed = true
	e.winner = nodeID
	e.deployed = true
	return Claim{BountyID: bountyID, NodeID: nodeID, Reward: e.bounty.Reward}, nil
}

// Winner returns the node id that won the bounty. ok is false if the bounty is
// unknown or has not yet been claimed.
func (b *Board) Winner(bountyID string) (nodeID string, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, exists := b.entries[bountyID]
	if !exists || !e.claimed {
		return "", false
	}
	return e.winner, true
}

// Deployed reports whether the workload for the bounty has been deployed (i.e.
// the bounty has a winning claim). It returns false for unknown or unclaimed
// bounties.
func (b *Board) Deployed(bountyID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.entries[bountyID]
	if !ok {
		return false
	}
	return e.deployed
}
