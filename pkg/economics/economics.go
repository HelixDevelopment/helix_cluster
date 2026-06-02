// Package economics implements participant reward distribution and ROI
// reporting for the Helix Cluster OS economic layer.
//
// It covers HXC-1460 (RewardDistributor multi-token distribution with a
// conservation invariant) and HXC-1461 (per-participant ROI and break-even).
package economics

import (
	"errors"
	"fmt"
	"math"
)

// conservationEpsilon bounds the acceptable float drift for the
// distribution conservation invariant.
const conservationEpsilon = 1e-9

// ErrUnknownParticipant is returned when a participant is not present in the
// supplied balances map.
var ErrUnknownParticipant = errors.New("economics: unknown participant")

// DistributionRule describes how gross earnings are split before the
// remainder is distributed pro-rata to participants. Cuts are fractions in
// [0,1) and their sum must be strictly less than 1 so that a positive
// distributable remainder always exists.
type DistributionRule struct {
	TreasuryCut float64
	ReinvestCut float64
}

// Validate reports whether the rule is well formed.
func (r DistributionRule) Validate() error {
	if math.IsNaN(r.TreasuryCut) || math.IsInf(r.TreasuryCut, 0) {
		return fmt.Errorf("economics: treasury cut is not a finite number")
	}
	if math.IsNaN(r.ReinvestCut) || math.IsInf(r.ReinvestCut, 0) {
		return fmt.Errorf("economics: reinvest cut is not a finite number")
	}
	if r.TreasuryCut < 0 || r.TreasuryCut >= 1 {
		return fmt.Errorf("economics: treasury cut %v out of range [0,1)", r.TreasuryCut)
	}
	if r.ReinvestCut < 0 || r.ReinvestCut >= 1 {
		return fmt.Errorf("economics: reinvest cut %v out of range [0,1)", r.ReinvestCut)
	}
	if r.TreasuryCut+r.ReinvestCut >= 1 {
		return fmt.Errorf("economics: combined cuts %v must be < 1", r.TreasuryCut+r.ReinvestCut)
	}
	return nil
}

// Distribution is the result of splitting gross earnings. The conservation
// invariant guarantees that the sum of all Ledger values plus Treasury plus
// Reinvest equals Total within conservationEpsilon.
type Distribution struct {
	Ledger   map[string]float64
	Treasury float64
	Reinvest float64
	Total    float64
}

// ROIReport summarizes return-on-investment for a single participant.
type ROIReport struct {
	ROIPercent    float64
	BreakEvenDays float64
}

// RewardDistributor computes reward splits and ROI reports.
type RewardDistributor struct{}

// NewRewardDistributor constructs a RewardDistributor.
func NewRewardDistributor() *RewardDistributor {
	return &RewardDistributor{}
}

// Distribute splits gross earnings according to rule.
//
//	total         = sum(earnings)
//	treasury      = total * rule.TreasuryCut
//	reinvest      = total * rule.ReinvestCut
//	distributable = total - treasury - reinvest
//	ledger[p]     = distributable * earnings[p] / total
//
// The conservation invariant sum(ledger) + treasury + reinvest == total is
// guaranteed within conservationEpsilon.
func (d *RewardDistributor) Distribute(earnings map[string]float64, rule DistributionRule) (Distribution, error) {
	if err := rule.Validate(); err != nil {
		return Distribution{}, err
	}
	if len(earnings) == 0 {
		return Distribution{}, fmt.Errorf("economics: earnings must be non-empty")
	}

	var total float64
	for p, e := range earnings {
		if math.IsNaN(e) || math.IsInf(e, 0) {
			return Distribution{}, fmt.Errorf("economics: earnings for %q is not finite", p)
		}
		if e < 0 {
			return Distribution{}, fmt.Errorf("economics: negative earnings %v for %q", e, p)
		}
		total += e
	}
	if total <= 0 {
		return Distribution{}, fmt.Errorf("economics: total earnings must be positive")
	}

	treasury := total * rule.TreasuryCut
	reinvest := total * rule.ReinvestCut
	// LOAD-BEARING GUARD: the treasury cut MUST be subtracted here. If this
	// subtraction is dropped (distributable = total - reinvest), the ledger
	// over-allocates and sum(ledger)+treasury+reinvest exceeds total, which
	// TestDistribute_Conservation detects.
	distributable := total - treasury - reinvest

	ledger := make(map[string]float64, len(earnings))
	for p, e := range earnings {
		ledger[p] = distributable * e / total
	}

	return Distribution{
		Ledger:   ledger,
		Treasury: treasury,
		Reinvest: reinvest,
		Total:    total,
	}, nil
}

// GetParticipantROI computes the ROI report for a single participant.
//
//	revenue       = balances[participant]
//	cost          = costs[participant]
//	ROIPercent    = (revenue - cost) / cost * 100
//	BreakEvenDays = cost / dailyNet
//
// If participant is absent from balances, ErrUnknownParticipant is returned.
func (d *RewardDistributor) GetParticipantROI(participant string, balances map[string]float64, costs map[string]float64, dailyNet float64) (ROIReport, error) {
	revenue, ok := balances[participant]
	if !ok {
		return ROIReport{}, fmt.Errorf("%w: %q", ErrUnknownParticipant, participant)
	}
	cost, ok := costs[participant]
	if !ok {
		return ROIReport{}, fmt.Errorf("economics: missing cost for participant %q", participant)
	}
	if cost == 0 {
		return ROIReport{}, fmt.Errorf("economics: zero cost for participant %q yields undefined ROI", participant)
	}
	if dailyNet == 0 {
		return ROIReport{}, fmt.Errorf("economics: zero daily net yields undefined break-even for %q", participant)
	}

	return ROIReport{
		ROIPercent:    (revenue - cost) / cost * 100,
		BreakEvenDays: cost / dailyNet,
	}, nil
}
