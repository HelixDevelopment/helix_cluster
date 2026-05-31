# Helix Cluster OS — Phase 7 Roadmap: Industry Benchmarking & Hardening

> **Research Document** | Phase 7 Planning | 2026-05-31
>
> This document defines the bridge from multi-cluster federation (Phase 6) into Phase 7, which applies lessons from 15+ industry-leading clustering systems to close 23 identified architectural gaps and deliver a production-hardened HelixCluster control plane.

---

## 1. Current State Summary

### Phases 0–6 Completed

| Phase | Name | Status | Key Deliverables |
|-------|------|--------|------------------|
| **0** | Foundation | Complete | 29 submodules, CI/CD scaffolding, 20-service docker-compose, 26 pkg stubs, go.work, buf proto pipeline |
| **1** | Core Infrastructure | Complete | Container orchestration (`pkg/infra`), VM testing framework, CLI skeleton (`helix_infra`) |
| **2** | Console Nodes | Complete | SWIM gossip (`pkg/swim`), WireGuard mesh (`pkg/wireguard`), PS4/PS5 Linux agents, Omega scheduler stub |
| **3** | Edge & Mobile | Complete | ARM64/Android/iOS edge agents, etcd integration (`pkg/etcd`), distributed locks (`pkg/lock`), WebSocket I/O |
| **4** | Virtual Testing | Complete | VM testing matrix, chaos harness, ClassAds (`pkg/classads`), integration test infrastructure |
| **5** | Advanced Devices | Complete | FPGA/NPU/exotic device support, `internal/gpu`, advanced scheduler plugins |
| **6** | Multi-Cluster Federation | Complete | Federation layer, cross-cluster routing, `internal/gateway`, WAN topology |

### Phase 7 Research Artifacts

| Document | Location | Contents |
|----------|----------|----------|
| `HELIXCLUSTER_PHASE7_COMPLETE_REPORT.converted.md` | `docs/research/phase_07/HelixCluster_Phase_07/` | 40,000-word complete report; 23 gaps, 25 improvements, 122 code blocks |
| `HELIXCLUSTER_PHASE7_HARDENING_ARCHITECTURE.md` | `docs/research/phase_07/HelixCluster_Phase_07/` | Hardened architecture diagram |
| `plan_phase7.md` | `docs/research/phase_07/HelixCluster_Phase_07/` | 8-dimension research plan; Kubernetes, databases, messaging, consensus, caching, enterprise, HPC, testing |
| `helixcluster_phase7_sec09.md` | `docs/research/phase_07/HelixCluster_Phase_07/` | Master 23-gap matrix with P0–P3 priority rankings |
| `helixcluster_phase7_sec12.md` | `docs/research/phase_07/HelixCluster_Phase_07/` | 24-week implementation roadmap; 4 sub-phases; weekly milestones |

---

## 2. Phase 7 Scope & Goals

### Primary Objective

Benchmark HelixCluster against the world's most sophisticated clustering systems, extract every production-proven lesson, close all identified architectural gaps, and deliver a system that survives heterogeneous hardware, network partitions, Byzantine edge devices, and the relentless entropy of production distributed systems — with a <100K LOC control plane deployable by a single engineer.

### Four Pillars

| Pillar | Description | Industry Baseline |
|--------|-------------|-------------------|
| **Industry Benchmarking** | Systematic analysis of 15+ production systems across 8 dimensions | K8s, CockroachDB, SLURM, Redis Cluster, Oracle RAC, FoundationDB, Kafka, NATS, Consul, Cassandra, Nomad, BOINC, Pacemaker, vSphere, Netflix |
| **Security Hardening** | STONITH fencing, BOINC-style trust model for semi-trusted hardware, voting quorum for split-brain prevention, admission control | Oracle RAC voting disk, Pacemaker STONITH, BOINC quorum validation |
| **Performance & SLO** | 50K writes/sec/cell (Multi-Raft), 90%+ cluster utilization (backfill), <30s session failover (ASM), sub-5ms p99 reads (MVCC) | SLURM 90%+ utilization, Redis Cluster <30s failover, CockroachDB parallel commit |
| **Testing & Compliance** | Deterministic simulation (1K sim passes/commit), Porcupine linearizability, nightly chaos pipeline, TLA+ formal verification, <100K LOC budget | FoundationDB 1T CPU-hour DST, etcd Porcupine, CockroachDB roachtest, Netflix Chaos Monkey |

