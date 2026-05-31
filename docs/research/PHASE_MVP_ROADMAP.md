# Helix Cluster OS — MVP Roadmap: Foundation Through First Demonstrable Cluster

> **Research Document** | MVP Planning | 2026-05-31
>
> This document synthesizes the MVP scope (Phases 0–8) from IMPLEMENTATION_PLAN.md and the current
> completion state from MVP_PROGRESS.md into a single actionable roadmap through v1.0.0-dev-mvp.

---

## 1. Current State Summary

| Phase | Name | Tasks (P0) | Done | % | Status |
|-------|------|-----------|------|---|--------|
| **0** | Foundation | 120 | ~110 | 92% | Near Complete |
| **1** | Core Infrastructure | 93 | ~45 | 48% | In Progress |
| **2** | Resource Management | 59 | ~5 | 8% | Not Started |
| **3** | Session Manager | 89 | ~15 | 17% | Not Started |
| **4** | Build Service | 17 | ~0 | 0% | Not Started |
| **5** | LLM Brain | 25 | ~0 | 0% | Not Started |
| **6** | Security Hardening | 7 | ~2 | 29% | Not Started |
| **7** | QA & Testing | 16 | ~5 | 31% | Partial |
| **8** | Polish & Release | 22 | ~3 | 14% | Not Started |

**Honest assessment:** Phase 0 is structurally complete (repo, CI scaffolding, docker-compose, 30+
pkg stubs). Phase 1 has real implementations for SWIM, WireGuard, discovery, leader election, JWT,
WebSocket, and tracing, but NATS/JetStream, Kafka, and etcd wrappers remain stubs. Phases 2–8 are
largely unimplemented; test suites exist but validate structure, not end-user operation.

---

## 2. MVP Scope & Goals

### Primary Objective

Deliver **v1.0.0-dev-mvp**: a minimal but genuinely functional Helix Cluster OS instance where
two or more nodes can form a cluster, a user can create a distributed session, and a build job can
be scheduled and executed — all provable via real integration tests with real services.

### Four Pillars

| Pillar | Description | Outcome |
|--------|-------------|---------|
| **Cluster Formation** | SWIM gossip + WireGuard mesh, etcd-backed registry, leader election | Two nodes join automatically and stay coherent |
| **Resource Awareness** | cgroups v2 + /proc readers, GPU probe, Omega-model scheduler | Jobs land on the right node for the right reason |
| **Distributed Sessions** | tmux control-mode backend, PTY forwarding, CRDT state | `htmux new` creates a session visible from a remote node |
| **Operability** | Prometheus metrics, Jaeger traces, Vault secrets, setup wizard | Operator can inspect and reason about the cluster |

---

## 3. Sub-Phases

| Sub-Phase | Name | Weeks | P0 Tasks | Goal |
|-----------|------|-------|----------|------|
| **Ph 0** | Foundation | 1–4 | 120 | Repo, CI/CD, docker-compose, 30 pkg stubs, proto pipeline |
| **Ph 1** | Core Infrastructure | 5–12 | 93 | SWIM, WireGuard, discovery, leader, NATS/Kafka/etcd, API gateway |
| **Ph 2** | Resource Management | 13–18 | 59 | cgroups/proc readers, eBPF probes, GPU, Omega scheduler |
| **Ph 3** | Session Manager | 19–26 | 89 | tmux/PTY backends, I/O forwarding, CRIU migration, htmux CLI |
| **Ph 4** | Build Service | 27–30 | 17 | Bazel RBE protocol, distcc worker pool, AOSP integration |
| **Ph 5** | LLM Brain | 31–36 | 25 | Advisory system, LLMsVerifier, RAG knowledge base |
| **Ph 6** | Security Hardening | 37–40 | 7 | Zero Trust, SPIFFE/SPIRE, audit logging, mTLS enforcement |
| **Ph 7** | QA & Testing | 41–46 | 16 | HelixQA Challenges, chaos engineering, TLA+ verification |
| **Ph 8** | Polish & Release | 47–50 | 22 | Setup wizard, Debian/Homebrew/Docker packages, docs |
| **Total** | | **50 wks** | **448** | **v1.0.0-dev-mvp tagged** |

---

## 4. Package Breakdown

### 4.1 Real pkg/ Packages (from `ls pkg/`)

