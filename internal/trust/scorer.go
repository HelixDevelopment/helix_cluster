// Package trust implements a BOINC-style trust and reliability scorer for
// volunteer / untrusted compute nodes.
//
// Each node carries two coupled quantities:
//
//   - credit: a monotonically-accumulated count of validated work units. Credit
//     only grows when a node's result survives quorum validation; it is never
//     spent. It is the "how much useful work has this node done" axis.
//
//   - reputation: a bounded reliability score in [0,1]. Reputation rises slowly
//     with each consecutive validated result (a streak) and drops *sharply* on a
//     single detected-bad / timed-out / abandoned result. The penalty is
//     multiplicative while recovery is additive, so the response is deliberately
//     asymmetric: one bad result costs many good results to rebuild. This mirrors
//     BOINC's adaptive replication, where a host must re-earn trust after an
//     invalid result.
//
// A node whose reputation sits below TrustFloor is flagged untrusted and its
// work should be replicated/validated more aggressively (or rejected).
package trust

import (
	"sort"
	"sync"
)

// Outcome describes how a single unit of work completed for a node.
type Outcome int

const (
	// OutcomeValid means the node's result was validated as correct (it agreed
	// with the accepted quorum value).
	OutcomeValid Outcome = iota
	// OutcomeInvalid means the node returned a result that disagreed with the
	// accepted quorum value (a detected-bad result).
	OutcomeInvalid
	// OutcomeTimeout means the node failed to return a result before its deadline.
	OutcomeTimeout
	// OutcomeAbandoned means the node accepted work and then never reported.
	OutcomeAbandoned
)

// good reports whether an outcome should be rewarded. Only OutcomeValid is good;
// every other outcome erodes trust.
func (o Outcome) good() bool { return o == OutcomeValid }

// Default tuning constants. They are exported as the zero-config defaults but
// every one is overridable via Config so the asymmetry can be tested directly.
const (
	// DefaultInitialReputation is the reputation a never-seen node starts at. New
	// volunteers are distrusted by default (below DefaultTrustFloor).
	DefaultInitialReputation = 0.10
	// DefaultRecoveryStep is the additive reputation gain per consecutive valid
	// result (before the streak bonus). Recovery is slow and additive.
	DefaultRecoveryStep = 0.10
	// DefaultStreakBonus is an extra additive gain multiplied by the current
	// streak length, so a sustained streak accelerates trust toward the cap.
	DefaultStreakBonus = 0.02
	// DefaultPenaltyFactor is the multiplicative survivor fraction of reputation
	// kept after a bad result. 0.25 means a bad result discards 75% of trust.
	DefaultPenaltyFactor = 0.25
	// DefaultPenaltyFloorDrop is an additional flat drop applied after the
	// multiplicative penalty, guaranteeing a *sharp* fall even from low scores.
	DefaultPenaltyFloorDrop = 0.05
	// DefaultTrustFloor is the reputation below which a node is flagged untrusted.
	DefaultTrustFloor = 0.50
	// DefaultMaxReputation caps reputation so a single node can never become
	// blindly trusted; replication is always at least partially warranted.
	DefaultMaxReputation = 0.99
)

// Config tunes the asymmetric scorer. A zero Config is invalid; use
// DefaultConfig and override fields as needed.
type Config struct {
	InitialReputation float64
	RecoveryStep      float64
	StreakBonus       float64
	PenaltyFactor     float64
	PenaltyFloorDrop  float64
	TrustFloor        float64
	MaxReputation     float64
}

// DefaultConfig returns the standard tuning.
func DefaultConfig() Config {
	return Config{
		InitialReputation: DefaultInitialReputation,
		RecoveryStep:      DefaultRecoveryStep,
		StreakBonus:       DefaultStreakBonus,
		PenaltyFactor:     DefaultPenaltyFactor,
		PenaltyFloorDrop:  DefaultPenaltyFloorDrop,
		TrustFloor:        DefaultTrustFloor,
		MaxReputation:     DefaultMaxReputation,
	}
}

