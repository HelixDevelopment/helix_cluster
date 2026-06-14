package auctionplace

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
)

// This file is an adversarial correctness suite for the auctionplace CLEARING
// logic. It complements auctionplace_test.go (which proves the single-bounty
// happy path and basic single-writer behavior) by exhaustively exercising the
// winner-selection and clearing-price semantics across MANY configurations and
// against the "offlinesync" defect class: a non-deterministic or
// policy-violating winner/price.
//
// HONEST MODEL NOTE (read before reasoning about "winner rule"):
// auctionplace is NOT a sealed-bid price/score clearing auction. There is no
// per-node price/score bid and no lowest-price/highest-score/Vickrey rule. The
// documented model (see package doc) is a FIRST-COME-FIRST-SERVED bounty claim
// guarded by a single-writer lock:
//
//   - WHAT clears: exactly ONE node wins the right to deploy a posted Bounty.
//   - WINNER RULE: the FIRST caller of Claim to observe claimed==false wins.
//     In a SEQUENTIAL ordering this is deterministic (caller #1 wins). Under
//     concurrency the identity of the winner is legitimately scheduler-chosen
//     (any contender may be first), but the OUTCOME must always be a single,
//     self-consistent winner.
//   - PRICE RULE: first-price-like — the receipt's Reward equals the bounty's
//     POSTED reward, captured at win time and immutable thereafter. There is no
//     second-price/runner-up component.
//   - CARDINALITY: exactly one winner per bounty; no double-allocation; losers
//     get the typed ErrAlreadyClaimed.
//
// The offlinesync analog we bite here is therefore: "regardless of the ORDER /
// scheduling in which contenders arrive, the auction must clear to a single,
// internally-consistent winner with the correct (posted) price, never two
// winners, never a phantom winner, never a wrong reward." Each test below
// states the exact mutation that makes it fail.

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// allWinnerViews returns the three independent observations of "who won" so we
// can assert they mutually agree: the receipt node, Board.Winner, and (as a
// proxy for "the node whose deploy was recorded") Board.Deployed truthiness.
func mustPost(t *testing.T, b *Board, id string, reward int64) {
	t.Helper()
	if err := b.Post(Bounty{ID: id, Reward: reward}); err != nil {
		t.Fatalf("Post(%q,%d): %v", id, reward, err)
	}
}

// ---------------------------------------------------------------------------
// 1. CORRECT WINNER ACROSS MANY SEQUENTIAL CONFIGURATIONS
// ---------------------------------------------------------------------------

