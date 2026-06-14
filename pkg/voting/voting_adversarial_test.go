package voting

// Adversarial probes for pkg/voting targeting the four tonight's classes:
//
//	(a) QUORUM / THRESHOLD BOUNDARY  — off-by-one `<` vs `<=` / `>` vs `>=`
//	(b) DUPLICATE-VOTER INFLATION    — one voter counted twice reaching quorum
//	(c) TOTAL-ORDER AT A TIE         — equal tallies must pick ONE winner,
//	                                   deterministically, never by map iteration
//	(d) UNGUARDED SHARED STATE       — concurrent Decide must be race-clean and
//	                                   conserve the tally
//
// Every assertion below is SINK-SIDE: it checks the actual verdict
// (CanLeadPrimary), the actual Reason, and the actual Reachable tally — never
// merely "did not panic". Where a class is provably correct in prod, the test
// is a contract-pinning guard with a documented mutation that would make it bite.
//
// FINDING (see report): NO BUG. prod is correct across all four classes; these
// tests pin the contract and would fail under the canonical mutations.

import (
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// (a) QUORUM / THRESHOLD BOUNDARY — sink-side, both sides of the boundary.
// -----------------------------------------------------------------------------

// TestAdv_QuorumBoundary_ExactVsOneBelow sweeps N=2..9 and proves the decision
// FLIPS exactly at the strict-majority threshold floor(N/2)+1: a reach of
// (quorum) leads, a reach of (quorum-1) does NOT lead via majority.
//
// This bites the off-by-one named in the task. Two canonical mutations:
//   - Quorum() -> len(d.members)/2  (drop +1): for ODD N, quorum becomes
//     floor(N/2) and the (quorum-1) side now equals the new quorum and FALSELY
//     leads with ReasonHasMajority -> the "one below must not have majority"
//     assertion fails.
//   - Decide `count >= quorum` -> `count > quorum`: the exact-quorum side stops
//     leading -> the "at quorum leads" assertion fails.
func TestAdv_QuorumBoundary_ExactVsOneBelow(t *testing.T) {
	for n := 2; n <= 9; n++ {
		members := make([]string, n)
		for i := range members {
			members[i] = string(rune('a' + i))
		}
		d, err := NewQuorumDecider(Config{LocalID: members[0], Members: members})
		require.NoError(t, err)

		q := d.Quorum()
		require.Equal(t, n/2+1, q, "quorum must be floor(N/2)+1 for N=%d", n)

		// Build a reach of exactly `q` members including self.
		atQuorum := append([]string{}, members[:q]...)
		require.Contains(t, atQuorum, members[0])
		decAt, err := d.Decide(atQuorum)
		require.NoError(t, err)
		assert.Truef(t, decAt.CanLeadPrimary,
			"N=%d: reaching exactly quorum (%d) is a strict majority and MUST lead", n, q)
		assert.Equalf(t, ReasonHasMajority, decAt.Reason,
			"N=%d: exact-quorum reach must be ReasonHasMajority", n)
		assert.Equalf(t, q, decAt.Reachable, "N=%d: tally must equal the reach", n)

		// Build a reach of exactly q-1 members including self.
		below := append([]string{}, members[:q-1]...)
		require.Contains(t, below, members[0])
		decBelow, err := d.Decide(below)
		require.NoError(t, err)
		// One below quorum must NEVER claim a strict majority. (It may be a tie on
		// an even split, but it must not be ReasonHasMajority and, for the
		// majority verdict specifically, must not lead via majority.)
		assert.NotEqualf(t, ReasonHasMajority, decBelow.Reason,
			"N=%d: reaching quorum-1 (%d) is NOT a strict majority — must not be ReasonHasMajority", n, q-1)
		assert.Equalf(t, q-1, decBelow.Reachable, "N=%d: tally must equal the reach", n)

		// Sink-side flip: on ODD N (no tie possible), quorum-1 must self-fence
		// with ReasonLostQuorum and NOT lead at all.
		if n%2 == 1 {
			assert.Falsef(t, decBelow.CanLeadPrimary,
				"N=%d (odd): quorum-1 (%d) must self-fence, never lead", n, q-1)
			assert.Equalf(t, ReasonLostQuorum, decBelow.Reason,
				"N=%d (odd): quorum-1 is decisively lost-quorum", n)
		}
	}
}

// -----------------------------------------------------------------------------
// (b) DUPLICATE-VOTER INFLATION — the verdict must NOT flip on a duplicate.
// -----------------------------------------------------------------------------

// TestAdv_DuplicateVoter_DoesNotFlipVerdict proves a duplicated voter cannot
// inflate the tally across the quorum boundary. A 2/5 minority lists one of its
// reachable members TWICE (and the dup repeated again) — the honest tally stays
// 2 and the verdict stays self-fence. If duplicates were counted, 2 distinct +
// 1 dup would reach 3 == quorum and FALSELY lead.
//
// Mutation: remove the dedup guard `if _, dup := seen[r]; dup { continue }` in
// Decide. Then "n2" listed 3x yields count=4 -> 4>=3 -> CanLeadPrimary=true with
// ReasonHasMajority -> BOTH the verdict-flip and tally assertions below FAIL.
func TestAdv_DuplicateVoter_DoesNotFlipVerdict(t *testing.T) {
	d, err := NewQuorumDecider(Config{
		LocalID: "n1", Members: []string{"n1", "n2", "n3", "n4", "n5"},
	})
	require.NoError(t, err)

	// Honest minority: {n1, n2} -> 2/5 -> must self-fence.
	honest, err := d.Decide([]string{"n1", "n2"})
	require.NoError(t, err)
	require.False(t, honest.CanLeadPrimary)
	require.Equal(t, 2, honest.Reachable)
	require.Equal(t, ReasonLostQuorum, honest.Reason)

	// Same reachable members, but n2 stuffed THREE times trying to fake a 3rd vote.
	stuffed, err := d.Decide([]string{"n1", "n2", "n2", "n2"})
	require.NoError(t, err)

	// Sink-side: the verdict must be IDENTICAL to the honest one — no inflation.
	assert.Equal(t, 2, stuffed.Reachable,
		"duplicate voter must count once: tally stays 2, not inflated to 3")
	assert.False(t, stuffed.CanLeadPrimary,
		"ballot-stuffing a duplicate must NOT manufacture a quorum / flip the verdict")
	assert.Equal(t, ReasonLostQuorum, stuffed.Reason)
	assert.Equal(t, honest.CanLeadPrimary, stuffed.CanLeadPrimary,
		"duplicate voter must not change the decision vs the honest reachable set")

	// Stronger: a duplicate at the exact boundary must not push a 2/4 even split
	// (a legitimate tie) into a false majority.
	d4, err := NewQuorumDecider(Config{
		LocalID: "a", Members: []string{"a", "b", "c", "d"}, Tiebreak: TiebreakLowestID,
	})
	require.NoError(t, err)
	tieDup, err := d4.Decide([]string{"a", "b", "b"}) // 2 distinct, 1 dup -> still 2/4 tie
	require.NoError(t, err)
	assert.Equal(t, 2, tieDup.Reachable, "dup must not push 2/4 tie to 3/4 majority")
	assert.Equal(t, ReasonTieWon, tieDup.Reason,
		"with lowest-ID 'a' present the tie is WON, but it must remain a TIE not a majority")
	assert.NotEqual(t, ReasonHasMajority, tieDup.Reason,
		"a duplicated voter must never upgrade a tie into a strict majority")
}

// -----------------------------------------------------------------------------
// (c) TOTAL-ORDER AT A TIE — equal tally => ONE deterministic winner, no
//     dependence on map iteration order.
// -----------------------------------------------------------------------------

// TestAdv_TieDeterminism_ManyRuns_SameWinner runs the SAME even-split tie many
// times and across freshly-constructed deciders (fresh internal maps each time,
// so Go's randomized map iteration would surface here if the winner depended on
// it). The authoritative side must be the SAME every single time.
//
// Two candidate sides of a 4-node 2-2 split have EQUAL tally (2 each). The
// lowest-ID tiebreak must pick the side holding "a" — deterministically.
//
// Mutation: if tiebreakWins iterated the `seen` map to pick a winner (instead of
// reading the sorted d.members[0]), the winning side could vary run-to-run; this
// loop of 500 fresh evaluations would observe both outcomes and the
// "single distinct winner" assertion FAILS. As written, the winner is fixed.
func TestAdv_TieDeterminism_ManyRuns_SameWinner(t *testing.T) {
	members := []string{"a", "b", "c", "d"}
	sideWithLowest := []string{"a", "b"} // holds globally-lowest "a"
	sideWithout := []string{"c", "d"}

	const runs = 500
	winSeen := map[bool]int{}  // side-with-lowest CanLeadPrimary -> count
	loseSeen := map[bool]int{} // side-without CanLeadPrimary -> count

	for i := 0; i < runs; i++ {
		dWin, err := NewQuorumDecider(Config{
			LocalID: "a", Members: members, Tiebreak: TiebreakLowestID,
		})
		require.NoError(t, err)
		decWin, err := dWin.Decide(sideWithLowest)
		require.NoError(t, err)
		winSeen[decWin.CanLeadPrimary]++
		require.Equal(t, 2, decWin.Reachable, "both sides have EQUAL tally 2")

		dLose, err := NewQuorumDecider(Config{
			LocalID: "c", Members: members, Tiebreak: TiebreakLowestID,
		})
		require.NoError(t, err)
		decLose, err := dLose.Decide(sideWithout)
		require.NoError(t, err)
		loseSeen[decLose.CanLeadPrimary]++
		require.Equal(t, 2, decLose.Reachable, "both sides have EQUAL tally 2")
	}

	// Sink-side determinism: across 500 fresh runs the winner is ALWAYS the same
	// single side. Exactly one outcome value was ever observed for each side.
	assert.Lenf(t, winSeen, 1,
		"tie winner must be DETERMINISTIC: side-with-lowest produced mixed verdicts %v", winSeen)
	assert.Lenf(t, loseSeen, 1,
		"tie loser must be DETERMINISTIC: side-without produced mixed verdicts %v", loseSeen)

	// And the deterministic outcome is the correct one: lowest-ID side wins,
	// the other fences. Exactly one authoritative side -> no split-brain, no
	// no-primary stall.
	assert.Equal(t, runs, winSeen[true], "side holding lowest ID 'a' must ALWAYS win the tie")
	assert.Equal(t, runs, loseSeen[false], "side without lowest ID must ALWAYS lose the tie")
}

// TestAdv_TieDeterminism_MemberOrderIrrelevant proves the tie winner does not
// depend on the ORDER members are supplied in Config (which seeds nothing random
// but is a common source of accidental order-dependence). Shuffled member orders
// and shuffled reachable orders must all elect the same single side.
//
// Mutation: tiebreakWins using `d.members[len(d.members)-1]` (highest) instead
// of [0] would still be deterministic but would flip the winner — caught by the
// "side with 'a' wins" assertion. A map-iteration-based winner would be caught
// by cross-permutation disagreement.
func TestAdv_TieDeterminism_MemberOrderIrrelevant(t *testing.T) {
	memberPerms := [][]string{
		{"a", "b", "c", "d"},
		{"d", "c", "b", "a"},
		{"c", "a", "d", "b"},
		{"b", "d", "a", "c"},
	}
	reachPerms := [][]string{
		{"a", "b"},
		{"b", "a"},
	}

	for _, mp := range memberPerms {
		for _, rp := range reachPerms {
			d, err := NewQuorumDecider(Config{
				LocalID: "a", Members: mp, Tiebreak: TiebreakLowestID,
			})
			require.NoError(t, err)
			dec, err := d.Decide(rp)
			require.NoError(t, err)
			assert.Truef(t, dec.CanLeadPrimary,
				"members=%v reach=%v: side holding lowest 'a' must win regardless of order", mp, rp)
			assert.Equalf(t, ReasonTieWon, dec.Reason,
				"members=%v reach=%v: must be a resolved tie win", mp, rp)
		}
	}
}

// -----------------------------------------------------------------------------
// (d) UNGUARDED SHARED STATE — concurrent Decide must be race-clean (under
//     -race) AND every call must return the correct, conserved tally.
// -----------------------------------------------------------------------------

// TestAdv_ConcurrentDecide_RaceClean_Conserved hammers ONE shared decider from
// many goroutines, each mixing majority / minority / tie reaches, and asserts
// every individual decision is correct (tally + verdict). Run under -race, any
// unsynchronized write to shared decider state trips the detector.
//
// Conservation: we count, atomically, how many calls returned each verdict and
// assert the totals match the deterministic expectation exactly (no lost or
// spurious "lead" verdicts). A data race that corrupted the shared `seen`/count
// (if Decide ever shared them) would produce a wrong tally on some call and trip
// the per-call assertions; a lost vote would skew the conservation totals.
//
// Mutation: give QuorumDecider a shared mutable field and have Decide write it
// without a lock; `go test -race` flags the write -> FAIL. (As written, Decide
// allocates its `seen` map per call and only reads immutable config, so it is
// race-clean and the conservation totals are exact.)
func TestAdv_ConcurrentDecide_RaceClean_Conserved(t *testing.T) {
	d, err := NewQuorumDecider(Config{
		LocalID: "n1", Members: []string{"n1", "n2", "n3", "n4", "n5"},
	})
	require.NoError(t, err)

	const (
		goroutines  = 96
		perGoroutine = 250
	)
	var leadCount, fenceCount, errCount int64

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		kind := g % 3
		go func(kind int) {
			defer wg.Done()
			var reach []string
			var wantLead bool
			var wantReason Reason
			var wantTally int
			switch kind {
			case 0: // majority 3/5
				reach = []string{"n1", "n2", "n3"}
				wantLead, wantReason, wantTally = true, ReasonHasMajority, 3
			case 1: // minority 2/5
				reach = []string{"n1", "n2"}
				wantLead, wantReason, wantTally = false, ReasonLostQuorum, 2
			default: // duplicate-stuffed minority 2/5 (still self-fences)
				reach = []string{"n1", "n2", "n2", "n2"}
				wantLead, wantReason, wantTally = false, ReasonLostQuorum, 2
			}
			for i := 0; i < perGoroutine; i++ {
				dec, derr := d.Decide(reach)
				if derr != nil {
					atomic.AddInt64(&errCount, 1)
					continue
				}
				// Sink-side per-call correctness.
				if dec.Reachable != wantTally || dec.CanLeadPrimary != wantLead || dec.Reason != wantReason {
					// Record as an error sentinel via a deliberately impossible bump.
					atomic.AddInt64(&errCount, 1)
					continue
				}
				if dec.CanLeadPrimary {
					atomic.AddInt64(&leadCount, 1)
				} else {
					atomic.AddInt64(&fenceCount, 1)
				}
			}
		}(kind)
	}
	wg.Wait()

	assert.Equal(t, int64(0), errCount, "no errors and no per-call tally/verdict mismatches under concurrency")

	// Conservation: exactly the goroutines assigned kind==0 produce 'lead'; the
	// other two thirds produce 'fence'. Totals must be exact — no lost/spurious
	// votes from a race.
	leadGoroutines := 0
	for g := 0; g < goroutines; g++ {
		if g%3 == 0 {
			leadGoroutines++
		}
	}
	expectLead := int64(leadGoroutines * perGoroutine)
	expectFence := int64((goroutines - leadGoroutines) * perGoroutine)
	assert.Equal(t, expectLead, leadCount, "lead verdicts conserved exactly")
	assert.Equal(t, expectFence, fenceCount, "fence verdicts conserved exactly")
	assert.Equal(t, int64(goroutines*perGoroutine), leadCount+fenceCount,
		"every concurrent Decide accounted for: no lost votes")
}

