package voting

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDecider5 builds a canonical 5-node decider with the given tiebreak config.
func newDecider5(t *testing.T) *QuorumDecider {
	t.Helper()
	d, err := NewQuorumDecider(Config{
		LocalID: "n1",
		Members: []string{"n1", "n2", "n3", "n4", "n5"},
	})
	require.NoError(t, err)
	return d
}

// TestQuorumThreshold_N5 pins the strict-majority threshold for N=5 to exactly 3.
//
// Mutation: change Quorum() from `len(d.members)/2 + 1` to `len(d.members)/2`
// (drop the +1). Then Quorum() returns 2 and this assertion fails.
func TestQuorumThreshold_N5(t *testing.T) {
	d := newDecider5(t)
	assert.Equal(t, 5, d.Total())
	assert.Equal(t, 3, d.Quorum(), "floor(5/2)+1 must be 3")
}

// TestReaching3of5_LeadsPrimary: a node that reaches 3/5 holds a strict majority
// and may lead.
//
// Mutation: in Decide, change `if count >= quorum` to `if count > quorum`.
// Then 3 >= 3 becomes 3 > 3 (false), the majority node loses leadership, and
// CanLeadPrimary/Reason assertions fail.
func TestReaching3of5_LeadsPrimary(t *testing.T) {
	d := newDecider5(t)
	dec, err := d.Decide([]string{"n1", "n2", "n3"})
	require.NoError(t, err)

	assert.True(t, dec.CanLeadPrimary, "3/5 is a strict majority and must lead")
	assert.Equal(t, ReasonHasMajority, dec.Reason)
	assert.Equal(t, 3, dec.Reachable)
	assert.Equal(t, 5, dec.Total)
	assert.Equal(t, 3, dec.Quorum)
}

// TestReaching2of5_SelfFences: a node that reaches only 2/5 is in the minority
// and MUST self-fence.
//
// This is the off-by-one mutation guard named in the task: a decider using
// '>=' on floor(N/2) (i.e. count >= 2 instead of count >= 3) would let a 2/5
// minority lead. Concretely, mutate Quorum() to `len(d.members)/2` (=2): then
// 2 >= 2 yields ReasonHasMajority and CanLeadPrimary=true, and BOTH assertions
// below fail. The minority would falsely lead -> split-brain.
func TestReaching2of5_SelfFences(t *testing.T) {
	d := newDecider5(t)
	dec, err := d.Decide([]string{"n1", "n2"})
	require.NoError(t, err)

	assert.False(t, dec.CanLeadPrimary, "2/5 is a minority and MUST self-fence")
	assert.Equal(t, ReasonLostQuorum, dec.Reason)
	assert.Equal(t, 2, dec.Reachable)
	assert.Equal(t, 3, dec.Quorum)
}

// TestReaching1of5_SelfFences: an isolated node (reaches only itself) self-fences.
//
// Mutation: replace the final `base.Reason = ReasonLostQuorum` with
// `ReasonHasMajority` and set CanLeadPrimary=true; the assertion that a lone
// node cannot lead fails.
func TestReaching1of5_SelfFences(t *testing.T) {
	d := newDecider5(t)
	dec, err := d.Decide([]string{"n1"})
	require.NoError(t, err)

	assert.False(t, dec.CanLeadPrimary)
	assert.Equal(t, ReasonLostQuorum, dec.Reason)
	assert.Equal(t, 1, dec.Reachable)
}

// TestReaching4of5_LeadsPrimary: the trivial supermajority leads.
//
// Mutation: same `>` instead of `>=` mutation; 4 >= 3 still holds so this is a
// weaker guard, but it pins that a clear majority is ReasonHasMajority not a tie.
func TestReaching4of5_LeadsPrimary(t *testing.T) {
	d := newDecider5(t)
	dec, err := d.Decide([]string{"n1", "n2", "n3", "n4"})
	require.NoError(t, err)
	assert.True(t, dec.CanLeadPrimary)
	assert.Equal(t, ReasonHasMajority, dec.Reason)
}