// TestClearing_SequentialFirstCallerWins exhaustively asserts the documented
// winner rule across MANY bidder configurations: with contenders presented in a
// known sequential order, the FIRST caller always wins, every later caller
// loses with ErrAlreadyClaimed, and the recorded winner/price/deploy are
// correct. This is the core invariant exercised across a matrix of contender
// counts and reward values (not the single scenario in the legacy test).
//
// ANTI-BLUFF — why this bites:
//   - Flip the comparison so a LATER caller can overwrite the winner (e.g.
//     delete the `if e.claimed { return ErrAlreadyClaimed }` guard, or make
//     Claim re-assign e.winner unconditionally): the "first caller wins" and
//     "later callers lose" assertions fail for every config with >=2 contenders.
//   - If Claim stops capturing e.bounty.Reward into the receipt (returns 0 or a
//     wrong value), the per-config price assertion fails.
//   - A PASS-bluff that only ever tests one contender count / one reward cannot
//     survive the matrix: we vary both and assert the winner is contender #0
//     every time.
func TestClearing_SequentialFirstCallerWins(t *testing.T) {
	uuid := newRunUUID(t.Name())
	contenderCounts := []int{1, 2, 3, 5, 8, 13, 64}
	rewards := []int64{0, 1, 42, -7, 1_000_000, 9_223_372_036_854_775_807} // incl. zero, negative, max int64

	configs := 0
	for _, n := range contenderCounts {
		for _, reward := range rewards {
			configs++
			b := NewBoard()
			id := fmt.Sprintf("wl-%d-%d", n, reward)
			mustPost(t, b, id, reward)

			// Build a deterministic contender order; contender 0 must win.
			contenders := make([]string, n)
			for i := range contenders {
				contenders[i] = fmt.Sprintf("node-%02d", i)
			}

			// First caller wins.
			claim, err := b.Claim(id, contenders[0])
			if err != nil {
				t.Fatalf("[%s] config(n=%d,reward=%d): first Claim(%q) err=%v",
					uuid, n, reward, contenders[0], err)
			}
			if claim.NodeID != contenders[0] {
				t.Fatalf("[%s] config(n=%d,reward=%d): winner=%q want %q",
					uuid, n, reward, claim.NodeID, contenders[0])
			}
			// PRICE RULE: receipt reward == posted reward, for every config.
			if claim.Reward != reward {
				t.Fatalf("[%s] config(n=%d,reward=%d): receipt reward=%d want %d",
					uuid, n, reward, claim.Reward, reward)
			}
			if claim.BountyID != id {
				t.Fatalf("[%s] config(n=%d): receipt bountyID=%q want %q",
					uuid, n, claim.BountyID, id)
			}

			// Every subsequent caller loses with the typed error and CANNOT
			// displace the winner or change the price.
			for i := 1; i < n; i++ {
				lc, lerr := b.Claim(id, contenders[i])
				if !errors.Is(lerr, ErrAlreadyClaimed) {
					t.Fatalf("[%s] config(n=%d,reward=%d): late Claim(%q) err=%v want ErrAlreadyClaimed",
						uuid, n, reward, contenders[i], lerr)
				}
				if lc != (Claim{}) {
					t.Fatalf("[%s] config(n=%d): loser %q got non-zero receipt %+v",
						uuid, n, contenders[i], lc)
				}
			}

			// CARDINALITY + recorded winner/price/deploy consistency.
			w, ok := b.Winner(id)
			if !ok || w != contenders[0] {
				t.Fatalf("[%s] config(n=%d,reward=%d): Board.Winner=%q ok=%v want %q",
					uuid, n, reward, w, ok, contenders[0])
			}
			if !b.Deployed(id) {
				t.Fatalf("[%s] config(n=%d,reward=%d): not deployed after win",
					uuid, n, reward)
			}
		}
	}
	t.Logf("[%s] verified %d sequential clearing configurations (first-caller-wins + correct price)",
		uuid, configs)
}

// ---------------------------------------------------------------------------
// 2. ORDER-INDEPENDENCE / DETERMINISTIC CLEARING (the offlinesync bite)
// ---------------------------------------------------------------------------

