package providerchain

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// This file is an adversarial audit of the providerchain fallback orchestrator.
// It pins the load-bearing contract with a single SHARED recording fake that logs
// every Attempt invocation in call order, so we can prove — not merely infer from
// Result fields — the exact provider call sequence, stop-on-success (no later tier
// is touched once one wins), error surfacing, classifier semantics, and duplicate
// handling.
//
// All clocks are injected (flat/step) per the package's CLAUDE-2 determinism rule;
// the real wall clock is never consulted.

const advRunID = "adv-1a2b3c4d-providerchain-audit"

// recorder logs, in call order, the name of every tier whose Attempt fired.
type recorder struct {
	calls []string
}

// attempt builds an Attempt bound to this recorder that records `name` on each
// invocation and then returns the supplied (out, err).
func (r *recorder) attempt(name, out string, err error) Attempt {
	return func(_ context.Context) (string, error) {
		r.calls = append(r.calls, name)
		return out, err
	}
}

// ── CENTERPIECE 1: exact call order across a multi-tier cascade ────────────────
//
// Three tiers all return retriable errors and a fourth succeeds. The SHARED
// recorder must observe the providers fired strictly in declared order:
// A, B, C, D — never reordered, never skipped, never out of sequence.
func TestAdversarial_ExactCallOrder(t *testing.T) {
	rec := &recorder{}
	chain := &Chain{
		Tiers: []Tier{
			{Name: "A", Attempt: rec.attempt("A", "", errors.New("429"))},
			{Name: "B", Attempt: rec.attempt("B", "", errors.New("429"))},
			{Name: "C", Attempt: rec.attempt("C", "", errors.New("503"))},
			{Name: "D", Attempt: rec.attempt("D", "winner-D", nil)},
		},
		Classifier: DefaultClassifier,
		Now:        flatClock(time.Unix(1_000, 0)),
		SLA:        10 * time.Second,
	}

	res, err := chain.Execute(context.Background(), advRunID)
	t.Logf("run=%s call-order=%v winner=%q cascade=%v err=%v",
		advRunID, rec.calls, res.WinnerTier, res.CascadePath, err)

	if err != nil {
		t.Fatalf("run=%s: unexpected error: %v", advRunID, err)
	}
	wantOrder := []string{"A", "B", "C", "D"}
	if !reflect.DeepEqual(rec.calls, wantOrder) {
		t.Fatalf("run=%s: providers fired OUT OF ORDER: got %v, want %v",
			advRunID, rec.calls, wantOrder)
	}
	// CascadePath must mirror the real invocation order exactly.
	if !reflect.DeepEqual(res.CascadePath, wantOrder) {
		t.Fatalf("run=%s: CascadePath %v != actual call order %v",
			advRunID, res.CascadePath, rec.calls)
	}
	if res.WinnerTier != "D" || res.Output != "winner-D" {
		t.Fatalf("run=%s: wrong winner/output: %q/%q", advRunID, res.WinnerTier, res.Output)
	}
}

// ── CENTERPIECE 2: stop-on-success — no LATER tier is invoked once one wins ────
//
// The first tier succeeds. The recorder must show ONLY "first" was ever called;
// the two trailing tiers must remain untouched. This is stronger than the
// existing TestFirstTierSucceeds, which only inspects Result fields and never
// proves the later Attempts were not executed.
func TestAdversarial_StopsOnFirstSuccess_NoLaterTierInvoked(t *testing.T) {
	rec := &recorder{}
	chain := &Chain{
		Tiers: []Tier{
			{Name: "first", Attempt: rec.attempt("first", "ok-first", nil)},
			{Name: "second", Attempt: rec.attempt("second", "ok-second", nil)},
			{Name: "third", Attempt: rec.attempt("third", "ok-third", nil)},
		},
		Now: flatClock(time.Unix(2_000, 0)),
		SLA: 10 * time.Second,
	}

	res, err := chain.Execute(context.Background(), advRunID)
	t.Logf("run=%s call-order=%v winner=%q", advRunID, rec.calls, res.WinnerTier)

	if err != nil {
		t.Fatalf("run=%s: unexpected error: %v", advRunID, err)
	}
	// MUTATION KILL: if the success path does not `return` (keeps looping), the
	// recorder would contain "second"/"third" too.
	if !reflect.DeepEqual(rec.calls, []string{"first"}) {
		t.Fatalf("run=%s: NOT stopping on first success — later tiers were invoked: %v",
			advRunID, rec.calls)
	}
	if res.WinnerTier != "first" || res.Output != "ok-first" {
		t.Fatalf("run=%s: winner must be the FIRST success, got %q/%q",
			advRunID, res.WinnerTier, res.Output)
	}
	if !reflect.DeepEqual(res.CascadePath, []string{"first"}) {
		t.Fatalf("run=%s: cascade must end at first winner, got %v", advRunID, res.CascadePath)
	}
}

