package verifier_test

// Second-pass adversarial sink-side probes for internal/verifier.
//
// The first wave (verifier_adversarial_test.go) covered the TrustLevel switch,
// nil/empty/truncated/1-bit tamper, and the concurrent race. This second pass
// targets decisions those tests do NOT exercise:
//
//   (e) ERROR-AS-SUCCESS: the trusted-executor error path (verifier.go:142-145).
//       A trusted recompute that ERRORS must never be folded into ACCEPT — not
//       even when the SEMI node's submitted bytes happen to look correct. This
//       is the classic verification fail-open: "couldn't run the real check, so
//       I'll pass it." Untested by both prior files.
//   (f) NON-DETERMINISTIC EXECUTOR: if the trusted executor returns a value that
//       differs from the SEMI result, the verdict MUST be reject — even if the
//       SEMI value is what a *correct* executor would produce. The verdict is
//       defined by the trusted side, never by the submitted side.
//   (g) HOSTILE INPUT / DoS: large and adversarial inputs must produce a clean
//       verdict, never a panic.
//   (h) UNKNOWN-TRUST AUDIT: the default arm rejects but emits NO MismatchRecord;
//       pin that contract so a future refactor that flips default to fail-open
//       (or that silently drops the reject) is caught.
//
// Every assertion checks the real verdict (nil vs non-nil / ErrMismatch), never
// merely "did not panic" except where a panic itself is the defect under test.

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/internal/console"
	"github.com/HelixDevelopment/helix_cluster/internal/verifier"
)

// doubler2 is a local trusted re-executor identical in spirit to the package's
// canonical doubler (out[i]=in[i]*2). Defined here to keep this file standalone.
func doubler2(job verifier.Job) ([]byte, error) {
	out := make([]byte, len(job.Input))
	for i, b := range job.Input {
		out[i] = b * 2
	}
	return out, nil
}

// --- (e) ERROR-AS-SUCCESS: trusted executor error must fail CLOSED ------------

// TestAdv2_ExecutorError_FailsClosed_EvenWithCorrectBytes is the headline
// second-pass probe. The trusted executor returns an error. The SEMI node
// submits bytes that a *correct* executor WOULD have produced. The verifier
// could not actually run the trusted check, so it MUST reject (fail closed).
//
// SINK assertion: verdict is a non-nil error. A nil here would mean "trusted
// recompute failed, but I accepted the SEMI result anyway" — a textbook
// verification fail-open (error-as-success).
//
// MUTATION PROOF: if verifier.go:143-145 were changed to swallow the error
// (e.g. `trusted, _ := v.executor(job)` and proceed), then for input {1,2,3}
// the broken executor returns its zero-ish/partial output; but more directly,
// any code path that ignores err and proceeds to compare would, for a
// correctly-guessed result, ACCEPT — and this test FAILS. With the real
// fail-closed code it PASSES.
func TestAdv2_ExecutorError_FailsClosed_EvenWithCorrectBytes(t *testing.T) {
	wantErr := errors.New("trusted enclave unavailable")
	// Executor returns BOTH a plausible (even correct-looking) output AND an error.
	// A fail-open implementation that ignores the error and compares bytes would
	// be tricked into ACCEPT.
	executor := func(job verifier.Job) ([]byte, error) {
		out := make([]byte, len(job.Input))
		for i, b := range job.Input {
			out[i] = b * 2 // identical to what an honest executor produces
		}
		return out, wantErr
	}
	v := verifier.New(executor)

	input := []byte{1, 2, 3}
	correctBytes := []byte{2, 4, 6} // what an honest executor would yield

	job := verifier.Job{ID: "job-exec-err", Input: input}
	err := v.VerifyResult(job, correctBytes, console.TrustSemi, "node-x")
	if err == nil {
		t.Fatal("FAIL-OPEN (error-as-success): trusted executor errored but result was ACCEPTED — must reject")
	}
	// The underlying executor error must be surfaced (wrapped), not masked.
	if !errors.Is(err, wantErr) {
		t.Fatalf("executor error must be wrapped/surfaced, got: %v", err)
	}
}

// TestAdv2_ExecutorError_NotLoggedAsMismatch pins that an executor error is a
// distinct failure mode from a content mismatch: it must NOT emit a
// MismatchRecord (which would mislabel an infrastructure failure as a forged
// result and pollute the rejection audit log), yet must still reject.
func TestAdv2_ExecutorError_NotLoggedAsMismatch(t *testing.T) {
	var logged int
	executor := func(_ verifier.Job) ([]byte, error) {
		return nil, errors.New("boom")
	}
	v := verifier.New(executor, verifier.WithMismatchLogger(func(_ verifier.MismatchRecord) {
		logged++
	}))

	job := verifier.Job{ID: "job-ee", Input: []byte{7}}
	err := v.VerifyResult(job, []byte{14}, console.TrustSemi, "node-y")
	if err == nil {
		t.Fatal("executor error must reject (fail closed)")
	}
	if logged != 0 {
		t.Fatalf("executor error must NOT emit a MismatchRecord (got %d) — it is not a forgery", logged)
	}
}