// TestN1_TriviallyLeads: a single-node cluster always leads (quorum=1).
//
// Mutation: change Quorum() to `len(d.members)/2 + 2`; for N=1 that yields 2,
// 1 >= 2 is false, and the single node would (wrongly) fail to lead — assertion
// fails.
func TestN1_TriviallyLeads(t *testing.T) {
	d, err := NewQuorumDecider(Config{LocalID: "solo", Members: []string{"solo"}})
	require.NoError(t, err)
	assert.Equal(t, 1, d.Quorum())

	dec, err := d.Decide([]string{"solo"})
	require.NoError(t, err)
	assert.True(t, dec.CanLeadPrimary, "N=1 trivially leads")
	assert.Equal(t, ReasonHasMajority, dec.Reason)
}

// --- Even-split (2-2) tiebreak tests ------------------------------------------

// build4 makes a 4-node decider for the given local node and tiebreak config.
func build4(t *testing.T, local string, mode TiebreakMode, witness string) *QuorumDecider {
	t.Helper()
	d, err := NewQuorumDecider(Config{
		LocalID:   local,
		Members:   []string{"a", "b", "c", "d"},
		Tiebreak:  mode,
		WitnessID: witness,
	})
	require.NoError(t, err)
	return d
}

// TestEvenSplit_LowestID_ExactlyOneWinner: a 2-2 split of {a,b,c,d}. With
// TiebreakLowestID the side containing the globally-lowest ID "a" wins; the
// other side loses. Exactly one winner.
//
// Side X = {a,b}; Side Y = {c,d}. Node "a" (on X) must win; node "c" (on Y)
// must lose. The decision pair proves "exactly one" — both cannot lead.
//
// Mutation: in tiebreakWins TiebreakLowestID case, replace `d.members[0]`
// (lowest) with `d.members[len(d.members)-1]` (highest, "d"). Then the {c,d}
// side wins and the {a,b} side loses, flipping BOTH expectations -> failure.
func TestEvenSplit_LowestID_ExactlyOneWinner(t *testing.T) {
	winnerSide := []string{"a", "b"}
	loserSide := []string{"c", "d"}

	// Node "a" sits on the winning side.
	da := build4(t, "a", TiebreakLowestID, "")
	decA, err := da.Decide(winnerSide)
	require.NoError(t, err)
	assert.True(t, decA.CanLeadPrimary, "side holding lowest ID 'a' wins the tie")
	assert.Equal(t, ReasonTieWon, decA.Reason)
	assert.Equal(t, 2, decA.Reachable)
	assert.Equal(t, 3, decA.Quorum, "floor(4/2)+1 == 3; 2 is NOT a strict majority")

	// Node "c" sits on the losing side.
	dc := build4(t, "c", TiebreakLowestID, "")
	decC, err := dc.Decide(loserSide)
	require.NoError(t, err)
	assert.False(t, decC.CanLeadPrimary, "side without lowest ID loses and self-fences")
	assert.Equal(t, ReasonTieLost, decC.Reason)

	// Exactly one winner across the two sides of the SAME partition.
	assert.NotEqual(t, decA.CanLeadPrimary, decC.CanLeadPrimary,
		"a 2-2 split must produce exactly one primary")
}

// TestEvenSplit_Witness_ExactlyOneWinner: same 2-2 split, but resolved by a
// witness. Witness = "d". The side that can reach "d" wins.
//
// Side {c,d} reaches the witness -> wins; side {a,b} cannot -> loses.
//
// Mutation: in tiebreakWins TiebreakWitness case, invert the result
// (`return !ok`). Then the side WITHOUT the witness would win, flipping both
// expectations -> failure. (Also proves the witness, not lowest-ID, drives this
// mode: here the winner {c,d} does NOT contain the lowest ID "a".)
func TestEvenSplit_Witness_ExactlyOneWinner(t *testing.T) {
	// Witness "d" lives on side {c,d}.
	withWitness := []string{"c", "d"}
	withoutWitness := []string{"a", "b"}

	dWin := build4(t, "c", TiebreakWitness, "d")
	decWin, err := dWin.Decide(withWitness)
	require.NoError(t, err)
	assert.True(t, decWin.CanLeadPrimary, "side reaching witness 'd' wins")
	assert.Equal(t, ReasonTieWon, decWin.Reason)

	dLose := build4(t, "a", TiebreakWitness, "d")
	decLose, err := dLose.Decide(withoutWitness)
	require.NoError(t, err)
	assert.False(t, decLose.CanLeadPrimary, "side unable to reach witness loses")
	assert.Equal(t, ReasonTieLost, decLose.Reason)

	assert.NotEqual(t, decWin.CanLeadPrimary, decLose.CanLeadPrimary,
		"witness tiebreak must produce exactly one primary")
}

