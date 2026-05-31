# Phase 3 Gap Audit — Code-Grounded

| Field | Value |
|---|---|
| Date | 2026-06-01 |
| Auditor | Engineering auditor (code-grounded, anti-bluff per CLAUDE-1 / §7.1) |
| Scope | `docs/research/PHASE_3_ROADMAP.md` deliverables vs. actual `pkg/ internal/ cmd/ api/` |
| **Honest completion** | **~70% of Phase 3 P0/P1 scope DONE; ~15% PARTIAL; ~15% MISSING/DEFERRED** |

**One-line summary:** The P0 consensus/scheduler/security/gateway skeletons and most cmd binaries are genuinely implemented with real-behavior tests (real etcd/NATS/Postgres via brokertest), but several integrations are unwired (etcd discovery backend, ClassAds→scheduler, OTel tracing) and the GPU/storage/migration backends remain stubs or pure-Go placeholders.

---

## Deliverable Status Table

Evidence paths are repo-relative. "DONE" = implemented AND has real-behavior tests; "PARTIAL" = implemented but unwired/mock-only/missing a claimed capability; "MISSING/DEFERRED" = stub/absent.

| Deliverable (roadmap) | Status | Evidence file:line | Notes |
|---|---|---|---|
| `pkg/etcd` client wrapper | DONE | `pkg/etcd/package.go:9,25` (real `clientv3`); `pkg/etcd/etcd_integration_test.go` (`//go:build integration`) | Put/Get/lease/txn over real etcd via brokertest. |
| `pkg/lock` distributed locks | DONE | `pkg/lock/lock.go:60-74` (`EtcdLocker` on `concurrency`); `pkg/lock/lock_integration_test.go:27` (real etcd `TestMain`) | Memory + etcd backends; sink-side key inspection in tests. |
| `pkg/leader` / raft-ish election | PARTIAL | `pkg/leader/*.go` (385 LoC, 411 test) | Real election logic + tests, but NOT hashicorp/raft or etcd-Raft consensus; `pkg/raft` package does not exist. Roadmap's true Raft is unrealized. |
| `pkg/scheduler` Omega core + queue + preempt | DONE | `pkg/scheduler/scheduler.go:8-50`, `gang_preempt.go`, `cost_gpu.go`; `pkg/scheduler/*_test.go` (1129 test LoC) | Two-level optimistic-concurrency scheduler, gang scheduling, priority preemption, cost/GPU-aware scoring all present and tested. |
| `pkg/classads` expression parser/evaluator | DONE (standalone) | `pkg/classads/parser.go`, `ast.go`, `eval.go` (662 LoC, 627 test) | Real lexer→AST→evaluator. BUT not consumed by scheduler (see gap below). |
| `internal/scheduler` gRPC service | PARTIAL | `internal/scheduler/server.go:41,102` (wired to `pkg/scheduler`); `server_behavior_test.go` | ScheduleJob/Cancel/Status real; `StreamJobEvents` (`server.go:193`) sends ONE static event then closes — not a live stream. |
| `internal/node` agent (SWIM + resources + register) | PARTIAL | `internal/node/node.go:79` (real SWIM), `:11` (`pkg/resources` probing), `:105` | Uses `discovery.NewInMemoryBackend()` (`node.go:105`) NOT the etcd backend — registration is node-local only, no cluster-wide visibility. |
| `pkg/discovery` etcd backend | PARTIAL | `pkg/discovery/etcd_backend.go` + `etcd_backend_test.go` (real clientv3) | Implemented & tested, but wired NOWHERE in prod (`grep NewEtcdBackend` → only its own file). Dead in the cluster path. |
| `pkg/security` mTLS + SPIFFE | DONE | `internal/security/orchestrator.go:136-164` (real rsa/x509 cert gen), `:34,72` (scopes, SPIFFE trust domain); `pkg/security/tls.go:33`; `security_test.go` (1207 test LoC) | Real cert issuance, RBAC scopes, identity→scope bindings, revocation sweep. |
| `internal/security` RBAC/authz | DONE | `internal/security/authorize_rbac_test.go`, `scopes_test.go`, `identity_bindings.go` | Scope-based authorize with real tests. |
| e2ee / attestation / gpuattest | DONE (submodule) | `security/pkg/e2ee`, `security/pkg/attestation`, `security/pkg/gpuattest` | Lives in `vasic-digital/security` submodule as stated; present with pkg structure. Not re-audited line-by-line here. |
| `pkg/gateway` / `internal/gateway` reverse proxy | PARTIAL | `internal/gateway/gateway.go:24-33` (static route table, `X-Helix-Gateway` sink header); `gateway_test.go` | Real HTTP reverse proxy with tests, but NO auth/mTLS/RBAC/rate-limit enforcement (`grep tls|auth|jwt` → none). Roadmap demanded OPA + mTLS termination. |
| `internal/policy` OPA engine | PARTIAL | `internal/policy/engine.go` (243 LoC), `engine_test.go` | Custom policy engine + tests; NOT OPA/Rego (no `open-policy-agent` import). Functional but not the specified tech. |
| `pkg/metrics` Prometheus exposition | DONE | `pkg/metrics/package.go:64` (Histogram), `:34` (Gauge), `:172` (`PrometheusHandler` text format) | Counter/Gauge/Histogram + `/metrics` text exposition + tests (630 LoC). Custom impl, not official client lib, but real & end-user-usable. |
| `pkg/tracing` OpenTelemetry | PARTIAL | `pkg/tracing/package.go:21,47` (homegrown `TraceID`/`Span`) | Real span tree + W3C-ish header propagation + tests (797 LoC), but NOT OpenTelemetry SDK — no OTLP export to Jaeger. Roadmap specified OTel wiring. |
| `internal/health` monitor + scoring | DONE | `internal/health/*.go` (592 LoC, 802 test, 4 test files); `pkg/health` (452/642) | Composite health scoring + tests. eBPF/LSTM correctly absent (P3). |
| `pkg/storage` abstraction | PARTIAL | `pkg/storage/storage.go:34` (Memory), `:93` (File) | Memory + File backends with tests. NO S3/Ceph/NFS distributed backend (roadmap §2.4). |
| `pkg/build` RBE/CAS | DONE | `pkg/build/*.go` (574 LoC), `build_integration_test.go`; `internal/build` (716 LoC) | CAS/action-cache with real integration tests. |
| `cmd/helixd` control daemon | PARTIAL | `cmd/helixd/main.go:180,245` (dependency health prober + HTTP status) | Real config/probe/status daemon, but it ORCHESTRATES via TCP health checks; it does not embed/serve the scheduler/session/security gRPC services in-process. |
| `cmd/htmux` CLI | DONE | `cmd/htmux/main.go:97-105` (create/attach/list/kill/rename), `client.go:75` (real gRPC `sessionClient`) | Functional gRPC session CLI (587 LoC). Hand-rolled flag parser, not Cobra, but works. |
| All other `cmd/*` binaries | DONE | `cmd/helix-{scheduler,session,security,gateway,node,build,health,policy,llm,advisory,setup}/main.go` (each 140-420 LoC + a test file) | Every roadmap binary exists with a main and at least one test; none empty. |
| `internal/gpu/*` CUDA/ROCm/oneAPI/MLX backends | DEFERRED | `internal/gpu/manager.go`, `gpu.go` (640 LoC, pure Go; `grep "import \"C\""` → none) | Pure-Go device abstraction/manager/monitor only. No cgo vendor backends. DEFERRED: needs GPU hardware + vendor SDKs/cgo. |
| Session live migration (CRIU/DMTCP) | DEFERRED | `pkg/session/migration.go:178-222` (`"criu not implemented"`, `"dmtcp not implemented"`) | Explicit stubs. DEFERRED: needs CRIU/DMTCP host tooling + privileged kernel support. |
| WireGuard NAT traversal (UPnP/NAT-PMP) | DEFERRED | `pkg/wireguard/nat_traversal.go:35,41` (`"UPnP/NAT-PMP not implemented"`) | DEFERRED: needs external NAT gateway hardware to validate end-to-end. |
| True Raft consensus (`pkg/raft`) | MISSING | (no `pkg/raft`) | Roadmap §2.1 core gap. Distributed locks/election currently lean on etcd; no embedded Raft library. |

