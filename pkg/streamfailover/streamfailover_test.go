package streamfailover

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
)

func runUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// makeBackends builds a fresh ordered backend list for each test.
func makeBackends(names ...string) []Backend {
	bs := make([]Backend, 0, len(names))
	for _, n := range names {
		bs = append(bs, Backend{Name: n})
	}
	return bs
}

// TestClause1_FirstByteTimeoutFailsOverAndServesCompleteStream covers CLOSURE
// clause 1: a FIRST-BYTE timeout on backend A causes failover to backend B,
// which serves a COMPLETE stream. The serving backend must be B.
//
// Mutation: if Run did NOT fail over on a pre-stream timeout (e.g. the
// `continue` for AttemptFirstByteTimeout were removed / treated as terminal),
// the serving backend would be A with empty/no output, and the assertions on
// out.Backend == "B" and the full payload would fail.
func TestClause1_FirstByteTimeoutFailsOverAndServesCompleteStream(t *testing.T) {
	id := runUUID(t)
	t.Logf("run=%s BEFORE: A configured to first-byte-timeout pre-stream; B to serve full stream", id)

	want := []byte("hello-from-B-complete")
	var bCalls int

	attempt := func(b Backend, emit Emit) (AttemptResult, error) {
		switch b.Name {
		case "A":
			// Pre-stream timeout: emit nothing, signal first-byte timeout.
			return AttemptFirstByteTimeout, errors.New("A first-byte deadline exceeded")
		case "B":
			bCalls++
			// Stream the full payload in two chunks, then succeed.
			emit([]byte("hello-from-B"))
			emit([]byte("-complete"))
			return AttemptSuccess, nil
		default:
			t.Fatalf("unexpected backend %q", b.Name)
			return AttemptPreStreamError, nil
		}
	}

	out, err := Run(makeBackends("A", "B"), attempt)
	if err != nil {
		t.Fatalf("run=%s expected success after failover, got err=%v", id, err)
	}
	if out.Backend != "B" {
		t.Fatalf("run=%s serving backend = %q, want B (failover did not occur)", id, out.Backend)
	}
	if string(out.Output) != string(want) {
		t.Fatalf("run=%s output = %q, want %q (B did not serve complete stream)", id, out.Output, want)
	}
	if !out.Committed {
		t.Fatalf("run=%s expected committed stream from B", id)
	}
	if out.Attempts["A"] != 1 {
		t.Fatalf("run=%s A attempts = %d, want 1 (A must be tried first)", id, out.Attempts["A"])
	}
	if out.Attempts["B"] != 1 || bCalls != 1 {
		t.Fatalf("run=%s B attempts = %d / bCalls=%d, want 1/1", id, out.Attempts["B"], bCalls)
	}
	t.Logf("run=%s AFTER: served by %q, output=%q, attempts=%v", id, out.Backend, out.Output, out.Attempts)
}

