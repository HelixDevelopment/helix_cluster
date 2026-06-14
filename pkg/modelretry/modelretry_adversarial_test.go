package modelretry

import (
	"errors"
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// CENTERPIECE A: IsRetryable exhaustive boundary matrix.
//
// The documented contract is precise: the retryable set is EXACTLY
// {429} ∪ {500..599}. Everything else is terminal (success or non-retryable
// abort). This table pins both sides at every interesting boundary so a flipped
// comparison, an off-by-one band edge, or an "always true/false" mutation is
// caught immediately.
// ---------------------------------------------------------------------------

func TestIsRetryable_ExhaustiveBoundaryMatrix(t *testing.T) {
	cases := []struct {
		status int
		want   bool
		note   string
	}{
		// Degenerate / non-HTTP inputs are terminal (never retryable).
		{-1, false, "negative"},
		{0, false, "zero"},
		{100, false, "1xx informational"},
		{199, false, "1xx upper"},

		// 2xx success band: terminal (a win, not a retry).
		{200, false, "OK success"},
		{201, false, "Created"},
		{204, false, "No Content"},
		{299, false, "2xx upper"},

		// 3xx redirect band: terminal.
		{300, false, "Multiple Choices"},
		{301, false, "Moved Permanently"},
		{399, false, "3xx upper"},

		// 4xx client errors: terminal EXCEPT 429.
		{400, false, "Bad Request"},
		{401, false, "Unauthorized"},
		{403, false, "Forbidden"},
		{404, false, "Not Found"},
		{428, false, "Precondition Required (429 lower neighbor)"},
		{429, true, "Too Many Requests -> RETRYABLE"},
		{430, false, "429 upper neighbor"},
		{451, false, "Unavailable For Legal Reasons"},
		{499, false, "4xx upper (non-429)"},

		// 5xx server errors: ALL retryable.
		{500, true, "Internal Server Error -> RETRYABLE"},
		{501, true, "Not Implemented -> RETRYABLE"},
		{502, true, "Bad Gateway -> RETRYABLE"},
		{503, true, "Service Unavailable -> RETRYABLE"},
		{504, true, "Gateway Timeout -> RETRYABLE"},
		{550, true, "mid 5xx -> RETRYABLE"},
		{599, true, "5xx upper -> RETRYABLE"},

		// Out of the 5xx band on the high side: terminal again.
		{600, false, "just above 5xx band"},
		{601, false, "well above 5xx band"},
		{999, false, "far above"},
	}

	for _, c := range cases {
		got := IsRetryable(c.status)
		if got != c.want {
			t.Errorf("IsRetryable(%d) = %v, want %v [%s]", c.status, got, c.want, c.note)
		}
	}
}

// Density sweep across the entire 0..699 range, asserting against an independent
// oracle that mirrors the documented set. This is the strongest anti-mutation
// net: any boundary shift anywhere in the range produces a mismatch.
func TestIsRetryable_FullRangeOracle(t *testing.T) {
	oracle := func(s int) bool { return s == 429 || (s >= 500 && s <= 599) }
	for s := 0; s <= 699; s++ {
		if got := IsRetryable(s); got != oracle(s) {
			t.Fatalf("IsRetryable(%d) = %v, oracle = %v (boundary drift)", s, got, oracle(s))
		}
	}
}

// ---------------------------------------------------------------------------
// Recording fake: records exactly which models were invoked, in order, and
// serves a per-model (status, body). Any model not in the table is a test bug
// (we fail loudly) rather than a silent default, so "never called" assertions
// are unambiguous.
// ---------------------------------------------------------------------------

type recordingFake struct {
	t     *testing.T
	table map[string]struct {
		status int
		body   string
	}
	calls []string
}

func newRecordingFake(t *testing.T) *recordingFake {
	return &recordingFake{
		t: t,
		table: map[string]struct {
			status int
			body   string
		}{},
	}
}

func (r *recordingFake) set(model string, status int, body string) *recordingFake {
	r.table[model] = struct {
		status int
		body   string
	}{status, body}
	return r
}

func (r *recordingFake) fn() Attempt {
	return func(model string) (int, string) {
		r.calls = append(r.calls, model)
		v, ok := r.table[model]
		if !ok {
			r.t.Fatalf("recordingFake: unexpected model %q invoked (not in table)", model)
		}
		return v.status, v.body
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE B: abort-on-terminal — a terminal primary must NOT invoke any
// fallback. Driven over EVERY terminal class (each distinct 4xx + a success-is-
// not-terminal control handled elsewhere). The recording fake proves the
// fallback was never called.
// ---------------------------------------------------------------------------

func TestRun_TerminalPrimary_NeverCallsFallback(t *testing.T) {
	// Each of these statuses must abort at the primary with ErrNonRetryable and
	// leave the fallback untouched.
	terminal := []int{400, 401, 403, 404, 405, 410, 422, 428, 430, 451, 499, 600}

	for _, st := range terminal {
		t.Run(fmt.Sprintf("status_%d", st), func(t *testing.T) {
			fake := newRecordingFake(t)
			fake.set("primary", st, "terminal-body").
				set("fallback", 200, "MUST-NOT-BE-SERVED")

			res, err := Run([]string{"primary", "fallback"}, fake.fn())

			if err == nil {
				t.Fatalf("status %d: expected terminal abort error, got nil (res=%+v)", st, res)
			}
			if !errors.Is(err, ErrNonRetryable) {
				t.Fatalf("status %d: err = %v, want wrapping ErrNonRetryable", st, err)
			}
			if res.Status != st {
				t.Fatalf("status %d: surfaced status = %d, want %d", st, res.Status, st)
			}
			if res.Model != "primary" {
				t.Fatalf("status %d: aborting model = %q, want primary", st, res.Model)
			}
			if res.Body != "terminal-body" {
				t.Fatalf("status %d: surfaced body = %q, want terminal-body", st, res.Body)
			}
			// THE load-bearing assertion: fallback was never invoked.
			if len(fake.calls) != 1 || fake.calls[0] != "primary" {
				t.Fatalf("status %d: invocations = %v, want [primary] ONLY (fallback must not run)", st, fake.calls)
			}
			// Trace carries exactly the one aborting attempt.
			if len(res.Trace) != 1 {
				t.Fatalf("status %d: trace len = %d, want 1: %+v", st, len(res.Trace), res.Trace)
			}
			if res.Trace[0] != (AttemptRecord{Model: "primary", Status: st, Retryable: false}) {
				t.Fatalf("status %d: trace[0] = %+v, want primary/%d/not-retryable", st, res.Trace[0], st)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE C: fallover-on-retryable — each retryable status at the primary
// DOES invoke the next model, and the first 200 wins and stops the chain.
// ---------------------------------------------------------------------------

func TestRun_RetryablePrimary_FallsOverAndFirstSuccessWins(t *testing.T) {
	retryable := []int{429, 500, 501, 502, 503, 504, 599}

	for _, st := range retryable {
		t.Run(fmt.Sprintf("status_%d", st), func(t *testing.T) {
			fake := newRecordingFake(t)
			fake.set("primary", st, "retryable-body").
				set("fallback", 200, "served-by-fallback")

			res, err := Run([]string{"primary", "fallback"}, fake.fn())
			if err != nil {
				t.Fatalf("status %d: unexpected error: %v", st, err)
			}
			if res.Status != 200 || res.Model != "fallback" || res.Body != "served-by-fallback" {
				t.Fatalf("status %d: winner = %+v, want fallback/200/served-by-fallback", st, res)
			}
			if len(fake.calls) != 2 || fake.calls[0] != "primary" || fake.calls[1] != "fallback" {
				t.Fatalf("status %d: invocations = %v, want [primary fallback]", st, fake.calls)
			}
			if len(res.Trace) != 2 {
				t.Fatalf("status %d: trace len = %d, want 2: %+v", st, len(res.Trace), res.Trace)
			}
			if !res.Trace[0].Retryable || res.Trace[0].Status != st {
				t.Fatalf("status %d: trace[0] = %+v, want retryable %d", st, res.Trace[0], st)
			}
			if res.Trace[1].Retryable || res.Trace[1].Status != 200 {
				t.Fatalf("status %d: trace[1] = %+v, want non-retryable 200", st, res.Trace[1])
			}
		})
	}
}

// First success wins and STOPS: a third model after a 200 winner must never run.
func TestRun_FirstSuccessStopsChain(t *testing.T) {
	fake := newRecordingFake(t)
	fake.set("m1", 503, "down").
		set("m2", 200, "served").
		set("m3", 200, "MUST-NOT-RUN")

	res, err := Run([]string{"m1", "m2", "m3"}, fake.fn())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "m2" || res.Status != 200 {
		t.Fatalf("winner = %+v, want m2/200", res)
	}
	if len(fake.calls) != 2 || fake.calls[0] != "m1" || fake.calls[1] != "m2" {
		t.Fatalf("invocations = %v, want [m1 m2] (m3 must not run after winner)", fake.calls)
	}
}

// A terminal status that appears MID-CHAIN (after some retryable fallthrough)
// must still abort and not advance to later models.
func TestRun_TerminalMidChain_AbortsAndStops(t *testing.T) {
	fake := newRecordingFake(t)
	fake.set("m1", 429, "rl").
		set("m2", 403, "forbidden").
		set("m3", 200, "MUST-NOT-RUN")

	res, err := Run([]string{"m1", "m2", "m3"}, fake.fn())
	if !errors.Is(err, ErrNonRetryable) {
		t.Fatalf("err = %v, want ErrNonRetryable", err)
	}
	if res.Status != 403 || res.Model != "m2" {
		t.Fatalf("surfaced = %+v, want m2/403", res)
	}
	if len(fake.calls) != 2 || fake.calls[1] != "m2" {
		t.Fatalf("invocations = %v, want [m1 m2] (m3 must not run after terminal abort)", fake.calls)
	}
	if len(res.Trace) != 2 ||
		res.Trace[0] != (AttemptRecord{Model: "m1", Status: 429, Retryable: true}) ||
		res.Trace[1] != (AttemptRecord{Model: "m2", Status: 403, Retryable: false}) {
		t.Fatalf("trace = %+v, want m1/429/retryable -> m2/403/terminal", res.Trace)
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE D: all-retryable-fail surfaces ErrAllFailed with full trace and
// terminates (no infinite loop). Bounded so a regression that loops forever is
// caught by the test timeout rather than hanging the suite silently.
// ---------------------------------------------------------------------------

func TestRun_AllRetryable_SurfacesErrAllFailed_NoInfiniteLoop(t *testing.T) {
	const n = 25
	models := make([]string, n)
	fake := newRecordingFake(t)
	for i := 0; i < n; i++ {
		m := fmt.Sprintf("m%02d", i)
		models[i] = m
		// Alternate across the retryable set so we exercise both 429 and 5xx.
		st := 503
		if i%2 == 0 {
			st = 429
		}
		fake.set(m, st, "down")
	}

	res, err := Run(models, fake.fn())
	if !errors.Is(err, ErrAllFailed) {
		t.Fatalf("err = %v, want ErrAllFailed", err)
	}
	// Every model tried exactly once, in order — proves bounded iteration.
	if len(fake.calls) != n {
		t.Fatalf("invocations = %d, want %d (each model exactly once, no looping)", len(fake.calls), n)
	}
	for i, m := range models {
		if fake.calls[i] != m {
			t.Fatalf("invocation[%d] = %q, want %q (order drift)", i, fake.calls[i], m)
		}
	}
	if len(res.Trace) != n {
		t.Fatalf("trace len = %d, want %d", len(res.Trace), n)
	}
	for _, rec := range res.Trace {
		if !rec.Retryable {
			t.Fatalf("trace entry %+v not marked retryable", rec)
		}
	}
	// Result surfaces the LAST attempted model/status.
	if res.Model != models[n-1] {
		t.Fatalf("res.Model = %q, want last model %q", res.Model, models[n-1])
	}
}

// ---------------------------------------------------------------------------
// EDGES: single model, nil response/empty body, nil attemptFn, no models.
// ---------------------------------------------------------------------------

// Single model that succeeds: wins immediately, no fallback exists.
func TestRun_SingleModel_Success(t *testing.T) {
	fake := newRecordingFake(t)
	fake.set("only", 200, "ok")
	res, err := Run([]string{"only"}, fake.fn())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "only" || res.Status != 200 || res.Body != "ok" {
		t.Fatalf("res = %+v, want only/200/ok", res)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("invocations = %v, want [only]", fake.calls)
	}
}

// Single model that retryable-fails: there is no next model, so ErrAllFailed.
func TestRun_SingleModel_RetryableFail_ErrAllFailed(t *testing.T) {
	fake := newRecordingFake(t)
	fake.set("only", 503, "down")
	res, err := Run([]string{"only"}, fake.fn())
	if !errors.Is(err, ErrAllFailed) {
		t.Fatalf("err = %v, want ErrAllFailed", err)
	}
	if res.Status != 503 || res.Model != "only" {
		t.Fatalf("res = %+v, want only/503", res)
	}
	if len(res.Trace) != 1 {
		t.Fatalf("trace len = %d, want 1", len(res.Trace))
	}
}

// Single model that terminal-fails: ErrNonRetryable, abort.
func TestRun_SingleModel_TerminalFail_ErrNonRetryable(t *testing.T) {
	fake := newRecordingFake(t)
	fake.set("only", 400, "bad")
	res, err := Run([]string{"only"}, fake.fn())
	if !errors.Is(err, ErrNonRetryable) {
		t.Fatalf("err = %v, want ErrNonRetryable", err)
	}
	if res.Status != 400 {
		t.Fatalf("status = %d, want 400", res.Status)
	}
}

// Empty body on a 200 winner is preserved verbatim (no panic, no substitution).
func TestRun_EmptyBodySuccess_NoPanic(t *testing.T) {
	res, err := Run([]string{"m"}, func(string) (int, string) { return 200, "" })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Body != "" || res.Status != 200 {
		t.Fatalf("res = %+v, want empty body / 200", res)
	}
}

// nil attemptFn is rejected without panic.
func TestRun_NilAttemptFn(t *testing.T) {
	res, err := Run([]string{"m"}, nil)
	if err == nil {
		t.Fatalf("expected error for nil attemptFn, got nil (res=%+v)", res)
	}
}

// Empty (non-nil) model slice is the ErrNoModels case too.
func TestRun_EmptySliceNoModels(t *testing.T) {
	_, err := Run([]string{}, func(string) (int, string) { return 200, "x" })
	if !errors.Is(err, ErrNoModels) {
		t.Fatalf("err = %v, want ErrNoModels", err)
	}
}

// Guard: 200 must win even when it is NOT the first model and a terminal 4xx
// would otherwise be reachable — proves success short-circuits before any later
// terminal check. (m1 retryable -> m2 success wins; m3 terminal never reached.)
func TestRun_SuccessBeforeLaterTerminal(t *testing.T) {
	fake := newRecordingFake(t)
	fake.set("m1", 500, "err").
		set("m2", 200, "served").
		set("m3", 400, "MUST-NOT-RUN")
	res, err := Run([]string{"m1", "m2", "m3"}, fake.fn())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "m2" || res.Status != 200 {
		t.Fatalf("res = %+v, want m2/200", res)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("invocations = %v, want 2 (m3 unreached)", fake.calls)
	}
}
