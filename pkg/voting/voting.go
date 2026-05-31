// Package voting provides split-brain-safe quorum decisions for Helix Cluster OS
// via largest-subcluster (majority) voting.
//
// After a network partition, a cluster may fracture into several disjoint
// subclusters. To avoid split-brain — where two subclusters each believe they
// are authoritative and accept conflicting writes — at most ONE subcluster may
// continue operating as primary. This package implements the canonical rule: a
// node may lead the primary subcluster only if the members it can currently
// reach (including itself) form a strict majority of the full known member set,
// i.e. reachable >= floor(N/2)+1. Nodes that cannot prove majority MUST
// self-fence (stop accepting writes).
//
// Even splits (e.g. a 2-2 partition of a 4-node cluster, where no side holds a
// strict majority) are resolved by an optional witness or deterministic
// tiebreak so that exactly one side wins and the other self-fences.
package voting

import (
	"errors"
	"sort"
)

// ErrSelfNotReachable is returned by Decide when the reachable set does not
// include the local node. A node must always be able to reach itself; a missing
// self is a caller bug (the reachable set is meant to be observed from THIS
// node), not a partition condition.
var ErrSelfNotReachable = errors.New("voting: local node absent from its own reachable set")

// ErrSelfNotMember is returned by Decide when the local node is not part of the
// full known member set. Voting is only meaningful for a member of the cluster.
var ErrSelfNotMember = errors.New("voting: local node absent from the known member set")

// Reason explains, in machine-readable form, WHY a Decision came out the way it
// did. Each value maps to exactly one branch of the quorum rule.
type Reason string

const (
	// ReasonHasMajority: the reachable set is a strict majority
	// (reachable >= floor(N/2)+1). This node may lead the primary.
	ReasonHasMajority Reason = "has-majority"

	// ReasonLostQuorum: the reachable set is strictly less than half the
	// cluster (reachable < floor(N/2)+1 and not a tie). This node must
	// self-fence.
	ReasonLostQuorum Reason = "lost-quorum"

	// ReasonTieWon: an even split with no strict majority on either side, but
	// the configured tiebreak (witness vote or deterministic lowest-ID anchor)
	// resolved in this node's favor. This node may lead the primary.
	ReasonTieWon Reason = "tie-won"

	// ReasonTieLost: an even split with no strict majority, and the tiebreak
	// resolved against this node. This node must self-fence.
	ReasonTieLost Reason = "tie-lost"
)

// Decision is the immutable result of a quorum evaluation.
type Decision struct {
	// CanLeadPrimary is true iff this node is permitted to operate as primary.
	// It is true ONLY for ReasonHasMajority and ReasonTieWon.
	CanLeadPrimary bool

	// Reason is the machine-readable justification for CanLeadPrimary.
	Reason Reason

	// Reachable is the count of distinct reachable members (including self)
	// that was used for the decision.
	Reachable int

	// Total is the size of the full known member set (N) used for the decision.
	Total int

	// Quorum is the strict-majority threshold floor(N/2)+1 used for the
	// decision. CanLeadPrimary via majority requires Reachable >= Quorum.
	Quorum int
}

// TiebreakMode selects how even splits (no strict majority) are resolved.
type TiebreakMode int

const (
	// TiebreakNone disables tiebreaking: an even split with no strict majority
	// always loses quorum (every side self-fences). This is the safest mode —
	// it can never cause split-brain — at the cost of full unavailability on a
	// perfectly even partition.
	TiebreakNone TiebreakMode = iota

	// TiebreakLowestID resolves an even split deterministically: the side that
	// contains the globally lowest member ID (by lexicographic order over the
	// full member set) wins. Because the lowest ID exists in exactly one side
	// of any partition, exactly one side wins.
	TiebreakLowestID

	// TiebreakWitness resolves an even split using a configured witness member.
	// The side that can reach the witness wins. The witness acts as a tie-
	// breaking vote. Because the witness lies in exactly one side of a
	// partition, exactly one side wins.
	TiebreakWitness
)

// Config configures a QuorumDecider.
type Config struct {
	// LocalID is the ID of THIS node. It must appear in Members. Required.
	LocalID string

	// Members is the full known member set (N nodes), by stable ID. Duplicates
	// are de-duplicated. Must be non-empty and must contain LocalID.
	Members []string

	// Tiebreak selects even-split resolution. Defaults to TiebreakNone.
	Tiebreak TiebreakMode

	// WitnessID is the witness member used when Tiebreak == TiebreakWitness.
	// It SHOULD be a member ID (typically a lightweight arbiter). Ignored for
	// other tiebreak modes.
	WitnessID string
}

// QuorumDecider evaluates split-brain-safe quorum decisions for a fixed local
// node and known member set. It is safe for concurrent use: Decide takes no
// locks because the decider's configuration is immutable after construction and
// every call operates only on its arguments and read-only config.
type QuorumDecider struct {
	localID  string
	members  []string // sorted, de-duplicated
	memberOf map[string]struct{}
	tiebreak TiebreakMode
	witness  string
}