// nodeState is the mutable per-node record.
type nodeState struct {
	reputation float64
	credit     int64
	streak     int   // consecutive valid results
	valid      int64 // lifetime valid count
	bad        int64 // lifetime non-valid count
}

// Scorer tracks per-node credit and reputation. It is safe for concurrent use.
type Scorer struct {
	cfg Config

	mu    sync.RWMutex
	nodes map[string]*nodeState
}

// NewScorer constructs a Scorer with the given config. Fields left at their
// zero value are filled from DefaultConfig so a partially-specified Config is
// still usable.
func NewScorer(cfg Config) *Scorer {
	d := DefaultConfig()
	if cfg.InitialReputation == 0 {
		cfg.InitialReputation = d.InitialReputation
	}
	if cfg.RecoveryStep == 0 {
		cfg.RecoveryStep = d.RecoveryStep
	}
	if cfg.StreakBonus == 0 {
		cfg.StreakBonus = d.StreakBonus
	}
	if cfg.PenaltyFactor == 0 {
		cfg.PenaltyFactor = d.PenaltyFactor
	}
	if cfg.PenaltyFloorDrop == 0 {
		cfg.PenaltyFloorDrop = d.PenaltyFloorDrop
	}
	if cfg.TrustFloor == 0 {
		cfg.TrustFloor = d.TrustFloor
	}
	if cfg.MaxReputation == 0 {
		cfg.MaxReputation = d.MaxReputation
	}
	return &Scorer{cfg: cfg, nodes: make(map[string]*nodeState)}
}

// NewDefaultScorer is a convenience constructor using DefaultConfig.
func NewDefaultScorer() *Scorer { return NewScorer(DefaultConfig()) }

// stateLocked returns (creating if needed) the state for node. Caller holds mu.
func (s *Scorer) stateLocked(node string) *nodeState {
	st, ok := s.nodes[node]
	if !ok {
		st = &nodeState{reputation: s.cfg.InitialReputation}
		s.nodes[node] = st
	}
	return st
}

// RecordResult applies one outcome to a node, updating its reputation, streak
// and (on valid results) its credit. It returns the node's reputation after the
// update.
//
// Reward path (OutcomeValid):
//
//	streak++ ; credit++ ; reputation += RecoveryStep + StreakBonus*streak
//	reputation is then clamped to MaxReputation.
//
// Penalty path (any non-valid outcome):
//
//	streak = 0
//	reputation = reputation*PenaltyFactor - PenaltyFloorDrop  (clamped at 0)
//
// The multiplicative-then-flat penalty guarantees a sharp drop, and resetting
// the streak removes any accumulated bonus so the node must rebuild from scratch.
func (s *Scorer) RecordResult(node string, outcome Outcome) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.stateLocked(node)

	if outcome.good() {
		st.streak++
		st.credit++
		st.valid++
		gain := s.cfg.RecoveryStep + s.cfg.StreakBonus*float64(st.streak)
		st.reputation += gain
		if st.reputation > s.cfg.MaxReputation {
			st.reputation = s.cfg.MaxReputation
		}
	} else {
		st.streak = 0
		st.bad++
		st.reputation = st.reputation*s.cfg.PenaltyFactor - s.cfg.PenaltyFloorDrop
		if st.reputation < 0 {
			st.reputation = 0
		}
	}
	return st.reputation
}

// Score returns the current reputation of a node in [0,1]. An unknown node
// reports the configured initial reputation without being created.
func (s *Scorer) Score(node string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.nodes[node]; ok {
		return st.reputation
	}
	return s.cfg.InitialReputation
}

// Credit returns the accumulated validated-work credit for a node.
func (s *Scorer) Credit(node string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.nodes[node]; ok {
		return st.credit
	}
	return 0
}

