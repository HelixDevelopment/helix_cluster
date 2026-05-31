# pkg/log — Anti-Bluff Audit

- **Test result:** PASS — 21 tests, all passing (`go test ./pkg/log/... -count=1 -v`)
- **Risk:** LOW

- **Real-behavior coverage:**
  This package wraps the standard library `log/slog` JSON handler; there are no mocks or stubs — every test drives the real implementation. Tests write to a real `bytes.Buffer` sink and inspect the produced bytes, satisfying the sink-side-evidence requirement (CLAUDE-1 rule 3/6). Genuinely proven:
  - `New` returns a usable logger defaulting to `InfoLevel`; nil output does not panic (`TestNew`, `TestNew_Mutation`).
  - Level filtering really suppresses below-threshold records: `SetLevel(ErrorLevel)` drops an `Info` line (`TestSetLevel_Mutation`), and `SetLevel(DebugLevel)` lets `Debug` through (`TestSetLevel`).
  - Structured key/values reach the output for Debug/Info/Error (`TestDebug`, `TestInfo`, `TestInfo_Mutation`, `TestError`).
  - `WithField`/`WithFields` attach fields AND do not mutate the parent logger (`TestWithField_Mutation` — a true mutation-paired test).
  - Context field propagation is end-to-end: `ContextWithFields` merges (not overwrites) and `WithContext` emits the merged field into output (`TestContextWithFields`, `TestWithContext`, plus their `_Mutation` pairs).
  - `TestStructuredJSONOutput` is the strongest test: it `json.Unmarshal`s the line and asserts `msg`, two custom fields, AND `level=="INFO"` — proving real serialized structure, not just substring presence.
  - `ParseLevel` covers all 5 valid inputs and the error path; `Level.String()` covers a valid and the `UNKNOWN` default.

- **PASS-bluff findings:**
  - **Dead test scaffolding (`package_test.go:11-20`).** `captureOutput` and the `defaultOutput *bytes.Buffer` var are defined but never referenced by any test, and `defaultOutput` is never wired into `package.go` (production has no such hook). This is misleading scaffolding, not an active bluff, but it suggests an intended capture mechanism that was abandoned. Low severity.
  - **`TestWarn` (`package_test.go:112-120`) is happy-path-only / weaker than siblings.** It checks only that the message substring appears; unlike `TestInfo`/`TestError` it never asserts a structured field reaches output for the Warn path, and there is no Warn field-drop mutation test. Minor coverage asymmetry.
  - **Uncovered real bug — prefix loss in `SetLevel` (`package.go:100`).** `SetLevel` rebuilds the handler with a hard-coded `.With("logger", "helix")`, silently discarding the prefix passed to `New(prefix, ...)`. No test calls `SetLevel` on a non-"helix" logger and then asserts the `logger` field. `TestSetLevel` uses prefix `"test"` but only checks the message text, so this regression passes undetected. A test asserting the `logger` field survives `SetLevel` would FAIL today — i.e. the suite is green on a partially-broken feature (CLAUDE-1 rule 2 smell).
  - **`logger` prefix field never sink-verified anywhere.** No test asserts that the prefix given to `New` actually appears as `"logger":"<prefix>"` in emitted JSON. The prefix is a claimed feature (`New(prefix string, ...)`) with zero output-side proof.
  - **`TestDefault` (`package_test.go:43-48`) is trivial** — only asserts non-nil. Acceptable as a smoke test but proves nothing about behavior.
  - **`Fatal` untested.** Acceptable (it calls `os.Exit(1)` and is hard to test without a subprocess), but the level-gating/serialization of the Fatal path is unproven.

- **Recommended hardening:**
  1. Add a test that `New("svc", &buf)` then logs and asserts the JSON contains `"logger":"svc"` (proves the prefix feature works at the sink).
  2. Add a mutation-paired test: `l := New("svc", &buf); l.SetLevel(WarnLevel); l.Warn("x")` then assert `record["logger"] == "svc"` — this would catch the prefix-loss bug at `package.go:100`.
  3. Strengthen `TestWarn` to assert a structured field value reaches output, and add a `TestWarn_Mutation` for field-dropping, matching the Info/Error pattern.
  4. Add a Debug-suppression test (`SetLevel(InfoLevel)` then `Debug(...)` produces empty output) to prove the lower-bound filter, mirroring `TestSetLevel_Mutation`.
  5. Remove or wire up the dead `captureOutput`/`defaultOutput` scaffolding (`package_test.go:11-20`) so the test file does not imply a capture path that does not exist.
  6. Optionally test `Fatal` via a subprocess (`os/exec` re-exec pattern) to prove it both emits the record and exits non-zero.
