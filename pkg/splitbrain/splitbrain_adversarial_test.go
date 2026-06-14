package splitbrain_test

import (
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/splitbrain"
)

// This file is an adversarial, mutation-verified audit of the split-brain safety
// oracle (Classify / Decide / Partition). It is the strongest single artifact for
// pinning the quorum-boundary contract:
//
//   - TestAdversarial_ClassifyBoundaryMatrix sweeps EVERY (reachable, total) for
//     total in 1..12 (even AND odd) and reachable in 0..total, and asserts the exact
//     (Partition, Action) at each point against an INDEPENDENT reference oracle that
//     is derived from a different arithmetic than production (it uses the
//     strict-majority count threshold quorum = floor(total/2)+1 rather than the
//     2*reachable vs total comparison). Two independently-derived formulas agreeing
//     on all ~90 grid points is strong evidence the boundary is exactly right; an
//     off-by-one in production (>= vs >, or a shifted threshold) diverges from the
//     oracle and fails loudly.
//
//   - TestAdversarial_DecideNeverLetsMinorityWrite is the split-brain hazard guard:
//     across the whole grid, a Minority / Tie(no witness) / TotalIsolation state must
//     NEVER yield ContinueWrites from Decide, for leader or follower.
//
//   - TestAdversarial_EvenOddTieVsMinority pins the even-vs-odd cliff at the n/2
//     boundary (4|2 tie vs 5|2 minority vs 5|3 majority).
//
//   - TestAdversarial_Edges nails the degenerate inputs (total 0/1, reachable>total,
//     negatives, single node) with no panic.

// ---------------------------------------------------------------------------
// Independent reference oracle.
//
// Deliberately NOT a copy of production's 2*reachable-vs-total arithmetic. This
// uses the textbook strict-majority count quorum = floor(total/2)+1 and an explicit
// half check, so a production off-by-one cannot hide behind a mirrored oracle.
// ---------------------------------------------------------------------------

func refClassify(reachable, total int, hasWitness bool) (splitbrain.Partition, splitbrain.Action) {
	if reachable < 0 || total <= 0 || reachable > total {
		return splitbrain.PartitionUnknown, splitbrain.ActionUnknown
	}
	if reachable == 0 {
		return splitbrain.PartitionTotalIsolation, splitbrain.ActionReadOnly
	}

	quorum := total/2 + 1 // strict majority needs at least floor(total/2)+1 nodes
	switch {
	case reachable >= quorum:
		return splitbrain.PartitionHasQuorum, splitbrain.ActionContinueWrites
	case total%2 == 0 && reachable == total/2:
		// Exactly half of an even cluster => Tie.
		if hasWitness {
			return splitbrain.PartitionTie, splitbrain.ActionContinueWrites
		}
		return splitbrain.PartitionTie, splitbrain.ActionReadOnly
	default:
		return splitbrain.PartitionMinority, splitbrain.ActionReadOnly
	}
}

// TestAdversarial_ClassifyBoundaryMatrix is the centerpiece: an exhaustive sweep of
// the (reachable, total) plane checked against the independent oracle, with both
// witness states at every grid point. It also logs a compact ASCII boundary map so
// the partition classes are visible as captured evidence.
func TestAdversarial_ClassifyBoundaryMatrix(t *testing.T) {
	const maxTotal = 12

	classLetter := map[splitbrain.Partition]string{
		splitbrain.PartitionUnknown:        "?",
		splitbrain.PartitionHasQuorum:      "Q",
		splitbrain.PartitionMinority:       "m",
		splitbrain.PartitionTie:            "T",
		splitbrain.PartitionTotalIsolation: "I",
	}

	for total := 1; total <= maxTotal; total++ {
		var row []byte
		for reachable := 0; reachable <= total; reachable++ {
			for _, witness := range []bool{false, true} {
				gotPart, gotAction := splitbrain.Classify(reachable, total, witness)
				wantPart, wantAction := refClassify(reachable, total, witness)

				if gotPart != wantPart {
					t.Errorf("Classify(%d,%d,witness=%v): Partition = %s, oracle wants %s (quorum boundary mismatch)",
						reachable, total, witness, gotPart, wantPart)
				}
				if gotAction != wantAction {
					t.Errorf("Classify(%d,%d,witness=%v): Action = %s, oracle wants %s",
						reachable, total, witness, gotAction, wantAction)
				}

				// Cross-check the documented arithmetic relation independently:
				// HasQuorum iff strictly more than half are reachable.
				if reachable > 0 {
					strictMajority := 2*reachable > total
					if (gotPart == splitbrain.PartitionHasQuorum) != strictMajority {
						t.Errorf("Classify(%d,%d): HasQuorum=%v but (2*reachable>total)=%v — boundary off-by-one",
							reachable, total, gotPart == splitbrain.PartitionHasQuorum, strictMajority)
					}
				}
			}
			// Build the no-witness boundary-map row.
			p, _ := splitbrain.Classify(reachable, total, false)
			row = append(row, classLetter[p][0])
		}
		t.Logf("N=%2d reachable[0..N]=%s", total, string(row))
	}
}

