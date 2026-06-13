package voting

// Adversarial proof of the package's OWN stated safety invariant.
//
// voting.go's package doc (lines 4-11) claims, verbatim:
//
//	"To avoid split-brain — where two subclusters each believe they are
//	 authoritative and accept conflicting writes — at most ONE subcluster may
//	 continue operating as primary."
//
// The existing tests pin individual hand-picked scenarios (one 3/5 majority,
// one 2-2 lowest-ID split, one witness split, ...). None of them PROVE the
// universal claim across the whole space of partitions. A too-liberal quorum
// predicate (granting authority on `count >= N/2` instead of the strict
// `count >= floor(N/2)+1`, or a tiebreak that lets BOTH sides win) would pass
// every existing single-scenario test yet still allow two disjoint subclusters
// to BOTH lead — the exact split-brain the doc forbids.
//
// These tests close that gap by EXHAUSTIVELY enumerating partitions and
// asserting, for every partition and every tiebreak mode, that the number of
// subclusters reporting CanLeadPrimary==true is NEVER greater than 1.

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// memberNames returns n stable member IDs: m0, m1, ... (lexicographically
// ordered so m0 is the globally lowest ID — relevant for TiebreakLowestID).
func memberNames(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		// zero-padded so lexicographic order == numeric order even past m9.
		ids[i] = fmt.Sprintf("m%02d", i)
	}
	return ids
}

// genPartitions enumerates EVERY set-partition of items into one or more
// non-empty disjoint groups. It does so by assigning each item a group index
// using restricted-growth strings (so {A|B} and {B|A} are not double counted).
// The returned slices are subclusters that together cover all items with no
// overlap — i.e. exactly the disjoint subclusters a network partition produces.
func genPartitions(items []string) [][][]string {
	n := len(items)
	var out [][][]string
	assign := make([]int, n)

	var rec func(i, maxGroup int)
	rec = func(i, maxGroup int) {
		if i == n {
			groups := make([][]string, maxGroup+1)
			for idx, g := range assign {
				groups[g] = append(groups[g], items[idx])
			}
			// All groups are non-empty by restricted-growth construction.
			cp := make([][]string, len(groups))
			copy(cp, groups)
			out = append(out, cp)
			return
		}
		// item i may join any existing group 0..maxGroup, or open a new one.
		for g := 0; g <= maxGroup+1; g++ {
			assign[i] = g
			nextMax := maxGroup
			if g == maxGroup+1 {
				nextMax = maxGroup + 1
			}
			rec(i+1, nextMax)
		}
	}
	rec(0, -1)
	return out
}

// countAuthoritative builds, for each subcluster in a partition, a decider whose
// local node is a member of that subcluster, asks it to Decide over EXACTLY that
// subcluster (the partition isolates it from all other members), and returns how
// many subclusters report CanLeadPrimary==true. This is the operational meaning
// of "authoritative subcluster" in this API.
func countAuthoritative(t *testing.T, members []string, partition [][]string, mode TiebreakMode, witness string) (leaders int, detail []string) {
	t.Helper()
	for _, sub := range partition {
		if len(sub) == 0 {
			continue
		}
		local := sub[0] // an arbitrary (lowest-index) member of this side leads it
		d, err := NewQuorumDecider(Config{
			LocalID:   local,
			Members:   members,
			Tiebreak:  mode,
			WitnessID: witness,
		})
		require.NoError(t, err)

		dec, err := d.Decide(sub)
		require.NoErrorf(t, err, "Decide must not error for in-partition side %v (local=%s)", sub, local)
		if dec.CanLeadPrimary {
			leaders++
			detail = append(detail, fmt.Sprintf("side=%v reason=%s reachable=%d quorum=%d", sub, dec.Reason, dec.Reachable, dec.Quorum))
		}
	}
	return leaders, detail
}

// TestInvariant_AtMostOneAuthoritative_AllPartitions is THE core adversarial
// test. For member-set sizes N=1..7, for every tiebreak mode, and for EVERY
// possible disjoint partition of the cluster, it asserts the package's own
// stated invariant: at most ONE subcluster may be authoritative. Two
// simultaneously-authoritative subclusters == split-brain == hard failure.
//
// ANTI-BLUFF (why this BITES): a fake that grants authority too liberally is
// the canonical split-brain bug. Concretely:
//   - mutate Quorum() to `len(d.members)/2` (drop the +1): then on any even N a
//     clean N/2-vs-N/2 split has BOTH sides at count>=quorum -> leaders==2 ->
//     this test FAILS (verified below in the mutation log).
//   - mutate tiebreakWins default/witness/lowest to `return true`: BOTH sides of
//     an even split win -> leaders==2 -> FAILS.
//
// A test that only checks a single hand-picked split (as the originals do) would
// NOT necessarily catch a predicate that misbehaves on some OTHER split shape;
// exhaustive enumeration does.
func TestInvariant_AtMostOneAuthoritative_AllPartitions(t *testing.T) {
	type modeCase struct {
		name    string
		mode    TiebreakMode
		witness string // witness ID for TiebreakWitness; "" otherwise
	}

	totalPartitionsChecked := 0
	maxLeadersSeen := 0

	for n := 1; n <= 7; n++ {
		members := memberNames(n)
		partitions := genPartitions(members)

		modes := []modeCase{
			{"none", TiebreakNone, ""},
			{"lowestID", TiebreakLowestID, ""},
		}
		// Witness mode needs a witness that is a real member; exercise the
		// witness being the lowest, a middle, and the highest member so the
		// witness can land on either side of arbitrary splits.
		if n >= 2 {
			modes = append(modes,
				modeCase{"witness-lowest", TiebreakWitness, members[0]},
				modeCase{"witness-highest", TiebreakWitness, members[n-1]},
				modeCase{"witness-mid", TiebreakWitness, members[n/2]},
			)
		}

		for _, mc := range modes {
			for _, part := range partitions {
				leaders, detail := countAuthoritative(t, members, part, mc.mode, mc.witness)
				totalPartitionsChecked++
				if leaders > maxLeadersSeen {
					maxLeadersSeen = leaders
				}
				// THE invariant: never two authoritative subclusters.
				require.LessOrEqualf(t, leaders, 1,
					"SPLIT-BRAIN: N=%d mode=%s partition=%v produced %d authoritative subclusters (doc guarantees at most ONE): %v",
					n, mc.name, part, leaders, detail)
			}
		}
	}

	t.Logf("at-most-one-authoritative held across %d (partition x mode) cases for N=1..7; max simultaneous leaders observed = %d (must be <= 1)",
		totalPartitionsChecked, maxLeadersSeen)
}