// Streak returns the current consecutive-valid streak for a node.
func (s *Scorer) Streak(node string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.nodes[node]; ok {
		return st.streak
	}
	return 0
}

// Trusted reports whether a node's reputation is at or above the trust floor.
func (s *Scorer) Trusted(node string) bool {
	return s.Score(node) >= s.cfg.TrustFloor
}

// TrustFloor returns the configured trust floor.
func (s *Scorer) TrustFloor() float64 { return s.cfg.TrustFloor }

// Result is a single node's submission for a work unit. Value is compared by
// equality to detect agreement; any comparable type works (hash digest string,
// int, etc.).
type Result struct {
	Node  string
	Value string
}

// Quorum is the outcome of redundancy validation.
type Quorum struct {
	// Accepted reports whether quorum (>= M agreeing nodes) was reached.
	Accepted bool
	// Value is the agreed-correct value when Accepted; empty otherwise.
	Value string
	// Agree lists the nodes that submitted the accepted value.
	Agree []string
	// Dissent lists the nodes that submitted any other value.
	Dissent []string
	// Votes is the agreeing-node count for the winning value.
	Votes int
}

// Validate performs BOINC-style redundant validation: a value is accepted only
// when at least m independent nodes submit it. The majority value wins; on a tie
// in vote count the lexicographically smallest value is chosen deterministically
// so the result is reproducible.
//
// Side effects on the scorer (sink-side trust adjustment):
//
//   - when quorum is reached, every agreeing node receives OutcomeValid and every
//     dissenting node receives OutcomeInvalid (its trust drops sharply);
//   - when quorum is NOT reached, no trust changes are applied (we cannot tell
//     who was right), and Accepted is false.
//
// Duplicate submissions from the same node for the same value are counted once.
func (s *Scorer) Validate(results []Result, m int) Quorum {
	if m < 1 {
		m = 1
	}

	// Tally distinct nodes per value (dedup per node+value).
	type valSet struct {
		nodes map[string]struct{}
		order []string
	}
	tally := make(map[string]*valSet)
	for _, r := range results {
		vs, ok := tally[r.Value]
		if !ok {
			vs = &valSet{nodes: make(map[string]struct{})}
			tally[r.Value] = vs
		}
		if _, seen := vs.nodes[r.Node]; !seen {
			vs.nodes[r.Node] = struct{}{}
			vs.order = append(vs.order, r.Node)
		}
	}

	// Pick the winning value: highest vote count, ties broken by smallest value.
	values := make([]string, 0, len(tally))
	for v := range tally {
		values = append(values, v)
	}
	sort.Strings(values)

	winner := ""
	winVotes := 0
	for _, v := range values {
		n := len(tally[v].nodes)
		if n > winVotes {
			winVotes = n
			winner = v
		}
	}

	q := Quorum{Value: winner, Votes: winVotes}
	if winVotes < m {
		// No quorum: report the plurality value/votes but accept nothing and
		// touch no trust scores.
		q.Accepted = false
		q.Value = ""
		return q
	}

	q.Accepted = true

	// Build agree/dissent node lists in first-seen order across the input. A node
	// that submitted the winning value (even among other submissions) counts as
	// an agreer; otherwise it is a dissenter. Each node is classified once.
	agreeSet := tally[winner].nodes
	classified := make(map[string]bool)
	for _, r := range results {
		if classified[r.Node] {
			continue
		}
		classified[r.Node] = true
		if _, ok := agreeSet[r.Node]; ok {
			q.Agree = append(q.Agree, r.Node)
		} else {
			q.Dissent = append(q.Dissent, r.Node)
		}
	}

	// Apply sink-side trust adjustments.
	for _, n := range q.Agree {
		s.RecordResult(n, OutcomeValid)
	}
	for _, n := range q.Dissent {
		s.RecordResult(n, OutcomeInvalid)
	}
	return q
}
