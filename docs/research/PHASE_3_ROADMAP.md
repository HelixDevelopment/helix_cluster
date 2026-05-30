# Helix Cluster OS — Phase 3 & Beyond Roadmap

> **Research Document** | Phase 3 Planning Agent | 2026-05-30
>
> This document synthesizes research from existing submodules, proto APIs, research artifacts, and the master implementation plan to define the next phases of Helix Cluster OS development.

---

## 1. Current State Summary

### Phases 0–2 Completed

| Phase | Name | Status | Key Deliverables |
|-------|------|--------|------------------|
| **0** | Foundation | ✅ Complete | 29 submodules, CI/CD, 20-service docker-compose, 26 pkg stubs, go.work, buf proto pipeline |
| **1** | Core Infrastructure | ✅ Complete | SWIM gossip (`pkg/swim`), WireGuard mesh (`pkg/wireguard`), service discovery (`pkg/discovery`), API gateway skeleton, security proto |
| **2** | Distributed Systems | ✅ Complete | Session CRDT (`pkg/session`), console nodes (PS4/PS5), edge/mobile research, scheduler proto, advisory locks proto |

### Existing pkg/ Packages (26 stubs)

```
backoff, classads, config, context, crypto, discovery, errors, events,
grpcutil, health, infra, jwt, leader, log, lru, metrics, middleware,
netutil, pubsub, ratelimit, retry, semaphore, serde, session, swim,
tracing, validator, websocket, wireguard, workerpool
```

### Existing API Definitions (api/v1/)

| Service | Proto | Status |
|---------|-------|--------|
| NodeService | `node.proto` | Defined |
| SessionService | `session.proto` | Defined |
| SchedulerService | `scheduler.proto` | Defined |
| HealthService | `health.proto` | Defined |
| SecurityService | `security.proto` | Defined |
| BuildService | `build.proto` | Defined |
| AdvisoryService | `advisory.proto` | Defined |

### Existing cmd/ Binaries

| Command | Status | Purpose |
|---------|--------|---------|
| `helix_infra` | ✅ Implemented | Infrastructure orchestrator (up/down/vm) |
| `helixd` | 🟡 Stub | Main control plane daemon |
| `helix-gateway` | 🟡 Stub | API gateway |
| `helix-scheduler` | 🟡 Stub | Job scheduler |
| `helix-security` | 🟡 Stub | Security manager |
| `helix-session` | 🟡 Stub | Session manager |
| `helix-agent` | 🟡 Stub | Per-node agent |
| `helix-build` | 🟡 Stub | Build service |
| `helix-health` | 🟡 Stub | Health monitor |
| `helix-llm` | 🟡 Stub | LLM brain |
| `helix-policy` | 🟡 Stub | Policy engine |
| `helix-setup` | 🟡 Stub | Setup wizard |
| `htmux` | 🟡 Stub | CLI client |

### Infrastructure Deployed via Docker Compose

- **PostgreSQL 16** (primary + replica)
- **Redis Cluster 7** (3 masters + 3 replicas)
- **etcd 3.5** (3-node Raft cluster)
- **NATS 2.10** with JetStream
- **Apache Kafka 4.0** (KRaft mode, 3 brokers)
- **RabbitMQ 3.13** (management)
- **Prometheus 2.50** + **Grafana 10.4** + **Jaeger 1.55**
- **HashiCorp Vault 1.16**

---

## 2. Research Findings

### 2.1 Consensus / Raft — NOT YET IMPLEMENTED

**Finding**: No Raft implementation exists in any submodule or pkg package.

- `pkg/leader` contains only a trivial `Election` stub with an atomic boolean — no consensus algorithm.
- etcd is deployed as an external dependency (3-node cluster in docker-compose) but there is **no etcd client wrapper** in the codebase.
- The master plan specifies Raft consensus for membership changes and distributed locks, but this is entirely unrealized.

**Gap**: The system currently has no distributed consensus for cluster state mutations. All membership changes, scheduler state, and session routing tables require Raft-backed durability.

### 2.2 Distributed KV Store — PARTIAL

**Finding**: etcd is deployed but not integrated.

