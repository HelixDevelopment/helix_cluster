package redundantexec

import (
	"errors"
	"testing"
)

const runUUID = "redundantexec-test-3f7c9a21-0bd4-4e6c-9a1f-1408a0c0ffee"

// TestMajorityAgreementRewardsAndPenalizes proves closure clause 1: with 5
// workers where 4 return X and 1 returns Y, the validated result is X, the 4
// agreeing workers' trust INCREASES and the lone outlier's trust DECREASES.
//
// Mutation: removing the TrustPenalty branch (outlier not penalized) leaves the
// outlier trust unchanged and fails the wAfterOutlier < wBeforeOutlier assert;
// removing the TrustReward branch leaves agreers unchanged and fails the
// increase asserts.
func TestMajorityAgreementRewardsAndPenalizes(t *testing.T) {
	v := NewValidator(0.5)

	results := []Result{
		{WorkerID: "w1", Value: "X"},
		{WorkerID: "w2", Value: "X"},
		{WorkerID: "w3", Value: "X"},
		{WorkerID: "w4", Value: "X"},
		{WorkerID: "w5", Value: "Y"}, // outlier
	}

	// Capture BEFORE trust for every worker.
	before := map[string]float64{}
	for _, id := range []string{"w1", "w2", "w3", "w4", "w5"} {
		before[id] = v.Trust(id)
		t.Logf("run=%s BEFORE worker=%s trust=%.4f", runUUID, id, before[id])
	}

	got, err := v.Validate("task-A", results)
	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", runUUID, err)
	}
	if got.Value != "X" {
		t.Fatalf("run=%s canonical value = %v, want X", runUUID, got.Value)
	}
	if got.AgreeCount != 4 || got.TotalCount != 5 {
		t.Fatalf("run=%s agree=%d total=%d, want 4/5", runUUID, got.AgreeCount, got.TotalCount)
	}
	wantAgree := []string{"w1", "w2", "w3", "w4"}
	if len(got.AgreeingWorkers) != len(wantAgree) {
		t.Fatalf("run=%s agreeing=%v, want %v", runUUID, got.AgreeingWorkers, wantAgree)
	}
	for i, id := range wantAgree {
		if got.AgreeingWorkers[i] != id {
			t.Fatalf("run=%s agreeing[%d]=%s, want %s", runUUID, i, got.AgreeingWorkers[i], id)
		}
	}

	// Capture AFTER trust and assert direction of movement.
	for _, id := range wantAgree {
		after := v.Trust(id)
		t.Logf("run=%s AFTER  worker=%s trust=%.4f (agreer)", runUUID, id, after)
		if !(after > before[id]) {
			t.Errorf("run=%s agreer %s trust did not increase: before=%.4f after=%.4f",
				runUUID, id, before[id], after)
		}
	}

	afterOutlier := v.Trust("w5")
	t.Logf("run=%s AFTER  worker=w5 trust=%.4f (outlier)", runUUID, afterOutlier)
	if !(afterOutlier < before["w5"]) {
		t.Errorf("run=%s outlier w5 trust did not decrease: before=%.4f after=%.4f",
			runUUID, before["w5"], afterOutlier)
	}
}

// TestNoQuorumAllDistinct proves closure clause 2: 5 workers returning 5
// distinct values reach no quorum, so Validate returns ErrNoQuorum, chooses no
// canonical value, and perturbs no trust.
//
// Mutation: lowering the quorum so a single result wins (e.g. threshold==1
// without the strict-plurality tie guard) would wrongly validate one of the
// distinct values and fail the ErrNoQuorum assert. Also, moving trust mutation
// before the quorum check would change trust here and fail the no-trust-change
// assert.
func TestNoQuorumAllDistinct(t *testing.T) {
	v := NewValidator(0.5)

	results := []Result{
		{WorkerID: "w1", Value: "A"},
		{WorkerID: "w2", Value: "B"},
		{WorkerID: "w3", Value: "C"},
		{WorkerID: "w4", Value: "D"},
		{WorkerID: "w5", Value: "E"},
	}

	before := map[string]float64{}
	for _, id := range []string{"w1", "w2", "w3", "w4", "w5"} {
		before[id] = v.Trust(id)
		t.Logf("run=%s BEFORE worker=%s trust=%.4f", runUUID, id, before[id])
	}

	got, err := v.Validate("task-B", results)
	if !errors.Is(err, ErrNoQuorum) {
		t.Fatalf("run=%s err=%v, want ErrNoQuorum", runUUID, err)
	}
	if got.Value != nil {
		t.Fatalf("run=%s canonical value=%v, want none (nil)", runUUID, got.Value)
	}
	t.Logf("run=%s no-quorum confirmed: err=%v value=%v", runUUID, err, got.Value)

	// No trust must have changed.
	for _, id := range []string{"w1", "w2", "w3", "w4", "w5"} {
		after := v.Trust(id)
		t.Logf("run=%s AFTER  worker=%s trust=%.4f", runUUID, id, after)
		if after != before[id] {
			t.Errorf("run=%s worker %s trust changed on no-quorum: before=%.4f after=%.4f",
				runUUID, id, before[id], after)
		}
	}
}

