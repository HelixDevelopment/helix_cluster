# Helix Cluster OS — Phase 2 Roadmap: Console Nodes & Distributed Foundations

> **Research Document** | Phase 2 Planning | 2026-05-30
>
> This document defines the bridge from MVP (Phases 0–1) into Phase 2, covering console node integration (PS4/PS5 Linux agents) and the distributed systems foundations required for cluster formation.

---

## 1. Current State Summary

### Phases 0–1 Completed

| Phase | Name | Status | Key Deliverables |
|-------|------|--------|------------------|
| **0** | Foundation | ✅ Complete | 29 submodules, CI/CD scaffolding, 20-service docker-compose, 26 pkg stubs, go.work, buf proto pipeline |
| **1** | Core Infrastructure | ✅ Complete | Container orchestration (`pkg/infra`), VM testing framework, snake_case compliance, CLI skeleton (`helix_infra`) |

### Phase 2 Research Artifacts (Existing)

| Document | Location | Contents |
|----------|----------|----------|
| `HELIXCLUSTER_PHASE2_COMPLETE_REPORT.md` | `docs/research/phase_02/` | Full Phase 2 deliverables summary |
| `HELIXCLUSTER_PHASE2_CONSOLE_ARCHITECTURE.md` | `docs/research/phase_02/` | PS4/PS5 console node architecture |
| `HelixCluster_Phase_2/` | `docs/research/phase_02/` | Deep-dive research: PS4/PS5 hardware, PS3 Cell, Linux on PlayStation, GPU compute |
| `clusteros_dim02_membership_topology.md` | `docs/research/phase_02/HelixCluster_Phase_2/research/` | SWIM + gossip protocols |
| `clusteros_dim06_network_protocols.md` | `docs/research/phase_02/HelixCluster_Phase_2/research/` | WireGuard, mTLS, QUIC |

---

## 2. Phase 2 Scope & Goals

### Primary Objective

Extend Helix Cluster OS from a **single-node infrastructure prototype** into a **multi-node distributed system** with console hardware as first-class cluster citizens.

### Three Pillars

| Pillar | Description | Outcome |
|--------|-------------|---------|
| **Console Nodes** | PS4/PS5 Linux agents that join the cluster as compute donors | Heterogeneous compute pool |
| **Membership & Mesh** | SWIM gossip + WireGuard for node discovery and secure communication | Self-healing cluster topology |
| **Resource & Session Foundations** | Resource aggregation, scheduling, and session state primitives | Workload placement + user sessions |

---

## 3. Phase 2 Sub-Phases

| Sub-Phase | Name | Weeks | Tasks | Goal |
|-----------|------|-------|-------|------|
| **2.1** | Console Hardware Research | 2 | 20 | PS4/PS5 Linux compatibility, jailbreak requirements, GPU access |
| **2.2** | SWIM Protocol | 2 | 25 | Gossip-based membership, failure detection, suspicion mechanism |
| **2.3** | WireGuard Mesh | 2 | 20 | Auto-tunneling, NAT traversal, key rotation |
| **2.4** | Service Discovery | 1 | 15 | TTL-based registry, health-aware routing, etcd backend prep |
| **2.5** | Leader Election | 1 | 10 | Distributed election with TTL, failover |
| **2.6** | Resource & Scheduler Foundations | 2 | 30 | Node resource aggregation, Omega-model scheduler stub, plugin framework |
| **2.7** | Session Foundations | 2 | 25 | CRDT state, backend interface (tmux/PTY), migration stub |

**Total: ~12 weeks | ~165 tasks | ~660 person-hours**

---

## 4. Package Breakdown

### 4.1 New pkg/ Packages (Phase 2)

| Package | Purpose | Integration Point |
|---------|---------|-------------------|
| `pkg/swim` | SWIM gossip protocol: membership list, suspicion, indirect pings | `pkg/discovery`, `internal/node` |
| `pkg/wireguard` | WireGuard mesh: tunnel management, key exchange, config generation | `pkg/netutil`, `internal/node` |
| `pkg/discovery` | Service registry with pluggable backends (`InMemoryBackend` → future `EtcdBackend`) | API Gateway, Scheduler |
| `pkg/leader` | Distributed leader election with TTL and graceful failover | Control plane services |
| `pkg/resources` | Node resource aggregator: CPU, memory, GPU, storage, network readers | Scheduler, Node Agent |
| `pkg/scheduler` | Omega-model scheduler: plugin framework, filter/score pipeline, optimistic concurrency | `cmd/helix-scheduler` |
| `pkg/session` | CRDT session state, manager, migration orchestrator | `cmd/helix-session` |
| `pkg/session/backends` | Backend interface + tmux + native PTY implementations | `pkg/session` |

### 4.2 New internal/ Packages (Phase 2)

| Package | Purpose |
|---------|---------|
| `internal/node` | Node agent skeleton: heartbeat, resource probes, SWIM + WireGuard wiring |
| `internal/console` | Console node adapter: PS4/PS5-specific resource detection, GPU wrapper |

### 4.3 Console-Specific Research → Code

| Research Artifact | Code Target |
|-------------------|-------------|
| `console_dim01_ps4_hardware_jailbreak.md` | `internal/console/ps4.go` — PS4 hardware detection |
| `console_dim02_ps5_hardware_jailbreak.md` | `internal/console/ps5.go` — PS5 hardware detection |
| `console_dim03_ps3_cell_engine.md` | Deferred to Phase 5 (niche) |
| `console_dim04_linux_on_playstation.md` | `internal/console/linux_boot.go` — Boot coordination |
| `console_dim05_gpu_compute_network.md` | `internal/console/gpu_wrapper.go` — GPU resource exposure |

