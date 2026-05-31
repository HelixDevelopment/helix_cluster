# Consolidated Anti-Bluff Audit Report — HelixCluster

| Field | Value |
|---|---|
| Created | 2026-05-31 |
| Scope | 24 `pkg/*` packages, one findings file each |
| Constitution anchors | CLAUDE-1 (§7.1 + §11.4.39), PCS-6 |
| Audit command baseline | `go test ./... -count=1` (no `-race` unless noted) |

---

## 1. Executive Summary

All 24 audited packages currently report **PASS** on their test suites. The audit asked a different question than "does the suite pass": *does the suite prove the feature works for end users, or does it pass-bluff?*

### Counts by risk

| Risk | Count | Packages |
|---|---|---|
| **HIGH** | 4 | etcd, infra, lock, build |
| **MEDIUM** | 10 | security, hxcregistry, wasm, semaphore, storage, metrics, grpcutil, context, workerpool, errors |
| **LOW** | 10 | backoff, classads, crypto, log, lru, pubsub, ratelimit, retry, serde, validator |

### Headline

**13 of 24 packages carry genuine PASS-bluff risk** — that is, at least one test that passes today while the behavior it claims to validate is unproven or non-functional (the 4 HIGH + 9 of the 10 MEDIUM; `validator` is MEDIUM-adjacent but its report explicitly states "there is **no PASS-bluff** in the strict sense", so it is excluded from the bluff count, leaving 4 HIGH + 9 MEDIUM = 13).

Of these, **3 packages are the canonical Constitution PCS-6 / CLAUDE-1 violation — tests PASS but the headline feature is likely NON-FUNCTIONAL** (see §4): **etcd, infra, build**. A 4th, **lock**, ships an entirely untested production backend (`EtcdLocker`) and a concurrency test that survives a no-op-lock mutation under the default (non-`-race`) command — functionally the same class of defect.

A recurring systemic pattern across MEDIUM packages: **concurrency/atomicity guarantees are "proven" by count-only tests that pass even when the lock/atomic is removed, because the audit command does not run `-race`** (errors, metrics, semaphore, storage, lock, pubsub, ratelimit). This is a fleet-wide CI gap, not just a per-package one.

---

## 2. Prioritized Findings Table (HIGH first)

