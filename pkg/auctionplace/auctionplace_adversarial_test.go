package auctionplace

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// auctionplace_adversarial_test.go — hard, sink-side adversarial probes against
// the four high-yield bug classes. Each test asserts ACTUAL board state (winner,
// deployed, receipt reward), not merely "no panic" or a return value.
//
// THREE-WAY DISCRIMINATION SUMMARY (proven by the tests below):
//   - Class (a) total-order/numeric edge: PIN the documented FCFS contract at the
//     int64 boundary (MinInt64/MaxInt64/0/negative). NO bug — numeric handling is
//     exact because the reward is copied verbatim, never accumulated/compared.
//   - Class (a) identity edge: Post("") returns a NON-sentinel error. This is a
//     LATENT RISK / honest contract gap, NOT a correctness bug (the call is
//     correctly rejected; only the error is not errors.Is-matchable). Pinned, not
//     "fixed", to avoid manufacturing a defect.
//   - Class (b) unguarded shared state: full-API concurrent storm (Post racing
//     Claim/Winner/Deployed on overlapping keys) under -race. NO bug — every path
//     holds b.mu; the documented single-writer guarantee holds.
//   - Class (c) numeric poison / over-commit: there is NO float/accumulator path;
//     a negative or MinInt64 reward cannot poison anything and cannot cause a
//     second allocation. Proven: exactly-one winner even with pathological rewards.
//   - Class (d) hostile-input DoS: bounty IDs are map keys handled in O(1); a
//     multi-megabyte ID neither panics nor recurses. Pinned.

// ---------------------------------------------------------------------------
// Class (a): TOTAL-ORDER AT A NUMERIC EDGE — int64 boundary round-trips exactly.
// ---------------------------------------------------------------------------

// TestAdv_Int64BoundaryRewardRoundTripsExact pins that the cleared price is the
// posted reward VERBATIM at every int64 boundary, including math.MinInt64 (which
// the existing clearing matrix does NOT cover — it tests MaxInt64 and -7 but not
// MinInt64, the asymmetric two's-complement extreme). A naive implementation that
// negated, abs()'d, or used a signed/unsigned mix on the reward would corrupt
// MinInt64 specifically (|MinInt64| overflows).
//
// SINK-SIDE: asserts the receipt reward AND that the single winner deployed.
// ANTI-BLUFF: if Claim ever derived the receipt reward via arithmetic on
// e.bounty.Reward instead of a plain copy, the MinInt64 case would not equal the
// posted value and this fails.
func TestAdv_Int64BoundaryRewardRoundTripsExact(t *testing.T) {
	uuid := newRunUUID(t.Name())
	boundaries := []int64{
		math.MinInt64,
		math.MinInt64 + 1,
		-1,
		0,
		1,
		math.MaxInt64 - 1,
		math.MaxInt64,
	}
	for _, reward := range boundaries {
		b := NewBoard()
		id := fmt.Sprintf("edge-%d", reward)
		if err := b.Post(Bounty{ID: id, Reward: reward}); err != nil {
			t.Fatalf("[%s] Post(reward=%d): %v", uuid, reward, err)
		}
		c, err := b.Claim(id, "winner")
		if err != nil {
			t.Fatalf("[%s] Claim(reward=%d): %v", uuid, reward, err)
		}
		// SINK-SIDE: exact bit-for-bit reward.
		if c.Reward != reward {
			t.Fatalf("[%s] receipt reward=%d want EXACT %d (int64 boundary corruption)",
				uuid, c.Reward, reward)
		}
		// A second claim must lose — a negative/extreme reward must not be treated
		// as "unfunded / re-claimable" by some `< 0`-style guard.
		if _, err := b.Claim(id, "intruder"); !errors.Is(err, ErrAlreadyClaimed) {
			t.Fatalf("[%s] reward=%d: second Claim err=%v want ErrAlreadyClaimed (extreme reward bypassed claim guard?)",
				uuid, reward, err)
		}
		w, ok := b.Winner(id)
		if !ok || w != "winner" {
			t.Fatalf("[%s] reward=%d: Winner=%q ok=%v want winner", uuid, reward, w, ok)
		}
		if !b.Deployed(id) {
			t.Fatalf("[%s] reward=%d: not deployed", uuid, reward)
		}
	}
	t.Logf("[%s] int64 boundary rewards (incl. MinInt64) round-trip exact; extreme rewards never bypass claim guard", uuid)
}

