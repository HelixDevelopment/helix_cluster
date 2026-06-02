package economics

import (
	"errors"
	"math"
	"testing"
)

const epsilon = 1e-9

func approxEqual(a, b float64) bool { return math.Abs(a-b) <= epsilon }

// TestDistribute_Conservation verifies exact per-participant amounts, the
// treasury and reinvest cuts, and the conservation invariant. It is the
// load-bearing test: if the treasury-cut subtraction in Distribute is removed
// (distributable = total - reinvest), the ledger over-allocates and the
// conservation sum exceeds Total, failing this test.
func TestDistribute_Conservation(t *testing.T) {
	const runUUID = "econ-test-3b1f7c2a-conservation"

	dist := NewRewardDistributor()
	earnings := map[string]float64{
		"alice": 100.0,
		"bob":   300.0,
	}
	rule := DistributionRule{TreasuryCut: 0.10, ReinvestCut: 0.05}

	got, err := dist.Distribute(earnings, rule)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}

	// total = 400; treasury = 40; reinvest = 20; distributable = 340.
	// alice share = 340 * 100/400 = 85; bob share = 340 * 300/400 = 255.
	const (
		wantTotal    = 400.0
		wantTreasury = 40.0
		wantReinvest = 20.0
		wantAlice    = 85.0
		wantBob      = 255.0
	)

	if !approxEqual(got.Total, wantTotal) {
		t.Errorf("[%s] Total = %v, want %v", runUUID, got.Total, wantTotal)
	}
	if !approxEqual(got.Treasury, wantTreasury) {
		t.Errorf("[%s] Treasury = %v, want %v", runUUID, got.Treasury, wantTreasury)
	}
	if !approxEqual(got.Reinvest, wantReinvest) {
		t.Errorf("[%s] Reinvest = %v, want %v", runUUID, got.Reinvest, wantReinvest)
	}
	if !approxEqual(got.Ledger["alice"], wantAlice) {
		t.Errorf("[%s] Ledger[alice] = %v, want %v", runUUID, got.Ledger["alice"], wantAlice)
	}
	if !approxEqual(got.Ledger["bob"], wantBob) {
		t.Errorf("[%s] Ledger[bob] = %v, want %v", runUUID, got.Ledger["bob"], wantBob)
	}

	// Conservation invariant: sum(ledger) + treasury + reinvest == total.
	var ledgerSum float64
	for _, v := range got.Ledger {
		ledgerSum += v
	}
	conserved := ledgerSum + got.Treasury + got.Reinvest
	if !approxEqual(conserved, got.Total) {
		t.Fatalf("[%s] conservation violated: sum(ledger)+treasury+reinvest = %v, want Total = %v (delta %v)",
			runUUID, conserved, got.Total, conserved-got.Total)
	}
	t.Logf("[%s] proved: earnings{alice:100,bob:300} cuts{T:0.10,R:0.05} -> ledger{alice:%.2f,bob:%.2f} treasury:%.2f reinvest:%.2f; conservation %.2f==Total %.2f (delta %.3g)",
		runUUID, got.Ledger["alice"], got.Ledger["bob"], got.Treasury, got.Reinvest, conserved, got.Total, conserved-got.Total)
}

// TestGetParticipantROI_HandCalculated checks exact ROIPercent and
// BreakEvenDays against hand-computed values.
func TestGetParticipantROI_HandCalculated(t *testing.T) {
	const runUUID = "econ-test-9d4e6a1b-roi-handcalc"

	dist := NewRewardDistributor()
	balances := map[string]float64{"alice": 1500.0}
	costs := map[string]float64{"alice": 1000.0}
	dailyNet := 50.0

	got, err := dist.GetParticipantROI("alice", balances, costs, dailyNet)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}

	// ROIPercent = (1500 - 1000) / 1000 * 100 = 50.
	// BreakEvenDays = 1000 / 50 = 20.
	const wantROI = 50.0
	const wantBreakEven = 20.0

	if !approxEqual(got.ROIPercent, wantROI) {
		t.Errorf("[%s] ROIPercent = %v, want %v", runUUID, got.ROIPercent, wantROI)
	}
	if !approxEqual(got.BreakEvenDays, wantBreakEven) {
		t.Errorf("[%s] BreakEvenDays = %v, want %v", runUUID, got.BreakEvenDays, wantBreakEven)
	}
	t.Logf("[%s] proved: balance 1500, cost 1000, dailyNet 50 -> ROIPercent %.1f (want %.1f), BreakEvenDays %.1f (want %.1f)",
		runUUID, got.ROIPercent, wantROI, got.BreakEvenDays, wantBreakEven)
}

// TestGetParticipantROI_UnknownParticipant verifies ErrUnknownParticipant is
// returned (and is errors.Is-matchable) when the participant is absent.
func TestGetParticipantROI_UnknownParticipant(t *testing.T) {
	const runUUID = "econ-test-5f2c8e0d-unknown"

	dist := NewRewardDistributor()
	balances := map[string]float64{"alice": 100.0}
	costs := map[string]float64{"carol": 100.0}

	_, err := dist.GetParticipantROI("carol", balances, costs, 10.0)
	if !errors.Is(err, ErrUnknownParticipant) {
		t.Fatalf("[%s] err = %v, want ErrUnknownParticipant", runUUID, err)
	}
	t.Logf("[%s] proved: absent-from-balances participant 'carol' -> error errors.Is ErrUnknownParticipant (err=%v)", runUUID, err)
}

// TestDistribute_RejectsInvalidInput guards the input-validation paths.
func TestDistribute_RejectsInvalidInput(t *testing.T) {
	const runUUID = "econ-test-7a0b3c5e-invalid"

	dist := NewRewardDistributor()
	validRule := DistributionRule{TreasuryCut: 0.10, ReinvestCut: 0.05}

	if _, err := dist.Distribute(map[string]float64{}, validRule); err == nil {
		t.Errorf("[%s] expected error for empty earnings", runUUID)
	}
	if _, err := dist.Distribute(map[string]float64{"x": -1}, validRule); err == nil {
		t.Errorf("[%s] expected error for negative earnings", runUUID)
	}
	badRule := DistributionRule{TreasuryCut: 0.6, ReinvestCut: 0.5}
	if _, err := dist.Distribute(map[string]float64{"x": 1}, badRule); err == nil {
		t.Errorf("[%s] expected error for cuts summing >= 1", runUUID)
	}
	t.Logf("[%s] proved: empty/negative earnings and cuts>=1 all rejected with non-nil error", runUUID)
}
