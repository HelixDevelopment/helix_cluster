package splitbrain_test

import (
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/splitbrain"
)

// This file proves the load-bearing split-brain SAFETY contract by modelling a
// real network partition that cleaves a cluster of N nodes into two disjoint,
// complementary sides A and B (sizeA + sizeB == N). Each side can only reach the
// nodes on its own side of the cut, so it calls Classify(sideSize, N, witness).
//
// The existing classification_test.go is a per-call truth table for Classify in
// isolation; it never wires two complementary sides of one partition together and
// asserts the cross-side invariant. That cross-side invariant — "at most one side
// may proceed" — is the actual split-brain safety property and is what these tests
// add. A bluff implementation that always returned (HasQuorum, ContinueWrites)
// would pass a naive single-side check but is destroyed by the at-most-one
// invariant below, because it would let BOTH sides proceed.

// sideAuthoritative reports whether a partition side of the given size, in a
// cluster of total nodes, is permitted to make progress (accept writes). A side
// is authoritative iff Classify grants it ContinueWrites. holdsWitness models a
// physical tiebreaker/arbiter node that lives on exactly ONE side of the cut.
func sideAuthoritative(t *testing.T, sideSize, total int, holdsWitness bool) bool {
	t.Helper()
	part, action := splitbrain.Classify(sideSize, total, holdsWitness)
	// Sanity: ContinueWrites must never be paired with a non-progress partition.
	if action == splitbrain.ActionContinueWrites &&
		part != splitbrain.PartitionHasQuorum && part != splitbrain.PartitionTie {
		t.Fatalf("inconsistent: side %d-of-%d got ContinueWrites with partition %s",
			sideSize, total, part)
	}
	return action == splitbrain.ActionContinueWrites
}

// TestPartition_MajorityProceedsMinorityStops is the headline scenario: for several
// odd clusters and split ratios, the strict-majority side proceeds and the minority
// side must NOT (it goes read-only / steps down). No witness in play (odd N has no
// tie).
func TestPartition_MajorityProceedsMinorityStops(t *testing.T) {
	type split struct {
		total    int
		majority int // size of the larger side
		minority int // size of the smaller side
	}
	splits := []split{
		{total: 5, majority: 3, minority: 2}, // 3|2
		{total: 5, majority: 4, minority: 1}, // 4|1
		{total: 3, majority: 2, minority: 1}, // 2|1
		{total: 7, majority: 4, minority: 3}, // 4|3
		{total: 7, majority: 5, minority: 2}, // 5|2
		{total: 7, majority: 6, minority: 1}, // 6|1
		{total: 9, majority: 5, minority: 4}, // 5|4 (closest odd split)
	}

	for _, s := range splits {
		s := s
		if s.majority+s.minority != s.total {
			t.Fatalf("bad test data: %d+%d != %d", s.majority, s.minority, s.total)
		}

		// No witness on either side (odd cluster: a strict majority always exists).
		majAuth := sideAuthoritative(t, s.majority, s.total, false)
		minAuth := sideAuthoritative(t, s.minority, s.total, false)

		t.Logf("split %d|%d of N=%d : majority(%d) authoritative=%v, minority(%d) authoritative=%v",
			s.majority, s.minority, s.total, s.majority, majAuth, s.minority, minAuth)

		if !majAuth {
			t.Errorf("N=%d split %d|%d: MAJORITY side (%d nodes) must be allowed to continue, but it was NOT",
				s.total, s.majority, s.minority, s.majority)
		}
		if minAuth {
			t.Errorf("N=%d split %d|%d: MINORITY side (%d nodes) must STOP (read-only/step-down), but it was allowed to continue — SPLIT-BRAIN",
				s.total, s.majority, s.minority, s.minority)
		}

		// And a leader on the minority side must actively step down, not just read-only.
		minPart, _ := splitbrain.Classify(s.minority, s.total, false)
		if got := splitbrain.Decide(minPart, true); got != splitbrain.ActionStepDown {
			t.Errorf("N=%d: a LEADER stranded on the minority side (%d nodes) must StepDown, got %s",
				s.total, s.minority, got)
		}
	}
}

// TestPartition_AtMostOneAuthoritative is the core safety invariant and the primary
// anti-bluff test. It enumerates EVERY way to cut a cluster of N into two
// complementary sides (sizeA from 0..N, sizeB = N-sizeA), for a range of N, and
// asserts it is NEVER the case that BOTH sides are simultaneously authoritative.
//
// Why this bites a bluff: a fake Classify that ignores its inputs and always returns
// (HasQuorum, ContinueWrites) would make BOTH sides of, say, a 3|2 cut return true,
// failing this invariant on the very first split. There is no way to satisfy
// "at most one side proceeds" across all cuts without genuinely comparing the side
// size against the cluster total.
func TestPartition_AtMostOneAuthoritative(t *testing.T) {
	for total := 1; total <= 12; total++ {
		authoritativeSplits := 0
		bothAuthoritative := 0

		for a := 0; a <= total; a++ {
			b := total - a // complementary side

			// No-witness world first: this is the pure-quorum guarantee.
			aAuth := sideAuthoritative(t, a, total, false)
			bAuth := sideAuthoritative(t, b, total, false)

			if aAuth && bAuth {
				bothAuthoritative++
				t.Errorf("N=%d split %d|%d: BOTH sides authoritative — SPLIT-BRAIN, safety invariant violated",
					total, a, b)
			}
			if aAuth || bAuth {
				authoritativeSplits++
			}
		}

		t.Logf("N=%2d : enumerated %d complementary cuts, both-authoritative=%d (must be 0), at-least-one-authoritative cuts=%d",
			total, total+1, bothAuthoritative, authoritativeSplits)
	}
}