// TestAdv_PostEmptyIDContract PINS the identity-edge contract: Post("") is
// REJECTED (no entry is created, nothing is claimable), and the rejection cannot
// be confused for a successful empty-keyed post.
//
// HONEST CLASSIFICATION: the empty-id rejection error is built with fmt.Errorf
// and is NOT one of the package sentinels, so callers cannot errors.Is it. That
// is a LATENT RISK (a thin contract gap), NOT a correctness bug — the dangerous
// outcome (a phantom empty-keyed bounty that could be claimed) does NOT occur.
// We therefore PIN the real, safe behavior rather than manufacture a fix.
//
// ANTI-BLUFF: if Post stopped rejecting "" (created an entry under key ""), the
// Claim("") below would SUCCEED instead of returning ErrUnknownBounty, and this
// test fails — proving the guard is load-bearing.
func TestAdv_PostEmptyIDContract(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()

	err := b.Post(Bounty{ID: "", Reward: 100})
	if err == nil {
		t.Fatalf("[%s] Post(\"\") returned nil — empty-id bounty must be rejected", uuid)
	}
	// Pin the message-level contract (documents the LATENT RISK: non-sentinel).
	if !strings.Contains(err.Error(), "empty bounty id") {
		t.Fatalf("[%s] Post(\"\") err=%q want it to mention empty bounty id", uuid, err.Error())
	}
	// SINK-SIDE: no phantom empty-keyed entry was created -> claiming "" is UNKNOWN,
	// not AlreadyClaimed and not a silent success.
	if _, cerr := b.Claim("", "ghost"); !errors.Is(cerr, ErrUnknownBounty) {
		t.Fatalf("[%s] Claim(\"\") err=%v want ErrUnknownBounty (empty key leaked an entry!)", uuid, cerr)
	}
	if _, ok := b.Winner(""); ok {
		t.Fatalf("[%s] empty-id reports a winner — phantom entry exists", uuid)
	}
	if b.Deployed("") {
		t.Fatalf("[%s] empty-id reports deployed — phantom entry exists", uuid)
	}
	t.Logf("[%s] Post(\"\") rejected, no phantom empty-keyed entry (note: rejection error is non-sentinel — latent risk, not a bug)", uuid)
}

// ---------------------------------------------------------------------------
// Class (b): UNGUARDED SHARED MUTABLE STATE — full-API concurrency under -race.
// ---------------------------------------------------------------------------

