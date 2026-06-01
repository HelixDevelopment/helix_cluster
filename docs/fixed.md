# Fixed

**Revision:** 8
**Last modified:** 2026-06-01T10:29:18Z
**Description:** Completed workable items (with evidence references)
**Authority:** Constitution §11.4.93 (workable-items DB single source of truth)
**Generated-by:** scripts/docs/db_to_md.py (DB is canonical; edit via cmd/hxc-registry, not by hand)

Total completed: **47**.

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
| HXC-1021 | Bug | P1 | Drive pkg/build StateFailed via a genuine build failure not the 'fail' sentinel | dbed10c |
| HXC-1024 | Bug | P2 | Enforce or document pkg/build content-addressable cache digest integrity | dbed10c |
| HXC-1026 | Feature | P0 | Add real mTLS handshake test for pkg/security (valid client succeeds, no/foreign cert rejected) | dbed10c |
| HXC-1027 | Bug | P1 | Assert MinVersion==TLS13 on the client TLS path in pkg/security | dbed10c |
| HXC-1030 | Bug | P1 | Verify pkg/hxcregistry item_history audit-sink rows after Create/Update | dbed10c |
| HXC-1031 | Bug | P2 | Assert exact and stable ComputeHeadingHash value in pkg/hxcregistry | dbed10c |
| HXC-1032 | Task | P2 | Add pkg/hxcregistry failure-path tests (dup PK, missing-id Get/Update) | dbed10c |
| HXC-1033 | Bug | P2 | Make pkg/hxcregistry concurrent test do concurrent writes (or rename) | dbed10c |
| HXC-1034 | Task | P3 | Prove pkg/hxcregistry timestamp round-trip and DB CHECK-constraint enforcement | dbed10c |
| HXC-1035 | Bug | P0 | Wire pkg/wasm WasmPlugin.Execute to real WASM output and prove transformed result | dbed10c |
| HXC-1036 | Task | P1 | Add pkg/wasm negative tests for missing execute export and corrupt module Init | dbed10c |
| HXC-1037 | Bug | P2 | Prove pkg/wasm init/shutdown exports actually run via memory side-effect | dbed10c |
| HXC-1042 | Bug | P1 | Convert pkg/storage error tests to errors.Is on exported sentinels | dbed10c |
| HXC-1043 | Bug | P2 | Reconcile and test pkg/storage FileStore vs MemoryStore copy semantics | dbed10c |
| HXC-1044 | Task | P2 | Add pkg/storage overwrite, missing-delete, and List edge tests for both stores | dbed10c |
| HXC-1045 | Feature | P1 | Add pkg/storage -race concurrent Put/Get/Delete test to prove RWMutex | dbed10c |
| HXC-1062 | Bug | P2 | Add pkg/crypto SHA-256 known-answer vectors and sentinel errors.Is checks | dbed10c |
| HXC-1063 | Task | P3 | Add pkg/crypto AES-128/192, wrong-key decrypt, short-ciphertext, PBKDF2 vector tests | dbed10c |
| HXC-902 | Task | P1 |  |  |
| HXC-903 | Task | P1 |  |  |