---

## 5. Integration Points with Phase 1

### 5.1 Infra Orchestrator → Node Agent

```
cmd/helix_infra (Phase 1) ──► internal/node (Phase 2)
pkg/infra/orchestrator.go   │   SWIM + WireGuard substrate
                            │   Resource probing
                            └── etcd registration (Phase 3)
```

### 5.2 Docker Compose → Console Nodes

```
deploy/compose/helix_infra.yml (Phase 1)
    PostgreSQL, Redis, etcd, NATS, Kafka, RabbitMQ
    Prometheus, Grafana, Jaeger, Vault
         │
         ▼
    Console nodes join as additional "compute" services
    (not containerized — bare-metal PS4/PS5 Linux)
```

### 5.3 Proto APIs → Phase 2 Services

| Proto | Phase 1 Status | Phase 2 Usage |
|-------|---------------|---------------|
| `node.proto` | Defined | Node registration, heartbeat, resource reports |
| `session.proto` | Defined | Session lifecycle, I/O streaming |
| `scheduler.proto` | Defined | Job submission, status, events |
| `health.proto` | Defined | Node health checks |

---

## 6. Priority Ordering

### P0 — Critical Path (Cluster Cannot Form Without These)

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 1 | `pkg/swim` gossip protocol | 2 weeks | Membership = prerequisite for everything |
| 2 | `pkg/wireguard` mesh | 2 weeks | Secure inter-node communication |
| 3 | `pkg/discovery` in-memory registry | 1 week | Service location without external deps |
| 4 | `pkg/leader` TTL election | 1 week | Control plane HA |
| 5 | `internal/node` agent skeleton | 1 week | Nodes must register and report |

### P1 — Essential for Phase 2 MVP

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 6 | `pkg/resources` aggregator | 1 week | Scheduler needs resource data |
| 7 | `pkg/scheduler` plugin framework | 2 weeks | Workload placement engine |
| 8 | `pkg/session` CRDT + backends | 2 weeks | User-facing session primitive |
| 9 | Console node research → stubs | 1 week | Hardware abstraction layer |

### P2 — Important but Can Be Deferred

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 10 | `pkg/session/backends` screen/zellij | 1 week | Alternative to tmux |
| 11 | Console GPU compute wrapper | 1 week | PS4/PS5 GPU as compute donors |
| 12 | CRIU/DMTCP migration research | 1 week | Live session migration (Phase 3) |

---

## 7. Risk Analysis

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| PS4/PS5 Linux boot fragility | High | Medium | Document tested firmware versions; graceful fallback to VM |
| WireGuard NAT traversal | Medium | High | Use STUN/TURN relay; UDP hole punching |
| SWIM false positives | Medium | Medium | Configurable suspicion timeout; indirect ping retries |
| Console GPU driver limitations | High | Medium | NVIDIA-first (CUDA); AMD ROCm deferred |
| Jailbreak legal/ethical concerns | Low | High | Only target owned hardware; no DRM circumvention |

---

## 8. Success Criteria (Phase 2 Exit Gates)

| KPI | Target | Measurement |
|-----|--------|-------------|
| Cluster formation time | <60s for 3-node cluster | Automated benchmark |
| SWIM detection latency | <5s for node failure | Unit test |
| WireGuard handshake | <2s | Integration test |
| Scheduling throughput | 10 decisions/sec | Benchmark |
| Session creation time | <3 seconds | End-to-end test |
| Test coverage (pkg/) | >60% line coverage | Codecov |
| Build success rate | >99% | CI pass rate |

---

## 9. Bridge to Phase 3

### Phase 3: Edge & Mobile Devices (Weeks 13–28)

| Sub-Phase | Deliverable |
|-----------|-------------|
| 3.1 | etcd integration + distributed locks |
| 3.2 | Full Omega scheduler + ClassAds |
| 3.3 | Session I/O forwarding + WebSocket |
| 3.4 | Security hardening (mTLS, SPIFFE) |
| 3.5 | Observability (Prometheus, OTel) |
| 3.6 | Edge/mobile agents (ARM64, Android, iOS) |

Phase 2 provides the **membership, mesh, and scheduling primitives** that Phase 3 extends with **persistence, security, and heterogeneous device support**.

---

## 10. References

1. `docs/research/mvp/IMPLEMENTATION_PLAN.md` — 50-week master plan
2. `docs/research/mvp/HELIX_CLUSTER_OS_COMPLETE_REPORT.md` — Architecture blueprint
3. `docs/research/phase_02/HELIXCLUSTER_PHASE2_COMPLETE_REPORT.md` — Phase 2 deliverables
4. `docs/research/phase_02/HELIXCLUSTER_PHASE2_CONSOLE_ARCHITECTURE.md` — Console architecture
5. `docs/research/phase_02/HelixCluster_Phase_2/` — Deep-dive research directory
6. `docs/research/PHASE_3_ROADMAP.md` — Phase 3 planning (next phase)
7. `api/v1/*.proto` — API definitions
8. `pkg/swim/`, `pkg/wireguard/`, `pkg/discovery/`, `pkg/leader/`, `pkg/scheduler/`, `pkg/session/` — Phase 2 packages