---

## 3. Sub-Phases

| Sub-Phase | Name | Weeks | Tasks | Goal |
|-----------|------|-------|-------|------|
| **7a** | Data Layer Hardening | 6 | 30 | Multi-Raft consensus, MVCC store, CRDT delta-sync, Cassandra 3-layer repair |
| **7b** | Scheduling & Session Hardening | 6 | 35 | SLURM backfill, device plugin framework, GRES, gang scheduling, hash-slot router, ASM |
| **7c** | Federation Hardening | 6 | 28 | Voting quorum, STONITH fencing, constraint engine, SCAN discovery, admission control |
| **7d** | Testing & Production Hardening | 6 | 27 | DST framework (Turmoil), BUGGIFY macros, Porcupine linearizability, nightly chaos, TLA+ |
| **Total** | | **24** | **120** | **23 gaps closed; production-ready control plane** |

---

## 4. Package Breakdown

### 4.1 New pkg/ Packages (Phase 7, Planned)

| Package | Purpose | Closes Gap | Integration Point |
|---------|---------|------------|-------------------|
| `pkg/multiraft` *(planned)* | Multi-Raft manager with per-shard Raft groups and heartbeat coalescing | G-01 | `pkg/etcd`, `internal/scheduler` |
| `pkg/mvcc` *(planned)* | Revision-tracked KV store with B-tree index; time-travel queries; persistent watch streams | G-08 | `pkg/etcd`, control plane services |
| `pkg/crdt` *(planned)* | LWW register, G-counter, PN-counter, OR-set, LWW map; delta-state 5s sync cycle | G-01 partial | `pkg/pubsub`, federation layer |
| `pkg/backfill` *(planned)* | SLURM-style backfill scheduler with resource availability timeline | G-02 | `pkg/scheduler` |
| `pkg/deviceplugin` *(planned)* | Extensible GPU/FPGA/NPU fingerprinting framework; GRES-style descriptors | G-11, G-13, G-17 | `pkg/scheduler`, `internal/gpu` |
| `pkg/hashslot` *(planned)* | 16,384-slot CRC16 hash router with MOVED/ASK; Atomic Slot Migration | G-03 | `pkg/session`, `internal/gateway` |
| `pkg/stonith` *(planned)* | Pluggable STONITH fencing agents (IPMI, AWS EC2, Azure ARM, SBD) | G-19 | `internal/advisory`, federation |
| `pkg/constraint` *(planned)* | Pacemaker-inspired four-type constraint engine (location, colocation, ordering, stickiness) | G-20 | `pkg/scheduler`, `internal/policy` |
| `pkg/voting` *(planned)* | Oracle RAC largest-subcluster-wins voting quorum; vote store with 3s TTL | G-19 | federation, `pkg/leader` |
| `pkg/scan` *(planned)* | SCAN-style stable virtual IP/DNS abstraction; listener proxy pool | G-21 | `internal/gateway` |
| `pkg/dst` *(planned)* | Turmoil-based deterministic simulation testing framework; BUGGIFY macros | G-14, G-15 | test harness |
| `pkg/porcupine` *(planned)* | Operation history recorder + Porcupine linearizability checker integration | G-16 | CI pipeline |
| `pkg/gangscheduler` *(planned)* | All-or-nothing GPU reservation; topology-aware NUMA/NVLink placement scoring | G-10, G-18 | `pkg/scheduler`, `internal/gpu` |
| `pkg/admissioncontrol` *(planned)* | vSphere HA-style failover capacity reservation before workload accept | G-22 | `internal/gateway`, scheduler |
| `pkg/repair` *(planned)* | Cassandra 3-layer repair: hinted handoff, read repair, anti-entropy Merkle | G-01 partial | `pkg/mvcc`, storage layer |

### 4.2 New internal/ Packages (Phase 7, Planned)

