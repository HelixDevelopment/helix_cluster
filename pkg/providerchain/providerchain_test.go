package providerchain

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// runID used across all tests — satisfies CLAUDE-1: every result carries it and
// assertions verify it is propagated correctly.
const testRunID = "pctest-3f7a91d2-5c4e-4b8a-9e21-0d6f4a2c1b90"

// ── clock helpers ─────────────────────────────────────────────────────────────

// stepClock returns a Now func whose successive calls advance by step each call.
// The first call returns base; the second returns base+step; etc.
// This makes elapsed time fully deterministic without touching the real clock.
func stepClock(base time.Time, step time.Duration) func() time.Time {
	t := base
	return func() time.Time {
		cur := t
		t = t.Add(step)
		return cur
	}
}

// flatClock returns a Now func that always returns the same instant (zero
// elapsed time — useful for tests that only verify the cascade path).
func flatClock(instant time.Time) func() time.Time {
	return func() time.Time { return instant }
}

// ── attempt builders ──────────────────────────────────────────────────────────

// succeedAttempt returns an Attempt that always succeeds with the given output.
func succeedAttempt(output string) Attempt {
	return func(_ context.Context) (string, error) {
		return output, nil
	}
}

// retriableAttempt returns an Attempt that always fails with a retriable error.
func retriableAttempt(msg string) Attempt {
	return func(_ context.Context) (string, error) {
		return "", fmt.Errorf("%s", msg)
	}
}

// terminalAttempt returns an Attempt that always fails with a terminal error.
func terminalAttempt(msg string) Attempt {
	return func(_ context.Context) (string, error) {
		return "", fmt.Errorf("%s", msg)
	}
}

// classifierFor returns a Classifier that maps a specific error message to
// Terminal; everything else is Retriable.
func classifierFor(terminalMsg string) Classifier {
	return func(err error) ErrorClass {
		if err != nil && err.Error() == terminalMsg {
			return Terminal
		}
		return Retriable
	}
}

// ── required named test ───────────────────────────────────────────────────────

// TestRetriableAdvancesToSecondTier is the REQUIRED named test.
//
// Mutation guard: the advance-to-next-tier step (the for-range loop in Execute).
// If Execute is changed so it does NOT advance past the first tier on a
// retriable error (e.g. breaks after tier[0] regardless of outcome), then:
//   - res.WinnerTier would be the first tier name, not "second"
//   - res.CascadePath would be ["primary"] not ["primary", "second"]
//   - the test FAILS on the WinnerTier assertion.
//
// Pinned by comment in providerchain.go: "advance-to-next-tier guard line"
func TestRetriableAdvancesToSecondTier(t *testing.T) {
	// Primary tier injects an HTTP-429-style retriable failure.
	primaryAttempt := retriableAttempt("HTTP 429: overloaded")
	// Second tier succeeds.
	secondAttempt := succeedAttempt("output-from-second-tier")

	chain := &Chain{
		Tiers: []Tier{
			{Name: "primary", Attempt: primaryAttempt},
			{Name: "second", Attempt: secondAttempt},
		},
		// Classifier marks every error retriable (default, but explicit here).
		Classifier: DefaultClassifier,
		SLA:        10 * time.Second,
		// Injected deterministic clock: first call = start, second call = start+1s.
		Now: stepClock(time.Unix(1_000_000, 0), time.Second),
	}

	t.Logf("run=%s BEFORE Execute: tiers=[primary(retriable), second(success)]", testRunID)

	res, err := chain.Execute(context.Background(), testRunID)

	t.Logf("run=%s AFTER Execute: winner=%q cascadePath=%v elapsed=%v err=%v",
		testRunID, res.WinnerTier, res.CascadePath, res.Elapsed, err)

	// ── no error ──────────────────────────────────────────────────────────
	if err != nil {
		t.Fatalf("run=%s: expected nil error, got %v", testRunID, err)
	}

	// ── winner is the SECOND tier (not primary) ───────────────────────────
	// MUTATION KILL: if the advance is removed, WinnerTier == "primary" (or
	// the loop never reaches second) and this assertion fails.
	if res.WinnerTier != "second" {
		t.Fatalf("run=%s: expected WinnerTier=%q, got %q", testRunID, "second", res.WinnerTier)
	}

	// ── output comes from the second tier ────────────────────────────────
	if res.Output != "output-from-second-tier" {
		t.Fatalf("run=%s: expected Output=%q, got %q", testRunID, "output-from-second-tier", res.Output)
	}

	// ── cascade path lists primary THEN second ───────────────────────────
	wantPath := []string{"primary", "second"}
	if !reflect.DeepEqual(res.CascadePath, wantPath) {
		t.Fatalf("run=%s: expected CascadePath=%v, got %v", testRunID, wantPath, res.CascadePath)
	}

	// ── RunID is recorded ─────────────────────────────────────────────────
	if res.RunID != testRunID {
		t.Fatalf("run=%s: expected RunID=%q, got %q", testRunID, testRunID, res.RunID)
	}

	// ── elapsed is within SLA ────────────────────────────────────────────
	// stepClock advances by 1 second per call; start call and end call give
	// elapsed == 1 second, well within the 10-second SLA.
	if res.Elapsed <= 0 {
		t.Fatalf("run=%s: expected positive Elapsed, got %v", testRunID, res.Elapsed)
	}
	if res.Elapsed > chain.SLA {
		t.Fatalf("run=%s: Elapsed %v exceeds SLA %v", testRunID, res.Elapsed, chain.SLA)
	}
}