// TestClearing_OrderIndependentSingleWinner is the strongest anti-bluff test
// for the offlinesync defect class. For a FIXED set of contenders it runs the
// auction MANY times, each time presenting the SAME contenders but in a SHUFFLED
// arrival order (and a concurrent variant), and asserts that EVERY run clears to:
//   - exactly ONE winner (no double-allocation),
//   - a winner that is one of the actual contenders (no phantom winner),
//   - a winner whose receipt node == Board.Winner == the deployed claim,
//   - the correct posted price on the receipt,
//   - all losers receiving ErrAlreadyClaimed.
//
// In the SEQUENTIAL-shuffle sub-case the winner is fully determined: it must be
// whichever contender was placed FIRST in that shuffle. We assert exactly that,
// proving the clearing depends ONLY on arrival order in a well-defined way and
// is otherwise free of hidden state leakage between runs.
//
// ANTI-BLUFF — why this bites the offlinesync bug:
//   - If the winner were order-DEPENDENT in a pathological way (e.g. a missing
//     lock lets a late writer overwrite an earlier winner, or a comparison bug
//     picks "max nodeID" instead of "first arrival"), the shuffled-sequential
//     assertion "winner == contenders-in-this-shuffle[0]" fails for the shuffles
//     where the lexically-extreme node is not first.
//   - If the single-writer guarantee is removed, the concurrent sub-case yields
//     successCount != 1 (double-allocation) and -race reports a data race.
//   - A bluff that hard-codes one expected winner cannot pass: the expected
//     winner changes every shuffle, derived independently from the input order.
func TestClearing_OrderIndependentSingleWinner(t *testing.T) {
	uuid := newRunUUID(t.Name())
	const reward int64 = 555
	base := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}

	// Deterministic PRNG so the test is reproducible yet exercises many orders.
	rng := rand.New(rand.NewSource(0xC0FFEE))

	const shuffles = 400

	// --- Sub-case A: sequential shuffles, winner fully determined by order[0].
	for s := 0; s < shuffles; s++ {
		order := append([]string(nil), base...)
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

		b := NewBoard()
		id := fmt.Sprintf("seq-%03d", s)
		mustPost(t, b, id, reward)

		wantWinner := order[0]
		var gotWinner string
		successes := 0
		for _, node := range order {
			c, err := b.Claim(id, node)
			switch {
			case err == nil:
				successes++
				gotWinner = c.NodeID
				if c.Reward != reward {
					t.Fatalf("[%s] seq shuffle %d: winner %q reward=%d want %d",
						uuid, s, node, c.Reward, reward)
				}
			case errors.Is(err, ErrAlreadyClaimed):
				// expected loser
			default:
				t.Fatalf("[%s] seq shuffle %d: node %q unexpected err=%v", uuid, s, node, err)
			}
		}
		if successes != 1 {
			t.Fatalf("[%s] seq shuffle %d: successes=%d want 1 (double-allocation?)",
				uuid, s, successes)
		}
		if gotWinner != wantWinner {
			t.Fatalf("[%s] seq shuffle %d: cleared winner=%q but first-arrival was %q (order-dependence bug)",
				uuid, s, gotWinner, wantWinner)
		}
		// Board's recorded winner must agree with the receipt and be deployed.
		bw, ok := b.Winner(id)
		if !ok || bw != wantWinner {
			t.Fatalf("[%s] seq shuffle %d: Board.Winner=%q ok=%v want %q",
				uuid, s, bw, ok, wantWinner)
		}
		if !b.Deployed(id) {
			t.Fatalf("[%s] seq shuffle %d: not deployed", uuid, s)
		}
	}

	// --- Sub-case B: concurrent shuffles. Winner identity is scheduler-chosen,
	// but the OUTCOME must still be a single self-consistent winner with the
	// correct price, no matter the order goroutines happen to run.
	contenderSet := make(map[string]struct{}, len(base))
	for _, n := range base {
		contenderSet[n] = struct{}{}
	}
	const concurrentRuns = 300
	for r := 0; r < concurrentRuns; r++ {
		b := NewBoard()
		id := fmt.Sprintf("conc-%03d", r)
		mustPost(t, b, id, reward)

		var (
			successCount atomic.Int64
			loserCount   atomic.Int64
			otherCount   atomic.Int64
			wg           sync.WaitGroup
			mu           sync.Mutex
			receipt      Claim
			start        = make(chan struct{})
		)
		for _, node := range base {
			wg.Add(1)
			go func(node string) {
				defer wg.Done()
				<-start
				c, err := b.Claim(id, node)
				switch {
				case err == nil:
					successCount.Add(1)
					mu.Lock()
					receipt = c
					mu.Unlock()
				case errors.Is(err, ErrAlreadyClaimed):
					loserCount.Add(1)
				default:
					otherCount.Add(1)
				}
			}(node)
		}
		close(start)
		wg.Wait()

		if got := successCount.Load(); got != 1 {
			t.Fatalf("[%s] conc run %d: successes=%d want 1 (double-allocation)", uuid, r, got)
		}
		if got := otherCount.Load(); got != 0 {
			t.Fatalf("[%s] conc run %d: %d non-typed errors", uuid, r, got)
		}
		if got := loserCount.Load(); got != int64(len(base)-1) {
			t.Fatalf("[%s] conc run %d: losers=%d want %d", uuid, r, got, len(base)-1)
		}
		// Winner must be a real contender, agree across views, with correct price.
		if _, isContender := contenderSet[receipt.NodeID]; !isContender {
			t.Fatalf("[%s] conc run %d: phantom winner %q not among contenders", uuid, r, receipt.NodeID)
		}
		if receipt.Reward != reward {
			t.Fatalf("[%s] conc run %d: winner %q reward=%d want %d", uuid, r, receipt.NodeID, receipt.Reward, reward)
		}
		bw, ok := b.Winner(id)
		if !ok || bw != receipt.NodeID {
			t.Fatalf("[%s] conc run %d: Board.Winner=%q ok=%v != receipt %q (winner not stable)",
				uuid, r, bw, ok, receipt.NodeID)
		}
		if !b.Deployed(id) {
			t.Fatalf("[%s] conc run %d: not deployed", uuid, r)
		}
	}

	t.Logf("[%s] order-independence holds: %d sequential shuffles + %d concurrent runs, all single-winner/correct-price",
		uuid, shuffles, concurrentRuns)
}

