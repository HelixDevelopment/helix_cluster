package streamfailover

import (
	"errors"
	"testing"
)

// recordingBackend scripts a single backend's behaviour and records every
// invocation so order / stop-on-success / no-retry can be asserted exactly.
type recordingBackend struct {
	name string
	// emits are the chunks to yield (in order) before the terminal result.
	emits [][]byte
	// result is the terminal AttemptResult returned after emitting.
	result AttemptResult
	// err is the optional underlying error.
	err error
}

// recorder threads a call log through a set of scripted backends and produces
// an AttemptFunc that records the exact order of invocation.
type recorder struct {
	scripts map[string]recordingBackend
	calls   []string // backend names in the exact order Run invoked them
}

func newRecorder(scripts ...recordingBackend) *recorder {
	m := make(map[string]recordingBackend, len(scripts))
	for _, s := range scripts {
		m[s.name] = s
	}
	return &recorder{scripts: m}
}

func (r *recorder) fn(t *testing.T) AttemptFunc {
	t.Helper()
	return func(b Backend, emit Emit) (AttemptResult, error) {
		r.calls = append(r.calls, b.Name)
		s, ok := r.scripts[b.Name]
		if !ok {
			t.Fatalf("unscripted backend %q invoked", b.Name)
		}
		for _, c := range s.emits {
			emit(c)
		}
		return s.result, s.err
	}
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAdv_OrderAndStopOnSuccess pins the centerpiece ORDER + STOP-ON-SUCCESS
// contract across a 4-backend chain: A and B fail pre-stream (in that order),
// C commits a complete stream, and D is NEVER tried. The exact call sequence
// must be [A, B, C] — proving in-order traversal AND stop-on-first-success.
//
// Mutation that bites: reverse the iteration order, or fall through past a
// committed success — either changes r.calls or invokes D (Attempts["D"] != 0).
func TestAdv_OrderAndStopOnSuccess(t *testing.T) {
	rec := newRecorder(
		recordingBackend{name: "A", result: AttemptFirstByteTimeout, err: errors.New("A fbt")},
		recordingBackend{name: "B", result: AttemptPreStreamError, err: errors.New("B refused")},
		recordingBackend{name: "C", emits: [][]byte{[]byte("part1-"), []byte("part2")}, result: AttemptSuccess},
		recordingBackend{name: "D", emits: [][]byte{[]byte("NEVER")}, result: AttemptSuccess},
	)
	out, err := Run(makeBackends("A", "B", "C", "D"), rec.fn(t))
	if err != nil {
		t.Fatalf("expected success after two pre-stream failovers, got %v", err)
	}
	wantCalls := []string{"A", "B", "C"}
	if !eqStrings(rec.calls, wantCalls) {
		t.Fatalf("call order = %v, want %v (in-order + stop-on-success violated)", rec.calls, wantCalls)
	}
	if out.Backend != "C" {
		t.Fatalf("serving backend = %q, want C", out.Backend)
	}
	if string(out.Output) != "part1-part2" {
		t.Fatalf("output = %q, want part1-part2", out.Output)
	}
	if !out.Committed {
		t.Fatalf("expected committed stream from C")
	}
	if out.Attempts["A"] != 1 || out.Attempts["B"] != 1 || out.Attempts["C"] != 1 {
		t.Fatalf("attempts A/B/C = %d/%d/%d, want 1/1/1", out.Attempts["A"], out.Attempts["B"], out.Attempts["C"])
	}
	if out.Attempts["D"] != 0 {
		t.Fatalf("D attempts = %d, want 0 (D must never be tried after C succeeds)", out.Attempts["D"])
	}
}

// TestAdv_MidStreamErrorOnLaterBackendNoFailover is the STREAMING-RULE
// centerpiece in its hardest form: failover legitimately occurs (A pre-stream
// fails), then B emits a byte (commits) and fails MID-STREAM. The contract
// forbids failing over a committed stream, so C must NEVER run, the partial
// from B must be preserved verbatim, and a *MidStreamError naming B is surfaced.
//
// Mutation that bites: allow failover after commit (mid-stream branch falls
// through to next) -> C runs (Attempts["C"] != 0) and output is replaced.
func TestAdv_MidStreamErrorOnLaterBackendNoFailover(t *testing.T) {
	underlying := errors.New("B reset after first byte")
	rec := newRecorder(
		recordingBackend{name: "A", result: AttemptPreStreamError, err: errors.New("A dial refused")},
		recordingBackend{name: "B", emits: [][]byte{[]byte("committed-B")}, result: AttemptMidStreamError, err: underlying},
		recordingBackend{name: "C", emits: [][]byte{[]byte("SHOULD-NOT-RUN")}, result: AttemptSuccess},
	)
	out, err := Run(makeBackends("A", "B", "C"), rec.fn(t))

	var mse *MidStreamError
	if !errors.As(err, &mse) {
		t.Fatalf("expected *MidStreamError, got %v", err)
	}
	if mse.Backend != "B" {
		t.Fatalf("mid-stream backend = %q, want B", mse.Backend)
	}
	if !errors.Is(err, underlying) {
		t.Fatalf("err does not wrap underlying cause: %v", err)
	}
	wantCalls := []string{"A", "B"}
	if !eqStrings(rec.calls, wantCalls) {
		t.Fatalf("call order = %v, want %v (C must not be reached after commit)", rec.calls, wantCalls)
	}
	if out.Attempts["C"] != 0 {
		t.Fatalf("C attempts = %d, want 0 (no failover after commit)", out.Attempts["C"])
	}
	if string(out.Output) != "committed-B" {
		t.Fatalf("partial output = %q, want committed-B (verbatim, no replacement)", out.Output)
	}
	if len(out.Chunks) != 1 {
		t.Fatalf("chunk count = %d, want 1 (no extra emission)", len(out.Chunks))
	}
	if out.Backend != "B" || !out.Committed {
		t.Fatalf("expected committed partial from B, got backend=%q committed=%v", out.Backend, out.Committed)
	}
}

// TestAdv_SingleBackendSuccess covers the single-backend edge: exactly one
// invocation, full stream served, attempts == 1.
func TestAdv_SingleBackendSuccess(t *testing.T) {
	rec := newRecorder(
		recordingBackend{name: "solo", emits: [][]byte{[]byte("only")}, result: AttemptSuccess},
	)
	out, err := Run(makeBackends("solo"), rec.fn(t))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if !eqStrings(rec.calls, []string{"solo"}) {
		t.Fatalf("calls = %v, want [solo]", rec.calls)
	}
	if out.Backend != "solo" || string(out.Output) != "only" || !out.Committed {
		t.Fatalf("got backend=%q output=%q committed=%v", out.Backend, out.Output, out.Committed)
	}
	if out.Attempts["solo"] != 1 {
		t.Fatalf("solo attempts = %d, want 1", out.Attempts["solo"])
	}
}

// TestAdv_SingleBackendPreStreamFailErrAllFailed covers the single-backend
// pre-stream failure edge: ErrAllFailed with the underlying cause joined, and
// no infinite loop (exactly one attempt).
func TestAdv_SingleBackendPreStreamFailErrAllFailed(t *testing.T) {
	cause := errors.New("solo dial refused")
	rec := newRecorder(
		recordingBackend{name: "solo", result: AttemptPreStreamError, err: cause},
	)
	out, err := Run(makeBackends("solo"), rec.fn(t))
	if !errors.Is(err, ErrAllFailed) {
		t.Fatalf("err = %v, want ErrAllFailed", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want it to join underlying cause", err)
	}
	if out.Committed || out.Backend != "" || len(out.Output) != 0 {
		t.Fatalf("expected empty non-committed outcome, got %+v", out)
	}
	if out.Attempts["solo"] != 1 {
		t.Fatalf("solo attempts = %d, want exactly 1 (no loop)", out.Attempts["solo"])
	}
}

// TestAdv_EmptySuccessfulStreamIsNotCommitted pins the documented edge that an
// AttemptSuccess with ZERO emitted chunks is an empty committed stream per the
// doc ("a success with zero chunks is treated as an empty committed stream"),
// but Committed reflects whether a byte was actually emitted (false here). The
// run terminates on that backend with nil error and no failover.
func TestAdv_EmptySuccessfulStreamIsNotCommitted(t *testing.T) {
	rec := newRecorder(
		recordingBackend{name: "A", result: AttemptSuccess}, // no emits
		recordingBackend{name: "B", emits: [][]byte{[]byte("NEVER")}, result: AttemptSuccess},
	)
	out, err := Run(makeBackends("A", "B"), rec.fn(t))
	if err != nil {
		t.Fatalf("empty-but-successful stream should not error, got %v", err)
	}
	if !eqStrings(rec.calls, []string{"A"}) {
		t.Fatalf("calls = %v, want [A] (empty success must not fail over)", rec.calls)
	}
	if out.Backend != "A" {
		t.Fatalf("serving backend = %q, want A", out.Backend)
	}
	if len(out.Output) != 0 || len(out.Chunks) != 0 {
		t.Fatalf("expected empty output/chunks, got output=%q chunks=%d", out.Output, len(out.Chunks))
	}
	if out.Committed {
		t.Fatalf("Committed = true, want false (no byte was emitted)")
	}
	if out.Attempts["B"] != 0 {
		t.Fatalf("B attempts = %d, want 0", out.Attempts["B"])
	}
}

// TestAdv_NilAttemptFnNoPanic pins nil-handling: a nil AttemptFunc returns a
// descriptive error (not a panic), even with a non-empty backend list.
func TestAdv_NilAttemptFnNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Run panicked on nil attemptFn: %v", r)
		}
	}()
	out, err := Run(makeBackends("A"), nil)
	if err == nil {
		t.Fatalf("expected an error for nil attemptFn")
	}
	if errors.Is(err, ErrAllFailed) {
		t.Fatalf("nil attemptFn should not masquerade as ErrAllFailed: %v", err)
	}
	if len(out.Attempts) != 0 {
		t.Fatalf("no attempt should be recorded for nil attemptFn, got %v", out.Attempts)
	}
}

// TestAdv_PreStreamErrorOrderingInJoinedError verifies the all-fail aggregate
// surfaces EACH backend's underlying cause AND ErrAllFailed, so the WRONG error
// is not surfaced and diagnostics survive. Order independent: all causes joined.
func TestAdv_PreStreamErrorOrderingInJoinedError(t *testing.T) {
	ca := errors.New("cause-A-unique")
	cb := errors.New("cause-B-unique")
	rec := newRecorder(
		recordingBackend{name: "A", result: AttemptFirstByteTimeout, err: ca},
		recordingBackend{name: "B", result: AttemptPreStreamError, err: cb},
	)
	_, err := Run(makeBackends("A", "B"), rec.fn(t))
	if !errors.Is(err, ErrAllFailed) {
		t.Fatalf("err = %v, want ErrAllFailed", err)
	}
	if !errors.Is(err, ca) || !errors.Is(err, cb) {
		t.Fatalf("joined err = %v, want both per-backend causes present", err)
	}
	if !eqStrings(rec.calls, []string{"A", "B"}) {
		t.Fatalf("call order = %v, want [A B]", rec.calls)
	}
}
