# Fixed

**Revision:** 58
**Last modified:** 2026-06-13T10:25:10Z
**Description:** Completed workable items (with evidence references)
**Authority:** Constitution §11.4.93 (workable-items DB single source of truth)
**Generated-by:** scripts/docs/db_to_md.py (DB is canonical; edit via cmd/hxc-registry, not by hand)

Total completed: **295**.

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
| HXC-1185 | Feature | P0 | Implement edge heartbeat with battery/thermal/network telemetry | 2a90f56 |
| HXC-1186 | Feature | P0 | Implement edge protocol gateway (MQTT/QUIC/WebSocket) | a4c9bf7 |
| HXC-1187 | Feature | P0 | Define EdgeWorkUnit and EdgeWorkResult protobuf schemas | af16635 |
| HXC-1188 | Feature | P0 | Enforce work-unit resource limits (duration/memory/CPU) on edge devices | 7718c20 |
| HXC-1189 | Feature | P0 | Implement EdgeAwarePlugin scheduler Filter stage | 203c42a |
| HXC-1190 | Feature | P1 | Implement EdgeAwarePlugin scheduler Score stage | 203c42a |
| HXC-1191 | Feature | P1 | Implement declarative per-tier ScheduleRule engine | 7718c20 |
| HXC-1195 | Feature | P0 | Implement edge trust-level model (STANDARD/SEMI/EDGE_DONOR) | 7718c20 |
| HXC-1196 | Feature | P0 | Enforce workload restriction matrix by trust level | 7718c20 |
| HXC-1198 | Feature | P1 | Implement offline sync protocol with delta compression | 203c42a |
| HXC-1199 | Feature | P1 | Implement edge sensor-fusion framework and stream workload type | 75f076f |
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
| HXC-1236 | Feature | P0 | Implement INetwork/HelixNetwork trait with production and simulation swappable impls | 7c41c2f |
| HXC-1237 | Feature | P0 | Wire DST sim transport seam into pkg/swim membership (prod+sim parity) | 656bec1 |
| HXC-1238 | Feature | P1 | Implement BUGGIFY macro framework with ~25% deterministic fire rate | 75f076f |
| HXC-1239 | Feature | P1 | Implement 10:1 DST virtual-time compression by fast-forwarding idle periods | 75f076f |
| HXC-1240 | Feature | P1 | Achieve 1,000+ simulated nodes in a single DST process | 75f076f |
| HXC-1241 | Feature | P1 | Author DST consensus+gossip workloads using SETUP→EXECUTION→CHECK→METRICS pattern | 75f076f |
| HXC-1243 | Feature | P0 | Implement 8 network fault injectors (latency, loss, corruption, reorder, dup, bandwidth, partition, DNS, TCP reset) | 15977c0 |
| HXC-1244 | Feature | P0 | Implement 8 node fault injectors (VM crash/restart/pause, CPU/mem/disk pressure, OOM kill, graceful shutdown) | e93fad6 |
| HXC-1247 | Feature | P1 | Implement pure-Go in-sim chaos faults toward 25+ (ClockSkew, DiskFill, MessageReorder, Byzantine, etc.) | 1c0d943 |
| HXC-1248 | Feature | P0 | Build YAML chaos Scenario Engine with phases, blast radius and abort-on-SLO-breach | e93fad6 |
| HXC-1249 | Feature | P0 | Implement emergency-stop and auto-recovery with <=2s halt latency | 15977c0 |
| HXC-1254 | Feature | P1 | Implement TestRunner with parallel suite execution and result collection | aba565b |
| HXC-1255 | Feature | P1 | Implement session test state machine (IDLE→SETUP→RUNNING→CHAOS_INJECT→VERIFY→RECOVERY→REPORT) | aba565b |
| HXC-1258 | Feature | P1 | Implement MetricsCollector exporting 15+ chaos Prometheus metric series with OpenTelemetry tracing | c642583 |
| HXC-1260 | Feature | P1 | Implement metrics validation against baseline KPI table with severity gating | 553a043 |
| HXC-1261 | Feature | P1 | Implement Welch's t-test statistical regression detector for HelixQA | 1c0d943 |
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
| HXC-1308 | Feature | P2 | Implement micro-benchmarker producing normalized per-compute-class scores | f4d8da9 |
| HXC-1310 | Feature | P0 | Implement per-tier security model enforcement (sandbox + data/network policy by trust level) | 441c7fe |
| HXC-1311 | Docs | P1 | Author complete YAML tier definitions (T1-T15) with min_requirements and constraints | d4829d1 |
| HXC-1328 | Feature | P1 | Implement cloud-spot AWS/Azure/GCP IMDS interruption pollers (adapters) | f4d8da9 |
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
| HXC-1347 | Feature | P1 | Implement mDNS/DNS-SD local cell discovery advertiser and browser | e5f963b |
| HXC-1351 | Feature | P1 | Implement QUIC fallback transport with 0-RTT and connection migration | d9718e8 |
| HXC-1353 | Feature | P1 | Implement WireGuard zero-downtime key rotation with overlap window | 0c8b4ca |
| HXC-1356 | Feature | P2 | Integrate libp2p DHT discovery, GossipSub and circuit relay | 8b0bb7a |
| HXC-1364 | Feature | P1 | Implement internal/federation Karmada PropagationPolicy engine | e5f963b |
| HXC-1365 | Feature | P1 | Extend pkg/scheduler with latency-aware spot/preemptible scoring | 0c8b4ca |
| HXC-1367 | Feature | P1 | Implement pkg/gitops ArgoCD ApplicationSet federation client | e5f963b |
| HXC-1373 | Feature | P1 | Implement split-brain prevention and partition-classification logic | 37c2a9a |
| HXC-1484 | Feature | P1 | Extend pkg/health with miner-api and GraVal DaemonSet health checks | e5f963b |
| HXC-1514 | Feature | P1 | Implement pkg/provider/ionet IONetProvider Ray cluster adapter | e5f963b |
| HXC-1527 | Feature | P1 | Define gpu_proxy.proto gRPC GPUProvider kernel-dispatch service | af16635 |
| HXC-1533 | Task | P1 | Add ProviderAdapter registration hooks to pkg/gpu | f4d8da9 |
| HXC-1561 | Feature | P0 | Add ChaCha20-Poly1305 AEAD cipher variant to pkg/security/e2ee | 69c1a89 (security fbdab51) |
| HXC-1563 | Feature | P1 | Implement bidirectional per-request response keypair in pkg/security/e2ee | 831089a (security f09227f) |
| HXC-1564 | Feature | P2 | Implement gzip-before-encrypt payload compression in pkg/security/e2ee | f4d8da9 |
| HXC-1565 | Feature | P1 | Implement streaming SSE E2EE decryption (e2e_init + per-chunk) in pkg/security/e2ee | 02c42d4 (security 16ae574) |
| HXC-1567 | Feature | P1 | Maintain E2EE length-prefixed framed transport over io.ReadWriter | 31ff29f (security 29a6193) |
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
| HXC-902 | Task | P1 |  |  |
| HXC-903 | Task | P1 |  |  |
| HXC-904 | Feature | P0 | Phase 4 Build Service | e5f963b |
| HXC-905 | Feature | P0 | SQLite HXC Registry | f4d8da9 |
| HXC-915 | Task | P2 | pkg/dst: wire 1000-sims/commit deterministic-simulation CI gate (GH Actions + cmd harness) | f4d8da9 |
| HXC-917 | Bug | P2 | pkg/multiraft: handle ShardStorage SetHardState/Append errors before Advance | 2a90f56 |
| HXC-918 | Bug | P2 | pkg/marketplaceadapter Akash: default MinReputation via constructor + escape SDL fields | 4c2d4b0 |
| HXC-919 | Bug | P2 | pkg/provider/runpod: surface Worker.Endpoint + don't hold lock across cold-provision network call | 4c2d4b0 |
| HXC-920 | Task | P3 | pkg/provider/chutes: test parseRetryAfter HTTP-date branch (RFC1123/RFC850/asctime + past-date clamp) | 4c2d4b0 |
| HXC-927 | Task | P2 | Surface multiraft durability persist-error through public API | 31f4139 |
| HXC-943 | Bug | P1 | Test HelixQA validators (done) and vision/ORB (blocked) + fix helixqa module graph | a39e89b |
| HXC-947 | Task | P3 | Add root go.mod require/replace for digital.vasic.security (out-of-workspace builds) | a39e89b |
| HXC-948 | Bug | P2 | Fix api/v1 module path /v1-suffix blocking out-of-workspace require/replace | 42d38fb |
| HXC-950 | Bug | P1 | sidecarutil HealthProbe load-robustness (WaitDelay + transient retry) | b081dad |
| HXC-951 | Research | P2 | TLA+ spec for SWIM membership/failure-detection (TLC-verified safety) | 3fedcbe |
| HXC-952 | Feature | P2 | Unified hardware-inventory engine (CPU+memory+GPU+NPU aggregation) | 7011255 |
| HXC-953 | Feature | P2 | Real Raft-backed leader-election service (pkg/raftleader adapting pkg/raft) | 7011255 |
| HXC-954 | Feature | P1 | Edge gateway multi-transport integration (QUIC + MQTT + WebSocket end-to-end) | 1515077 |
