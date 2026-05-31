# pkg/serde — Anti-Bluff Audit

- **Test result:** PASS (5/5 tests: TestMarshalUnmarshal, TestMustMarshal, TestUnmarshal_RoundTrip_Mutation, TestMustMarshal_PanicsOnError_Mutation, TestMarshal_ErrorPropagated_Mutation)
- **Risk:** LOW

- **Real-behavior coverage:**
  The package is a thin, honest wrapper over the standard library `encoding/json`:
  `Marshal` → `json.Marshal`, `Unmarshal` → `json.Unmarshal`, `MustMarshal` → `Marshal` + panic-on-error. The tests exercise the REAL code path (no mocks, no stubs, no interfaces faked out):
  - Round-trip behavior is genuinely proven: `Marshal` output is fed back into `Unmarshal` and the decoded struct field is compared to the original value (`package_test.go:18` and `:46`). This is true sink-side verification — it checks the actual produced/recovered data, not merely `err == nil`.
  - Failure-path is covered for all three functions: `Marshal` returning a non-nil error on a non-marshalable type (`chan int`, `package_test.go:68-73`) and `MustMarshal` actually panicking on the same input via `recover()` (`package_test.go:51-66`). These are real negative tests, not happy-path-only.
  - Mutation pairing per Constitution 1.1 is satisfied: each mutation test names a concrete mutation and would FAIL if the behavior were removed — e.g., if `Unmarshal` ignored input the round-trip assert fails; if `MustMarshal` returned nil instead of panicking the `panicked` flag stays false; if `Marshal` swallowed errors the error assert fails.

- **PASS-bluff findings:**
  - Minor (not a true bluff): `TestMustMarshal` (`package_test.go:23-28`) asserts only `len(data) != 0`. This is a weak/`len>=0`-style assertion that does NOT verify the produced JSON content (e.g., that `{"k":"v"}` was actually produced). It would still pass if `MustMarshal` emitted arbitrary non-empty garbage. The stronger `MustMarshal` guarantee (panic-on-error) is, however, properly covered by the separate mutation test at `:51`, so the overall function is not unproven — only this one assertion is shallow.
  - No bluffs of the PASS-on-broken-feature class were found. There are no `t.Skip` calls, no mock-only validation, no swallowed errors, and no tautological "assert the literal I just set" assertions in the core round-trip/error tests.

- **Recommended hardening:**
  1. In `TestMustMarshal`, replace the `len(data) != 0` check with an exact-content assertion, e.g. `if string(data) != "{\"k\":\"v\"}" { t.Errorf(...) }`, so the test proves the actual serialized output, not just non-emptiness.
  2. Add an `Unmarshal` failure-path test (currently absent): feed malformed JSON (e.g., `[]byte("{not json")`) and assert a non-nil error is returned and propagated, plus a mutation note that swallowing the decode error would break it.
  3. Add a nested/complex round-trip case (struct with slices, maps, nested structs, and a `json:"-"`/omitempty tag) to prove tag-honoring and structural fidelity beyond a single string field.
  4. Optional: assert `Unmarshal` into a nil/non-pointer target returns an error, documenting the contract for end-user misuse.
