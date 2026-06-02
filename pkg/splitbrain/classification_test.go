package splitbrain_test

import (
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/splitbrain"
)

// classifyCase captures one Classify scenario.
type classifyCase struct {
	name       string
	reachable  int
	total      int
	hasWitness bool
	wantPart   splitbrain.Partition
	wantAction splitbrain.Action
}

func TestClassify(t *testing.T) {
	cases := []classifyCase{
		// ---------------------------------------------------------------
		// Core anti-bluff bites (HXC-1373)
		// ---------------------------------------------------------------

		// 3-of-5: 2*3=6 > 5 → HasQuorum / ContinueWrites
		{
			name:      "3-of-5 majority",
			reachable: 3, total: 5,
			wantPart: splitbrain.PartitionHasQuorum, wantAction: splitbrain.ActionContinueWrites,
		},

		// 2-of-5: 2*2=4 < 5 → Minority / ReadOnly
		// The integer-division trap: floor(5/2)=2; naive reachable >= total/2 would
		// mis-classify this as a Tie or quorum. We assert Minority, not Tie.
		{
			name:      "2-of-5 minority (NOT tie — integer-division trap)",
			reachable: 2, total: 5,
			wantPart: splitbrain.PartitionMinority, wantAction: splitbrain.ActionReadOnly,
		},

		// 2-of-4, no witness: 2*2=4 == 4 → Tie / ReadOnly
		// Mutation: flipping > to >= in the HasQuorum branch would make 2*2==4 fall
		// into HasQuorum instead of Tie — this test will catch that mutation.
		{
			name:      "2-of-4 tie no witness",
			reachable: 2, total: 4,
			wantPart: splitbrain.PartitionTie, wantAction: splitbrain.ActionReadOnly,
		},

		// 2-of-4, with witness: same Tie partition but witness promotes to ContinueWrites
		{
			name:      "2-of-4 tie with witness",
			reachable: 2, total: 4, hasWitness: true,
			wantPart: splitbrain.PartitionTie, wantAction: splitbrain.ActionContinueWrites,
		},

		// 0-of-5: total isolation
		{
			name:      "0-of-5 total isolation",
			reachable: 0, total: 5,
			wantPart: splitbrain.PartitionTotalIsolation, wantAction: splitbrain.ActionReadOnly,
		},

		// 5-of-5: all reachable → quorum
		{
			name:      "5-of-5 full cluster",
			reachable: 5, total: 5,
			wantPart: splitbrain.PartitionHasQuorum, wantAction: splitbrain.ActionContinueWrites,
		},

		// ---------------------------------------------------------------
		// Input validation
		// ---------------------------------------------------------------

		// reachable > total: invalid
		{
			name:      "invalid 6-of-5",
			reachable: 6, total: 5,
			wantPart: splitbrain.PartitionUnknown, wantAction: splitbrain.ActionUnknown,
		},

		// total == 0: invalid (division by zero territory)
		{
			name:      "invalid total=0",
			reachable: 0, total: 0,
			wantPart: splitbrain.PartitionUnknown, wantAction: splitbrain.ActionUnknown,
		},

		// reachable < 0: invalid
		{
			name:      "invalid negative reachable",
			reachable: -1, total: 5,
			wantPart: splitbrain.PartitionUnknown, wantAction: splitbrain.ActionUnknown,
		},

		// total < 0: invalid
		{
			name:      "invalid negative total",
			reachable: 1, total: -3,
			wantPart: splitbrain.PartitionUnknown, wantAction: splitbrain.ActionUnknown,
		},

		// ---------------------------------------------------------------
		// Additional coverage
		// ---------------------------------------------------------------

		// 1-of-3: 2*1=2 < 3 → Minority
		{
			name:      "1-of-3 minority",
			reachable: 1, total: 3,
			wantPart: splitbrain.PartitionMinority, wantAction: splitbrain.ActionReadOnly,
		},

		// 2-of-3: 2*2=4 > 3 → HasQuorum
		{
			name:      "2-of-3 quorum",
			reachable: 2, total: 3,
			wantPart: splitbrain.PartitionHasQuorum, wantAction: splitbrain.ActionContinueWrites,
		},

		// 1-of-2 tie no witness: 2*1=2 == 2
		{
			name:      "1-of-2 tie no witness",
			reachable: 1, total: 2,
			wantPart: splitbrain.PartitionTie, wantAction: splitbrain.ActionReadOnly,
		},

		// 1-of-2 tie with witness
		{
			name:      "1-of-2 tie with witness",
			reachable: 1, total: 2, hasWitness: true,
			wantPart: splitbrain.PartitionTie, wantAction: splitbrain.ActionContinueWrites,
		},

		// Single-node cluster: 1-of-1 → quorum (edge case, no peers needed)
		{
			name:      "1-of-1 single node",
			reachable: 1, total: 1,
			wantPart: splitbrain.PartitionHasQuorum, wantAction: splitbrain.ActionContinueWrites,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gotPart, gotAction := splitbrain.Classify(tc.reachable, tc.total, tc.hasWitness)

			if gotPart != tc.wantPart {
				t.Errorf("Classify(%d, %d, witness=%v): Partition = %s, want %s",
					tc.reachable, tc.total, tc.hasWitness,
					gotPart, tc.wantPart)
			}
			if gotAction != tc.wantAction {
				t.Errorf("Classify(%d, %d, witness=%v): Action = %s, want %s",
					tc.reachable, tc.total, tc.hasWitness,
					gotAction, tc.wantAction)
			}
		})
	}
}

