# Helix Cluster OS — Phase 4 Roadmap: Virtual Testing Matrix

> **Research Document** | Phase 4 Planning | 2026-05-31
>
> This document defines Phase 4 of Helix Cluster OS: the Virtual Testing Matrix — a comprehensive deterministic testing infrastructure that simulates all eight device tiers (T1–T8) without requiring physical hardware.

---

## 1. Current State Summary

### Phases 0–3 Completed

| Phase | Name | Status | Key Deliverables |
|-------|------|--------|------------------|
| **0** | Foundation | ✅ Complete | 29 submodules, CI/CD, 20-service docker-compose, 26 pkg stubs, go.work, buf proto pipeline |
| **1** | Core Infrastructure | ✅ Complete | Container orchestration (`pkg/infra`), VM testing framework, snake_case compliance |
| **2** | Console Nodes & Distributed Foundations | ✅ Complete | SWIM gossip, WireGuard mesh, service discovery, leader election, resource aggregator, scheduler, session CRDT + backends |
| **3** | Edge & Mobile Devices | ✅ Complete | Edge/mobile research, advanced scheduling, session I/O, security hardening, observability foundations |

### Phase 4 Research Artifacts (Existing)

| Document | Location | Contents |
|----------|----------|----------|
| `HELIXCLUSTER_PHASE4_COMPLETE_REPORT.md` | `docs/research/phase_04/HelixCluster_Phase_04/` | Full Phase 4 technical report (86 sections) |
| `HELIXCLUSTER_PHASE4_TEST_ARCHITECTURE.md` | `docs/research/phase_04/HelixCluster_Phase_04/` | Virtual Testing Matrix architecture spec |
| `HelixCluster_Phase4_Virtual_Testing_Matrix.docx` | `docs/research/phase_04/` | Testing matrix reference document |
| `test_dim01_qemu_kvm_virtualization.md` | `docs/research/phase_04/HelixCluster_Phase_04/research/` | QEMU/KVM deep dive |
| `test_dim02_container_microvm.md` | `docs/research/phase_04/HelixCluster_Phase_04/research/` | Firecracker + container simulation |
| `test_dim06_cutting_edge_testing.md` | `docs/research/phase_04/HelixCluster_Phase_04/research/` | DST, chaos engineering, formal verification |
| `clusteros_dim13_aosp_builds.md` | `docs/research/phase_04/HelixCluster_Phase_04/research/` | AOSP build distribution research |

---

## 2. Phase 4 Scope & Goals

### Primary Objective

Build a **Virtual Testing Matrix** that compresses months of production exposure into hours of deterministic simulation, with perfect bug reproducibility from a single seed value.

### Five Pillars

| Pillar | Description | Outcome |
|--------|-------------|---------|
| **Device Simulation** | Firecracker microVMs (T1–T3), QEMU/KVM (T4–T6), Docker/binfmt_misc (T7–T8) | All 8 tiers simulable without physical hardware |
| **Deterministic Simulation Testing** | Rust `turmoil` + seeded PRNG + virtual time | 10:1 time compression, perfect reproducibility |
| **Chaos Engineering** | 25+ fault injection types across 4 categories | Production failure modes exercised in CI |
| **Virtual Testing Controller** | Elixir/OTP + Phoenix LiveView dashboard | Orchestration, observability, snapshot management |
| **HelixQA Integration** | Automatic challenge generation, regression detection | Quality gates on every PR |

---

## 3. Phase 4 Sub-Phases

