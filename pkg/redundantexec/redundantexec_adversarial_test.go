package redundantexec

import (
	"errors"
	"sync"
	"testing"
	"time"
)

const advRunUUID = "redundantexec-adv-9c2f51ad-7e60-4b13-8a44-6d0fe2c3b9a1"

// withTimeout runs fn and fails the test if it does not return within d. It
// exists so any test that could in principle block can never hang CI.
func withTimeout(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("run=%s test body exceeded %s (possible hang)", advRunUUID, d)
	}
}

// TestNilValueQuorumIsSuccessNotNoQuorum probes the ambiguity between a
// SUCCESSFUL quorum whose canonical value is nil, and the ErrNoQuorum/empty
// outcome (which also yields Value==nil). The contract (the package doc and the
// Validate doc) is that the ERROR, not Validated.Value, signals whether a
// canonical was chosen. This pins that:
//
//   - When all workers agree on the untyped nil value, Validate MUST succeed
//     (err==nil) with AgreeCount==total, even though Value==nil.
//   - A caller that distinguishes outcomes by err (not by Value!=nil) is correct.
//
// CLASSIFICATION: DOCUMENTED CONTRACT / LATENT RISK pin. nil is a comparable
// value and is a legitimate quorum winner; callers MUST branch on err. This is
// load-bearing because the quorum path treats nil like any other value.
func TestNilValueQuorumIsSuccessNotNoQuorum(t *testing.T) {
	v := NewValidator(0.5)
	results := []Result{
		{WorkerID: "w1", Value: nil},
		{WorkerID: "w2", Value: nil},
		{WorkerID: "w3", Value: nil},
	}
	got, err := v.Validate("nil-task", results)
	t.Logf("run=%s nil-quorum err=%v value=%v agree=%d/%d",
		advRunUUID, err, got.Value, got.AgreeCount, got.TotalCount)
	if err != nil {
		t.Fatalf("run=%s all-agree-on-nil should validate, got err=%v", advRunUUID, err)
	}
	if got.AgreeCount != 3 || got.TotalCount != 3 {
		t.Fatalf("run=%s nil quorum agree=%d total=%d, want 3/3", advRunUUID, got.AgreeCount, got.TotalCount)
	}
	// Value is legitimately nil here, AND err is nil: the ONLY reliable signal is
	// err. Prove a Value!=nil check would mis-classify this success as failure.
	if got.Value != nil {
		t.Fatalf("run=%s nil canonical value=%v, want nil", advRunUUID, got.Value)
	}
	// Trust of all three was rewarded (they agreed with the quorum).
	for _, id := range []string{"w1", "w2", "w3"} {
		if tr := v.Trust(id); !(tr > InitialTrust) {
			t.Errorf("run=%s worker %s agreed on nil quorum but trust=%.4f not rewarded", advRunUUID, id, tr)
		}
	}
}

// TestMixedNilAndValueQuorum proves nil is tallied as a distinct comparable
// value alongside non-nil values and can win or lose the plurality like any
// other. Here 2 nil vs 1 "Z": nil is the unique plurality at threshold 2 -> nil
// validates.
func TestMixedNilAndValueQuorum(t *testing.T) {
	v := NewValidator(0.5)
	results := []Result{
		{WorkerID: "w1", Value: nil},
		{WorkerID: "w2", Value: nil},
		{WorkerID: "w3", Value: "Z"},
	}
	got, err := v.Validate("mixed-nil", results)
	t.Logf("run=%s mixed-nil err=%v value=%v agree=%d/%d",
		advRunUUID, err, got.Value, got.AgreeCount, got.TotalCount)
	if err != nil {
		t.Fatalf("run=%s 2-nil/1-Z should validate nil, err=%v", advRunUUID, err)
	}
	if got.Value != nil {
		t.Fatalf("run=%s want nil canonical, got %v", advRunUUID, got.Value)
	}
	if got.AgreeCount != 2 {
		t.Fatalf("run=%s want agree=2, got %d", advRunUUID, got.AgreeCount)
	}
	// The "Z" worker disagreed with the nil quorum and must be penalized.
	if tr := v.Trust("w3"); !(tr < InitialTrust) {
		t.Errorf("run=%s w3 dissented from nil quorum but trust=%.4f not penalized", advRunUUID, tr)
	}
}

