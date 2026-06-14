# Fixed

**Revision:** 185
**Last modified:** 2026-06-14T13:28:54Z
**Description:** Completed workable items (with evidence references)
**Authority:** Constitution §11.4.93 (workable-items DB single source of truth)
**Generated-by:** scripts/docs/db_to_md.py (DB is canonical; edit via cmd/hxc-registry, not by hand)

Total completed: **684**.

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
| HXC-1000 | Bug | P0 | FIX delta-ORSet strong-eventual-consistency defect (remove-before-add divergence) | 84141b9 |
| HXC-1001 | Task | P0 | Stand up embedded-etcd integration harness for pkg/etcd tests | dbed10c |
| HXC-1002 | Feature | P0 | Prove etcd Put/Get round-trip and missing-key semantics against real etcd | dbed10c |
| HXC-1003 | Feature | P1 | Prove etcd GetPrefix returns exactly matching multi-key set | dbed10c |
| HXC-1004 | Feature | P1 | Prove etcd Delete/DeletePrefix remove keys (sink-side absence) | dbed10c |
| HXC-1005 | Feature | P0 | Prove etcd Watch fires correct event and closes on ctx cancel | dbed10c |
| HXC-1006 | Feature | P1 | Prove etcd Lease/PutWithLease/RevokeLease/KeepAlive TTL behavior | dbed10c |
| HXC-1007 | Feature | P0 | Prove etcd distributed Lock mutual exclusion (highest-risk path) | dbed10c |
| HXC-1008 | Task | P2 | Add mutation-paired etcd New() default-timeout injection test | dbed10c |
| HXC-1009 | Task | P2 | Prove etcd error-wrapping on dead endpoint | dbed10c |
| HXC-1010 | Feature | P0 | Replace pkg/infra map-echo orchestrator with real service Boot via testcontainers | 85ee85e85f221075dfc5f0304e3577e1df5bc930 |
| HXC-1011 | Feature | P0 | Make pkg/infra Health probe real services and report false on failure | 85ee85e85f221075dfc5f0304e3577e1df5bc930 |
| HXC-1012 | Feature | P1 | Make pkg/infra Logs capture real sink-side output honoring tail/since/timestamps | 85ee85e85f221075dfc5f0304e3577e1df5bc930 |
| HXC-1013 | Feature | P1 | Make pkg/infra Scale create real replicas verified via runtime query | 85ee85e85f221075dfc5f0304e3577e1df5bc930 |
| HXC-1014 | Feature | P1 | Make pkg/infra VMSpawn/VMSSH establish a real session end-to-end | 44db9182bb5abe8f00ff83a53eb512506031932e |
| HXC-1015 | Feature | P2 | Make pkg/infra VMSimulateFailure/VMSimulatePartition assert observable network effect | 85ee85e85f221075dfc5f0304e3577e1df5bc930 |
| HXC-1016 | Feature | P0 | Add EtcdLocker integration tests against real etcd (block/acquire/lease) | dbed10c |
| HXC-1017 | Bug | P0 | Rewrite TestMemoryLockerConcurrent to detect a broken lock without -race | dbed10c |
| HXC-1018 | Task | P2 | Add explicit block-then-acquire ordering test for MemoryLocker | dbed10c |
| HXC-1019 | Bug | P2 | Characterize or fix MemoryLocker.Lock context-cancellation goroutine leak | 32feec3 |
| HXC-1020 | Feature | P0 | Wire a real Builder behind pkg/build Service and prove image is pullable | 85ee85e85f221075dfc5f0304e3577e1df5bc930 |
| HXC-1021 | Bug | P1 | Drive pkg/build StateFailed via a genuine build failure not the 'fail' sentinel | dbed10c |
| HXC-1022 | Bug | P1 | Make pkg/build List/Concurrent tests Start() the service and assert terminal states | 32feec3 |
| HXC-1023 | Task | P2 | Replace time.Sleep polling in pkg/build with completion sync and add state-machine mutation tests | 32feec3 |
| HXC-1024 | Bug | P2 | Enforce or document pkg/build content-addressable cache digest integrity | dbed10c |
| HXC-1025 | Task | P2 | Cover pkg/build cancel-mid-flight branch sets StateCancelled | 32feec3 |
| HXC-1026 | Feature | P0 | Add real mTLS handshake test for pkg/security (valid client succeeds, no/foreign cert rejected) | dbed10c |
| HXC-1027 | Bug | P1 | Assert MinVersion==TLS13 on the client TLS path in pkg/security | dbed10c |
| HXC-1028 | Feature | P0 | Locate/build production Vault client and add dev-Vault integration test (KVv2 + PKI) | 85ee85e85f221075dfc5f0304e3577e1df5bc930 |
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
| HXC-1075 | Feature | P0 | Implement NATS/JetStream client wrapper (pkg/events) | 44db9182bb5abe8f00ff83a53eb512506031932e |
| HXC-1076 | Feature | P0 | Implement etcd client wrapper (pkg/etcd) | 44db9182bb5abe8f00ff83a53eb512506031932e |
| HXC-1077 | Feature | P0 | Implement etcd discovery backend (pkg/discovery) | 44db9182bb5abe8f00ff83a53eb512506031932e |
| HXC-1078 | Feature | P0 | Implement real Kafka producer/consumer (pkg/pubsub) | 44db9182bb5abe8f00ff83a53eb512506031932e |
| HXC-1079 | Feature | P0 | Implement cgroups v2 + /proc resource reader (pkg/resources) | 44db9182bb5abe8f00ff83a53eb512506031932e |
| HXC-1080 | Feature | P0 | Implement Omega-model scheduler core (pkg/scheduler, internal/scheduler) | 44db9182bb5abe8f00ff83a53eb512506031932e |
| HXC-1081 | Feature | P0 | Implement optimistic-concurrency versioning for scheduler pool | 44db9182bb5abe8f00ff83a53eb512506031932e |
| HXC-1082 | Feature | P1 | Implement HTCondor ClassAds matching in scheduler (pkg/classads) | 44db9182bb5abe8f00ff83a53eb512506031932e |
| HXC-1083 | Feature | P1 | Implement gang scheduling and preemption in scheduler | 44db9182bb5abe8f00ff83a53eb512506031932e |
| HXC-1084 | Feature | P0 | Upgrade tmux backend to control-mode (-CC) with streaming I/O (pkg/session) | 1367dca3bd532a491adb1a55b417151f0ae3cd2e |
| HXC-1085 | Feature | P0 | Implement CRDT session state and lifecycle (create/attach/migrate) | 1367dca3bd532a491adb1a55b417151f0ae3cd2e |
| HXC-1086 | Feature | P0 | Implement real Node gRPC handlers serving helixv1 Node API (internal/node) | 4d0ac6681bf586c30aaa881226aa2113f6b956b1 |
| HXC-1087 | Feature | P0 | Make node registry etcd-backed and cross-process shared (internal/node) | 2f27ac9001d3fc0a900f0386a1b9df198890e411 |
| HXC-1088 | Feature | P0 | Implement SWIM gossip membership and failure detection (pkg/swim) | 4d0ac6681bf586c30aaa881226aa2113f6b956b1 |
| HXC-1089 | Feature | P0 | Implement WireGuard mesh config-gen and key rotation (pkg/wireguard) | 2f27ac9001d3fc0a900f0386a1b9df198890e411 |
| HXC-1090 | Feature | P1 | Implement WireGuard NAT traversal / STUN hole-punch (pkg/wireguard) | 1367dca3bd532a491adb1a55b417151f0ae3cd2e |
| HXC-1091 | Feature | P0 | Wire API gateway reverse proxy with prefix routing and /health (internal/gateway) | 2f27ac9001d3fc0a900f0386a1b9df198890e411 |
| HXC-1092 | Feature | P0 | Add JWT/RBAC auth middleware to API gateway (internal/gateway) | 2f27ac9001d3fc0a900f0386a1b9df198890e411 |
| HXC-1093 | Feature | P0 | Implement leader election with fencing tokens (pkg/leader) | 4d0ac6681bf586c30aaa881226aa2113f6b956b1 |
| HXC-1094 | Feature | P0 | Implement distributed lock primitives over etcd (pkg/lock) | 2f27ac9001d3fc0a900f0386a1b9df198890e411 |
| HXC-1095 | Feature | P1 | Implement Postgres-backed internal artifact registry (pkg/hxcregistry) | 1367dca3bd532a491adb1a55b417151f0ae3cd2e |
| HXC-1096 | Feature | P1 | Implement GPU resource probe with /proc parser (internal/gpu) | 9c58146 |
| HXC-1097 | Feature | P1 | Define vendor-agnostic GPUBackend interface and registry (internal/gpu) | 05f408d |
| HXC-1098 | Feature | P2 | Implement GPU sharing modes (MPS, time-slice, MIG, exclusive) | 1a67f0a |
| HXC-1099 | Feature | P1 | Implement Build Service orchestrator and worker pool (internal/build) | 1367dca3bd532a491adb1a55b417151f0ae3cd2e |
| HXC-1101 | Feature | P0 | Implement e2ee transport record protocol (security/pkg/e2ee) | e12003abbb68db0e08fa13f44cfbf7c57c46c8b8 |
| HXC-1102 | Feature | P1 | Implement software-rooted attestation (security/pkg/attestation) | e12003abbb68db0e08fa13f44cfbf7c57c46c8b8 |
| HXC-1103 | Feature | P0 | Implement RBAC scopes and identity bindings (internal/security) | e12003abbb68db0e08fa13f44cfbf7c57c46c8b8 |
| HXC-1104 | Feature | P1 | Implement SPIFFE/SPIRE SVID issuance and mTLS identity (internal/security) | 7634fbbfaf4e66eba233c79adb5d2b4c169e9d36 |
| HXC-1105 | Feature | P0 | Implement htmux CLI raw-mode terminal client (cmd/htmux) | e12003abbb68db0e08fa13f44cfbf7c57c46c8b8 |
| HXC-1106 | Feature | P0 | Implement session I/O PTY-over-WebSocket forwarding (cmd/helix-session) | e12003abbb68db0e08fa13f44cfbf7c57c46c8b8 |
| HXC-1107 | Feature | P0 | Implement WebSocket envelope and MessagePack framing (pkg/websocket) | e12003abbb68db0e08fa13f44cfbf7c57c46c8b8 |
| HXC-1108 | Feature | P1 | Implement HelixQA live-service Challenge runner (cmd/helix-test) | 6c825dfc56a498b371578de921db8458a97d09f9 |
| HXC-1109 | Feature | P1 | Implement deterministic-sim, chaos, and device-sim test harness (cmd/helix-test) | 6c825dfc56a498b371578de921db8458a97d09f9 |
| HXC-1110 | Task | P1 | Wire Prometheus /metrics endpoint uniformly across all services (cmd/helix-*) | 1a67f0a |
| HXC-1111 | Task | P1 | Wire Jaeger/OTel trace-ID propagation across services (pkg/tracing) | 05f408d |
| HXC-1112 | Task | P1 | Wire pkg/health gRPC health protocol on every service (pkg/health, cmd/helix-*) | 05f408d |
| HXC-1113 | Bug | P2 | Remove dangling pkg/gpuattest placeholder and reuse security/pkg/gpuattest |  |
| HXC-1114 | Feature | P0 | Provision PostgreSQL primary schema with 15 tables, indexes, and triggers | e12003abbb68db0e08fa13f44cfbf7c57c46c8b8 |
| HXC-1115 | Task | P0 | Define etcd key namespace and key-builder constants (pkg/etcd) | e12003abbb68db0e08fa13f44cfbf7c57c46c8b8 |
| HXC-1116 | Feature | P1 | Define Redis cache, routing, and rate-limit key structure (pkg/storage) | 7634fbbfaf4e66eba233c79adb5d2b4c169e9d36 |
| HXC-1117 | Task | P0 | Define and generate proto service stubs (node/session/scheduler/health/auth) | bd605bf |
| HXC-1118 | Feature | P1 | Implement REST API surface on API Gateway with OpenAPI 3.0 | 6c825dfc56a498b371578de921db8458a97d09f9 |
| HXC-1119 | Feature | P1 | Implement Avro event schemas and schema-validated routing (Event Bus) | f2b9a4d |
| HXC-1120 | Task | P1 | Configure Kafka topics with partitions/replication/retention | 6c825dfc56a498b371578de921db8458a97d09f9 |
| HXC-1121 | Task | P0 | Configure NATS JetStream streams (HELIX_NODES/SESSIONS/SCHEDULER/HEALTH/ALERTS) | 6c825dfc56a498b371578de921db8458a97d09f9 |
| HXC-1122 | Feature | P2 | Implement Cap'n Proto zero-copy serialization for control-plane (pkg/serde) | 63822b3bcd439a360fae67d282fe7154af491c06 |
| HXC-1123 | Feature | P2 | Implement FlatBuffers serialization for GPU compute payloads | 63822b3bcd439a360fae67d282fe7154af491c06 |
| HXC-1124 | Feature | P2 | Implement ZeroMQ message patterns for internal data planes | 63822b3bcd439a360fae67d282fe7154af491c06 |
| HXC-1125 | Feature | P1 | Implement Setup Wizard single-command node onboarding (cmd, BASH+Go) | 9c58146 |
| HXC-1126 | Feature | P2 | Implement LLM Brain advisory engine with LLMsVerifier gate (internal/llm) | f4d8da9 |
| HXC-1127 | Feature | P1 | Implement OPA/WASM Policy Engine with HelixConstitution enforcement | 7634fbbfaf4e66eba233c79adb5d2b4c169e9d36 |
| HXC-1130 | Feature | P2 | Implement Zellij and GNU screen session backends | 15977c0 |
| HXC-1131 | Feature | P2 | Implement Backup Service (etcd snapshots, WAL archival, Ceph checkpoints) | 467a6dc62cc792f48a64c687cab03209ff99ff27 |
| HXC-1132 | Feature | P1 | Implement Metrics Collector node scrape endpoint and GPU metrics aggregation | 7634fbbfaf4e66eba233c79adb5d2b4c169e9d36 |
| HXC-1133 | Feature | P1 | Implement token-bucket rate limiter for API gateway (pkg/ratelimit) | e12003abbb68db0e08fa13f44cfbf7c57c46c8b8 |
| HXC-1134 | Feature | P2 | Implement Vault secret injection and rotation for all services | 467a6dc62cc792f48a64c687cab03209ff99ff27 |
| HXC-1135 | Feature | P0 | Implement two-node cluster formation end-to-end (exit gate) | 1c0d943 |
| HXC-1136 | Feature | P0 | Implement session-create end-to-end path (htmux new exit gate) | 553a043 |
| HXC-1137 | Task | P1 | Benchmark job scheduling decision latency under 100ms (exit gate) | 553a043 |
| HXC-1138 | Task | P1 | Enforce >60% pkg/ line coverage and paired mutation tests (Constitution 1.1) | 75f076f |
| HXC-1139 | Research | P2 | Author TLA+ specifications for consensus and scheduling concurrency | a39e89b |
| HXC-1141 | Feature | P2 | Implement interactive-mode AI CLI agent resource provisioning | 1a67f0a |
| HXC-1142 | Feature | P2 | Implement hybrid mode switching and batch/interactive resource sharing | 9c58146 |
| HXC-1144 | Feature | P2 | Implement WireGuard mesh network policies (segmentation) | 467a6dc62cc792f48a64c687cab03209ff99ff27 |
| HXC-1145 | Docs | P2 | Author MVP architecture and component-specification documentation artifacts | f4d8da9 |
| HXC-1146 | Docs | P0 | Document node-provisioning boundary: project does NOT implement jailbreak/exploit/DRM-circumvention | 2a90f56 |
| HXC-1147 | Feature | P1 | Implement internal/console/linux_boot.go BootCoordinator state machine | 4c2d4b0 |
| HXC-1148 | Feature | P1 | Replace pkg/wireguard NAT discovery with RFC 5389 STUN client | 7718c20 |
| HXC-1149 | Task | P3 | Return typed ErrUnsupported for UPnP/NAT-PMP in pkg/wireguard | 7718c20 |
| HXC-1150 | Feature | P1 | Add WireGuard key-rotation grace/overlap window with ActiveKeys() | 7718c20 |
| HXC-1151 | Task | P1 | Add pkg/discovery etcd-backed integration test against a real etcd | 7718c20 |
| HXC-1152 | Feature | P1 | Implement pkg/session container-checkpoint migration strategy (CRDT + manifest) | 1d79b5c |
| HXC-1156 | Feature | P1 | Implement thermal-aware scheduler plugin (throttle on overheat) | 7718c20 |
| HXC-1157 | Feature | P1 | Implement semi-trusted node output verification (redundant-compute/checksum) | 7718c20 |
| HXC-1158 | Task | P2 | Harden device hardware-identification in internal/console/detector.go (generic, fixture-tested) | da02870 |
| HXC-1160 | Feature | P2 | Improve tmux session backend to full PTY wrap on Attach | 1d79b5c |
| HXC-1162 | Task | P2 | E2E: multi-node session CRDT convergence | 8e8cebb |
| HXC-1164 | Docs | P2 | Author phase_02 architecture diagram (operator console + mesh + node integration) | 4c2d4b0 |
| HXC-1165 | Task | P2 | Ensure api/v1/node.proto carries trust_level + thermal fields consumed end-to-end | af16635 |
| HXC-1167 | Task | P0 | Build ARM64 cross-compilation toolchain for Helix agent | da02870 |
| HXC-1175 | Feature | P0 | Implement charging-gated PowerGater scheduling guard | 2a90f56 |
| HXC-1178 | Feature | P0 | Implement MQTT client for edge work dispatch and status | cd8bb86 |
| HXC-1184 | Feature | P0 | Implement edge node registration for all tiers (T3-T8) | 0bb95e9 |
| HXC-1185 | Feature | P0 | Implement edge heartbeat with battery/thermal/network telemetry | 2a90f56 |
| HXC-1186 | Feature | P0 | Implement edge protocol gateway (MQTT/QUIC/WebSocket) | a4c9bf7 |
| HXC-1187 | Feature | P0 | Define EdgeWorkUnit and EdgeWorkResult protobuf schemas | af16635 |
| HXC-1188 | Feature | P0 | Enforce work-unit resource limits (duration/memory/CPU) on edge devices | 7718c20 |
| HXC-1189 | Feature | P0 | Implement EdgeAwarePlugin scheduler Filter stage | 203c42a |
| HXC-1190 | Feature | P1 | Implement EdgeAwarePlugin scheduler Score stage | 203c42a |
| HXC-1191 | Feature | P1 | Implement declarative per-tier ScheduleRule engine | 7718c20 |
| HXC-1193 | Feature | P1 | Implement per-tier workload quantization selection | 0bb95e9 |
| HXC-1195 | Feature | P0 | Implement edge trust-level model (STANDARD/SEMI/EDGE_DONOR) | 7718c20 |
| HXC-1196 | Feature | P0 | Enforce workload restriction matrix by trust level | 7718c20 |
| HXC-1197 | Feature | P0 | Implement edge output verification (LLMsVerifier/redundant/checksum) | 0bb95e9 |
| HXC-1198 | Feature | P1 | Implement offline sync protocol with delta compression | 203c42a |
| HXC-1199 | Feature | P1 | Implement edge sensor-fusion framework and stream workload type | 75f076f |
| HXC-1201 | Feature | P2 | Implement low-bandwidth protocol optimizations (CoAP option) | 5ebb093 |
| HXC-1202 | Research | P3 | Implement WebRTC P2P data-channel transport for edge devices | 4261d34 |
| HXC-1207 | Task | P0 | Implement edge device chaos test suite | 646b1ad |
| HXC-1210 | Bug | P0 | Wire EtcdBackend into the node agent for cluster-wide discovery | 203c42a |
| HXC-1211 | Bug | P0 | Make internal/scheduler.StreamJobEvents a real event stream | 7718c20 |
| HXC-1212 | Feature | P0 | Connect pkg/classads to scheduler Filter/Score stages | 7718c20 |
| HXC-1213 | Feature | P0 | Add auth + RBAC enforcement to the gateway | 203c42a |
| HXC-1214 | Feature | P1 | Add S3-compatible (minio) backend to pkg/storage | 7718c20 |
| HXC-1215 | Feature | P1 | Add OTLP/Jaeger export shim for pkg/tracing | 7718c20 |
| HXC-1216 | Feature | P2 | Implement true embedded Raft consensus (pkg/raft) | a39e89b |
| HXC-1220 | Task | P2 | Replace custom policy engine with OPA/Rego | 203c42a |
| HXC-1229 | Task | P0 | Build device profile registry with versioned YAML schema for T1-T8 | 1c0d943 |
| HXC-1230 | Feature | P1 | Implement automated tier detection validating host KVM/ARM64/binfmt capabilities | f4d8da9 |
| HXC-1231 | Feature | P1 | Implement device provisioning lifecycle abstraction (Provisioner/Instance state machine) | 553a043 |
| HXC-1233 | Feature | P1 | Implement qcow2 copy-on-write overlay management with 10-deep chain limit | 9ee8592 |
| HXC-1236 | Feature | P0 | Implement INetwork/HelixNetwork trait with production and simulation swappable impls | 7c41c2f |
| HXC-1237 | Feature | P0 | Wire DST sim transport seam into pkg/swim membership (prod+sim parity) | 656bec1 |
| HXC-1238 | Feature | P1 | Implement BUGGIFY macro framework with ~25% deterministic fire rate | 75f076f |
| HXC-1239 | Feature | P1 | Implement 10:1 DST virtual-time compression by fast-forwarding idle periods | 75f076f |
| HXC-1240 | Feature | P1 | Achieve 1,000+ simulated nodes in a single DST process | 75f076f |
| HXC-1241 | Feature | P1 | Author DST consensus+gossip workloads using SETUP→EXECUTION→CHECK→METRICS pattern | 75f076f |
| HXC-1243 | Feature | P0 | Implement 8 network fault injectors (latency, loss, corruption, reorder, dup, bandwidth, partition, DNS, TCP reset) | 15977c0 |
| HXC-1244 | Feature | P0 | Implement 8 node fault injectors (VM crash/restart/pause, CPU/mem/disk pressure, OOM kill, graceful shutdown) | e93fad6 |
| HXC-1245 | Feature | P1 | Implement 3 time fault injectors (clock skew, clock freeze, monotonic drift) | 0bb95e9 |
| HXC-1247 | Feature | P1 | Implement pure-Go in-sim chaos faults toward 25+ (ClockSkew, DiskFill, MessageReorder, Byzantine, etc.) | 1c0d943 |
| HXC-1248 | Feature | P0 | Build YAML chaos Scenario Engine with phases, blast radius and abort-on-SLO-breach | e93fad6 |
| HXC-1249 | Feature | P0 | Implement emergency-stop and auto-recovery with <=2s halt latency | 15977c0 |
| HXC-1254 | Feature | P1 | Implement TestRunner with parallel suite execution and result collection | aba565b |
| HXC-1255 | Feature | P1 | Implement session test state machine (IDLE→SETUP→RUNNING→CHAOS_INJECT→VERIFY→RECOVERY→REPORT) | aba565b |
| HXC-1258 | Feature | P1 | Implement MetricsCollector exporting 15+ chaos Prometheus metric series with OpenTelemetry tracing | c642583 |
| HXC-1259 | Feature | P1 | Implement HelixQA automatic challenge generation from test outcomes | 58ae8b1 |
| HXC-1260 | Feature | P1 | Implement metrics validation against baseline KPI table with severity gating | 553a043 |
| HXC-1261 | Feature | P1 | Implement Welch's t-test statistical regression detector for HelixQA | 1c0d943 |
| HXC-1264 | Task | P1 | Define WIT interfaces for device-simulator, workload-generator, fault-injector, metrics-exporter | 765236d |
| HXC-1265 | Feature | P1 | Implement capability-based plugin sandbox with resource limits and audit logging | 7c41c2f |
| HXC-1267 | Feature | P0 | Implement cmd/helix-test CLI with dst/chaos/device/snapshot subcommands | aba565b |
| HXC-1268 | Feature | P1 | Build cmd/helix-snapshot standalone CLI reusing pkg/testing/snapshot | 7c41c2f |
| HXC-1278 | Research | P3 | Add property-based + coverage-guided fuzzing for cluster operations | 656bec1 |
| HXC-1290 | Feature | P0 | Implement pkg/device universal capability Descriptor with 15-tier + trust enums | 9baae11 |
| HXC-1291 | Feature | P0 | Implement device.Probe(ctx) arch/CPU/mem auto-detection with override labels | e4f0b03 |
| HXC-1292 | Feature | P0 | Implement device.AssignTier decision tree per §50 taxonomy (riscv64->T10, x86+open-fw->T1, GPU+battery->T9) | 9baae11 |
| HXC-1293 | Feature | P0 | Extend api/v1/node.proto with tier, trust_level, compute_class, arch fields | af16635 |
| HXC-1294 | Feature | P0 | Create api/v1/resources.proto with npu_tops, fpga_logic_elements, tflops_gpu | af16635 |
| HXC-1295 | Feature | P0 | Extend api/v1/scheduler.proto with tier constraints and power_budget | af16635 |
| HXC-1296 | Feature | P0 | Implement device.Descriptor <-> pb.Node round-trip mapping helpers | 441c7fe |
| HXC-1297 | Feature | P0 | Implement pkg/scheduler tier-aware + power-aware Plugin (tier_power.go) | 9baae11 |
| HXC-1298 | Feature | P0 | Implement power-aware T9 HANDHELD scheduling policy (battery/thermal/gaming-active gating) | 441c7fe |
| HXC-1299 | Feature | P0 | Implement pkg/discovery 15-tier registry + SelectByTier(tier, minTrust) | e4f0b03 |
| HXC-1300 | Feature | P0 | Extend pkg/resources to probe NPU TOPS, FPGA logic elements, GPU TFLOPS, QPU | 2a90f56 |
| HXC-1301 | Feature | P1 | Implement pkg/cloudspot PreemptionWatcher with injectable SignalSource and ordered hooks | d4829d1 |
| HXC-1302 | Feature | P1 | Extend pkg/wireguard with preemption-safe teardown / spot-drain hook | e4f0b03 |
| HXC-1303 | Feature | P1 | Implement pkg/inference provider-agnostic Backend interface + capability/tier Router | d4829d1 |
| HXC-1304 | Feature | P1 | Implement pkg/inference LocalBackend wrapping internal/llm manager | 0346e1c |
| HXC-1305 | Feature | P1 | Implement device discovery engine: GPU detection (Vulkan/CUDA/ROCm/Mali strategies) | e3099cf |
| HXC-1306 | Feature | P1 | Implement device discovery engine: NPU detection (Rockchip/NVIDIA DLA/Qualcomm/Apple) | 583c6ca |
| HXC-1307 | Feature | P2 | Implement device discovery engine: FPGA detection (Xilinx/Intel/Lattice, hard vs soft core) | b545d84 |
| HXC-1308 | Feature | P2 | Implement micro-benchmarker producing normalized per-compute-class scores | f4d8da9 |
| HXC-1309 | Feature | P1 | Implement capability manifest generation + control-plane negotiation | 14f13c6 |
| HXC-1310 | Feature | P0 | Implement per-tier security model enforcement (sandbox + data/network policy by trust level) | 441c7fe |
| HXC-1311 | Docs | P1 | Author complete YAML tier definitions (T1-T15) with min_requirements and constraints | d4829d1 |
| HXC-1328 | Feature | P1 | Implement cloud-spot AWS/Azure/GCP IMDS interruption pollers (adapters) | f4d8da9 |
| HXC-1331 | Docs | P2 | Author 64-device master taxonomy table as machine-readable device catalog | cbe329a |
| HXC-1334 | Feature | P0 | Implement pkg/hlc Hybrid Logical Clock with drift clamp and total order | 0346e1c |
| HXC-1335 | Feature | P0 | Implement pkg/crdt G-Counter with per-replica max-merge convergence | 0346e1c |
| HXC-1336 | Feature | P0 | Implement pkg/crdt LWW-Register with HLC last-writer-wins tiebreak | 0346e1c |
| HXC-1337 | Feature | P0 | Implement pkg/crdt OR-Set with unique-tag add/remove semantics | 0346e1c |
| HXC-1338 | Feature | P1 | Implement pkg/crdt/merkle anti-entropy tree with divergent-key Diff | 0346e1c |
| HXC-1339 | Feature | P1 | Implement Vector Clock causality type for Tier-2 operational state | 37c2a9a |
| HXC-1340 | Feature | P0 | Implement pkg/federation Cell model and lifecycle state machine | 0346e1c |
| HXC-1341 | Feature | P0 | Implement thread-safe Cell Registry with add/remove/list/snapshot | 0346e1c |
| HXC-1342 | Feature | P1 | Implement pkg/federation federated API aggregation with partial failure | 0c8b4ca |
| HXC-1343 | Feature | P1 | Implement Phi-accrual failure detector in pkg/swim | 37c2a9a |
| HXC-1344 | Feature | P0 | Implement pkg/swim/hierarchical two-tier LAN+WAN delegate gossip | 7f465a6 |
| HXC-1345 | Feature | P1 | Implement gateway-relayed cross-cell suspicion propagation | 7f465a6 |
| HXC-1346 | Feature | P1 | Implement bootstrap/rendezvous strategies (static, DNS-SRV, DHT, cloud, mDNS) | ddd5efa |
| HXC-1347 | Feature | P1 | Implement mDNS/DNS-SD local cell discovery advertiser and browser | e5f963b |
| HXC-1348 | Feature | P0 | Implement pkg/nattraversal STUN client and NAT-type classification | 7f465a6 |
| HXC-1349 | Feature | P0 | Implement ICE candidate gathering, prioritization and connectivity checks | 18c4d60 |
| HXC-1350 | Feature | P1 | Implement TURN relay allocation and fallback chain | 48e13fe |
| HXC-1351 | Feature | P1 | Implement QUIC fallback transport with 0-RTT and connection migration | d9718e8 |
| HXC-1352 | Feature | P0 | Implement internal/cell WireGuard inter-cell mesh manager | 18c4d60 |
| HXC-1353 | Feature | P1 | Implement WireGuard zero-downtime key rotation with overlap window | 0c8b4ca |
| HXC-1356 | Feature | P2 | Integrate libp2p DHT discovery, GossipSub and circuit relay | 8b0bb7a |
| HXC-1357 | Feature | P1 | Implement pkg/spiffe/federation trust-bundle exchange across cells | 18c4d60 |
| HXC-1358 | Feature | P2 | Implement OPA/Gatekeeper cross-cluster federated-trust admission | 14f13c6 |
| HXC-1359 | Feature | P2 | Implement OPA data-residency / data-sovereignty admission policy | 996610f |
| HXC-1361 | Feature | P1 | Implement double-encryption (WireGuard L3 + mTLS L7) data path | 18c4d60 |
| HXC-1363 | Feature | P1 | Implement federated service discovery (cell-local + global registry) | ddd5efa |
| HXC-1364 | Feature | P1 | Implement internal/federation Karmada PropagationPolicy engine | e5f963b |
| HXC-1365 | Feature | P1 | Extend pkg/scheduler with latency-aware spot/preemptible scoring | 0c8b4ca |
| HXC-1367 | Feature | P1 | Implement pkg/gitops ArgoCD ApplicationSet federation client | e5f963b |
| HXC-1368 | Feature | P2 | Implement CRDT-based config sync for cell-local overrides | 996610f |
| HXC-1371 | Task | P1 | Enforce etcd-stays-within-cell static-analysis lint gate | ddd5efa |
| HXC-1372 | Feature | P2 | Implement per-cell etcd Raft WAN-safe tuning profiles | 996610f |
| HXC-1373 | Feature | P1 | Implement split-brain prevention and partition-classification logic | 37c2a9a |
| HXC-1374 | Feature | P2 | Implement internal/chaos CE-01..CE-12 chaos experiment suite | 14f13c6 |
| HXC-1375 | Feature | P2 | Implement Turmoil-style deterministic multi-cluster simulation harness | 9a4ecbd |
| HXC-1377 | Docs | P2 | Author Phase 6 FMEA catalog with detection and recovery runbooks | 9a4ecbd |
| HXC-1380 | Feature | P1 | Implement split-brain detection PromQL alerts with runbook links | 7f465a6 |
| HXC-1381 | Feature | P3 | Build Grafana global-health cell-status-grid dashboard | 9a4ecbd |
| HXC-1383 | Feature | P2 | Implement five federation topology patterns and block-binding modes | 996610f |
| HXC-1386 | Task | P1 | Verify sub-phase dependency gates 6a->6b->6c->6d soak conditions | 14f13c6 |
| HXC-1387 | Feature | P0 | Implement Multi-Raft per-shard consensus manager (G-01, P0) | ca80b35 |
| HXC-1388 | Feature | P0 | Implement heartbeat coalescer for Multi-Raft network overhead control | f6bb012 |
| HXC-1389 | Feature | P1 | Implement leaseholder fast-path local reads in Multi-Raft | 28fd683 |
| HXC-1390 | Feature | P0 | Implement MVCC revision store with B-tree index and time-travel (G-08, P0) | f6bb012 |
| HXC-1391 | Feature | P1 | Implement persistent Watch Manager with synced/unsynced groups (G-08 support) | 5c7d8db |
| HXC-1392 | Feature | P0 | Implement SLURM-style backfill scheduler over availability timeline (G-02, P0) | 3d1daa9 |
| HXC-1393 | Feature | P1 | Implement multifactor priority queue for scheduler (age+fairshare+size+QoS) | 4ec944c |
| HXC-1394 | Feature | P0 | Implement CRC16 16,384 hash-slot session router (G-03, P0) | 3d1daa9 |
| HXC-1395 | Feature | P0 | Implement MOVED/ASK redirection and ASKING for in-flight slot migration (G-03) | 3d1daa9 |
| HXC-1396 | Feature | P2 | Implement Atomic Slot Migration controller for live session migration (G-03, P2) | 5c7d8db |
| HXC-1397 | Feature | P2 | Add config-epoch conflict resolution for simultaneous failovers | ac17134 |
| HXC-1398 | Feature | P1 | Add Startup health-probe tier with GPU grace period (G-04, P1) | 5c7d8db |
| HXC-1399 | Feature | P1 | Implement SWIM majority PFAIL->FAIL two-phase failure confirmation (G-23, P1) | 5c7d8db |
| HXC-1400 | Feature | P0 | Implement largest-subcluster-wins voting quorum (G-19, P0) | 3d1daa9 |
| HXC-1401 | Feature | P1 | Implement STONITH fencing agents (IPMI/cloud/shared-disk) (G-19, P1) | ca80b35 |
| HXC-1402 | Feature | P2 | Implement Pacemaker-style four-type constraint engine (G-20, P2) | f6bb012 |
| HXC-1403 | Feature | P2 | Implement SCAN stable virtual-IP/DNS client endpoint (G-21, P2) | b60e36f |
| HXC-1404 | Feature | P1 | Implement N+K failover capacity admission control (G-22, P1) | dbcd006 |
| HXC-1405 | Feature | P1 | Implement gang scheduling all-or-nothing GPU reservation (G-18, P1) | dbcd006 |
| HXC-1406 | Feature | P2 | Implement topology-aware NUMA/NVLink GPU placement scoring (G-10/G-18, P2) | b60e36f |
| HXC-1407 | Feature | P1 | Implement device-plugin / GRES fingerprinting framework (G-11/G-13/G-17, P1) | ca80b35 |
| HXC-1408 | Feature | P1 | Implement BOINC-style redundant-execution trust scorer (G-09, P1) | 4ec944c |
| HXC-1409 | Feature | P2 | Implement standalone delta-state CRDT package (G-counter/PN-counter/OR-set/LWW-map) (G-01 part, P2) | b60e36f |
| HXC-1410 | Feature | P1 | Implement Cassandra 3-layer repair: hinted handoff + read repair + Merkle anti-entropy (G-01 part, P1) | 5485d4b |
| HXC-1411 | Feature | P2 | Implement Informer cache helixcache.Watcher (list-watch local cache) (G-05, P2) | 4ec944c |
| HXC-1412 | Feature | P2 | Implement rate-limited work queue with exponential backoff (G-06, P2) | f6bb012 |
| HXC-1413 | Feature | P2 | Implement API Priority & Fairness FlowSchema->PriorityLevel->Queue (G-07, P2) | 4ec944c |
| HXC-1415 | Feature | P2 | Implement idempotent producer (PID + sequence) for exactly-once messaging | 5485d4b |
| HXC-1416 | Feature | P2 | Implement cooperative incremental rebalancing for consumers/membership | 5485d4b |
| HXC-1417 | Feature | P3 | Implement embedded KRaft-style Raft quorum to remove external ZooKeeper | 28fd683 |
| HXC-1418 | Feature | P2 | Implement tiered cache hot(memory)/warm(NVMe)/cold(SSD) data tiers | ac17134 |
| HXC-1419 | Feature | P0 | Build deterministic simulation testing (DST) framework on Turmoil (G-14, P0) | 28fd683 |
| HXC-1420 | Feature | P0 | Implement BUGGIFY chaos macros at 25% deterministic fire rate (G-15, P0) | 0563e52 |
| HXC-1421 | Feature | P1 | Integrate Porcupine linearizability checking in CI under fault injection (G-16, P1) | 28fd683 |
| HXC-1422 | Feature | P2 | Build nightly chaos pipeline (pod kill, partition, disk stall, clock skew) (G-15 part) | 28fd683 |
| HXC-1424 | Docs | P2 | Produce hardened HelixCluster architecture diagram and component map | b75a553 |
| HXC-1425 | Docs | P1 | Produce Phase 7 phase-by-phase 23-gap matrix as tracked deliverable ledger | 70f6634 |
| HXC-1426 | Feature | P1 | Implement pkg/chutes Chutes OpenAI-compatible API Client (non-streaming) | b31d53a |
| HXC-1427 | Feature | P1 | Implement Chutes API client fallback-model chain on retriable failure | b31d53a |
| HXC-1428 | Feature | P1 | Implement SSE streaming decoder for Chutes chat completions | b31d53a |
| HXC-1429 | Feature | P1 | Implement Chutes API model-list and user-account/balance queries | 1c8d0d1 |
| HXC-1430 | Feature | P1 | Implement model-router default-model resolution by strategy (latency/throughput/quality/cost) | 9d02879 |
| HXC-1431 | Feature | P1 | Wire E2EE inference envelope between pkg/chutes and security/pkg/e2ee | 2278f85 |
| HXC-1435 | Feature | P0 | Implement GraValVerifier.BatchVerify concurrent attestation with pass-rate KPI | 1c8d0d1 |
| HXC-1436 | Feature | P0 | Implement GraVal VRAM-threshold (95%) gate in attestation | 1c8d0d1 |
| HXC-1439 | Feature | P0 | Define ChutesMinerConfig and ValidatorConfig data structures | ca80b35 |
| HXC-1446 | Feature | P0 | Implement custom HelixGepetto dual-resource arbitration strategy | 1c8d0d1 |
| HXC-1447 | Feature | P0 | Implement dual-workload capacity reservation in internal/gpu | ca80b35 |
| HXC-1448 | Feature | P1 | Implement GraVal attestation hook in internal/gpu Manager | 5b4a875 |
| HXC-1449 | Feature | P2 | Implement MIG profile management in internal/gpu | 7aabc72 |
| HXC-1454 | Feature | P1 | Implement MarketplaceAdapter interface and Chutes adapter | ba72e83 |
| HXC-1455 | Feature | P1 | Implement UnifiedManager.RouteWorkload concurrent pricing + composite scoring | 7e01512 |
| HXC-1456 | Feature | P2 | Implement RevenueOptimizer.OptimizeAllocation greedy GPU-to-marketplace assignment | cbe329a |
| HXC-1458 | Feature | P2 | Implement Akash marketplace adapter | bd90b85 |
| HXC-1460 | Feature | P2 | Implement pkg/economics RewardDistributor multi-token distribution | cbe329a |
| HXC-1461 | Feature | P2 | Implement RewardDistributor.GetParticipantROI and break-even tracking | cbe329a |
| HXC-1469 | Feature | P1 | Wire internal/gateway to route internal AI requests through Chutes API client | 7aabc72 |
| HXC-1470 | Feature | P1 | Replace internal/llm stub Inference with real model router (latency/throughput/quality/cost) | 5b4a875 |
| HXC-1473 | Feature | P2 | Enable AWQ 4-bit quantization as default model format | 7aabc72 |
| HXC-1476 | Feature | P2 | Implement hybrid PQC TLS (X25519 + ML-KEM-768) node-to-node transport | f983f8b |
| HXC-1480 | Feature | P2 | Implement carbon-aware scheduler with per-job energy metering | f983f8b |
| HXC-1481 | Feature | P2 | Implement EU AI Act compliance documentation pipeline | f983f8b |
| HXC-1482 | Feature | P2 | Implement export-control tier verification (country-code KYC) at node onboarding | cbe329a |
| HXC-1483 | Feature | P1 | Extend pkg/metrics + Prometheus/Grafana with TAO/GraVal/throughput dashboards | 5b4a875 |
| HXC-1484 | Feature | P1 | Extend pkg/health with miner-api and GraVal DaemonSet health checks | e5f963b |
| HXC-1493 | Feature | P0 | Implement GPUTier type with priority ordering (Local..Decentralized) | b31d53a |
| HXC-1494 | Feature | P0 | Implement PoolManager registry with tier-ordered Allocate walk | a442a9a |
| HXC-1495 | Feature | P0 | Implement WorkloadSpec/GPUDevice/GPUAllocation/PoolStatus data structures | 7aabc72 |
| HXC-1496 | Feature | P0 | Implement candidate filtering (model/VRAM/cost/label selector) | a442a9a |
| HXC-1497 | Feature | P1 | Implement global MaxCostPerHour budget cap on allocations | 5485d4b |
| HXC-1498 | Feature | P0 | Implement PriorityScheduler (tier then least-load then cost) | a442a9a |
| HXC-1499 | Feature | P1 | Implement CostAwareScheduler selecting cheapest GPUs meeting SLA | 4f6f440 |
| HXC-1500 | Feature | P1 | Implement LatencyAwareScheduler for inference routing | 4f6f440 |
| HXC-1501 | Feature | P1 | Implement HealthMonitor with 30s checks and auto-failover trigger | 4f6f440 |
| HXC-1502 | Feature | P0 | Implement internal/costbroker ComputeBroker weighted scorer | a733d87 |
| HXC-1503 | Feature | P1 | Implement ComputeBroker 60s re-scoring loop with price-spike guard | a442a9a |
| HXC-1504 | Feature | P0 | Implement pkg/burst BurstController MONITOR->SPILL->RECOVER hysteresis | 9d02879 |
| HXC-1505 | Feature | P1 | Implement BurstController utilization RingBuffer moving average | 7558107 |
| HXC-1506 | Feature | P1 | Implement BurstController activateBurst capacity estimation + allocation | a442a9a |
| HXC-1507 | Feature | P1 | Implement pkg/burst CostRouter per-workload-type provider scoring | 7558107 |
| HXC-1508 | Feature | P1 | Implement 5-tier fallback chain Chutes->io.net->RunPod->AWS | 9d02879 |
| HXC-1509 | Feature | P2 | Implement QoS tiers (real-time, interactive, batch, best-effort) | af7b7f5 |
| HXC-1510 | Feature | P0 | Implement pkg/provider/chutes ChutesProvider OpenAI-compatible adapter | 7aabc72 |
| HXC-1511 | Feature | P1 | Implement Chutes retryWithFallback across fallback models on 429 | a442a9a |
| HXC-1512 | Feature | P2 | Implement Chutes GetBalance USD balance monitor | b60e36f |
| HXC-1513 | Feature | P2 | Implement Chutes streaming (SSE) chat-completion variant | b31d53a |
| HXC-1514 | Feature | P1 | Implement pkg/provider/ionet IONetProvider Ray cluster adapter | e5f963b |
| HXC-1515 | Feature | P1 | Implement pkg/provider/runpod RunPodProvider serverless + warm pool | 7783030 |
| HXC-1516 | Feature | P2 | Implement pkg/provider/aws AWSProvider EC2 Spot adapter | 7783030 |
| HXC-1519 | Feature | P0 | Implement pkg/local LocalGPURegistrar with TCO effective-cost | a733d87 |
| HXC-1523 | Feature | P1 | Implement pkg/e2ee GraValVerifier provider admission gate | 9d02879 |
| HXC-1527 | Feature | P1 | Define gpu_proxy.proto gRPC GPUProvider kernel-dispatch service | af16635 |
| HXC-1529 | Feature | P1 | Implement workload-suitability classifier (HPC local vs inference remote) | af7b7f5 |
| HXC-1530 | Feature | P0 | Implement cmd/gpu-pool-manager HTTP+gRPC front-end binary | ba72e83 |
| HXC-1531 | Feature | P0 | Implement cmd/burst-controller utilization-driven binary | ac17134 |
| HXC-1532 | Feature | P0 | Implement cmd/e2ee-proxy transparent ML-KEM-768 proxy binary | c10b33f |
| HXC-1533 | Task | P1 | Add ProviderAdapter registration hooks to pkg/gpu | f4d8da9 |
| HXC-1534 | Feature | P1 | Add GPUTier-aware filter predicate to pkg/scheduler | 7783030 |
| HXC-1535 | Feature | P1 | Add GPU-tier utilization/cost/provider-health metrics to pkg/metrics | 7e01512 |
| HXC-1538 | Feature | P2 | Implement @helix.task Go decorator/SDK with lifecycle hooks | a442a9a |
| HXC-1539 | Feature | P2 | Implement intelligent ModelRouter (latency/throughput/cost/tee/balanced) | ac17134 |
| HXC-1544 | Research | P3 | Implement predictive scaling forecast to pre-warm before peaks | e12881c |
| HXC-1547 | Feature | P2 | Implement TCO calculator for hybrid own+burst economic model | e12881c |
| HXC-1548 | Feature | P1 | Implement CostTracker monthly cost report vs AWS on-demand | e12881c |
| HXC-1551 | Task | P1 | Build Prometheus/Grafana Phase 8B tier-utilization + cost dashboards | 7783030 |
| HXC-1556 | Task | P1 | Benchmark ML-KEM-768 E2EE handshake latency (<1ms target) | c10b33f |
| HXC-1561 | Feature | P0 | Add ChaCha20-Poly1305 AEAD cipher variant to pkg/security/e2ee | 69c1a89 (security fbdab51) |
| HXC-1563 | Feature | P1 | Implement bidirectional per-request response keypair in pkg/security/e2ee | 831089a (security f09227f) |
| HXC-1564 | Feature | P2 | Implement gzip-before-encrypt payload compression in pkg/security/e2ee | f4d8da9 |
| HXC-1565 | Feature | P1 | Implement streaming SSE E2EE decryption (e2e_init + per-chunk) in pkg/security/e2ee | 02c42d4 (security 16ae574) |
| HXC-1567 | Feature | P1 | Maintain E2EE length-prefixed framed transport over io.ReadWriter | 31ff29f (security 29a6193) |
| HXC-1568 | Feature | P0 | Implement pkg/gpuattest device-info challenge/response + fingerprint | 4f6f440 |
| HXC-1569 | Feature | P0 | Implement seeded matmul proof-of-GPU-work (PoVW) in pkg/gpuattest | 4f6f440 |
| HXC-1570 | Feature | P0 | Implement O(1) spot-check verification in pkg/gpuattest | 4f6f440 |
| HXC-1571 | Feature | P0 | Implement device-sealed encrypt/decrypt (SealForDevice/OpenFromDevice) in pkg/gpuattest | 4f6f440 |
| HXC-1572 | Feature | P2 | Implement filesystem residency challenge in pkg/gpuattest | b60e36f |
| HXC-1573 | Feature | P2 | Implement multi-GPU node enumeration in pkg/gpuattest | f983f8b |
| HXC-1579 | Feature | P2 | Implement pkg/security/admission OPA-style policy + signed-image (cosign) gate | 9ad74be |
| HXC-1580 | Feature | P1 | Implement pkg/modelintegrity hf_cache_verify gate (SHA-256 + size) | a733d87 |
| HXC-1581 | Feature | P2 | Implement model/revision anti-cheat verification token (V2 HMAC-SHA256) | ec5bdc8 |
| HXC-1582 | Feature | P3 | Implement X25519 ephemeral session-key handshake for verification tokens | 9ad74be |
| HXC-1583 | Feature | P1 | Maintain pkg/scheduler cost-aware GPU placement plugin | b31d53a |
| HXC-1584 | Feature | P1 | Implement value-multiplier preemption in pkg/scheduler (Gepetto model) | 70f6634 |
| HXC-1585 | Feature | P1 | Implement SKIP LOCKED-style optimistic work-claiming in pkg/scheduler | 70f6634 |
| HXC-1586 | Feature | P1 | Implement attestation-gated scheduler admission predicate | 70f6634 |
| HXC-1587 | Feature | P2 | Implement bounty/auction economic-placement plugin for scheduler | ec5bdc8 |
| HXC-1588 | Feature | P2 | Implement utilization-aware EWMA candidate ranking for scheduler/orchestrator | af7b7f5 |
| HXC-1589 | Feature | P2 | Implement Kueue-style suspend-then-admit tiered job admission | ec5bdc8 |
| HXC-1590 | Feature | P1 | Implement NodeSelector placement-constraint schema in pkg/resources | 7558107 |
| HXC-1592 | Feature | P1 | Implement SUPPORTED_GPUS compute-multiplier catalog + LookupMultiplier | 9d02879 |
| HXC-1595 | Feature | P1 | Implement streaming-safe failover (retry only before first byte) in LLMOrchestrator | ec5bdc8 |
| HXC-1596 | Feature | P2 | Implement per-task ordered fallback chains with dedup/cap + empty-response detection | af7b7f5 |
| HXC-1597 | Feature | P2 | Implement thermal pre-warm / scale-from-zero state machine (Therm) | e12881c |
| HXC-1598 | Feature | P2 | Implement passthrough cords + disconnect-aware upstream abort in LLMOrchestrator | 9ad74be |
| HXC-1599 | Feature | P3 | Implement Claude/Responses API-shape adapters for LLMOrchestrator front door | b60e36f |
| HXC-1600 | Feature | P3 | Implement error-classified LLM failover taxonomy + sandbox scheduling-hint contract | b60e36f |
| HXC-1601 | Feature | P2 | Implement pkg/inferenceproxy correlation-ID + backend audit trail | 9ad74be |
| HXC-1602 | Feature | P2 | Implement deterministic keyed-hash anonymization in pkg/inferenceproxy | 9ad74be |
| HXC-1603 | Feature | P2 | Implement spoof-proof managed-header sanitization at the recording edge | 9ad74be |
| HXC-1604 | Feature | P1 | Maintain pkg/fiber length-prefixed framed miner<->validator transport | b31d53a |
| HXC-1605 | Feature | P1 | Implement fiber ed25519 signed-identity handshake + stake-gated admission | b31d53a |
| HXC-1606 | Feature | P2 | Implement node identity verify-then-pin (TOFU) keypair-rooted TLS | 7558107 |
| HXC-1607 | Feature | P2 | Implement pkg/marketplace registration -> bounty -> metering -> payout loop | 9ad74be |
| HXC-1608 | Feature | P2 | Implement pkg/marketplace/audit commit-then-prove reproducible reconciliation | 9ad74be |
| HXC-1609 | Feature | P3 | Implement scale-to-zero metered hot/cold billing state machine | a442a9a |
| HXC-1610 | Feature | P3 | Implement watchtower-style liveness/integrity prober for served instances | a442a9a |
| HXC-1612 | Feature | P3 | Implement OpenAPI request-validation admission middleware for control-plane APIs | a442a9a |
| HXC-1613 | Docs | P1 | Document Phase 8C exit-gate evidence matrix (CLAUDE-1 usability proofs) | da02870 |
| HXC-1614 | Docs | P2 | Document Phase 8C architecture integration diagrams (scheduler<-attest, orchestrator<-e2ee<-TEE, marketplace) | da02870 |
| HXC-1616 | Bug | P2 | Honor WithClock in cloudspot Azure/GCP pollers (currently silently ignored) | 31f4139 |
| HXC-1621 | Bug | P1 | SELinux bind-mount relabel hardcoded :Z — make conditional + cross-platform (containers/pkg/crossbuild) | da02870 (main pointer); containers submodule 1598f28 |
| HXC-1626 | Feature | P2 | HXC-1119 follow-up: wire pkg/events Avro codec into the NATS HelixBackend wire path + schemas for SessionEvent/SchedulerEvent/AuditEvent | c700da5 |
| HXC-1627 | Feature | P2 | HXC-1626 follow-up: NATSBackend typed Avro publish/subscribe for SessionEvent/SchedulerEvent/AuditEvent | feb8f0a |
| HXC-1628 | Task | P2 | buf lint STYLE conformance for api/v1 protos (PACKAGE_DIRECTORY_MATCH + RPC-response naming) | 3a7812b |
| HXC-1629 | Task | P2 | Wire Makefile migrate-up/migrate-down to scripts/run-migrations.sh (PRR item 46) | a2643db |
| HXC-1630 | Task | P1 | Run govulncheck vulnerability scan across main/api/v1/security modules (PRR security gap) | a2643db |
| HXC-1631 | Bug | P1 | govulncheck: bump Go toolchain (go.mod go-directive) to clear 9 reachable stdlib advisories | b96a8d5 |
| HXC-1632 | Bug | P2 | govulncheck: bump golang.org/x/net v0.54.0 -> v0.55.0 (GO-2026-5026 idna) | b96a8d5 |
| HXC-1633 | Feature | P1 | Implement real wireguard-go userspace path on macOS (CLAUDE-2 parity for pkg/wireguard) | 69c1a89 |
| HXC-1634 | Feature | P2 | Implement real darwin GPU/device probes (Metal/IOKit) for internal/gpu + pkg/device (CLAUDE-2) | 69c1a89 |
| HXC-1635 | Task | P2 | Add SBOM generation + dependabot/renovate to complete supply-chain posture (PRR items 34/80) | 69c1a89 |
| HXC-1636 | Task | P2 | Local make deps-update target (no-CI dependency-update maintenance, PRR item 80) | 1a3c3d7 |
| HXC-1637 | Bug | P1 | make build is broken: builds non-existent ./cmd/helix-cluster | c99d225 |
| HXC-1638 | Bug | P2 | helixctl binary referenced in docs but absent from cmd/ | c99d225 |
| HXC-1639 | Bug | P2 | SQL schema drift: 0001_primary_schema.sql diverges from the golang-migrate chain (001-015) | 9fef232 |
| HXC-1640 | Feature | P1 | pkg/stonith fencing-safety test (positive confirmation + fenced node cannot act) | 4cff5ea |
| HXC-1641 | Bug | P1 | FIX registry NextHXCID lexical-vs-numeric id-allocation bug | 34482ee |
| HXC-1642 | Feature | P1 | pkg/epochresolve adversarial fencing test (stale epoch rejected, at-most-one owner) | fa82504 |
| HXC-1643 | Bug | P0 | FIX pkg/idempotent concurrency bug (data race + double-commit broke exactly-once) | c48a8c0 |
| HXC-1644 | Feature | P1 | pkg/workclaim concurrent mutual-exclusion test (exactly-one claimant) | 2ecd5f2 |
| HXC-1645 | Feature | P1 | pkg/voting exhaustive at-most-one-authoritative test (split-brain invariant) | f74ce34 |
| HXC-1646 | Feature | P1 | pkg/semaphore adversarial bounded-concurrency test (never exceed N, reaches N) | 0a0caac |
| HXC-1647 | Feature | P1 | pkg/lru adversarial eviction + capacity test (true LRU not FIFO, exact bound) | 771c77f |
| HXC-1648 | Bug | P0 | FIX pkg/lock double-release crash + cross-owner free (unguarded UnlockFunc) | a3f3393 |
| HXC-1649 | Bug | P0 | FIX pkg/heartbeatcoalescer Receiver data race (unsynchronized lastSeen) | 0bc06ee |
| HXC-1650 | Bug | P1 | harden EtcdLocker fire-once release guard (follow-up to HXC-1648) | 85397fb |
| HXC-1651 | Feature | P1 | pkg/flowcontrol concurrent credit-conservation test (never over-admit) | 916e8cf |
| HXC-1652 | Feature | P1 | pkg/ratelimit UserLimiter adversarial over-admission test (dual-bucket, frozen clock) | af8455d |
| HXC-1653 | Feature | P1 | pkg/pool GPU-instance-pool adversarial concurrency test (no double-issue, capacity bound) | 916ea58 |
| HXC-1654 | Feature | P1 | pkg/watchmanager adversarial concurrency test (register/notify/sync churn) | 47a0599 |
| HXC-1655 | Feature | P1 | pkg/multiraft adversarial registry-concurrency test (concurrent create/route/remove) | 87efe0e |
| HXC-1656 | Feature | P1 | pkg/pubsub adversarial concurrent sub/unsub/publish test (no send-on-closed) | b388843 |
| HXC-1657 | Feature | P1 | pkg/tieredcache coherence test (re-Put shadows stale lower tier, no contamination) | 57a2a75 |
| HXC-1658 | Feature | P1 | pkg/session adversarial concurrent FSM test (at-most-once terminal transition) | 5f070a5 |
| HXC-1659 | Bug | P1 | FIX pkg/offlinesync Reconcile conflict winner non-deterministic (violated documented lower-Seq policy) | 681c24b |
| HXC-1660 | Feature | P1 | pkg/workqueue adversarial concurrency test (no double-processing, dedup three-set safety) | fab2305 |
| HXC-1661 | Feature | P1 | pkg/metering conservation test (exact revenue accounting, per-key isolation) | a970df4 |
| HXC-1662 | Feature | P1 | pkg/budgetcap concurrent over-spend test (cap never exceeded) | 72b254c |
| HXC-1663 | Feature | P1 | pkg/billingfsm adversarial money-safety test (transition matrix, no double-bill, conservation) | 13686ac |
| HXC-1664 | Feature | P1 | pkg/informer cache-coherence test (latest-delta-wins, delete coherence, no resurrection) | f2afc09 |
| HXC-1665 | Feature | P1 | pkg/rebalance adversarial minimal-disruption test (sticky, balance, idempotence) | a3c6a49 |
| HXC-1666 | Bug | P0 | FIX pkg/slotmigration concurrent-map crash (routing lookups raced the commit flip) | 2895ae5 |
| HXC-1667 | Bug | P0 | FIX pkg/edgeregistry concurrent-map race (Register raced Get/Len/ListByTier/Dump) | d623d24 |
| HXC-1668 | Feature | P1 | pkg/modelrouter concurrent read-safety test (immutable routing table) | 8b17477 |
| HXC-1669 | Feature | P1 | pkg/fedtopology concurrent read-safety + path-correctness test (immutable topology) | ab37c75 |
| HXC-1670 | Bug | P0 | FIX pkg/jwt keystore concurrent-map crash on auth path (verify raced rotation) | 698ef55 |
| HXC-1671 | Feature | P1 | pkg/ewmarank concurrency-contract + ranking-correctness test (documented single-threaded) | 2b0c1c3 |
| HXC-1672 | Bug | P0 | FIX pkg/jobadmit concurrent-map crash (admit/complete raced state lookups) | cc67630 |
| HXC-1673 | Feature | P1 | pkg/failconfirm concurrency-contract + quorum-confirmation test (documented single-threaded) | e4d4e98 |
| HXC-1674 | Bug | P1 | FIX pkg/redundantexec concurrent-map + trust-field race (validate raced trust reads) | c24ac4d |
| HXC-1675 | Bug | P1 | FIX pkg/rescorer concurrent ranking/map race (Tick rerank raced Top/Ranking) | 46c06d5 |
| HXC-1676 | Feature | P1 | pkg/fedtrust concurrent trust-policy test (immutable-after-build, deny-never-permitted) | 8a1413d |
| HXC-1677 | Bug | P1 | FIX pkg/thermalwarm concurrent-map + state-field race (dispatch/state raced warming) | 0c820ba |
| HXC-1678 | Feature | P1 | pkg/phasegate concurrent read-safety + gating-correctness test (immutable-after-build) | 79db281 |
| HXC-1679 | Feature | P1 | pkg/dataplane partial-lock audit + concurrent race test (Distributor.pending fully guarded) | facc96f |
| HXC-1680 | Feature | P1 | pkg/inferenceproxy partial-lock audit + concurrent race test (MemAudit fully guarded) | c526da2 |
| HXC-1681 | Feature | P1 | pkg/timefault concurrency-contract + fault-model test (deterministic single-sequence harness) | 78cd830 |
| HXC-1682 | Feature | P1 | pkg/auctionplace adversarial clearing test (first-caller-wins, order-independent, single winner) | 34c0b7b |
| HXC-1683 | Feature | P1 | pkg/constraints Forbidden hard-ban precedence test (closes latent Forbidden>Required gap) | 12fa963 |
| HXC-1684 | Feature | P1 | pkg/preempt 20k-case oracle preemption test (priority-safety, minimality, no false reject) | 1b26424 |
| HXC-1685 | Feature | P1 | pkg/carbonsched oracle selection test (lowest-carbon argmin, order-independent) | 782b3a0 |
| HXC-1686 | Feature | P1 | pkg/costsched oracle selection test (lowest-cost, lex tie-break; closes unstable-sort gap) | d6303ed |
| HXC-1687 | Feature | P1 | pkg/suitability exhaustive placement test (HPC never remote/non-low-latency hard rule) | 39895b8 |
| HXC-1688 | Feature | P1 | pkg/revenueopt adversarial pricing test (TEE->Chutes bonus, oracle winner+revenue) | 2f7d65e |
| HXC-1689 | Feature | P1 | pkg/forecast numeric OLS-correctness test (formula vs oracle, exact window, direction) | e8ca5d3 |
| HXC-1690 | Bug | P1 | FIX pkg/qos intransitive near-tie comparator (order-dependent QoS selection) | 1d5f748 |
| HXC-1691 | Feature | P1 | pkg/latencysched comparator transitivity audit + order-independence test (transitive, no band) | b34d938 |
| HXC-1692 | Feature | P1 | pkg/gpupool comparator transitivity audit + order-independence test (transitive, no band) | 9ab1d54 |
| HXC-1693 | Feature | P1 | pkg/smartrouter dual audit (comparator transitivity + partial-lock) clean on both | 40ad3c5 |
| HXC-1694 | Feature | P1 | pkg/bursthysteresis exhaustive hysteresis-boundary test (no flap, inclusive thresholds) | 35fafe3 |
| HXC-1695 | Feature | P1 | pkg/healthmonitor FSM transition-matrix + concurrency test (edge-triggered, fully guarded) | 7c08ddf |
| HXC-1696 | Feature | P1 | pkg/balancemonitor event-log conservation + concurrency test (no lost append, strict threshold) | b41a6fe |
| HXC-1697 | Feature | P1 | pkg/hybridkex adversarial hybrid-binding test (X25519+ML-KEM-768, both components bind) | c122650 |
| HXC-1698 | Feature | P1 | pkg/x25519session adversarial key-agreement test (auth, wrong-peer, low-order rejection) | 84a9e64 |
| HXC-1699 | Feature | P1 | pkg/modelintegrity adversarial tamper/forgery test (SHA-256 content-pinning, path-swap) | 2ba8f10 |
| HXC-1700 | Feature | P1 | pkg/gpuattest adversarial attestation-forgery test (ed25519, pinned-key trust) | d2c5d33 |
| HXC-1701 | Feature | P1 | pkg/spiffefed adversarial trust-domain-confusion test (exact host match, no lookalike bypass) | b39ad90 |
| HXC-1702 | Task | P1 | attestadmit fail-closed adversarial tests | 2a0616f |
| HXC-1703 | Task | P1 | gravalverify anti-cheat adversarial tests | 2a0616f |
| HXC-1704 | Task | P1 | exportcontrol embargo/lookalike adversarial tests | 2a0616f |
| HXC-1705 | Task | P1 | imagepolicy admission adversarial tests | 470d7fc |
| HXC-1706 | Task | P1 | headersanitize spoof-proofing adversarial tests | 470d7fc |
| HXC-1707 | Task | P1 | edgeverify content-hash adversarial tests | 470d7fc |
| HXC-1708 | Task | P1 | anonymize keyed-hash tokenizer adversarial tests | 470d7fc |
| HXC-1709 | Task | P1 | priorityqueue total-order adversarial tests | 5b8e8b5 |
| HXC-1710 | Task | P1 | auditproof canonical-serialization adversarial tests | 5b8e8b5 |
| HXC-1711 | Task | P1 | backoff cap/overflow adversarial tests | 5b8e8b5 |
| HXC-1712 | Task | P1 | nodeselector constraint-satisfaction adversarial tests | 7bd9267 |
| HXC-1713 | Task | P1 | economics conservation/ROI adversarial tests | 7bd9267 |
| HXC-1714 | Task | P1 | hybridtco monotonicity/accounting adversarial tests | 7bd9267 |
| HXC-1715 | Task | P1 | costrouter cost-optimal selection adversarial tests | c6967ef |
| HXC-1716 | Task | P1 | fallbackchain orchestration adversarial tests | c6967ef |
| HXC-1717 | Task | P1 | openapivalidate fail-closed validation adversarial tests | c6967ef |
| HXC-1718 | Task | P1 | correlation audit-trail adversarial tests | 0a504e5 |
| HXC-1719 | Task | P1 | modelretry classification adversarial tests | 0a504e5 |
| HXC-1720 | Task | P1 | workloadrouter routing+concurrency adversarial tests | 0a504e5 |
| HXC-1721 | Task | P1 | streamfailover orchestration adversarial tests | 4244b6d |
| HXC-1722 | Task | P1 | burstcapacity spillover-estimator adversarial tests | 4244b6d |
| HXC-1723 | Task | P1 | powergater night-window/state adversarial tests | 4244b6d |
| HXC-1724 | Task | P1 | residency data-residency compliance adversarial tests | bad44dc |
| HXC-1725 | Task | P1 | watchtower tamper-integrity adversarial tests | bad44dc |
| HXC-1726 | Task | P1 | fsresidency file content-integrity adversarial tests | bad44dc |
| HXC-1727 | Task | P1 | devicecatalog lookup adversarial tests | 1810b72 |
| HXC-1728 | Task | P1 | gputopo topology-selection adversarial tests | 1810b72 |
| HXC-1729 | Task | P1 | configsync CRDT store concurrency adversarial tests | 1810b72 |
| HXC-1730 | Task | P1 | config defaults/validation adversarial tests | 0043627 |
| HXC-1731 | Task | P1 | deviceprofile YAML-load adversarial tests | 0043627 |
| HXC-1732 | Task | P1 | gpucatalog scoring/multiplier adversarial tests | 0043627 |
| HXC-1733 | Task | P1 | healthprobe grace-window adversarial tests | c09c24d |
| HXC-1734 | Task | P1 | devicemap round-trip mapping adversarial tests | c09c24d |
| HXC-1735 | Task | P1 | burst autoscaling-hysteresis adversarial tests | c09c24d |
| HXC-1736 | Task | P1 | archlint doc-vs-disk linter adversarial tests | 3b8f1d1 |
| HXC-1737 | Task | P1 | etcdlint import-ban linter adversarial tests | 3b8f1d1 |
| HXC-1738 | Task | P1 | tierdetect per-OS detection adversarial tests | 3b8f1d1 |
| HXC-1739 | Task | P1 | llmadapter translation-fidelity adversarial tests | 75ac469 |
| HXC-1740 | Task | P1 | passthrough streaming-fidelity adversarial tests | 75ac469 |
| HXC-1741 | Task | P1 | grpcutil auth-interceptor adversarial tests | 75ac469 |
| HXC-1742 | Task | P1 | middleware panic-recovery/chain adversarial tests | e534ad3 |
| HXC-1743 | Task | P1 | llmfailover failover-path/hint adversarial tests | e534ad3 |
| HXC-1744 | Task | P1 | capability manifest/negotiate adversarial tests | e534ad3 |
| HXC-1745 | Task | P1 | sandbox capability-isolation adversarial tests | 5cb37ea |
| HXC-1746 | Task | P1 | splitbrainalert detection adversarial tests | 5cb37ea |
| HXC-1747 | Task | P1 | serde capnp serialization adversarial tests | 5cb37ea |
| HXC-1748 | Task | P1 | netutil SSRF-defense adversarial tests | f0dc4a2 |
| HXC-1749 | Task | P1 | healthmonitor FSM/concurrency adversarial tests | f0dc4a2 |
| HXC-1750 | Task | P1 | leader fencing split-brain-safety adversarial tests | f0dc4a2 |
| HXC-1751 | Task | P1 | workclaim claim-exclusivity adversarial tests | 081a9ad |
| HXC-1752 | Task | P1 | tierdef config-load adversarial tests | 081a9ad |
| HXC-1753 | Bug | P0 | hlc Update/Now logical-counter overflow breaks HLC domination (causality) — FIXED | 081a9ad |
| HXC-1754 | Task | P1 | hashslot CRC16/hashtag adversarial tests | 78c3cdb |
| HXC-1755 | Task | P1 | mvcc snapshot-isolation adversarial tests | 78c3cdb |
| HXC-1756 | Task | P1 | stats Welch-t-test adversarial tests | 78c3cdb |
| HXC-1757 | Task | P1 | ringavg moving-average adversarial tests | 78c3cdb |
| HXC-1758 | Task | P1 | crdt semilattice-law adversarial tests | c0cd240 |
| HXC-1759 | Bug | P1 | antientropy equal-version conflict never converges (permanent split-brain) — FIXED | c0cd240 |
| HXC-1760 | Task | P1 | splitbrain quorum-classification adversarial tests | c0cd240 |
| HXC-1761 | Task | P1 | porcupine linearizability-checker adversarial tests | c0cd240 |
| HXC-1762 | Bug | P1 | offlinesync: decompression-bomb DoS + equal-Seq split-brain — BOTH FIXED | 96bb4c5 |
| HXC-1763 | Task | P1 | heartbeatcoalescer coalescing/concurrency adversarial tests | 96bb4c5 |
| HXC-1764 | Task | P1 | multiraft durability-safety adversarial tests | 96bb4c5 |
| HXC-1765 | Bug | P2 | computeproto: FlatBuffers decode DoS (panic on hostile bytes) — hardened with ReadComputeTaskSafe | bdf7837 |
| HXC-1766 | Task | P1 | nattraversal STUN codec adversarial tests | bdf7837 |
| HXC-1767 | Task | P1 | websocket envelope codec adversarial tests | bdf7837 |
| HXC-1768 | Task | P1 | fiber framing/admission adversarial tests | bdf7837 |
| HXC-1769 | Task | P1 | ice RFC8445 priority adversarial tests | fe35955 |
| HXC-1770 | Task | P1 | wasm sandbox-enforcement adversarial tests | fe35955 |
| HXC-1771 | Bug | P1 | helixnet prod fabric teardown race: send on closed channel panic — FIXED | fe35955 |
| HXC-1772 | Task | P1 | edgefusion windowed-fusion adversarial tests | fe35955 |
| HXC-1773 | Task | P1 | watchmanager watch-fanout concurrency adversarial tests | 6260409 |
| HXC-1774 | Task | P1 | informer cache-coherence adversarial tests | 6260409 |
| HXC-1775 | Task | P1 | cellmesh overlap-detection adversarial tests | 6260409 |
| HXC-1776 | Task | P1 | edgeheartbeat liveness/concurrency adversarial tests | 6260409 |
| HXC-1777 | Task | P1 | checkpoint_merge merge/migrate adversarial tests | 73e955e |
| HXC-1778 | Task | P1 | kraft metadata-log determinism adversarial tests | 73e955e |
| HXC-1779 | Task | P1 | gepetto reservation/high-water adversarial tests | 73e955e |
| HXC-1780 | Task | P1 | edgeregistry concurrency/copy-isolation adversarial tests | 73e955e |
| HXC-1781 | Task | P1 | marketplace/scan/chutes adversarial tests (over-commit, routing, attestation) | 51bf06d |
| HXC-1782 | Bug | P1 | classads parser/evaluator stack-overflow DoS on deeply-nested expression — FIXED | 33aaaa0 |
| HXC-1783 | Task | P1 | raftprofile parse/timing-invariant adversarial tests | 33aaaa0 |
| HXC-1784 | Task | P1 | phasegate gating/concurrency adversarial tests | 33aaaa0 |
| HXC-1785 | Bug | P1 | costtracker unsynchronized accumulator: data race + cost non-conservation — FIXED | ccab647 |
| HXC-1786 | Task | P1 | providerchain fallback adversarial tests | ccab647 |
| HXC-1787 | Task | P1 | modelrouter selection adversarial tests | ccab647 |
| HXC-1788 | Bug | P1 | dataplane Distributor.SendToWorker concurrent-send libzmq SIGABRT — FIXED | 96f86cd |
| HXC-1789 | Task | P1 | marketplaceadapter translation-fidelity adversarial tests | 96f86cd |
| HXC-1790 | Task | P1 | raftleader election-safety adversarial tests | 96f86cd |
| HXC-902 | Task | P1 |  |  |
| HXC-903 | Task | P1 |  |  |
| HXC-904 | Feature | P0 | Phase 4 Build Service | e5f963b |
| HXC-905 | Feature | P0 | SQLite HXC Registry | f4d8da9 |
| HXC-908 | Bug | P1 | Harden pkg/stonith IPMI credential handling (no -P on argv, redact in errors) | 5b4a875 |
| HXC-909 | Task | P2 | pkg/multiraft: make RaftTransport async-delivery safe (Step under shard lock) | bd90b85 |
| HXC-910 | Task | P2 | pkg/deviceplugin: add concurrency test exercising Registry mutex under -race | 5b4a875 |
| HXC-911 | Bug | P2 | pkg/chutes StreamChannel: honor cancellation on blocked reads + close reader | bd90b85 |
| HXC-912 | Bug | P2 | pkg/kraft: CreateTopic should return ErrTopicExists on conflicting re-create | bd90b85 |
| HXC-913 | Task | P3 | pkg/porcupine: implement state-dedup memoization via Model.Equal for large histories | bd90b85 |
| HXC-915 | Task | P2 | pkg/dst: wire 1000-sims/commit deterministic-simulation CI gate (GH Actions + cmd harness) | f4d8da9 |
| HXC-916 | Task | P2 | pkg/provider/chutes: ctx-interruptible backoff + honor Retry-After header | 7783030 |
| HXC-917 | Bug | P2 | pkg/multiraft: handle ShardStorage SetHardState/Append errors before Advance | 2a90f56 |
| HXC-918 | Bug | P2 | pkg/marketplaceadapter Akash: default MinReputation via constructor + escape SDL fields | 4c2d4b0 |
| HXC-919 | Bug | P2 | pkg/provider/runpod: surface Worker.Endpoint + don't hold lock across cold-provision network call | 4c2d4b0 |
| HXC-920 | Task | P3 | pkg/provider/chutes: test parseRetryAfter HTTP-date branch (RFC1123/RFC850/asctime + past-date clamp) | 4c2d4b0 |
| HXC-927 | Task | P2 | Surface multiraft durability persist-error through public API | 31f4139 |
| HXC-934 | Bug | P0 | Make cmd/e2ee-proxy consume security/pkg/e2ee instead of inline crypto (de-bluff E2EE) | bc21b0d |
| HXC-937 | Task | P1 | Add e2e + chaos test types for consensus/federation/marketplace | d688cc6 |
| HXC-938 | Task | P2 | Add fuzz to crypto and benchmarks to hot paths | 11d48a5 |
| HXC-939 | Docs | P2 | Refresh stale per-phase GAP_AUDIT.md files (CLAUDE-3) | 6bf944d |
| HXC-940 | Task | P2 | Wire the orphaned gate packages (covgate/archlint/etcdlint/qualitygate/phasegate) | 6042daf |
| HXC-941 | Task | P2 | Reconcile E2EE AEAD spec deviation (AES-256-GCM vs roadmap ChaCha20-Poly1305) | 3d6e249 |
| HXC-942 | Bug | P1 | Add e2e/chaos -race integration tests for the 8 fixed concurrency hazards | f92b1ff |
| HXC-943 | Bug | P1 | Test HelixQA validators (done) and vision/ORB (blocked) + fix helixqa module graph | a39e89b |
| HXC-944 | Bug | P0 | Fix 3 CRITICAL CVEs from trivy security scan | cc34fa3 |
| HXC-945 | Bug | P1 | Triage and resolve gosec HIGH findings (99 HIGH) from security scan | 4aa1d5d |
| HXC-946 | Task | P2 | Triage trivy misconfig HIGH (50) + suppress QA-sentinel secret false-positives | e352c51 |
| HXC-947 | Task | P3 | Add root go.mod require/replace for digital.vasic.security (out-of-workspace builds) | a39e89b |
| HXC-948 | Bug | P2 | Fix api/v1 module path /v1-suffix blocking out-of-workspace require/replace | 42d38fb |
| HXC-950 | Bug | P1 | sidecarutil HealthProbe load-robustness (WaitDelay + transient retry) | b081dad |
| HXC-951 | Research | P2 | TLA+ spec for SWIM membership/failure-detection (TLC-verified safety) | 3fedcbe |
| HXC-952 | Feature | P2 | Unified hardware-inventory engine (CPU+memory+GPU+NPU aggregation) | 7011255 |
| HXC-953 | Feature | P2 | Real Raft-backed leader-election service (pkg/raftleader adapting pkg/raft) | 7011255 |
| HXC-954 | Feature | P1 | Edge gateway multi-transport integration (QUIC + MQTT + WebSocket end-to-end) | 1515077 |
| HXC-955 | Feature | P2 | Distributed KV demo over Raft (cmd/raftkv-demo) — building blocks composed | c52eee8 |
| HXC-956 | Feature | P2 | OpenTelemetry single-runtime tracing foundation (observability/tracing) | a2a4c01 |
| HXC-957 | Research | P2 | TLA+ spec for WireGuard/Noise-IK handshake (TLC-verified safety) | 462baaf |
| HXC-958 | Research | P2 | TLA+ spec for OR-Set CRDT convergence (TLC-verified, add-wins) | be574fe |
| HXC-959 | Research | P2 | TLA+ spec for Two-Phase Commit atomicity (TLC-verified) | dac5022 |
| HXC-960 | Research | P2 | TLA+ spec for Saga compensation atomicity (TLC-verified) | de127fc |
| HXC-961 | Research | P2 | TLA+ spec for vector-clock causal delivery (TLC-verified) | 7d54dd1 |
| HXC-962 | Research | P2 | TLA+ spec for Raft single-server membership change safety (TLC-verified) | d8d39e9 |
| HXC-963 | Research | P2 | TLA+ spec for leader-lease leaseholder reads / no-stale-read (TLC-verified) | e7dc5a4 |
| HXC-964 | Research | P2 | TLA+ spec for phi-accrual failure-detector accuracy (TLC-verified) | 70824c5 |
| HXC-965 | Feature | P1 | End-to-end cluster node-agent: compose discovery+raft+capabilities+gateway (clusternode) | 03b7162 |
| HXC-966 | Research | P2 | TLA+ spec for Chandy-Lamport distributed snapshot (TLC-verified) | ea0c6a7 |
| HXC-967 | Research | P2 | TLA+ spec for Lamport distributed mutual exclusion (TLC-verified) | 6d34c9b |
| HXC-968 | Research | P2 | TLA+ spec for Dynamo-style quorum consistency R+W>N (TLC-verified) | c9fc6dd |
| HXC-969 | Feature | P1 | clusternode real-WebSocket transport: cross-node messaging over real TCP | c9fc6dd |
| HXC-970 | Research | P2 | TLA+ spec for Hybrid Logical Clock (TLC-verified) | d1bba72 |
| HXC-971 | Research | P2 | TLA+ spec for Raft ReadIndex linearizable reads (TLC-verified) | 5eb06fe |
| HXC-972 | Research | P2 | TLA+ spec for STONITH fencing safety / no dual-active (TLC-verified) | 19d676c |
| HXC-973 | Research | P2 | TLA+ spec for gossip/epidemic dissemination convergence (TLC-verified) | 7c05953 |
| HXC-974 | Feature | P1 | Real TCP-networked Raft for pkg/raft (network transport + leak-safe cluster) | 33de5cc |
| HXC-975 | Research | P2 | TLA+ spec for PN-Counter CRDT convergence (TLC-verified) | 46805f7 |
| HXC-976 | Feature | P1 | Real BoltDB persistence for pkg/raft (durable on-disk store + recovery) | 426b213 |
| HXC-977 | Research | P2 | TLA+ spec for rendezvous (HRW) consistent hashing minimal disruption (TLC-verified) | f061694 |
| HXC-978 | Feature | P1 | pkg/raft: combined persistent+networked node (real TCP transport + on-disk BoltDB) with restart-recover-rejoin | 75fc874 |
| HXC-979 | Research | P1 | TLA+ snapshot-install / log-compaction safety spec (RaftSnapshot), TLC-verified exhaustive | 58aabc3 |
| HXC-980 | Bug | P1 | registry CLI: update auto-moves current_location on status change + reconcile backfill (doc-sync truthfulness) | f1b66c1 |
| HXC-981 | Feature | P1 | pkg/wasm: combined-bounds sandbox E2E (fuel + memory cap + capability deny enforced simultaneously) | 4457f9c |
| HXC-982 | Feature | P0 | helix-raftd: real multi-process persistent Raft daemon (TCP + BoltDB) with kill+restart-from-disk E2E | 99f9a6c |
| HXC-983 | Docs | P1 | docs/consensus.md: consolidated Raft consensus architecture doc + README link + docs_chain tracking | c81f940 |
| HXC-984 | Feature | P1 | pkg/raft: snapshot/log-compaction InstallSnapshot integration test (runtime proof of HXC-979) | 92fb006 |
| HXC-985 | Feature | P1 | clusternode durable consensus: NodeAgent on persistent+networked raft with full-restart-from-disk E2E | f7d19eb |
| HXC-986 | Research | P1 | TLA+ Raft PreVote spec — stale/restarted node cannot disrupt a stable leader (TLC-verified) | 46f72fd |
| HXC-987 | Feature | P1 | helix-raftd Prometheus /metrics endpoint (live raft state, real scrape proven) | a56363d |
| HXC-988 | Feature | P1 | e2ee adversarial tamper/wrong-key crypto tests (AEAD tag, ML-KEM-768 implicit rejection) | 8f84114 |
| HXC-989 | Docs | P2 | helix-raftd observability materials: Prometheus scrape config + Grafana dashboard + docs/observability.md | 87a157d |
| HXC-990 | Feature | P1 | pkg/raft PreVote runtime disruption test (runtime pairing of HXC-986) | 03a9cea |
| HXC-991 | Feature | P2 | helix-raftctl CLI client for helix-raftd admin API (live-cluster proven) | 8c8922a |
| HXC-992 | Feature | P1 | pkg/scheduler Omega optimistic-concurrency barrier-race test (no double-booking) | 8a3f499 |
| HXC-993 | Task | P2 | gofmt hygiene: format 125 unformatted committed Go files across pkg/ + cmd/ | a709acb |
| HXC-994 | Bug | P0 | FIX real SWIM false-positive failure detection (healthy members wrongly marked DEAD) | 8d24960 |
| HXC-995 | Feature | P1 | pkg/antientropy end-to-end Merkle reconciliation test (only-differences-transferred) | feab638 |
| HXC-996 | Feature | P1 | pkg/mvcc snapshot-isolation + race-safety test (consistent point-in-time reads) | 934cdec |
| HXC-997 | Feature | P1 | pkg/deltacrdt adversarial convergence tests + characterizes a real ORSet SEC defect | f2eee0d |
| HXC-999 | Feature | P1 | pkg/splitbrain partition-safety test (at-most-one-authoritative across all cuts) | 93e61a2 |