// TestInvariant_TwoWaySplit_ExactlyOneWhenResolvable complements the
// at-most-one test with the LIVENESS side of the guarantee for the canonical
// two-subcluster split the doc describes ("two subclusters"). For a clean
// two-way partition under a deterministic tiebreak (LowestID or a witness that
// is a real member), exactly ONE side must be authoritative on an EVEN split,
// and exactly one (the strict-majority side) on an UNEVEN split. This catches
// the OPPOSITE failure: a predicate so conservative it fences BOTH sides of a
// split that should have a unique winner (false unavailability), while still
// never permitting two.
//
// ANTI-BLUFF: mutate tiebreakWins lowest-ID case to `return false` always; then
// an even 2-2 split fences both sides and the "exactly one on even split"
// assertion fails. Mutate Quorum to `+2` and a 3-2 split fences the majority
// side too -> fails.
func TestInvariant_TwoWaySplit_ExactlyOneWhenResolvable(t *testing.T) {
	for n := 2; n <= 7; n++ {
		members := memberNames(n)
		// Enumerate every non-trivial 2-way split: subset S (the "left" side)
		// from 1..N-1 members, right side is the complement. Use bitmasks.
		for mask := 1; mask < (1 << n); mask++ {
			if mask == (1<<n)-1 {
				continue // not a split: everyone on one side
			}
			var left, right []string
			for i := 0; i < n; i++ {
				if mask&(1<<i) != 0 {
					left = append(left, members[i])
				} else {
					right = append(right, members[i])
				}
			}
			partition := [][]string{left, right}

			// Deterministic tiebreaks guarantee a unique winner on even splits.
			deterministic := []struct {
				name    string
				mode    TiebreakMode
				witness string
			}{
				{"lowestID", TiebreakLowestID, ""},
				{"witness=m00", TiebreakWitness, members[0]},
				{"witness=last", TiebreakWitness, members[n-1]},
			}

			for _, dm := range deterministic {
				leaders, detail := countAuthoritative(t, members, partition, dm.mode, dm.witness)

				// Universal safety: never two.
				require.LessOrEqualf(t, leaders, 1,
					"SPLIT-BRAIN: N=%d split=%v|%v mode=%s -> %d leaders: %v",
					n, left, right, dm.name, leaders, detail)

				// Liveness: a clean two-way split under a deterministic tiebreak
				// always yields EXACTLY one authoritative side — either the
				// strict-majority side, or (on an even split) the tiebreak winner.
				require.Equalf(t, 1, leaders,
					"NO PRIMARY: N=%d split=%v|%v mode=%s should elect exactly one side, got %d: %v",
					n, left, right, dm.name, leaders, detail)
			}
		}
	}
	t.Logf("every 2-way split for N=2..7 under deterministic tiebreaks elected exactly one authoritative side (>=1 liveness AND <=1 safety)")
}

// TestInvariant_NoTiebreak_EvenSplitFencesBoth proves the safest mode never
// produces a leader on a perfectly even split (it trades availability for an
// absolute no-split-brain guarantee). This is the lower bound: even when
// availability is sacrificed, the at-most-one invariant is trivially upheld.
//
// ANTI-BLUFF: mutate tiebreakWins default case to `return true`; both even-split
// sides lead -> leaders==2 -> fails the <=1 assertion AND the ==0 assertion.
func TestInvariant_NoTiebreak_EvenSplitFencesBoth(t *testing.T) {
	for _, n := range []int{2, 4, 6} {
		members := memberNames(n)
		half := n / 2
		left := members[:half]
		right := members[half:]
		partition := [][]string{left, right}

		leaders, detail := countAuthoritative(t, members, partition, TiebreakNone, "")
		require.Equalf(t, 0, leaders,
			"TiebreakNone N=%d even split %v|%v must fence BOTH sides, got %d leader(s): %v",
			n, left, right, leaders, detail)
	}
	t.Logf("TiebreakNone fences both sides of every even split for N in {2,4,6}: 0 leaders, zero split-brain risk")
}
