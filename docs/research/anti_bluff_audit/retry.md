# pkg/retry — Anti-Bluff Audit

- **Test result:** PASS — 18/18 tests pass (`go test ./pkg/retry/... -count=1 -v`, 2.33s).
- **Risk:** LOW

- **Real-behavior coverage:**
  This is a pure-Go utility package with no external dependencies (no DB/network/services), so testing the real implementation directly — with no mocks — is the correct approach, and that is exactly what the suite does. Genuinely proven behaviors:
  - **Retry exhaustion & attempt counting:** `TestDo_MaxAttemptsRespected_Mutation` (retry_test.go:213) asserts the wrapped `fn` is invoked *exactly* `MaxAttempts` times (callCount == 3), not just that an error came back. This is a strong sink-side check.
  - **Last-error propagation/wrapping:** `TestDo_ReturnsLastError_Mutation` (retry_test.go:225) uses `errors.Is` to prove the original sentinel error is wrapped through `%w` into the final exhaustion error — verifies the actual produced output, not just `err != nil`.
  - **Backoff timing is real:** `TestExponentialBackoff` (retry_test.go:59), `TestLinearBackoff` (retry_test.go:72), `TestMutation_ExponentialDoubles` (retry_test.go:239) measure wall-clock elapsed time and assert lower bounds that match the doubling/linear schedule — these would fail if `computeDelay` were neutered. `TestMaxDelayCap` (retry_test.go:85) asserts an upper bound proving the cap actually clamps growth.
  - **Context cancellation aborts early:** `TestContextCancellation` (retry_test.go:98) and its mutation twin assert both that an error is returned AND that `callCount < 10`, proving the loop short-circuits rather than running to exhaustion.
  - **Non-retryable short-circuit:** `TestNonRetryableError` (retry_test.go:121) asserts callCount == 1, proving `ErrNonRetryable` stops retrying immediately.
  - **Jitter introduces variance:** `TestMutation_JitterReducesDeterminism` (retry_test.go:252) runs 5 times and asserts not-all-identical durations.
  - Mutation-paired tests per Constitution 1.1 exist for the load-bearing behaviors (attempt count, exponential doubling, last-error wrapping, context cancel, jitter).

- **PASS-bluff findings:**
  - **MEDIUM — `DoWithResult` failure path is shallow** (`TestDoWithResultExhausted`, retry_test.go:49): only asserts `err == nil` → `Fatal`. It does NOT verify the returned value is the zero value, does not verify `errors.Is` wrapping of the last error, and does not verify attempt count. The generic `DoWithResult` is a separate code path from `Do` (it has its own loop/backoff/cancellation logic, retry.go:89-114) yet has NO mutation test, NO context-cancellation test, and NO backoff-timing test. Its non-retryable branch (retry.go:98) and its context-cancellation branch (retry.go:104-108) are entirely unexercised — a regression there would not fail any test. This is the weakest area.
  - **LOW — `DoWithResultSuccess` does not prove retry-then-success** (retry_test.go:36): `fn` succeeds on first call, so the value-passthrough is proven but the "recovers after N transient failures and returns the eventual value" behavior is never shown for the generic variant. Likewise `Do` has no "fails twice then succeeds" test — only all-success or all-fail. The transient-recovery path (the actual point of a retry library) is under-proven.
  - **LOW — `TestDoExhausted` (retry_test.go:26) and `TestDoWithResultExhausted`** are happy-to-fail-only `err == nil` checks with no sink-side verification (no attempt count, no error-content check). They are redundant-but-harmless given the stronger mutation tests cover `Do`, but on their own they are bluff-prone shape.
  - **LOW — Jitter bound never asserted:** `computeDelay` claims "up to 25% random jitter" (retry.go:132-135). Tests prove jitter is non-zero/variable but never assert the upper bound (delay never exceeds 1.25×). A bug widening jitter would pass. Also `rand.Int63n(int64(delay)/4)` panics if `delay < 4ns` — no test guards the tiny-delay edge.
  - **LOW — `Fixed` strategy and the `default` branch of `computeDelay` (retry.go:119-127) are not directly asserted for delay magnitude** (Fixed is used as a vehicle in other tests but its constant-delay property is never the assertion target).

- **Recommended hardening:**
  1. Add `TestDoWithResult_*_Mutation` mirrors of the `Do` mutation tests: exact attempt count, `errors.Is` last-error wrapping, zero-value-on-failure assertion, context-cancellation early-abort (callCount check), and an exponential-timing test — `DoWithResult` is a duplicated loop and currently has near-zero behavioral coverage.
  2. Add a transient-recovery test for both `Do` and `DoWithResult`: `fn` returns error for the first K calls then `nil`, asserting (a) final success/value, (b) callCount == K+1.
  3. Strengthen `TestDoExhausted`-class tests with `errors.Is(err, sentinel)` and a "retry exhausted after N attempts" message/count assertion.
  4. Add a jitter upper-bound test: with `Jitter` on and `Fixed` delay D, assert every observed per-attempt delay ∈ [D, 1.25·D); and add a tiny-delay test (e.g. Delay=1ns) to pin down the `rand.Int63n(.../4)` panic edge.
  5. Add a direct `computeDelay` unit test table (Fixed/Linear/Exponential/default + MaxDelay clamp) asserting exact returned durations with jitter disabled, so the math is proven independent of wall-clock flakiness.