| Package | Purpose | Integration Point |
|---------|---------|-------------------|
| `pkg/swim` | SWIM gossip: membership, failure detection, suspicion | `pkg/discovery`, `internal/node` |
| `pkg/wireguard` | WireGuard mesh: tunnel mgmt, key exchange, config gen | `pkg/netutil`, `internal/node` |
| `pkg/discovery` | Service registry with TTL, pluggable backends | API gateway, scheduler |
| `pkg/leader` | Distributed leader election with TTL failover | All control-plane services |
| `pkg/session` | CRDT session state, lifecycle (create/attach/migrate) | `cmd/helix-session` |
| `pkg/scheduler` | Omega-model scheduler: filter/score pipeline | `cmd/helix-scheduler` |
| `pkg/resources` | CPU/mem/GPU resource aggregator (cgroups v2, /proc) | Scheduler, node agent |
| `pkg/jwt` | HMAC/RSA JWT issuance and verification | API gateway, auth |
| `pkg/middleware` | HTTP middleware chain (auth, rate-limit, tracing) | All HTTP services |
| `pkg/tracing` | UUID trace-ID propagation, OTel integration | All services |
| `pkg/websocket` | Real WebSocket upgrade and bidirectional I/O | Session I/O forwarding |
| `pkg/config` | Env-var and file config loading | All services |
| `pkg/metrics` | Prometheus counter/histogram helpers | All services |
| `pkg/health` | gRPC health protocol implementation | All services |
| `pkg/events` | NATS/JetStream client wrapper **(stub — P0 gap)** | Messaging bus |
| `pkg/etcd` | etcd client wrapper **(stub — P0 gap)** | Discovery backend, locks |
| `pkg/pubsub` | Kafka producer/consumer wrapper **(stub — P0 gap)** | Audit log, event stream |
| `pkg/grpcutil` | gRPC dial/interceptor helpers | All gRPC services |
| `pkg/netutil` | IP/CIDR helpers, interface enumeration | WireGuard, SWIM |
| `pkg/crypto` | Key generation, TLS helpers | Security layer |
| `pkg/lock` | Distributed lock primitives | etcd-backed critical sections |
| `pkg/log` | Structured zerolog wrapper | All services |
| `pkg/errors` | Typed error hierarchy | All packages |
| `pkg/retry` | Exponential backoff with jitter | All network clients |
| `pkg/ratelimit` | Token bucket rate limiter | API gateway |
| `pkg/backoff` | Configurable backoff strategies | Retry, reconnect logic |
| `pkg/storage` | KV storage abstraction (Redis backend) | Session state, cache |
| `pkg/classads` | HTCondor ClassAds expression evaluator | Scheduler matching |
| `pkg/serde` | Cap'n Proto / msgpack serialization helpers | Data plane |
| `pkg/wasm` | WebAssembly plugin runtime | Scheduler plugin framework |
| `pkg/context` | Deadline / cancellation utilities | All async code |
| `pkg/semaphore` | Counting semaphore | Resource throttling |
| `pkg/workerpool` | Goroutine pool with backpressure | Build service, batch jobs |
| `pkg/lru` | LRU cache with TTL | Discovery cache, session cache |
| `pkg/build` | Build abstraction (RBE client stub) **(planned)** | `cmd/helix-build` |
| `pkg/hxcregistry` | Internal image/artifact registry client | Node agent, build service |
| `pkg/testing` | Test helpers, golden files, fake backends | All test suites |
| `pkg/validator` | Input validation helpers | API handlers |
| `pkg/security` | Policy evaluation primitives | `internal/security` |

### 4.2 Real internal/ Packages (from `ls internal/`)

| Package | Purpose |
|---------|---------|
| `internal/node` | Node agent: heartbeat, SWIM+WireGuard wiring, resource probe |
| `internal/gateway` | API gateway: routing, auth, WebSocket upgrade |
| `internal/scheduler` | Omega scheduler runtime: concurrent transaction model |
| `internal/session` | Session manager runtime: backend dispatch, migration |
| `internal/health` | Health monitor: node liveness aggregation |
| `internal/llm` | LLM brain: advisory pipeline, LLMsVerifier wiring |
| `internal/security` | Zero Trust policy engine, SPIFFE integration |
| `internal/console` | Console node adapter: PS4/PS5 GPU detection |
| `internal/wireguard` | WireGuard daemon wrapper, netlink interface |
| `internal/build` | Build service runtime: Bazel RBE dispatch |
| `internal/messaging` | NATS/Kafka subscriber wiring |
| `internal/gpu` | GPU backend (CUDA/ROCm/oneAPI/MLX) **(planned)** |
| `internal/advisory` | Advisory service: suggestion queue, confidence scoring |
| `internal/policy` | Policy rule engine (OPA-compatible) |

---

## 5. Integration Points

### 5.1 Seven-Layer Stack