// TestPartition_WitnessGivesExactlyOneSide proves the tiebreaker model for EVEN N
// at the exact 50/50 cut (N/2 | N/2). Without a witness, NEITHER side may proceed
// (no strict majority => both read-only, cluster correctly unavailable). With the
// witness placed on exactly ONE physical side, EXACTLY that side proceeds and the
// other does not — still at most one authoritative side, never two.
func TestPartition_WitnessGivesExactlyOneSide(t *testing.T) {
	for _, total := range []int{2, 4, 6, 8} {
		half := total / 2

		// --- No witness: a 50/50 cut must strand BOTH sides (read-only). ---
		aAuthNo := sideAuthoritative(t, half, total, false)
		bAuthNo := sideAuthoritative(t, half, total, false)
		if aAuthNo || bAuthNo {
			t.Errorf("N=%d even split %d|%d with NO witness: neither side may proceed, got a=%v b=%v",
				total, half, half, aAuthNo, bAuthNo)
		}
		// Confirm the partition state is precisely Tie (not a mislabelled majority).
		if part, _ := splitbrain.Classify(half, total, false); part != splitbrain.PartitionTie {
			t.Errorf("N=%d: %d-of-%d must classify as Tie, got %s", total, half, total, part)
		}

		// --- Witness on side A only: A proceeds, B does not. ---
		aAuthW := sideAuthoritative(t, half, total, true /* A holds witness */)
		bAuthW := sideAuthoritative(t, half, total, false /* B has no witness */)

		t.Logf("N=%2d even split %d|%d : no-witness(A=%v,B=%v) ; witness-on-A(A=%v,B=%v)",
			total, half, half, aAuthNo, bAuthNo, aAuthW, bAuthW)

		if !aAuthW {
			t.Errorf("N=%d even split %d|%d: witness-holding side A must proceed, but it did NOT", total, half, half)
		}
		if bAuthW {
			t.Errorf("N=%d even split %d|%d: non-witness side B must NOT proceed — SPLIT-BRAIN via double-tiebreak", total, half, half)
		}
		// The whole point: still at most one authoritative side.
		if aAuthW && bAuthW {
			t.Errorf("N=%d: witness must not authorize BOTH sides of a tie", total)
		}
	}
}

// TestPartition_WitnessNeverCreatesTwoAuthoritative hardens the witness model against
// the worst-case bluff for tiebreakers: it enumerates every even-N tie cut and tries
// EVERY witness placement (on A, on B, on neither). A correct implementation must
// never let a single witness make both sides authoritative; at most one side wins,
// and only the side actually holding the witness.
func TestPartition_WitnessNeverCreatesTwoAuthoritative(t *testing.T) {
	for _, total := range []int{2, 4, 6, 8, 10} {
		half := total / 2

		// Witness placements: {A,B}. A real arbiter is a single node => it is on
		// at most one side. We therefore test (A=true,B=false), (A=false,B=true),
		// and (A=false,B=false). We deliberately do NOT test (true,true): a single
		// physical witness cannot be on both sides of a cut, and asserting that
		// impossible input is "safe" would be the bluff.
		placements := []struct {
			name      string
			witnessA  bool
			witnessB  bool
			wantAAuth bool
			wantBAuth bool
		}{
			{"witness-on-A", true, false, true, false},
			{"witness-on-B", false, true, false, true},
			{"no-witness", false, false, false, false},
		}

		for _, p := range placements {
			aAuth := sideAuthoritative(t, half, total, p.witnessA)
			bAuth := sideAuthoritative(t, half, total, p.witnessB)

			if aAuth && bAuth {
				t.Errorf("N=%d %s: BOTH sides authoritative — tiebreaker created split-brain", total, p.name)
			}
			if aAuth != p.wantAAuth || bAuth != p.wantBAuth {
				t.Errorf("N=%d %s: got (A=%v,B=%v), want (A=%v,B=%v)",
					total, p.name, aAuth, bAuth, p.wantAAuth, p.wantBAuth)
			}
		}
		t.Logf("N=%2d tie %d|%d : every legal witness placement yields at most one authoritative side", total, half, half)
	}
}

// TestPartition_FullClusterAndTotalLoss covers the two degenerate ends:
//   - No partition at all (the whole cluster is intact) => the cluster proceeds.
//   - A single node isolated from a multi-node cluster => it must NOT proceed.
func TestPartition_FullClusterAndTotalLoss(t *testing.T) {
	for _, total := range []int{1, 3, 4, 5, 7, 8} {
		// Intact cluster: every node reachable.
		if !sideAuthoritative(t, total, total, false) {
			t.Errorf("N=%d intact cluster (all %d reachable) must proceed, but it did NOT", total, total)
		}

		if total >= 2 {
			// One node, isolated from the rest: reachable==1 of total>=2.
			isolatedAuth := sideAuthoritative(t, 1, total, false)
			if isolatedAuth {
				t.Errorf("N=%d: a single isolated node must NOT proceed, but it was authoritative — SPLIT-BRAIN", total)
			}
			// And in a cluster of >=3 it is strictly a minority, not a tie.
			if total >= 3 {
				if part, _ := splitbrain.Classify(1, total, false); part != splitbrain.PartitionMinority {
					t.Errorf("N=%d: a single isolated node must be Minority, got %s", total, part)
				}
			}
			t.Logf("N=%2d : intact-cluster proceeds=%v ; single-isolated-node proceeds=%v (want false)",
				total, true, isolatedAuth)
		}
	}

	// A genuinely-down node (reachable==0) in a multi-node cluster must be read-only,
	// never authoritative.
	for _, total := range []int{2, 3, 5} {
		if sideAuthoritative(t, 0, total, false) {
			t.Errorf("N=%d: a node reaching ZERO peers must not proceed (total isolation)", total)
		}
	}
}