// NewQuorumDecider validates cfg and returns a ready decider. It returns an
// error for an empty member set, a LocalID not in Members, or a witness
// tiebreak with no witness configured.
func NewQuorumDecider(cfg Config) (*QuorumDecider, error) {
	if cfg.LocalID == "" {
		return nil, errors.New("voting: LocalID is required")
	}
	if len(cfg.Members) == 0 {
		return nil, errors.New("voting: Members must be non-empty")
	}

	set := make(map[string]struct{}, len(cfg.Members))
	for _, m := range cfg.Members {
		if m == "" {
			return nil, errors.New("voting: Members must not contain an empty ID")
		}
		set[m] = struct{}{}
	}
	if _, ok := set[cfg.LocalID]; !ok {
		return nil, ErrSelfNotMember
	}
	if cfg.Tiebreak == TiebreakWitness {
		if cfg.WitnessID == "" {
			return nil, errors.New("voting: TiebreakWitness requires WitnessID")
		}
		if _, ok := set[cfg.WitnessID]; !ok {
			return nil, errors.New("voting: WitnessID must be a known member")
		}
	}

	uniq := make([]string, 0, len(set))
	for m := range set {
		uniq = append(uniq, m)
	}
	sort.Strings(uniq)

	return &QuorumDecider{
		localID:  cfg.LocalID,
		members:  uniq,
		memberOf: set,
		tiebreak: cfg.Tiebreak,
		witness:  cfg.WitnessID,
	}, nil
}

// Quorum returns the strict-majority threshold floor(N/2)+1 for the configured
// member set. A reachable count >= Quorum is a strict majority.
func (d *QuorumDecider) Quorum() int {
	return len(d.members)/2 + 1
}

// Total returns N, the size of the known member set.
func (d *QuorumDecider) Total() int { return len(d.members) }

// CanLeadPrimary is a convenience wrapper over Decide that returns only the
// boolean verdict. It returns false on any error.
func (d *QuorumDecider) CanLeadPrimary(reachable []string) bool {
	dec, err := d.Decide(reachable)
	if err != nil {
		return false
	}
	return dec.CanLeadPrimary
}

// Decide evaluates whether THIS node may lead the primary subcluster given the
// set of members it can currently reach. reachable is interpreted from the
// local node's vantage point and MUST include LocalID. Members in reachable
// that are not in the known member set are ignored (a stale/foreign node cannot
// contribute to quorum). Duplicates are counted once.
//
// The rule:
//   - reachable >= floor(N/2)+1  -> CanLeadPrimary=true, ReasonHasMajority
//   - exactly half (even N, no majority) -> tiebreak:
//     win  -> CanLeadPrimary=true,  ReasonTieWon
//     lose -> CanLeadPrimary=false, ReasonTieLost
//   - otherwise -> CanLeadPrimary=false, ReasonLostQuorum
func (d *QuorumDecider) Decide(reachable []string) (Decision, error) {
	n := len(d.members)
	quorum := d.Quorum()

	// Count distinct reachable members that are actually part of the cluster.
	seen := make(map[string]struct{}, len(reachable))
	selfReachable := false
	count := 0
	for _, r := range reachable {
		if r == d.localID {
			selfReachable = true
		}
		if _, ok := d.memberOf[r]; !ok {
			continue // foreign/stale node: cannot vote.
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		count++
	}
	if !selfReachable {
		return Decision{}, ErrSelfNotReachable
	}

	base := Decision{Reachable: count, Total: n, Quorum: quorum}

	// Strict majority.
	if count >= quorum {
		base.CanLeadPrimary = true
		base.Reason = ReasonHasMajority
		return base, nil
	}

	// Even split with exactly half the cluster reachable (only possible when N
	// is even and count == N/2). No strict majority on either side, so the
	// tiebreak decides. Any count strictly below N/2 has decisively lost.
	if n%2 == 0 && count == n/2 {
		if d.tiebreakWins(seen) {
			base.CanLeadPrimary = true
			base.Reason = ReasonTieWon
			return base, nil
		}
		base.CanLeadPrimary = false
		base.Reason = ReasonTieLost
		return base, nil
	}

	base.CanLeadPrimary = false
	base.Reason = ReasonLostQuorum
	return base, nil
}

// tiebreakWins reports whether this node's reachable side wins an even split.
// reachableSet is the set of in-cluster members this node can reach (including
// self).
func (d *QuorumDecider) tiebreakWins(reachableSet map[string]struct{}) bool {
	switch d.tiebreak {
	case TiebreakLowestID:
		// members is sorted ascending; the globally lowest ID is members[0].
		// Exactly one side of the partition holds it, so exactly one side wins.
		_, ok := reachableSet[d.members[0]]
		return ok
	case TiebreakWitness:
		// The side that can reach the configured witness wins. The witness sits
		// in exactly one side of the partition, so exactly one side wins.
		_, ok := reachableSet[d.witness]
		return ok
	default: // TiebreakNone
		return false
	}
}
