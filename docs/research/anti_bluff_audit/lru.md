# pkg/lru — Anti-Bluff Audit

- **Test result:** PASS — 4/4 tests pass (`TestCache`, `TestCache_GetMovesToFront_Mutation`, `TestCache_UpdateExisting_Mutation`, `TestCache_CapacityRespected_Mutation`); `ok github.com/HelixDevelopment/helix_cluster/pkg/lru 0.339s`.

- **Risk:** LOW

- **Real-behavior coverage:**
  The package is a real, self-contained LRU cache built on `container/list` + a `map[string]*list.Element` (no mocks, no stubs, no external services — so the "mock instead of real implementation" smell does not apply). The tests exercise the actual implementation through its public API (`NewCache`/`Put`/`Get`) and verify sink-side outcomes rather than just no-panic:
  - **Eviction by capacity** is proven: `TestCache` with cap=2 confirms `a` is evicted after a third insert and `b` survives with the correct value; `TestCache_CapacityRespected_Mutation` proves it again at cap=1. These assert the actual eviction effect (key absence) and surviving value, not just `err==nil`.
  - **Recency promotion on Get** is proven: `TestCache_GetMovesToFront_Mutation` accesses `a`, then inserts `c`, and asserts `a` survived while the untouched `b` was evicted — directly validating `Get`'s `MoveToFront` (package.go:30) and the eviction-of-LRU path (package.go:43-49).
  - **Update-in-place semantics** are proven: `TestCache_UpdateExisting_Mutation` confirms a repeated `Put("a", …)` returns the new value (10, not 1) AND that no duplicate phantom element bloats the cache (a remains evictable after two more inserts), validating package.go:38-42.
  - Each mutation test is genuinely mutation-paired: removing `MoveToFront` from `Get`, the update branch from `Put`, or the eviction block would each flip a specific assertion to failure. The comments accurately describe the mutant each test kills.

- **PASS-bluff findings:**
  - none — assertions are concrete (key presence/absence and exact values), there are no tautological checks (`len>=0`, asserting a literal just set), no swallowed errors, no `t.Skip`, and each test would fail if the corresponding behavior were deleted.
  - Minor (non-bluff) gaps, noted for completeness only: no test for the `NewCache(0)` / negative-capacity edge (with cap=0, `Put` evicts then re-inserts, leaving 1 item — undocumented behavior, currently unverified); no concurrency test, though the type is not documented as goroutine-safe (and it is not — no mutex), so this is a documentation/scope question, not a bluff; `Get` on a never-inserted key returning `(nil, false)` is exercised only indirectly via the eviction assertions, never asserted on a fully fresh cache.

- **Recommended hardening:**
  1. Add a boundary test for `NewCache(0)` and `NewCache(-1)` asserting the intended behavior (either reject, or document/verify that nothing is retained) so the eviction guard at package.go:43 is pinned for the degenerate capacity.
  2. Add an explicit miss test: `c := NewCache(1); v, ok := c.Get("absent")` asserting `!ok && v == nil` to lock the cold-path return at package.go:33.
  3. Add a value-type-preservation assertion (store a non-int, e.g. a struct, and assert equality after retrieval) to prove the `interface{}` round-trip, since all current tests store ints.
  4. If the cache is ever used concurrently anywhere in the codebase, either add a `-race` concurrency test or document non-thread-safety in package.go; otherwise leave as-is.