// TestDuplicateWorkerInflatesQuorumAndTrust was originally written to PIN a
// Sybil-via-duplicate weakness as a "latent risk": Validate counted results PER
// ROW, so one worker appearing twice (a) manufactured a 2-of-3 majority alone,
// (b) was rewarded twice, and (c) appeared twice in AgreeingWorkers.
//
// That weakness has been confirmed an ALWAYS-ON, publicly reachable bug and
// FIXED: Validate now tallies quorum and trust per DISTINCT WorkerID
// (first-row-wins dedup). The original assertions pinned the vulnerability as
// "correct", making this a CLAUDE-1 PASS-bluff; they are replaced below with
// the corrected oracle. The exhaustive proof lives in
// redundantexec_adversarial2_test.go.
//
// CLASSIFICATION: REAL BUG (fixed). A duplicated worker must NOT forge quorum.
func TestDuplicateWorkerInflatesQuorumAndTrust(t *testing.T) {
	v := NewValidator(0.5)
	results := []Result{
		{WorkerID: "solo", Value: "X"},
		{WorkerID: "solo", Value: "X"}, // SAME worker, duplicate row — must be ignored
		{WorkerID: "other", Value: "Y"},
	}
	beforeSolo := v.Trust("solo")
	got, err := v.Validate("dup-task", results)
	t.Logf("run=%s dup err=%v value=%v agree=%d/%d agreeing=%v",
		advRunUUID, err, got.Value, got.AgreeCount, got.TotalCount, got.AgreeingWorkers)

	// After dedup the distinct set is {solo:X, other:Y} — a 1-1 tie among two
	// distinct workers, which is NOT a quorum. The duplicate no longer forges a
	// majority for X.
	if !errors.Is(err, ErrNoQuorum) {
		t.Fatalf("run=%s duplicated worker forged a quorum (err=%v value=%v) — dedup failed",
			advRunUUID, err, got.Value)
	}
	if got.Value != nil {
		t.Fatalf("run=%s want no canonical value after dedup tie, got %v", advRunUUID, got.Value)
	}
	// No quorum => no trust change at all.
	afterSolo := v.Trust("solo")
	if afterSolo != beforeSolo {
		t.Errorf("run=%s solo trust changed on no-quorum: before=%.4f after=%.4f",
			advRunUUID, beforeSolo, afterSolo)
	}
}

// TestSingleResultValidatesItself probes the smallest non-empty input: one
// result. threshold = ceil(frac*1); for any frac in (0,1], ceil <= 1 and clamped
// to >=1 => threshold 1. A single result is a unique plurality, so it validates
// itself and its worker is rewarded. This pins the n==1 boundary (distinct from
// the empty path).
//
// Mutation target: the `if t < 1 { t = 1 }` floor in thresholdCount. With a tiny
// frac (0.2) and total 1, frac*total=0.2, int(0.2)=0, then the ceil bump makes
// it 1; if the floor were removed AND the bump mis-handled, threshold 0 would
// validate with leader.count 1 anyway, so we instead assert the success + reward
// contract directly.
func TestSingleResultValidatesItself(t *testing.T) {
	v := NewValidator(0.2)
	got, err := v.Validate("single", []Result{{WorkerID: "only", Value: "V"}})
	t.Logf("run=%s single threshold=%d err=%v value=%v agree=%d/%d",
		advRunUUID, v.thresholdCount(1), err, got.Value, got.AgreeCount, got.TotalCount)
	if err != nil {
		t.Fatalf("run=%s single result should validate itself, err=%v", advRunUUID, err)
	}
	if got.Value != "V" || got.AgreeCount != 1 || got.TotalCount != 1 {
		t.Fatalf("run=%s single result value=%v agree=%d total=%d, want V 1/1",
			advRunUUID, got.Value, got.AgreeCount, got.TotalCount)
	}
	if tr := v.Trust("only"); !(tr > InitialTrust) {
		t.Errorf("run=%s lone worker not rewarded: trust=%.4f", advRunUUID, tr)
	}
}

