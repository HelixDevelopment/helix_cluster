# Fixed

**Revision:** 11
**Last modified:** 2026-06-01T11:29:04Z
**Description:** Completed workable items (with evidence references)
**Authority:** Constitution §11.4.93 (workable-items DB single source of truth)
**Generated-by:** scripts/docs/db_to_md.py (DB is canonical; edit via cmd/hxc-registry, not by hand)

Total completed: **83**.

| HXC | Type | Pri | Title | Commit |
|---|---|---|---|---|
| HXC-001 | Task | P1 |  |  |
| HXC-002 | Task | P1 |  |  |
| HXC-003 | Task | P1 |  |  |
| HXC-004 | Task | P1 |  |  |
| HXC-005 | Task | P1 |  |  |
| HXC-006 | Task | P1 |  |  |
| HXC-007 | Task | P1 |  |  |
| HXC-008 | Task | P1 |  |  |
| HXC-009 | Task | P1 |  |  |
| HXC-010 | Task | P1 |  |  |
| HXC-011 | Task | P1 |  |  |
| HXC-012 | Task | P1 |  |  |
| HXC-013 | Task | P1 |  |  |
| HXC-014 | Task | P1 |  |  |
| HXC-015 | Task | P1 |  |  |
| HXC-1001 | Task | P0 | Stand up embedded-etcd integration harness for pkg/etcd tests | dbed10c |
| HXC-1002 | Feature | P0 | Prove etcd Put/Get round-trip and missing-key semantics against real etcd | dbed10c |
| HXC-1003 | Feature | P1 | Prove etcd GetPrefix returns exactly matching multi-key set | dbed10c |
| HXC-1004 | Feature | P1 | Prove etcd Delete/DeletePrefix remove keys (sink-side absence) | dbed10c |
| HXC-1005 | Feature | P0 | Prove etcd Watch fires correct event and closes on ctx cancel | dbed10c |
| HXC-1006 | Feature | P1 | Prove etcd Lease/PutWithLease/RevokeLease/KeepAlive TTL behavior | dbed10c |
| HXC-1007 | Feature | P0 | Prove etcd distributed Lock mutual exclusion (highest-risk path) | dbed10c |
| HXC-1008 | Task | P2 | Add mutation-paired etcd New() default-timeout injection test | dbed10c |
| HXC-1009 | Task | P2 | Prove etcd error-wrapping on dead endpoint | dbed10c |
| HXC-1016 | Feature | P0 | Add EtcdLocker integration tests against real etcd (block/acquire/lease) | dbed10c |
| HXC-1017 | Bug | P0 | Rewrite TestMemoryLockerConcurrent to detect a broken lock without -race | dbed10c |
| HXC-1018 | Task | P2 | Add explicit block-then-acquire ordering test for MemoryLocker | dbed10c |
| HXC-1019 | Bug | P2 | Characterize or fix MemoryLocker.Lock context-cancellation goroutine leak | 32feec3 |
| HXC-1021 | Bug | P1 | Drive pkg/build StateFailed via a genuine build failure not the 'fail' sentinel | dbed10c |
| HXC-1022 | Bug | P1 | Make pkg/build List/Concurrent tests Start() the service and assert terminal states | 32feec3 |
| HXC-1023 | Task | P2 | Replace time.Sleep polling in pkg/build with completion sync and add state-machine mutation tests | 32feec3 |
| HXC-1024 | Bug | P2 | Enforce or document pkg/build content-addressable cache digest integrity | dbed10c |
| HXC-1025 | Task | P2 | Cover pkg/build cancel-mid-flight branch sets StateCancelled | 32feec3 |
| HXC-1026 | Feature | P0 | Add real mTLS handshake test for pkg/security (valid client succeeds, no/foreign cert rejected) | dbed10c |
| HXC-1027 | Bug | P1 | Assert MinVersion==TLS13 on the client TLS path in pkg/security | dbed10c |
| HXC-1029 | Task | P3 | Replace pkg/security non-nil-only TestNewTLSConfigBuilder with meaningful assertion | 32feec3 |
| HXC-1030 | Bug | P1 | Verify pkg/hxcregistry item_history audit-sink rows after Create/Update | dbed10c |
| HXC-1031 | Bug | P2 | Assert exact and stable ComputeHeadingHash value in pkg/hxcregistry | dbed10c |
| HXC-1032 | Task | P2 | Add pkg/hxcregistry failure-path tests (dup PK, missing-id Get/Update) | dbed10c |
| HXC-1033 | Bug | P2 | Make pkg/hxcregistry concurrent test do concurrent writes (or rename) | dbed10c |
| HXC-1034 | Task | P3 | Prove pkg/hxcregistry timestamp round-trip and DB CHECK-constraint enforcement | dbed10c |
| HXC-1035 | Bug | P0 | Wire pkg/wasm WasmPlugin.Execute to real WASM output and prove transformed result | dbed10c |
| HXC-1036 | Task | P1 | Add pkg/wasm negative tests for missing execute export and corrupt module Init | dbed10c |
| HXC-1037 | Bug | P2 | Prove pkg/wasm init/shutdown exports actually run via memory side-effect | dbed10c |
| HXC-1038 | Task | P3 | Cover pkg/wasm Host.Call ErrUnexpectedResult branch | 32feec3 |
| HXC-1039 | Bug | P1 | Replace self-defeating pkg/semaphore TestSemaphore_ReleaseTooMany_Mutation | a084eb1 |
| HXC-1040 | Feature | P1 | Add pkg/semaphore M>>N concurrency invariant test under -race | a084eb1 |
| HXC-1041 | Task | P3 | Add pkg/semaphore New(0) barrier and exact-capacity boundary tests | a084eb1 |
| HXC-1042 | Bug | P1 | Convert pkg/storage error tests to errors.Is on exported sentinels | dbed10c |
| HXC-1043 | Bug | P2 | Reconcile and test pkg/storage FileStore vs MemoryStore copy semantics | dbed10c |
| HXC-1044 | Task | P2 | Add pkg/storage overwrite, missing-delete, and List edge tests for both stores | dbed10c |
| HXC-1045 | Feature | P1 | Add pkg/storage -race concurrent Put/Get/Delete test to prove RWMutex | dbed10c |
| HXC-1046 | Bug | P1 | Gate -race in CI for pkg/metrics concurrency mutation tests | a084eb1 |
| HXC-1047 | Bug | P2 | Complete pkg/metrics Prometheus exposition assertions (+Inf, _sum, ordering) | a084eb1 |
| HXC-1048 | Task | P2 | Add pkg/metrics duplicate-registration and histogram edge tests | a084eb1 |
| HXC-1049 | Task | P3 | Add pkg/metrics /metrics integration test via httptest.Server | a084eb1 |
| HXC-1050 | Bug | P1 | Test pkg/grpcutil LoggingStreamInterceptor recv/send counters | a084eb1 |
| HXC-1051 | Bug | P2 | Strengthen pkg/grpcutil log assertions to dynamic behavior-dependent fields | a084eb1 |
| HXC-1052 | Task | P2 | Cover pkg/grpcutil status.FromError code extraction for handler errors | a084eb1 |
| HXC-1053 | Bug | P1 | Add pkg/context TestDetach_PreservesValues for the value-retention guarantee | a084eb1 |
| HXC-1054 | Bug | P2 | Strengthen pkg/context WithTimeout deadline and expiry assertions | a084eb1 |
| HXC-1055 | Bug | P1 | Give pkg/workerpool TestPool_NilWorkSafe_Mutation a real sink | a084eb1 |
| HXC-1056 | Feature | P1 | Add pkg/workerpool fan-in count==N, post-Stop Submit, panic-recovery, idempotent-Stop tests | a084eb1 |
| HXC-1057 | Bug | P1 | Make pkg/errors concurrency test honest via locked GetFields path + -race | a084eb1 |
| HXC-1058 | Task | P2 | Pin pkg/errors Code enum exact values, distinctness, and WithFields nil receiver | a084eb1 |
| HXC-1059 | Bug | P2 | Replace tautological pkg/backoff cap-mutation test and pin Default() | a084eb1 |
| HXC-1060 | Bug | P2 | Assert pkg/classads parser AST Op per operator and paren precedence | a084eb1 |
| HXC-1061 | Task | P3 | Add pkg/classads eval failure-path and regexp non-match tests | a084eb1 |
| HXC-1062 | Bug | P2 | Add pkg/crypto SHA-256 known-answer vectors and sentinel errors.Is checks | dbed10c |
| HXC-1063 | Task | P3 | Add pkg/crypto AES-128/192, wrong-key decrypt, short-ciphertext, PBKDF2 vector tests | dbed10c |
| HXC-1064 | Bug | P1 | Add pkg/log prefix-field proof catching the SetLevel prefix-loss bug | a084eb1 |
| HXC-1065 | Task | P3 | Strengthen pkg/log Warn test, add Debug-suppression, remove dead captureOutput scaffolding | a084eb1 |
| HXC-1066 | Task | P3 | Add pkg/lru boundary, cold-miss, and struct-value round-trip tests | a084eb1 |
| HXC-1067 | Feature | P2 | Add pkg/pubsub -race concurrency test and explicit drop-boundary assertion | a084eb1 |
| HXC-1068 | Bug | P2 | Prove pkg/ratelimit bucket starts full and add Limiter conformance + -race tests | a084eb1 |
| HXC-1069 | Bug | P1 | Bring pkg/retry DoWithResult to behavioral parity with Do | a084eb1 |
| HXC-1070 | Task | P2 | Add pkg/retry transient-recovery, jitter-bound, and computeDelay table tests | a084eb1 |
| HXC-1071 | Task | P3 | Strengthen pkg/serde MustMarshal exact-content and add Unmarshal failure tests | a084eb1 |
| HXC-1072 | Bug | P2 | Pin pkg/validator error contract via errors.Is and remove/wire unused sentinels | a084eb1 |
| HXC-1073 | Task | P3 | Add pkg/validator uint min/max, IsValidID empty, oneof/email boundary tests | a084eb1 |
| HXC-1074 | Task | P0 | Establish fleet-wide -race CI gate across concurrency-sensitive packages | a084eb1 |
| HXC-902 | Task | P1 |  |  |
| HXC-903 | Task | P1 |  |  |