- `cmd/helix_infra/main.go` references etcd clusters
- `pkg/infra/orchestrator.go` lists etcd in default services
- **No Go etcd client code exists** in pkg/ or internal/
- `pkg/discovery` has an `InMemoryBackend` only — no etcd backend

**Gap**: Need etcd client wrapper, distributed lock implementation (advisory.proto exists but no server), and KV backend for the discovery registry.

### 2.3 Scheduler — PROTO-ONLY

**Finding**: The scheduler API is defined but unimplemented.

- `api/v1/scheduler.proto` defines `ScheduleJob`, `CancelJob`, `GetJobStatus`, `ListJobs`, `StreamJobEvents`
- `cmd/helix-scheduler/` is an empty directory
- `internal/scheduler/` is an empty directory
- `pkg/classads` has a minimal `ClassAd` map stub — no expression parser
- The master plan describes a 12-stage Omega-model plugin pipeline; none of this exists

**Gap**: Complete scheduler implementation — queue, plugin framework, ClassAds evaluator, optimistic concurrency via etcd, binding, preemption.

### 2.4 Storage — MINIMAL

**Finding**: No distributed storage abstraction exists.

- `api/v1/types.proto` defines `StorageResources` but no storage service
- `Filesystem/` and `Storage/` submodules exist but are not integrated
- The master plan specifies Ceph for distributed storage; no Ceph integration exists
- No S3/minio abstraction layer

**Gap**: Distributed storage abstraction with pluggable backends (Ceph, NFS, S3, local).

### 2.5 Monitoring — INFRASTRUCTURE-ONLY

**Finding**: Prometheus/Grafana/Jaeger are deployed but not integrated into services.

- `pkg/metrics` has a trivial `Counter` stub — no Prometheus client integration
- `pkg/tracing` has a stub — no OpenTelemetry wiring
- `pkg/health` has a basic `Checker` — no composite scoring, no eBPF, no LSTM
- `observability/` submodule exists but is not wired into the core

**Gap**: Prometheus metrics exposition, OpenTelemetry tracing, health score computation, eBPF probes, LSTM failure prediction (Python isolated process).

### 2.6 Security — PROTO-ONLY

**Finding**: Security concepts are defined but largely unimplemented.

- `api/v1/security.proto` defines authn/authz RPCs
- `api/v1/types.proto` includes `spiffe_id` field
- `pkg/crypto` has a stub — no WireGuard key management, no mTLS
- `pkg/jwt` has a stub — no OIDC federation
- No SPIFFE/SPIRE integration, no OPA policy engine, no Vault client

**Gap**: mTLS everywhere, SPIFFE identity issuance, OPA policy evaluation, certificate rotation, secret management via Vault.

### 2.7 API Gateway — STUB

**Finding**: Gateway is defined but unimplemented.

- `cmd/helix-gateway/` is empty
- `pkg/middleware` has basic HTTP middleware chain — no Gin integration, no gRPC-Gateway, no rate limiting, no auth
- `pkg/websocket` has an `Upgrader` stub — no I/O streaming

**Gap**: Full gateway with REST/gRPC/WebSocket, mTLS termination, OPA enforcement, rate limiting, request routing.

### 2.8 CLI — INFRA-ONLY

**Finding**: Only `helix_infra` CLI is implemented.

- `cmd/helix_infra` has `up`, `down`, `vm`, `version` commands (Cobra)
- `cmd/htmux` is empty — no distributed tmux CLI
- No cluster management CLI (`node list`, `session attach`, etc.)

**Gap**: Rich `htmux` CLI for session management, node operations, cluster status, GPU inventory.

---

## 3. Phase 3 Scope & Goals

### Primary Objective

Transform Helix Cluster OS from a **research prototype with stubs** into a **functional distributed cluster OS** capable of:

1. Forming a multi-node cluster with durable consensus-backed state
2. Scheduling workloads across heterogeneous nodes
3. Managing distributed sessions with I/O forwarding
4. Operating securely with Zero Trust defaults
5. Observing itself with production-grade monitoring

### Phase 3 Sub-Phases

