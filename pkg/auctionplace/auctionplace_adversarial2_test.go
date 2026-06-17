package auctionplace

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// auctionplace_adversarial2_test.go — DEEPER second pass focused on the two
// classes the first adversarial pass did not prove sink-side:
//
//   (C) POINTER/MAP/SLICE ALIASING of returned bid/offer state — prove that
//       neither the caller's input Bounty nor the returned Claim receipt shares
//       mutable backing storage with the Board's internal entry. (Today the
//       exported types are all-scalar so there is structurally nothing to alias;
//       these tests PIN that and would catch a future field addition — e.g. a
//       []string Tags — that introduced a shared backing array.)
//
//   (D) CONSERVATION — across arbitrary contention a single bounty is "charged"
//       exactly once: the sum of winning receipt rewards equals the bounty's
//       reward exactly (no double-count, no zero-count), and the three observable
//       views (receipt, Winner, Deployed) never disagree.
//
// VERDICT (proven below): NO always-on bug in either class. auctionplace is a
// first-caller-wins claim lock with a verbatim scalar reward copy; there is no
// price comparator (so NaN/Inf and total-order classes are inapplicable), no
// returned reference type (so aliasing is inapplicable), and the single-writer
// lock makes conservation hold exactly. These are PINS, not fixes.

// ---------------------------------------------------------------------------
// Class (C): RETURN / INPUT ISOLATION — no aliasing of internal entry state.
// ---------------------------------------------------------------------------

// TestAdv2_InputBountyMutationDoesNotAliasBoard proves the Board does not retain
// a reference to caller-mutable backing storage from the posted Bounty. After
// Post, the caller scribbles all over its OWN Bounty value; the board's recorded
// reward (seen via the winning receipt and via a fresh post-claim view) must be
// the value AT POST TIME, unaffected by the later mutation.
//
// SINK-SIDE: the cleared receipt reward and the immutability across a post-Post
// mutation. ANTI-BLUFF: if Post stored a shared reference whose reward could be
// changed through the caller's copy, the receipt would reflect the mutated value
// and this fails. (With today's value-typed Bounty this is guaranteed; the test
// locks it so a future reference-typed field cannot silently alias.)
func TestAdv2_InputBountyMutationDoesNotAliasBoard(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()
	const id = "alias-input"

	posted := Bounty{ID: id, Reward: 500}
	if err := b.Post(posted); err != nil {
		t.Fatalf("[%s] Post: %v", uuid, err)
	}

	// Caller mutates its own copy AFTER posting. Must not touch the board.
	posted.Reward = -999999
	posted.ID = "hijacked"

	c, err := b.Claim(id, "winner")
	if err != nil {
		t.Fatalf("[%s] Claim: %v", uuid, err)
	}
	if c.Reward != 500 {
		t.Fatalf("[%s] receipt reward=%d want 500 (board aliased caller's mutated Bounty)", uuid, c.Reward)
	}
	if c.BountyID != id {
		t.Fatalf("[%s] receipt bountyID=%q want %q (id aliased)", uuid, c.BountyID, id)
	}
	// The mutated id must NOT have leaked a second entry into the board.
	if _, ok := b.Winner("hijacked"); ok {
		t.Fatalf("[%s] mutated id leaked an entry into the board", uuid)
	}
	w, ok := b.Winner(id)
	if !ok || w != "winner" {
		t.Fatalf("[%s] Winner=%q ok=%v want winner", uuid, w, ok)
	}
	t.Logf("[%s] post-Post mutation of caller Bounty did not alias board state", uuid)
}

