package modelretry

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
)

// runUUID returns a random run identifier for observability logging.
func runUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("runUUID: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// recordingAttempt builds an Attempt that returns a per-model (status, body)
// from the given table and records the order of models it was asked about.
func recordingAttempt(table map[string][2]any, order *[]string) Attempt {
	return func(model string) (int, string) {
		*order = append(*order, model)
		v, ok := table[model]
		if !ok {
			return 599, "missing:" + model
		}
		return v[0].(int), v[1].(string)
	}
}

// Clause 1: Primary returns 429, a fallback returns 200.
// Run must fall over to the fallback and return its 200 body; the trace must
// show primary(429)->fallback(200) in order.
//
// Mutation: if 429 were treated as non-retryable (abort after primary), the run
// would surface 429 / ErrNonRetryable and never reach the fallback's 200 — this
// test fails. The body/winner assertions derive from the fallback's 200 entry.
func TestRun_PrimaryRateLimited_FallsOverToFallback200(t *testing.T) {
	uuid := runUUID(t)
	models := []string{"primary", "fallback"}
	var order []string
	attempt := recordingAttempt(map[string][2]any{
		"primary":  {429, "rate-limited-body"},
		"fallback": {200, "served-by-fallback"},
	}, &order)

	// before: primary is expected to report 429.
	t.Logf("run=%s before: primary expected status=429 (retryable, fall through)", uuid)

	res, err := Run(models, attempt)
	if err != nil {
		t.Fatalf("run=%s Run returned unexpected error: %v", uuid, err)
	}

	// after: fallback served with 200.
	t.Logf("run=%s after: winner=%s status=%d body=%q trace=%+v",
		uuid, res.Model, res.Status, res.Body, res.Trace)

	if res.Status != 200 {
		t.Fatalf("run=%s winner status = %d, want 200", uuid, res.Status)
	}
	if res.Model != "fallback" {
		t.Fatalf("run=%s winner model = %q, want fallback", uuid, res.Model)
	}
	if res.Body != "served-by-fallback" {
		t.Fatalf("run=%s winner body = %q, want served-by-fallback", uuid, res.Body)
	}

	// Trace must be exactly primary(429,retryable) -> fallback(200,not retryable).
	if len(res.Trace) != 2 {
		t.Fatalf("run=%s trace len = %d, want 2: %+v", uuid, len(res.Trace), res.Trace)
	}
	if res.Trace[0] != (AttemptRecord{Model: "primary", Status: 429, Retryable: true}) {
		t.Fatalf("run=%s trace[0] = %+v, want primary/429/retryable", uuid, res.Trace[0])
	}
	if res.Trace[1] != (AttemptRecord{Model: "fallback", Status: 200, Retryable: false}) {
		t.Fatalf("run=%s trace[1] = %+v, want fallback/200/not-retryable", uuid, res.Trace[1])
	}
	// before=429 from primary, after=200 from fallback (ordering check).
	if res.Trace[0].Status != 429 || res.Trace[1].Status != 200 {
		t.Fatalf("run=%s expected ordered 429->200, got %d->%d",
			uuid, res.Trace[0].Status, res.Trace[1].Status)
	}
	// The fallback must actually have been invoked (order proves fallthrough).
	if len(order) != 2 || order[0] != "primary" || order[1] != "fallback" {
		t.Fatalf("run=%s attempt order = %v, want [primary fallback]", uuid, order)
	}
}

// Clause 2: A non-retryable 400 from the primary ABORTS — it must NOT try the
// fallback — and the 400 is surfaced.
//
// Mutation: if 400 were treated as retryable (fall through), the fallback would
// be invoked and served 200, so res.Status would be 200 and the abort error
// would be nil — this test fails on both the error and the no-fallthrough check.
func TestRun_PrimaryBadRequest_AbortsWithoutFallback(t *testing.T) {
	uuid := runUUID(t)
	models := []string{"primary", "fallback"}
	var order []string
	attempt := recordingAttempt(map[string][2]any{
		"primary":  {400, "bad-request-body"},
		"fallback": {200, "should-never-be-served"},
	}, &order)

	t.Logf("run=%s before: primary expected status=400 (non-retryable, abort)", uuid)

	res, err := Run(models, attempt)

	t.Logf("run=%s after: status=%d model=%s err=%v order=%v trace=%+v",
		uuid, res.Status, res.Model, err, order, res.Trace)

	if err == nil {
		t.Fatalf("run=%s expected non-retryable abort error, got nil", uuid)
	}
	if !errors.Is(err, ErrNonRetryable) {
		t.Fatalf("run=%s error = %v, want wrapping ErrNonRetryable", uuid, err)
	}
	if res.Status != 400 {
		t.Fatalf("run=%s surfaced status = %d, want 400", uuid, res.Status)
	}
	if res.Model != "primary" {
		t.Fatalf("run=%s aborting model = %q, want primary", uuid, res.Model)
	}
	// Crucial no-fallthrough check: only the primary may have been attempted.
	if len(order) != 1 || order[0] != "primary" {
		t.Fatalf("run=%s attempt order = %v, want [primary] only (no fallback)", uuid, order)
	}
	if len(res.Trace) != 1 || res.Trace[0].Status != 400 || res.Trace[0].Retryable {
		t.Fatalf("run=%s trace = %+v, want single primary/400/not-retryable", uuid, res.Trace)
	}
}

// Clause 3: All models retryable-fail -> a typed ErrAllFailed with the full
// trace of every attempt.
//
// Mutation: if any of these statuses (429, 503, 500) were misclassified as
// success or as a non-retryable abort, the run would not reach ErrAllFailed and
// the full 3-entry trace assertion would fail.
func TestRun_AllRetryableFail_ErrAllFailedWithFullTrace(t *testing.T) {
	uuid := runUUID(t)
	models := []string{"m1", "m2", "m3"}
	var order []string
	attempt := recordingAttempt(map[string][2]any{
		"m1": {429, "rl"},
		"m2": {503, "unavailable"},
		"m3": {500, "server-error"},
	}, &order)

	t.Logf("run=%s before: all models expected retryable statuses 429,503,500", uuid)

	res, err := Run(models, attempt)

	t.Logf("run=%s after: err=%v lastStatus=%d trace=%+v", uuid, err, res.Status, res.Trace)

	if err == nil {
		t.Fatalf("run=%s expected ErrAllFailed, got nil", uuid)
	}
	if !errors.Is(err, ErrAllFailed) {
		t.Fatalf("run=%s error = %v, want wrapping ErrAllFailed", uuid, err)
	}
	// Full trace: every model tried, in order, all marked retryable.
	if len(res.Trace) != 3 {
		t.Fatalf("run=%s trace len = %d, want 3: %+v", uuid, len(res.Trace), res.Trace)
	}
	want := []AttemptRecord{
		{Model: "m1", Status: 429, Retryable: true},
		{Model: "m2", Status: 503, Retryable: true},
		{Model: "m3", Status: 500, Retryable: true},
	}
	for i := range want {
		if res.Trace[i] != want[i] {
			t.Fatalf("run=%s trace[%d] = %+v, want %+v", uuid, i, res.Trace[i], want[i])
		}
	}
	if len(order) != 3 {
		t.Fatalf("run=%s attempt order len = %d, want 3 (all tried): %v", uuid, len(order), order)
	}
}

// Mutation guard for IsRetryable boundaries: pins the exact retryable set so a
// flipped boundary (e.g. including 200/400 or excluding 429/5xx) is caught.
func TestIsRetryable_BoundaryClassification(t *testing.T) {
	uuid := runUUID(t)
	cases := []struct {
		status int
		want   bool
	}{
		{200, false}, // success, not retryable
		{400, false}, // non-retryable abort
		{401, false},
		{404, false},
		{429, true},  // rate limited -> retryable
		{499, false}, // 4xx (non-429) not retryable
		{500, true},
		{503, true},
		{599, true},
		{600, false}, // out of 5xx band
	}
	for _, c := range cases {
		got := IsRetryable(c.status)
		t.Logf("run=%s IsRetryable(%d)=%v want=%v", uuid, c.status, got, c.want)
		if got != c.want {
			t.Fatalf("run=%s IsRetryable(%d) = %v, want %v", uuid, c.status, got, c.want)
		}
	}
}

// Empty model list is a distinct, non-panicking error.
func TestRun_NoModels(t *testing.T) {
	uuid := runUUID(t)
	res, err := Run(nil, func(string) (int, string) { return 200, "x" })
	t.Logf("run=%s NoModels: err=%v trace=%+v", uuid, err, res.Trace)
	if !errors.Is(err, ErrNoModels) {
		t.Fatalf("run=%s error = %v, want ErrNoModels", uuid, err)
	}
}