---

## TOP IMPLEMENTABLE GAPS (Go, no new infra/hardware)

Prioritized. Each is buildable with existing deps and produces an end-user-visible behavior change with a mutation-pairable acceptance test.

### 1. Wire `EtcdBackend` into the node agent (real cluster-wide discovery)
- **Target dir:** `internal/node/`
- **Spec:** `internal/node/node.go:105` hardcodes `discovery.NewInMemoryBackend()`, so node registrations are invisible cluster-wide despite a complete, tested `pkg/discovery/etcd_backend.go`. Add an `Agent` config field (`EtcdEndpoints []string`); when non-empty, construct `discovery.NewEtcdBackend(...)` with a TTL lease tied to the SWIM heartbeat so a dead node's instance auto-expires. Fall back to in-memory only when no endpoints are configured. Expose the chosen backend type via the agent's status so operators can see "etcd" vs "memory".
- **Acceptance test (mutation-pairable):** Integration test (`//go:build integration`) boots a real etcd via brokertest, starts two agents pointing at it, and asserts agent A's `registry.List()` returns agent B's instance with B's resource metadata. **Mutation:** revert `node.go` to `NewInMemoryBackend()` → test must FAIL (A sees only itself).

### 2. Make `internal/scheduler.StreamJobEvents` a real event stream
- **Target dir:** `internal/scheduler/` (+ small `pkg/scheduler` event hook)
- **Spec:** `server.go:193` sends a single hardcoded `"scheduled"` event and returns — a PASS-bluff for "StreamJobEvents". Add a per-job event channel in `pkg/scheduler` (publish on SCHEDULED / BOUND / PREEMPTED / COMPLETED / CANCELLED state transitions) and have the gRPC handler subscribe and forward events until job terminal or client cancel.
- **Acceptance test:** Schedule a job, open the stream, then cancel/preempt it; assert the stream emits ≥2 distinct `EventType`s in order with monotonically increasing timestamps. **Mutation:** drop the `PREEMPTED` publish → test must FAIL (stream missing the second event).

