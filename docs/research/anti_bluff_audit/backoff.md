# pkg/backoff — Anti-Bluff Audit

- **Test result:** PASS (5/5 tests: TestDefault, TestDuration, TestDuration_NegativeIteration_Mutation, TestDuration_CapRemoved_Mutation, TestDuration_FactorChanged_Mutation)
- **Risk:** LOW
- **Real-behavior coverage:** This is a pure, deterministic computation package (exponential backoff math, no I/O, no external services, no mocks possible or needed). Tests exercise the real `Config.Duration` and `Default` implementations directly — there is no mock/stub substitution, so the "exercises a mock instead of real code" PASS-bluff class does not apply. Genuinely proven behavior:
  - Base case: `Duration(0)` returns exactly `Base` (100ms) — exact sink-side value assertion (package_test.go:17-19).
  - Cap behavior: `Duration(10)` with `Max=1s` returns exactly `c.Max` — proves the `if d > c.Max` branch actually caps (package_test.go:20-22).
  - Negative-input guard: `Duration(-1)` clamps to `Base`; this is a real mutation-paired test — removing `if n < 0 { n = 0 }` makes the result `50ms != Base`, failing the test (package_test.go:27-36).
  - Exponential factor: with `Factor=1`, all iterations equal `Base`; kills any mutation that changes/removes the `math.Pow(Factor, n)` growth (package_test.go:51-62).
- **PASS-bluff findings:**
  - **package_test.go:38-49 `TestDuration_CapRemoved_Mutation` — MISNAMED / non-functional mutation test.** It sets `Max: 1<<62` (effectively no cap) and asserts the result `> 30s`. This does NOT kill the "cap removed" mutation: if the cap branch (`if d > c.Max { return c.Max }`) were deleted so the function just `return d`, then with `Max=1<<62` the returned value is byte-for-byte identical (verified: `Duration(20)` = 29h7m37s in both cases). The test asserts the input (`Max`) is large, not that the cap logic works. It is a tautological "huge Max → huge output" check dressed up as a mutation test. (The real cap IS covered, but by TestDuration:20-22, not by this test.)
  - **package_test.go:8-13 `TestDefault` — weak/partial assertion.** Only checks `Base != 0 && Max != 0`. It never asserts `Factor` (source sets 2) and accepts any non-zero `Base`/`Max`. A mutation setting `Default()`'s `Factor` to 0/1, or `Base`/`Max` to any other non-zero value, would still PASS. It proves "fields are populated," not "the default config is correct."
  - Minor: comment at package_test.go:30 is misleading ("Without the n<0 guard, this would be 50ms... With the guard... 100ms") — the prose conflates the two branches, though the actual assertion (line 33) is correct.
- **Recommended hardening:**
  - Replace `TestDuration_CapRemoved_Mutation` with a real cap-killing test: use a small `Max` (e.g. `Max=500ms`, `Base=100ms`, `Factor=2`) and assert `Duration(20) == 500ms` exactly. Removing the cap branch would then yield ~29h and fail. (This actually exercises `if d > c.Max`.)
  - Strengthen `TestDefault` to assert exact expected values: `Base == 100*time.Millisecond`, `Max == 30*time.Second`, and `Factor == 2`. Add a behavioral check that `Default().Duration(1) == 200*time.Millisecond` so a wrong default `Factor` is caught.
  - Add a monotonic-growth assertion (`Duration(n+1) >= Duration(n)` across a range) and an exact mid-range value (`Duration(3)` with Factor=2 == 800ms) to lock the `math.Pow` formula beyond the Factor=1 degenerate case.