| Sub-Phase | Name | Weeks | Tasks | Goal |
|-----------|------|-------|-------|------|
| **3.1** | Consensus & State | 3 | 40 | Raft-backed etcd integration, distributed locks, leader election |
| **3.2** | Scheduler Engine | 4 | 60 | Omega-model scheduler, ClassAds parser, plugin pipeline |
| **3.3** | Session Core | 3 | 40 | Distributed session lifecycle, I/O forwarding, WebSocket streaming |
| **3.4** | Security Hardening | 3 | 40 | mTLS, SPIFFE, OPA, Vault, certificate rotation |
| **3.5** | Observability | 2 | 30 | Prometheus metrics, OpenTelemetry tracing, health scoring |
| **3.6** | Gateway & CLI | 2 | 30 | API Gateway, htmux CLI, REST/gRPC/WebSocket |
| **3.7** | Integration | 2 | 20 | End-to-end tests, chaos experiments, performance validation |

**Total: ~16 weeks | ~260 tasks | ~1,040 person-hours**

---

## 4. Package Breakdown

### 4.1 New pkg/ Packages Required

| Package | Purpose | Integration Point |
|---------|---------|-------------------|
| `pkg/etcd` | etcd client wrapper, KV operations, watches, leases, transactions | All control plane services |
| `pkg/raft` | Raft consensus primitives (wrapper over etcd Raft or hashicorp/raft) | `pkg/leader`, `pkg/discovery` |
| `pkg/lock` | Distributed advisory locks (etcd-backed) | Scheduler, Session Manager |
| `pkg/scheduler` | Omega-model scheduler core, plugin framework, queue | `cmd/helix-scheduler` |
| `pkg/classads` *(expand)* | ClassAds expression parser, evaluator, matcher | `pkg/scheduler` |
| `pkg/gpu` | GPU device abstraction, DRA-compatible models, backend manager | `cmd/helixd`, `internal/gpu/*` |
| `pkg/security` | mTLS config, SPIFFE ID parsing, certificate rotation | All services |
| `pkg/policy` | OPA policy evaluation, Rego compilation, decision caching | API Gateway, Scheduler |
| `pkg/gateway` | HTTP/gRPC reverse proxy, WebSocket upgrade, route table | `cmd/helix-gateway` |
| `pkg/httmux` | htmux CLI client library, WebSocket I/O, session RPCs | `cmd/htmux` |
| `pkg/observability` | Prometheus registry, OTel tracer provider, health aggregator | All services |
| `pkg/storage` | Distributed storage abstraction, Ceph/NFS/S3 backends | Build Service, Session Manager |
| `pkg/build` | Bazel RBE protocol, CAS, action cache, worker lease | `cmd/helix-build` |

### 4.2 New internal/ Packages Required

| Package | Purpose | Language |
|---------|---------|----------|
| `internal/scheduler` | Scheduler service implementation, gRPC handlers | Go |
| `internal/session` | Session manager service, backend factory, migration orchestrator | Go |
| `internal/security` | Security manager service, SPIRE agent wrapper, attestation | Go |
| `internal/gateway` | API gateway service, Gin server, middleware stack | Go |
| `internal/health` | Health monitor service, eBPF loader, LSTM gRPC client | Go + Python |
| `internal/node` | Node agent service, resource probes, heartbeat | Go |
| `internal/gpu/cuda` | NVIDIA CUDA backend (cgo) | C + Go |
| `internal/gpu/rocm` | AMD ROCm backend (cgo) | C + Go |
| `internal/gpu/oneapi` | Intel oneAPI backend (cgo) | C + Go |
| `internal/gpu/mlx` | Apple MLX backend (cgo) | C + Go |
| `internal/session/tmux` | tmux backend wrapper | Go |
| `internal/session/zellij` | Zellij backend wrapper | Go |
| `internal/session/screen` | GNU screen backend wrapper | Go |
| `internal/session/native` | Native PTY backend | Go |

### 4.3 cmd/ Binaries to Implement