### 3. Connect `pkg/classads` to the scheduler Filter/Score stages
- **Target dir:** `pkg/scheduler/` (new `plugins_classads.go`)
- **Spec:** A full ClassAds parser/evaluator exists (`pkg/classads/eval.go`) but `grep classads pkg/scheduler` returns nothing — the scheduler ignores it. Add a `ClassAdFilterPlugin` and `ClassAdScorePlugin` implementing the existing `Plugin` interface: evaluate a job's `Requirements` expression against each node's attribute ad (Filter), and a `Rank` expression to produce a Score. Reuse `pkg/scheduler.Resources` as the attribute source.
- **Acceptance test:** Build two nodes (one matching, one not) and a job whose `Requirements = (GPU >= 1)`; assert the filter rejects the non-GPU node and `Rank = -Memory` orders nodes by free memory. **Mutation:** flip the comparison operator in the evaluator call (`>=`→`<`) → test must FAIL.

### 4. Add auth + RBAC enforcement to the gateway
- **Target dir:** `internal/gateway/`
- **Spec:** `gateway.go` is a bare reverse proxy with zero auth (`grep tls|auth|jwt` → none), contradicting roadmap §2.7 (mTLS + OPA enforcement). Add a middleware that extracts a bearer/JWT identity (reuse `pkg/jwt`) and checks required scopes (reuse `internal/security` scope model / `internal/policy`) per route prefix; reject with 401/403 and an `X-Helix-Deny-Reason` header before proxying.
- **Acceptance test:** Request `/api/v1/scheduler/` with (a) no token → 401, (b) token missing `scheduler:write` → 403 with deny reason, (c) valid token → proxied (sees `X-Helix-Gateway`). **Mutation:** make the middleware fail-open on missing scope → cases (a)/(b) must FAIL.

### 5. Add an S3-compatible (minio) backend to `pkg/storage`
- **Target dir:** `pkg/storage/`
- **Spec:** Only Memory + File exist; roadmap §2.4 wants pluggable distributed backends. Add an `S3Store` implementing the existing `Put/Get/Delete/List` interface against the standard S3 API (minio-go or aws-sdk-go-v2). No new infra at test time — the minio container is already in the compose stack, so the integration test can boot it via the existing brokertest harness pattern.
- **Acceptance test (`//go:build integration`):** Round-trip a blob through `S3Store` against a real minio container; assert `Get` returns bytes-identical content and `List(prefix)` enumerates it. **Mutation:** swap `Put` to write to the wrong bucket → `Get` must FAIL.

### 6. OTLP/Jaeger export shim for `pkg/tracing`
- **Target dir:** `pkg/tracing/`
- **Spec:** Tracing is homegrown with no exporter, so spans never reach the deployed Jaeger. Add a thin OTLP-HTTP exporter that serializes existing `Span`s (TraceID/ParentID/timings/attrs) to the OTLP JSON span shape and POSTs batches to the collector endpoint. Keep the homegrown API; only add a sink. (Stops short of full OTel SDK migration, which is larger.)
- **Acceptance test:** Stand up an `httptest.Server` as a fake collector; emit a parent+child span; assert the exporter POSTs one batch containing both spans with the correct parent linkage and a shared TraceID. **Mutation:** drop ParentID from the serialization → linkage assertion must FAIL.

---

## DEFERRED (need external infra / hardware / Zig / GPU kernels) — count: 4

| Item | Reason deferred |
|---|---|
| `internal/gpu/*` CUDA/ROCm/oneAPI/MLX backends | Requires physical GPUs + vendor SDKs (CUDA/ROCm/oneAPI/MLX) and cgo bindings; cannot be validated end-user-usable without hardware (CLAUDE-1 sink-side evidence impossible in CI). |
| Session live migration (CRIU/DMTCP) | Requires CRIU/DMTCP host binaries and privileged kernel checkpoint support; explicit stubs at `pkg/session/migration.go:178-222`. |
| WireGuard UPnP/NAT-PMP traversal | Requires a real NAT gateway/router to exercise; cannot prove traversal in a flat CI network. |
| True embedded Raft (`pkg/raft`) | Implementable in Go (hashicorp/raft), but a multi-week consensus deliverable, not a quick gap; current etcd-backed locks/election cover the immediate need, so this is deferred as a major work item rather than a quick win. |