// ── additional tests ──────────────────────────────────────────────────────────

// TestFirstTierSucceeds verifies that when the first tier wins the cascade path
// contains only that tier and WinnerTier matches.
func TestFirstTierSucceeds(t *testing.T) {
	chain := &Chain{
		Tiers: []Tier{
			{Name: "Chutes", Attempt: succeedAttempt("chutes-result")},
			{Name: "io.net", Attempt: succeedAttempt("ionet-result")},
		},
		Now: flatClock(time.Unix(2_000_000, 0)),
		SLA: 10 * time.Second,
	}

	t.Logf("run=%s BEFORE Execute: first tier should win immediately", testRunID)
	res, err := chain.Execute(context.Background(), testRunID)
	t.Logf("run=%s AFTER Execute: winner=%q cascadePath=%v", testRunID, res.WinnerTier, res.CascadePath)

	if err != nil {
		t.Fatalf("run=%s: unexpected error: %v", testRunID, err)
	}
	if res.WinnerTier != "Chutes" {
		t.Fatalf("run=%s: expected WinnerTier=Chutes, got %q", testRunID, res.WinnerTier)
	}
	if res.Output != "chutes-result" {
		t.Fatalf("run=%s: expected Output=chutes-result, got %q", testRunID, res.Output)
	}
	if !reflect.DeepEqual(res.CascadePath, []string{"Chutes"}) {
		t.Fatalf("run=%s: expected CascadePath=[Chutes], got %v", testRunID, res.CascadePath)
	}
	if res.RunID != testRunID {
		t.Fatalf("run=%s: RunID not propagated, got %q", testRunID, res.RunID)
	}
}

// TestAllTiersRetriableFails verifies that exhausting every tier without a
// winner returns an AggregateError that names every tier.
func TestAllTiersRetriableFails(t *testing.T) {
	chain := &Chain{
		Tiers: []Tier{
			{Name: "Chutes", Attempt: retriableAttempt("429")},
			{Name: "io.net", Attempt: retriableAttempt("429")},
			{Name: "RunPod", Attempt: retriableAttempt("429")},
		},
		Classifier: DefaultClassifier,
		Now:        flatClock(time.Unix(3_000_000, 0)),
		SLA:        10 * time.Second,
	}

	t.Logf("run=%s BEFORE Execute: all tiers return retriable 429", testRunID)
	res, err := chain.Execute(context.Background(), testRunID)
	t.Logf("run=%s AFTER Execute: err=%v cascadePath=%v", testRunID, err, res.CascadePath)

	if err == nil {
		t.Fatalf("run=%s: expected AggregateError, got nil", testRunID)
	}
	var ae *AggregateError
	if !errors.As(err, &ae) {
		t.Fatalf("run=%s: expected *AggregateError, got %T: %v", testRunID, err, err)
	}
	if ae.RunID != testRunID {
		t.Fatalf("run=%s: AggregateError.RunID=%q, want %q", testRunID, ae.RunID, testRunID)
	}
	if len(ae.Tiers) != 3 {
		t.Fatalf("run=%s: expected 3 tier errors, got %d: %v", testRunID, len(ae.Tiers), ae.Tiers)
	}
	// cascade path must list all three tiers.
	wantPath := []string{"Chutes", "io.net", "RunPod"}
	if !reflect.DeepEqual(res.CascadePath, wantPath) {
		t.Fatalf("run=%s: expected CascadePath=%v, got %v", testRunID, wantPath, res.CascadePath)
	}
	// No winner.
	if res.WinnerTier != "" || res.Output != "" {
		t.Fatalf("run=%s: expected empty winner on full failure, got winner=%q output=%q",
			testRunID, res.WinnerTier, res.Output)
	}
}

