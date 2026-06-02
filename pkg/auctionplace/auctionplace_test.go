package auctionplace

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// newRunUUID returns a deterministic-but-unique-per-run id so log lines from a
// single test invocation can be correlated. It is seeded from a package counter
// plus the test name; no time/randomness so the test stays deterministic.
var runCounter atomic.Uint64

func newRunUUID(name string) string {
	return fmt.Sprintf("run-%s-%04d", name, runCounter.Add(1))
}

// TestClaimDeploysExactlyOneNode covers CLOSURE clause 1:
// Post a bounty; before = no node deployed; after = exactly ONE node claims and
// Deployed==true with that node as Winner.
//
// Mutation: if Claim stops setting e.deployed/e.winner (or Deployed/Winner stop
// reading them), the "after" assertions below fail. If the claim lock is removed
// the second Claim no longer returns ErrAlreadyClaimed and the test fails.
func TestClaimDeploysExactlyOneNode(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()
	const bountyID = "wl-underserved-1"
	if err := b.Post(Bounty{ID: bountyID, Reward: 42}); err != nil {
		t.Fatalf("[%s] Post: unexpected error: %v", uuid, err)
	}

	// BEFORE observable: nothing deployed, no winner.
	beforeDeployed := b.Deployed(bountyID)
	_, beforeWinnerOK := b.Winner(bountyID)
	t.Logf("[%s] BEFORE deployed=%v winnerOK=%v", uuid, beforeDeployed, beforeWinnerOK)
	if beforeDeployed {
		t.Fatalf("[%s] BEFORE: expected not deployed, got deployed", uuid)
	}
	if beforeWinnerOK {
		t.Fatalf("[%s] BEFORE: expected no winner, got one", uuid)
	}

	// The winning claim.
	claim, err := b.Claim(bountyID, "node-A")
	if err != nil {
		t.Fatalf("[%s] Claim(node-A): unexpected error: %v", uuid, err)
	}
	if claim.NodeID != "node-A" || claim.BountyID != bountyID {
		t.Fatalf("[%s] Claim receipt wrong: %+v", uuid, claim)
	}
	if claim.Reward != 42 {
		t.Fatalf("[%s] Claim reward = %d, want 42", uuid, claim.Reward)
	}

	// A different node attempting to claim must lose with the typed error.
	if _, err := b.Claim(bountyID, "node-B"); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("[%s] Claim(node-B) err = %v, want ErrAlreadyClaimed", uuid, err)
	}

	// AFTER observable: deployed, winner is exactly node-A.
	afterDeployed := b.Deployed(bountyID)
	winner, afterWinnerOK := b.Winner(bountyID)
	t.Logf("[%s] AFTER deployed=%v winner=%q winnerOK=%v", uuid, afterDeployed, winner, afterWinnerOK)
	if !afterDeployed {
		t.Fatalf("[%s] AFTER: expected deployed==true", uuid)
	}
	if !afterWinnerOK || winner != "node-A" {
		t.Fatalf("[%s] AFTER: winner = %q (ok=%v), want node-A", uuid, winner, afterWinnerOK)
	}
}