// stop-on-success for a MID-chain winner: a retriable first tier, a winning
// second tier, and a trailing third tier that must never fire.
func TestAdversarial_StopsOnMidChainSuccess(t *testing.T) {
	rec := &recorder{}
	chain := &Chain{
		Tiers: []Tier{
			{Name: "t0", Attempt: rec.attempt("t0", "", errors.New("429"))},
			{Name: "t1", Attempt: rec.attempt("t1", "win-t1", nil)},
			{Name: "t2", Attempt: rec.attempt("t2", "ok-t2", nil)},
		},
		Classifier: DefaultClassifier,
		Now:        flatClock(time.Unix(2_500, 0)),
		SLA:        10 * time.Second,
	}

	res, err := chain.Execute(context.Background(), advRunID)
	t.Logf("run=%s call-order=%v winner=%q", advRunID, rec.calls, res.WinnerTier)

	if err != nil {
		t.Fatalf("run=%s: unexpected error: %v", advRunID, err)
	}
	if !reflect.DeepEqual(rec.calls, []string{"t0", "t1"}) {
		t.Fatalf("run=%s: expected only t0,t1 to fire (t2 must be skipped), got %v",
			advRunID, rec.calls)
	}
	if res.WinnerTier != "t1" || res.Output != "win-t1" {
		t.Fatalf("run=%s: wrong mid-chain winner: %q/%q", advRunID, res.WinnerTier, res.Output)
	}
}

// ── DEDUP behavior: pin the ACTUAL contract (no dedup) ─────────────────────────
//
// The package documents "tries each tier in order" and performs NO deduplication.
// A provider listed twice (same Name, distinct Attempt closures) is therefore
// attempted twice when the first instance fails retriably. This test pins that
// real behavior so a future silent change to dedup-or-not is caught. If product
// intent were "dedup", THIS test failing would be the signal to revisit — but as
// written and documented today, both instances must fire.
func TestAdversarial_DuplicateTierIsNotDeduplicated(t *testing.T) {
	rec := &recorder{}
	chain := &Chain{
		Tiers: []Tier{
			{Name: "dup", Attempt: rec.attempt("dup#1", "", errors.New("429"))},
			{Name: "dup", Attempt: rec.attempt("dup#2", "win", nil)},
		},
		Classifier: DefaultClassifier,
		Now:        flatClock(time.Unix(3_000, 0)),
		SLA:        10 * time.Second,
	}

	res, err := chain.Execute(context.Background(), advRunID)
	t.Logf("run=%s call-order=%v cascade=%v winner=%q", advRunID, rec.calls, res.CascadePath, res.WinnerTier)

	if err != nil {
		t.Fatalf("run=%s: unexpected error: %v", advRunID, err)
	}
	// Both physical attempts must fire (no dedup). The recorder distinguishes them.
	if !reflect.DeepEqual(rec.calls, []string{"dup#1", "dup#2"}) {
		t.Fatalf("run=%s: expected BOTH duplicate attempts to fire (no dedup), got %v",
			advRunID, rec.calls)
	}
	// CascadePath records the Name twice — proving no name-level dedup either.
	if !reflect.DeepEqual(res.CascadePath, []string{"dup", "dup"}) {
		t.Fatalf("run=%s: expected cascade [dup dup], got %v", advRunID, res.CascadePath)
	}
	if res.WinnerTier != "dup" || res.Output != "win" {
		t.Fatalf("run=%s: duplicate-tier winner wrong: %q/%q", advRunID, res.WinnerTier, res.Output)
	}
}

