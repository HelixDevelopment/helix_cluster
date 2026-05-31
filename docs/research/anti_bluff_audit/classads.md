# pkg/classads — Anti-Bluff Audit

- **Test result:** PASS — 29 tests pass (`go test ./pkg/classads/... -count=1 -v`), 0 failures, 0 skips.
- **Risk:** LOW

## Real-behavior coverage

`pkg/classads` is a self-contained ClassAd expression parser + evaluator (the HTCondor-style `Requirements`/`Rank` matching primitive). It performs pure computation: no network, no DB, no filesystem, no external service. Crucially, **every test drives the REAL code path** — `Eval`, `Match`, `NewParser().Parse()`, and the real `ClassAd` map type. There are no mocks or stubs anywhere in the package, which is correct here because the feature makes no real-world-operation claim that a mock could falsify (CLAUDE-1 §5 / §11.4.27 is satisfied — mocks would be inappropriate, and none are used).

Genuinely proven, end-user-meaningful behavior (sink-side value asserted, not just "no panic"):
- Operator precedence: `1 + 2 * 3 == 7` (eval_test.go:28) actually proves `*` binds tighter than `+` — a real correctness property, not a tautology.
- Identifier resolution against an attribute map: `Memory > 4096` over `{Memory: 8192}` → `true` (eval_test.go:38).
- String equality: `Arch == "x86_64"` → `true` (eval_test.go:49).
- Logical `&&` / `||` produce correct booleans (eval_test.go:60, 71).
- `regexp("ubuntu.*", OS)` matches a real string → `true` (eval_test.go:92) — exercises the real `regexp.MatchString` function path.
- `Match` returns true on a satisfied requirement and **false on an unsatisfied one** (TestMatchNoMatch, eval_test.go:118) — failure-path coverage present.
- Error / failure paths are covered, not just happy path: undefined attribute (eval_test.go:129), division by zero (eval_test.go:136), incomplete expression parse error (parser_test.go:128).
- Short-circuit semantics proven by behavior: `false && Missing` and `true || Missing` succeed WITHOUT erroring on the undefined `Missing` identifier (eval_test.go:143, 154). This is a strong, mutation-resistant test — if short-circuiting were removed, `Missing` would raise "undefined attribute" and the test would fail.
- `ClassAd` Get/Set including missing-key-returns-false and overwrite semantics, with explicit mutation-paired tests (package_test.go:20, 29, 43) per Constitution §1.1.

## PASS-bluff findings

These are MINOR weaknesses (MEDIUM-bluff-prone at worst), not true PASS-bluffs; the package's core behavior is independently proven by the value-asserting tests above:

- **parser_test.go:64 (TestParseComparison)** — asserts only `err == nil` for six comparison operators; never inspects the returned AST `Op` field. A mutation that parsed `<` as `>` would still pass this test. (Mitigated: eval_test.go independently proves `>` evaluates correctly, but the other 5 operators' parse-to-correct-op mapping is not directly asserted anywhere.)
- **parser_test.go:81 (TestParseLogical)** — `a && b || c && d` asserts only no-error; does not verify the resulting tree shape or `&&`/`||` precedence (that `||` is the root). Precedence bug here would go undetected by this test.
- **parser_test.go:121 (TestParseParen)** — `(1 + 2) * 3` asserts only no-error; never confirms parens actually regroup (no eval to `9`). A parser that ignored parens would still pass. There is no eval-level test proving parentheses change precedence.
- **eval_test.go:7 (TestEvalLiteral)** uses `nil` attrs; acceptable, but the relational/equality operators with **string operands that are NOT numbers** (e.g. `"a" < "b"`, which the code rejects via `toFloat`) and the `valuesEqual` fallback `fmt.Sprintf` branch are unexercised — minor uncovered edges.

## Recommended hardening

Concrete tests/assertions to add to make the suite fully honest (no behavior unproven):
1. In TestParseComparison, assert `expr.(BinaryOp).Op` equals the expected operator for each of the six cases (kills the "parse `<` as `>`" mutation).
2. Add an eval-level paren test: `Eval("(1 + 2) * 3", nil)` must equal `9`, and `Eval("1 + 2 * 3", nil)` equals `7` — together they prove parentheses actually override precedence (mutation-paired).
3. In TestParseLogical, assert the root node is `BinaryOp{Op:"||"}` with both children `BinaryOp{Op:"&&"}`, proving `&&` binds tighter than `||`.
4. Add failure-path eval tests for type mismatches the code explicitly guards: `Eval("!5", nil)` should error ("! requires bool"), `Eval(`"x" < 1`, nil)` should error ("requires numbers"), and `regexp` with a non-string arg should error — proving the validation branches in eval.go are reachable and correct.
5. Add a `Match` test where the requirement evaluates to a non-bool (e.g. `Match("1 + 1", attrs)`) asserting the "did not evaluate to bool" error — currently the `toBool`-failure branch in Match (eval.go:26) is never hit by any test.
6. Add a regexp non-match case (`regexp("^win", "linux")` → `false`) to pair with the positive regexp test (mutation: regexp always returns true).