// TestAdv2_ReturnedClaimMutationDoesNotAliasBoard proves the receipt returned by
// Claim does not share backing storage with the board: the caller mutates EVERY
// field of the returned Claim, then an independent observer (Winner) and a second
// (losing) claimant must still see the original, board-authoritative values.
//
// SINK-SIDE: Board.Winner and a fresh re-claim attempt are unaffected by the
// caller scribbling on its receipt. ANTI-BLUFF: if Claim returned a pointer into
// (or a struct sharing a slice/map with) e, the mutation would corrupt the board
// and Winner would report the scribbled node id.
func TestAdv2_ReturnedClaimMutationDoesNotAliasBoard(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()
	const id = "alias-receipt"
	if err := b.Post(Bounty{ID: id, Reward: 77}); err != nil {
		t.Fatalf("[%s] Post: %v", uuid, err)
	}

	c, err := b.Claim(id, "node-A")
	if err != nil {
		t.Fatalf("[%s] Claim: %v", uuid, err)
	}

	// Scribble the entire receipt.
	c.NodeID = "ATTACKER"
	c.BountyID = "other"
	c.Reward = 0

	// Board must be unmoved: winner still node-A, still deployed.
	w, ok := b.Winner(id)
	if !ok || w != "node-A" {
		t.Fatalf("[%s] Winner=%q ok=%v want node-A (receipt aliased internal winner)", uuid, w, ok)
	}
	if !b.Deployed(id) {
		t.Fatalf("[%s] not deployed after mutating receipt (receipt aliased internal state)", uuid)
	}
	// A second claim still loses with the typed error (claimed flag intact).
	if _, err := b.Claim(id, "node-B"); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("[%s] second Claim err=%v want ErrAlreadyClaimed (receipt mutation cleared claimed?)", uuid, err)
	}
	t.Logf("[%s] mutating the returned Claim did not alias/corrupt board state", uuid)
}

// TestAdv2_TwoBountiesNoSharedBacking posts two bounties and proves their state
// is fully independent — claiming/mutating one never bleeds into the other. This
// is the aliasing analogue at the entry-map level: a single shared/last-written
// entry pointer would make these collide.
//
// SINK-SIDE: each bounty has its own winner/reward/deploy; the unclaimed one is
// untouched by the claimed one.
func TestAdv2_TwoBountiesNoSharedBacking(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()
	if err := b.Post(Bounty{ID: "one", Reward: 10}); err != nil {
		t.Fatalf("[%s] Post one: %v", uuid, err)
	}
	if err := b.Post(Bounty{ID: "two", Reward: 20}); err != nil {
		t.Fatalf("[%s] Post two: %v", uuid, err)
	}

	c1, err := b.Claim("one", "winner-one")
	if err != nil {
		t.Fatalf("[%s] Claim one: %v", uuid, err)
	}
	if c1.Reward != 10 {
		t.Fatalf("[%s] one reward=%d want 10", uuid, c1.Reward)
	}

	// "two" must be completely untouched by claiming "one".
	if b.Deployed("two") {
		t.Fatalf("[%s] bounty two deployed by claiming one (shared backing)", uuid)
	}
	if _, ok := b.Winner("two"); ok {
		t.Fatalf("[%s] bounty two has a winner after only one was claimed (shared backing)", uuid)
	}
	c2, err := b.Claim("two", "winner-two")
	if err != nil {
		t.Fatalf("[%s] Claim two: %v", uuid, err)
	}
	if c2.Reward != 20 {
		t.Fatalf("[%s] two reward=%d want 20 (price read from shared/last entry)", uuid, c2.Reward)
	}
	// And one is still its own winner.
	if w, ok := b.Winner("one"); !ok || w != "winner-one" {
		t.Fatalf("[%s] one winner=%q ok=%v want winner-one (clobbered by two)", uuid, w, ok)
	}
	t.Logf("[%s] two bounties are independent; no shared entry backing", uuid)
}

// ---------------------------------------------------------------------------
// Class (D): CONSERVATION — a bounty is charged EXACTLY ONCE; views never disagree.
// ---------------------------------------------------------------------------