// TestAdv_ConcurrentTie_DeterministicUnderRace runs the tie path concurrently
// across fresh deciders and asserts the winning side is unanimous. Combines
// class (c) determinism with class (d) concurrency: even under -race scheduling
// jitter the same side wins every time.
func TestAdv_ConcurrentTie_DeterministicUnderRace(t *testing.T) {
	members := []string{"a", "b", "c", "d"}
	const goroutines = 64

	var winnerTrue, winnerFalse int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			d, err := NewQuorumDecider(Config{
				LocalID: "a", Members: members, Tiebreak: TiebreakLowestID,
			})
			if err != nil {
				return
			}
			dec, err := d.Decide([]string{"a", "b"})
			if err != nil {
				return
			}
			if dec.CanLeadPrimary {
				atomic.AddInt64(&winnerTrue, 1)
			} else {
				atomic.AddInt64(&winnerFalse, 1)
			}
		}()
	}
	wg.Wait()

	// The lowest-ID side must win unanimously; never a mixed/nondeterministic
	// outcome across concurrent fresh deciders.
	assert.Equal(t, int64(goroutines), winnerTrue,
		"side holding lowest ID must win the tie on EVERY concurrent run")
	assert.Equal(t, int64(0), winnerFalse,
		"no run may report the lowest-ID side as a loser (would be nondeterminism)")
}

// sanity: keep sort import used even if future edits drop a usage; sorting member
// perms documents that order-independence is intentional, not accidental.
var _ = sort.Strings