// TestAdversarial_DecideNeverLetsMinorityWrite is the split-brain hazard guard.
// For every grid point, if Classify did NOT grant quorum then Decide must NEVER
// return ContinueWrites — neither for a leader nor a follower. A minority that keeps
// writing is exactly the split-brain defect this oracle exists to prevent.
func TestAdversarial_DecideNeverLetsMinorityWrite(t *testing.T) {
	for total := 1; total <= 12; total++ {
		for reachable := 0; reachable <= total; reachable++ {
			for _, witness := range []bool{false, true} {
				part, action := splitbrain.Classify(reachable, total, witness)

				// Only a true majority (or witness-endorsed tie) may write at all.
				mayWrite := action == splitbrain.ActionContinueWrites
				if !mayWrite {
					for _, isLeader := range []bool{false, true} {
						got := splitbrain.Decide(part, isLeader)
						if got == splitbrain.ActionContinueWrites {
							t.Errorf("SPLIT-BRAIN: Classify(%d,%d,witness=%v)=%s denied writes, but Decide(%s,leader=%v)=ContinueWrites",
								reachable, total, witness, part, part, isLeader)
						}
					}
				}

				// A leader denied quorum must actively StepDown (not merely read-only),
				// except for the Unknown/invalid state which maps to Unknown.
				if part == splitbrain.PartitionMinority ||
					part == splitbrain.PartitionTotalIsolation ||
					(part == splitbrain.PartitionTie && !witness) {
					if got := splitbrain.Decide(part, true); got != splitbrain.ActionStepDown {
						t.Errorf("leader in non-quorum state %s (from %d-of-%d) must StepDown, got %s",
							part, reachable, total, got)
					}
				}
			}
		}
	}
}

// TestAdversarial_EvenOddTieVsMinority pins the even-vs-odd cliff at the n/2 boundary:
// the same reachable count flips class depending on cluster parity.
func TestAdversarial_EvenOddTieVsMinority(t *testing.T) {
	type want struct {
		reachable, total int
		witness          bool
		part             splitbrain.Partition
		action           splitbrain.Action
	}
	cases := []want{
		// Even total=4: reachable 2 is exactly half => Tie.
		{2, 4, false, splitbrain.PartitionTie, splitbrain.ActionReadOnly},
		{2, 4, true, splitbrain.PartitionTie, splitbrain.ActionContinueWrites},
		{3, 4, false, splitbrain.PartitionHasQuorum, splitbrain.ActionContinueWrites},
		{1, 4, false, splitbrain.PartitionMinority, splitbrain.ActionReadOnly},

		// Odd total=5: reachable 2 is below half => Minority (NOT tie), 3 => majority.
		{2, 5, false, splitbrain.PartitionMinority, splitbrain.ActionReadOnly},
		{2, 5, true, splitbrain.PartitionMinority, splitbrain.ActionReadOnly}, // witness must NOT rescue a true minority
		{3, 5, false, splitbrain.PartitionHasQuorum, splitbrain.ActionContinueWrites},

		// Even total=6: 3 is half => Tie; 4 => majority; 2 => minority.
		{3, 6, false, splitbrain.PartitionTie, splitbrain.ActionReadOnly},
		{4, 6, false, splitbrain.PartitionHasQuorum, splitbrain.ActionContinueWrites},
		{2, 6, false, splitbrain.PartitionMinority, splitbrain.ActionReadOnly},
	}

	for _, c := range cases {
		gotPart, gotAction := splitbrain.Classify(c.reachable, c.total, c.witness)
		if gotPart != c.part || gotAction != c.action {
			t.Errorf("Classify(%d,%d,witness=%v) = (%s,%s), want (%s,%s)",
				c.reachable, c.total, c.witness, gotPart, gotAction, c.part, c.action)
		}
	}

	// Crucial contrast: witness rescues a TIE but must NOT rescue a true MINORITY.
	if _, a := splitbrain.Classify(2, 4, true); a != splitbrain.ActionContinueWrites {
		t.Errorf("witness must promote 2-of-4 tie to ContinueWrites, got %s", a)
	}
	if _, a := splitbrain.Classify(2, 5, true); a == splitbrain.ActionContinueWrites {
		t.Errorf("SPLIT-BRAIN: witness must NOT let the 2-of-5 minority write, got %s", a)
	}
}

// TestAdversarial_Edges nails the degenerate / impossible inputs: no panic, and the
// documented Unknown / single-node behavior.
func TestAdversarial_Edges(t *testing.T) {
	edge := []struct {
		name             string
		reachable, total int
		witness          bool
		part             splitbrain.Partition
		action           splitbrain.Action
	}{
		{"total=0 invalid", 0, 0, false, splitbrain.PartitionUnknown, splitbrain.ActionUnknown},
		{"reachable>total invalid", 3, 2, false, splitbrain.PartitionUnknown, splitbrain.ActionUnknown},
		{"negative reachable invalid", -1, 5, false, splitbrain.PartitionUnknown, splitbrain.ActionUnknown},
		{"negative total invalid", 1, -1, false, splitbrain.PartitionUnknown, splitbrain.ActionUnknown},
		{"single node intact", 1, 1, false, splitbrain.PartitionHasQuorum, splitbrain.ActionContinueWrites},
		{"single node witness no-op", 1, 1, true, splitbrain.PartitionHasQuorum, splitbrain.ActionContinueWrites},
		{"reachable 0 of 1 isolation", 0, 1, false, splitbrain.PartitionTotalIsolation, splitbrain.ActionReadOnly},
	}
	for _, e := range edge {
		gotPart, gotAction := splitbrain.Classify(e.reachable, e.total, e.witness)
		if gotPart != e.part || gotAction != e.action {
			t.Errorf("%s: Classify(%d,%d,witness=%v) = (%s,%s), want (%s,%s)",
				e.name, e.reachable, e.total, e.witness, gotPart, gotAction, e.part, e.action)
		}
	}

	// Invalid partition input to Decide is Unknown for both roles, no panic.
	if got := splitbrain.Decide(splitbrain.PartitionUnknown, true); got != splitbrain.ActionUnknown {
		t.Errorf("Decide(Unknown, leader) = %s, want Unknown", got)
	}
	if got := splitbrain.Decide(splitbrain.PartitionUnknown, false); got != splitbrain.ActionUnknown {
		t.Errorf("Decide(Unknown, follower) = %s, want Unknown", got)
	}
}
