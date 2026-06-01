# Continuation Document

**Revision:** 62
**Last modified:** 2026-06-01T19:08:09Z
**Description:** Sacred invariant resumption document for Helix Cluster OS
**Authority:** Constitution §12.10
**Maintainer:** Operator + AI loop

---

## §0: How to use this document

Any CLI agent resuming work on this project MUST read this file first.

## §1: Recently Completed Work (last 10 commits)

| Commit | Message |
|--------|---------|
| `203c42a` | Foundation wave 13: 5 disjoint-package streams, 6 items. internal/gateway X-Helix-Deny-Reason header + scheduler:write scope enforcement (401/403 reject-before-proxy); internal/node REAL EtcdBackend wiring (Config.EtcdEndpoints -> NewEtcdBackend w/ SWIM-tied TTL lease + resource metadata, BackendType() status, in-memory fallback) — node registrations now cluster-visible (unblocks 1135 two-node formation); internal/policy real persisted OPA decision-log sink (DecisionLogEntry RunUUID/Allow/Reason/PolicyName); NEW pkg/scheduler/edgeaware.go EdgeAware Filter (tier>=6 reject session/sleeping/over-thermal/not-charging w/ reasons) + Score (per-tier+thermal-headroom bonuses); NEW pkg/offlinesync delta-compression (flate) offline job-ledger + idempotent reconcile (100% recovery). ZERO new deps. GATE: build/vet/vet-integration clean, full -short -race green, integration tier 1210 vs REAL etcd (A sees B+metadata, UUID) + gateway/policy green, live mutation spot-check on edgeaware T6 guard confirmed real. |
| `23df87b` | Foundation wave 12: mark 14 items Completed (HXC-1148/1149/1150/1151/1156/1157/1188/1191/1195/1196/1211/1212/1214/1215) — 12 integration-proven vs real etcd/minio + 2 gate-verified already-implemented (STUN/OTLP). Registry 137->151 Completed / 488 Queued. |
| `7718c20` | Foundation wave 12: 5 disjoint-package streams, 12 items integration-proven vs real etcd/minio. pkg/wireguard typed ErrUnsupported (errors.Is) + key-rotation grace/overlap ActiveKeys() consumed by mesh; pkg/scheduler named ClassAdFilter/ScorePlugin (classads Requirements/Rank) + thermal-aware plugin (filter/score on temp_c threshold); internal/scheduler real PREEMPTED lifecycle event (terminal, ordered); NEW pkg/edge trust model (FULL/STANDARD/SEMI/EDGE_DONOR) + workload restriction matrix + work-unit resource-limit executor (kill-after duration) + declarative per-tier ScheduleRule engine; NEW internal/verifier SEMI output verification (sha256 redundant-recompute reject tamper) + pkg/discovery etcd TTL-lifecycle integration test + pkg/storage real-minio S3 round-trip integration test. ZERO new deps. GATE: build/vet/vet-integration clean, full -short -race green, integration tier vs real etcd(:38623 ephemeral)+minio(:9000) green, mutation spot-check on verifier confirmed real. |
| `6715b3a` | Foundation wave 11: mark 3 items Completed (HXC-1122/1123/1124) — integration-proven vs real capnp/clang++/libzmq |
| `63822b3` | Foundation wave 11: Cap'n Proto zero-copy serde (node.capnp + capnpc-go, AllocsPerRun==0), FlatBuffers GPU compute payloads (compute.fbs + flatc Go + pooled builder + clang++ C++ consumer harness), ZeroMQ data-plane (ROUTER/DEALER correlated + PUSH/PULL at-least-once, pebbe/zmq4) — integration-proven (real capnp/clang++/libzmq). Toolchains capnp/flatc/libzmq + Go runtimes wired. GATE: fixed C++ harness flatc root-accessor (GetRootAsComputeTask->GetComputeTask) |
| `546c68d` | Foundation wave 10: mark 3 items Completed (HXC-1131/1134/1144) — integration-proven vs real vault/etcd/postgres |
| `467a6dc` | Foundation wave 10: Vault secret injection + rotation (KV v2, no-downtime refresh), WireGuard mesh segmentation policy engine (DENY-wins label selectors + enforcement ruleset), Backup service (etcd clientv3 snapshot + pg_dump/restore state-match) — integration-proven vs real vault/etcd/postgres. GATE: fixed 2 non-compiling integration files (unused ctx, unused require import) |
| `f1e920b` | Foundation wave 9: mark 4 items Completed (HXC-1104/1116/1127/1132) — integration-proven vs real redis/mTLS/opa/HTTP; CLAUDE-2 macOS CPU utilization now real |
| `7634fbb` | Foundation wave 9: Redis store (routing TTL + NodeEvent pub/sub, go-redis/v9), SPIFFE SVID issuance + mTLS identity (go-spiffe/v2), OPA policy engine + HelixConstitution scheduling (open-policy-agent/opa), metrics collector scrape endpoint — integration-proven vs real redis/mTLS/opa/HTTP. GATE FIXES: tmux ControlModeAttach EOF-before-drain race; pkg/resources DarwinReader now samples REAL macOS CPU utilization via top (CLAUDE-2: no more 0%% on macOS); redis test mutex-copy vet violation |
| `51cea15` | Foundation wave 8: mark 5 items Completed (HXC-1108/1109/1118/1120/1121) — integration-proven vs real NATS/Kafka/HTTP |

## §2: Environment Snapshot

| Property | Value |
|----------|-------|
| **Branch** | `main` |
| **Commit** | `203c42a` |
| **Timestamp** | 2026-06-01T19:08:09Z |

## §3: Active Work

| HXC | Title | Status |
|-----|-------|--------|
| HXC-922 | Node service, advisory locks, chaos tests, docs | ✅ Done |
| HXC-921 | Web UI, K8s/Helm, integration, E2E, benchmarks | ✅ Done |
| HXC-920 | Health, security, build, wireguard services | ✅ Done |
| HXC-919 | htmux, messaging, GPU, WASM, storage, build manifest | ✅ Done |
| HXC-918 | Wire scheduler/session gRPC to real backends, etcd discovery | ✅ Done |
| HXC-916 | Stub expansion (config, retry, ratelimit, validator, build cache) | ✅ Done |
| HXC-914 | LLM, policy, setup, distributed lock | ✅ Done |
| HXC-913 | Health, security, ClassAds parser | ✅ Done |
| HXC-912 | Gateway, helixd, helix-agent | ✅ Done |
| HXC-911 | Scheduler, Session gRPC services | ✅ Done |
| HXC-910 | Console, security, testing infra | ✅ Done |

## §4: Next Planned Work

All MVP, Phase 2, Phase 3, and Phase 4 are **COMPLETE**. Potential future enhancements:

1. **Performance optimization** — Profile and optimize hot paths
2. **Multi-region support** — Cross-region cluster federation
3. **GPU scheduling v2** — Multi-GPU, fractional GPU allocation
4. **Web UI real-time** — WebSocket integration for live updates
5. **Operator pattern** — Kubernetes operator for cluster management

## §5: Known Issues / Blockers

- None

## §6: Quick Commands

```bash
# Run all tests (requires GOWORK due to workspace modules)
GOWORK=$(pwd)/go.work go test ./... -race -count=1

# Build all binaries
GOWORK=$(pwd)/go.work go build ./cmd/...

# Build web UI
cd web && npm run build

# Render Helm chart
helm template helix-cluster deploy/helm/

# Run benchmarks
go test -bench=. ./test/benchmark

# Run chaos tests
go test -race -count=1 ./test/chaos
```