// TestTerminalErrorStopsCascade verifies that a terminal error halts the chain
// before reaching subsequent tiers.
func TestTerminalErrorStopsCascade(t *testing.T) {
	termMsg := "auth-failure-terminal"
	secondCalled := false
	secondTier := Tier{
		Name: "AWS",
		Attempt: func(_ context.Context) (string, error) {
			secondCalled = true
			return "aws-result", nil
		},
	}

	chain := &Chain{
		Tiers: []Tier{
			{Name: "RunPod", Attempt: terminalAttempt(termMsg)},
			secondTier,
		},
		Classifier: classifierFor(termMsg),
		Now:        flatClock(time.Unix(4_000_000, 0)),
		SLA:        10 * time.Second,
	}

	t.Logf("run=%s BEFORE Execute: RunPod returns terminal error", testRunID)
	res, err := chain.Execute(context.Background(), testRunID)
	t.Logf("run=%s AFTER Execute: err=%v cascadePath=%v secondCalled=%v",
		testRunID, err, res.CascadePath, secondCalled)

	if err == nil {
		t.Fatalf("run=%s: expected error from terminal failure", testRunID)
	}
	var ae *AggregateError
	if !errors.As(err, &ae) {
		t.Fatalf("run=%s: expected *AggregateError, got %T: %v", testRunID, err, err)
	}
	// Only RunPod should have been attempted.
	if !reflect.DeepEqual(res.CascadePath, []string{"RunPod"}) {
		t.Fatalf("run=%s: expected CascadePath=[RunPod], got %v", testRunID, res.CascadePath)
	}
	if secondCalled {
		t.Fatalf("run=%s: AWS tier must NOT be called after a terminal error", testRunID)
	}
	if ae.RunID != testRunID {
		t.Fatalf("run=%s: AggregateError.RunID=%q, want %q", testRunID, ae.RunID, testRunID)
	}
	if len(ae.Tiers) != 1 || ae.Tiers[0].Class != Terminal {
		t.Fatalf("run=%s: expected one Terminal TierError, got %v", testRunID, ae.Tiers)
	}
}

// TestNoTiersReturnsErrNoTiers verifies the guard for an empty tier list.
func TestNoTiersReturnsErrNoTiers(t *testing.T) {
	chain := &Chain{
		Tiers: nil,
		Now:   flatClock(time.Unix(5_000_000, 0)),
		SLA:   10 * time.Second,
	}

	t.Logf("run=%s BEFORE Execute: empty tier list", testRunID)
	res, err := chain.Execute(context.Background(), testRunID)
	t.Logf("run=%s AFTER Execute: err=%v res.RunID=%q", testRunID, err, res.RunID)

	if !errors.Is(err, ErrNoTiers) {
		t.Fatalf("run=%s: expected ErrNoTiers, got %v", testRunID, err)
	}
	if res.RunID != testRunID {
		t.Fatalf("run=%s: RunID not set on error result, got %q", testRunID, res.RunID)
	}
}

