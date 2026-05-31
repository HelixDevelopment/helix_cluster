# pkg/context — Anti-Bluff Audit

- **Test result:** PASS (5/5: TestWithTimeout, TestDetach, TestDetach_DoneNotNil_Mutation, TestDetach_ErrNotNil_Mutation, TestWithTimeout_ZeroTimeout_Mutation)
- **Risk:** MEDIUM

- **Real-behavior coverage:**
  - The tests exercise the REAL implementation (no mocks/stubs). `Detach` is a genuine custom type (`detached` struct overriding `Done()`→nil and `Err()`→nil), and the tests drive the actual exported `Detach`/`WithTimeout` functions.
  - `Detach`'s core promise — "a context that is never cancelled" — is genuinely proven. `TestDetach_DoneNotNil_Mutation` (package_test.go:30) cancels the parent and asserts the detached `Done()` channel never fires within 50ms; `TestDetach_ErrNotNil_Mutation` (package_test.go:44) cancels the parent and asserts `Err()` stays nil. These are real, sink-side, mutation-paired assertions: if either override were removed (reverting to embedded parent behavior), both tests would fail. This is honest coverage of the cancellation-immunity behavior.
  - `WithTimeout` has a real failure-path / mutation test: `TestWithTimeout_ZeroTimeout_Mutation` (package_test.go:56) proves a zero timeout actually expires (the `Done()` channel fires), which would fail if the timeout parameter were ignored.

- **PASS-bluff findings:**
  - **Unproven primary documented behavior — value retention.** package.go:14 documents `Detach` as "keeping only values," yet NO test ever stores a value in the parent context (`context.WithValue`) and reads it back through the detached context. The single most load-bearing reason `Detach` exists (carry values forward while dropping cancellation) is completely unverified. A mutation that made `Detach` return `context.Background()` (dropping all values) would still pass every existing test. This is a PASS-bluff: the feature's headline guarantee is asserted by the doc comment but not by any test.
  - **Weak/trivial deadline assertion.** `TestWithTimeout` (package_test.go:9) only checks that `ctx.Deadline()` returns `ok==true`. It never verifies the returned deadline is approximately `now + timeout`, nor that the context actually cancels at the deadline. Since `WithTimeout` is a thin pass-through to `stdctx.WithTimeout`, this is close to tautological — it would pass for any timeout value, including a wrong/ignored one (e.g. a bug substituting a hardcoded large duration). The zero-timeout mutation test partially compensates, but no positive-duration expiry is exercised.
  - **No verification that `WithTimeout`'s CancelFunc works / no leak coverage.** The returned `cancel` is only `defer`-called for cleanup; no test asserts that calling `cancel()` transitions `ctx.Err()` to `context.Canceled`. Happy-path only.
  - **`Detach` value-context type integrity unchecked.** No test confirms `detachedCtx.Value(k)` delegates to the embedded parent (the embedding provides it for free, but per CLAUDE-1 the end-user-visible effect must be proven, not assumed from struct embedding).

- **Recommended hardening:**
  - Add `TestDetach_PreservesValues`: `parent := context.WithValue(context.Background(), key, "v"); d := Detach(parent); cancelParent(); require d.Value(key) == "v"`. This proves the documented "keeping only values" guarantee and is mutation-paired (fails if `Detach` returns a fresh/Background context).
  - Strengthen `TestWithTimeout` to assert the deadline is within tolerance of `time.Now().Add(timeout)` (e.g. `dl.Sub(start)` ∈ [timeout-ε, timeout+ε]) AND add a positive-duration expiry test that waits for `ctx.Done()` to fire after a short real timeout, checking `ctx.Err() == context.DeadlineExceeded`.
  - Add a cancel-path test: after `cancel()`, assert `ctx.Err() == context.Canceled` to prove the CancelFunc is wired and not swallowed.
  - Optionally add `TestDetach_ValueDelegation_Mutation` asserting value lookups still reach the parent, so removal of struct embedding is caught.