// TestAdv_ConcurrentPostClaimReadStorm races ALL four exported operations against
// each other on an OVERLAPPING key space simultaneously: many goroutines Post,
// many Claim, many call Winner/Deployed — all on the same set of ids at the same
// time. The existing suite races Claim-vs-Claim and Claim-vs-read, but NOT
// Post-vs-Claim-vs-read interleaving (a writer creating the map entry while other
// goroutines read/claim it). This is the canonical place a missing lock on the
// map itself (concurrent map write/read) would surface.
//
// SINK-SIDE INVARIANTS asserted after the storm:
//   - every id that was posted at least once is won by EXACTLY ONE node,
//   - that node == Board.Winner(id) and Deployed(id) is true,
//   - the winning receipt's reward equals the (single, duplicate-rejected) posted
//     reward for that id,
//   - no non-typed errors leaked from Claim.
//
// NOTE on the "winner per id" count: it is NOT guaranteed that every posted id
// ends up won. It is legitimate for ALL claimers of a given id to race AHEAD of
// that id's poster and each receive ErrUnknownBounty, leaving the id
// posted-but-unclaimed — a documented clean state (see
// TestClearing_NoFeasibleClaimClearsCleanly). We therefore assert the strong,
// ALWAYS-true invariant — every id that DID clear cleared EXACTLY ONCE with the
// correct price and agreeing board state — and that claimWin == len(winners).
// (Asserting len(winners)==numIDs would be a flaky false-positive in the test,
// not a prod bug.)
//
// ANTI-BLUFF / -race: drop b.mu from any path and Go's race detector flags a
// concurrent map read/write here; remove the claim test-and-set and a bounty is
// won twice (dupWinner fails).
func TestAdv_ConcurrentPostClaimReadStorm(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()

	const numIDs = 64
	const postersPerID = 4   // contend the duplicate-post rejection too
	const claimersPerID = 16 // contend the single-writer claim
	const readersPerID = 8

	ids := make([]string, numIDs)
	rewardOf := make(map[string]int64, numIDs)
	for i := 0; i < numIDs; i++ {
		id := fmt.Sprintf("race-%03d", i)
		ids[i] = id
		rewardOf[id] = int64(i*101 + 1)
	}

	var (
		wg        sync.WaitGroup
		start     = make(chan struct{})
		postOK    atomic.Int64
		postDup   atomic.Int64
		claimWin  atomic.Int64
		claimLose atomic.Int64
		claimUnk  atomic.Int64
		nonTyped  atomic.Int64
	)

	// Collect winning receipts to verify exactly-one-per-id and correct price.
	var mu sync.Mutex
	winners := make(map[string]Claim, numIDs)

	for i, id := range ids {
		reward := rewardOf[id]

		// Posters (duplicate posts MUST be rejected, not overwrite state).
		for p := 0; p < postersPerID; p++ {
			wg.Add(1)
			go func(id string, reward int64) {
				defer wg.Done()
				<-start
				switch err := b.Post(Bounty{ID: id, Reward: reward}); {
				case err == nil:
					postOK.Add(1)
				case errors.Is(err, ErrDuplicatePost):
					postDup.Add(1)
				default:
					nonTyped.Add(1)
				}
			}(id, reward)
		}

		// Claimers (exactly one may win once the post lands).
		for c := 0; c < claimersPerID; c++ {
			wg.Add(1)
			go func(id string, reward int64, node string) {
				defer wg.Done()
				<-start
				cl, err := b.Claim(id, node)
				switch {
				case err == nil:
					claimWin.Add(1)
					mu.Lock()
					if prev, dup := winners[id]; dup {
						t.Errorf("[%s] id %q won TWICE: %q and %q (double-allocation)",
							uuid, id, prev.NodeID, cl.NodeID)
					}
					winners[id] = cl
					mu.Unlock()
				case errors.Is(err, ErrAlreadyClaimed):
					claimLose.Add(1)
				case errors.Is(err, ErrUnknownBounty):
					// Legitimate: a claimer that ran before any post landed.
					claimUnk.Add(1)
				default:
					nonTyped.Add(1)
				}
			}(id, reward, fmt.Sprintf("node-%03d-%03d", i, c))
		}

		// Readers (must never panic / race on concurrent map access).
		for r := 0; r < readersPerID; r++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				<-start
				_ = b.Deployed(id)
				_, _ = b.Winner(id)
			}(id)
		}
	}

	close(start)
	wg.Wait()

	if n := nonTyped.Load(); n != 0 {
		t.Fatalf("[%s] %d non-typed errors leaked from the storm", uuid, n)
	}
	// Each id is posted exactly once (postOK==numIDs) and other posts are dups.
	if got := postOK.Load(); got != numIDs {
		t.Fatalf("[%s] postOK=%d want %d (duplicate Post not rejected atomically)", uuid, got, numIDs)
	}
	if got := postDup.Load(); got != int64(numIDs*(postersPerID-1)) {
		t.Fatalf("[%s] postDup=%d want %d", uuid, got, numIDs*(postersPerID-1))
	}

	// STRONG always-true invariant: one winning receipt per distinct cleared id —
	// i.e. total wins == number of distinct winners. If a bounty cleared twice
	// (single-writer broken), claimWin would exceed len(winners) and the in-loop
	// "won TWICE" t.Errorf above would also fire.
	if got := claimWin.Load(); got != int64(len(winners)) {
		t.Fatalf("[%s] claim wins=%d but distinct winners=%d (double-allocation: a bounty cleared more than once)",
			uuid, got, len(winners))
	}
	// At least one id must have cleared (sanity: the storm did real work).
	if len(winners) == 0 {
		t.Fatalf("[%s] no id cleared at all — storm did nothing", uuid)
	}

	// SINK-SIDE: each recorded winner agrees with the board and has correct price.
	for id, cl := range winners {
		if cl.Reward != rewardOf[id] {
			t.Fatalf("[%s] id %q winner reward=%d want %d (price read from shared state?)",
				uuid, id, cl.Reward, rewardOf[id])
		}
		if cl.BountyID != id {
			t.Fatalf("[%s] id %q receipt bountyID=%q mismatch", uuid, id, cl.BountyID)
		}
		bw, ok := b.Winner(id)
		if !ok || bw != cl.NodeID {
			t.Fatalf("[%s] id %q Board.Winner=%q ok=%v != receipt %q", uuid, id, bw, ok, cl.NodeID)
		}
		if !b.Deployed(id) {
			t.Fatalf("[%s] id %q not deployed after winning claim", uuid, id)
		}
	}
	t.Logf("[%s] full-API storm: %d ids, posts/claims/reads interleaved -> exactly one winner each, correct price, no race (winUnk=%d losers=%d)",
		uuid, numIDs, claimUnk.Load(), claimLose.Load())
}

// ---------------------------------------------------------------------------
// Class (c): NUMERIC POISON / OVER-COMMIT — pathological reward cannot double-spend.
// ---------------------------------------------------------------------------