// TestQuorumBoundaryTieRejected proves closure clause 3: a borderline even split
// exactly at the threshold count but tied does NOT validate (the documented
// strict-plurality rule), while the same total with a unique leader at the
// threshold DOES validate.
//
// Mutation: dropping the leaderTied guard would let the 2-2 tie validate an
// arbitrary value and fail the ErrNoQuorum assert in the tie sub-case.
func TestQuorumBoundaryTieRejected(t *testing.T) {
	// Sub-case A: 4 results, 2 vs 2. threshold = ceil(0.5*4) = 2. Leader count
	// meets threshold but is tied -> no quorum.
	t.Run("tie_at_threshold_rejected", func(t *testing.T) {
		v := NewValidator(0.5)
		tied := []Result{
			{WorkerID: "w1", Value: "X"},
			{WorkerID: "w2", Value: "X"},
			{WorkerID: "w3", Value: "Y"},
			{WorkerID: "w4", Value: "Y"},
		}
		got, err := v.Validate("task-tie", tied)
		t.Logf("run=%s tie-case threshold=%d err=%v value=%v",
			runUUID, v.thresholdCount(4), err, got.Value)
		if !errors.Is(err, ErrNoQuorum) {
			t.Fatalf("run=%s 2-2 tie err=%v, want ErrNoQuorum", runUUID, err)
		}
		if got.Value != nil {
			t.Fatalf("run=%s tie produced canonical value=%v, want none", runUUID, got.Value)
		}
	})

	// Sub-case B: 4 results, 3 vs 1. threshold = 2; leader count 3 >= 2 and is a
	// unique plurality -> validates X.
	t.Run("unique_leader_at_threshold_validates", func(t *testing.T) {
		v := NewValidator(0.5)
		split := []Result{
			{WorkerID: "w1", Value: "X"},
			{WorkerID: "w2", Value: "X"},
			{WorkerID: "w3", Value: "X"},
			{WorkerID: "w4", Value: "Y"},
		}
		got, err := v.Validate("task-split", split)
		t.Logf("run=%s split-case threshold=%d err=%v value=%v agree=%d/%d",
			runUUID, v.thresholdCount(4), err, got.Value, got.AgreeCount, got.TotalCount)
		if err != nil {
			t.Fatalf("run=%s 3-1 split err=%v, want validated", runUUID, err)
		}
		if got.Value != "X" {
			t.Fatalf("run=%s 3-1 split value=%v, want X", runUUID, got.Value)
		}
		if got.AgreeCount != 3 {
			t.Fatalf("run=%s 3-1 split agree=%d, want 3", runUUID, got.AgreeCount)
		}
	})
}

// TestThresholdCountRule pins the documented ceil(frac*total) threshold so a
// silent change to the boundary arithmetic is caught.
//
// Mutation: changing ceil to floor (or off-by-one) flips at least one of these
// expectations.
func TestThresholdCountRule(t *testing.T) {
	cases := []struct {
		frac  float64
		total int
		want  int
	}{
		{0.5, 5, 3},  // ceil(2.5)=3
		{0.5, 4, 2},  // ceil(2.0)=2
		{0.66, 3, 2}, // ceil(1.98)=2
		{1.0, 3, 3},  // unanimity
		{0.2, 5, 1},  // ceil(1.0)=1
	}
	for _, c := range cases {
		v := NewValidator(c.frac)
		got := v.thresholdCount(c.total)
		t.Logf("run=%s frac=%.2f total=%d threshold=%d", runUUID, c.frac, c.total, got)
		if got != c.want {
			t.Errorf("run=%s thresholdCount(frac=%.2f,total=%d)=%d, want %d",
				runUUID, c.frac, c.total, got, c.want)
		}
	}
}

// TestTrustClampAndPersistence proves trust persists across tasks and is clamped
// to [TrustFloor, TrustCeil]. A worker that keeps disagreeing eventually floors
// at TrustFloor rather than going negative.
//
// Mutation: removing the clamp lower bound lets trust go below TrustFloor and
// fails the >= TrustFloor assert.
func TestTrustClampAndPersistence(t *testing.T) {
	v := NewValidator(0.5)

	// Run many tasks where w_bad always disagrees with a 3-worker majority.
	for i := 0; i < 20; i++ {
		results := []Result{
			{WorkerID: "good1", Value: "OK"},
			{WorkerID: "good2", Value: "OK"},
			{WorkerID: "good3", Value: "OK"},
			{WorkerID: "bad", Value: "WRONG"},
		}
		if _, err := v.Validate("task-loop", results); err != nil {
			t.Fatalf("run=%s iter=%d unexpected err=%v", runUUID, i, err)
		}
	}

	badTrust := v.Trust("bad")
	goodTrust := v.Trust("good1")
	t.Logf("run=%s after 20 tasks bad=%.4f good=%.4f", runUUID, badTrust, goodTrust)

	if badTrust < TrustFloor {
		t.Errorf("run=%s bad trust %.4f below floor %.4f", runUUID, badTrust, TrustFloor)
	}
	if badTrust != TrustFloor {
		t.Errorf("run=%s bad trust %.4f did not reach floor %.4f after sustained disagreement",
			runUUID, badTrust, TrustFloor)
	}
	if goodTrust > TrustCeil {
		t.Errorf("run=%s good trust %.4f above ceil %.4f", runUUID, goodTrust, TrustCeil)
	}
	if goodTrust != TrustCeil {
		t.Errorf("run=%s good trust %.4f did not reach ceil %.4f after sustained agreement",
			runUUID, goodTrust, TrustCeil)
	}
}

// TestEmptyResults proves the typed ErrNoResults path.
//
// Mutation: removing the len==0 guard would index/iterate empty data and not
// return ErrNoResults.
func TestEmptyResults(t *testing.T) {
	v := NewValidator(0.5)
	got, err := v.Validate("empty", nil)
	t.Logf("run=%s empty err=%v value=%v", runUUID, err, got.Value)
	if !errors.Is(err, ErrNoResults) {
		t.Fatalf("run=%s err=%v, want ErrNoResults", runUUID, err)
	}
}