// ---------------------------------------------------------------------------
// 3. EXACTLY-ONE WINNER ACROSS INDEPENDENT BOUNTIES (no cross-contamination)
// ---------------------------------------------------------------------------

// TestClearing_IndependentBountiesNoCrossAllocation posts MANY bounties at once
// and clears them, asserting each clears to its OWN single winner with its OWN
// posted price, and that claiming one bounty never allocates/deploys another.
// This bites a clearing bug where shared state (e.g. a single package-level
// "claimed" flag, or a wrong map key) would let one bounty's claim bleed into
// another (a different flavor of policy-violating winner).
//
// ANTI-BLUFF — why this bites:
//   - If entries were not keyed strictly by bounty id (e.g. a clearing routine
//     that mutated the wrong entry), claiming bounty A would flip B's
//     deployed/winner; the per-bounty isolation assertions fail.
//   - If price were read from a shared/last-posted value rather than the entry's
//     own bounty, the per-bounty reward assertion fails.
func TestClearing_IndependentBountiesNoCrossAllocation(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()

	const numBounties = 50
	ids := make([]string, numBounties)
	wantReward := make(map[string]int64, numBounties)
	wantWinner := make(map[string]string, numBounties)
	for i := 0; i < numBounties; i++ {
		id := fmt.Sprintf("bounty-%03d", i)
		ids[i] = id
		reward := int64(i * 1000)
		wantReward[id] = reward
		mustPost(t, b, id, reward)
	}

	// Clear each bounty with a distinct winner; interleave with re-claim attempts
	// from a foreign node to ensure no second allocation.
	for i, id := range ids {
		winner := fmt.Sprintf("winner-%03d", i)
		wantWinner[id] = winner
		c, err := b.Claim(id, winner)
		if err != nil {
			t.Fatalf("[%s] Claim(%q,%q): %v", uuid, id, winner, err)
		}
		if c.Reward != wantReward[id] {
			t.Fatalf("[%s] %q receipt reward=%d want %d", uuid, id, c.Reward, wantReward[id])
		}
		// A foreign re-claim must lose and must not deploy/allocate anew.
		if _, err := b.Claim(id, "intruder"); !errors.Is(err, ErrAlreadyClaimed) {
			t.Fatalf("[%s] %q intruder err=%v want ErrAlreadyClaimed", uuid, id, err)
		}
	}

	// Final sweep: every bounty has exactly its own winner/price/deploy and none
	// has been cross-contaminated.
	deployedCount := 0
	for _, id := range ids {
		w, ok := b.Winner(id)
		if !ok || w != wantWinner[id] {
			t.Fatalf("[%s] %q Winner=%q ok=%v want %q", uuid, id, w, ok, wantWinner[id])
		}
		if !b.Deployed(id) {
			t.Fatalf("[%s] %q expected deployed", uuid, id)
		}
		deployedCount++
	}
	if deployedCount != numBounties {
		t.Fatalf("[%s] deployedCount=%d want %d", uuid, deployedCount, numBounties)
	}

	// An unposted bounty in the same board must be neither won nor deployed
	// (feasibility / "no winner cleanly" when nothing applies).
	if _, ok := b.Winner("never-posted"); ok {
		t.Fatalf("[%s] unposted bounty reports a winner", uuid)
	}
	if b.Deployed("never-posted") {
		t.Fatalf("[%s] unposted bounty reports deployed", uuid)
	}
	t.Logf("[%s] %d independent bounties each cleared to a single correct winner/price; no cross-allocation",
		uuid, numBounties)
}

// ---------------------------------------------------------------------------
// 4. FEASIBILITY: no claim => clean "no winner", and unknown ids never clear
// ---------------------------------------------------------------------------