// TestAdv2_ExecutorNilNil_TreatsEmptyTrusted documents a subtle boundary: an
// executor that returns (nil, nil) means "trusted output is empty". A SEMI node
// submitting empty bytes then legitimately matches (empty==empty) and is
// accepted; a SEMI node submitting non-empty bytes must be rejected. This proves
// the (nil,nil) executor return is NOT a blanket accept — only the empty
// submission matches it.
func TestAdv2_ExecutorNilNil_TreatsEmptyTrusted(t *testing.T) {
	executor := func(_ verifier.Job) ([]byte, error) { return nil, nil }
	v := verifier.New(executor)
	job := verifier.Job{ID: "job-nn", Input: []byte{1, 2, 3}}

	// Empty submission matches empty trusted output -> accept.
	if err := v.VerifyResult(job, nil, console.TrustSemi, "node-a"); err != nil {
		t.Fatalf("empty submission vs empty trusted output must accept, got: %v", err)
	}
	// Non-empty submission must NOT be accepted against an empty trusted output.
	if err := v.VerifyResult(job, []byte{0x00}, console.TrustSemi, "node-b"); err == nil {
		t.Fatal("FAIL-OPEN: non-empty submission accepted against empty trusted output — must reject")
	}
}

// --- (f) VERDICT DEFINED BY TRUSTED SIDE, NOT SUBMITTED SIDE ------------------

// TestAdv2_TrustedSideDefinesVerdict: the trusted executor is the ORACLE. If the
// executor (deliberately) computes a different transform than the SEMI node used,
// the SEMI result must be rejected even though the SEMI node's own arithmetic was
// internally consistent. Proves the comparison binds to the trusted recompute,
// not to any property of the submitted bytes themselves.
func TestAdv2_TrustedSideDefinesVerdict(t *testing.T) {
	// Trusted executor triples; SEMI node doubled. Both are "valid" arithmetic,
	// but only the trusted transform defines correctness.
	tripler := func(job verifier.Job) ([]byte, error) {
		out := make([]byte, len(job.Input))
		for i, b := range job.Input {
			out[i] = b * 3
		}
		return out, nil
	}
	v := verifier.New(tripler)
	job := verifier.Job{ID: "job-oracle", Input: []byte{5, 6, 7}}

	doubled := []byte{10, 12, 14} // self-consistent, but NOT the trusted transform
	if err := v.VerifyResult(job, doubled, console.TrustSemi, "node-o"); err == nil {
		t.Fatal("FAIL-OPEN: result matching a DIFFERENT transform accepted — verdict not bound to trusted oracle")
	}

	tripled := []byte{15, 18, 21} // matches trusted transform
	if err := v.VerifyResult(job, tripled, console.TrustSemi, "node-o"); err != nil {
		t.Fatalf("result matching trusted oracle must accept, got: %v", err)
	}
}

// --- (g) HOSTILE INPUT / DoS: no panic, clean verdict -------------------------

// TestAdv2_LargeAndHostileInput_NoPanic feeds large and structured-adversarial
// inputs. A correct executor recompute must yield a clean verdict; a tampered
// submission on the same large input must reject. No panic / no hang.
func TestAdv2_LargeAndHostileInput_NoPanic(t *testing.T) {
	v := verifier.New(doubler2)

	// 1 MiB input (bounded — anti-DoS for the TEST itself).
	const n = 1 << 20
	big := make([]byte, n)
	for i := range big {
		big[i] = byte(i)
	}
	trusted := make([]byte, n)
	for i := range trusted {
		trusted[i] = big[i] * 2
	}

	job := verifier.Job{ID: "job-big", Input: big}

	// Correct large result accepts.
	if err := v.VerifyResult(job, trusted, console.TrustSemi, "node-big"); err != nil {
		t.Fatalf("correct large result must accept, got: %v", err)
	}
	// A single tampered byte deep in a 1 MiB payload must still reject.
	tampered := make([]byte, n)
	copy(tampered, trusted)
	tampered[n/2] ^= 0x01
	if err := v.VerifyResult(job, tampered, console.TrustSemi, "node-big"); err == nil {
		t.Fatal("FAIL-OPEN: deep single-byte tamper in large payload accepted — must reject")
	}
}