```
L7  htmux CLI / Web UI (React+Vite)    cmd/htmux, web/
L6  API Gateway                         cmd/helix-gateway   (internal/gateway)
L5  Control Plane microservices         cmd/helix-{scheduler,session,health,llm,advisory,policy}
L4  Data & Messaging                    etcd, PostgreSQL, Redis, Kafka, NATS  (docker-compose)
L3  Node Runtime                        cmd/helix-node      (internal/node)
L2  System Primitives                   pkg/swim, pkg/wireguard, pkg/resources, pkg/classads
L1  Hardware Abstraction                pkg/crypto (SPIFFE), internal/wireguard, internal/gpu
L0  Physical Hardware                   CPU / GPU / RAM / NIC
```

### 5.2 Control-Plane Message Bus

```
internal/node ──NATS──► internal/scheduler ──gRPC──► internal/session
      │                       │                             │
      └──SWIM──► pkg/discovery ◄──etcd watch──── internal/health
                       │
                  cmd/helix-gateway  (REST + WebSocket)
```

### 5.3 Proto APIs → Services

| Proto file | Current Status | MVP Consumer |
|------------|---------------|--------------|
| `node.proto` | Defined | `cmd/helix-node` heartbeat + resource reports |
| `scheduler.proto` | Defined | `cmd/helix-scheduler` job submission |
| `session.proto` | Defined | `cmd/helix-session` lifecycle + I/O |
| `health.proto` | Defined | `cmd/helix-health` liveness checks |
| `auth.proto` | Defined | `cmd/helix-security` SPIFFE + JWT |

---

## 6. Priority Ordering

### P0 — Critical Path (MVP Blocked Without These)

| # | Task | Package / Cmd | Effort | Reason |
|---|------|--------------|--------|--------|
| 1 | NATS/JetStream real client | `pkg/events` | 1 wk | Messaging backbone; every service depends on it |
| 2 | etcd real client wrapper | `pkg/etcd` | 1 wk | Discovery backend; distributed locks |
| 3 | Kafka producer/consumer | `pkg/pubsub` | 1 wk | Audit log; event streaming |
| 4 | cgroups v2 + /proc reader | `pkg/resources` | 1 wk | Scheduler blind without resource data |
| 5 | Omega scheduler core | `internal/scheduler` | 2 wks | Job placement; MVP blocker |
| 6 | tmux control-mode backend | `pkg/session` | 2 wks | Session is the primary user UX |
| 7 | Node gRPC handlers (real) | `cmd/helix-node` | 1 wk | Nodes must register and report resources |
| 8 | NAT traversal / mesh | `pkg/wireguard` | 1 wk | Multi-node cluster needs full mesh |
| 9 | API gateway wiring | `cmd/helix-gateway` | 1 wk | External entry point for CLI + Web UI |

### P1 — Essential for MVP Quality

| # | Task | Package / Cmd | Effort | Reason |
|---|------|--------------|--------|--------|
| 10 | GPU resource probe | `internal/gpu` | 1 wk | GPU scheduling pillar |
| 11 | Bazel RBE client | `internal/build` | 2 wks | Build service; AOSP demo |
| 12 | HelixQA challenge suite | `cmd/helix-test` | 1 wk | CLAUDE-1 mandate: end-user proof |
| 13 | SPIFFE SVID issuance | `internal/security` | 1 wk | Zero Trust enforcement |
| 14 | Session I/O WebSocket forwarding | `cmd/helix-session` | 1 wk | Remote session usability |
| 15 | Prometheus metrics on all services | `pkg/metrics` | 0.5 wk | Operability gate |
| 16 | Jaeger trace propagation | `pkg/tracing` | 0.5 wk | Observability gate |

### P2 — Deferred Post-MVP

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 17 | Zellij / screen backends | 1 wk | tmux sufficient for MVP |
| 18 | CRIU live migration | 2 wks | Graceful restart acceptable for v1 |
| 19 | LLM Brain (advisory) | 2 wks | Optional for MVP; Phase 5 |
| 20 | Zig build system | 1 wk | Go-only stack sufficient for MVP |
| 21 | C/C++ CMake / GPU kernels | 2 wks | Software GPU fallback for MVP |
| 22 | Console node (PS4/PS5) | 2 wks | x86_64/ARM64 nodes sufficient for MVP |

---

## 7. Risk Analysis

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| Tests pass on non-functional features (PASS-bluff) | High | Critical | CLAUDE-1 mandate: require sink-side evidence; HelixQA Challenge gates |
| NATS/etcd/Kafka stubs shipped as real | High | High | Integration tests MUST hit real docker-compose services; no mock-only CI |
| WireGuard NAT traversal fragility | Medium | High | STUN/TURN relay fallback; UDP hole-punch timeout |
| Omega scheduler race conditions | Medium | High | TLA+ model for concurrency; chaos test with concurrent job submissions |
| CRIU migration failures | Medium | Medium | Graceful-restart fallback; transparent client reconnect |
| GPU backend vendor fragmentation | High | Medium | CUDA-first; ROCm/oneAPI behind feature flag |
| etcd scalability beyond 100 nodes | Low | Medium | MultiRaft sharding in Phase 2 |
| Split-brain during network partition | Low | Critical | Raft quorum enforcement; automatic fencing |
| LLM advisory hallucination | Medium | Critical | Advisory-only (never binding alone); LLMsVerifier mandatory gate |