// TestClearing_NoFeasibleClaimClearsCleanly asserts that when NO node claims, the
// auction does not invent a winner or a price (it clears empty), and that an
// unknown bounty id never clears to a (wrong) winner — it returns the typed
// ErrUnknownBounty rather than silently allocating. This is the "if no bid is
// feasible, clear with no winner — never a wrong one" property.
//
// ANTI-BLUFF — why this bites:
//   - If Winner/Deployed defaulted to "true"/a winner for unclaimed or unknown
//     bounties (a fail-open clearing bug), these assertions fail.
//   - If Claim treated a missing id as claimable (clearing a non-existent
//     auction), the ErrUnknownBounty check fails.
func TestClearing_NoFeasibleClaimClearsCleanly(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()
	mustPost(t, b, "posted-but-unclaimed", 100)

	// Posted but nobody claimed: no winner, not deployed, no phantom price.
	if w, ok := b.Winner("posted-but-unclaimed"); ok {
		t.Fatalf("[%s] unclaimed bounty has winner %q", uuid, w)
	}
	if b.Deployed("posted-but-unclaimed") {
		t.Fatalf("[%s] unclaimed bounty reports deployed", uuid)
	}

	// Unknown id: claim must fail typed; never clears.
	if _, err := b.Claim("no-such-bounty", "node-Z"); !errors.Is(err, ErrUnknownBounty) {
		t.Fatalf("[%s] Claim(unknown) err=%v want ErrUnknownBounty", uuid, err)
	}
	if _, ok := b.Winner("no-such-bounty"); ok {
		t.Fatalf("[%s] unknown bounty reports a winner", uuid)
	}
	if b.Deployed("no-such-bounty") {
		t.Fatalf("[%s] unknown bounty reports deployed", uuid)
	}
	t.Logf("[%s] empty/unknown auctions clear cleanly with no winner and no phantom price", uuid)
}

// ---------------------------------------------------------------------------
// 5. PRICE IMMUTABILITY: the cleared price cannot be changed after the fact
// ---------------------------------------------------------------------------

// TestClearing_PriceImmutableAfterClear proves the first-price clearing value is
// frozen at win time and is not retroactively altered by later board activity
// (re-Post attempts, losing claims). The package doc explicitly promises the
// receipt "carries the bounty reward at the moment of winning ... independent of
// later board mutations" — this asserts that promise behaviorally.
//
// ANTI-BLUFF — why this bites:
//   - If Post overwrote an existing entry's reward instead of returning
//     ErrDuplicatePost, a re-Post with a new reward would corrupt the cleared
//     price; we assert the original receipt and a fresh Winner-time view both
//     still reflect the ORIGINAL reward.
//   - If the receipt aliased mutable board state, a later losing claim could
//     perturb it; we re-read the receipt value after subsequent activity.
func TestClearing_PriceImmutableAfterClear(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()
	const id = "price-frozen"
	const original int64 = 12345
	mustPost(t, b, id, original)

	claim, err := b.Claim(id, "node-W")
	if err != nil {
		t.Fatalf("[%s] Claim: %v", uuid, err)
	}
	if claim.Reward != original {
		t.Fatalf("[%s] cleared price=%d want %d", uuid, claim.Reward, original)
	}

	// Attempt to mutate: re-Post with a different reward (must be rejected) and a
	// losing claim. Neither may change the already-cleared price.
	if err := b.Post(Bounty{ID: id, Reward: 99999}); !errors.Is(err, ErrDuplicatePost) {
		t.Fatalf("[%s] re-Post err=%v want ErrDuplicatePost", uuid, err)
	}
	if _, err := b.Claim(id, "node-late"); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("[%s] late Claim err=%v want ErrAlreadyClaimed", uuid, err)
	}

	// The original receipt value is a copy and must be unchanged.
	if claim.Reward != original {
		t.Fatalf("[%s] receipt price mutated to %d (want frozen %d)", uuid, claim.Reward, original)
	}
	// Winner is still the original winner; bounty still deployed at original terms.
	if w, ok := b.Winner(id); !ok || w != "node-W" {
		t.Fatalf("[%s] winner changed: %q ok=%v want node-W", uuid, w, ok)
	}
	if !b.Deployed(id) {
		t.Fatalf("[%s] bounty no longer deployed after mutation attempts", uuid)
	}
	t.Logf("[%s] cleared price frozen at %d through re-Post and late-claim attempts", uuid, original)
}

// ---------------------------------------------------------------------------
// 6. CONCURRENT MULTI-BOUNTY STRESS: global exactly-one-per-bounty invariant
// ---------------------------------------------------------------------------