// TestEvenSplit_NoTiebreak_BothFence: with TiebreakNone a 2-2 split fences BOTH
// sides (maximally safe, never split-brain).
//
// Mutation: change tiebreakWins default case to `return true`. Then both sides
// would lead and the "both fence" / ReasonTieLost assertions fail.
func TestEvenSplit_NoTiebreak_BothFence(t *testing.T) {
	for _, side := range [][]string{{"a", "b"}, {"c", "d"}} {
		d := build4(t, side[0], TiebreakNone, "")
		dec, err := d.Decide(side)
		require.NoError(t, err)
		assert.False(t, dec.CanLeadPrimary, "TiebreakNone: even split self-fences both sides")
		assert.Equal(t, ReasonTieLost, dec.Reason)
	}
}

// TestEvenSplit_OneSideLessThanHalf_LostQuorum: in a 4-node cluster a side that
// reaches only 1/4 (less than half) decisively loses quorum — it is NOT a tie.
//
// Mutation: change the even-split guard `count == n/2` to `count <= n/2`. Then
// a 1/4 reach would be misclassified as a tie (ReasonTie*), failing the
// ReasonLostQuorum assertion.
func TestEvenSplit_OneSideLessThanHalf_LostQuorum(t *testing.T) {
	d := build4(t, "a", TiebreakLowestID, "")
	dec, err := d.Decide([]string{"a"}) // reaches only itself: 1/4
	require.NoError(t, err)
	assert.False(t, dec.CanLeadPrimary)
	assert.Equal(t, ReasonLostQuorum, dec.Reason, "1/4 < half is lost-quorum, not a tie")
	assert.Equal(t, 1, dec.Reachable)
}

// --- Robustness / contract tests ----------------------------------------------

// TestForeignNodesDoNotCountTowardQuorum: members not in the known set cannot
// inflate the reachable count. n1 reaches itself + n2 + two FOREIGN ids; only
// 2 in-cluster members count, so 2/5 still self-fences.
//
// Mutation: delete the `if _, ok := d.memberOf[r]; !ok { continue }` filter in
// Decide. Then the two foreign ids inflate count to 4, 4 >= 3 leads, and the
// self-fence assertion fails -> a stale node could manufacture a false quorum.
func TestForeignNodesDoNotCountTowardQuorum(t *testing.T) {
	d := newDecider5(t)
	dec, err := d.Decide([]string{"n1", "n2", "ghost-x", "ghost-y"})
	require.NoError(t, err)
	assert.Equal(t, 2, dec.Reachable, "only in-cluster members count")
	assert.False(t, dec.CanLeadPrimary)
	assert.Equal(t, ReasonLostQuorum, dec.Reason)
}

// TestDuplicateReachableCountedOnce: a duplicated reachable id is counted once.
//
// Mutation: remove the dedup `if _, dup := seen[r]; dup { continue }` guard.
// Then n2 listed twice makes count=3 and 3 >= 3 falsely leads; the self-fence
// assertion fails.
func TestDuplicateReachableCountedOnce(t *testing.T) {
	d := newDecider5(t)
	dec, err := d.Decide([]string{"n1", "n2", "n2"})
	require.NoError(t, err)
	assert.Equal(t, 2, dec.Reachable, "duplicate n2 counts once")
	assert.False(t, dec.CanLeadPrimary)
}

