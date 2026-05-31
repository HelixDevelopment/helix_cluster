# pkg/ratelimit — Anti-Bluff Audit

- **Test result:** PASS — 11/11 tests pass (`go test ./pkg/ratelimit/... -count=1`, 4.8s wall, real timing-based tests with `time.Sleep`).

- **Risk:** LOW

- **Real-behavior coverage:** The package is a self-contained, pure-Go implementation (token bucket, sliding window, per-key map of limiters) with no external services and no mocks. Tests exercise the real implementation directly and verify sink-side state, not just non-panic:
  - **Boundary enforcement is genuinely proven.** `TestTokenBucket` consumes exactly the burst capacity, then asserts the over-limit call is *denied* (`if l.Allow() { t.Error(...) }`) — the deny path is checked, not just the allow path. Same for `TestSlidingWindow` (4th call denied) and the `AllowN` variants (partial-token exhaustion proven: 3 then deny-3 then allow-2).
  - **Refill / time-based recovery proven.** `TestTokenBucket_RefillCalculation_Mutation` exhausts, sleeps ~1.1s, and asserts recovery at refill=1/sec — would fail if refill math (`elapsed * l.refill`, ratelimit.go:45) were broken.
  - **Max-cap proven.** `TestTokenBucket_MaxCap_Mutation` uses refill=1000/sec + sleep so tokens would overflow, then asserts only ONE allow succeeds — directly mutation-kills the cap clamp at ratelimit.go:46-48.
  - **Window slide proven.** `TestSlidingWindow_Slides_Mutation` fills the window, asserts denial, sleeps past the window, asserts recovery — kills the cutoff-eviction loop at ratelimit.go:96-102.
  - **Per-key isolation proven.** `TestPerKeyLimiter_Isolated_Mutation` exhausts key "x" and asserts key "y" still allows — kills the map-keying logic; would fail if all keys shared one limiter.
  - Mutation-paired coverage (Constitution 1.1) is present and substantive: each core behavior (cap, refill, window eviction, key isolation, non-zero initial tokens) has a test that fails if the behavior is removed.

- **PASS-bluff findings:**
  - `TestTokenBucket_ZeroTokensInitially_Mutation` (ratelimit_test.go:131-136) is weak: despite the name claiming to guard initial-token semantics, it only asserts a single `Allow()` succeeds on a bucket with max=5. It does not assert the bucket starts *full* (it would still pass if the bucket started with just 1 token). A near-tautology relative to its stated intent. Low severity — the stronger `MaxCap`/`AllowN` tests indirectly cover initial capacity.
  - No test asserts interface conformance. `Limiter` (ratelimit.go:10-13) declares `Allow(key string)` / `AllowN(key string, n int)`, but `TokenBucket` and `SlidingWindow` implement `AllowKey`/`AllowKeyN` and bare `Allow()`/`AllowN(int)` instead — so only `PerKeyLimiter` satisfies `Limiter`. No test contains a `var _ Limiter = ...` assertion, so the API's central abstraction is unproven/unused. This is a design wart surfaced by missing coverage rather than a pass-bluff per se.
  - No concurrency test. All three types take a `sync.Mutex` and advertise safe concurrent use implicitly, but no `-race` / goroutine test exercises it. The mutex correctness is unproven (would not be caught by current tests).
  - Sliding-window `AllowN` partial-admission semantics: `AllowN` is all-or-nothing (ratelimit.go:103 rejects if `len+n > limit`); no test proves that a partially-fitting batch is rejected atomically rather than partially admitted. Covered indirectly by `TestSlidingWindowAllowN` but not asserting the no-side-effect-on-reject property (events slice unchanged after a denied AllowN).

- **Recommended hardening:**
  1. Strengthen `TestTokenBucket_ZeroTokensInitially_Mutation` to assert the bucket starts at full capacity: `NewTokenBucket(5,0)` then assert exactly 5 successive `Allow()` succeed and the 6th fails (refill=0 isolates initial state).
  2. Add `var _ Limiter = (*PerKeyLimiter)(nil)` and reconcile `TokenBucket`/`SlidingWindow` with the `Limiter` interface (or add a compile-time assertion documenting they intentionally do not satisfy it). Add a test that uses a value through the `Limiter` interface so the abstraction is actually exercised.
  3. Add a `-race` concurrency test: spawn N goroutines hammering `AllowN` on a shared `TokenBucket`/`PerKeyLimiter` and assert the total number of granted tokens never exceeds `max` (proves the mutex actually guards the counter — a strong sink-side invariant).
  4. Add a denied-`AllowN` no-side-effect test for `SlidingWindow`: after a rejected `AllowN(big)`, assert a subsequent `AllowN(fitting)` still succeeds (proves rejection did not consume window slots).