// TestAdv2_ConfusableHexLikeBytes_Rejected guards against a hypothetical
// short-circuit that compared raw bytes rather than digests: feed inputs whose
// submitted result is a byte sequence that resembles a hex digest. The verifier
// must still bind to the real sha256 comparison and reject when wrong.
func TestAdv2_ConfusableHexLikeBytes_Rejected(t *testing.T) {
	v := verifier.New(doubler2)
	job := verifier.Job{ID: "job-hexlike", Input: []byte("abc")} // trusted: doubled bytes
	// Submit 64 ASCII hex chars (looks like a sha256 hex string) — wrong content.
	hexLike := []byte("0000000000000000000000000000000000000000000000000000000000000000")
	if err := v.VerifyResult(job, hexLike, console.TrustSemi, "node-h"); err == nil {
		t.Fatal("FAIL-OPEN: hex-digest-looking bytes accepted as result — comparison confused")
	}
}

// --- (h) UNKNOWN-TRUST AUDIT contract pin -------------------------------------

// TestAdv2_DefaultArm_RejectsWithoutMismatchRecord pins the default-arm
// behavior: an unknown trust level rejects (non-nil error) but emits NO
// MismatchRecord and never invokes the executor. This documents the current
// contract so a refactor that turns default into accept (fail-open) OR that
// silently drops the rejection is caught.
func TestAdv2_DefaultArm_RejectsWithoutMismatchRecord(t *testing.T) {
	executorCalled := false
	executor := func(job verifier.Job) ([]byte, error) {
		executorCalled = true
		return doubler2(job)
	}
	var logged int
	v := verifier.New(executor, verifier.WithMismatchLogger(func(_ verifier.MismatchRecord) {
		logged++
	}))

	job := verifier.Job{ID: "job-unk", Input: []byte{1, 2, 3}}
	err := v.VerifyResult(job, []byte{2, 4, 6}, console.TrustLevel("WEIRD"), "node-unk")
	if err == nil {
		t.Fatal("FAIL-OPEN: unknown trust level accepted — must reject in default arm")
	}
	if executorCalled {
		t.Fatal("unknown trust level must NOT invoke the trusted executor")
	}
	if logged != 0 {
		t.Fatalf("unknown trust level must not emit a MismatchRecord (got %d)", logged)
	}
}

// --- contract pin: ErrMismatch wrapping vs executor-error (distinct classes) ---

// TestAdv2_ExecutorError_DoesNotWrapErrMismatch pins that an infrastructure
// failure (executor error) is a DISTINCT error class from a forgery rejection:
// callers using errors.Is(err, ErrMismatch) to count forged-result rejections
// must NOT mis-count an executor outage as a forgery. Both still fail closed.
func TestAdv2_ExecutorError_DoesNotWrapErrMismatch(t *testing.T) {
	executor := func(_ verifier.Job) ([]byte, error) {
		return nil, fmt.Errorf("enclave down")
	}
	v := verifier.New(executor)
	job := verifier.Job{ID: "job-class", Input: []byte{1}}
	err := v.VerifyResult(job, []byte{2}, console.TrustSemi, "node-class")
	if err == nil {
		t.Fatal("executor error must reject (fail closed)")
	}
	if errors.Is(err, verifier.ErrMismatch) {
		t.Fatal("executor error must NOT be classified as ErrMismatch (forgery) — distinct failure modes")
	}
}

// --- concurrency on the executor-error path -----------------------------------

// TestAdv2_ConcurrentExecutorError_AllReject runs the error path concurrently to
// confirm every verdict is reject (no goroutine accidentally accepts) and no
// race/panic occurs on the shared Verifier. Capped workers — anti-hang.
func TestAdv2_ConcurrentExecutorError_AllReject(t *testing.T) {
	executor := func(_ verifier.Job) ([]byte, error) {
		return []byte{0xAA}, errors.New("flaky enclave")
	}
	v := verifier.New(executor)

	const workers = 8
	const perWorker = 20
	var mu sync.Mutex
	var accepted int
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				job := verifier.Job{ID: "j", Input: []byte{byte(w), byte(i)}}
				if err := v.VerifyResult(job, []byte{0xAA}, console.TrustSemi, "n"); err == nil {
					mu.Lock()
					accepted++
					mu.Unlock()
				}
			}
		}(w)
	}
	wg.Wait()
	if accepted != 0 {
		t.Fatalf("FAIL-OPEN under concurrency: %d executor-error verdicts ACCEPTED — must all reject", accepted)
	}
}
