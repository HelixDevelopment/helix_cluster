package redundantexec

import (
	"errors"
	"testing"
)

// adv2RunUUID tags log lines for this adversarial wave.
const adv2RunUUID = "redundantexec-adv2-5b1d7e44-2a90-4c3f-9f7e-71c0a8d4e2b6"

// adv2 — Sybil-via-duplicate quorum/trust forgery.
//
// VERDICT: REAL BUG (was mislabeled "latent"). Validate previously tallied
// quorum AND trust PER ROW, so a single worker appearing twice in the public
// `results` slice (which the caller assembles from public Result{} structs with
// no distinctness guard) deterministically:
//   - manufactured a fake majority alone (e.g. 2-of-3), admitting a
//     wrong/malicious value as the trusted canonical result, and
//   - double-rewarded itself once per duplicate row.
// It is publicly reachable (no API precondition documented or enforced) and
// always-on (no race, no map-order dependence). The fix tallies per DISTINCT
// WorkerID (first-row-wins dedup). These probes are the sink-side oracle.

// TestAdv2DuplicateCannotForgeQuorumAlone is the load-bearing probe. One real
// worker ("solo") duplicated against one honest dissenter ("other") must NOT
// produce a quorum: after dedup the distinct vote set is {X, Y} 1-1, a tie.
//
// MUTATION (proves load-bearing): without the dedup block in Validate, the two
// "solo" rows count as 2 votes for X vs 1 for Y => threshold ceil(0.5*3)=2 met,
// unique leader X => Validate WRONGLY returns X with AgreeCount 2. This test
// then fails on the ErrNoQuorum assertion. With the fix it passes.
func TestAdv2DuplicateCannotForgeQuorumAlone(t *testing.T) {
	v := NewValidator(0.5)
	results := []Result{
		{WorkerID: "solo", Value: "X"},
		{WorkerID: "solo", Value: "X"}, // duplicate row from the SAME worker
		{WorkerID: "other", Value: "Y"},
	}
	got, err := v.Validate("adv2-forge", results)
	t.Logf("run=%s err=%v value=%v agree=%d/%d agreeing=%v",
		adv2RunUUID, err, got.Value, got.AgreeCount, got.TotalCount, got.AgreeingWorkers)

	if !errors.Is(err, ErrNoQuorum) {
		t.Fatalf("run=%s a single duplicated worker FORGED a quorum: err=%v value=%v agree=%d/%d — Sybil-via-duplicate is live",
			adv2RunUUID, err, got.Value, got.AgreeCount, got.TotalCount)
	}
	if got.Value != nil {
		t.Fatalf("run=%s forged canonical value=%v, want none", adv2RunUUID, got.Value)
	}
}