// TestAdv2_ConservationSingleChargeUnderStorm is the conservation proof: under a
// heavy concurrent storm on ONE bounty, the auction "charges" the reward exactly
// once. We sum the reward of every winning receipt; it MUST equal exactly the
// posted reward (one charge), never 0 (lost charge) and never 2x (double-charge).
// We also assert the deployed-once and stable-winner properties from the same run.
//
// This is the analogue of "winner charged == bid; no double-spend; a bid is not
// counted twice" for a claim-lock auction: the single allocatable resource (the
// deploy + its reward) is conserved.
//
// SINK-SIDE: summed winning reward, success count, board winner/deploy agreement.
// ANTI-BLUFF: removing the claimed test-and-set lets >1 goroutine win, doubling
// the summed reward (conservation broken) and tripping successCount!=1.
func TestAdv2_ConservationSingleChargeUnderStorm(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()
	const id = "conserve-1"
	const reward int64 = 1_000_003 // distinctive prime so a double-count is unmistakable
	if err := b.Post(Bounty{ID: id, Reward: reward}); err != nil {
		t.Fatalf("[%s] Post: %v", uuid, err)
	}

	const goroutines = 256
	var (
		successCount atomic.Int64
		chargedSum   atomic.Int64 // sum of winning receipt rewards
		nonTyped     atomic.Int64
		wg           sync.WaitGroup
		start        = make(chan struct{})
		mu           sync.Mutex
		winningNode  string
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			node := fmt.Sprintf("node-%03d", idx)
			<-start
			c, err := b.Claim(id, node)
			switch {
			case err == nil:
				successCount.Add(1)
				chargedSum.Add(c.Reward)
				mu.Lock()
				winningNode = c.NodeID
				mu.Unlock()
			case errors.Is(err, ErrAlreadyClaimed):
			default:
				nonTyped.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if n := nonTyped.Load(); n != 0 {
		t.Fatalf("[%s] %d non-typed errors", uuid, n)
	}
	if got := successCount.Load(); got != 1 {
		t.Fatalf("[%s] CONSERVATION: successes=%d want exactly 1", uuid, got)
	}
	// The decisive conservation assertion: total charged == exactly one reward.
	if got := chargedSum.Load(); got != reward {
		t.Fatalf("[%s] CONSERVATION VIOLATED: total charged=%d want exactly %d (double/under-charge)",
			uuid, got, reward)
	}
	// Tri-view agreement from the same run.
	bw, ok := b.Winner(id)
	if !ok || bw != winningNode {
		t.Fatalf("[%s] Winner=%q ok=%v != winning receipt node %q", uuid, bw, ok, winningNode)
	}
	if !b.Deployed(id) {
		t.Fatalf("[%s] not deployed after the single winning charge", uuid)
	}
	t.Logf("[%s] conservation holds: %d-way storm charged the reward exactly once (sum=%d)", uuid, goroutines, chargedSum.Load())
}

// TestAdv2_ViewsNeverDisagree pins the cross-view invariant across the bounty
// lifecycle: at NO observable point may (Winner ok) and Deployed disagree, and
// whenever there is a winner the receipt's reward equals the posted reward. Claim
// sets winner+deployed+claimed in one critical section, so the only legal
// transitions are (no winner, not deployed) -> (winner W, deployed). A bug that
// set deployed without recording the winner (or vice versa) is caught here.
//
// SINK-SIDE: before/after snapshots of both views plus the receipt reward.
func TestAdv2_ViewsNeverDisagree(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()
	const id = "views"
	const reward int64 = 314159
	if err := b.Post(Bounty{ID: id, Reward: reward}); err != nil {
		t.Fatalf("[%s] Post: %v", uuid, err)
	}

	// BEFORE any claim: not deployed AND no winner (both "empty").
	_, winOK0 := b.Winner(id)
	dep0 := b.Deployed(id)
	if winOK0 || dep0 {
		t.Fatalf("[%s] BEFORE: winnerOK=%v deployed=%v want both false", uuid, winOK0, dep0)
	}

	c, err := b.Claim(id, "the-winner")
	if err != nil {
		t.Fatalf("[%s] Claim: %v", uuid, err)
	}
	if c.Reward != reward {
		t.Fatalf("[%s] receipt reward=%d want %d", uuid, c.Reward, reward)
	}

	// AFTER: deployed AND winner present, and they name the same record.
	w, winOK1 := b.Winner(id)
	dep1 := b.Deployed(id)
	if winOK1 != dep1 {
		t.Fatalf("[%s] AFTER: winnerOK=%v deployed=%v MUST agree (deployed-without-winner or winner-without-deploy)",
			uuid, winOK1, dep1)
	}
	if !winOK1 {
		t.Fatalf("[%s] AFTER: expected a winner and deploy", uuid)
	}
	if w != "the-winner" {
		t.Fatalf("[%s] AFTER: winner=%q want the-winner", uuid, w)
	}
	t.Logf("[%s] Winner-ok and Deployed agree across the lifecycle; receipt reward consistent", uuid)
}