// ── ERROR SURFACING: all-fail aggregates EVERY tier error in declared order ────
//
// When every tier fails retriably the AggregateError must carry one TierError per
// tier, in the same order they were attempted, each wrapping the correct cause.
func TestAdversarial_AllFail_AggregatesEveryErrorInOrder(t *testing.T) {
	rec := &recorder{}
	eA := errors.New("err-A")
	eB := errors.New("err-B")
	eC := errors.New("err-C")
	chain := &Chain{
		Tiers: []Tier{
			{Name: "A", Attempt: rec.attempt("A", "", eA)},
			{Name: "B", Attempt: rec.attempt("B", "", eB)},
			{Name: "C", Attempt: rec.attempt("C", "", eC)},
		},
		Classifier: DefaultClassifier,
		Now:        flatClock(time.Unix(4_000, 0)),
		SLA:        10 * time.Second,
	}

	res, err := chain.Execute(context.Background(), advRunID)
	t.Logf("run=%s call-order=%v err=%v", advRunID, rec.calls, err)

	var ae *AggregateError
	if !errors.As(err, &ae) {
		t.Fatalf("run=%s: expected *AggregateError, got %T: %v", advRunID, err, err)
	}
	if len(ae.Tiers) != 3 {
		t.Fatalf("run=%s: expected 3 tier errors, got %d", advRunID, len(ae.Tiers))
	}
	// Order + cause correctness: TierError[i] must name tier i and wrap cause i.
	wantNames := []string{"A", "B", "C"}
	wantCauses := []error{eA, eB, eC}
	for i := range wantNames {
		if ae.Tiers[i].TierName != wantNames[i] {
			t.Fatalf("run=%s: TierError[%d].TierName=%q, want %q",
				advRunID, i, ae.Tiers[i].TierName, wantNames[i])
		}
		if !errors.Is(ae.Tiers[i].Cause, wantCauses[i]) {
			t.Fatalf("run=%s: TierError[%d].Cause=%v, want %v",
				advRunID, i, ae.Tiers[i].Cause, wantCauses[i])
		}
		if ae.Tiers[i].Class != Retriable {
			t.Fatalf("run=%s: TierError[%d].Class should be Retriable", advRunID, i)
		}
	}
	// errors.Is must match the sentinel-style AggregateError target.
	if !errors.Is(err, &AggregateError{}) {
		t.Fatalf("run=%s: errors.Is(err, &AggregateError{}) must be true", advRunID)
	}
	if res.WinnerTier != "" || res.Output != "" {
		t.Fatalf("run=%s: all-fail must surface empty winner, got %q/%q",
			advRunID, res.WinnerTier, res.Output)
	}
}

// ── ERROR SURFACING: a mid-chain success MASKS earlier retriable errors ────────
//
// When an earlier tier fails retriably but a later tier wins, the returned error
// MUST be nil and the Result must carry only the winner — the earlier failure
// must NOT leak into the success result.
func TestAdversarial_SuccessMasksEarlierErrors(t *testing.T) {
	rec := &recorder{}
	chain := &Chain{
		Tiers: []Tier{
			{Name: "flaky", Attempt: rec.attempt("flaky", "", errors.New("boom"))},
			{Name: "good", Attempt: rec.attempt("good", "good-out", nil)},
		},
		Classifier: DefaultClassifier,
		Now:        flatClock(time.Unix(5_000, 0)),
		SLA:        10 * time.Second,
	}

	res, err := chain.Execute(context.Background(), advRunID)
	t.Logf("run=%s winner=%q output=%q err=%v", advRunID, res.WinnerTier, res.Output, err)

	if err != nil {
		t.Fatalf("run=%s: earlier error must be masked by later success, got err=%v", advRunID, err)
	}
	if res.WinnerTier != "good" || res.Output != "good-out" {
		t.Fatalf("run=%s: wrong winner after masking: %q/%q", advRunID, res.WinnerTier, res.Output)
	}
}

