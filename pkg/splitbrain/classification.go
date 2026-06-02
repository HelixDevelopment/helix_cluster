// Package splitbrain implements split-brain prevention and partition classification
// for distributed consensus clusters (HXC-1373).
//
// Quorum arithmetic uses the 2*reachable vs total comparison to avoid integer-division
// rounding bugs (e.g. 2-of-4 is a Tie, not a false majority).
package splitbrain

import "fmt"

// Partition describes the quorum state of the local partition.
type Partition int

const (
	// PartitionUnknown is returned when inputs are invalid.
	PartitionUnknown Partition = iota

	// PartitionHasQuorum means the local partition can see a strict majority of nodes.
	PartitionHasQuorum

	// PartitionMinority means the local partition can see fewer than half of the nodes.
	PartitionMinority

	// PartitionTie means the local partition can see exactly half of the nodes.
	// Tie-breaking requires an external witness.
	PartitionTie

	// PartitionTotalIsolation means the local node can reach no peers at all.
	PartitionTotalIsolation
)

// String returns a human-readable name for the partition state.
func (p Partition) String() string {
	switch p {
	case PartitionHasQuorum:
		return "HasQuorum"
	case PartitionMinority:
		return "Minority"
	case PartitionTie:
		return "Tie"
	case PartitionTotalIsolation:
		return "TotalIsolation"
	default:
		return fmt.Sprintf("Unknown(%d)", int(p))
	}
}

// Action is the recommended action for the local node given its partition state.
type Action int

const (
	// ActionUnknown is returned when inputs are invalid.
	ActionUnknown Action = iota

	// ActionContinueWrites means the node may continue accepting write requests.
	ActionContinueWrites

	// ActionReadOnly means the node must reject writes and serve reads only.
	ActionReadOnly

	// ActionStepDown means a leader must abdicate and transition to follower/read-only.
	// Used by Decide when a leader finds itself in a non-quorum partition.
	ActionStepDown
)

// String returns a human-readable name for the action.
func (a Action) String() string {
	switch a {
	case ActionContinueWrites:
		return "ContinueWrites"
	case ActionReadOnly:
		return "ReadOnly"
	case ActionStepDown:
		return "StepDown"
	default:
		return fmt.Sprintf("Unknown(%d)", int(a))
	}
}

// Classify determines the partition state and recommended action for a node that can
// reach reachable peers out of a cluster of total nodes (including itself).
//
// hasWitness is true when an external tiebreaker (witness / arbiter node) has been
// consulted and it endorses this partition as the authoritative side.
//
// Quorum arithmetic deliberately uses 2*reachable compared against total to avoid
// integer-division truncation — e.g. floor(4/2)==2 would make 2-of-4 appear as a
// majority, but 2*2==4==total correctly identifies it as a Tie.
//
// Rules:
//
//	reachable < 0 || total <= 0 || reachable > total  => (Unknown, Unknown)
//	reachable == 0                                     => (TotalIsolation, ReadOnly)
//	2*reachable  > total                               => (HasQuorum,  ContinueWrites)
//	2*reachable == total                               => (Tie,        ContinueWrites if hasWitness else ReadOnly)
//	2*reachable  < total                               => (Minority,   ReadOnly)
func Classify(reachable, total int, hasWitness bool) (Partition, Action) {
	// Validate inputs.
	if reachable < 0 || total <= 0 || reachable > total {
		return PartitionUnknown, ActionUnknown
	}

	// Complete isolation: the node cannot talk to any peer.
	if reachable == 0 {
		return PartitionTotalIsolation, ActionReadOnly
	}

	// Use multiplication to avoid integer-division rounding.
	twice := 2 * reachable

	switch {
	case twice > total:
		return PartitionHasQuorum, ActionContinueWrites

	case twice == total:
		if hasWitness {
			return PartitionTie, ActionContinueWrites
		}
		return PartitionTie, ActionReadOnly

	default: // twice < total
		return PartitionMinority, ActionReadOnly
	}
}

// Decide maps a partition classification to a concrete action, taking the local
// node's leadership status into account.
//
// When the node is a leader and the partition does not grant quorum (i.e. the
// recommended action is ReadOnly), the leader must actively step down so that a
// majority partition can elect a new leader without waiting for the old one to
// time out. This avoids prolonged unavailability and prevents a stale leader from
// serving divergent reads.
//
// Mapping:
//
//	isLeader && action == ReadOnly  => StepDown
//	otherwise                       => action (unchanged)
func Decide(partition Partition, isLeader bool) Action {
	_ = partition // partition is passed for future extensibility / logging

	switch partition {
	case PartitionUnknown:
		return ActionUnknown
	case PartitionHasQuorum:
		return ActionContinueWrites
	default:
		// Minority, Tie (no witness), TotalIsolation.
		if isLeader {
			return ActionStepDown
		}
		return ActionReadOnly
	}
}