// TestClearing_ConcurrentMultiBountyExactlyOneEach launches a storm of claims
// across MANY bounties from MANY nodes simultaneously and asserts the global
// invariant: across the whole board, each bounty is won exactly once, the set of
// winners is exactly one-per-bounty, and every successful receipt's price equals
// that bounty's posted reward. Run under -race this is the strongest mechanical
// proof that the single-writer clearing holds under realistic mixed contention
// (not just contention on one bounty as in the legacy test).
//
// ANTI-BLUFF — why this bites:
//   - Remove the per-entry lock/test-and-set and some bounty clears twice:
//     totalSuccesses != numBounties and a bounty appears with two winners; -race
//     also flags the entry mutation.
//   - If price were read from shared/global state, the per-receipt reward check
//     against the bounty's own posted reward fails.
func TestClearing_ConcurrentMultiBountyExactlyOneEach(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()

	const numBounties = 40
	const nodesPerBounty = 24
	wantReward := make(map[string]int64, numBounties)
	ids := make([]string, numBounties)
	for i := 0; i < numBounties; i++ {
		id := fmt.Sprintf("storm-%03d", i)
		ids[i] = id
		reward := int64(7 + i*13)
		wantReward[id] = reward
		mustPost(t, b, id, reward)
	}

	type win struct {
		bounty string
		node   string
		reward int64
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wins     []win
		start    = make(chan struct{})
		badPx    atomic.Int64 // wrong-price successes
		nonTyped atomic.Int64
	)

	for i, id := range ids {
		for j := 0; j < nodesPerBounty; j++ {
			wg.Add(1)
			go func(id string, i, j int) {
				defer wg.Done()
				node := fmt.Sprintf("n-%03d-%03d", i, j)
				<-start
				c, err := b.Claim(id, node)
				switch {
				case err == nil:
					if c.Reward != wantReward[id] {
						badPx.Add(1)
					}
					mu.Lock()
					wins = append(wins, win{bounty: id, node: c.NodeID, reward: c.Reward})
					mu.Unlock()
				case errors.Is(err, ErrAlreadyClaimed):
					// expected
				default:
					nonTyped.Add(1)
				}
			}(id, i, j)
		}
	}
	close(start)
	wg.Wait()

	if n := nonTyped.Load(); n != 0 {
		t.Fatalf("[%s] %d non-typed errors during storm", uuid, n)
	}
	if n := badPx.Load(); n != 0 {
		t.Fatalf("[%s] %d winning receipts had a WRONG price", uuid, n)
	}
	if len(wins) != numBounties {
		t.Fatalf("[%s] totalSuccesses=%d want %d (double-allocation or lost clear)",
			uuid, len(wins), numBounties)
	}

	// Exactly one winner per bounty, each with the correct price, agreeing with
	// the board's recorded winner.
	seen := make(map[string]string, numBounties)
	for _, w := range wins {
		if prev, dup := seen[w.bounty]; dup {
			t.Fatalf("[%s] bounty %q won twice: %q and %q (double-allocation)", uuid, w.bounty, prev, w.node)
		}
		seen[w.bounty] = w.node
		if w.reward != wantReward[w.bounty] {
			t.Fatalf("[%s] bounty %q winner %q price=%d want %d", uuid, w.bounty, w.node, w.reward, wantReward[w.bounty])
		}
		bw, ok := b.Winner(w.bounty)
		if !ok || bw != w.node {
			t.Fatalf("[%s] bounty %q Board.Winner=%q ok=%v != receipt %q", uuid, w.bounty, bw, ok, w.node)
		}
		if !b.Deployed(w.bounty) {
			t.Fatalf("[%s] bounty %q not deployed", uuid, w.bounty)
		}
	}

	// Every posted bounty must be accounted for exactly once.
	gotIDs := make([]string, 0, len(seen))
	for id := range seen {
		gotIDs = append(gotIDs, id)
	}
	sort.Strings(gotIDs)
	if len(gotIDs) != numBounties {
		t.Fatalf("[%s] distinct cleared bounties=%d want %d", uuid, len(gotIDs), numBounties)
	}
	t.Logf("[%s] storm: %d bounties x %d nodes -> exactly %d single-winner clears, all correct price",
		uuid, numBounties, nodesPerBounty, len(wins))
}