| Package | Purpose | Closes Gap |
|---------|---------|------------|
| `internal/advisory` *(planned)* | Trust scoring for semi-trusted console/edge hardware; BOINC-style redundant execution | G-09 |
| `internal/chaos` *(planned)* | Nightly chaos pipeline: GitHub Actions + Chaos Mesh; pod kill, partition, disk stall, clock skew | G-15 partial |

### 4.3 Existing Packages Extended

| Existing Package | Phase 7 Extension |
|------------------|-------------------|
| `pkg/scheduler` | Backfill plugin, gang scheduler, multifactor priority, fair-share tree |
| `pkg/session` | Hash-slot routing, ASM, PFAIL-to-FAIL failure detection |
| `pkg/swim` | Two-phase PFAIL→FAIL consensus (G-23); 77K-client WAN gossip patterns |
| `pkg/health` | Three-tier probes: liveness / readiness / startup with GPU grace periods (G-04) |
| `pkg/etcd` | MVCC B-tree backend (bbolt); per-cell sharding |
| `pkg/security` | APF FlowSchema→PriorityLevel→Queue (G-07) |
| `internal/node` | Informer-cache Watcher (`helixcache.Watcher`), rate-limited work queue (G-05, G-06) |

---

## 5. Integration Points with Phase 6

### 5.1 Federation Layer → Phase 7 Hardening

```
internal/gateway (Phase 6)              pkg/voting + pkg/stonith (Phase 7)
  Multi-cluster routing       ──────►   Voting quorum resolves partitions
  WAN topology                          STONITH fences evicted nodes
  Cross-cluster sessions      ──────►   pkg/hashslot + pkg/scan
                                        Hash-slot routing + SCAN VIPs
```

### 5.2 Existing Scheduler → Backfill + Gang Scheduler

```
pkg/scheduler (Phase 1–6 FIFO)          pkg/backfill + pkg/gangscheduler (Phase 7)
  Plugin framework (Omega model) ─────► Backfill fills utilization gaps (90%+)
  Resource aggregation                  Gang scheduling: all-or-nothing GPU alloc
  ClassAds matching             ─────► pkg/deviceplugin: GRES descriptors
```

### 5.3 etcd Single-Write Path → Multi-Raft

```
pkg/etcd (Phases 1-6 single cluster)    pkg/multiraft + pkg/mvcc (Phase 7)
  Single Raft leader bottleneck ──────► Per-shard Raft groups (horizontal scale)
  Simple KV                             MVCC revision tracking + time-travel queries
  Polling watchers              ──────► Persistent gRPC watch streams (no polling)
```

---

## 6. Priority Ordering

### P0 — Production Deployment Blocked Without These (7 items)

| # | Task | Package | Industry Source |
|---|------|---------|----------------|
| 1 | Multi-Raft per-shard consensus + heartbeat coalescing | `pkg/multiraft` | CockroachDB |
| 2 | SLURM-style backfill scheduler (90%+ utilization target) | `pkg/backfill` | SLURM |
| 3 | DST framework (Turmoil) + 1,000 sim passes per commit | `pkg/dst` | FoundationDB |
| 4 | BUGGIFY macros on all timeouts, caches, retry limits | `pkg/dst` | FoundationDB |
| 5 | Voting quorum (largest-subcluster-wins) for split-brain | `pkg/voting` | Oracle RAC |
| 6 | MVCC revision store + B-tree index + watch streams | `pkg/mvcc` | etcd v3 |
| 7 | Hash-slot router (CRC16, MOVED/ASK, Atomic Slot Migration) | `pkg/hashslot` | Redis Cluster |

### P1 — Significant Operational Risk Without These (8 items)

| # | Task | Package | Industry Source |
|---|------|---------|----------------|
| 8 | STONITH fencing framework (IPMI + cloud + SBD agents) | `pkg/stonith` | Pacemaker |
| 9 | Device plugin framework (GPU/FPGA/NPU GRES descriptors) | `pkg/deviceplugin` | Nomad, K8s |
| 10 | Three-tier health probes (liveness/readiness/startup) | `pkg/health` | Kubernetes |
| 11 | Porcupine linearizability checking in CI | `pkg/porcupine` | etcd |
| 12 | BOINC-style trust model for semi-trusted hardware | `internal/advisory` | BOINC |
| 13 | Failover admission control (reserve capacity pre-accept) | `pkg/admissioncontrol` | vSphere HA |
| 14 | Gang scheduling + topology-aware NUMA/NVLink placement | `pkg/gangscheduler` | SLURM, K8s |
| 15 | Cassandra 3-layer repair (hinted handoff, read repair, Merkle anti-entropy) | `pkg/repair` | Cassandra |