// TestSelfMustBeReachable: the reachable set must include the local node.
//
// Mutation: delete the `if !selfReachable { return ..., ErrSelfNotReachable }`
// check. Then Decide would return a Decision instead of the error and this
// assertion fails.
func TestSelfMustBeReachable(t *testing.T) {
	d := newDecider5(t)
	_, err := d.Decide([]string{"n2", "n3", "n4"}) // n1 missing
	assert.ErrorIs(t, err, ErrSelfNotReachable)
}

// TestCanLeadPrimaryConvenience_FalseOnError: the convenience wrapper returns
// false when Decide errors (self not reachable), never a panic or true.
//
// Mutation: in CanLeadPrimary change `return false` (on err) to `return true`;
// assertion fails.
func TestCanLeadPrimaryConvenience_FalseOnError(t *testing.T) {
	d := newDecider5(t)
	assert.False(t, d.CanLeadPrimary([]string{"n2", "n3"}), "self absent -> false, not true")
	assert.True(t, d.CanLeadPrimary([]string{"n1", "n2", "n3"}), "3/5 majority -> true")
}

// --- Constructor validation ---------------------------------------------------

func TestConstructorRejectsBadConfig(t *testing.T) {
	_, err := NewQuorumDecider(Config{LocalID: "x", Members: nil})
	assert.Error(t, err, "empty members rejected")

	_, err = NewQuorumDecider(Config{LocalID: "x", Members: []string{"a", "b"}})
	assert.ErrorIs(t, err, ErrSelfNotMember, "local not in members rejected")

	_, err = NewQuorumDecider(Config{LocalID: "a", Members: []string{"a", "b"}, Tiebreak: TiebreakWitness})
	assert.Error(t, err, "witness mode without WitnessID rejected")

	_, err = NewQuorumDecider(Config{
		LocalID: "a", Members: []string{"a", "b"},
		Tiebreak: TiebreakWitness, WitnessID: "zzz",
	})
	assert.Error(t, err, "witness must be a known member")
}

// TestDeduplicatedMembersAffectQuorum: duplicate member IDs in config collapse,
// so N and quorum reflect distinct members. Members {a,a,b} => N=2, quorum=2.
//
// Mutation: build memberOf/members without de-duplication (count raw slice len
// for N). Then N would be 3, quorum 2 still — but Total() would report 3; the
// Total==2 assertion fails.
func TestDeduplicatedMembersAffectQuorum(t *testing.T) {
	d, err := NewQuorumDecider(Config{LocalID: "a", Members: []string{"a", "a", "b"}})
	require.NoError(t, err)
	assert.Equal(t, 2, d.Total(), "duplicates collapse: N=2")
	assert.Equal(t, 2, d.Quorum())
}

// --- Concurrency: race-clean decisions ----------------------------------------

// TestConcurrentDecisions_RaceClean runs many Decide calls concurrently against
// a shared decider and asserts every result is deterministic and correct. Run
// under -race, a data race in Decide (e.g. if it mutated shared state) trips the
// detector and fails the test.
//
// Mutation: add a shared mutable field to QuorumDecider and write it inside
// Decide without synchronization; `go test -race` flags the write and the test
// fails. (As written, Decide is read-only over immutable config, so it is race
// clean.)
func TestConcurrentDecisions_RaceClean(t *testing.T) {
	d := newDecider5(t)

	const goroutines = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		majoritySide := i%2 == 0
		go func(majority bool) {
			defer wg.Done()
			var reach []string
			if majority {
				reach = []string{"n1", "n2", "n3"} // 3/5
			} else {
				reach = []string{"n1", "n2"} // 2/5
			}
			for j := 0; j < 200; j++ {
				dec, err := d.Decide(reach)
				if err != nil {
					t.Errorf("unexpected error: %v", err)
					return
				}
				if majority {
					if !dec.CanLeadPrimary || dec.Reason != ReasonHasMajority {
						t.Errorf("majority side must lead: %+v", dec)
						return
					}
				} else {
					if dec.CanLeadPrimary || dec.Reason != ReasonLostQuorum {
						t.Errorf("minority side must self-fence: %+v", dec)
						return
					}
				}
			}
		}(majoritySide)
	}
	wg.Wait()
}