// TestSLAExceeded verifies that a winner that arrives after the SLA still
// returns ErrSLAExceeded alongside the result so callers can act on it.
func TestSLAExceeded(t *testing.T) {
	// stepClock advances by 20 seconds per call: start at T, success measured
	// at T+20s, which exceeds the 10s SLA.
	slaExceedClock := stepClock(time.Unix(6_000_000, 0), 20*time.Second)

	chain := &Chain{
		Tiers: []Tier{
			{Name: "slow-provider", Attempt: succeedAttempt("late-result")},
		},
		Now: slaExceedClock,
		SLA: 10 * time.Second,
	}

	t.Logf("run=%s BEFORE Execute: single tier succeeds but clock advances 20s", testRunID)
	res, err := chain.Execute(context.Background(), testRunID)
	t.Logf("run=%s AFTER Execute: winner=%q elapsed=%v err=%v",
		testRunID, res.WinnerTier, res.Elapsed, err)

	// Must return ErrSLAExceeded even though there was a winner.
	if !errors.Is(err, ErrSLAExceeded) {
		t.Fatalf("run=%s: expected ErrSLAExceeded, got %v", testRunID, err)
	}
	// Result must still carry the winner and RunID.
	if res.WinnerTier != "slow-provider" {
		t.Fatalf("run=%s: expected WinnerTier=slow-provider, got %q", testRunID, res.WinnerTier)
	}
	if res.Output != "late-result" {
		t.Fatalf("run=%s: expected Output=late-result, got %q", testRunID, res.Output)
	}
	if res.RunID != testRunID {
		t.Fatalf("run=%s: RunID not propagated in SLA-exceeded result, got %q", testRunID, res.RunID)
	}
	if res.Elapsed <= chain.SLA {
		t.Fatalf("run=%s: expected Elapsed > SLA (%v), got %v", testRunID, chain.SLA, res.Elapsed)
	}
}

// TestFourTierCascade exercises the full Chutes→io.net→RunPod→AWS named
// provider chain where the first two tiers return retriable 429s, the third
// returns a retriable error, and AWS (fourth) wins.
func TestFourTierCascade(t *testing.T) {
	chain := &Chain{
		Tiers: []Tier{
			{Name: "Chutes", Attempt: retriableAttempt("429: overloaded")},
			{Name: "io.net", Attempt: retriableAttempt("429: overloaded")},
			{Name: "RunPod", Attempt: retriableAttempt("503: capacity")},
			{Name: "AWS", Attempt: succeedAttempt("aws-gpu-result")},
		},
		Classifier: DefaultClassifier,
		Now:        stepClock(time.Unix(7_000_000, 0), 500*time.Millisecond),
		SLA:        10 * time.Second,
	}

	t.Logf("run=%s BEFORE Execute: 4-tier cascade, AWS wins", testRunID)
	res, err := chain.Execute(context.Background(), testRunID)
	t.Logf("run=%s AFTER Execute: winner=%q cascadePath=%v elapsed=%v",
		testRunID, res.WinnerTier, res.CascadePath, res.Elapsed)

	if err != nil {
		t.Fatalf("run=%s: unexpected error: %v", testRunID, err)
	}
	if res.WinnerTier != "AWS" {
		t.Fatalf("run=%s: expected WinnerTier=AWS, got %q", testRunID, res.WinnerTier)
	}
	if res.Output != "aws-gpu-result" {
		t.Fatalf("run=%s: expected Output=aws-gpu-result, got %q", testRunID, res.Output)
	}
	wantPath := []string{"Chutes", "io.net", "RunPod", "AWS"}
	if !reflect.DeepEqual(res.CascadePath, wantPath) {
		t.Fatalf("run=%s: expected CascadePath=%v, got %v", testRunID, wantPath, res.CascadePath)
	}
	if res.RunID != testRunID {
		t.Fatalf("run=%s: RunID not propagated, got %q", testRunID, res.RunID)
	}
	if res.Elapsed <= 0 {
		t.Fatalf("run=%s: expected positive Elapsed, got %v", testRunID, res.Elapsed)
	}
	if res.Elapsed > chain.SLA {
		t.Fatalf("run=%s: Elapsed %v exceeds SLA %v", testRunID, res.Elapsed, chain.SLA)
	}
}