// TestSingleWriterExactlyOneWinner covers CLOSURE clause 2:
// many goroutines Claim the SAME bounty concurrently -> exactly ONE returns
// success, all others ErrAlreadyClaimed; Winner is stable. Run with -race.
//
// Mutation: removing the claim lock (the e.claimed test-and-set under b.mu)
// allows multiple goroutines to observe claimed==false and succeed, driving
// successCount > 1. The exactly-one assertion then fails detectably, and -race
// surfaces the data race on the entry fields.
func TestSingleWriterExactlyOneWinner(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()
	const bountyID = "wl-contended"
	if err := b.Post(Bounty{ID: bountyID, Reward: 7}); err != nil {
		t.Fatalf("[%s] Post: %v", uuid, err)
	}

	const goroutines = 256
	var (
		successCount atomic.Int64
		loserCount   atomic.Int64
		otherCount   atomic.Int64
		wg           sync.WaitGroup
		start        = make(chan struct{})
	)

	// BEFORE observable.
	t.Logf("[%s] BEFORE deployed=%v goroutines=%d", uuid, b.Deployed(bountyID), goroutines)

	winners := make([]string, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			nodeID := fmt.Sprintf("node-%03d", idx)
			<-start // maximize contention
			c, err := b.Claim(bountyID, nodeID)
			switch {
			case err == nil:
				successCount.Add(1)
				winners[idx] = c.NodeID
			case errors.Is(err, ErrAlreadyClaimed):
				loserCount.Add(1)
			default:
				otherCount.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	succ := successCount.Load()
	lose := loserCount.Load()
	other := otherCount.Load()

	// Determine the single recorded winner node.
	var winnerNode string
	for _, w := range winners {
		if w != "" {
			winnerNode = w
			break
		}
	}
	boardWinner, ok := b.Winner(bountyID)

	// AFTER observable.
	t.Logf("[%s] AFTER success=%d losers=%d other=%d deployed=%v boardWinner=%q claimWinner=%q",
		uuid, succ, lose, other, b.Deployed(bountyID), boardWinner, winnerNode)

	if succ != 1 {
		t.Fatalf("[%s] exactly-one violated: success=%d (want 1)", uuid, succ)
	}
	if other != 0 {
		t.Fatalf("[%s] unexpected non-typed errors: %d", uuid, other)
	}
	if lose != goroutines-1 {
		t.Fatalf("[%s] losers=%d, want %d", uuid, lose, goroutines-1)
	}
	if !ok {
		t.Fatalf("[%s] board reports no winner after contention", uuid)
	}
	if boardWinner != winnerNode {
		t.Fatalf("[%s] winner unstable: board=%q claim=%q", uuid, boardWinner, winnerNode)
	}
	if !b.Deployed(bountyID) {
		t.Fatalf("[%s] expected deployed after winning claim", uuid)
	}
}

// TestClaimUnknownBounty covers CLOSURE clause 3:
// claiming a bounty that was never posted yields a typed error.
//
// Mutation: if Claim stops checking map membership (treats missing as claimable)
// it would not return ErrUnknownBounty; the errors.Is check below fails.
func TestClaimUnknownBounty(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()

	beforeDeployed := b.Deployed("ghost")
	t.Logf("[%s] BEFORE deployed=%v", uuid, beforeDeployed)

	_, err := b.Claim("ghost", "node-X")
	t.Logf("[%s] AFTER claim err=%v", uuid, err)
	if !errors.Is(err, ErrUnknownBounty) {
		t.Fatalf("[%s] Claim(unknown) err = %v, want ErrUnknownBounty", uuid, err)
	}
	if b.Deployed("ghost") {
		t.Fatalf("[%s] unknown bounty must never be deployed", uuid)
	}
	if _, ok := b.Winner("ghost"); ok {
		t.Fatalf("[%s] unknown bounty must have no winner", uuid)
	}
}

// TestDuplicatePostRejected guards the invariant that a posted (and possibly
// claimed) bounty cannot be silently reset by a re-Post.
//
// Mutation: if Post overwrites existing entries instead of returning
// ErrDuplicatePost, the winner/deployed state would be lost and the post-claim
// re-Post check below fails.
func TestDuplicatePostRejected(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()
	if err := b.Post(Bounty{ID: "dup", Reward: 1}); err != nil {
		t.Fatalf("[%s] first Post: %v", uuid, err)
	}
	if _, err := b.Claim("dup", "node-A"); err != nil {
		t.Fatalf("[%s] Claim: %v", uuid, err)
	}
	err := b.Post(Bounty{ID: "dup", Reward: 999})
	t.Logf("[%s] re-Post err=%v", uuid, err)
	if !errors.Is(err, ErrDuplicatePost) {
		t.Fatalf("[%s] re-Post err = %v, want ErrDuplicatePost", uuid, err)
	}
	// State must be preserved: winner still node-A, still deployed.
	if w, ok := b.Winner("dup"); !ok || w != "node-A" {
		t.Fatalf("[%s] winner changed after re-Post: %q ok=%v", uuid, w, ok)
	}
}