// ── CLASSIFICATION: terminal on a MIDDLE tier aborts; later tiers untouched ────
//
// A retriable first tier advances; a terminal second tier must stop the chain so
// the third (which would otherwise win) is never invoked, and the surfaced
// AggregateError records exactly the two attempted tiers with the 2nd Terminal.
func TestAdversarial_TerminalMidChainAborts(t *testing.T) {
	rec := &recorder{}
	termMsg := "fatal-auth"
	chain := &Chain{
		Tiers: []Tier{
			{Name: "t0", Attempt: rec.attempt("t0", "", errors.New("429"))},
			{Name: "t1", Attempt: rec.attempt("t1", "", errors.New(termMsg))},
			{Name: "t2", Attempt: rec.attempt("t2", "would-win", nil)},
		},
		Classifier: classifierFor(termMsg),
		Now:        flatClock(time.Unix(6_000, 0)),
		SLA:        10 * time.Second,
	}

	res, err := chain.Execute(context.Background(), advRunID)
	t.Logf("run=%s call-order=%v cascade=%v err=%v", advRunID, rec.calls, res.CascadePath, err)

	// MUTATION KILL: if Terminal did not stop the loop, t2 would fire and win.
	if !reflect.DeepEqual(rec.calls, []string{"t0", "t1"}) {
		t.Fatalf("run=%s: terminal must abort before t2, got call order %v", advRunID, rec.calls)
	}
	var ae *AggregateError
	if !errors.As(err, &ae) {
		t.Fatalf("run=%s: expected *AggregateError, got %T", advRunID, err)
	}
	if len(ae.Tiers) != 2 {
		t.Fatalf("run=%s: expected 2 tier errors (t0 retriable, t1 terminal), got %d", advRunID, len(ae.Tiers))
	}
	if ae.Tiers[0].Class != Retriable || ae.Tiers[1].Class != Terminal {
		t.Fatalf("run=%s: classes wrong: t0=%v t1=%v", advRunID, ae.Tiers[0].Class, ae.Tiers[1].Class)
	}
	if res.WinnerTier != "" {
		t.Fatalf("run=%s: terminal abort must have no winner, got %q", advRunID, res.WinnerTier)
	}
}

// ── CLASSIFICATION: a RETRIABLE error on the LAST tier exhausts (no abort) ─────
//
// Mirror of the terminal case: the same error message classified as Retriable
// must fall through to the next tier rather than aborting. Pins that the
// classifier decision — not the error identity — drives abort-vs-advance.
func TestAdversarial_RetriableVsTerminal_SameMessageDivergentBehavior(t *testing.T) {
	const msg = "ambiguous-503"

	// Variant 1: classified Retriable -> advances and the 2nd tier wins.
	recR := &recorder{}
	chainR := &Chain{
		Tiers: []Tier{
			{Name: "p", Attempt: recR.attempt("p", "", errors.New(msg))},
			{Name: "q", Attempt: recR.attempt("q", "q-win", nil)},
		},
		Classifier: DefaultClassifier, // everything retriable
		Now:        flatClock(time.Unix(7_000, 0)),
		SLA:        10 * time.Second,
	}
	resR, errR := chainR.Execute(context.Background(), advRunID)
	if errR != nil || resR.WinnerTier != "q" {
		t.Fatalf("run=%s: retriable variant should advance and win at q, got winner=%q err=%v",
			advRunID, resR.WinnerTier, errR)
	}
	if !reflect.DeepEqual(recR.calls, []string{"p", "q"}) {
		t.Fatalf("run=%s: retriable variant call order %v != [p q]", advRunID, recR.calls)
	}

	// Variant 2: SAME message classified Terminal -> aborts, q never fires.
	recT := &recorder{}
	chainT := &Chain{
		Tiers: []Tier{
			{Name: "p", Attempt: recT.attempt("p", "", errors.New(msg))},
			{Name: "q", Attempt: recT.attempt("q", "q-win", nil)},
		},
		Classifier: classifierFor(msg), // this exact message is terminal
		Now:        flatClock(time.Unix(7_500, 0)),
		SLA:        10 * time.Second,
	}
	_, errT := chainT.Execute(context.Background(), advRunID)
	var ae *AggregateError
	if !errors.As(errT, &ae) {
		t.Fatalf("run=%s: terminal variant should AggregateError, got %v", advRunID, errT)
	}
	if !reflect.DeepEqual(recT.calls, []string{"p"}) {
		t.Fatalf("run=%s: terminal variant must NOT fire q, got %v", advRunID, recT.calls)
	}
}

