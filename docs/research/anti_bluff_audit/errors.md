# pkg/errors — Anti-Bluff Audit

- **Test result:** PASS (18 tests: TestNew, TestNew_Mutation, TestWrap, TestWrap_Mutation, TestWithField, TestWithField_Mutation, TestWithFields, TestIsCode, TestIsCode_Mutation, TestGetFields, TestGetFields_Mutation, TestStackTrace, TestStackTrace_Mutation, TestErrorFormatting, TestErrorFormattingWithCause, TestErrorFormattingWithCause_Mutation, TestCodeEnumValues, TestConcurrentFieldAccess). Pure in-package unit test; exercises the REAL implementation (no mocks/stubs) — appropriate for a library package with no external sinks.

- **Risk:** MEDIUM

- **Real-behavior coverage (genuinely proven):**
  - `New` sets Code/Message, captures a non-empty stack, and renders into `Error()` string (sink-side: actual output checked).
  - `Wrap` preserves cause and is `errors.Is`-compatible (real stdlib `errors.Is` traversal, not just `==`); nil-input returns nil (failure path covered with a real mutation-paired test).
  - `IsCode` traverses the Unwrap cause chain, rejects unrelated codes, and handles nil — true positive, true negative, and nil all asserted.
  - `GetFields` merges across the cause chain AND proves first-seen-wins precedence (`TestGetFields_Mutation`) — this is a genuine behavioral assertion that would fail if merge order were reversed.
  - `Error()` formatting proven to include message, `fields=`, and cause text against real rendered output.
  - `StackTrace` proven to reference the real test file (`package_test.go`), confirming it captures actual frames, not a literal; nil-receiver returns "" (failure path).
  - Mutation-paired tests exist for New, Wrap, IsCode, GetFields, StackTrace, Error-with-cause — each would fail if the behavior were removed. Good Constitution §1.1 discipline overall.

- **PASS-bluff findings:**
  - `TestConcurrentFieldAccess` (package_test.go:210-228) — PASS-bluff under the audited command. It claims to validate thread-safe field access (the package's only concurrency guarantee, the `sync.RWMutex`), but: (a) the default `go test` run has NO `-race` flag, so a data race would still PASS — the test only proves "no deadlock/panic", not safety; (b) the reader goroutines do `_ = err.Fields` (line 221), reading the map header directly WITHOUT `RLock`, so it does not even exercise the `GetFields`/locked read path the mutex protects; (c) no assertion on the resulting map contents (no sink-side check that all 50 writes landed). It only genuinely proves anything when run as `-race` (verified: passes under `-race`), but the audited/CI command does not use it.
  - `TestCodeEnumValues` (package_test.go:198-208) — near-tautological. It only asserts each constant is non-empty (`c == ""`). It does not assert the codes are distinct or equal their expected string values, so it would still PASS if two codes collided or a code were renamed/duplicated. Low-value guard.
  - `TestWithFields` (package_test.go:78-89) — happy-path only. No coverage of the nil-receiver guard in `WithFields` (the `if e == nil` branch at package.go:139 is untested), unlike `WithField` which has `TestWithField_Mutation`. Asymmetric: the symmetric failure path is unproven.
  - `TestNew_Mutation` (package_test.go:26-32) — duplicates the stack assertion already made in `TestNew` (line 18); it is a weak mutation test because `len(err.Stack)==0` would also be caught by `TestNew`. Acceptable but redundant.
  - Minor: no test asserts `Stack` frames contain a resolved `Function` name or that `captureStack` honors the 32-frame cap (package.go:179) — that bound is unexercised.

- **Recommended hardening:**
  1. Make the concurrency guarantee honest: change reader goroutines in `TestConcurrentFieldAccess` to call `GetFields(err)` (the locked path) instead of `_ = err.Fields`; after the wait, assert `len(GetFields(err)) == 50` so all writes are observed (sink-side). Add a build/CI requirement that the `errors` package is run with `-race` (or add a `//go:build race`-gated assertion / a dedicated `make test-race` target), since the mutex contract is only verifiable under the race detector.
  2. Strengthen `TestCodeEnumValues` to assert exact string values (e.g. `E_NOT_FOUND == "E_NOT_FOUND"`) and pairwise distinctness via a `map[Code]bool` set, so a rename or duplicate fails the test.
  3. Add a `TestWithFields_NilReceiver` mirroring `TestWithField_Mutation` to cover the `e == nil` guard at package.go:139.
  4. Add an `errors.As` test that extracts `*Error` through a `Wrap` chain (proves `As` compatibility, not just `Is`), and a test that `GetFields(nil)` returns an empty non-nil map.
  5. Add a stack-content assertion: verify at least one frame's `Function` is non-empty and references the calling test, and add a test that a deep call chain caps `Stack` at 32 frames.