// TestAdv2QuorumCountsDistinctWorkers proves AgreeCount/TotalCount reflect
// DISTINCT workers, not rows, and that a legitimately-formed majority still
// validates when a duplicate is present. Three distinct workers agree on X, one
// of them ("a") is duplicated, and one honest worker dissents with Y.
//
// Rows: a,a,b,c -> X ; d -> Y. Distinct: {a,b,c}=X (3), {d}=Y (1). total
// distinct = 4, threshold ceil(0.5*4)=2, leader 3 unique -> validates X with
// AgreeCount 3 (NOT 4) and TotalCount 4 (NOT 5).
//
// MUTATION: without dedup, AgreeCount becomes 4 and TotalCount 5 (row counts),
// failing the exact-count asserts below.
func TestAdv2QuorumCountsDistinctWorkers(t *testing.T) {
	v := NewValidator(0.5)
	results := []Result{
		{WorkerID: "a", Value: "X"},
		{WorkerID: "a", Value: "X"}, // duplicate of a
		{WorkerID: "b", Value: "X"},
		{WorkerID: "c", Value: "X"},
		{WorkerID: "d", Value: "Y"},
	}
	got, err := v.Validate("adv2-distinct", results)
	t.Logf("run=%s err=%v value=%v agree=%d/%d agreeing=%v",
		adv2RunUUID, err, got.Value, got.AgreeCount, got.TotalCount, got.AgreeingWorkers)
	if err != nil {
		t.Fatalf("run=%s legitimate 3-of-4 majority did not validate: err=%v", adv2RunUUID, err)
	}
	if got.Value != "X" {
		t.Fatalf("run=%s canonical=%v, want X", adv2RunUUID, got.Value)
	}
	if got.AgreeCount != 3 {
		t.Fatalf("run=%s AgreeCount=%d, want 3 (distinct agreers a,b,c — not row count 4)",
			adv2RunUUID, got.AgreeCount)
	}
	if got.TotalCount != 4 {
		t.Fatalf("run=%s TotalCount=%d, want 4 (distinct workers a,b,c,d — not row count 5)",
			adv2RunUUID, got.TotalCount)
	}
	// AgreeingWorkers must list each distinct agreer exactly once, sorted.
	want := []string{"a", "b", "c"}
	if len(got.AgreeingWorkers) != len(want) {
		t.Fatalf("run=%s AgreeingWorkers=%v, want %v (no duplicate 'a')",
			adv2RunUUID, got.AgreeingWorkers, want)
	}
	for i, id := range want {
		if got.AgreeingWorkers[i] != id {
			t.Fatalf("run=%s AgreeingWorkers[%d]=%s, want %s", adv2RunUUID, i, got.AgreeingWorkers[i], id)
		}
	}
}

// TestAdv2DuplicateRewardedAtMostOnce proves the trust delta for a duplicated
// agreeing worker is bounded to a SINGLE reward per Validate, not one per row.
// Setup keeps a clean quorum that validates X so the reward path is exercised:
// a (duplicated) + b + c agree on X; d dissents with Y.
//
// MUTATION: without dedup, "a" is rewarded twice in one Validate, so its gain
// is ~2*TrustReward, failing the single-reward bound below.
func TestAdv2DuplicateRewardedAtMostOnce(t *testing.T) {
	v := NewValidator(0.5)
	beforeA := v.Trust("a")
	results := []Result{
		{WorkerID: "a", Value: "X"},
		{WorkerID: "a", Value: "X"}, // duplicate row
		{WorkerID: "b", Value: "X"},
		{WorkerID: "c", Value: "X"},
		{WorkerID: "d", Value: "Y"},
	}
	if _, err := v.Validate("adv2-reward", results); err != nil {
		t.Fatalf("run=%s unexpected err=%v", adv2RunUUID, err)
	}
	afterA := v.Trust("a")
	gain := afterA - beforeA
	t.Logf("run=%s a trust before=%.6f after=%.6f gain=%.6f (want exactly 1x reward=%.6f)",
		adv2RunUUID, beforeA, afterA, gain, TrustReward)

	const eps = 1e-9
	if diff := gain - TrustReward; diff < -eps || diff > eps {
		t.Errorf("run=%s duplicated agreer 'a' net trust gain=%.6f, want %.6f (rewarded once per worker, not per row)",
			adv2RunUUID, gain, TrustReward)
	}
}