| Command | Priority | Depends On |
|---------|----------|------------|
| `helixd` | P0 | All pkg/ foundations |
| `helix-scheduler` | P0 | `pkg/scheduler`, `pkg/etcd` |
| `helix-session` | P0 | `pkg/session`, `pkg/scheduler` |
| `helix-security` | P0 | `pkg/security`, `pkg/policy` |
| `helix-gateway` | P0 | `pkg/gateway`, `pkg/security` |
| `helix-agent` | P0 | `pkg/swim`, `pkg/wireguard` |
| `htmux` | P1 | `pkg/httmux` |
| `helix-health` | P1 | `pkg/observability` |
| `helix-build` | P1 | `pkg/build` |
| `helix-llm` | P2 | `pkg/observability`, LLMsVerifier |
| `helix-policy` | P2 | `pkg/policy` |
| `helix-setup` | P2 | `pkg/config`, `pkg/netutil` |

---

## 5. Integration Points with Phase 2

### 5.1 SWIM + WireGuard → Node Agent

```
pkg/swim (Phase 2) ──► internal/node (Phase 3)
pkg/wireguard (Phase 2) ──► internal/node (Phase 3)
```

- The Phase 2 SWIM protocol and WireGuard mesh become the **node agent's** networking substrate.
- Node agent adds resource probing, heartbeat to control plane, and etcd registration.

### 5.2 Discovery → etcd Backend

```
pkg/discovery (Phase 2) ──► pkg/etcd (Phase 3)
```

- Replace `InMemoryBackend` with `EtcdBackend` implementing the `Backend` interface.
- Add TTL leases for automatic instance expiration.

### 5.3 Session CRDT → Session Manager

```
pkg/session (Phase 2) ──► internal/session (Phase 3)
```

- The Phase 2 `CRDTSessionState` becomes the session manager's distributed state synchronization primitive.
- Merge logic is reused for multi-node pane state.

### 5.4 ClassAds → Scheduler

```
pkg/classads (Phase 2) ──► pkg/scheduler (Phase 3)
```

- Expand the stub `ClassAd` map into a full expression parser (AST, evaluator).
- Scheduler uses ClassAds for Filter and Score stages.

### 5.5 Leader Election → Raft

```
pkg/leader (Phase 2) ──► pkg/raft (Phase 3)
```

- Replace atomic-boolean election with true Raft consensus.
- Used for control plane leader election and distributed lock coordination.

---

## 6. Priority Ordering

### P0 — Critical Path (Cluster Cannot Form Without These)

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 1 | `pkg/etcd` client wrapper | 1 week | All stateful services depend on etcd |
| 2 | `pkg/raft` / leader election | 1 week | Control plane HA, split-brain prevention |
| 3 | `pkg/lock` distributed locks | 1 week | Scheduler concurrency safety |
| 4 | `pkg/scheduler` core + queue | 2 weeks | Workload placement — the economic engine |
| 5 | `pkg/classads` expression parser | 1 week | Required for scheduler Filter/Score |
| 6 | `internal/scheduler` gRPC service | 1 week | Exposes scheduler.proto API |
| 7 | `internal/node` agent service | 1 week | Nodes must register and report resources |
| 8 | `pkg/security` mTLS + SPIFFE | 2 weeks | Zero Trust baseline |
| 9 | `pkg/gateway` reverse proxy | 1 week | Unified API ingress |
| 10 | `helixd` control plane daemon | 1 week | Orchestrates all services |

### P1 — Essential for MVP

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 11 | `internal/session` + backends | 2 weeks | User-facing session management |
| 12 | `pkg/httmux` + `htmux` CLI | 1 week | Primary user interface |
| 13 | `pkg/observability` metrics | 1 week | Production operability |
| 14 | `internal/health` monitor | 1 week | Self-healing, failure prediction |
| 15 | `pkg/storage` abstraction | 1 week | Build service, session persistence |
| 16 | `pkg/build` RBE protocol | 1 week | First high-value application |

### P2 — Important but Can Be Deferred

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 17 | `pkg/policy` OPA integration | 1 week | Fine-grained authorization |
| 18 | `internal/gpu/*` backends | 2 weeks | GPU scheduling (NVIDIA-first) |
| 19 | `helix-llm` advisory brain | 1 week | Intelligent optimization |
| 20 | `helix-setup` wizard | 1 week | User onboarding |
| 21 | CRIU/DMTCP migration | 2 weeks | Live session migration |
| 22 | Web UI (web/) expansion | 2 weeks | Browser-based management |