// ── EDGE: single tier that fails retriably exhausts to AggregateError ──────────
func TestAdversarial_SingleTierRetriableExhausts(t *testing.T) {
	rec := &recorder{}
	chain := &Chain{
		Tiers: []Tier{
			{Name: "solo", Attempt: rec.attempt("solo", "", errors.New("429"))},
		},
		Classifier: DefaultClassifier,
		Now:        flatClock(time.Unix(8_000, 0)),
		SLA:        10 * time.Second,
	}
	res, err := chain.Execute(context.Background(), advRunID)
	t.Logf("run=%s calls=%v err=%v", advRunID, rec.calls, err)

	var ae *AggregateError
	if !errors.As(err, &ae) {
		t.Fatalf("run=%s: single retriable tier should exhaust to AggregateError, got %v", advRunID, err)
	}
	if !reflect.DeepEqual(rec.calls, []string{"solo"}) {
		t.Fatalf("run=%s: solo tier must fire exactly once, got %v", advRunID, rec.calls)
	}
	if len(ae.Tiers) != 1 || ae.Tiers[0].TierName != "solo" {
		t.Fatalf("run=%s: aggregate should name solo once, got %v", advRunID, ae.Tiers)
	}
	if res.RunID != advRunID {
		t.Fatalf("run=%s: RunID not propagated on exhaustion, got %q", advRunID, res.RunID)
	}
}

// ── EDGE: a tier with a nil Attempt panics — documents the no-guard contract ───
//
// The package does not guard against a nil Attempt; calling it would nil-deref.
// We assert that the FIRST reachable nil Attempt panics, pinning that callers are
// responsible for populating Attempt (rather than the chain silently skipping it,
// which would be a stop-on-success-adjacent correctness hazard).
func TestAdversarial_NilAttemptPanics(t *testing.T) {
	chain := &Chain{
		Tiers: []Tier{
			{Name: "nil-tier", Attempt: nil},
		},
		Now: flatClock(time.Unix(9_000, 0)),
		SLA: 10 * time.Second,
	}

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("run=%s: expected panic from nil Attempt, got none", advRunID)
		} else {
			t.Logf("run=%s: nil Attempt panicked as documented: %v", advRunID, r)
		}
	}()

	_, _ = chain.Execute(context.Background(), advRunID)
}

// ── CONTEXT propagation: the SAME ctx value reaches every attempted tier ───────
func TestAdversarial_ContextPropagatedToEveryTier(t *testing.T) {
	type ctxKey string
	const k ctxKey = "trace"
	parent := context.WithValue(context.Background(), k, "vXYZ")

	seen := make([]string, 0, 2)
	mk := func(name, out string, err error) Attempt {
		return func(ctx context.Context) (string, error) {
			v, _ := ctx.Value(k).(string)
			seen = append(seen, name+":"+v)
			return out, err
		}
	}
	chain := &Chain{
		Tiers: []Tier{
			{Name: "first", Attempt: mk("first", "", errors.New("429"))},
			{Name: "second", Attempt: mk("second", "ok", nil)},
		},
		Classifier: DefaultClassifier,
		Now:        flatClock(time.Unix(10_000, 0)),
		SLA:        10 * time.Second,
	}

	if _, err := chain.Execute(parent, advRunID); err != nil {
		t.Fatalf("run=%s: unexpected error: %v", advRunID, err)
	}
	want := []string{"first:vXYZ", "second:vXYZ"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("run=%s: ctx value not propagated to every tier: got %v want %v", advRunID, seen, want)
	}
}