| Package | Risk | Test result | PASS-bluff findings (short) | Top hardening action |
|---|---|---|---|---|
| **etcd** | HIGH | PASS (4 tests) | All 4 tests are filler: `TestMockKV` asserts a literal in a local map; `TestContextTimeout`/`TestConfigDefaults` test the stdlib, not the package; only nil-receiver `Close()` touches production code. Every real etcd op (Put/Get/Watch/Lease/**Lock**) is untested. | Add embedded-etcd integration tests: Put→Get round-trip, Watch event, Lease TTL, and the distributed-**Lock** mutual-exclusion proof. |
| **infra** | HIGH | PASS (17 tests) | Orchestrator does **zero** real work — every method reads/writes hardcoded strings in a map; tests assert those same literals (`"running"`, `Healthy==true`, `IP:10.0.0.N`). No service ever boots; `Health` echoes, never probes. | Use testcontainers so `Boot("postgres-primary")` really starts Postgres; assert sink-side (`SELECT 1`, Redis `PING`, etc.) and a real failure path. |
| **lock** | HIGH | PASS (4 tests, also `-race`) | Production `EtcdLocker` has **zero** coverage (the package's whole reason to exist). `TestMemoryLockerConcurrent` asserts only `counter==10`, which survives a no-op-lock mutation under the default command (race only caught with `-race`). | Add real-etcd `EtcdLocker` block/acquire test; rewrite the concurrent test to detect a broken lock without `-race` (shared `inCritical` flag + violation count). |
| **build** | HIGH | PASS (33 tests) | `runBuild` is a stub (`time.After` + magic string `"fail"`); `TestServiceSubmitAndGet` asserts `State==Succeeded`/`ImageTag!=""` on a simulated build — no image, layer, or registry artifact ever exists. `List`/`Concurrent` never even `Start()` the workers. | Inject a `Builder` interface + integration test running a real builder against a fixture repo, asserting the produced image is pullable; or rename the stub path `*_Simulated` and stop asserting success implies a build. (cache/platform/manifest units are honest.) |
| **security** | MEDIUM | PASS (11 fns) | mTLS never handshakes — tests only inspect `*tls.Config` fields, never prove a valid client connects and an invalid client is rejected. Vault is **mock-only**; no production `VaultClient` exists in-package. `TestNewTLSConfigBuilder` is non-nil-only. | Add a real `tls.Listener`/`tls.Dial` handshake test (valid cert succeeds, no/foreign cert rejected); locate or build the real Vault client + integration test. |
| **hxcregistry** | MEDIUM | PASS (4 tests) | `item_history` audit rows written with swallowed errors and never queried (feature could be fully broken). Hash asserted only `!= ""`. `TestRegistryConcurrentAccess` does reads-only despite its name. No failure paths (dup PK, missing id). | Query `item_history` after Create/Update and assert the rows; assert exact stable hash value; make the concurrent test do concurrent writes. |
| **wasm** | MEDIUM | PASS (8 tests) | `WasmPlugin.Execute` discards the WASM return and `return input, nil`; `TestWasmPluginLifecycle` asserts the literal it passed in. `Init`/`Shutdown` swallow export results; tests pass against a module exporting none of them. | Wire `Execute` to the real WASM return/memory; test a module that transforms input and assert the **transformed** output; add init/shutdown side-effect proof. (Host layer is genuinely real.) |
| **semaphore** | MEDIUM | PASS (4 tests) | `TestSemaphore_ReleaseTooMany_Mutation` is self-defeating: `Release()` blocks forever on empty channel, the test accepts the timeout branch and asserts nothing — cannot fail; leaks a goroutine. No concurrency/`-race` test. | Replace the over-release test with one that asserts the block (or makes over-release detectable); add an `M>>N` concurrent invariant test under `-race`. |
| **storage** | MEDIUM | PASS (14 tests) | Exported sentinels (`ErrKeyNotFound`/`ErrEmptyKey`) never proven — error tests only check `err != nil`. `FileStore` copy semantics untested and diverge from `MemoryStore`. Overwrite, missing-delete, `List("")` edges, and `RWMutex` concurrency all unproven. | Switch error tests to `errors.Is`; add overwrite + concurrent `-race` tests; reconcile/assert FileStore vs MemoryStore copy contract. |
| **metrics** | MEDIUM | PASS (12 tests) | Concurrency mutation tests are count-only and pass with atomics removed (no `-race`). Prometheus `+Inf`/`_sum` exposition lines and registry ordering unverified; duplicate-name semantics untested. | CI-gate `-race`; assert `+Inf`/`_sum`/ordered body (golden); add duplicate-registration + histogram edge tests. |
| **grpcutil** | MEDIUM | PASS (12 tests) | `LoggingStreamInterceptor`/`wrappedStream` recv/send counters entirely untested (could be a no-op). Log assertions check hardcoded substrings (`"unary"`) that always appear regardless of branch; `code=`/`duration=` never asserted. | Add a stream test asserting `recv=N send=M`; assert dynamic log fields; test handler-returns-error code extraction. |
| **context** | MEDIUM | PASS (5 tests) | The headline guarantee — `Detach` "keeps only values" — is never tested; a mutation returning `context.Background()` would pass everything. `TestWithTimeout` only checks `Deadline() ok`, near-tautological. | Add `TestDetach_PreservesValues` (cancel parent, assert value survives); assert deadline ≈ now+timeout and a real positive-duration expiry. |
| **workerpool** | MEDIUM | PASS (4 tests) | `TestPool_NilWorkSafe_Mutation` has **no assertion** (relies on "no panic", non-deterministic). Post-`Stop()` `Submit` hang, panicking-task crash, and double-`Stop()` panic all uncovered. | Give the nil-work test a real sink (follow-up task signals it survived); add fan-in count==N, post-Stop submit, and idempotent-Stop tests. |
| **errors** | MEDIUM | PASS (18 tests) | `TestConcurrentFieldAccess` reads `err.Fields` without `RLock` and runs without `-race`, so it proves nothing about the mutex it claims to guard. `TestCodeEnumValues` only checks non-empty (collisions/renames pass). | Route concurrent readers through the locked `GetFields` path, assert all 50 writes land, and CI-gate `-race`; assert exact + distinct enum values. |
| **backoff** | LOW | PASS (5 tests) | `TestDuration_CapRemoved_Mutation` is misnamed — with `Max=1<<62` the output is byte-identical whether the cap branch exists or not; asserts the input is big, not that capping works. `TestDefault` only checks non-zero. | Replace with a small-`Max` test asserting `Duration(20)==Max` exactly; pin `Default()` to exact `Base/Max/Factor`. |
| **classads** | LOW | PASS (29 tests) | Parser tests assert only `err==nil`, never the resulting `Op`/tree shape (a `<`-parsed-as-`>` mutation survives). No eval-level paren test proving precedence override. | Assert `BinaryOp.Op` per operator; add `Eval("(1+2)*3")==9` paren test; add regexp non-match + non-bool `Match` error cases. |
| **crypto** | LOW | PASS (19 tests) | Strong overall. Minor: `Hash`/`GenerateKey` assert length only (a deterministic-but-wrong digest passes); sentinel errors never checked via `errors.Is`; only AES-256 covered. | Add SHA-256 known-answer vectors; assert sentinels with `errors.Is`; add AES-128/192 + wrong-key decrypt + short-ciphertext tests. |
| **log** | LOW | PASS (21 tests) | Real bug uncovered: `SetLevel` hardcodes `.With("logger","helix")`, dropping the `New(prefix)` value; no test asserts the `logger` field survives `SetLevel`. Dead `captureOutput` scaffolding. | Add a test asserting `"logger":"<prefix>"` in JSON output and that it survives `SetLevel` (catches the prefix-loss bug). |
| **lru** | LOW | PASS (4 tests) | No pass-bluffs found; eviction/recency/update all mutation-paired and sink-checked. Minor gaps: `NewCache(0)`/negative, fresh-miss, non-int value round-trip. | Add `NewCache(0)`/`(-1)` boundary, explicit cold-miss, and struct-value round-trip tests. |
| **pubsub** | LOW | PASS (4 tests) | No pass-bluffs; delivery/fan-out/isolation/non-blocking-drop all mutation-paired. Gaps: no `-race` concurrency test for the `RWMutex`; drop boundary (11th dropped) not explicit. | Add `-race` concurrent producer/consumer test; assert exactly 10 retained / 11th dropped. |
| **ratelimit** | LOW | PASS (11 tests) | Strong boundary/refill/window coverage. `TestTokenBucket_ZeroTokensInitially_Mutation` weak (doesn't prove starts-full). `Limiter` interface satisfied only by `PerKeyLimiter`; no `-race` test. | Assert bucket starts at full capacity; add `var _ Limiter` conformance + `-race` "granted ≤ max" test. |
| **retry** | LOW | PASS (18 tests) | `Do` is well covered; the generic `DoWithResult` is a duplicated loop with near-zero behavioral coverage (no mutation/cancel/timing test). Jitter upper bound never asserted. | Mirror the `Do` mutation tests onto `DoWithResult`; add transient-recovery + jitter-upper-bound tests. |
| **serde** | LOW | PASS (5 tests) | No bluffs of the broken-feature class. Minor: `TestMustMarshal` asserts `len != 0` (garbage would pass); no `Unmarshal` malformed-input failure test. | Assert exact JSON content; add malformed-input `Unmarshal` failure + nested round-trip tests. |
| **validator** | LOW | PASS (17 tests) | **No PASS-bluff** in the strict sense. Weak: sentinels never pinned via `errors.Is`; `ErrMinExceeded`/`ErrMaxExceeded` declared but never returned (latent API lie); uint min/max branches untested. | Switch to `errors.Is`; wire or delete the unused sentinels; add uint min/max cases. |

---

## 3. Remediation Backlog (ordered, most impactful first)

Each task is scoped to a single package so it can be dispatched as an independent TDD stream. Order reflects PASS-bluff severity × end-user blast radius.

1. **etcd — prove the distributed primitives exist.** Stand up an embedded etcd (`go.etcd.io/etcd/server/v3/embed`) test harness. TDD: Put→Get round-trip; missing-key `ok==false`; GetPrefix multi-key; Delete/DeletePrefix sink-side absence; Watch event Type/Key/Value + ctx-cancel channel close; Lease+RevokeLease TTL expiry; **Lock mutual-exclusion** (B blocks until A unlocks); `New` default-injection mutation test via `Raw()`; dead-endpoint error-wrap path. Delete `TestMockKV`/`TestContextTimeout`.
2. **infra — make orchestration real (or quarantine the claim).** Replace map-echo assertions with testcontainers-backed integration: `Boot` actually starts each service; `Health` measures a real probe and reports `false` on a killed service; `Logs` captures a known marker; `Scale` queries the runtime for N replicas; `VMSSH` runs `echo ok` over a real session. Add mutation-paired connectivity checks that fail when the service never started.
3. **build — wire a real builder behind a `Builder` interface.** TDD: integration test runs a real builder against a fixture repo+Dockerfile and asserts the produced image is pullable (sink-side); drive `StateFailed` via a genuine build failure (not the `"fail"` sentinel); `Start()` the service in `List`/`Concurrent` and assert terminal states; replace `time.Sleep` polling with completion sync; add cache-digest integrity + cancel-mid-flight tests.
4. **lock — cover the production backend and harden the concurrency proof.** TDD: `EtcdLocker` against real etcd (A holds, B blocks, A releases, B acquires; lease key present-then-gone; error-wrap paths). Rewrite `TestMemoryLockerConcurrent` to fail on a no-op lock *without* `-race` (shared `inCritical` flag + violation counter). Add an explicit block-then-acquire ordering test.
5. **security — prove mTLS end-to-end and locate the real Vault client.** TDD: real `tls.Listener`/`tls.Dial` handshake — valid same-CA client succeeds and exchanges a byte; no-cert / foreign-CA client is rejected with a handshake error. Add `MinVersion==TLS13` to `TestClientTLS`. Locate/build the production `VaultClient` and add a dev-Vault integration test (KVv2 round-trip, PKI issue→parse via `x509.ParseCertificate`); otherwise mark Vault incomplete.
6. **hxcregistry — verify the audit-history sink and failure paths.** TDD: query `item_history` after Create (`event_type='Opened'`) and Update (`Updated`); stop swallowing those `Exec` errors. Assert exact + stable `ComputeHeadingHash`. Add dup-PK, missing-id `GetItem`, and non-existent `UpdateItem` failure tests. Make the "concurrent" test do concurrent writes (or rename it).
7. **wasm — make `WasmPlugin.Execute` use real WASM output.** TDD: a module whose `execute` transforms input (e.g. `len*2` or a memory write); assert the **transformed** result so an ignored WASM call FAILS the test. Add init/shutdown side-effect (sentinel-in-memory) proof, a missing-`execute`-export negative, and a corrupt-`.wasm` `Init` error test.
8. **semaphore — replace the self-defeating over-release test and add concurrency.** TDD: assert `Release()` on an unheld semaphore blocks (fail if `done` closes early) and clean up the goroutine, or make over-release detectable and test the detection. Add an `M>>N` invariant test (`active` never exceeds `N`) under `-race`; add `New(0)` barrier behavior.
9. **storage — lock the error contract and concurrency.** TDD: convert the five error tests to `errors.Is(err, ErrKeyNotFound/ErrEmptyKey)`. Add overwrite (`v1`→`v2`), missing-delete idempotency, `List("")`-returns-all / `List("nomatch")`-empty, and a `-race` concurrent Put/Get/Delete test. Assert or remove `FileStore` copy divergence; remove the ineffective dir-Stat branch.
10. **metrics — gate `-race` and complete the exposition assertions.** TDD: add `+Inf`/`_sum`/ordered-body (golden) assertions to `TestPrometheusHandler`; duplicate-registration semantics; histogram edge cases (empty/unsorted buckets, `Inf`/`NaN`, boundary equality); add a `go test -race ./pkg/metrics/...` CI job.
11. **grpcutil — test the stream logging counters.** TDD: drive `RecvMsg`/`SendMsg` N/M times and assert the `recv=N send=M` log line + `recvCount`/`sendCount` fields. Strengthen log assertions to dynamic fields (`/test/Method`, `code=OK`); add a handler-returns-error test asserting code extraction for unary and stream.
12. **context — prove the value-retention guarantee.** TDD: `TestDetach_PreservesValues` (store value in parent, cancel parent, assert value survives through detached ctx — fails if `Detach` returns `Background()`). Assert `WithTimeout` deadline ≈ now+timeout and a real positive-duration expiry to `DeadlineExceeded`; add a cancel-path `Canceled` assertion.
13. **workerpool — give the nil-work test a sink and cover lifecycle hazards.** TDD: nil submission followed by a signaling task, asserting the worker survived. Add fan-in `counter==N`, post-`Stop()` `Submit` hang guard, panicking-task recovery, and idempotent double-`Stop()` tests.
14. **errors — make the concurrency guarantee honest.** TDD: route concurrent readers through `GetFields` (locked path), assert all 50 writes observed, and CI-gate `-race`. Assert exact + pairwise-distinct `Code` enum values; add `WithFields` nil-receiver, `errors.As` chain, and stack-frame-content tests.
15. **log — add the missing prefix-field proof (catches a real bug).** TDD: assert `"logger":"<prefix>"` appears in emitted JSON and survives `SetLevel(...)` — this fails today (`SetLevel` hardcodes `"helix"`). Strengthen `TestWarn` to a structured-field assertion; add Debug-suppression; remove dead `captureOutput` scaffolding.
16. **backoff — replace the tautological cap mutation test.** TDD: small-`Max` (`Base=100ms,Max=500ms,Factor=2`) asserting `Duration(20)==500ms`; pin `Default()` to exact `Base/Max/Factor` and `Default().Duration(1)==200ms`; add monotonic-growth + exact mid-range assertions.
17. **classads — assert parser output, not just no-error.** TDD: assert `BinaryOp.Op` per comparison operator; eval-level `(1+2)*3==9` paren test; logical-precedence tree-shape; regexp non-match; non-bool `Match` error path.
18. **retry — bring `DoWithResult` to parity with `Do`.** TDD: mirror the `Do` mutation tests onto `DoWithResult` (exact attempt count, `errors.Is` wrapping, zero-value-on-failure, ctx-cancel early abort, exponential timing). Add transient-recovery (fail K then succeed) for both; jitter upper-bound (≤1.25×) and tiny-delay panic-edge tests.
19. **crypto — add known-answer vectors and error-identity.** TDD: SHA-256 KAT (`Hash("abc")==...`); `errors.Is` on `ErrVerifyFailed`/`ErrInvalidKey`/`ErrInvalidCiphertext`; AES-128/192 + wrong-key decrypt + short-ciphertext; PBKDF2 fixed vector.
20. **validator — pin the error contract and remove the API lie.** TDD: convert assertions to `errors.Is` on each sentinel; wire `ErrMinExceeded`/`ErrMaxExceeded` through `%w` (or delete them) with a matching test; add uint min/max, `IsValidID("")`, and `email` boundary cases.
21. **ratelimit — prove starts-full and concurrency.** TDD: `NewTokenBucket(5,0)` allows exactly 5 then denies; `var _ Limiter` conformance; `-race` "total granted ≤ max" under concurrent `AllowN`; denied-`AllowN` no-side-effect test.
22. **pubsub — add the `-race` concurrency proof.** TDD: N subscribers / M publishers over overlapping subjects with deterministic per-subscriber counts under `-race`; explicit buffer-boundary (10 retained, 11th dropped); unknown-subject no-op test.
23. **serde — strengthen the shallow assertions.** TDD: exact-JSON-content assertion in `TestMustMarshal`; malformed-input `Unmarshal` failure; nested/tagged round-trip; nil/non-pointer target error.
24. **lru — pin the boundary/edge cases.** TDD: `NewCache(0)`/`(-1)` behavior; explicit fresh-cache cold miss `(nil,false)`; struct-value round-trip; optional `-race` if used concurrently.

---

## 4. PCS-6 Callout — Tests PASS but Feature Likely NON-FUNCTIONAL

Constitution **PCS-6 / CLAUDE-1 rule 2** forbids exactly this: *a test that passes on a non-functional feature is a PASS-bluff of §7.1 severity.* The following packages exhibit it directly — the suite is green while the advertised feature does not actually work for an end user:

- **infra (HIGH) — most severe.** The "infrastructure orchestrator" performs **zero** real operations: `Boot`, `Health`, `Logs`, `Scale`, `VMSpawn`, `VMSSH` all read/write hardcoded strings in a map, and the tests assert those literals. A real PostgreSQL/Redis/etcd never starts; `Health` can never report unhealthy; yet all 17 tests pass. The stub is shipped as the production implementation — worse than a mock, because nothing signals it is fake.
- **etcd (HIGH).** Every real KV/Lease/Watch/**Lock** operation is untested; the only production line any test touches is a nil-receiver `Close()`. `TestMockKV` asserts a literal in a test-local map and `TestContextTimeout` tests the stdlib. The distributed-lock and watch features could be entirely broken and the suite stays green.
- **build (HIGH).** `runBuild` is a timer + magic-string simulation (`"Override in production"` per its own comment). `TestServiceSubmitAndGet` asserts `State==Succeeded` and `ImageTag!=""` though **no image, layer, digest, or registry artifact is ever produced**. The build-orchestration headline feature is non-functional behind a passing suite.

**Adjacent (same defect class, listed for completeness):**

- **lock (HIGH).** The production `EtcdLocker` distributed backend has zero tests, and the in-memory concurrency test passes even against a no-op lock under the default (non-`-race`) command — a broken lock would not be detected.
- **security (MEDIUM).** mTLS is never handshaked and Vault has no production implementation in-package (mock-only), so "mutual auth works" and "issue a cert from Vault" are unproven as end-user features even though the suite passes.
- **wasm (MEDIUM).** `WasmPlugin.Execute` discards the WASM result and echoes its own input; the lifecycle test asserts the literal it passed in, so the "run a plugin and get its output" feature is non-functional yet green.

These four-plus packages must be treated as §7.1-severity defects: **the passing test is the bug.** Remediation tasks 1–4 (and 5, 7) in §3 convert them into honest, behavior-proving suites.