### P2 — Important for Differentiation (6 items)

| # | Task | Package | Industry Source |
|---|------|---------|----------------|
| 16 | Pacemaker 4-type constraint engine | `pkg/constraint` | Pacemaker |
| 17 | SCAN stable virtual IP/DNS discovery | `pkg/scan` | Oracle RAC |
| 18 | CRDT delta-state cross-cell sync (LWW, G-counter, OR-set) | `pkg/crdt` | Automerge |
| 19 | APF FlowSchema → PriorityLevel → Queue | `pkg/security` | Kubernetes APF |
| 20 | Informer cache (`helixcache.Watcher`) + rate-limited queues | `internal/node` | Kubernetes |
| 21 | TLA+ formal specs (Raft safety, session migration state machine) | test infra | AWS, industry |

---

## 7. Risk Analysis

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| MVCC LSM-tree build exceeds budget | High | High | Use bbolt (etcd's proven backend) for Phase 7a; schedule custom LSM to Phase 8 |
| DST framework scope creep | Medium | High | Build on Turmoil (15M downloads) — do not build from scratch |
| STONITH hardware dependency | Medium | Medium | Shared-disk SBD as universal fallback; STONITH optional with clear warnings |
| Topology data absent for GPU scheduling | High | Medium | Graceful degradation to bin-packing when topology data missing |
| K8s Complexity Trap (feature accumulation) | Medium | Critical | Hard 100K LOC budget enforced via CI; no feature without gap closure justification |
| etcd wall resurfaces under load | Low | High | Per-cell Multi-Raft is the structural fix; benchmark confirms horizontal scale |
| STONITH misconfiguration causes data loss | Low | Critical | Mandatory integration tests gating STONITH enablement; SBD as safe default |
| Nightly chaos pipeline false positives | Medium | Low | Canary safeguards; automated abort conditions; 1% production traffic cap |

---

## 8. Success Criteria / Phase 7 Exit Gates

### Performance KPIs

| KPI | Target | Measurement |
|-----|--------|-------------|
| Multi-Raft write throughput | 10,000 writes/sec per shard; 50,000/sec per cell | DST benchmark suite |
| MVCC p99 read latency | <5ms under load | Automated benchmark |
| SLURM-style backfill utilization | ≥90% on synthetic workloads | Scheduler benchmark |
| Session failover via ASM | <10s for slot migration; <30s full failover | End-to-end test |
| PFAIL-to-FAIL detection | <30s; requires majority consensus | Integration test |
| 3-layer repair convergence | Full anti-entropy within repair window | Chaos + recovery test |
| Cluster formation (3-node) | <60s | Automated benchmark |

### Testing & Correctness KPIs

| KPI | Target | Measurement |
|-----|--------|-------------|
| DST simulation passes | 1,000 per commit | CI gate — build fails below threshold |
| Porcupine linearizability | Zero violations on every CI run | CI gate — any violation aborts pipeline |
| BUGGIFY coverage | All timeouts, cache sizes, retry limits instrumented | Code coverage audit |
| Nightly chaos scenarios | Pod kill, network partition, disk stall, clock skew passing | GitHub Actions nightly |
| TLA+ model checking | Raft safety + session migration exhaustively verified (3–5 nodes) | TLC model checker |
| Test coverage (new pkg/) | >70% line coverage | Codecov |

### Architectural Constraints

| Constraint | Target | Rationale |
|------------|--------|-----------|
| Control plane binary size | <100 MB | Fits on smart TV, PS4, edge devices |
| Control plane LOC | <100K | One engineer understands it in one week; K8s is 2M+ LOC |
| Gaps closed | All 23 identified gaps | Full Phase 7 gap matrix closure |

### CLAUDE-1 End-User Usability Gate (§7.1 Enforcement)

All Phase 7 features are subject to the following non-negotiable gates before declaring completion:

| Gate | Requirement | Evidence Required |
|------|-------------|-------------------|
| Unit tests | Paired mutation tests per §1.1 | Coverage report + mutation score |
| Integration tests | Real services (etcd, Redis, NATS, Kafka) — no mock-only validation | CI logs with live service containers |
| End-to-end tests | Exercise feature as end user: session failover, scheduler dispatch, federation partition | E2E test suite pass; log capture |
| HelixQA Challenge validation | Each hardened subsystem passes a Challenge | Challenge PASS artifacts |
| Sink-side evidence | Screenshot, metrics capture, or log proving end-user-visible operation | Attached evidence per feature |
| Benchmark reproducibility | Any performance claim reproducible by any engineer with `make bench` | Benchmark script + baseline CSV committed |

> A DST pass on non-functional code is a **PASS-bluff** — equivalent severity to §7.1 violation. Every simulation must exercise real code paths, not mocked stubs.

---

## 9. Bridge to Phase 8 (Chutes AI Integration)

Phase 7 delivers the hardened substrate on which Phase 8 can safely build AI-native workloads. The specific handoffs are:

| Phase 7 Deliverable | Phase 8 Dependency |
|--------------------|-------------------|
| `pkg/backfill` + `pkg/gangscheduler` | GPU gang allocation for Chutes AI inference jobs |
| `pkg/deviceplugin` (GRES descriptors) | NPU/TPU/custom accelerator discovery for AI serving |
| `pkg/hashslot` + `pkg/scan` | Stable routing for stateful AI agent sessions |
| `pkg/dst` + `pkg/porcupine` | Test harness extended to cover AI workload correctness |
| `pkg/multiraft` horizontal writes | High-throughput model parameter sync across cells |
| `pkg/crdt` delta-state sync | Eventually-consistent AI metadata (model registry, experiment state) |
| `pkg/admissioncontrol` | GPU capacity reservation for Chutes inference SLOs |
| `internal/advisory` trust model | Trust scoring for edge AI inference nodes |

Phase 8 Theme: **Chutes AI Integration** — AI-native scheduling, model serving lifecycle, inference SLOs, and agent orchestration on the hardened HelixCluster substrate.

---

## 10. References

1. `docs/research/phase_07/HelixCluster_Phase_07/HELIXCLUSTER_PHASE7_COMPLETE_REPORT.converted.md` — Full 40,000-word Phase 7 report; 23 gaps, 25 improvements, 122 code blocks
2. `docs/research/phase_07/HelixCluster_Phase_07/plan_phase7.md` — 8-dimension research plan
3. `docs/research/phase_07/HelixCluster_Phase_07/helixcluster_phase7_sec09.md` — Master gap matrix (Table 1)
4. `docs/research/phase_07/HelixCluster_Phase_07/helixcluster_phase7_sec12.md` — 24-week implementation schedule (Tables 1 & 2)
5. `docs/research/phase_07/HelixCluster_Phase_07/HELIXCLUSTER_PHASE7_HARDENING_ARCHITECTURE.md` — Hardened architecture diagrams
6. `docs/research/PHASE_6_ROADMAP.md` — Phase 6 planning (multi-cluster federation)
7. `docs/research/mvp/IMPLEMENTATION_PLAN.md` — 50-week master plan
8. `pkg/scheduler/`, `pkg/swim/`, `pkg/etcd/`, `pkg/session/`, `pkg/security/` — Existing packages extended in Phase 7
9. Kubernetes source: `github.com/kubernetes/kubernetes` — Informer cache, APF, scheduler framework
10. CockroachDB: Multi-Raft, parallel commit, roachtest — consensus and testing patterns
11. FoundationDB DST: deterministic simulation, BUGGIFY — testing culture foundation
12. SLURM backfill scheduler — 90%+ utilization pattern; GRES resource descriptions
13. Redis Cluster: 16,384 hash slots, ASM, PFAIL/FAIL — session routing and failure detection
14. Oracle RAC: voting quorum, SCAN — split-brain resolution and stable endpoints
15. Pacemaker/Corosync: STONITH, constraint engine — enterprise-grade fencing and placement