| Sub-Phase | Name | Weeks | Tasks | Goal |
|-----------|------|-------|-------|------|
| **4.1** | K3s Foundation | 2 | 15 | K3s cluster deployment, RuntimeClasses (Firecracker, Kata, standard), golden image pipeline |
| **4.2** | Device Simulation Layer | 4 | 40 | Firecracker microVMs (28ms boot), QEMU/KVM full-system emulation, Docker cross-arch, device profile registry |
| **4.3** | DST Engine | 4 | 35 | `turmoil` integration, BUGGIFY macros, interface swapping (Net2/Sim2), workload generators |
| **4.4** | Chaos Engineering | 3 | 30 | 25+ fault types, Elixir/OTP Chaos Controller, YAML scenario definitions, auto-recovery |
| **4.5** | Virtual Testing Controller | 3 | 25 | SessionManager, DevicePool, TestRunner, SnapshotManager, Phoenix LiveView dashboard |
| **4.6** | HelixQA Integration | 3 | 25 | Invariant violation detection, statistical regression (Welch's t-test), CI/CD gates, challenge pipeline |
| **4.7** | WebAssembly Plugin System | 2 | 15 | Wasmtime Component Model, WIT interfaces, 5μs spawn, capability sandboxing |
| **4.8** | Production Hardening | 3 | 20 | Performance optimization, operator training, readiness review, documentation |

**Total: ~24 weeks | ~205 tasks | ~820 person-hours**

---

## 4. Technology Stack

### 4.1 Core Technologies

| Layer | Technology | Version | Purpose |
|-------|-----------|---------|---------|
| Orchestration | K3s | 1.29+ | Lightweight Kubernetes for test infrastructure |
| MicroVMs | Firecracker | 1.7+ | 28ms boot, 5,000+ VMs/host, T1–T3 simulation |
| Full Emulation | QEMU/KVM | 8.2+ | 15+ ISAs, T4–T6 simulation |
| Containers | Docker + Sysbox | 25.0+ | T7–T8 protocol simulation |
| DST Framework | Rust `turmoil` | 0.6+ | Deterministic simulation testing |
| Chaos Engine | Elixir/OTP + Chaos Mesh | 2.6+ | 25+ fault injection types |
| Dashboard | Phoenix LiveView | 1.0+ | Real-time test observability |
| Plugins | Wasmtime | 20.0+ | WebAssembly plugin system |
| Metrics | Prometheus + Grafana | 2.50+ / 10.4+ | Metrics collection and visualization |

### 4.2 Device Tier Mapping

| Tier | Device Class | Simulator | Boot Time | Fidelity |
|------|-------------|-----------|-----------|----------|
| T1 | Desktop PC | Firecracker | 28ms (snapshot) | High |
| T2 | Workstation | Firecracker | 28ms (snapshot) | High |
| T3 | Server | Firecracker | 125ms (cold) | High |
| T4 | Console (PS4/PS5) | QEMU/KVM | <5 min | Medium |
| T5 | Android | QEMU/KVM + Cuttlefish | <3 min | High |
| T6 | SBC (Orange Pi) | QEMU/KVM `virt` | <2 min | Medium |
| T7 | iOS | Docker protocol stubs | Instant | Low (protocol only) |
| T8 | HarmonyOS | Docker + binfmt_misc | Instant | Low (protocol only) |

---

## 5. Package Breakdown

### 5.1 New pkg/ Packages (Phase 4)

| Package | Purpose | Integration Point |
|---------|---------|-------------------|
| `pkg/testing/dst` | Deterministic simulation testing: virtual time, seeded PRNG, network simulation | All distributed packages |
| `pkg/testing/chaos` | Chaos engineering primitives: fault injection, scenario composition | CI/CD pipelines |
| `pkg/testing/device` | Device simulation abstraction: profile registry, provisioning, lifecycle | Virtual Testing Controller |
| `pkg/testing/snapshot` | Golden snapshot management: create, restore, compare, discard | Test runner |
| `pkg/wasm` | WebAssembly plugin host: Wasmtime integration, WIT bindings, capability sandbox | Plugin system |

### 5.2 New internal/ Packages (Phase 4)

| Package | Purpose | Language |
|---------|---------|----------|
| `internal/testing/controller` | Elixir-based OTP controller: SessionManager, DevicePool, TestRunner, SnapshotManager | Elixir |
| `internal/testing/dashboard` | Phoenix LiveView dashboard: real-time metrics, test control, device health | Elixir |
| `internal/testing/faults` | Fault injection implementations: network, node, time, hardware | Go + Rust |
| `internal/testing/workloads` | Workload generators: synthetic jobs, session patterns, build tasks | Go |

### 5.3 New cmd/ Binaries (Phase 4)

| Command | Priority | Purpose |
|---------|----------|---------|
| `helix-test` | P0 | CLI for running DST suites, chaos scenarios, device simulations |
| `helix-testd` | P0 | Daemon: Virtual Testing Controller OTP node |
| `helix-snapshot` | P1 | Golden snapshot management CLI |

---

## 6. Integration Points with Phase 3

### 6.1 Phase 3 Services → Test Targets

```
pkg/swim (Phase 2) ──► pkg/testing/dst ──► Deterministic SWIM validation
pkg/wireguard (Phase 2) ──► pkg/testing/dst ──► Mesh partition recovery tests
pkg/scheduler (Phase 2) ──► pkg/testing/dst ──► Scheduling correctness proofs
pkg/session (Phase 2) ──► pkg/testing/dst ──► Session migration under fault
```

### 6.2 HelixQA → Challenge Pipeline

```
Test failure ──► Invariant violation detection ──► DST seed capture
    │                                              │
    ▼                                              ▼
Performance regression ──► Welch's t-test ──► Point-valued challenge
    │                                              │
    ▼                                              ▼
CI quality gate ──► Block/allow merge ──► Challenge published to HelixQA
```

### 6.3 Device Agents → Simulation Targets

| Agent | Phase | Simulated In |
|-------|-------|-------------|
| `internal/node` | Phase 2 | Firecracker T1–T3, QEMU T4–T6 |
| `internal/console` | Phase 2 | QEMU T4 (PS4/PS5) |
| `internal/session` | Phase 2 | All tiers with I/O forwarding |
| Edge/mobile agents | Phase 3 | Docker T7–T8 protocol stubs |

---

## 7. Priority Ordering

### P0 — Critical Path (Matrix Cannot Operate Without These)

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 1 | K3s cluster deployment | 1 week | Foundation for all test infrastructure |
| 2 | Firecracker RuntimeClass + snapshots | 2 weeks | 28ms boot enables fast iteration |
| 3 | Device profile registry (YAML) | 1 week | Defines all 8 tiers for provisioning |
| 4 | Rust `turmoil` DST integration | 2 weeks | Deterministic simulation core |
| 5 | Interface swapping (Net2/Sim2) | 1 week | Same code runs in prod and sim |
| 6 | Basic chaos faults (partition, crash) | 1 week | Core failure mode coverage |
| 7 | Elixir OTP controller skeleton | 2 weeks | Orchestration backbone |

### P1 — Essential for Phase 4 MVP

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 8 | QEMU/KVM T4–T6 emulation | 2 weeks | Console and Android coverage |
| 9 | Docker T7–T8 protocol stubs | 1 week | iOS/HarmonyOS simulation |
| 10 | BUGGIFY macro system | 1 week | Forces rare-path execution |
| 11 | 25+ fault injection types | 2 weeks | Comprehensive chaos coverage |
| 12 | Phoenix LiveView dashboard | 2 weeks | Operator visibility |
| 13 | HelixQA challenge pipeline | 2 weeks | Automatic quality gating |
| 14 | WebAssembly plugin system | 2 weeks | Extensibility |

### P2 — Important but Can Be Deferred

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 15 | Golden snapshot optimization (<10ms reset) | 1 week | Faster test iteration |
| 16 | Multi-host K3s scaling | 1 week | 10,000+ device simulation |
| 17 | TLA+ formal verification integration | 2 weeks | Mathematical correctness proofs |
| 18 | Jepsen-style black-box testing | 1 week | External validation |
| 19 | Antithesis autonomous testing | 2 weeks | AI-guided test exploration |

---

## 8. Risk Analysis

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| Firecracker snapshot corruption | Low | High | Golden image validation checksums; qcow2 overlay fallback |
| QEMU T4 console emulation gaps | Medium | High | Hardware-in-the-loop for PS4/PS5-specific paths |
| iOS virtualization cost ($9,995+) | High | Medium | Docker protocol stubs for 90% of validation |
| DST time compression accuracy | Medium | High | Validate against real-time runs; calibrate virtual clocks |
| Elixir/OTP team expertise gap | Medium | Medium | Training investment; Rust alternatives for critical paths |
| CI pipeline time budget overrun | High | Medium | Parallel seed execution; smart test selection |

---

## 9. Success Criteria (Phase 4 Exit Gates)

| KPI | Target | Measurement |
|-----|--------|-------------|
| Simulated device boot time | 28ms (snapshot) | Automated benchmark |
| VMs per host | 5,000+ | Load test |
| DST time compression | 10:1 minimum | Benchmark vs real time |
| Fault injection types | 25+ implemented | Checklist verification |
| Test reproducibility | 100% from seed | Seed replay validation |
| CI pipeline integration | <30 min per PR | CI timing |
| Plugin spawn latency | <5μs | Microbenchmark |
| Test coverage (pkg/) | >80% line coverage | Codecov |

---

## 10. Bridge to Phase 5

### Phase 5: Intelligence & Scale (Weeks 25–40)

| Sub-Phase | Deliverable |
|-----------|-------------|
| 5.1 | LLM Brain advisory system (RAG + Constitutional AI) |
| 5.2 | LLMsVerifier integration |
| 5.3 | LSTM failure prediction (Python isolated process) |
| 5.4 | Reinforcement learning feedback loop |
| 5.5 | Multi-region cluster federation |
| 5.6 | Console node integration (PS4/PS5 Linux agents) |
| 5.7 | Edge/mobile node integration (ARM64, Android, iOS donors) |

Phase 4 provides the **testing infrastructure** that Phase 5 uses to validate AI-driven optimizations, failure prediction models, and multi-region federation scenarios at scale.

---

## 11. References

1. `docs/research/phase_04/HelixCluster_Phase_04/HELIXCLUSTER_PHASE4_COMPLETE_REPORT.md` — Full technical report (86 sections)
2. `docs/research/phase_04/HelixCluster_Phase_04/HELIXCLUSTER_PHASE4_TEST_ARCHITECTURE.md` — Architecture specification
3. `docs/research/phase_04/HelixCluster_Phase_04/research/test_dim01_qemu_kvm_virtualization.md` — QEMU/KVM deep dive
4. `docs/research/phase_04/HelixCluster_Phase_04/research/test_dim02_container_microvm.md` — Firecracker + containers
5. `docs/research/phase_04/HelixCluster_Phase_04/research/test_dim06_cutting_edge_testing.md` — DST + chaos engineering
6. `docs/research/phase_04/HelixCluster_Phase_04/research/clusteros_dim13_aosp_builds.md` — AOSP build distribution
7. `docs/research/PHASE_3_ROADMAP.md` — Previous phase roadmap
8. `docs/research/PHASE_2_ROADMAP.md` — Phase 2 roadmap
9. `pkg/swim/`, `pkg/wireguard/`, `pkg/scheduler/`, `pkg/session/` — Test target packages