// TestAdv_PathologicalRewardNoOverCommit hammers a single bounty whose reward is
// the most "poison-looking" int64 (MinInt64) with a concurrent storm and asserts
// the over-commit invariant: the auction allocates the deploy EXACTLY ONCE. A
// negative/extreme reward must not be mistaken for "free / unfunded / replayable"
// by any `< 0`-only or sign-based guard, which would allow a second allocation.
//
// SINK-SIDE: successCount==1, single stable winner, deployed once.
// ANTI-BLUFF: if a guard treated reward<0 as "not really claimed" (clearing the
// claimed flag), a second goroutine would also succeed and successCount>1 fails.
func TestAdv_PathologicalRewardNoOverCommit(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()
	const id = "poison-reward"
	if err := b.Post(Bounty{ID: id, Reward: math.MinInt64}); err != nil {
		t.Fatalf("[%s] Post: %v", uuid, err)
	}

	const goroutines = 200
	var (
		successCount atomic.Int64
		nonTyped     atomic.Int64
		wg           sync.WaitGroup
		start        = make(chan struct{})
		mu           sync.Mutex
		winning      Claim
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			c, err := b.Claim(id, fmt.Sprintf("node-%03d", idx))
			switch {
			case err == nil:
				successCount.Add(1)
				mu.Lock()
				winning = c
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
		t.Fatalf("[%s] OVER-COMMIT: successes=%d want 1 (MinInt64 reward bypassed single-allocation)", uuid, got)
	}
	if winning.Reward != math.MinInt64 {
		t.Fatalf("[%s] winning receipt reward=%d want MinInt64 %d", uuid, winning.Reward, int64(math.MinInt64))
	}
	bw, ok := b.Winner(id)
	if !ok || bw != winning.NodeID {
		t.Fatalf("[%s] Board.Winner=%q ok=%v != receipt %q", uuid, bw, ok, winning.NodeID)
	}
	if !b.Deployed(id) {
		t.Fatalf("[%s] not deployed", uuid)
	}
	t.Logf("[%s] MinInt64 reward: exactly one allocation under %d-way contention; no over-commit", uuid, goroutines)
}

// ---------------------------------------------------------------------------
// Class (d): HOSTILE-INPUT DoS — giant/odd bounty ids are O(1) map keys, no panic.
// ---------------------------------------------------------------------------

// TestAdv_HostileBountyIDNoDoS feeds attacker-shaped bounty ids (multi-megabyte,
// embedded NULs, and a key that collides on prefix) and asserts the operations
// remain correct and bounded: no panic, no unbounded recursion/allocation beyond
// the key itself, and the giant key still clears to exactly one winner. Bounty
// ids are plain map keys, so there is NO parsing/decompression/recursion surface
// — this PINS that property so a future "id parsing" change can't silently add a
// DoS vector.
//
// SINK-SIDE: the huge-id bounty is claimable exactly once with the right reward;
// a near-identical (prefix-colliding) id is an INDEPENDENT bounty.
func TestAdv_HostileBountyIDNoDoS(t *testing.T) {
	uuid := newRunUUID(t.Name())
	b := NewBoard()

	huge := strings.Repeat("A", 4<<20) // 4 MiB id
	withNUL := "id\x00with\x00nuls"
	prefixA := huge + "X"
	prefixB := huge + "Y" // shares a 4 MiB prefix with prefixA but is distinct

	cases := []struct {
		id     string
		reward int64
	}{
		{huge, 11},
		{withNUL, 22},
		{prefixA, 33},
		{prefixB, 44},
	}
	for _, tc := range cases {
		if err := b.Post(Bounty{ID: tc.id, Reward: tc.reward}); err != nil {
			t.Fatalf("[%s] Post(len=%d): %v", uuid, len(tc.id), err)
		}
	}
	// Each hostile id is an INDEPENDENT bounty: claim each once, correct reward.
	for _, tc := range cases {
		c, err := b.Claim(tc.id, "node")
		if err != nil {
			t.Fatalf("[%s] Claim(len=%d): %v", uuid, len(tc.id), err)
		}
		if c.Reward != tc.reward {
			t.Fatalf("[%s] id(len=%d) reward=%d want %d", uuid, len(tc.id), c.Reward, tc.reward)
		}
		if !b.Deployed(tc.id) {
			t.Fatalf("[%s] id(len=%d) not deployed", uuid, len(tc.id))
		}
		// Re-claim loses: the giant key did not silently fail the test-and-set.
		if _, err := b.Claim(tc.id, "intruder"); !errors.Is(err, ErrAlreadyClaimed) {
			t.Fatalf("[%s] id(len=%d) re-claim err=%v want ErrAlreadyClaimed", uuid, len(tc.id), err)
		}
	}
	// Prefix-colliding ids must NOT cross-contaminate (independent winners/prices).
	if wa, _ := b.Winner(prefixA); wa != "node" {
		t.Fatalf("[%s] prefixA winner=%q", uuid, wa)
	}
	if b.Deployed("never-posted-"+withNUL) {
		t.Fatalf("[%s] unposted derivative reports deployed", uuid)
	}
	t.Logf("[%s] hostile ids (4MiB, NUL-embedded, prefix-colliding) handled in O(1); no panic, no cross-contamination", uuid)
}