// TestNoTrustChangeOnNoQuorumTie complements the existing tie test by asserting
// the trust-mutation side effect is fully skipped on the no-quorum tie path —
// i.e. the early return happens BEFORE the reward/penalty loop. A 2-2 tie must
// leave every worker at InitialTrust.
//
// Mutation: moving the trust loop above the quorum/tie guard (or dropping the
// `leaderTied` term from the guard) would either perturb trust here or validate
// the tie. We assert BOTH the error and zero trust movement.
func TestNoTrustChangeOnNoQuorumTie(t *testing.T) {
	v := NewValidator(0.5)
	results := []Result{
		{WorkerID: "p", Value: "X"},
		{WorkerID: "q", Value: "X"},
		{WorkerID: "r", Value: "Y"},
		{WorkerID: "s", Value: "Y"},
	}
	got, err := v.Validate("tie-notrust", results)
	if !errors.Is(err, ErrNoQuorum) {
		t.Fatalf("run=%s 2-2 tie err=%v, want ErrNoQuorum", advRunUUID, err)
	}
	t.Logf("run=%s tie err=%v value=%v", advRunUUID, err, got.Value)
	for _, id := range []string{"p", "q", "r", "s"} {
		if tr := v.Trust(id); tr != InitialTrust {
			t.Errorf("run=%s worker %s trust=%.4f changed on no-quorum tie (want %.4f)",
				advRunUUID, id, tr, InitialTrust)
		}
	}
}

// TestNewValidatorRejectsOutOfRangeFraction pins the (0,1] constructor guard:
// 0, negatives, and >1 must panic; the inclusive upper bound 1.0 must NOT panic.
//
// Mutation: flipping `> 1` to `>= 1` would panic on the legal unanimity fraction
// 1.0 and fail the no-panic sub-assert; dropping the `<= 0` term would accept 0
// (degenerate threshold) and fail the panic sub-assert.
func TestNewValidatorRejectsOutOfRangeFraction(t *testing.T) {
	mustPanic := func(frac float64) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("run=%s NewValidator(%v) did not panic, want panic", advRunUUID, frac)
			}
		}()
		_ = NewValidator(frac)
	}
	mustPanic(0)
	mustPanic(-0.5)
	mustPanic(1.0001)

	// 1.0 is the legal inclusive upper bound: must NOT panic.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("run=%s NewValidator(1.0) panicked (%v), want accepted", advRunUUID, r)
			}
		}()
		v := NewValidator(1.0)
		if v.thresholdCount(3) != 3 {
			t.Errorf("run=%s unanimity threshold(3)=%d, want 3", advRunUUID, v.thresholdCount(3))
		}
	}()
}

// TestZeroValueValidatorUsableConcurrently proves the documented "a zero
// Validator is usable" promise AND that the lazy workers-map init in Validate is
// race-safe: a zero &Validator{} (nil internal map, QuorumFraction 0 so any
// single-value set is a unanimous plurality at threshold>=1) is driven from many
// goroutines doing first-write registration concurrently with Trust reads.
//
// Note: a zero Validator has QuorumFraction==0. thresholdCount clamps to >=1, so
// a 3-of-3 same-value set validates. We confirm no panic / no map corruption and
// final trust within bounds. Guarded by a timeout so it can never hang.
//
// Mutation: removing the `if v.workers == nil` lazy-init in Validate makes the
// first concurrent write panic (assignment to entry in nil map) -> the timeout
// or a panic fails the test.
func TestZeroValueValidatorUsableConcurrently(t *testing.T) {
	withTimeout(t, 30*time.Second, func() {
		var v Validator // zero value, nil workers map, QuorumFraction 0
		var wg sync.WaitGroup
		const g = 16
		for i := 0; i < g; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					results := []Result{
						{WorkerID: "z1", Value: "C"},
						{WorkerID: "z2", Value: "C"},
						{WorkerID: "z3", Value: "C"},
					}
					if _, err := v.Validate("zero", results); err != nil {
						t.Errorf("run=%s zero-validator validate err=%v", advRunUUID, err)
						return
					}
					_ = v.Trust("z1")
				}
			}(i)
		}
		wg.Wait()
		for _, id := range []string{"z1", "z2", "z3"} {
			if tr := v.Trust(id); tr < TrustFloor || tr > TrustCeil {
				t.Errorf("run=%s zero-validator worker %s trust=%.4f out of bounds", advRunUUID, id, tr)
			}
		}
	})
}