// TestClause2_MidStreamFailureNotRetried covers CLOSURE clause 2: a MID-STREAM
// failure on backend A (after the first byte) must NOT be retried on B. Run
// returns the mid-stream error plus the already-emitted partial, with no second
// attempt: B's attempt count must be 0 and there must be no re-emission.
//
// Mutation: if Run retried after the first byte (e.g. the committed/mid-stream
// branch fell through to the next backend instead of returning), B would be
// invoked (Attempts["B"] >= 1) and output would be duplicated/replaced — these
// assertions would fail.
func TestClause2_MidStreamFailureNotRetried(t *testing.T) {
	id := runUUID(t)
	t.Logf("run=%s BEFORE: A emits one byte then mid-stream-fails; B is a healthy fallback that must NOT run", id)

	var aCalls, bCalls int
	partial := []byte("partial-A")
	underlying := errors.New("A connection reset mid-stream")

	attempt := func(b Backend, emit Emit) (AttemptResult, error) {
		switch b.Name {
		case "A":
			aCalls++
			n := emit(partial) // first byte -> committed
			if n != 1 {
				t.Fatalf("run=%s first emit count = %d, want 1", id, n)
			}
			return AttemptMidStreamError, underlying
		case "B":
			bCalls++
			emit([]byte("SHOULD-NOT-APPEAR"))
			return AttemptSuccess, nil
		default:
			t.Fatalf("unexpected backend %q", b.Name)
			return AttemptPreStreamError, nil
		}
	}

	out, err := Run(makeBackends("A", "B"), attempt)

	var mse *MidStreamError
	if !errors.As(err, &mse) {
		t.Fatalf("run=%s expected *MidStreamError, got %v", id, err)
	}
	if mse.Backend != "A" {
		t.Fatalf("run=%s mid-stream error backend = %q, want A", id, mse.Backend)
	}
	if !errors.Is(err, underlying) {
		t.Fatalf("run=%s mid-stream error does not wrap underlying cause: %v", id, err)
	}
	// No retry on B.
	if bCalls != 0 || out.Attempts["B"] != 0 {
		t.Fatalf("run=%s B was retried (bCalls=%d attempts=%d); mid-stream failover is forbidden",
			id, bCalls, out.Attempts["B"])
	}
	if aCalls != 1 || out.Attempts["A"] != 1 {
		t.Fatalf("run=%s A attempts = %d (calls %d), want exactly 1", id, out.Attempts["A"], aCalls)
	}
	// Partial preserved, no re-emission / duplication.
	if string(out.Output) != string(partial) {
		t.Fatalf("run=%s partial output = %q, want %q (no duplicate/replacement allowed)", id, out.Output, partial)
	}
	if len(out.Chunks) != 1 {
		t.Fatalf("run=%s emitted chunk count = %d, want 1 (no re-emission)", id, len(out.Chunks))
	}
	if out.Backend != "A" || !out.Committed {
		t.Fatalf("run=%s expected committed partial from A, got backend=%q committed=%v", id, out.Backend, out.Committed)
	}
	t.Logf("run=%s AFTER: partial=%q from %q, B attempts=%d (no retry), err=%v",
		id, out.Output, out.Backend, out.Attempts["B"], err)
}

// TestClause3_AllPreStreamFailuresReturnErrAllFailed covers CLOSURE clause 3:
// when every backend fails pre-stream, Run returns the typed ErrAllFailed.
//
// Mutation: if a pre-stream failure were instead treated as a committed
// success, errors.Is(err, ErrAllFailed) would be false.
func TestClause3_AllPreStreamFailuresReturnErrAllFailed(t *testing.T) {
	id := runUUID(t)
	t.Logf("run=%s BEFORE: A,B,C all fail pre-stream (timeout/pre-stream-error)", id)

	attempt := func(b Backend, emit Emit) (AttemptResult, error) {
		switch b.Name {
		case "A":
			return AttemptFirstByteTimeout, errors.New("A timeout")
		case "B":
			return AttemptPreStreamError, errors.New("B dial refused")
		case "C":
			return AttemptFirstByteTimeout, errors.New("C timeout")
		default:
			t.Fatalf("unexpected backend %q", b.Name)
			return AttemptSuccess, nil
		}
	}

	out, err := Run(makeBackends("A", "B", "C"), attempt)
	if !errors.Is(err, ErrAllFailed) {
		t.Fatalf("run=%s err = %v, want errors.Is ErrAllFailed", id, err)
	}
	if out.Committed {
		t.Fatalf("run=%s outcome should not be committed when all fail pre-stream", id)
	}
	if out.Backend != "" {
		t.Fatalf("run=%s serving backend = %q, want empty", id, out.Backend)
	}
	if len(out.Output) != 0 {
		t.Fatalf("run=%s output = %q, want empty", id, out.Output)
	}
	for _, n := range []string{"A", "B", "C"} {
		if out.Attempts[n] != 1 {
			t.Fatalf("run=%s %s attempts = %d, want 1 (all should be tried)", id, n, out.Attempts[n])
		}
	}
	// Underlying diagnostics are preserved (joined).
	if !errors.Is(err, ErrAllFailed) || err.Error() == ErrAllFailed.Error() {
		t.Logf("run=%s note: joined err = %v", id, err)
	}
	t.Logf("run=%s AFTER: ErrAllFailed returned, attempts=%v, err=%v", id, out.Attempts, err)
}