// TestClassify_IntegerDivisionTrap explicitly documents and guards the 2-of-5 / 2-of-4
// contrast that catches the naive `reachable >= total/2` implementation.
func TestClassify_IntegerDivisionTrap(t *testing.T) {
	// A naive implementation using integer division: total/2 == 2 for both 4 and 5.
	// floor(5/2)=2 would make reachable==2 look like a tie or quorum for a 5-node cluster.
	// Our implementation must give Minority for 2-of-5 and Tie for 2-of-4.

	part25, action25 := splitbrain.Classify(2, 5, false)
	if part25 != splitbrain.PartitionMinority {
		t.Errorf("2-of-5 must be Minority, got %s (integer-division bug?)", part25)
	}
	if action25 != splitbrain.ActionReadOnly {
		t.Errorf("2-of-5 action must be ReadOnly, got %s", action25)
	}

	part24, action24 := splitbrain.Classify(2, 4, false)
	if part24 != splitbrain.PartitionTie {
		t.Errorf("2-of-4 must be Tie, got %s (integer-division bug?)", part24)
	}
	if action24 != splitbrain.ActionReadOnly {
		t.Errorf("2-of-4 action must be ReadOnly, got %s", action24)
	}
}

// TestDecide verifies that a leader steps down when the partition is non-quorum,
// and that followers simply get ReadOnly in the same scenario.
func TestDecide(t *testing.T) {
	decCases := []struct {
		name      string
		partition splitbrain.Partition
		isLeader  bool
		want      splitbrain.Action
	}{
		{"leader with quorum continues writes", splitbrain.PartitionHasQuorum, true, splitbrain.ActionContinueWrites},
		{"follower with quorum continues writes", splitbrain.PartitionHasQuorum, false, splitbrain.ActionContinueWrites},
		{"leader in minority steps down", splitbrain.PartitionMinority, true, splitbrain.ActionStepDown},
		{"follower in minority goes read-only", splitbrain.PartitionMinority, false, splitbrain.ActionReadOnly},
		{"leader in tie steps down", splitbrain.PartitionTie, true, splitbrain.ActionStepDown},
		{"follower in tie goes read-only", splitbrain.PartitionTie, false, splitbrain.ActionReadOnly},
		{"leader isolated steps down", splitbrain.PartitionTotalIsolation, true, splitbrain.ActionStepDown},
		{"follower isolated goes read-only", splitbrain.PartitionTotalIsolation, false, splitbrain.ActionReadOnly},
		{"unknown partition unknown action", splitbrain.PartitionUnknown, true, splitbrain.ActionUnknown},
		{"unknown partition follower unknown action", splitbrain.PartitionUnknown, false, splitbrain.ActionUnknown},
	}

	for _, dc := range decCases {
		dc := dc
		t.Run(dc.name, func(t *testing.T) {
			got := splitbrain.Decide(dc.partition, dc.isLeader)
			if got != dc.want {
				t.Errorf("Decide(%s, leader=%v) = %s, want %s",
					dc.partition, dc.isLeader, got, dc.want)
			}
		})
	}
}

// TestPartitionString and TestActionString guard that String() methods
// produce meaningful, non-empty output for every defined constant.
func TestPartitionString(t *testing.T) {
	for _, p := range []splitbrain.Partition{
		splitbrain.PartitionUnknown,
		splitbrain.PartitionHasQuorum,
		splitbrain.PartitionMinority,
		splitbrain.PartitionTie,
		splitbrain.PartitionTotalIsolation,
	} {
		s := p.String()
		if s == "" {
			t.Errorf("Partition(%d).String() returned empty string", int(p))
		}
	}
}

func TestActionString(t *testing.T) {
	for _, a := range []splitbrain.Action{
		splitbrain.ActionUnknown,
		splitbrain.ActionContinueWrites,
		splitbrain.ActionReadOnly,
		splitbrain.ActionStepDown,
	} {
		s := a.String()
		if s == "" {
			t.Errorf("Action(%d).String() returned empty string", int(a))
		}
	}
}