### P3 — Post-MVP Enhancements

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 23 | LSTM failure prediction (Python) | 1 week | Predictive maintenance |
| 24 | eBPF probes | 1 week | Kernel-level observability |
| 25 | Chaos engineering framework | 1 week | Resilience validation |
| 26 | TLA+ formal verification | 2 weeks | Safety-critical correctness |
| 27 | Multi-region federation | 2 weeks | Geographic distribution |
| 28 | Console node agents (PS4/PS5) | 2 weeks | Phase 2 hardware integration |
| 29 | Edge/mobile agents (ARM64) | 2 weeks | Phase 3 hardware integration |

---

## 7. Risk Analysis

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| etcd client complexity | Medium | High | Use official `go.etcd.io/etcd/client/v3`, wrap for testability |
| Scheduler conflict rates | Medium | High | Start with single-scheduler, add optimistic concurrency later |
| CRIU reliability | Medium | High | DMTCP fallback; graceful restart option |
| GPU backend fragmentation | High | Medium | NVIDIA-first (CUDA), defer AMD/Intel/Apple |
| mTLS certificate rotation | Low | Critical | Short TTL (24h), automatic renewal at 83% |
| Scope creep | High | High | Strict P0-first gating; no Phase 3+ work in Phase 3 |
| Go 1.25/1.26 compatibility | Low | Medium | CI tests on Go 1.25 + 1.26 |

---

## 8. Success Criteria (Phase 3 Exit Gates)

| KPI | Target | Measurement |
|-----|--------|-------------|
| Cluster formation time | <30s for 5-node cluster | Automated benchmark |
| etcd write latency | p99 <10ms | etcd benchmark |
| Scheduling throughput | 100 decisions/sec | Scheduler benchmark |
| Session creation time | <2 seconds | End-to-end test |
| mTLS handshake success | 100% | Security test suite |
| Test coverage (pkg/) | >70% line coverage | Codecov |
| Build success rate | >99% | CI pass rate |

---

## 9. Beyond Phase 3 — Phase 4 & 5 Preview

### Phase 4: Applications (Weeks 17–22)

| Sub-Phase | Deliverable |
|-----------|-------------|
| 4.1 | Bazel RBE server + AOSP build distribution |
| 4.2 | distcc/icecream worker pool |
| 4.3 | Content-addressed distributed cache (CAS) |
| 4.4 | GPU compute job submission (batch + interactive) |

### Phase 5: Intelligence & Scale (Weeks 23–30)

| Sub-Phase | Deliverable |
|-----------|-------------|
| 5.1 | LLM Brain advisory system (RAG + Constitutional AI) |
| 5.2 | LLMsVerifier integration |
| 5.3 | LSTM failure prediction (Python isolated process) |
| 5.4 | Reinforcement learning feedback loop |
| 5.5 | Multi-region cluster federation |
| 5.6 | Console node integration (PS4/PS5 Linux agents) |
| 5.7 | Edge/mobile node integration (ARM64, Android, iOS donors) |

---

## 10. References

1. `docs/research/mvp/IMPLEMENTATION_PLAN.md` — 50-week master plan (10,000+ tasks)
2. `docs/research/mvp/HELIX_CLUSTER_OS_COMPLETE_REPORT.md` — Architecture blueprint
3. `docs/research/phase_02/HELIXCLUSTER_PHASE2_COMPLETE_REPORT.md` — Phase 2 deliverables
4. `docs/research/phase_03/HelixCluster_Phase_3/HELIXCLUSTER_ALL_PHASES_MASTER.md` — All phases master
5. `docs/research/phase_03/HelixCluster_Phase_3/HELIXCLUSTER_PHASE3_COMPLETE_REPORT.md` — Phase 3 edge/mobile
6. `api/v1/*.proto` — Current API definitions
7. `pkg/*` — Existing package stubs
8. `deploy/compose/helix_infra.yml` — 20-service infrastructure stack