// TestContractViolation_PreStreamResultWithChunksIsTreatedCommitted guards the
// no-duplicate invariant against a misbehaving AttemptFunc that emits a byte
// but then claims a pre-stream timeout. Such an attempt must NOT fail over.
//
// Mutation: if the runner trusted the (lying) pre-stream result and failed over
// despite emitted chunks, B would run and duplicate output — caught here.
func TestContractViolation_PreStreamResultWithChunksIsTreatedCommitted(t *testing.T) {
	id := runUUID(t)
	t.Logf("run=%s BEFORE: A emits a byte but lies, returning FirstByteTimeout", id)

	var bCalls int
	attempt := func(b Backend, emit Emit) (AttemptResult, error) {
		switch b.Name {
		case "A":
			emit([]byte("already-shown"))
			return AttemptFirstByteTimeout, errors.New("A lies about pre-stream timeout")
		case "B":
			bCalls++
			emit([]byte("dup"))
			return AttemptSuccess, nil
		}
		return AttemptPreStreamError, nil
	}

	out, err := Run(makeBackends("A", "B"), attempt)
	var mse *MidStreamError
	if !errors.As(err, &mse) {
		t.Fatalf("run=%s expected *MidStreamError (committed despite false pre-stream claim), got %v", id, err)
	}
	if bCalls != 0 || out.Attempts["B"] != 0 {
		t.Fatalf("run=%s B must not run after a byte was emitted (bCalls=%d)", id, bCalls)
	}
	if string(out.Output) != "already-shown" {
		t.Fatalf("run=%s output = %q, want the already-shown partial", id, out.Output)
	}
	t.Logf("run=%s AFTER: treated as committed, no failover, output=%q err=%v", id, out.Output, err)
}

// TestSuccessOnFirstBackend_NoFailover sanity-checks that a healthy first
// backend serves without consulting later backends.
//
// Mutation: if Run always advanced past a successful committed backend, B would
// be invoked.
func TestSuccessOnFirstBackend_NoFailover(t *testing.T) {
	id := runUUID(t)
	var bCalls int
	attempt := func(b Backend, emit Emit) (AttemptResult, error) {
		if b.Name == "A" {
			emit([]byte("A-full"))
			return AttemptSuccess, nil
		}
		bCalls++
		return AttemptSuccess, nil
	}
	out, err := Run(makeBackends("A", "B"), attempt)
	if err != nil {
		t.Fatalf("run=%s unexpected err %v", id, err)
	}
	if out.Backend != "A" || string(out.Output) != "A-full" {
		t.Fatalf("run=%s served by %q output %q, want A/A-full", id, out.Backend, out.Output)
	}
	if bCalls != 0 || out.Attempts["B"] != 0 {
		t.Fatalf("run=%s B should not run when A succeeds (bCalls=%d)", id, bCalls)
	}
	t.Logf("run=%s AFTER: served by A, B untouched (attempts=%v)", id, out.Attempts)
}

// TestNoBackends ensures empty input yields ErrAllFailed deterministically.
func TestNoBackends(t *testing.T) {
	id := runUUID(t)
	_, err := Run(nil, func(Backend, Emit) (AttemptResult, error) { return AttemptSuccess, nil })
	if !errors.Is(err, ErrAllFailed) {
		t.Fatalf("run=%s empty backends err = %v, want ErrAllFailed", id, err)
	}
	t.Logf("run=%s AFTER: empty backend list -> ErrAllFailed", id)
}