// TestAdv2EquivocatingDuplicateFirstRowWins proves the documented tie-break for
// a worker that contradicts ITSELF across duplicate rows: only the first row
// counts. Here "a" says X then (illegally) Y; honest "b" and "c" say X.
//
// Distinct after first-row-wins dedup: a=X, b=X, c=X => 3-of-3, validates X.
// The second "a:Y" row must be discarded entirely (it neither adds a Y vote nor
// penalizes a). This pins the dedup policy so a later change is intentional.
//
// MUTATION: without dedup, votes are X=3 (a,b,c) and Y=1 (a) over total=4;
// threshold ceil(0.5*4)=2; X still wins but TotalCount would be 4 and "a" would
// be both rewarded (X row) and penalized (Y row), netting reward-penalty. With
// the fix TotalCount==3 and "a" is rewarded once with no penalty.
func TestAdv2EquivocatingDuplicateFirstRowWins(t *testing.T) {
	v := NewValidator(0.5)
	beforeA := v.Trust("a")
	results := []Result{
		{WorkerID: "a", Value: "X"}, // first row for a — authoritative
		{WorkerID: "b", Value: "X"},
		{WorkerID: "c", Value: "X"},
		{WorkerID: "a", Value: "Y"}, // a equivocates — must be ignored
	}
	got, err := v.Validate("adv2-equiv", results)
	t.Logf("run=%s err=%v value=%v agree=%d/%d agreeing=%v",
		adv2RunUUID, err, got.Value, got.AgreeCount, got.TotalCount, got.AgreeingWorkers)
	if err != nil {
		t.Fatalf("run=%s unexpected err=%v", adv2RunUUID, err)
	}
	if got.Value != "X" {
		t.Fatalf("run=%s canonical=%v, want X", adv2RunUUID, got.Value)
	}
	if got.TotalCount != 3 {
		t.Fatalf("run=%s TotalCount=%d, want 3 (a counted once via first row)",
			adv2RunUUID, got.TotalCount)
	}
	if got.AgreeCount != 3 {
		t.Fatalf("run=%s AgreeCount=%d, want 3 (a,b,c all agree on a's first-row X)",
			adv2RunUUID, got.AgreeCount)
	}
	// a's first row agreed with the canonical X, so a is rewarded exactly once,
	// never penalized for the ignored Y row.
	afterA := v.Trust("a")
	gain := afterA - beforeA
	const eps = 1e-9
	if diff := gain - TrustReward; diff < -eps || diff > eps {
		t.Errorf("run=%s equivocating 'a' net gain=%.6f, want +%.6f (first row X rewarded once, Y row ignored)",
			adv2RunUUID, gain, TrustReward)
	}
}

// TestAdv2NoDuplicatesUnaffected is a regression guard: when every WorkerID is
// already distinct, the dedup pass is a no-op and the classic 4-of-5 majority
// validates exactly as before (counts equal row counts).
func TestAdv2NoDuplicatesUnaffected(t *testing.T) {
	v := NewValidator(0.5)
	results := []Result{
		{WorkerID: "w1", Value: "X"},
		{WorkerID: "w2", Value: "X"},
		{WorkerID: "w3", Value: "X"},
		{WorkerID: "w4", Value: "X"},
		{WorkerID: "w5", Value: "Y"},
	}
	got, err := v.Validate("adv2-clean", results)
	t.Logf("run=%s err=%v value=%v agree=%d/%d", adv2RunUUID, err, got.Value, got.AgreeCount, got.TotalCount)
	if err != nil {
		t.Fatalf("run=%s unexpected err=%v", adv2RunUUID, err)
	}
	if got.Value != "X" || got.AgreeCount != 4 || got.TotalCount != 5 {
		t.Fatalf("run=%s distinct-input regression: value=%v agree=%d total=%d, want X 4/5",
			adv2RunUUID, got.Value, got.AgreeCount, got.TotalCount)
	}
}

// TestAdv2InputSliceNotMutated proves the dedup pass does not mutate the
// caller's backing array (no aliasing side effect): the input slice's contents
// and length are byte-identical after Validate returns.
func TestAdv2InputSliceNotMutated(t *testing.T) {
	v := NewValidator(0.5)
	results := []Result{
		{WorkerID: "a", Value: "X"},
		{WorkerID: "a", Value: "X"},
		{WorkerID: "b", Value: "Y"},
	}
	snapshot := make([]Result, len(results))
	copy(snapshot, results)

	_, _ = v.Validate("adv2-nomutate", results)

	if len(results) != len(snapshot) {
		t.Fatalf("run=%s input length changed: got %d want %d", adv2RunUUID, len(results), len(snapshot))
	}
	for i := range snapshot {
		if results[i] != snapshot[i] {
			t.Fatalf("run=%s input row %d mutated: got %+v want %+v",
				adv2RunUUID, i, results[i], snapshot[i])
		}
	}
}