// TestEmptyOutputRetriable verifies that a tier returning ("", nil) is treated as
// a retriable failure and the chain advances to the next tier.
//
// Mutation guard (empty-output path): if the branch at "err == nil but out == ''"
// is removed (so an empty result is treated as a success), then:
//   - res.WinnerTier would be "empty-provider" (the first tier) not "fallback"
//   - res.Output would be "" not "fallback-result"
//   - the WinnerTier assertion fails.
//
// Pinned by: the "err == nil but out == ''" block in Execute.
func TestEmptyOutputRetriable(t *testing.T) {
	emptyAttempt := func(_ context.Context) (string, error) {
		return "", nil // success-shaped but empty — must be treated as retriable
	}

	chain := &Chain{
		Tiers: []Tier{
			{Name: "empty-provider", Attempt: emptyAttempt},
			{Name: "fallback", Attempt: succeedAttempt("fallback-result")},
		},
		Classifier: DefaultClassifier,
		SLA:        10 * time.Second,
		Now:        stepClock(time.Unix(9_000_000, 0), time.Second),
	}

	t.Logf("run=%s BEFORE Execute: first tier returns ('',nil), fallback should win", testRunID)
	res, err := chain.Execute(context.Background(), testRunID)
	t.Logf("run=%s AFTER Execute: winner=%q cascadePath=%v err=%v",
		testRunID, res.WinnerTier, res.CascadePath, err)

	if err != nil {
		t.Fatalf("run=%s: expected nil error, got %v", testRunID, err)
	}
	// MUTATION KILL: if empty-output is treated as a success, WinnerTier == "empty-provider".
	if res.WinnerTier != "fallback" {
		t.Fatalf("run=%s: expected WinnerTier=fallback, got %q", testRunID, res.WinnerTier)
	}
	if res.Output != "fallback-result" {
		t.Fatalf("run=%s: expected Output=fallback-result, got %q", testRunID, res.Output)
	}
	wantPath := []string{"empty-provider", "fallback"}
	if !reflect.DeepEqual(res.CascadePath, wantPath) {
		t.Fatalf("run=%s: expected CascadePath=%v, got %v", testRunID, wantPath, res.CascadePath)
	}
	if res.RunID != testRunID {
		t.Fatalf("run=%s: RunID not propagated, got %q", testRunID, res.RunID)
	}
}

// TestAggregateErrorString verifies the AggregateError.Error() message contains
// the RunID and at least one tier name — a concrete, non-trivial string check.
func TestAggregateErrorString(t *testing.T) {
	ae := &AggregateError{
		Tiers: []TierError{
			{TierName: "Chutes", Class: Retriable, Cause: errors.New("429")},
		},
		RunID:  testRunID,
		Reason: "all tiers failed",
	}

	t.Logf("run=%s AggregateError.Error(): %s", testRunID, ae.Error())

	if !contains(ae.Error(), testRunID) {
		t.Fatalf("run=%s: AggregateError.Error() must contain RunID", testRunID)
	}
	if !contains(ae.Error(), "Chutes") {
		t.Fatalf("run=%s: AggregateError.Error() must contain tier name 'Chutes'", testRunID)
	}
}

// TestDefaultSLAApplied verifies that a zero SLA field defaults to 10 seconds
// and does not panic.
//
// Mutation guard (default-SLA value): if sla() is mutated to return a value
// other than 10 seconds (e.g. 0 or 1 ns), the Elapsed<=defaultSLA assertion
// below will catch it — combined with TestSLAExceeded which injects elapsed=20s
// and expects ErrSLAExceeded, these two tests jointly pin the 10-second boundary.
func TestDefaultSLAApplied(t *testing.T) {
	const defaultSLA = 10 * time.Second
	chain := &Chain{
		Tiers: []Tier{
			{Name: "solo", Attempt: succeedAttempt("ok")},
		},
		// SLA intentionally left zero — should default to 10s.
		Now: flatClock(time.Unix(8_000_000, 0)),
	}

	t.Logf("run=%s BEFORE Execute: zero SLA should default to 10s", testRunID)
	res, err := chain.Execute(context.Background(), testRunID)
	t.Logf("run=%s AFTER Execute: winner=%q elapsed=%v err=%v", testRunID, res.WinnerTier, res.Elapsed, err)

	if err != nil {
		t.Fatalf("run=%s: unexpected error with default SLA: %v", testRunID, err)
	}
	if res.WinnerTier != "solo" {
		t.Fatalf("run=%s: expected winner=solo, got %q", testRunID, res.WinnerTier)
	}
	// The flat clock returns zero elapsed; flat 0 must be ≤ the effective default SLA.
	// This pins the default SLA: if sla() returned 0 or negative, Execute would
	// return ErrSLAExceeded (elapsed 0 > sla 0 is false, so 0 would actually pass —
	// but the chain.sla() accessor is tested directly below).
	if chain.sla() != defaultSLA {
		t.Fatalf("run=%s: expected default sla=%v, got %v", testRunID, defaultSLA, chain.sla())
	}
}

// ── helper ────────────────────────────────────────────────────────────────────

// contains reports whether sub appears in s.
// Uses stdlib strings.Contains to avoid reimplementing substring search.
func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
