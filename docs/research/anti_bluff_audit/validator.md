# pkg/validator — Anti-Bluff Audit

- **Test result:** PASS — 17/17 tests pass (`go test ./pkg/validator/... -count=1 -v`, ok 0.346s).
- **Risk:** LOW

- **Real-behavior coverage:**
  The package is a self-contained, pure-Go struct-tag validation library (no I/O, no network, no external services), so there is no real-vs-mock gap to bluff: every test drives the genuine production code path through `Validator.ValidateStruct` / `validateField` / `checkRule` / `checkMin` / `checkMax`. Coverage is genuinely behavioral, not just no-panic:
  - **Both happy AND failure paths** are asserted for every tag: `required` (empty vs filled), `min`/`max` on ints (below/above/in-range), `min`/`max` on string length, `min`/`max` on float64, `email` (invalid vs valid), `uuid` (invalid vs valid), `oneof` (not-in-set vs in-set).
  - **Sink-side verification of output** exists where it matters: `TestValidateStructMultipleErrors` and `TestMutationValidateStructCollectsAllErrors` inspect the actual error *message* and assert both offending field names (`Name`/`Email`, `A`/`B`) appear — proving the error-aggregation join in `ValidateStruct` line 91-95 really runs, not just that an error is non-nil.
  - **Mutation-paired tests** that would fail if behavior were removed: `TestMutationMinMaxOnStringLength` pins the string-length branch of `checkMin`/`checkMax` (default case, lines 196-199 / 222-225); `TestMutationUUIDCaseInsensitive` pins the `[a-fA-F]` class in `uuidRe` (would fail if regex were case-sensitive); `TestMutationValidateStructCollectsAllErrors` pins multi-field accumulation.
  - Edge/guard paths are exercised: `nil` struct, non-struct input, struct with no `validate` tags (no false positive), pointer-to-struct, and a non-nil pointer field.
  - `RegisterRule` is proven end-to-end: a custom `startswith_h` rule is registered and shown to both pass ("hello") and fail ("world") through the real `checkRule` default-case dispatch (lines 168-173).

- **PASS-bluff findings:**
  - **Weak (not bluff): error identity never pinned via `errors.Is`** — the package deliberately exports sentinel errors (`ErrRequired`, `ErrInvalidEmail`, `ErrInvalidUUID`, `ErrOneOf`, `ErrMinExceeded`, `ErrMaxExceeded`, `ErrNilStruct`, `ErrNotStruct`, `ErrValidationFailed`) and wraps them with `%w`, but every test asserts only `err == nil` / `err != nil` or a substring. The tests would still pass if the wrong sentinel were returned, or if `%w` wrapping were dropped. Note also `ErrMinExceeded`/`ErrMaxExceeded` are declared (validator.go:28-29) but **never actually returned** by `checkMin`/`checkMax` (which return ad-hoc `fmt.Errorf` without `%w`); no test catches this dead/inconsistent API. (`validator_test.go` throughout; `validator.go:178-228`).
  - **Weak: `min`/`max` on uint kinds is never tested.** `checkMin`/`checkMax` have dedicated `reflect.Uint*` branches (validator.go:185-189, 211-215) that no test exercises — removing them would not fail any test.
  - **Weak: happy-path-only / unproven failure for `IsValidID`.** `TestIsValidID` checks one valid and one invalid input but never the empty-string case (`alphaNum` uses `+`, so `""` is invalid) — a documented boundary left unpinned.
  - **Weak: `oneof` with surrounding whitespace / single-value set and the `email` `mail.ParseAddress` quirks** (e.g. `"a@b"` is accepted by `net/mail`) are untested; failure-mode granularity is shallow but the core branch is covered.
  - **Minor: `New()` / `registerDefaults()` not asserted** — `registerDefaults` is intentionally empty, and no test asserts `New()` returns non-nil or an empty rule map. Low value, noted for completeness.
  - No `t.Skip`, no swallowed errors, no mock-only validation, no tautological literal-echo assertions were found. There is **no PASS-bluff** in the strict sense (no test passes on broken behavior).

- **Recommended hardening:**
  1. Replace `err != nil` assertions with `errors.Is(err, validator.ErrRequired)` (and the other sentinels) so the public error contract and `%w` wrapping are actually enforced.
  2. Either make `checkMin`/`checkMax` wrap `ErrMinExceeded`/`ErrMaxExceeded` and add a test asserting `errors.Is(..., ErrMinExceeded)`, or delete the unused sentinels — current state is a latent API lie no test guards.
  3. Add uint min/max test cases (e.g. a `uint8`/`uint64` field) to cover the dedicated reflect branches.
  4. Add `IsValidID("")` (expect false) and an underscore/dash positive case to fully pin `alphaNum`.
  5. Add a negative-path test for a *registered* custom rule combined with `required` ordering, and a test that an unregistered rule name in a tag is silently ignored (current `default` fall-through in `checkRule` line 168) — document and pin that behavior so it can't silently change.
  6. Add an `email` boundary test (`"a@b"` accepted, `"user@"` rejected) so the `mail.ParseAddress` contract is explicit rather than assumed.
