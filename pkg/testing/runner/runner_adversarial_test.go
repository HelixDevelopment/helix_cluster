package runner

import (
	"context"
	"errors"
	"testing"
	"time"
)

// withDeadline returns a context that is guaranteed to be cancelled so a buggy
// runner can never hang the test binary.
func advCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// ─────────────────────────────────────────────────────────────────────────────
// ADVERSARIAL 1 — A panicking suite MUST be reported as FAILED, not crash the
// runner.
//
// This is the highest-value result-reporting integrity property: a single
// faulty suite (e.g. a nil deref) must not take down the orchestrator and
// discard every other suite's result. The runner must (a) return a Report at
// all (no process crash) and (b) classify the panicking suite as Failed while
// still counting the healthy suites that ran alongside it.
//
// Mutation proof: removing the recover() in runSuite turns the suite call into
// an unrecovered goroutine panic — the test binary aborts before any assertion,
// producing a non-`ok` FAIL (panic trace), so this test is load-bearing.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarial_PanicSuiteReportedFailedNotCrash(t *testing.T) {
	r := New(Options{MaxConcurrency: 3})

	if err := r.Register("healthy-1", func(ctx context.Context) Result {
		return Result{Passed: true}
	}); err != nil {
		t.Fatalf("Register healthy-1: %v", err)
	}
	if err := r.Register("healthy-2", func(ctx context.Context) Result {
		return Result{Passed: true}
	}); err != nil {
		t.Fatalf("Register healthy-2: %v", err)
	}
	if err := r.Register("panicker", func(ctx context.Context) Result {
		panic("simulated nil-deref in a test step")
	}); err != nil {
		t.Fatalf("Register panicker: %v", err)
	}

	// If the runner crashes the process this line is never reached.
	report := r.Run(advCtx(t))

	if report.Total != 3 {
		t.Fatalf("Total: want 3, got %d (panicking suite lost results?)", report.Total)
	}
	if report.Passed != 2 {
		t.Fatalf("Passed: want 2 healthy suites, got %d", report.Passed)
	}
	if report.Failed != 1 {
		t.Fatalf("Failed: want 1 (the panicker), got %d", report.Failed)
	}

	// Sink-side: the panicking suite must be present AND marked failed AND
	// carry an error explaining the panic — a PASS or a silent drop is a bluff.
	var panicker *SuiteResult
	for i := range report.Suites {
		if report.Suites[i].Name == "panicker" {
			panicker = &report.Suites[i]
		}
	}
	if panicker == nil {
		t.Fatal("panicking suite missing from Report.Suites — its result was silently dropped")
	}
	if panicker.Passed {
		t.Fatal("BLUFF: panicking suite reported as PASSED")
	}
	if panicker.Err == nil {
		t.Fatal("panicking suite must carry a non-nil Err describing the panic")
	}
	if !errors.Is(panicker.Err, ErrSuitePanicked) {
		t.Fatalf("panicking suite Err should wrap ErrSuitePanicked, got %v", panicker.Err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ADVERSARIAL 2 — One failing step among many passing MUST flip the overall
// verdict to "not all passed". Guards against an OR-vs-AND aggregation error
// where a single failure is swallowed.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarial_SingleFailureFlipsVerdict(t *testing.T) {
	r := New(Options{MaxConcurrency: 8})

	const numPass = 7
	for i := 0; i < numPass; i++ {
		name := "ok-" + string(rune('a'+i))
		if err := r.Register(name, func(ctx context.Context) Result {
			return Result{Passed: true}
		}); err != nil {
			t.Fatalf("Register %q: %v", name, err)
		}
	}
	if err := r.Register("the-one-failure", func(ctx context.Context) Result {
		return Result{Passed: false}
	}); err != nil {
		t.Fatalf("Register failure: %v", err)
	}

	report := r.Run(advCtx(t))

	if report.Total != numPass+1 {
		t.Fatalf("Total: want %d, got %d", numPass+1, report.Total)
	}
	if report.Failed != 1 {
		t.Fatalf("Failed: want 1, got %d — a failing suite was swallowed", report.Failed)
	}
	// Overall verdict: an all-pass run requires Passed == Total. One failure must
	// break that, otherwise the runner would bluff a green run on a red suite.
	allPassed := report.Failed == 0 && report.Passed == report.Total
	if allPassed {
		t.Fatal("BLUFF: overall verdict reports all-passed despite a failing suite")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ADVERSARIAL 3 — Counter conservation: Passed + Failed == Total exactly, and
// Total == number of registered suites, across a mixed batch. Catches a counter
// that double-counts or drops a result.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarial_CountersConserved(t *testing.T) {
	r := New(Options{MaxConcurrency: 4})

	sentinel := errors.New("boom")
	specs := []struct {
		name string
		res  Result
	}{
		{"p1", Result{Passed: true}},
		{"p2", Result{Passed: true}},
		{"f-flag", Result{Passed: false}},
		{"f-err", Result{Passed: true, Err: sentinel}}, // Err overrides Passed
		{"f-both", Result{Passed: false, Err: sentinel}},
	}
	for _, s := range specs {
		res := s.res
		if err := r.Register(s.name, func(ctx context.Context) Result { return res }); err != nil {
			t.Fatalf("Register %q: %v", s.name, err)
		}
	}

	report := r.Run(advCtx(t))

	if report.Total != len(specs) {
		t.Fatalf("Total: want %d, got %d", len(specs), report.Total)
	}
	if report.Passed+report.Failed != report.Total {
		t.Fatalf("counter conservation violated: Passed(%d)+Failed(%d) != Total(%d)",
			report.Passed, report.Failed, report.Total)
	}
	if report.Passed != 2 {
		t.Fatalf("Passed: want 2, got %d", report.Passed)
	}
	if report.Failed != 3 {
		t.Fatalf("Failed: want 3 (flag, err-override, both), got %d", report.Failed)
	}
	// Independently recount from the per-suite slice — the summary counters must
	// match the actual suite verdicts (no divergence between summary and detail).
	var sp, sf int
	for _, sr := range report.Suites {
		if sr.Passed {
			sp++
		} else {
			sf++
		}
	}
	if sp != report.Passed || sf != report.Failed {
		t.Fatalf("summary counters diverge from per-suite results: summary(p=%d,f=%d) detail(p=%d,f=%d)",
			report.Passed, report.Failed, sp, sf)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ADVERSARIAL 4 — An empty run is NOT a vacuous "all passed". With zero suites
// the Report must have Total==0 and Passed==0 (so callers gating on
// Passed==Total && Total>0 are safe). It must also not crash.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarial_EmptyRunNotVacuousPass(t *testing.T) {
	r := New(Options{MaxConcurrency: 4})

	report := r.Run(advCtx(t))

	if report.Total != 0 {
		t.Fatalf("empty run Total: want 0, got %d", report.Total)
	}
	if report.Passed != 0 {
		t.Fatalf("empty run Passed: want 0, got %d — vacuous success bluff", report.Passed)
	}
	if report.Failed != 0 {
		t.Fatalf("empty run Failed: want 0, got %d", report.Failed)
	}
	if len(report.Suites) != 0 {
		t.Fatalf("empty run Suites: want 0 entries, got %d", len(report.Suites))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ADVERSARIAL 5 — A suite that returns a non-nil Err must be Failed even if it
// also set Passed=true. Guards the explicit "Err overrides Passed" contract on
// the sink side.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarial_ErrOverridesPassedFlag(t *testing.T) {
	r := New(Options{MaxConcurrency: 2})
	sentinel := errors.New("crashed mid-step")

	if err := r.Register("lies-about-passing", func(ctx context.Context) Result {
		return Result{Passed: true, Err: sentinel}
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	report := r.Run(advCtx(t))

	if report.Passed != 0 {
		t.Fatalf("BLUFF: suite with non-nil Err counted as passed (Passed=%d)", report.Passed)
	}
	if report.Failed != 1 {
		t.Fatalf("Failed: want 1, got %d", report.Failed)
	}
	if len(report.Suites) != 1 || report.Suites[0].Passed {
		t.Fatalf("suite with Err must be marked Passed=false: %+v", report.Suites)
	}
	if !errors.Is(report.Suites[0].Err, sentinel) {
		t.Fatalf("suite Err not propagated to Report: got %v", report.Suites[0].Err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ADVERSARIAL 6 — A panicking suite that panics with nil... actually Go's
// recover() returns non-nil only for non-nil panics; but a suite can also panic
// with an error value. Ensure such a panic is still classified failed and does
// not crash. Also asserts a panic does not corrupt the counts of sibling
// suites.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarial_PanicWithErrorValueStillFailed(t *testing.T) {
	r := New(Options{MaxConcurrency: 2})
	panicErr := errors.New("typed panic value")

	if err := r.Register("ok", func(ctx context.Context) Result {
		return Result{Passed: true}
	}); err != nil {
		t.Fatalf("Register ok: %v", err)
	}
	if err := r.Register("panic-err", func(ctx context.Context) Result {
		panic(panicErr)
	}); err != nil {
		t.Fatalf("Register panic-err: %v", err)
	}

	report := r.Run(advCtx(t))

	if report.Total != 2 {
		t.Fatalf("Total: want 2, got %d", report.Total)
	}
	if report.Passed != 1 || report.Failed != 1 {
		t.Fatalf("want passed=1 failed=1, got passed=%d failed=%d", report.Passed, report.Failed)
	}
	for _, sr := range report.Suites {
		if sr.Name == "panic-err" {
			if sr.Passed {
				t.Fatal("BLUFF: suite that panicked reported passed")
			}
			if !errors.Is(sr.Err, ErrSuitePanicked) {
				t.Fatalf("panic Err should wrap ErrSuitePanicked, got %v", sr.Err)
			}
		}
	}
}