---

## 8. Success Criteria (MVP Exit Gates)

| KPI | Target | Measurement |
|-----|--------|-------------|
| Two-node cluster formation | Under 60 s | Integration test: two real nodes join via SWIM+WireGuard |
| SWIM failure detection | Under 5 s | Integration test: kill one node, detect within window |
| Session create (htmux new) | Under 3 s | End-to-end test: CLI → gateway → session service → tmux backend |
| Remote session attach | Under 1 s round-trip | E2E test: PTY keystrokes forwarded and echoed via WebSocket |
| Job scheduling decision | Under 100 ms | Benchmark: submit 10 jobs, each placed correctly by Omega |
| AOSP build dispatch | At least 4 nodes utilized | Integration test: Bazel RBE tasks spread across node pool |
| Test coverage (pkg/) | Greater than 60% line | go test -cover; NOT mock-only |
| Mutation test coverage | Every pkg has paired mutation | Constitution §1.1 check; automated scan |
| Integration tests vs real services | 100% of P0 features | docker-compose up; no fake backends for integration suite |
| HelixQA Challenge pass rate | 100% of P0 Challenges | `cmd/helix-test` Challenge runner; sink-side log evidence captured |
| **CLAUDE-1 End-User Gate** | Feature works for end user | Screenshot/log/metric proving visible operation; no PASS-bluff |
| Prometheus metrics live | All 14 services scraped | Grafana dashboard loads without gaps |
| Vault secrets accessible | All services use Vault-injected creds | `vault kv get` confirms rotation |
| Release tag published | `v1.0.0-dev-mvp` on all modules | `git tag`; go.work verify |

**CLAUDE-1 Operative Requirement (Constitution §7.1):** No feature may be declared complete based
solely on unit tests. Every MVP feature MUST have: (a) an integration test against a real service
(docker-compose, not mock), (b) an end-to-end test exercising it as an end user would, (c) a
HelixQA Challenge that validates it, and (d) captured sink-side evidence (log line, screenshot,
or metric) proving observable operation.

---

## 9. Bridge to Phase 2

Phase 2 (Console Nodes & Distributed Foundations) picks up directly from MVP artifacts:

| MVP Deliverable | Phase 2 Consumer |
|----------------|-----------------|
| `pkg/swim` + `pkg/wireguard` (real, tested) | PS4/PS5 nodes join the same mesh |
| `internal/scheduler` Omega core | Extended with ClassAds bilateral matching |
| `pkg/session` CRDT + tmux backend | CRIU live migration; Zellij/screen backends |
| `pkg/resources` cgroups reader | `internal/console` GPU wrapper for PS4/PS5 |
| `pkg/etcd` real client | Full etcd-backed discovery; distributed locks |
| HelixQA Challenge suite | Extended with console-node and GPU Challenges |
| `v1.0.0-dev-mvp` release | Baseline for Phase 2 regression; known-good cluster state |

Phase 2 extends the cluster from x86_64/ARM64 servers to heterogeneous console hardware, adds the
full Omega scheduler with resource negotiation, and hardens the security layer with SPIFFE/SPIRE.

---

## 10. References

1. `/Users/milosvasic/Projects/HelixCluster/docs/research/mvp/IMPLEMENTATION_PLAN.md` — 50-week master plan (10,000+ tasks)
2. `/Users/milosvasic/Projects/HelixCluster/docs/research/MVP_PROGRESS.md` — Authoritative completion status
3. `/Users/milosvasic/Projects/HelixCluster/docs/research/mvp/HELIX_CLUSTER_OS_COMPLETE_REPORT.md` — Architecture blueprint
4. `/Users/milosvasic/Projects/HelixCluster/docs/research/mvp/CLUSTER_OS_ARCHITECTURE.md` — Architecture deep-dive
5. `/Users/milosvasic/Projects/HelixCluster/docs/research/PHASE_2_ROADMAP.md` — Phase 2 planning (next phase)
6. `/Users/milosvasic/Projects/HelixCluster/pkg/` — Real package tree (38 packages)
7. `/Users/milosvasic/Projects/HelixCluster/internal/` — Real internal package tree (14 packages)
8. `/Users/milosvasic/Projects/HelixCluster/cmd/` — Real command binaries (17 commands)
9. `/Users/milosvasic/Projects/HelixCluster/CLAUDE.md` — CLAUDE-1 end-user usability mandate
